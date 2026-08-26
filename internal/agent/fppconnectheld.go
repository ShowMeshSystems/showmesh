package agent

import (
	"encoding/json"
	"errors"
	"fmt"
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
// only writer; fppconnecthttp.go's GET /api/playlist/{name} handler and
// FC3's registration seam (via SetOnHeld/Held) are its readers.
//
// Layout under <AssetDir>/fppConnectUploadStateSubdir:
//
//	staging/<dir>/<Upload-Name>.partial            in-progress bytes
//	staging/<dir>/<Upload-Name>.partial.meta.json  offset/length sidecar
//	held/<dir>/<Upload-Name>                       assembled, hashed bytes
//	index.json                                     every held record, every
//	                                                pending binding, and the
//	                                                bounded evidence log
//
// staging/ is swept at boot (sweepFPPConnectUploadStaging, mirroring
// assets.go's sweepAssetStaging): a partial file left behind by an
// interrupted process is never a partially-usable asset. held/ and
// index.json are never touched by that sweep.

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

	Bound           bool   `json:"bound"`
	Show            string `json:"show,omitempty"`
	LogicalSequence string `json:"logicalSequence,omitempty"`
	UnboundReason   string `json:"unboundReason,omitempty"`
}

// fppConnectPlaylistEvent is evidence not tied to any one held file:
// a POST /api/playlist/{name} whose name matched no show, or matched more
// than one (ADR-044 decision 8's unknown and ambiguous cases). Kept as a
// small bounded log (fppConnectMaxEvents) alongside the held records
// rather than invented as a second store, since both exist for the same
// reason: an operator must be able to see what happened without reading
// logs.
type fppConnectPlaylistEvent struct {
	Kind       string    `json:"kind"` // "unknown" or "ambiguous"
	Name       string    `json:"name"`
	Entries    []string  `json:"entries"`
	MatchCount int       `json:"matchCount,omitempty"`
	At         time.Time `json:"at"`
}

// fppConnectMaxEvents bounds fppConnectHeldStore.events: the oldest event
// is dropped once this many are held, so a misbehaving or merely curious
// client posting many unknown playlist names cannot grow this file
// without bound.
const fppConnectMaxEvents = 50

// fppConnectIndex is the whole of fppConnectHeldStore's durable state, and
// exactly what persistLocked/load read and write as one JSON document,
// matching internal/agent/heldcatalog.FileStore's identical "one atomic
// file holds the one durable record" discipline, generalized here to a
// small collection rather than a single record.
type fppConnectIndex struct {
	Records map[string]fppConnectHeldRecord `json:"records"`
	Pending map[string]string               `json:"pending"`
	Events  []fppConnectPlaylistEvent       `json:"events"`
}

// fppConnectChunkWriter is the seam a test substitutes to inject a disk-
// full outcome (ADR-044 decision 4's fourth bound) without actually
// filling a disk. The production implementation, osFPPConnectChunkWriter,
// writes through the real filesystem.
type fppConnectChunkWriter interface {
	WriteAt(path string, offset int64, data []byte) error
}

// osFPPConnectChunkWriter is fppConnectChunkWriter's real implementation:
// open-or-create, then a single positioned write. Offset is always exactly
// the file's current size by the time this is called (WriteChunk's gap
// check enforces that), so this never leaves a hole.
type osFPPConnectChunkWriter struct{}

