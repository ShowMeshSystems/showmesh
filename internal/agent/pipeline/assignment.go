package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// assignmentStateDir is the subdirectory (relative to the node's asset
// directory — config.Config.AssetDir) that holds the persisted set of
// applied surface assignments, mirroring the ".staging" convention
// internal/agent/assets.go already uses for its own reserved subdirectory.
const assignmentStateDir = ".render-state"

// assignmentFileName is the single file every applied assignment is stored
// in, as a JSON array. One file rather than one file per surface: with
// ADR-026's N surfaces per node, a caller re-reading state at boot needs
// "everything that was applied," and one atomically-replaced file is
// simpler to keep consistent than N files that could disagree about which
// ones exist.
const assignmentFileName = "assignments.json"

// Assignment is one surface's last-applied render.surface.apply params,
// persisted verbatim (see RawParams) so a node that restarts with no
// coordinator reachable can resume rendering without needing the
// coordinator to resend anything (build contract ruling 4).
type Assignment struct {
	SurfaceID string          `json:"surfaceId"`
	RawParams json.RawMessage `json:"rawParams"`
	AppliedAt time.Time       `json:"appliedAt"`
}

// AssignmentStore persists and reloads the set of currently-applied surface
// assignments under dir (the node's asset directory).
type AssignmentStore struct {
	dir string
}

// NewAssignmentStore builds a store rooted at assetDir.
func NewAssignmentStore(assetDir string) *AssignmentStore {
	return &AssignmentStore{dir: assetDir}
}

func (a *AssignmentStore) path() string {
	return filepath.Join(a.dir, assignmentStateDir, assignmentFileName)
}

// Load reads every persisted assignment, or returns an empty (non-nil)
// slice if the file does not exist yet (a fresh node, or one that has never
// received a render.surface.apply). Any other read or decode error is
// returned — a corrupt state file is reported, never silently treated as
// "no assignments," so an operator can tell "genuinely nothing assigned"
// apart from "the state file is broken."
func (a *AssignmentStore) Load() ([]Assignment, error) {
	raw, err := os.ReadFile(a.path())
	if err != nil {
		if os.IsNotExist(err) {
			return []Assignment{}, nil
		}
		return nil, fmt.Errorf("pipeline: reading assignment state: %w", err)
	}
	var out []Assignment
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("pipeline: decoding assignment state: %w", err)
	}
	return out, nil
}

// Save replaces the entire persisted assignment set with assignments,
// atomically (temp file + rename, matching internal/agent/assets.go's
// stage-then-rename discipline) so a crash mid-write never leaves a
// truncated or partially-written state file behind.
func (a *AssignmentStore) Save(assignments []Assignment) error {
	stateDir := filepath.Join(a.dir, assignmentStateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("pipeline: creating assignment state directory: %w", err)
	}

	// Stable ordering makes the on-disk file (and every test asserting
	// against it) deterministic regardless of map iteration order upstream.
	sorted := make([]Assignment, len(assignments))
	copy(sorted, assignments)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SurfaceID < sorted[j].SurfaceID })

	raw, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: encoding assignment state: %w", err)
	}

	tmp, err := os.CreateTemp(stateDir, assignmentFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("pipeline: creating temp assignment state file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: writing temp assignment state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: closing temp assignment state file: %w", err)
	}
	if err := os.Rename(tmpPath, a.path()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: renaming assignment state file into place: %w", err)
	}
	return nil
}

// Upsert loads the current set, replaces (or adds) surfaceID's entry, and
// saves the result — the single read-modify-write render.surface.apply
// needs. Not safe to call concurrently from multiple goroutines against the
// same store; internal/agent's operation handlers serialize through the
// command handler's per-key idempotency claim, which is sufficient for this
// seam's one caller.
func (a *AssignmentStore) Upsert(entry Assignment) error {
	existing, err := a.Load()
	if err != nil {
		return err
	}
	out := make([]Assignment, 0, len(existing)+1)
	replaced := false
	for _, e := range existing {
		if e.SurfaceID == entry.SurfaceID {
			out = append(out, entry)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, entry)
	}
	return a.Save(out)
}

// Remove deletes surfaceID's entry, if present, and saves the result — used
// by render.surface.clear so a cleared surface does not resume rendering on
// the next boot.
func (a *AssignmentStore) Remove(surfaceID string) error {
	existing, err := a.Load()
	if err != nil {
		return err
	}
	out := make([]Assignment, 0, len(existing))
	for _, e := range existing {
		if e.SurfaceID != surfaceID {
			out = append(out, e)
		}
	}
	return a.Save(out)
}
