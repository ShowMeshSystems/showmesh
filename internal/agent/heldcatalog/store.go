// Package heldcatalog is the node agent's own storage for TRACK-H-H3-SPEC.md
// section 4's "held Cue catalog": the one resolved catalog a node currently
// holds, plus the authorization tuple (Show, generation, catalog revision)
// it was deployed under. A node holds exactly one catalog at a time (H3 spec
// section 4: "there is no partial state: a catalog is one object and a node
// either holds this one or it does not"), which is why this package follows
// internal/agent/audio.FileSessionStore's shape (Save/Load/Delete on one
// record) rather than internal/agent/pipeline.AssignmentStore's shape (a
// keyed collection): a held catalog has no key to collect by, there is only
// ever the one.
package heldcatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// stateSubdir is where [FileStore] keeps its one file, rooted at the
// agent's asset directory — matching internal/agent/audio.
// sessionStateSubdir's and internal/agent/pipeline.assignmentStateDir's
// identical convention of rooting local durable state under AssetDir
// rather than adding a second configured directory.
const stateSubdir = "cue-catalog-state"

// fileName is the single file the held catalog is stored in.
const fileName = "catalog.json"

// HeldCatalog is the node's one persisted record: a resolved catalog
// (TRACK-H-H3-SPEC.md section 3), the authorization tuple it carries
// (section 5's Show and Generation, plus the catalog's own Revision — a
// held catalog has no Cue of its own to carry a CueID/CueRevision for), and
// when the node accepted it. Node is carried for completeness and so a
// held record is self-describing on disk, even though a node only ever
// loads its own.
type HeldCatalog struct {
	Show       string             `json:"show"`
	Generation int64              `json:"generation"`
	Node       string             `json:"node"`
	Revision   string             `json:"revision"`
	Entries    []cuecatalog.Entry `json:"entries"`
	ReceivedAt time.Time          `json:"receivedAt"`
}

// KnownCueRevisions projects h's entries into the
// map[cueId]cueRevision shape [pkg/cueauth.HeldState.KnownCueRevisions]
// needs, so a caller checking a Cue activation attempt against this held
// catalog never has to walk h.Entries a second, independently-written way.
func (h HeldCatalog) KnownCueRevisions() map[string]int64 {
	out := make(map[string]int64, len(h.Entries))
	for _, e := range h.Entries {
		out[e.CueID] = e.CueRevision
	}
	return out
}

// FileStore is a held catalog store backed by one JSON file, written
// atomically (temp file + rename, matching internal/agent/audio.
// FileSessionStore.Save and internal/agent/pipeline.AssignmentStore.Save's
// identical stage-then-rename discipline).
type FileStore struct {
	dir string
}

// NewFileStore roots a store under assetDir.
func NewFileStore(assetDir string) *FileStore {
	return &FileStore{dir: filepath.Join(assetDir, stateSubdir)}
}

func (f *FileStore) path() string {
	return filepath.Join(f.dir, fileName)
}

// Save atomically replaces the persisted held catalog with rec.
func (f *FileStore) Save(rec HeldCatalog) error {
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		return fmt.Errorf("heldcatalog: create state directory: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("heldcatalog: encode held catalog: %w", err)
	}
	target := f.path()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("heldcatalog: write held catalog: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("heldcatalog: commit held catalog: %w", err)
	}
	return nil
}

// Load returns the persisted held catalog, or (zero, false, nil) if no
// catalog has ever been deployed to this node — a fresh node, matching H3
// spec section 7's "a node with no held catalog at all resumes nothing".
//
// A corrupt file (unreadable, or present but not decodable JSON) is
// reported as an error, never silently treated as "no catalog held":
// collapsing the two would let a disk-corruption event masquerade as an
// honest fresh-node state, which TRACK-H-H3-SPEC.md's whole point is to
// make impossible to do quietly — matching
// internal/agent/pipeline.AssignmentStore.Load's identical "corrupt state
// is reported, never treated as absent" rule for the render assignment
// store this package sits beside.
func (f *FileStore) Load() (HeldCatalog, bool, error) {
	data, err := os.ReadFile(f.path())
	if err != nil {
		if os.IsNotExist(err) {
			return HeldCatalog{}, false, nil
		}
		return HeldCatalog{}, false, fmt.Errorf("heldcatalog: read held catalog: %w", err)
	}
	var rec HeldCatalog
	if err := json.Unmarshal(data, &rec); err != nil {
		return HeldCatalog{}, false, fmt.Errorf("heldcatalog: decode held catalog: %w", err)
	}
	return rec, true, nil
}

// Delete removes the persisted held catalog, if any. Deleting an
// already-absent record is not an error.
func (f *FileStore) Delete() error {
	if err := os.Remove(f.path()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("heldcatalog: delete held catalog: %w", err)
	}
	return nil
}
