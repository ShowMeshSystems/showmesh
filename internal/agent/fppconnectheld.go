package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file is FC2's held-file store (Track E phase 2, ADR-044 decisions 8
// and 9): the node's memory of every file a chunked xLights upload has
// completed, its content hash, and its show binding, persisted so a
// restart does not lose it. fppconnectupload.go's HTTP handlers are its
// only writer; fppconnecthttp.go's GET /api/playlist/{name} handler,
// internal/agent/renderreport.go (the only place ADR-044 decision 8's
// unbound-held-file evidence reaches an operator), and FC3's registration
// seam (via SetOnHeld/Held) are its readers.
//
// Layout under <AssetDir>/fppConnectUploadStateSubdir:
//
//	staging/<dir>/<Upload-Name>.partial  in-progress bytes
//	held/<dir>/<Upload-Name>             assembled, hashed bytes
//	index.json                           every held record, every pending
//	                                      binding, and the bounded evidence
//	                                      log
//
// staging/ is swept at boot (sweepFPPConnectUploadStaging, mirroring
// assets.go's sweepAssetStaging): a partial file left behind by an
// interrupted process is never a partially-usable asset. held/ and
// index.json are never touched by that sweep.
//
// An in-progress upload's offset, running content hash, and asset-
// directory-cap reservation live only in memory (s.inFlight), not on disk:
// a restart already discards every partial file at boot regardless (the
// sweep above), so persisting that bookkeeping to disk would only add I/O
// per chunk for state a restart throws away anyway. Review round 1 on this
// seam's PR flagged the earlier on-disk sidecar as exactly that: an unused
// durability guarantee paid for on every chunk.

// fppConnectUploadStateSubdir roots FC2's own durable state under the
// agent's asset directory, matching internal/agent/heldcatalog.stateSubdir
// and fppConnectStateSubdir's identical convention of no second configured
// directory. Distinct from both of those, and from assets.go's ".staging",
// so none of this package's several staging areas can collide.
const fppConnectUploadStateSubdir = "fppconnect-uploads"

const fppConnectIndexFileName = "index.json"

// fppConnectAllowedDirs is ADR-044 decision 4's directory allowlist: only
// these three names are ever accepted as the {dir} path segment of
// PATCH/POST /api/file/{dir}. Not "effects" (xLights' own effects
// directory): this seam never receives .eseq files, and RES-003 section
// 9.4 lists it only as one of real FPP's four accepted directories, never
// as one ShowMesh accepts.
var fppConnectAllowedDirs = map[string]bool{
	"sequences": true,
	"music":     true,
	"videos":    true,
}

// fppConnectHeldRecord is one file this node has fully received: its
// identity on disk (Dir, Name), its content (SizeBytes, ContentHash), when
// assembly completed (ReceivedAt), and its current show binding. Bound is
// false for a held-but-unbound file (ADR-044 decision 8's "kept,
// registered nowhere, reported as an unbound held file"); UnboundReason
// then names which unresolved case produced that (see completeLocked),
// distinguishing the states ADR-039 decision 5 requires stay distinct.
type fppConnectHeldRecord struct {
	Dir         string    `json:"dir"`
	Name        string    `json:"name"`
	SizeBytes   int64     `json:"sizeBytes"`
	ContentHash string    `json:"contentHash"`
	ReceivedAt  time.Time `json:"receivedAt"`

	Bound bool   `json:"bound"`
	Show  string `json:"show,omitempty"`

	// ShowID is Show's resolved config object id (FC3, ADR-028 decision
	// 8), set whenever Bound is true: xLights and FC2's own binding speak
	// only Show's display name, but POST /api/v1/assets' `show` field
	// requires the id, which the coordinator's own showExists validates
	// against, not the name. Resolved once, at bind time, from the
	// coordinator's pushed "shows" id/name list (fppConnectView.ShowID),
	// alongside Show rather than computed again by the registrar, so the
	// two can never drift apart from each other.
	ShowID          string `json:"showId,omitempty"`
	LogicalSequence string `json:"logicalSequence,omitempty"`
	UnboundReason   string `json:"unboundReason,omitempty"`

	// RegistrationState is FC3's addition (ADR-028 decision 8): "" for a
	// bound file this lane has not yet attempted to register, one of
	// [fppConnectRegistrationSkipped] (a music/videos file: this lane
	// registers FSEQ content only), [fppConnectRegistrationPending] (an
	// attempt failed retryably and another is scheduled),
	// [fppConnectRegistrationRegistered] (the coordinator accepted it), or
	// [fppConnectRegistrationFailed] (a non-retryable coordinator refusal,
	// or a local content-hash mismatch against the coordinator's
	// response). Always "" for an unbound record: ADR-030 decision 5's
	// "interrupted upload registers nothing" extends to "unresolvable
	// binding registers nothing," and this state must never read as
	// "pending" for a file that was never a registration candidate at all.
	RegistrationState string `json:"registrationState,omitempty"`

	// RegistrationAssetID is the coordinator-assigned asset id, set only
	// when RegistrationState is "registered".
	RegistrationAssetID string `json:"registrationAssetId,omitempty"`

	// RegistrationRolledBack mirrors the coordinator's own rolledBack flag
	// (ADR-028 decision 10) from the registration that produced
	// RegistrationAssetID.
	RegistrationRolledBack bool `json:"registrationRolledBack,omitempty"`

	// RegistrationReason is human-readable evidence for the current
	// RegistrationState: why registration is skipped, the retry reason
	// while pending, or the failure detail. Empty when RegistrationState
	// is "" or "registered", except immediately after BindShow resets an
	// already-registered record to "" for a rebind to a new show or
	// sequence identity (review round 5 finding 1,
	// resetRegistrationForRebindLocked): RegistrationReason then carries
	// the superseded asset id as evidence, until the next registration
	// attempt under the new identity overwrites it.
	RegistrationReason string `json:"registrationReason,omitempty"`

	// RegistrationProblemType is the coordinator's RFC 9457 problem `type`
	// for a non-retryable refusal, set only when RegistrationState is
	// "failed" and the failure came from the coordinator's own response
	// (empty for a locally-detected failure, e.g. a content-hash
	// mismatch).
	RegistrationProblemType string `json:"registrationProblemType,omitempty"`

	// RegistrationNextRetryAt is when the retry loop will next attempt
	// registration, set only when RegistrationState is "pending".
	RegistrationNextRetryAt time.Time `json:"registrationNextRetryAt,omitempty"`
}

