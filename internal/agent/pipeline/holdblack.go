package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// heldBlackFileName is the file recording which surfaces are currently held
// black by render.surface.blackout, alongside assignmentFileName in the
// same state directory (build item 3: "persists beside the saved
// assignments"). Kept separate from [Assignment] itself rather than a field
// on it: a surface can be held black with no assignment at all (build item
// 1), and every one of Assignment's own content fields is required together
// to build a real or idle pipeline spec, so recording the flag there would
// force fabricating a fake assignment just to carry it.
const heldBlackFileName = "held-black.json"

// HoldBlackStore persists the set of surface IDs currently held black under
// dir (the node's asset directory), mirroring [AssignmentStore]'s own
// atomic-file discipline.
type HoldBlackStore struct {
	dir string
}

// NewHoldBlackStore builds a store rooted at assetDir.
func NewHoldBlackStore(assetDir string) *HoldBlackStore {
	return &HoldBlackStore{dir: assetDir}
}

func (h *HoldBlackStore) path() string {
	return filepath.Join(h.dir, assignmentStateDir, heldBlackFileName)
}

// Load returns the set of surface IDs currently held black, or an empty
// (non-nil) set if the file does not exist yet — a fresh node, or one that
// has never received a render.surface.blackout. Any other read or decode
// error is returned, matching [AssignmentStore.Load]'s "a corrupt state
// file is reported, never silently treated as empty" rule.
func (h *HoldBlackStore) Load() (map[string]bool, error) {
	raw, err := os.ReadFile(h.path())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("pipeline: reading held-black state: %w", err)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("pipeline: decoding held-black state: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// Set records surfaceID's held-black flag and persists the result. Not safe
// to call concurrently from multiple goroutines against the same store,
// matching [AssignmentStore.Upsert]'s identical read-modify-write contract.
//
// An existing file this call cannot read starts the write from an empty
// set rather than failing: Set is how a node recovers from exactly a
// corrupt or unreadable file, one surface at a time (a caller such as
// [renderOperations] already defaults every surface with no explicit entry
// to held black in memory for this process's lifetime — see its own
// holdBlackDefaultAll), and refusing every future write while the old file
// stays unreadable would make that recovery permanently impossible. [Load]
// itself keeps reporting the real error to a caller that needs to know
// honestly, such as a fresh process reading this store at startup.
func (h *HoldBlackStore) Set(surfaceID string, held bool) error {
	existing, err := h.Load()
	if err != nil {
		existing = map[string]bool{}
	}
	if held {
		existing[surfaceID] = true
	} else {
		delete(existing, surfaceID)
	}
	return h.save(existing)
}

// save replaces the entire persisted held-black set, atomically (temp file
// plus rename), matching [AssignmentStore.Save]'s identical discipline so a
// crash mid-write never leaves a truncated state file behind.
func (h *HoldBlackStore) save(set map[string]bool) error {
	stateDir := filepath.Join(h.dir, assignmentStateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("pipeline: creating held-black state directory: %w", err)
	}

	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	raw, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return fmt.Errorf("pipeline: encoding held-black state: %w", err)
	}

	tmp, err := os.CreateTemp(stateDir, heldBlackFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("pipeline: creating temp held-black state file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: writing temp held-black state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: closing temp held-black state file: %w", err)
	}
	if err := os.Rename(tmpPath, h.path()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("pipeline: renaming held-black state file into place: %w", err)
	}
	return nil
}
