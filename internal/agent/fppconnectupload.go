package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// This file is FC2's HTTP surface: the chunked upload route
// (PATCH/POST /api/file/{dir}) and the playlist bind route
// (POST /api/playlist/{name}; its GET half lives in fppconnecthttp.go,
// FC1b's own file, since it predates this seam). Every byte-moving
// decision (staging, gap detection, the byte caps, ENOSPC, hashing,
// rename, binding resolution) lives in fppconnectheld.go's
// fppConnectHeldStore; this file is HTTP framing around it: parse
// headers, bound the body, translate fppConnectChunkOutcome into a status
// code, and never write to the filesystem directly.

const (
	// fppConnectMaxChunkBytes bounds one PATCH request body: headroom
	// over xLights' own 16 MiB chunk size (RES-003 section 9.4), not the
	// operator-configured per-file cap, which is enforced separately (and
	// per-upload, not per-chunk) by fppConnectHeldStore.WriteChunk.
	fppConnectMaxChunkBytes = 32 * 1024 * 1024

	// fppConnectMaxPlaylistBodyBytes bounds a POST /api/playlist/{name}
	// body: ample for even a large playlist's worth of entries, far below
	// a chunked FSEQ upload, and unrelated to fppConnectMaxBodyBytes's
	// 4 KiB cap on the four read-only discovery routes.
	fppConnectMaxPlaylistBodyBytes = 1 * 1024 * 1024
)

// handleFileRoute is PATCH/POST /api/file/{dir}'s method dispatch. dir is
// route()'s decoded, NOT YET VALIDATED remainder of the path (see
// fppConnectMatchFilePath's doc comment on why validation happens here
// rather than at the routing layer): the directory allowlist check below
// is what turns "effects", "../sequences", an empty segment, or a name
// containing "/" into a 403 naming the reason, with nothing written
// anywhere (ADR-044 decision 4's first bound). Any method other than
// PATCH or POST is this listener's ordinary 404, checked first so a wrong
// method never leaks whether dir happened to be valid.
func (s *fppConnectServer) handleFileRoute(w http.ResponseWriter, r *http.Request, dir string) {
	fppConnectSetReadDeadline(w, fppConnectFileReadDeadline, s.logger)
	switch r.Method {
	case http.MethodPatch, http.MethodPost:
	default:
		// Safe to set the write deadline immediately: nothing above this
		// check has read any part of the request (review round 4 finding
		// 3).
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		http.NotFound(w, r)
		return
	}

	if !fppConnectAllowedDirs[dir] {
		reason := fmt.Sprintf("directory %q is not accepted; only sequences, music, and videos are", dir)
		s.held.RecordRefusal("bad-dir", dir, "", reason, s.now())
		// Safe to set the write deadline immediately before this
		// response: nothing above read any of the body, so there is
		// nothing this deadline's window could overlap (review round 3
		// finding 1).
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusForbidden, reason)
		return
	}

	if r.Method == http.MethodPost {
		// RES-003 section 9.4: xLights never sends this documented
		// initiating POST, and FPP's own handler treats it as a no-op
		// that returns an opaque id whose value is never inspected.
		// Accepted the same way, for the same reason: a server that
		// insisted on this call would break against the real client, and
		// one that rejects it breaks nothing that client sends but could
		// break some other FPP Connect tool that does.
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteJSON(w, http.StatusOK, map[string]string{"id": uuid.NewString()})
		return
	}

	s.handleFilePatch(w, r, dir)
}