// fppConnectEvent is evidence not (necessarily) tied to any one held
// file's current state: a POST /api/playlist/{name} whose name matched no
// show or matched more than one ("unknown"/"ambiguous", ADR-044 decision
// 8), or a refused upload chunk ("too-large", "dir-full", "disk-full",
// "gap", "bad-name", "bad-dir", ADR-044 decision 4). Kept as a small
// bounded log (fppConnectMaxEvents) alongside the held records rather than
// invented as a second store, since both exist for the same reason: an
// operator must be able to see what happened without reading logs, and
// xLights never inspects any of these calls' response status.
type fppConnectEvent struct {
	Kind string    `json:"kind"`
	Name string    `json:"name"`
	At   time.Time `json:"at"`

	// Dir and Reason are set for a refused-upload event; Entries,
	// EntriesTruncated and MatchCount are set for an "unknown"/"ambiguous"
	// playlist event. The two families never populate each other's
	// fields.
	Dir    string `json:"dir,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Entries is capped at fppConnectMaxEventEntries, independently of
	// fppConnectMaxEvents' bound on the whole log (review round 3 finding
	// 2): a single POST /api/playlist/{name} body, up to 1 MiB
	// (fppConnectMaxPlaylistBodyBytes) with no per-entry count limit,
	// could otherwise name tens of thousands of distinct values and carry
	// every one of them in this one event, onto every render report,
	// forever. EntriesTruncated states how many were cut so the
	// capping itself is never silent.
	Entries          []string `json:"entries,omitempty"`
	EntriesTruncated int      `json:"entriesTruncated,omitempty"`
	MatchCount       int      `json:"matchCount,omitempty"`
}

// fppConnectMaxEvents bounds fppConnectHeldStore.events: the oldest event
// is dropped once this many are held, so a misbehaving or merely curious
// client posting many unknown playlist names, or triggering many refused
// uploads, cannot grow this file without bound.
const fppConnectMaxEvents = 50

// fppConnectMaxEventEntries bounds fppConnectEvent.Entries; see that
// field's own doc comment.
const fppConnectMaxEventEntries = 32

// fppConnectMaxEventStringBytes bounds fppConnectEvent.Name, Reason, and
// each of Entries' own strings (review round 4 finding 1): Name can be an
// Upload-Name header value bounded only by fppConnectMaxHeaderBytes (16
// KiB) upstream, Reason often echoes Name straight back inside a
// formatted sentence, and a single playlist entry has no length limit of
// its own short of fppConnectMaxPlaylistBodyBytes (1 MiB): any one of
// those alone could already carry a single event past mqttproto's own
// envelope limit, independent of fppConnectMaxEventEntries' bound on how
// MANY entries one event carries.
const fppConnectMaxEventStringBytes = 256

// fppConnectEventStringTruncatedSuffix marks a string this store bounded
// at record time, matching mqttproto.RenderStderrTruncatedSuffix's own
// "truncation is visible, never silent" rule one field over.
const fppConnectEventStringTruncatedSuffix = "...[truncated]"

// fppConnectBoundEventString truncates s to fppConnectMaxEventStringBytes
// bytes, appending fppConnectEventStringTruncatedSuffix when truncation
// actually happens. Pure and side-effect-free so it can be exercised
// directly in a test.
func fppConnectBoundEventString(s string) string {
	if len(s) <= fppConnectMaxEventStringBytes {
		return s
	}
	cut := fppConnectMaxEventStringBytes - len(fppConnectEventStringTruncatedSuffix)
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + fppConnectEventStringTruncatedSuffix
}

// capEventEntries truncates entries to fppConnectMaxEventEntries,
// reporting how many were cut, and bounds each surviving entry's own
// length to fppConnectMaxEventStringBytes (review round 4 finding 1: the
// count cap alone does nothing against a single, individually oversized
// entry). Pure and side-effect-free so it can be exercised directly in a
// test independent of the store.
func capEventEntries(entries []string) (capped []string, truncated int) {
	if len(entries) > fppConnectMaxEventEntries {
		truncated = len(entries) - fppConnectMaxEventEntries
		entries = entries[:fppConnectMaxEventEntries]
	}
	capped = make([]string, len(entries))
	for i, e := range entries {
		capped[i] = fppConnectBoundEventString(e)
	}
	return capped, truncated
}

// fppConnectMaxPending bounds fppConnectHeldStore.pending the same way
// fppConnectMaxEvents bounds events: oldest entries are evicted first once
// this many are held. RES-003 section 10.6's playlist body has no size
// limit of its own beyond fppConnectMaxPlaylistBodyBytes (1 MiB), which
// alone could carry tens of thousands of distinct sequenceName/mediaName
// entries, so an unbounded pending map would grow (and be persisted)
// without bound from a single POST.
const fppConnectMaxPending = 500

// fppConnectPendingBinding is one entry of fppConnectHeldStore.pending: the
// show a POST /api/playlist/{name} named for a file that did not exist
// yet, resolved to both its display name (what BindShow's own idempotent
// re-application compares against) and its config object id (FC3, ADR-028
// decision 8), so a file completing afterwards binds with the same id a
// same-tick bind would have used, never a name alone that a later
// registration attempt would have to re-resolve. ShowID is empty exactly
// when the display name resolved but its id has not been pushed yet
// (review round 2 finding D, BindPendingShowID): the entry survives in
// this same map either way, and RebindPendingShowIDs is what fills in
// ShowID once a later push can.
type fppConnectPendingBinding struct {
	ShowName string `json:"showName"`
	ShowID   string `json:"showId"`
}

// fppConnectUnboundReasonShowIDNotPushed is fppConnectHeldRecord.
// UnboundReason's value for review round 2 finding D: a POST
// /api/playlist/{name} (or the active-show pending fallback) named a
// show this node's ShowNames() lists exactly once, but whose config
// object id has not been pushed yet (a node snapshot, or the pushing
// coordinator, that predates the additive "shows" field). Distinct from
// genuine ambiguity (two shows sharing the display name): the fix is a
// later push resolving automatically, not an operator renaming a show.
const fppConnectUnboundReasonShowIDNotPushed = "show id not yet pushed"

// fppConnectIndex is the whole of fppConnectHeldStore's durable state, and
// exactly what persistLocked/load read and write as one JSON document,
// matching internal/agent/heldcatalog.FileStore's identical "one atomic
// file holds the one durable record" discipline, generalized here to a
// small collection rather than a single record.
type fppConnectIndex struct {
	Records map[string]fppConnectHeldRecord     `json:"records"`
	Pending map[string]fppConnectPendingBinding `json:"pending"`
	// PendingOrder is Pending's insertion order, oldest first, so
	// fppConnectMaxPending's eviction survives a restart correctly rather
	// than reverting to Go's unspecified map iteration order.
	PendingOrder []string          `json:"pendingOrder"`
	Events       []fppConnectEvent `json:"events"`
}

// fppConnectChunkWriter is the seam a test substitutes to inject a disk-
// full outcome (ADR-044 decision 4's fourth bound) without actually
// filling a disk. The production implementation, osFPPConnectChunkWriter,
// writes through the real filesystem.
type fppConnectChunkWriter interface {
	// WriteChunk copies up to n bytes read from r into path at offset,
	// creating path if needed, and returns how many bytes were actually
	// written. When truncate is true (offset 0's "start fresh" case;
	// review round 1 finding 7), path is truncated to exactly the bytes
	// this call writes: no tail from a longer previous attempt at this
	// name can ever survive into the new one, even if an earlier
	// best-effort os.Remove of the stale file silently failed.
	WriteChunk(path string, offset int64, r io.Reader, n int64, truncate bool) (written int64, err error)
}

// fppConnectChunkFile is the *os.File subset osFPPConnectChunkWriter needs:
// positioned writes, and a Close whose own error must never be discarded
// (review round 7 finding 2).
type fppConnectChunkFile interface {
	io.WriterAt
	io.Closer
}

// fppConnectOpenChunkFile opens path for osFPPConnectChunkWriter.WriteChunk,
// a package-level var matching fppConnectRegisterHTTPClient's identical
// swap-for-tests convention: a test substitutes a fppConnectChunkFile whose
// Close deliberately fails, standing in for a real filesystem condition
// (ENOSPC under delayed allocation, a network mount) a sandboxed test
// cannot reliably reproduce, while still exercising osFPPConnectChunkWriter's
// own real Close-handling logic unchanged.
var fppConnectOpenChunkFile = func(path string, flags int, perm os.FileMode) (fppConnectChunkFile, error) {
	return os.OpenFile(path, flags, perm)
}

// osFPPConnectChunkWriter is fppConnectChunkWriter's real implementation:
// open (creating or truncating as directed), then a single positioned
// streaming copy. Offset is always exactly the file's current size by the
// time this is called for a non-truncating write (WriteChunk's gap check
// enforces that), so this never leaves a hole.
type osFPPConnectChunkWriter struct{}

func (osFPPConnectChunkWriter) WriteChunk(path string, offset int64, r io.Reader, n int64, truncate bool) (int64, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if truncate {
		flags |= os.O_TRUNC
	}
	f, err := fppConnectOpenChunkFile(path, flags, 0o644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(io.NewOffsetWriter(f, offset), io.LimitReader(r, n))
	closeErr := f.Close()
	if copyErr != nil {
		return written, copyErr
	}
	// A write failure can surface only at Close, not at the write itself
	// (review round 7 finding 2): delayed allocation means ENOSPC, or any
	// other write error, on some filesystems (and on a network mount) is
	// reported only once the kernel actually flushes buffered pages,
	// which Close is what forces. Discarding this error previously
	// reported io.Copy's own byte count as a successful chunk regardless,
	// so finishChunkLocked recorded a content hash for bytes that were
	// never actually committed to disk. fppConnectIsDiskFull classifies
	// this the identical way it already classifies a write-time ENOSPC,
	// since finishChunkLocked branches on writeErr alone, never on which
	// step produced it.
	return written, closeErr
}

// fppConnectIsDiskFull reports whether err is the ENOSPC outcome ADR-044
// decision 4's fourth bound requires be classified distinctly from a
// generic write failure. errors.Is unwinds through the *fs.PathError (or
// similar) net/http's and os's own write paths wrap a raw syscall.Errno
// in, which is exactly what a fake fppConnectChunkWriter in a test, or a
// real ENOSPC from the kernel, both produce.
func fppConnectIsDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC)
}

// fppConnectChunkOutcome is WriteChunk's result: exactly one of these per
// call, each mapped to one HTTP status by fppconnectupload.go's
// handleFilePatch.
type fppConnectChunkOutcome int

const (
	// fppConnectChunkAccepted: the chunk landed; more are expected before
	// this upload completes.
	fppConnectChunkAccepted fppConnectChunkOutcome = iota
	// fppConnectChunkCompleted: this chunk completed the upload; rec is
	// the resulting held record, already resolved (bound or unbound).
	fppConnectChunkCompleted
	// fppConnectChunkGap: offset did not match bytes already received;
	// the fragment was discarded.
	fppConnectChunkGap
	// fppConnectChunkTooLarge: the per-file cap, or the declared
	// Upload-Length, was or would be exceeded; the fragment was
	// discarded (or, at offset 0, nothing was ever written).
	fppConnectChunkTooLarge
	// fppConnectChunkDirFull: the total asset-directory cap would be
	// exceeded; nothing was written.
	fppConnectChunkDirFull
	// fppConnectChunkDiskFull: the filesystem returned ENOSPC while
	// writing; the fragment was discarded.
	fppConnectChunkDiskFull
	// fppConnectChunkWriteFailed: any other I/O failure (creating a
	// directory, writing, or renaming into place); the fragment was
	// discarded and nothing was registered.
	fppConnectChunkWriteFailed
)

// fppConnectInFlight is one upload's in-memory bookkeeping while chunks
// are still arriving: its running content hash (review round 1 finding 4:
// hashed incrementally as chunks land, so completion only finalizes
// rather than re-reading a multi-gigabyte file from disk against
// WriteTimeout), how many bytes have landed so far, and the total this
// attempt declared. Also this upload's asset-directory-cap reservation
// (review round 1 finding 5): uploadLength-bytesReceived is what it still
// intends to write, added to any concurrent upload's own offset-0 check so
// two uploads that individually fit under today's on-disk usage cannot
// together exceed the cap. Lives only in s.inFlight, never on disk: see
// this file's package doc comment.
type fppConnectInFlight struct {
	hash          hash.Hash
	bytesReceived int64
	uploadLength  int64

	// writing is true while a goroutine is actively copying a chunk for
	// this key with the store's mutex released (review round 3 finding
	// 3, see WriteChunk's own doc comment): a second request for the same
	// key must never be allowed to discard or continue this entry out
	// from under the goroutine that owns it, so every locked section that
	// would otherwise mutate or delete this entry checks writing first
	// and refuses instead.
	writing bool

	// lastChunkAt is stamped on creation and on every successfully
	// reconciled chunk, never while writing is true. sweepIdleInFlightLocked
	// uses it to find, and discard, an abandoned upload's reservation
	// (review round 3 finding 4).
	lastChunkAt time.Time
}

// fppConnectHeldStore is the whole of FC2's server-side state: in-flight
// chunk bookkeeping (fppConnectInFlight) lives only in memory, but every
// completed file's record, every pending (not-yet-held) binding, and the
// bounded evidence log live here, guarded by one mutex and persisted as
// one atomic JSON document (fppConnectIndex) on every mutation.
//
// One mutex serializes every upload chunk and every playlist POST across
// every directory and every file: a deliberate simplification, not an
// oversight. ADR-044's client is one xLights instance talking to one node,
// and correctness (never miscounting the asset-directory cap under a race,
// never losing a gap detection to a concurrent write) is worth more here
// than concurrent throughput a single compatibility client will not
// exercise.
type fppConnectHeldStore struct {
	mu       sync.Mutex
	assetDir string
	writer   fppConnectChunkWriter
	logger   *slog.Logger

	records map[string]fppConnectHeldRecord     // key: dir + "/" + name
	pending map[string]fppConnectPendingBinding // file name -> show name/id
	// pendingOrder is pending's insertion order, oldest first; see
	// fppConnectMaxPending.
	pendingOrder []string
	events       []fppConnectEvent

	inFlight map[string]*fppConnectInFlight // key: dir + "/" + name

	onHeld func(fppConnectHeldRecord)
}

// newFPPConnectHeldStore constructs a store rooted at assetDir, backed by
// the real filesystem, loading any persisted index from a previous run. A
// load failure (missing file is not a failure; a corrupt one is) is
// logged and treated as an empty store, matching agent.go's own "a corrupt
// held-catalog file is exactly as untrustworthy as no catalog at all"
// precedent for heldcatalog.FileStore.Load: losing this seam's memory of
// prior bindings must never crash the agent or block it from accepting a
// fresh upload.
func newFPPConnectHeldStore(assetDir string, logger *slog.Logger) *fppConnectHeldStore {
	return newFPPConnectHeldStoreWithWriter(assetDir, osFPPConnectChunkWriter{}, logger)
}

// newFPPConnectHeldStoreWithWriter is newFPPConnectHeldStore's fuller
// form, taking an explicit fppConnectChunkWriter so a test can inject a
// disk-full (or otherwise failing) writer without touching a real
// filesystem's free space.
func newFPPConnectHeldStoreWithWriter(assetDir string, writer fppConnectChunkWriter, logger *slog.Logger) *fppConnectHeldStore {
	s := &fppConnectHeldStore{
		assetDir: assetDir,
		writer:   writer,
		logger:   logger,
		records:  map[string]fppConnectHeldRecord{},
		pending:  map[string]fppConnectPendingBinding{},
		inFlight: map[string]*fppConnectInFlight{},
	}
	if err := s.load(); err != nil {
		logger.Warn("fppconnect: failed to load held upload index; starting with an empty catalog", "error", err)
	}
	return s
}

// SetOnHeld registers the callback invoked, synchronously and still under
// this store's lock, every time a held record is created or its binding
// changes: on upload completion (bound or not) and on every successful
// POST /api/playlist/{name} bind. This is FC3's hook: the registration
// seam listens here to learn when a file is ready to register with the
// coordinator's asset store. nil (the default; nothing calls SetOnHeld
// before FC3 exists) means no callback runs. Because this runs under the
// store's lock, an implementation must return quickly and must never call
// back into this store.
func (s *fppConnectHeldStore) SetOnHeld(fn func(fppConnectHeldRecord)) {
	s.mu.Lock()
	s.onHeld = fn
	s.mu.Unlock()
}

// Held returns every held record, sorted by dir then name for a stable,
// readable listing. This is FC3's second hook: a registration seam that
// starts after this node already holds files (a restart, for instance)
// reads its backlog here rather than waiting on SetOnHeld alone. It is
// also what internal/agent/renderreport.go reads to publish ADR-044
// decision 8's unbound-held-file evidence.
func (s *fppConnectHeldStore) Held() []fppConnectHeldRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fppConnectHeldRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Events returns a copy of the bounded evidence log (unknown/ambiguous
// playlist posts and refused uploads), oldest first.
func (s *fppConnectHeldStore) Events() []fppConnectEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fppConnectEvent, len(s.events))
	copy(out, s.events)
	return out
}

// HeldFilePath returns the on-disk path of the completed, held file for
// (dir, name), for FC3's registrar to open and stream as the registration
// request's file part. Exported (unlike the identical unexported
// heldFilePath below) because the registrar lives in a different file
// within this same package but has no other need to reach into this
// store's internal layout.
func (s *fppConnectHeldStore) HeldFilePath(dir, name string) string {
	return s.heldFilePath(dir, name)
}

// RecordFor returns the currently held record for (dir, name), if any.
// FC3's registerLoop calls this at the top of every iteration to read the
// record's CURRENT state rather than trust the copy it was started or
// last retried with, which can be stale by the time it actually runs (a
// concurrent rebind, or another path already resolving this record's
// registration state while this goroutine was queued, review round 1
// finding 3).
func (s *fppConnectHeldStore) RecordFor(dir, name string) (fppConnectHeldRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[dir+"/"+name]
	return rec, ok
}

// CollidingRecords returns every OTHER record in dir that shares (dir,
// name)'s intended identity (review round 7 finding 1: with three or more
// files sharing one identity, returning only the first match, as a single
// prior record did, could surface any one of the OTHER competitors, not
// necessarily the strongest one, letting a caller wrongly conclude that
// identity has no registered or in-flight owner when it actually does): two
// distinct file name stems can slugify to the identical sequence id ("My
// Show.fseq" and "my_show.fseq" both slug to "my-show"), and the assets
// API's identity is (show, sequence, targetKind, target), never the
// filename, so registering both under the same show would silently
// supersede one with the other. A competitor is either another currently
// bound record sharing showID and logicalSequence, or another record still
// awaiting its show's config object id
// (fppConnectUnboundReasonShowIDNotPushed, BindPendingShowID) whose
// intended show name (showName) and logicalSequence both match (review
// round 5 finding 4): BindPendingShowID temporarily unbinds a record
// exactly like this while it waits for a later push to carry the id it
// needs, and skipping every unbound record here used to make it invisible
// to a competitor's collision check for as long as that wait lasted,
// letting whichever of two colliding files happened to attempt while the
// other was mid-wait claim the identity uncontested, decided by push
// timing rather than claimIdentity's own ReceivedAt fairness rule.
// Scoped to dir so a music/videos file, which this lane never registers
// at all, is never flagged against a sequences file it could never
// actually collide with at the API. Returns nil when no such collision
// exists.
func (s *fppConnectHeldStore) CollidingRecords(dir, name, showID, showName, logicalSequence string) []fppConnectHeldRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []fppConnectHeldRecord
	for _, rec := range s.records {
		if rec.Dir != dir || rec.Name == name || rec.LogicalSequence != logicalSequence {
			continue
		}
		if rec.Bound && rec.ShowID == showID {
			out = append(out, rec)
			continue
		}
		if !rec.Bound && rec.UnboundReason == fppConnectUnboundReasonShowIDNotPushed && rec.Show == showName {
			out = append(out, rec)
		}
	}
	return out
}

// setRegistrationLocked applies one registration-state transition to
// key's (dir, name) record and persists it, s.mu already held. A no-op
// (returns false) when the record no longer exists, its content hash no
// longer matches wantHash, or its resolved identity (ShowID,
// LogicalSequence) no longer matches wantShowID/wantLogicalSequence:
// either the first means a fresh upload has since replaced what this call
// was about to report on, or the second means a rebind (BindShow) landed
// while this attempt was in flight (review round 6 finding 1) and the
// record now belongs to a different show or sequence than the attempt
// this call reports on ever ran against. Either way, whatever process
// owns the record now (a fresh upload's own registerLoop, or the same
// loop's next iteration re-reading the rebound record) owns its state,
// never this stale call.
func (s *fppConnectHeldStore) setRegistrationLocked(dir, name, wantHash, wantShowID, wantLogicalSequence string, apply func(*fppConnectHeldRecord)) bool {
	key := dir + "/" + name
	rec, exists := s.records[key]
	if !exists || rec.ContentHash != wantHash || rec.ShowID != wantShowID || rec.LogicalSequence != wantLogicalSequence {
		return false
	}
	apply(&rec)
	s.records[key] = rec
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a registration state change", "dir", dir, "name", name, "error", err)
	}
	return true
}

// SetRegistrationSkipped records that (dir, name), whose content hash and
// resolved identity must still equal wantHash/wantShowID/
// wantLogicalSequence, is held but will never be registered in this lane
// (a music or videos upload: FC3 registers FSEQ content only). reason
// names why, for the render report's evidence. Returns false (a no-op)
// when wantHash, wantShowID, or wantLogicalSequence no longer matches the
// current record.
func (s *fppConnectHeldStore) SetRegistrationSkipped(dir, name, wantHash, wantShowID, wantLogicalSequence, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setRegistrationLocked(dir, name, wantHash, wantShowID, wantLogicalSequence, func(rec *fppConnectHeldRecord) {
		rec.RegistrationState = fppConnectRegistrationSkipped
		rec.RegistrationReason = reason
		rec.RegistrationProblemType = ""
		rec.RegistrationNextRetryAt = time.Time{}
	})
}

// SetRegistrationPending records that a registration attempt for (dir,
// name) failed retryably: reason is the transport error, the coordinator's
// problem detail, or "coordinator base URL not configured"; nextRetryAt is
// when the retry loop will try again. Returns false (a no-op) when
// wantHash, wantShowID, or wantLogicalSequence no longer matches the
// current record.
func (s *fppConnectHeldStore) SetRegistrationPending(dir, name, wantHash, wantShowID, wantLogicalSequence, reason string, nextRetryAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setRegistrationLocked(dir, name, wantHash, wantShowID, wantLogicalSequence, func(rec *fppConnectHeldRecord) {
		rec.RegistrationState = fppConnectRegistrationPending
		rec.RegistrationReason = reason
		rec.RegistrationProblemType = ""
		rec.RegistrationNextRetryAt = nextRetryAt
	})
}

// SetRegistrationFailed records a non-retryable registration failure for
// (dir, name): problemType is the coordinator's RFC 9457 problem `type`
// (empty for a locally-detected failure, e.g. a content-hash mismatch),
// and reason is its detail, verbatim. Returns false (a no-op) when
// wantHash, wantShowID, or wantLogicalSequence no longer matches the
// current record.
func (s *fppConnectHeldStore) SetRegistrationFailed(dir, name, wantHash, wantShowID, wantLogicalSequence, problemType, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setRegistrationLocked(dir, name, wantHash, wantShowID, wantLogicalSequence, func(rec *fppConnectHeldRecord) {
		rec.RegistrationState = fppConnectRegistrationFailed
		rec.RegistrationReason = reason
		rec.RegistrationProblemType = problemType
		rec.RegistrationNextRetryAt = time.Time{}
	})
}

// SetRegistrationRegistered records that (dir, name) is now registered
// with the coordinator's asset store: assetID and rolledBack are the
// coordinator's own response fields (ADR-028 decision 10). Returns false
// (a no-op) when wantHash, wantShowID, or wantLogicalSequence no longer
// matches the current record.
func (s *fppConnectHeldStore) SetRegistrationRegistered(dir, name, wantHash, wantShowID, wantLogicalSequence, assetID string, rolledBack bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setRegistrationLocked(dir, name, wantHash, wantShowID, wantLogicalSequence, func(rec *fppConnectHeldRecord) {
		rec.RegistrationState = fppConnectRegistrationRegistered
		rec.RegistrationAssetID = assetID
		rec.RegistrationRolledBack = rolledBack
		rec.RegistrationReason = ""
		rec.RegistrationProblemType = ""
		rec.RegistrationNextRetryAt = time.Time{}
	})
}

// MainPlaylistFor returns one fppConnectPlaylistEntry per held record
// currently bound to showName, sorted by file name, for GET
// /api/playlist/{name}. Returns nil (never a non-nil empty slice) when
// nothing is bound; the HTTP handler is what turns that into a JSON "[]"
// rather than "null".
//
// Every entry uses RES-003 section 10.6's "without media" shape
// regardless of which directory the underlying file came from: a held
// music/ or videos/ file bound to a show is emitted here as a
// sequenceName entry too, not the "with media" shape RES-003 documents
// for a paired sequence+media row. That is a real divergence from the
// documented shape, not an oversight; see
// docs/build/TRACK-E-FPP-CONNECT.md's "Listener surface" section for the
// reasoning (this store binds a sequence file and a media file as two
// independent held records sharing one show, never as one paired entry,
// so there is no mediaName to attach to any single entry it constructs),
// and for why it does not break xLights' read-modify-write round trip.
func (s *fppConnectHeldStore) MainPlaylistFor(showName string) []fppConnectPlaylistEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var names []string
	for _, rec := range s.records {
		if rec.Bound && rec.Show == showName {
			names = append(names, rec.Name)
		}
	}
	if names == nil {
		return nil
	}
	sort.Strings(names)

	out := make([]fppConnectPlaylistEntry, 0, len(names))
	for _, n := range names {
		out = append(out, fppConnectPlaylistEntry{
			Type:         "sequence",
			Enabled:      1,
			PlayOnce:     0,
			SequenceName: n,
			Duration:     0,
		})
	}
	return out
}

// fppConnectStem returns name's file name stem (RES-003 section 10.6's
// "logical sequence"): name with its final extension removed. A name with
// no extension is its own stem.
func fppConnectStem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// fppConnectLogicalSequenceSlug derives the value the assets API's
// identity requires for POST /api/v1/assets' `sequence` field (FC3,
// ADR-028 decision 8) from name's file name stem: the same slug rule
// every other Track E object id uses (config.ValidateShowObjectID,
// 1 to 64 characters of [a-z0-9-], never starting or ending with a
// hyphen). The stem is lowercased, every run of characters outside
// [a-z0-9] (spaces, underscores, punctuation, and any hyphen already
// present) collapses to one hyphen, leading and trailing hyphens are
// trimmed, and the result is truncated to 64 characters (trimming a
// trailing hyphen the truncation itself could newly expose). "" when the
// stem contains no [a-z0-9] character at all, e.g. a name of only
// punctuation: the caller stores that empty result as-is, and FC3's
// registrar refuses to register it (a request built from an empty
// sequence would only 400 forever) rather than silently sending an
// invalid value.
func fppConnectLogicalSequenceSlug(name string) string {
	stem := strings.ToLower(fppConnectStem(name))
	var b strings.Builder
	pendingHyphen := false // suppresses a hyphen until a real character has been written, so the result never starts with one
	for _, r := range stem {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
			continue
		}
		pendingHyphen = true
	}
	slug := b.String()
	if len(slug) > 64 {
		slug = strings.TrimSuffix(slug[:64], "-")
	}
	return slug
}

// stagingDir, stagingFilePath, heldFileDir, heldFilePath and indexPath are
// this store's own path layout, all rooted at s.assetDir; see this file's
// package doc comment for the full tree.
func (s *fppConnectHeldStore) stagingDir(dir string) string {
	return filepath.Join(s.assetDir, fppConnectUploadStateSubdir, "staging", dir)
}

func (s *fppConnectHeldStore) stagingFilePath(dir, name string) string {
	return filepath.Join(s.stagingDir(dir), name+".partial")
}

func (s *fppConnectHeldStore) heldFileDir(dir string) string {
	return filepath.Join(s.assetDir, fppConnectUploadStateSubdir, "held", dir)
}

func (s *fppConnectHeldStore) heldFilePath(dir, name string) string {
	return filepath.Join(s.heldFileDir(dir), name)
}

func (s *fppConnectHeldStore) indexPath() string {
	return filepath.Join(s.assetDir, fppConnectUploadStateSubdir, fppConnectIndexFileName)
}

// fppConnectInFlightTTL bounds how long an in-flight upload's asset-
// directory-cap reservation (fppConnectInFlight.uploadLength -
// bytesReceived) survives with no chunk arriving (review round 3 finding
// 4): an abandoned upload (xLights crashed, the network dropped) would
// otherwise reserve that headroom against fppconnect.settings.
// maxAssetDirBytes for the rest of this process's life, since nothing
// else ever revisits an in-flight entry once its client stops sending
// chunks. A few multiples of fppConnectFileReadDeadline
// (fppconnecthttp.go), so it never fires on an upload that is merely
// slow, only one that has genuinely stopped.
const fppConnectInFlightTTL = 3 * fppConnectFileReadDeadline

// fppConnectStuckWritingTTL bounds how long an in-flight entry may stay
// writing==true before the sweep reclaims it regardless of that flag
// (review round 4 finding 5's second, independent safety net). WriteChunk's
// own deferred cleanup already resets writing on every path, ordinary or
// panicking (see that function's own doc comment), so this only ever
// fires against some other way a writing entry's owning goroutine could
// be gone without that deferred reset having run. Set well beyond
// fppConnectFileReadDeadline (the longest a single legitimate chunk
// transfer can possibly still be writing==true for), so this never
// reclaims one that is merely slow.
const fppConnectStuckWritingTTL = 3 * fppConnectInFlightTTL // 90 minutes

// sweepIdleInFlightLocked discards every in-flight entry whose last chunk
// arrived more than fppConnectInFlightTTL before now, removing its
// staging file the same way discardFragment does. Called at the top of
// WriteChunk, s.mu already held. An entry currently being written
// (writing true) is normally left alone regardless of how old
// lastChunkAt is: its owning goroutine has the store unlocked and is
// actively extending it (review round 3 finding 3), so it is not idle by
// definition, but one still writing past fppConnectStuckWritingTTL is
// reclaimed anyway (review round 4 finding 5): at that age, its owning
// goroutine is gone, not merely slow.
func (s *fppConnectHeldStore) sweepIdleInFlightLocked(now time.Time) {
	for key, inf := range s.inFlight {
		if inf.writing {
			if now.Sub(inf.lastChunkAt) <= fppConnectStuckWritingTTL {
				continue
			}
		} else if now.Sub(inf.lastChunkAt) <= fppConnectInFlightTTL {
			continue
		}
		dir, name := fppConnectSplitKey(key)
		delete(s.inFlight, key)
		_ = os.Remove(s.stagingFilePath(dir, name))
	}
}

// fppConnectSplitKey reverses dir+"/"+name (fppConnectHeldStore's
// internal map key shape) back into its two parts. name itself is
// already validated elsewhere (fppConnectValidPlaylistName) to never
// contain "/", so splitting on the first occurrence is unambiguous even
// though dir theoretically could in principle; dir is always one of
// fppConnectAllowedDirs' three literal, slash-free names in practice.
func fppConnectSplitKey(key string) (dir, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return "", key
	}
	return parts[0], parts[1]
}

// discardFragment removes a staging file, ignoring a not-exist error, and
// drops any in-memory tracking for key. Every abandonment path in
// WriteChunk calls this so "the fragment is discarded" (ADR-044 decision
// 9) is one code path, not several copies that could drift apart. It is
// NOT what guarantees a stale tail cannot survive an offset-0 restart
// (this os.Remove's error is deliberately ignored, exactly the case review
// round 1 finding 7 flagged); the writer's truncate=true on that path is
// what makes that guarantee real regardless of whether this Remove
// succeeds.
func (s *fppConnectHeldStore) discardFragment(dir, name string) {
	key := dir + "/" + name
	delete(s.inFlight, key)
	_ = os.Remove(s.stagingFilePath(dir, name))
}

// refuseLocked records one refusal as evidence (review round 1 finding 2:
// ADR-044 decision 4 says exceeding a bound, or exhausting the disk, "is
// reported as evidence," and every refusal previously returned its reason
// to the HTTP caller and persisted nothing), then returns WriteChunk's
// result shape. s.mu already held.
func (s *fppConnectHeldStore) refuseLocked(outcome fppConnectChunkOutcome, kind, dir, name, reason string, at time.Time) (fppConnectChunkOutcome, string, fppConnectHeldRecord) {
	s.appendEventLocked(fppConnectEvent{Kind: kind, Dir: dir, Name: name, Reason: reason, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a refusal", "kind", kind, "dir", dir, "name", name, "error", err)
	}
	return outcome, reason, fppConnectHeldRecord{}
}

// RecordRefusal records a refusal that happens before WriteChunk is ever
// called: the directory allowlist ("bad-dir") and Upload-Name safety
// ("bad-name") checks in fppconnectupload.go's HTTP handlers, ahead of any
// staging. Public (unlike refuseLocked) because those checks run outside
// this store's lock.
func (s *fppConnectHeldStore) RecordRefusal(kind, dir, name, reason string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(fppConnectEvent{Kind: kind, Dir: dir, Name: name, Reason: reason, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a refusal", "kind", kind, "dir", dir, "name", name, "error", err)
	}
}

// WriteChunk applies one PATCH /api/file/{dir} chunk: offset-0 discard,
// gap detection, the per-file and asset-directory byte caps, the actual
// streaming write (classifying ENOSPC distinctly), and, once accumulated
// bytes equal uploadLength, finalizing the incrementally-computed hash,
// renaming into the held area, and binding resolution.
//
// The store's mutex is held only for validation (prepareChunkLocked) and
// for reconciling the result (finishChunkLocked), never for the network
// read itself (review round 3 finding 3): the old single-lock version
// held it across the whole copy, which meant one slow client blocked
// Held()/Events() (and so the render report, and every other upload or
// playlist request) for up to fppConnectFileReadDeadline. prepareChunkLocked
// marks the in-flight entry writing=true before releasing the lock, so a
// second concurrent request for the SAME key is refused outright rather
// than racing this copy; a request for any OTHER key proceeds normally
// and independently the whole time. A deferred cleanup resets writing
// back to false even if the unlocked copy panics (review round 4 finding
// 5): without it, a recovered per-connection panic would leave that
// entry's writing flag stuck true forever, refusing every future request
// for the same key, including a fresh offset-0 retry, as still "in
// progress."
//
// body is read directly, never buffered whole in memory (review round 1
// finding 3): n (the caller's r.ContentLength) bounds exactly how much of
// body this call reads, and the running SHA-256 in s.inFlight is updated
// as bytes stream through (finding 4), so completion never re-reads the
// finished file from disk to hash it.
//
// dir is assumed already validated against fppConnectAllowedDirs by the
// caller (fppconnectupload.go's handleFileRoute); name is assumed already
// validated by fppConnectValidPlaylistName. activeShow and resolveShowID
// are the view's ActiveShow and ShowID methods, threaded through as
// functions rather than the whole view so this store stays independent of
// fppConnectView (keeping this file's own test surface small: a test
// supplies two small closures rather than a whole fake view).
func (s *fppConnectHeldStore) WriteChunk(dir, name string, offset, uploadLength int64, body io.Reader, n int64, maxFileBytes, maxAssetDirBytes int64, at time.Time, activeShow func() (name string, known bool, everSet bool), resolveShowID func(name string) (id string, ok bool), showNames func() []string) (outcome fppConnectChunkOutcome, reason string, rec fppConnectHeldRecord) {
	key := dir + "/" + name
	stagingPath := s.stagingFilePath(dir, name)

	inf, bytesReceived, prepOutcome, prepReason, ok := s.prepareChunkLocked(dir, name, key, stagingPath, offset, uploadLength, n, maxFileBytes, maxAssetDirBytes, at)
	if !ok {
		return prepOutcome, prepReason, fppConnectHeldRecord{}
	}

	// finishedNormally guards the deferred cleanup below so it only ever
	// runs on the panic path (review round 4 finding 5, tightened by
	// review round 5 finding 1): the ordinary path already clears
	// inf.writing, and reconciles far more besides, inside
	// finishChunkLocked.
	finishedNormally := false
	defer func() {
		if finishedNormally {
			return
		}
		// A panic during the unlocked copy below is recovered per
		// connection by net/http, so the process keeps running, but this
		// goroutine never reaches finishChunkLocked. TeeReader has
		// already fed whatever bytes the writer read before panicking
		// into inf.hash, but inf.bytesReceived was never advanced to
		// match (only finishChunkLocked does that, and it is never
		// reached), so the in-flight entry's hash and offset have gone
		// out of sync with each other. Merely resetting writing back to
		// false (review round 4's fix) left that poisoned entry in
		// place: a retry at the offset the client still believes is
		// correct would be accepted as an ordinary continuation, feed
		// its bytes into the same already-poisoned hash a second time,
		// and complete with a ContentHash that does not match the bytes
		// actually on disk. Discarding the whole fragment instead forces
		// any retry to restart at offset 0, the one place a fresh hash
		// and stagingPath truncation are guaranteed. Guarded by an
		// identity check against s.inFlight[key] (review round 5 finding
		// 5's same guard): if this entry was already swept and replaced
		// by the time the panic unwinds, that replacement, not this
		// panicked attempt, owns the key now, and must not be discarded
		// out from under it.
		s.mu.Lock()
		if s.inFlight[key] == inf {
			s.discardFragment(dir, name)
		}
		s.mu.Unlock()
	}()

	// Unlocked from here through the copy: see this function's own doc
	// comment. TeeReader hashes exactly the bytes the writer actually
	// reads from body, whether or not the write ultimately succeeds; a
	// failed write is discarded in finishChunkLocked, so a hash polluted
	// by an aborted write never survives to be used.
	teed := io.TeeReader(body, inf.hash)
	written, writeErr := s.writer.WriteChunk(stagingPath, offset, teed, n, offset == 0)

	outcome, reason, rec = s.finishChunkLocked(dir, name, key, stagingPath, inf, bytesReceived, n, uploadLength, written, writeErr, at, activeShow, resolveShowID, showNames)
	finishedNormally = true
	return outcome, reason, rec
}

// prepareChunkLocked validates one chunk and, on success, installs (or
// reuses) its in-flight entry with writing set true, all under s.mu,
// which it releases before returning either way. ok is false whenever the
// chunk is refused outright (outcome/reason already recorded as evidence
// where applicable); ok is true only when the caller must proceed to copy
// the chunk and call finishChunkLocked.
func (s *fppConnectHeldStore) prepareChunkLocked(dir, name, key, stagingPath string, offset, uploadLength, n, maxFileBytes, maxAssetDirBytes int64, at time.Time) (inf *fppConnectInFlight, bytesReceived int64, outcome fppConnectChunkOutcome, reason string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sweepIdleInFlightLocked(at)

	existing, exists := s.inFlight[key]

	// A concurrent request for the SAME key currently being copied
	// (review round 3 finding 3) must never be allowed to discard or
	// continue it out from under the goroutine that owns it: refuse, the
	// same way an ordinary gap is refused, rather than risk two
	// goroutines writing one staging file at once. A real xLights client
	// never sends two requests for the same upload concurrently, so this
	// never fires in normal operation.
	if exists && existing.writing {
		o, r, _ := s.refuseLocked(fppConnectChunkGap, "gap", dir, name, fmt.Sprintf(
			"another request for %q is already in progress; refused rather than racing it", name), at)
		return nil, 0, o, r, false
	}

	if offset == 0 {
		// Offset 0 always starts fresh: drop any in-memory tracking and
		// best-effort remove any stale fragment of the same name before
		// anything else (ADR-044 decision 9's "the first chunk" reasoning
		// starts here). The write below's truncate=true is the real
		// guarantee against a surviving stale tail; see discardFragment's
		// doc comment.
		s.discardFragment(dir, name)
		exists = false
	} else if !exists || offset != existing.bytesReceived {
		s.discardFragment(dir, name)
		got := int64(-1)
		if exists {
			got = existing.bytesReceived
		}
		o, r, _ := s.refuseLocked(fppConnectChunkGap, "gap", dir, name, fmt.Sprintf(
			"upload offset %d does not match %d bytes already received for %q; the fragment was discarded",
			offset, got, name), at)
		return nil, 0, o, r, false
	} else if uploadLength != existing.uploadLength {
		// The declared total changed mid-upload (review round 3 finding
		// 5): trusting a later, different Upload-Length both mis-sizes
		// the eventual held record and lets bytesReceived grow past the
		// reservation frozen at offset 0, driving this upload's own
		// asset-directory-cap remainder negative and manufacturing free
		// headroom for a concurrent upload that should not exist.
		s.discardFragment(dir, name)
		o, r, _ := s.refuseLocked(fppConnectChunkGap, "length-mismatch", dir, name, fmt.Sprintf(
			"Upload-Length %d does not match the %d declared at offset 0 for %q; the fragment was discarded",
			uploadLength, existing.uploadLength, name), at)
		return nil, 0, o, r, false
	}

	if exists {
		bytesReceived = existing.bytesReceived
	}

	if offset == 0 {
		if uploadLength > maxFileBytes {
			o, r, _ := s.refuseLocked(fppConnectChunkTooLarge, "too-large", dir, name, fmt.Sprintf(
				"upload length %d bytes exceeds the per-file cap of %d bytes", uploadLength, maxFileBytes), at)
			return nil, 0, o, r, false
		}
		existingOnDisk, err := fppConnectDirBytes(s.assetDir)
		if err != nil {
			s.logger.Warn("fppconnect: failed to measure asset directory size; refusing this upload rather than risk exceeding the cap silently", "error", err)
			o, r, _ := s.refuseLocked(fppConnectChunkDirFull, "dir-full", dir, name, fmt.Sprintf(
				"could not measure the asset directory's current size against the %d byte cap: %v", maxAssetDirBytes, err), at)
			return nil, 0, o, r, false
		}
		// Every OTHER in-flight upload's still-undelivered remainder
		// counts against the cap too (review round 1 finding 5): without
		// this, two concurrent uploads each individually under today's
		// on-disk usage could both pass this check and, once both
		// finish, together exceed maxAssetDirBytes. This upload's own
		// remainder is its full uploadLength, added separately below.
		// Each other remainder is clamped at zero (review round 3
		// finding 5): the length-mismatch refusal above stops a negative
		// remainder from arising going forward, but this clamp is
		// defense in depth against ever letting one manufacture extra
		// headroom for somebody else.
		var reserved int64
		for k, other := range s.inFlight {
			if k == key {
				continue
			}
			remainder := other.uploadLength - other.bytesReceived
			if remainder < 0 {
				remainder = 0
			}
			reserved += remainder
		}
		if existingOnDisk+reserved+uploadLength > maxAssetDirBytes {
			o, r, _ := s.refuseLocked(fppConnectChunkDirFull, "dir-full", dir, name, fmt.Sprintf(
				"accepting %d declared bytes (plus %d bytes reserved by other in-progress uploads) would bring the asset directory to more than its %d byte cap (%d bytes already used)",
				uploadLength, reserved, maxAssetDirBytes, existingOnDisk), at)
			return nil, 0, o, r, false
		}
	}

	newTotal := bytesReceived + n
	if newTotal > uploadLength || newTotal > maxFileBytes {
		s.discardFragment(dir, name)
		o, r, _ := s.refuseLocked(fppConnectChunkTooLarge, "too-large", dir, name, fmt.Sprintf(
			"accumulated upload of %d bytes would exceed the declared length of %d bytes or the per-file cap of %d bytes",
			newTotal, uploadLength, maxFileBytes), at)
		return nil, 0, o, r, false
	}

	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o755); err != nil {
		return nil, 0, fppConnectChunkWriteFailed, fmt.Sprintf("creating staging directory for %q: %v", name, err), false
	}

	if offset == 0 {
		existing = &fppConnectInFlight{hash: sha256.New(), uploadLength: uploadLength}
		s.inFlight[key] = existing
	}
	existing.lastChunkAt = at
	existing.writing = true

	return existing, bytesReceived, 0, "", true
}

// finishChunkLocked reconciles one chunk's copy result under s.mu,
// retaken here after prepareChunkLocked released it (see WriteChunk's own
// doc comment): a write failure discards the fragment, a completed
// upload finalizes the hash and renames into the held area and resolves
// its binding, and an accepted-but-incomplete chunk just clears the
// writing flag and updates bytesReceived/lastChunkAt for the next one.
func (s *fppConnectHeldStore) finishChunkLocked(dir, name, key, stagingPath string, inf *fppConnectInFlight, bytesReceived, n, uploadLength, written int64, writeErr error, at time.Time, activeShow func() (name string, known bool, everSet bool), resolveShowID func(name string) (id string, ok bool), showNames func() []string) (outcome fppConnectChunkOutcome, reason string, rec fppConnectHeldRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.inFlight[key] != inf {
		// This attempt's own in-flight entry was swept as idle (review
		// round 5 finding 5) while its chunk copy ran unlocked above
		// (see WriteChunk's own doc comment for why the copy runs
		// unlocked at all). Whatever now occupies key, if anything,
		// belongs to a newer attempt: this stale one must never clear
		// its writing flag, discard its staging file, or rename it into
		// place as if it were the newer upload's own bytes. Refuse
		// without touching key's current entry.
		return fppConnectChunkGap, fmt.Sprintf("upload for %q was reclaimed as idle while this chunk was still being written; restart at offset 0", name), fppConnectHeldRecord{}
	}

	inf.writing = false

	if writeErr != nil {
		s.discardFragment(dir, name)
		if fppConnectIsDiskFull(writeErr) {
			return s.refuseLocked(fppConnectChunkDiskFull, "disk-full", dir, name, fmt.Sprintf("the disk is full while writing %q: %v", name, writeErr), at)
		}
		return fppConnectChunkWriteFailed, fmt.Sprintf("writing a chunk for %q: %v", name, writeErr), fppConnectHeldRecord{}
	}
	if written != n {
		// io.Copy stops cleanly (nil error) the moment its source
		// reports EOF, even mid-LimitReader, so a client that closes its
		// body early against a declared Content-Length it does not
		// honor produces a short written count here with no error at
		// all, not the error this code might otherwise be tempted to
		// rely on catching. Treat it the same as any other write failure
		// rather than accepting a truncated chunk as if it were the
		// whole one.
		s.discardFragment(dir, name)
		return fppConnectChunkWriteFailed, fmt.Sprintf("short write for %q: wrote %d of %d declared bytes", name, written, n), fppConnectHeldRecord{}
	}

	newTotal := bytesReceived + n
	inf.bytesReceived = newTotal
	inf.lastChunkAt = at

	if inf.bytesReceived < uploadLength {
		return fppConnectChunkAccepted, "", fppConnectHeldRecord{}
	}

	hashSum := "sha256:" + hex.EncodeToString(inf.hash.Sum(nil))
	delete(s.inFlight, key) // clear the reservation on completion (round 1 finding 5)

	heldPath := s.heldFilePath(dir, name)
	if err := os.MkdirAll(s.heldFileDir(dir), 0o755); err != nil {
		_ = os.Remove(stagingPath)
		return fppConnectChunkWriteFailed, fmt.Sprintf("creating held directory for %q: %v", name, err), fppConnectHeldRecord{}
	}
	if err := os.Rename(stagingPath, heldPath); err != nil {
		_ = os.Remove(stagingPath)
		return fppConnectChunkWriteFailed, fmt.Sprintf("finalizing held file %q: %v", name, err), fppConnectHeldRecord{}
	}

	rec = s.completeLocked(dir, name, uploadLength, hashSum, at, activeShow, resolveShowID, showNames)
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after completion", "dir", dir, "name", name, "error", err)
	}
	return fppConnectChunkCompleted, "", rec
}

// completeLocked resolves rec's binding on completion (ADR-044 decision
// 8) and records it, s.mu already held. Resolution order: a pending
// binding from a playlist POST that named this file before it existed
// wins first and is consumed; otherwise a PRIOR record for this (dir,
// name) that was already bound keeps its binding (a re-uploaded file, same
// identity, updated bytes, is not silently unbound); otherwise the active
// show, in ADR-039 decision 5's tri-state (bound if known AND non-empty,
// else unbound with a reason distinguishing "pushed null," "never
// pushed," and "pushed known but with an empty name," review round 1
// finding 8). Every bound branch also resolves the show's config object
// id via resolveShowID (FC3, ADR-028 decision 8): when the display name
// does not currently resolve to exactly one show (an active show pushed
// under a name that two shows now share, a genuinely stale edge case),
// the file is left unbound with its own distinct reason rather than
// binding with no id at all, which the registrar could never use. When
// the name IS known (showNames) but resolveShowID still fails, the
// coordinator has simply not pushed that show's id yet: bindTo holds the
// file unbound with fppConnectUnboundReasonShowIDNotPushed and Show set,
// mirroring handlePlaylistPost's identical distinction, so
// RebindPendingShowIDs (which requires that exact reason and a non-empty
// Show) converges it once a later push carries shows (review round 3
// finding 2; before this, the generic reason left Show empty and this
// record could never be rebound automatically).
func (s *fppConnectHeldStore) completeLocked(dir, name string, sizeBytes int64, hash string, at time.Time, activeShow func() (name string, known bool, everSet bool), resolveShowID func(name string) (id string, ok bool), showNames func() []string) fppConnectHeldRecord {
	key := dir + "/" + name
	prev, hadPrev := s.records[key]

	rec := fppConnectHeldRecord{Dir: dir, Name: name, SizeBytes: sizeBytes, ContentHash: hash, ReceivedAt: at}

	bindTo := func(showName string) {
		id, ok := resolveShowID(showName)
		if ok {
			rec.Bound = true
			rec.Show = showName
			rec.ShowID = id
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(name)
			return
		}
		if fppConnectContainsShow(showNames(), showName) {
			rec.Show = showName
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(name)
			rec.UnboundReason = fppConnectUnboundReasonShowIDNotPushed
			return
		}
		rec.UnboundReason = fmt.Sprintf("show %q does not currently resolve to exactly one show id", showName)
	}

	if binding, ok := s.pending[name]; ok {
		if binding.ShowID == "" {
			// A POST /api/playlist/{name} named this file's show by
			// display name before the coordinator ever pushed that
			// show's id (review round 2 finding D): held unbound, not
			// guessed, with a reason RebindPendingShowIDs specifically
			// looks for once a later push carries shows.
			rec.Show = binding.ShowName
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(name)
			rec.UnboundReason = fppConnectUnboundReasonShowIDNotPushed
		} else {
			rec.Bound = true
			rec.Show = binding.ShowName
			rec.ShowID = binding.ShowID
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(name)
		}
		s.deletePendingLocked(name)
	} else if hadPrev && prev.Bound {
		rec.Bound = true
		rec.Show = prev.Show
		rec.ShowID = prev.ShowID
		rec.LogicalSequence = prev.LogicalSequence
	} else {
		showName, known, everSet := activeShow()
		switch {
		case known && showName != "":
			bindTo(showName)
		case known:
			// known==true with an empty name is not a real show to bind
			// to (review round 1 finding 8): a prior version of this
			// code bound to the empty show here, which is exactly the
			// kind of wrong silent guess ADR-044 decision 8 forbids.
			rec.UnboundReason = "active show was pushed as known but with an empty name"
		case everSet:
			rec.UnboundReason = "no active show: the coordinator explicitly pushed no active show (pushed null)"
		default:
			rec.UnboundReason = "no active show: this node has never been pushed an active show"
		}
	}

	s.records[key] = rec
	if s.onHeld != nil {
		s.onHeld(rec)
	}
	return rec
}

// BindShow applies one POST /api/playlist/{name} whose name resolved to
// exactly one show (fppconnectupload.go's handlePlaylistPost decides
// that, and resolves showID from the same name at the same time, FC3,
// ADR-028 decision 8): every held record, in any directory, whose Name is
// in fileNames is bound to showName/showID; a name with no held match yet
// is remembered in s.pending so a file completing afterwards binds on
// completion (ADR-044 decision 8). Idempotent: binding an already-bound
// record to the same show sets the same fields again, and re-posting the
// same fileNames a second time produces the same records, not new ones
// (RES-003 section 10.6's "up to twice per target" requirement). A record
// that already carries registration progress under a DIFFERENT resolved
// identity (ShowID or LogicalSequence) has that progress reset to
// unregistered first (review round 5 finding 1): without this, a file
// registered under one show that a later playlist POST renames into
// another show kept reporting "registered" for the new show while no
// asset existed there at all, since nothing ever told the registrar to
// try again under the new identity.
func (s *fppConnectHeldStore) BindShow(showName, showID string, fileNames []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, fname := range fileNames {
		if fname == "" {
			continue
		}
		matched := false
		for key, rec := range s.records {
			if rec.Name != fname {
				continue
			}
			newSeq := fppConnectLogicalSequenceSlug(fname)
			if rec.RegistrationState != "" && (rec.ShowID != showID || rec.LogicalSequence != newSeq) {
				rec = resetRegistrationForRebindLocked(rec)
			}
			rec.Bound = true
			rec.Show = showName
			rec.ShowID = showID
			rec.LogicalSequence = newSeq
			rec.UnboundReason = ""
			s.records[key] = rec
			matched = true
			if s.onHeld != nil {
				s.onHeld(rec)
			}
		}
		if matched {
			s.deletePendingLocked(fname)
		} else {
			s.addPendingLocked(fname, showName, showID)
		}
	}

	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a playlist bind", "show", showName, "error", err)
	}
}

// resetRegistrationForRebindLocked clears rec's registration progress
// ahead of a rebind to a new (ShowID, LogicalSequence) identity (review
// round 5 finding 1's own helper, called from BindShow): the registrar's
// OnHeld sees the resulting empty RegistrationState as a fresh, not yet
// attempted candidate and starts a new attempt under the new identity.
// When rec was actually registered, the superseded asset id, show id, and
// sequence survive in RegistrationReason as evidence that a real
// registration existed under the old identity, even though it is
// otherwise empty for state "" (see RegistrationReason's own doc
// comment for that one documented exception).
func resetRegistrationForRebindLocked(rec fppConnectHeldRecord) fppConnectHeldRecord {
	reason := ""
	if rec.RegistrationState == fppConnectRegistrationRegistered {
		reason = fmt.Sprintf(
			"previously registered as asset %q under show %q, sequence %q, before being rebound to a different show",
			rec.RegistrationAssetID, rec.ShowID, rec.LogicalSequence)
	}
	rec.RegistrationState = ""
	rec.RegistrationAssetID = ""
	rec.RegistrationRolledBack = false
	rec.RegistrationReason = reason
	rec.RegistrationProblemType = ""
	rec.RegistrationNextRetryAt = time.Time{}
	return rec
}

// BindPendingShowID applies one POST /api/playlist/{name} whose name
// resolved to exactly one show by display name, but whose config object
// id has not been pushed yet (review round 2 finding D,
// fppConnectUnboundReasonShowIDNotPushed): every already-held record
// among fileNames is marked unbound with that reason, carrying showName
// so RebindPendingShowIDs can resolve it once a later push provides
// shows; a name with no held match yet is remembered in s.pending with an
// empty ShowID for the identical reason, resolved on completion
// (fppConnectHeldStore.completeLocked) the same way. Mirrors BindShow's
// own shape one field over, differing only in that nothing here ever sets
// Bound true. A record already bound with a non-empty ShowID is left
// untouched (review round 3 finding 5): without this, a push that
// regresses the coordinator's shows list back to not carrying this show's
// id (or a stale playlist POST arriving after a record has already bound
// and registered) would knock an already-registered record back to
// unbound, undoing real progress for no benefit, since the record is
// already bound to a resolvable show id and needs no rescue. Unlike
// ShowID, LogicalSequence is kept, not cleared (review round 5 finding
// 4): CollidingRecord matches a record awaiting its show id against
// showName and logicalSequence precisely so a second, colliding file
// attempting to register while this one waits still finds it as a
// competitor, rather than treating this temporary sub-state as if the
// file did not exist at all.
func (s *fppConnectHeldStore) BindPendingShowID(showName string, fileNames []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, fname := range fileNames {
		if fname == "" {
			continue
		}
		matched := false
		for key, rec := range s.records {
			if rec.Name != fname {
				continue
			}
			matched = true
			if rec.Bound && rec.ShowID != "" {
				continue
			}
			rec.Bound = false
			rec.Show = showName
			rec.ShowID = ""
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(fname)
			rec.UnboundReason = fppConnectUnboundReasonShowIDNotPushed
			s.records[key] = rec
			if s.onHeld != nil {
				s.onHeld(rec)
			}
		}
		if matched {
			s.deletePendingLocked(fname)
		} else {
			s.addPendingLocked(fname, showName, "")
		}
	}

	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a pending-show-id playlist bind", "show", showName, "error", err)
	}
}

// RebindPendingShowIDs re-resolves every held record whose UnboundReason
// is fppConnectUnboundReasonShowIDNotPushed, and every pending binding
// with an empty ShowID, against resolveShowID (review round 2 finding D):
// a node whose held store predates the coordinator's shows field, or that
// bound a playlist post before a push ever carried it, converges
// automatically the next time any push resolves the show name it already
// knew, with no operator action required. Called after every applied
// "fppconnect.configure" push (agent.go), whether or not this particular
// push actually changed shows: a push carrying the identical list as
// before just walks and finds nothing left to resolve.
func (s *fppConnectHeldStore) RebindPendingShowIDs(resolveShowID func(name string) (id string, ok bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := false
	for key, rec := range s.records {
		if rec.UnboundReason != fppConnectUnboundReasonShowIDNotPushed || rec.Show == "" {
			continue
		}
		id, ok := resolveShowID(rec.Show)
		if !ok {
			continue
		}
		rec.Bound = true
		rec.ShowID = id
		rec.LogicalSequence = fppConnectLogicalSequenceSlug(rec.Name)
		rec.UnboundReason = ""
		s.records[key] = rec
		changed = true
		if s.onHeld != nil {
			s.onHeld(rec)
		}
	}

	for fname, binding := range s.pending {
		if binding.ShowID != "" {
			continue
		}
		id, ok := resolveShowID(binding.ShowName)
		if !ok {
			continue
		}
		s.pending[fname] = fppConnectPendingBinding{ShowName: binding.ShowName, ShowID: id}
		changed = true
	}

	if !changed {
		return
	}
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after rebinding pending show ids", "error", err)
	}
}

// addPendingLocked records fname -> showName/showID in s.pending, evicting
// the oldest pending entry first when fppConnectMaxPending would otherwise
// be exceeded (review round 1 finding 6). Updating an already-pending
// name's show does not change its position in the eviction order: it is
// not a fresh entry. s.mu already held.
func (s *fppConnectHeldStore) addPendingLocked(fname, showName, showID string) {
	binding := fppConnectPendingBinding{ShowName: showName, ShowID: showID}
	if _, exists := s.pending[fname]; exists {
		s.pending[fname] = binding
		return
	}
	if len(s.pending) >= fppConnectMaxPending {
		if len(s.pendingOrder) > 0 {
			oldest := s.pendingOrder[0]
			s.pendingOrder = s.pendingOrder[1:]
			delete(s.pending, oldest)
		} else {
			// Should be unreachable given load's own fallback below, but
			// never silently grow past the cap: drop one arbitrary entry
			// rather than skip eviction.
			for k := range s.pending {
				delete(s.pending, k)
				break
			}
		}
	}
	s.pending[fname] = binding
	s.pendingOrder = append(s.pendingOrder, fname)
}

// deletePendingLocked removes fname from s.pending and s.pendingOrder, if
// present. s.mu already held.
func (s *fppConnectHeldStore) deletePendingLocked(fname string) {
	if _, exists := s.pending[fname]; !exists {
		return
	}
	delete(s.pending, fname)
	for i, k := range s.pendingOrder {
		if k == fname {
			s.pendingOrder = append(s.pendingOrder[:i], s.pendingOrder[i+1:]...)
			break
		}
	}
}

// fppConnectBoundEvent returns ev with every per-event bound applied: Name,
// Dir, and Reason each capped to fppConnectMaxEventStringBytes, and
// Entries capped both in count (fppConnectMaxEventEntries) and in each
// surviving entry's own length, via capEventEntries. EntriesTruncated is
// increased (never reset) by however many entries this call newly cuts, so
// a value already carried on ev survives. Shared by appendEventLocked
// (every freshly recorded event) and load (review round 4 finding 2: a
// persisted index can carry an event that predates this bound, or one
// edited or corrupted outside this store's own writes). Dir joined Name
// and Reason under this bound in review round 5 finding 2: a "bad-dir"
// refusal records the raw {dir} URL segment verbatim, which carries none
// of Upload-Name's own length limit.
func fppConnectBoundEvent(ev fppConnectEvent) fppConnectEvent {
	ev.Name = fppConnectBoundEventString(ev.Name)
	ev.Dir = fppConnectBoundEventString(ev.Dir)
	ev.Reason = fppConnectBoundEventString(ev.Reason)
	capped, truncated := capEventEntries(ev.Entries)
	ev.Entries = capped
	ev.EntriesTruncated += truncated
	return ev
}

// appendEventLocked applies fppConnectBoundEvent to ev, then appends it to
// s.events, dropping the oldest entry once fppConnectMaxEvents is
// exceeded. This is the one checkpoint every freshly recorded event passes
// through regardless of kind (review round 4 finding 1), whether built by
// refuseLocked/RecordRefusal (Name and Reason, no Entries) or
// RecordUnknownPlaylist/RecordAmbiguousPlaylist (Name and Entries, no
// Reason). s.mu already held.
func (s *fppConnectHeldStore) appendEventLocked(ev fppConnectEvent) {
	s.events = append(s.events, fppConnectBoundEvent(ev))
	if len(s.events) > fppConnectMaxEvents {
		s.events = s.events[len(s.events)-fppConnectMaxEvents:]
	}
}

// RecordUnknownPlaylist records that a POST /api/playlist/{name} named a
// show this node does not know (ADR-044 decision 8's unknown case):
// nothing binds, and this is the evidence an operator reads in place of
// the log line xLights will never surface (it never inspects this
// request's status).
func (s *fppConnectHeldStore) RecordUnknownPlaylist(name string, entries []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(fppConnectEvent{Kind: "unknown", Name: name, Entries: entries, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after an unknown playlist post", "name", name, "error", err)
	}
}

// RecordAmbiguousPlaylist records that a POST /api/playlist/{name} named
// a show this node's ShowNames() lists more than once: two shows sharing
// one display name, unresolvable from this listener's view (ADR-044
// decision 8's ambiguous case). matchCount is how many times name occurred
// in ShowNames(); this interface carries no show id, so that count is the
// most specific evidence available for "naming both."
func (s *fppConnectHeldStore) RecordAmbiguousPlaylist(name string, matchCount int, entries []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(fppConnectEvent{Kind: "ambiguous", Name: name, Entries: entries, MatchCount: matchCount, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after an ambiguous playlist post", "name", name, "error", err)
	}
}

// RecordShowIDNotPushed records that a POST /api/playlist/{name} named a
// show this node's ShowNames() lists exactly once, but whose config
// object id ShowID cannot resolve (review round 2 finding D): a node
// whose held snapshot, or the coordinator push that reached it, predates
// the additive "shows" id/name list. Distinct from "ambiguous" (two shows
// sharing this display name): reporting this case as ambiguous with a
// matchCount of 1 would misname a temporary propagation gap as a genuine
// naming collision. BindPendingShowID is this event's usual companion
// call: it is what actually holds the named files pending, and
// RebindPendingShowIDs is what resolves them once a later push carries
// shows.
func (s *fppConnectHeldStore) RecordShowIDNotPushed(name string, entries []string, at time.Time) {
	capped, truncated := capEventEntries(entries)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendEventLocked(fppConnectEvent{Kind: "show-id-not-pushed", Name: name, Entries: capped, EntriesTruncated: truncated, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a show-id-not-pushed playlist post", "name", name, "error", err)
	}
}

// persistLocked writes the whole index as one file, temp-then-rename, s.mu
// already held. Matches internal/agent/heldcatalog.FileStore.Save's exact
// discipline.
func (s *fppConnectHeldStore) persistLocked() error {
	idx := fppConnectIndex{Records: s.records, Pending: s.pending, PendingOrder: s.pendingOrder, Events: s.events}
	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("fppconnect: encode held upload index: %w", err)
	}
	dir := filepath.Join(s.assetDir, fppConnectUploadStateSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("fppconnect: create held upload state directory: %w", err)
	}
	target := s.indexPath()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("fppconnect: write held upload index: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("fppconnect: commit held upload index: %w", err)
	}
	return nil
}

// fppConnectIndexOnDisk mirrors fppConnectIndex's shape exactly, except
// Pending is left as raw JSON: load decodes it tolerantly against either
// shape (review round 2 finding E). An index the pre-FC3 build wrote has
// Pending as map[string]string (a bare show display name); the current
// build writes map[string]fppConnectPendingBinding. Unmarshaling the
// WHOLE document strictly against the new shape fails outright on an
// old-shape Pending value, which previously discarded Records too, even
// though only Pending's own shape had changed underneath it: the node's
// entire held-file memory was lost while the files themselves stayed on
// disk, untracked. persistLocked still always writes the current shape;
// only decoding needs to tolerate the old one.
type fppConnectIndexOnDisk struct {
	Records      map[string]fppConnectHeldRecord `json:"records"`
	Pending      json.RawMessage                 `json:"pending"`
	PendingOrder []string                        `json:"pendingOrder"`
	Events       []fppConnectEvent               `json:"events"`
}

// decodePendingTolerant decodes raw as the current
// map[string]fppConnectPendingBinding shape, falling back to the pre-FC3
// map[string]string shape (a bare show display name, no id) on failure
// and converting its entries to the current shape with an empty ShowID:
// exactly fppConnectUnboundReasonShowIDNotPushed's own "not resolved yet"
// state, which RebindPendingShowIDs (or simply a fresh playlist POST for
// the same show) heals automatically rather than requiring a distinct
// migration step. Returns (nil, nil) for an absent or null Pending key,
// matching encoding/json's own treatment of an omitted map.
func decodePendingTolerant(raw json.RawMessage) (map[string]fppConnectPendingBinding, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var current map[string]fppConnectPendingBinding
	if err := json.Unmarshal(raw, &current); err == nil {
		return current, nil
	}
	var legacy map[string]string
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	out := make(map[string]fppConnectPendingBinding, len(legacy))
	for fname, showName := range legacy {
		out[fname] = fppConnectPendingBinding{ShowName: showName}
	}
	return out, nil
}

// load reads a previously persisted index, if any, matching
// internal/agent/heldcatalog.FileStore.Load's identical "missing is fine,
// corrupt is an error" contract. Called only from the constructor, before
// s is shared, so it does not take s.mu itself.
func (s *fppConnectHeldStore) load() error {
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fppconnect: read held upload index: %w", err)
	}
	var idx fppConnectIndexOnDisk
	if err := json.Unmarshal(data, &idx); err != nil {
		return fmt.Errorf("fppconnect: decode held upload index: %w", err)
	}
	pending, err := decodePendingTolerant(idx.Pending)
	if err != nil {
		return fmt.Errorf("fppconnect: decode held upload index pending bindings: %w", err)
	}
	if idx.Records != nil {
		s.records = idx.Records
		for key, rec := range s.records {
			if !rec.Bound || rec.ShowID != "" {
				continue
			}
			// A pre-FC3 index predates ShowID entirely (ADR-028 decision
			// 8, review round 6 finding 2): every record it wrote decoded
			// as Bound true with ShowID's zero value, "". Left alone,
			// attemptRegister would send an empty show field forever, a
			// request the coordinator can only ever refuse terminally,
			// with nothing left to retry it: RebindPendingShowIDs only
			// ever walks a record whose UnboundReason already names it as
			// awaiting an id, and nothing rebinds an already-bound record
			// on its own. Repaired into that exact awaiting-id shape
			// instead, the same one BindPendingShowID produces: unbound,
			// UnboundReason fppConnectUnboundReasonShowIDNotPushed, Show
			// (a pre-FC3 field, already correct) kept, LogicalSequence
			// recomputed since a pre-FC3 record predates it too, so
			// RebindPendingShowIDs converges it the next time any push
			// resolves the show it already names.
			rec.Bound = false
			rec.LogicalSequence = fppConnectLogicalSequenceSlug(rec.Name)
			rec.UnboundReason = fppConnectUnboundReasonShowIDNotPushed
			s.records[key] = rec
		}
	}
	if pending != nil {
		s.pending = pending
	}
	if idx.PendingOrder != nil {
		s.pendingOrder = idx.PendingOrder
	} else if len(s.pending) > 0 {
		// A pre-finding-6 index has Pending but no PendingOrder: rebuild
		// one (Go map iteration order, arbitrary but deterministic for
		// this one rebuild) so eviction has a well-defined "oldest" from
		// here on, rather than panicking on an empty pendingOrder the
		// first time addPendingLocked needs to evict.
		for k := range s.pending {
			s.pendingOrder = append(s.pendingOrder, k)
		}
	}
	// Re-apply both caps to whatever was just loaded (review round 3
	// finding 9): a persisted index can exceed either one if it predates
	// that cap, if the cap was lowered since, or if the file was edited
	// or corrupted outside this store's own writes. addPendingLocked and
	// appendEventLocked only ever enforce the cap going forward; loading
	// is the one path that can hand this store a collection already over
	// it, so it must trim on the way in rather than assume its own
	// invariant already holds.
	if len(s.pendingOrder) > fppConnectMaxPending {
		evicted := s.pendingOrder[:len(s.pendingOrder)-fppConnectMaxPending]
		s.pendingOrder = s.pendingOrder[len(s.pendingOrder)-fppConnectMaxPending:]
		for _, k := range evicted {
			delete(s.pending, k)
		}
	}

	s.events = idx.Events
	if len(s.events) > fppConnectMaxEvents {
		s.events = s.events[len(s.events)-fppConnectMaxEvents:]
	}
	// Re-apply the per-event Name/Reason/Entries bound to each loaded
	// event too (review round 4 finding 2): appendEventLocked only ever
	// enforces fppConnectBoundEvent going forward, so a persisted index
	// predating fppConnectMaxEventStringBytes, or one whose Entries count
	// cap was lowered since, or one edited or corrupted outside this
	// store's own writes, must still be trimmed on the way in, the same
	// way the pending/event LIST caps just above are.
	for i, ev := range s.events {
		s.events[i] = fppConnectBoundEvent(ev)
	}
	return nil
}

// fppConnectDirBytes sums the size of every regular file under root,
// recursively: ADR-044 decision 4's third bound reads "the bytes already
// under AssetDir (assets, held files, staging)," which is the whole tree,
// not just this seam's own subdirectory.
func fppConnectDirBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return total, nil
		}
		return 0, err
	}
	return total, nil
}

// sweepFPPConnectUploadStaging removes every entry under
// <AssetDir>/fppConnectUploadStateSubdir/staging at startup, mirroring
// assets.go's sweepAssetStaging: a partial upload left behind by a
// previous, interrupted process run is never a partially-usable asset and
// must never be resumed against a chunk sequence a fresh process has no
// memory of (this store never resumes across a restart at all: see this
// file's package doc comment). held/ and index.json, siblings of
// staging/, are untouched: this never removes an already-completed held
// file or its record. Missing dir or missing staging/ is not an error.
func sweepFPPConnectUploadStaging(assetDir string) error {
	stagingRoot := filepath.Join(assetDir, fppConnectUploadStateSubdir, "staging")
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(stagingRoot, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
