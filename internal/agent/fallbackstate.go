package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the state behind the fallback ingress (ADR-048 decision
// 3): the programs this node holds, its replay fence, its rate limit and
// the record of every answer it gave.

const (
	fallbackStateSubdir    = "fallback-state"
	fallbackProgramsSubdir = "programs"
	fallbackFenceFile      = "executions.jsonl"
)

// heldFallbackProgram is one FPP player's program as this node verified it.
type heldFallbackProgram struct {
	program     fallbackprogram.Program
	executorKey ed25519.PublicKey
	installedAt time.Time
}

// fallbackProgramStore keeps one verified program per FPP player, on
// disk, so a restart keeps them.
type fallbackProgramStore struct {
	dir string

	mu   sync.Mutex
	held map[string]heldFallbackProgram
}

type storedFallbackProgram struct {
	InstalledAt time.Time       `json:"installedAt"`
	Document    json.RawMessage `json:"document"`
}

// newFallbackProgramStore loads every stored program that still verifies
// against coordinatorKey. A stored file is never trusted because this
// process once wrote it. A nil key loads nothing.
func newFallbackProgramStore(assetDir string, coordinatorKey ed25519.PublicKey, logger *slog.Logger) *fallbackProgramStore {
	s := &fallbackProgramStore{
		dir:  filepath.Join(assetDir, fallbackStateSubdir, fallbackProgramsSubdir),
		held: make(map[string]heldFallbackProgram),
	}
	if coordinatorKey == nil {
		return s
	}
	names, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			logger.Warn("could not read a stored fallback program; this node holds no program from that FPP player until the plugin sends it again", "path", name, "error", err)
			continue
		}
		var stored storedFallbackProgram
		if err := json.Unmarshal(data, &stored); err != nil {
			logger.Warn("a stored fallback program is unreadable; this node holds no program from that FPP player until the plugin sends it again", "path", name, "error", err)
			continue
		}
		program, err := fallbackprogram.VerifyDocument(stored.Document, coordinatorKey)
		if err != nil {
			logger.Warn("a stored fallback program no longer verifies; this node holds no program from that FPP player until the plugin sends it again", "path", name, "error", err)
			continue
		}
		s.held[program.FPPInstanceUUID] = newHeldFallbackProgram(program, stored.InstalledAt)
	}
	return s
}

func newHeldFallbackProgram(program fallbackprogram.Program, installedAt time.Time) heldFallbackProgram {
	held := heldFallbackProgram{program: program, installedAt: installedAt}
	if key, err := fallbackprogram.ParseExecutorPublicKey(program.ExecutorPublicKey); err == nil {
		held.executorKey = key
	}
	return held
}

func (s *fallbackProgramStore) get(fppInstanceUUID string) (heldFallbackProgram, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.held[fppInstanceUUID]
	return held, ok
}

// errFallbackProgramSuperseded means a newer copy is already held.
var errFallbackProgramSuperseded = errors.New("a newer fallback program is already held")