// handleFilePatch assembles one chunk of a chunked upload (RES-003 section
// 9.4): parse and validate the required headers, bound the chunk body,
// then hand the raw bytes to fppConnectHeldStore.WriteChunk, which owns
// every filesystem and byte-cap decision. dir is already known allowed by
// the caller.
//
// Upload-Offset and Upload-Length are checked for bare presence here (a
// missing one really is a different, genuinely malformed request, 400).
// Upload-Name is NOT checked for bare presence before validation (review
// round 3 finding 6): an empty Upload-Name used to fall into that same
// 400 branch and record no evidence, when it should take exactly the
// same 403 bad-name path any other unsafe name does, since
// fppConnectValidPlaylistName already refuses "" on its own (its first
// check): an absent header and a present-but-empty one are
// indistinguishable to r.Header.Get either way, and both are a name
// problem, not a missing-header problem.
func (s *fppConnectServer) handleFilePatch(w http.ResponseWriter, r *http.Request, dir string) {
	offsetHeader := r.Header.Get("Upload-Offset")
	lengthHeader := r.Header.Get("Upload-Length")
	if offsetHeader == "" || lengthHeader == "" {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusBadRequest, "Upload-Offset and Upload-Length headers are both required")
		return
	}

	// fppConnectValidPlaylistName (fppconnecthttp.go) is reused here
	// deliberately, not reimplemented: both this header and the fourth
	// route's {name} are "a string this listener writes near the
	// filesystem," and both need exactly the same safety checks (empty,
	// no path separator, no NUL, no ".." segment, no "." or ".." once
	// cleaned). Upload-Name never escaping its directory is ADR-044
	// decision 4's second bound.
	name := r.Header.Get("Upload-Name")
	if !fppConnectValidPlaylistName(name) {
		reason := fmt.Sprintf("upload name %q is not a safe bare file name", name)
		s.held.RecordRefusal("bad-name", dir, name, reason, s.now())
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusForbidden, reason)
		return
	}

	offset, errOffset := strconv.ParseInt(offsetHeader, 10, 64)
	length, errLength := strconv.ParseInt(lengthHeader, 10, 64)
	if errOffset != nil || offset < 0 || errLength != nil || length < 0 {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusBadRequest, "Upload-Offset and Upload-Length must both be non-negative integers")
		return
	}

	if r.ContentLength < 0 || r.ContentLength > fppConnectMaxChunkBytes {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusBadRequest, fmt.Sprintf(
			"chunk body must have a known length within this listener's %d byte chunk ceiling", fppConnectMaxChunkBytes))
		return
	}

	// The chunk body streams straight into WriteChunk (review round 1
	// finding 3): it is read directly off r.Body, at most r.ContentLength
	// bytes, and never buffered whole in this handler's own memory the
	// way io.ReadAll would (a real 16 MiB xLights chunk reallocating as
	// it grows, times however many uploads are in flight).
	outcome, reason, _ := s.held.WriteChunk(dir, name, offset, length, r.Body, r.ContentLength, s.view.MaxFileBytes(), s.view.MaxAssetDirBytes(), s.now(), s.view.ActiveShow, s.view.ShowID, s.view.ShowNames)

	// Only now, after WriteChunk has fully finished reading the request
	// body, is it safe to bound the response write (review round 3
	// finding 1): setting this any earlier could have its deadline expire
	// while the chunk copy above was still in flight, on the very
	// connection the client is waiting on for its response.
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)

	switch outcome {
	case fppConnectChunkAccepted, fppConnectChunkCompleted:
		fppConnectWriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case fppConnectChunkGap:
		fppConnectWriteErr(w, http.StatusConflict, reason)
	case fppConnectChunkTooLarge:
		fppConnectWriteErr(w, http.StatusRequestEntityTooLarge, reason)
	case fppConnectChunkDirFull, fppConnectChunkDiskFull:
		fppConnectWriteErr(w, http.StatusInsufficientStorage, reason)
	default: // fppConnectChunkWriteFailed
		fppConnectWriteErr(w, http.StatusInternalServerError, reason)
	}
}

// fppConnectPlaylistPostEntry is one mainPlaylist element as posted, in
// either of RES-003 section 10.6's two documented shapes. Every other
// field FPP's own object carries (type, enabled, playOnce, duration,
// videoOut, random, playlistInfo, ...) is deliberately not modeled: the
// seam spec is explicit that sequenceName and mediaName are the only
// fields this route reads.
type fppConnectPlaylistPostEntry struct {
	SequenceName string `json:"sequenceName"`
	MediaName    string `json:"mediaName"`
}

// fppConnectPlaylistPostBody is POST /api/playlist/{name}'s body: FPP's
// playlist object, of which only mainPlaylist is read here.
type fppConnectPlaylistPostBody struct {
	MainPlaylist []fppConnectPlaylistPostEntry `json:"mainPlaylist"`
}

// fppConnectExtractFileNames collects every non-empty, distinct
// sequenceName and mediaName across body's mainPlaylist, in first-seen
// order: the join key set (ADR-044 decision 8) this POST is asking the
// node to bind, or to remember as pending.
func fppConnectExtractFileNames(body fppConnectPlaylistPostBody) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, entry := range body.MainPlaylist {
		add(entry.SequenceName)
		add(entry.MediaName)
	}
	return out
}

