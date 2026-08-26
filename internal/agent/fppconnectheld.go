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

	Bound           bool   `json:"bound"`
	Show            string `json:"show,omitempty"`
	LogicalSequence string `json:"logicalSequence,omitempty"`
	UnboundReason   string `json:"unboundReason,omitempty"`
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

// fppConnectIndex is the whole of fppConnectHeldStore's durable state, and
// exactly what persistLocked/load read and write as one JSON document,
// matching internal/agent/heldcatalog.FileStore's identical "one atomic
// file holds the one durable record" discipline, generalized here to a
// small collection rather than a single record.
type fppConnectIndex struct {
	Records map[string]fppConnectHeldRecord `json:"records"`
	Pending map[string]string               `json:"pending"`
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
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return io.Copy(io.NewOffsetWriter(f, offset), io.LimitReader(r, n))
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

	records map[string]fppConnectHeldRecord // key: dir + "/" + name
	pending map[string]string               // file name -> show name
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
		pending:  map[string]string{},
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
// validated by fppConnectValidPlaylistName. activeShow is the view's
// ActiveShow method, threaded through as a function rather than the whole
// view so this store stays independent of fppConnectView (avoiding an
// import cycle risk and keeping this file's own test surface small).
func (s *fppConnectHeldStore) WriteChunk(dir, name string, offset, uploadLength int64, body io.Reader, n int64, maxFileBytes, maxAssetDirBytes int64, at time.Time, activeShow func() (name string, known bool, everSet bool)) (outcome fppConnectChunkOutcome, reason string, rec fppConnectHeldRecord) {
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

	outcome, reason, rec = s.finishChunkLocked(dir, name, key, stagingPath, inf, bytesReceived, n, uploadLength, written, writeErr, at, activeShow)
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
func (s *fppConnectHeldStore) finishChunkLocked(dir, name, key, stagingPath string, inf *fppConnectInFlight, bytesReceived, n, uploadLength, written int64, writeErr error, at time.Time, activeShow func() (name string, known bool, everSet bool)) (outcome fppConnectChunkOutcome, reason string, rec fppConnectHeldRecord) {
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

	rec = s.completeLocked(dir, name, uploadLength, hashSum, at, activeShow)
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
// finding 8).
func (s *fppConnectHeldStore) completeLocked(dir, name string, sizeBytes int64, hash string, at time.Time, activeShow func() (name string, known bool, everSet bool)) fppConnectHeldRecord {
	key := dir + "/" + name
	prev, hadPrev := s.records[key]

	rec := fppConnectHeldRecord{Dir: dir, Name: name, SizeBytes: sizeBytes, ContentHash: hash, ReceivedAt: at}

	if show, ok := s.pending[name]; ok {
		rec.Bound = true
		rec.Show = show
		rec.LogicalSequence = fppConnectStem(name)
		s.deletePendingLocked(name)
	} else if hadPrev && prev.Bound {
		rec.Bound = true
		rec.Show = prev.Show
		rec.LogicalSequence = prev.LogicalSequence
	} else {
		showName, known, everSet := activeShow()
		switch {
		case known && showName != "":
			rec.Bound = true
			rec.Show = showName
			rec.LogicalSequence = fppConnectStem(name)
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
			s.deletePendingLocked(fname)
		} else {
			s.addPendingLocked(fname, showName)
		}
	}

	if err := s.persistLocked(); err != nil {
		s.logger.Warn("fppconnect: failed to persist held upload index after a playlist bind", "show", showName, "error", err)
	}
}

// addPendingLocked records fname -> showName in s.pending, evicting the
// oldest pending entry first when fppConnectMaxPending would otherwise be
// exceeded (review round 1 finding 6). Updating an already-pending name's
// show does not change its position in the eviction order: it is not a
// fresh entry. s.mu already held.
func (s *fppConnectHeldStore) addPendingLocked(fname, showName string) {
	if _, exists := s.pending[fname]; exists {
		s.pending[fname] = showName
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
	s.pending[fname] = showName
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