func (osFPPConnectChunkWriter) WriteAt(path string, offset int64, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteAt(data, offset)
	return err
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

// fppConnectStagingMeta is the sidecar WriteChunk keeps beside a partial
// file: what Upload-Length this attempt declared, how many bytes have
// landed so far, and when the last chunk arrived. Read on every chunk
// after the first to detect a gap or overlap (ADR-044 decision 9); a
// restart never needs to trust anything beyond what is actually on disk,
// since sweepFPPConnectUploadStaging discards every partial (and its
// sidecar) at boot rather than trying to resume across a restart.
type fppConnectStagingMeta struct {
	UploadLength  int64     `json:"uploadLength"`
	BytesReceived int64     `json:"bytesReceived"`
	LastChunkAt   time.Time `json:"lastChunkAt"`
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
	// directory, writing, hashing, or renaming into place); the fragment
	// was discarded and nothing was registered.
	fppConnectChunkWriteFailed
)

// fppConnectHeldStore is the whole of FC2's server-side state: in-flight
// chunk bookkeeping lives on disk as sidecars (fppConnectStagingMeta), but
// every completed file's record, every pending (not-yet-held) binding, and
// the bounded evidence log live here, guarded by one mutex and persisted
// as one atomic JSON document (fppConnectIndex) on every mutation.
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

	records map[string]fppConnectHeldRecord // key: dir + "/" + name
	pending map[string]string               // file name -> show name
	events  []fppConnectPlaylistEvent

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
		pending:  map[string]string{},
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
// reads its backlog here rather than waiting on SetOnHeld alone.
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

// Events returns a copy of the bounded evidence log (unknown and
// ambiguous playlist posts), oldest first.
func (s *fppConnectHeldStore) Events() []fppConnectPlaylistEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fppConnectPlaylistEvent, len(s.events))
	copy(out, s.events)
	return out
}

// MainPlaylistFor returns one fppConnectPlaylistEntry per held record
// currently bound to showName, sorted by file name, for GET
// /api/playlist/{name}. Returns nil (never a non-nil empty slice) when
// nothing is bound; the HTTP handler is what turns that into a JSON "[]"
// rather than "null".
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

// stagingDir, stagingFilePath, stagingMetaPath, heldFileDir, heldFilePath
// and indexPath are this store's own path layout, all rooted at
// s.assetDir; see this file's package doc comment for the full tree.
func (s *fppConnectHeldStore) stagingDir(dir string) string {
	return filepath.Join(s.assetDir, fppConnectUploadStateSubdir, "staging", dir)
}

func (s *fppConnectHeldStore) stagingFilePath(dir, name string) string {
	return filepath.Join(s.stagingDir(dir), name+".partial")
}

func (s *fppConnectHeldStore) stagingMetaPath(dir, name string) string {
	return filepath.Join(s.stagingDir(dir), name+".partial.meta.json")
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

// readStagingMeta reads and decodes the sidecar at path. A missing or
// undecodable sidecar reports ok=false: WriteChunk treats that exactly
// like a genuine gap, since without it there is no trustworthy "bytes
// received so far" to compare offset against.
func (s *fppConnectHeldStore) readStagingMeta(path string) (meta fppConnectStagingMeta, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fppConnectStagingMeta{}, false
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return fppConnectStagingMeta{}, false
	}
	return meta, true
}