// fppConnectCountShowMatches counts how many times want occurs in names:
// 0 means unknown, 1 means resolved, more than 1 means ambiguous (ADR-044
// decision 8), two shows sharing one display name, which is the only way
// a plain name list (no id) can represent that.
func fppConnectCountShowMatches(names []string, want string) int {
	n := 0
	for _, name := range names {
		if name == want {
			n++
		}
	}
	return n
}

// handlePlaylistPost is POST /api/playlist/{name} (ADR-044 decision 8):
// bind every held file named in the body's mainPlaylist to name, when name
// resolves to exactly one show; otherwise record the unknown or ambiguous
// case as evidence and bind nothing. Always answers 200 regardless of
// outcome: RES-003 section 10.6 records that xLights never inspects this
// call's status, fires it up to twice per target (the first time before
// any file exists), and this route must be idempotent under that, which
// fppConnectHeldStore.BindShow and its pending-map fallback provide. name
// is route()-matched and NOT YET VALIDATED for filesystem safety;
// fppConnectValidPlaylistName here is the same check GET's handlePlaylist
// applies, refusing a path-shaped name with the same 404 an unknown name
// would otherwise reach only after a membership check.
//
// The write deadline is set immediately before each response below, never
// up front (review round 4 finding 4): this handler reads its own body
// (up to fppConnectMaxPlaylistBodyBytes), so setting it any earlier would
// be exactly the "before a body read that might still be in flight"
// ordering fppConnectSetWriteDeadline's own doc comment forbids, even
// though that read is itself already bounded by handlePlaylistRoute's
// read deadline.
func (s *fppConnectServer) handlePlaylistPost(w http.ResponseWriter, r *http.Request, name string) {
	if !fppConnectValidPlaylistName(name) {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		http.NotFound(w, r)
		return
	}

	if r.ContentLength < 0 || r.ContentLength > fppConnectMaxPlaylistBodyBytes {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusBadRequest, fmt.Sprintf(
			"playlist body must have a known length within %d bytes", fppConnectMaxPlaylistBodyBytes))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, fppConnectMaxPlaylistBodyBytes+1))
	if err != nil || int64(len(raw)) > fppConnectMaxPlaylistBodyBytes {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		fppConnectWriteErr(w, http.StatusBadRequest, "failed to read the playlist body")
		return
	}

	// A malformed body is tolerated, not refused: xLights never inspects
	// this response's status (RES-003 section 10.6), so there is no
	// signal a refusal could ever reach it through, and treating it as an
	// empty playlist (no entries to bind) is the same "keep everything,
	// guess nothing" posture ADR-044 decision 9 takes toward a partial
	// upload.
	var payload fppConnectPlaylistPostBody
	_ = json.Unmarshal(raw, &payload)

	fileNames := fppConnectExtractFileNames(payload)
	now := s.now()

	switch matches := fppConnectCountShowMatches(s.view.ShowNames(), name); {
	case matches == 0:
		s.held.RecordUnknownPlaylist(name, fileNames, now)
	case matches > 1:
		s.held.RecordAmbiguousPlaylist(name, matches, fileNames, now)
	default:
		showID, ok := s.view.ShowID(name)
		if !ok {
			// matches == 1 against ShowNames() but ShowID cannot resolve
			// it: a node snapshot, or the coordinator push that reached
			// it, predates the additive "shows" id/name list (review
			// round 2 finding D). Distinct from genuine ambiguity (two
			// shows sharing this name): reporting this as "ambiguous"
			// with a matchCount of 1 would misname a temporary
			// propagation gap as a naming collision an operator would
			// have to fix by hand. Held unbound with its own reason
			// instead; RebindPendingShowIDs resolves it automatically the
			// next time any push carries shows.
			s.held.RecordShowIDNotPushed(name, fileNames, now)
			s.held.BindPendingShowID(name, fileNames, now)
			break
		}
		s.held.BindShow(name, showID, fileNames, now)
	}

	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
	fppConnectWriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// fppConnectWriteErr writes a plain-text refusal body, matching this
// listener's package-wide "never HTML" rule: http.Error sets
// Content-Type: text/plain itself.
func fppConnectWriteErr(w http.ResponseWriter, status int, reason string) {
	http.Error(w, reason, status)
}