// install persists an already verified program and makes it the held one,
// unless a newer copy is held. The file name is a hash of the FPP instance
// UUID, so no caller-chosen text ever reaches the file system.
func (s *fallbackProgramStore) install(program fallbackprogram.Program, document []byte, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held, ok := s.held[program.FPPInstanceUUID]; ok && program.CompiledAt.Before(held.program.CompiledAt) {
		return errFallbackProgramSuperseded
	}
	if err := mkdirAllSynced(s.dir); err != nil {
		return fmt.Errorf("create fallback program directory: %w", err)
	}
	data, err := json.Marshal(storedFallbackProgram{InstalledAt: now, Document: document})
	if err != nil {
		return fmt.Errorf("encode fallback program: %w", err)
	}
	sum := sha256.Sum256([]byte(program.FPPInstanceUUID))
	target := filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
	if err := writeFileSynced(target+".tmp", data); err != nil {
		return fmt.Errorf("write fallback program: %w", err)
	}
	if err := os.Rename(target+".tmp", target); err != nil {
		return fmt.Errorf("commit fallback program: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("sync fallback program directory: %w", err)
	}
	s.held[program.FPPInstanceUUID] = newHeldFallbackProgram(program, now)
	return nil
}

func (s *fallbackProgramStore) snapshot() []mqttproto.FallbackHeldProgram {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mqttproto.FallbackHeldProgram, 0, len(s.held))
	for _, held := range s.held {
		out = append(out, mqttproto.FallbackHeldProgram{
			FPPInstanceUUID: held.program.FPPInstanceUUID, PackageID: held.program.PackageID,
			Revision: held.program.Revision, ExpiresAt: held.program.ExpiresAt,
			InstalledAt: held.installedAt, ExecutorEnrolled: held.executorKey != nil,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FPPInstanceUUID < out[j].FPPInstanceUUID })
	return out
}

// syncDir makes a file created or renamed inside dir survive a power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// mkdirAllSynced creates dir and any missing parents, and syncs the
// directory each new one was created in.
func mkdirAllSynced(dir string) error {
	var created []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil || filepath.Dir(d) == d {
			break
		}
		created = append(created, d)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range created {
		if err := syncDir(filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// fallbackFenceRetention is how long past its program's expiry an
// execution id is remembered. No request can be accepted under an expired
// program, so the margin only covers this node's clock being set back.
const fallbackFenceRetention = 24 * time.Hour

// fallbackFenceOutcomeUnknown is the first outcome reported for an
// execution this node recorded and then restarted before finishing.
const fallbackFenceOutcomeUnknown = "unknown"

type fallbackFenceEntry struct {
	ExecutionID string    `json:"executionId"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Outcome     string    `json:"outcome,omitempty"`
}

// fallbackFence is the persistent record of every execution id this node
// has processed. An id is written and synced to disk before anything is
// applied, so a restart can never make this node apply it twice.
type fallbackFence struct {
	path string

	mu      sync.Mutex
	entries map[string]fallbackFenceEntry
	// size is the length of the file's known-good content. Anything past
	// it is the torn tail of a failed write and is cut off before the next.
	size int64
	// writeFailed is true while the last write to the file failed.
	writeFailed bool
}

// openFallbackFence loads the fence, dropping entries past retention and
// rewriting the file without them. A final line with no newline is the
// torn tail of an interrupted write and is dropped: its request was never
// applied. Any other unreadable line fails the open, because a damaged
// record cannot say which requests this node already ran.
func openFallbackFence(assetDir string, now time.Time) (*fallbackFence, error) {
	f := &fallbackFence{
		path:    filepath.Join(assetDir, fallbackStateSubdir, fallbackFenceFile),
		entries: make(map[string]fallbackFenceEntry),
	}
	if err := mkdirAllSynced(filepath.Dir(f.path)); err != nil {
		return nil, fmt.Errorf("create fallback state directory: %w", err)
	}
	data, err := os.ReadFile(f.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read fallback execution record: %w", err)
	}
	for lineNumber := 1; len(data) > 0; lineNumber++ {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		var entry fallbackFenceEntry
		if err := json.Unmarshal(data[:end], &entry); err != nil || entry.ExecutionID == "" {
			return nil, fmt.Errorf("fallback execution record %s is damaged at line %d", f.path, lineNumber)
		}
		if prior, ok := f.entries[entry.ExecutionID]; ok && entry.Outcome == "" {
			entry.Outcome = prior.Outcome
		}
		f.entries[entry.ExecutionID] = entry
		data = data[end+1:]
	}
	for id, entry := range f.entries {
		if now.After(entry.ExpiresAt.Add(fallbackFenceRetention)) {
			delete(f.entries, id)
		}
	}
	if err := f.rewrite(); err != nil {
		return nil, err
	}
	return f, nil
}

// rewrite replaces the file with exactly the entries in memory and syncs
// its directory, so the file exists durably before the first id is written.
func (f *fallbackFence) rewrite() error {
	var data []byte
	for _, entry := range f.entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode fallback execution record: %w", err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := writeFileSynced(f.path+".tmp", data); err != nil {
		return fmt.Errorf("write fallback execution record: %w", err)
	}
	if err := os.Rename(f.path+".tmp", f.path); err != nil {
		return fmt.Errorf("commit fallback execution record: %w", err)
	}
	if err := syncDir(filepath.Dir(f.path)); err != nil {
		return fmt.Errorf("sync fallback state directory: %w", err)
	}
	f.size = int64(len(data))
	return nil
}

// appendEntry writes one whole line at the end of the known-good content
// and syncs it. A torn tail left by an earlier failed write is cut off
// first, so a line never joins onto a partial one.
func (f *fallbackFence) appendEntry(entry fallbackFenceEntry) (err error) {
	defer func() { f.writeFailed = err != nil }()
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	file, err := os.OpenFile(f.path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < f.size {
		return fmt.Errorf("fallback execution record %s is shorter than what this node wrote", f.path)
	}
	if info.Size() > f.size {
		if err := file.Truncate(f.size); err != nil {
			return err
		}
	}
	if err := fallbackFenceWrite(file, line, f.size); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	f.size += int64(len(line))
	return nil
}

// fallbackFenceWrite is the one write appendEntry makes. A test replaces
// it to leave a partial line behind, as a full disk or a crash would.
var fallbackFenceWrite = func(file *os.File, line []byte, offset int64) error {
	_, err := file.WriteAt(line, offset)
	return err
}

// failed reports whether the last write to the file failed.
func (f *fallbackFence) failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeFailed
}

// claim records executionID as processed. replayed is true, with the
// first outcome, when it was already recorded. An error means the id
// could not be made durable, and the caller must apply nothing.
func (f *fallbackFence) claim(executionID string, expiresAt time.Time) (firstOutcome string, replayed bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, ok := f.entries[executionID]; ok {
		if prior.Outcome == "" {
			return fallbackFenceOutcomeUnknown, true, nil
		}
		return prior.Outcome, true, nil
	}
	entry := fallbackFenceEntry{ExecutionID: executionID, ExpiresAt: expiresAt}
	if err := f.appendEntry(entry); err != nil {
		return "", false, err
	}
	f.entries[executionID] = entry
	return "", false, nil
}

// finish records what a claimed execution answered. Failing to persist it
// only costs a later replay its first outcome, so it is not an error the
// caller acts on.
func (f *fallbackFence) finish(executionID, outcome string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[executionID]
	if !ok {
		return nil
	}
	entry.Outcome = outcome
	f.entries[executionID] = entry
	return f.appendEntry(entry)
}

// fallbackRateLimiter allows limit requests per caller address over a
// rolling minute.
type fallbackRateLimiter struct {
	limit int

	mu   sync.Mutex
	seen map[string][]time.Time
}

func newFallbackRateLimiter(limit int) *fallbackRateLimiter {
	return &fallbackRateLimiter{limit: limit, seen: make(map[string][]time.Time)}
}

func (l *fallbackRateLimiter) allow(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	for other, times := range l.seen {
		if len(times) == 0 || !times[len(times)-1].After(cutoff) {
			delete(l.seen, other)
		}
	}
	recent := l.seen[addr][:0]
	for _, t := range l.seen[addr] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.limit {
		l.seen[addr] = recent
		return false
	}
	l.seen[addr] = append(recent, now)
	return true
}

// fallbackDecisionLogSize bounds the decisions a report carries.
const fallbackDecisionLogSize = 50

// fallbackDecisionLog remembers the most recent answers and counts all
// of them. A run of rate-limited refusals on one route within a minute
// is one entry with a count, so a flood cannot push everything else out.
type fallbackDecisionLog struct {
	mu        sync.Mutex
	decisions []mqttproto.FallbackDecision
	accepted  int64
	refused   int64
	changed   chan struct{}
}

func newFallbackDecisionLog() *fallbackDecisionLog {
	return &fallbackDecisionLog{changed: make(chan struct{}, 1)}
}

func (l *fallbackDecisionLog) record(d mqttproto.FallbackDecision) {
	l.mu.Lock()
	if d.Accepted {
		l.accepted++
	} else {
		l.refused++
	}
	d.Count = 1
	merged := false
	if d.Outcome == string(fallbackactivation.OutcomeRateLimited) {
		for i := len(l.decisions) - 1; i >= 0; i-- {
			prior := &l.decisions[i]
			if prior.Outcome == d.Outcome && prior.Route == d.Route && d.At.Sub(prior.At) < time.Minute {
				prior.Count++
				merged = true
				break
			}
		}
	}
	if !merged {
		l.decisions = append(l.decisions, d)
		if len(l.decisions) > fallbackDecisionLogSize {
			l.decisions = l.decisions[len(l.decisions)-fallbackDecisionLogSize:]
		}
	}
	l.mu.Unlock()
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

func (l *fallbackDecisionLog) snapshot() (decisions []mqttproto.FallbackDecision, accepted, refused int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]mqttproto.FallbackDecision(nil), l.decisions...), l.accepted, l.refused
}