// writeStagingMeta persists meta at path, best-effort: a failure here
// does not undo the chunk bytes already written, and is logged rather than
// failing the request, since the sidecar is recovery bookkeeping, not the
// upload's own content.
func (s *fppConnectHeldStore) writeStagingMeta(path string, meta fppConnectStagingMeta) {
	data, err := json.Marshal(meta)
	if err != nil {
		s.logger.Warn("fppconnect: failed to encode upload staging sidecar", "path", path, "error", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.logger.Warn("fppconnect: failed to write upload staging sidecar", "path", path, "error", err)
	}
}

// discardFragment removes both a staging file and its sidecar, ignoring a
// not-exist error: every refusal path in WriteChunk calls this so "the
// fragment is discarded" (ADR-044 decision 9) is one code path, not
// several copies that could drift apart.
func (s *fppConnectHeldStore) discardFragment(dir, name string) {
	_ = os.Remove(s.stagingFilePath(dir, name))
	_ = os.Remove(s.stagingMetaPath(dir, name))
}

// WriteChunk applies one PATCH /api/file/{dir} chunk: offset-0 discard,
// gap detection, the per-file and asset-directory byte caps, the actual
// write (classifying ENOSPC distinctly), and, once accumulated bytes equal
// uploadLength, hashing, renaming into the held area, and binding
// resolution. dir is assumed already validated against
// fppConnectAllowedDirs by the caller (fppconnectupload.go's
// handleFileRoute); name is assumed already validated by
// fppConnectValidPlaylistName. activeShow is the view's ActiveShow method,
// threaded through as a function rather than the whole view so this store
// stays independent of fppConnectView (avoiding an import cycle risk and
// keeping this file's own test surface small).
func (s *fppConnectHeldStore) WriteChunk(dir, name string, offset, uploadLength int64, chunk []byte, maxFileBytes, maxAssetDirBytes int64, at time.Time, activeShow func() (name string, known bool, everSet bool)) (outcome fppConnectChunkOutcome, reason string, rec fppConnectHeldRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stagingPath := s.stagingFilePath(dir, name)
	metaPath := s.stagingMetaPath(dir, name)

	var bytesReceived int64
	if offset == 0 {
		// Offset 0 always starts fresh: discard any stale fragment of the
		// same name before anything else (ADR-044 decision 9's "the first
		// chunk" reasoning starts here).
		s.discardFragment(dir, name)
	} else {
		meta, ok := s.readStagingMeta(metaPath)
		if !ok || offset != meta.BytesReceived {
			s.discardFragment(dir, name)
			got := int64(-1)
			if ok {
				got = meta.BytesReceived
			}
			return fppConnectChunkGap, fmt.Sprintf(
				"upload offset %d does not match %d bytes already received for %q; the fragment was discarded",
				offset, got, name), fppConnectHeldRecord{}
		}
		bytesReceived = meta.BytesReceived
	}

	if offset == 0 {
		if uploadLength > maxFileBytes {
			return fppConnectChunkTooLarge, fmt.Sprintf(
				"upload length %d bytes exceeds the per-file cap of %d bytes", uploadLength, maxFileBytes,
			), fppConnectHeldRecord{}
		}
		existingTotal, err := fppConnectDirBytes(s.assetDir)
		if err != nil {
			s.logger.Warn("fppconnect: failed to measure asset directory size; refusing this upload rather than risk exceeding the cap silently", "error", err)
			return fppConnectChunkDirFull, fmt.Sprintf(
				"could not measure the asset directory's current size against the %d byte cap: %v", maxAssetDirBytes, err,
			), fppConnectHeldRecord{}
		}
		if existingTotal+uploadLength > maxAssetDirBytes {
			return fppConnectChunkDirFull, fmt.Sprintf(
				"accepting %d declared bytes would bring the asset directory to more than its %d byte cap (%d bytes already used)",
				uploadLength, maxAssetDirBytes, existingTotal,
			), fppConnectHeldRecord{}
		}
	}

	newTotal := bytesReceived + int64(len(chunk))
	if newTotal > uploadLength || newTotal > maxFileBytes {
		s.discardFragment(dir, name)
		return fppConnectChunkTooLarge, fmt.Sprintf(
			"accumulated upload of %d bytes would exceed the declared length of %d bytes or the per-file cap of %d bytes",
			newTotal, uploadLength, maxFileBytes,
		), fppConnectHeldRecord{}
	}

	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o755); err != nil {
		return fppConnectChunkWriteFailed, fmt.Sprintf("creating staging directory for %q: %v", name, err), fppConnectHeldRecord{}
	}
	if err := s.writer.WriteAt(stagingPath, offset, chunk); err != nil {
		if fppConnectIsDiskFull(err) {
			s.discardFragment(dir, name)
			return fppConnectChunkDiskFull, fmt.Sprintf("the disk is full while writing %q: %v", name, err), fppConnectHeldRecord{}
		}
		s.discardFragment(dir, name)
		return fppConnectChunkWriteFailed, fmt.Sprintf("writing a chunk for %q: %v", name, err), fppConnectHeldRecord{}
	}

	bytesReceived = newTotal
	s.writeStagingMeta(metaPath, fppConnectStagingMeta{UploadLength: uploadLength, BytesReceived: bytesReceived, LastChunkAt: at})

	if bytesReceived < uploadLength {
		return fppConnectChunkAccepted, "", fppConnectHeldRecord{}
	}

	hash, err := hashFile(stagingPath)
	if err != nil {
		s.discardFragment(dir, name)
		return fppConnectChunkWriteFailed, fmt.Sprintf("hashing completed upload %q: %v", name, err), fppConnectHeldRecord{}
	}

	heldPath := s.heldFilePath(dir, name)
	if err := os.MkdirAll(s.heldFileDir(dir), 0o755); err != nil {
		s.discardFragment(dir, name)
		return fppConnectChunkWriteFailed, fmt.Sprintf("creating held directory for %q: %v", name, err), fppConnectHeldRecord{}
	}
	if err := os.Rename(stagingPath, heldPath); err != nil {
		s.discardFragment(dir, name)
		return fppConnectChunkWriteFailed, fmt.Sprintf("finalizing held file %q: %v", name, err), fppConnectHeldRecord{}
	}
	_ = os.Remove(metaPath)

	rec = s.completeLocked(dir, name, bytesReceived, hash, at, activeShow)
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
// show, in ADR-039 decision 5's tri-state (bound if known, else unbound
// with a reason distinguishing "pushed null" from "never pushed").
func (s *fppConnectHeldStore) completeLocked(dir, name string, sizeBytes int64, hash string, at time.Time, activeShow func() (name string, known bool, everSet bool)) fppConnectHeldRecord {
	key := dir + "/" + name
	prev, hadPrev := s.records[key]

	rec := fppConnectHeldRecord{Dir: dir, Name: name, SizeBytes: sizeBytes, ContentHash: hash, ReceivedAt: at}

	if show, ok := s.pending[name]; ok {
		rec.Bound = true
		rec.Show = show
		rec.LogicalSequence = fppConnectStem(name)
		delete(s.pending, name)
	} else if hadPrev && prev.Bound {
		rec.Bound = true
		rec.Show = prev.Show
		rec.LogicalSequence = prev.LogicalSequence
	} else {
		showName, known, everSet := activeShow()
		switch {
		case known:
			rec.Bound = true
			rec.Show = showName
			rec.LogicalSequence = fppConnectStem(name)
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
// that): every held record, in any directory, whose Name is in fileNames
// is bound to showName; a name with no held match yet is remembered in
// s.pending so a file completing afterwards binds on completion (ADR-044
// decision 8). Idempotent: binding an already-bound record to the same
// show sets the same fields again, and re-posting the same fileNames a
// second time produces the same records, not new ones (RES-003 section
// 10.6's "up to twice per target" requirement).
func (s *fppConnectHeldStore) BindShow(showName string, fileNames []string, at time.Time) {
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
			rec.Bound = true
			rec.Show = showName
			rec.LogicalSequence = fppConnectStem(fname)
			rec.UnboundReason = ""
			s.records[key] = rec
			matched = true
			if s.onHeld != nil {
				s.onHeld(rec)
			}
		}
		if matched {
			delete(s.pending, fname)
		} else {
			s.pending[fname] = showName
		}
	}

	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a playlist bind", "show", showName, "error", err)
	}
}

// appendEventLocked appends ev to s.events, dropping the oldest entries
// once fppConnectMaxEvents is exceeded. s.mu already held.
func (s *fppConnectHeldStore) appendEventLocked(ev fppConnectPlaylistEvent) {
	s.events = append(s.events, ev)
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
	s.appendEventLocked(fppConnectPlaylistEvent{Kind: "unknown", Name: name, Entries: entries, At: at})
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
	s.appendEventLocked(fppConnectPlaylistEvent{Kind: "ambiguous", Name: name, Entries: entries, MatchCount: matchCount, At: at})
	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after an ambiguous playlist post", "name", name, "error", err)
	}
}

// persistLocked writes the whole index as one file, temp-then-rename, s.mu
// already held. Matches internal/agent/heldcatalog.FileStore.Save's exact
// discipline.
func (s *fppConnectHeldStore) persistLocked() error {
	idx := fppConnectIndex{Records: s.records, Pending: s.pending, Events: s.events}
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
	var idx fppConnectIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return fmt.Errorf("fppconnect: decode held upload index: %w", err)
	}
	if idx.Records != nil {
		s.records = idx.Records
	}
	if idx.Pending != nil {
		s.pending = idx.Pending
	}
	s.events = idx.Events
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
// memory of. held/ and index.json, siblings of staging/, are untouched:
// this never removes an already-completed held file or its record.
// Missing dir or missing staging/ is not an error.
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
