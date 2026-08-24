package heldcatalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

func TestFileStoreLoadEmptyIsFreshNode(t *testing.T) {
	store := NewFileStore(t.TempDir())
	rec, ok, err := store.Load()
	if err != nil {
		t.Fatalf("Load on a fresh node: %v", err)
	}
	if ok {
		t.Fatalf("Load on a fresh node reported ok=true, want false (no catalog ever deployed)")
	}
	if rec.Show != "" || rec.Generation != 0 || rec.Revision != "" || len(rec.Entries) != 0 {
		t.Fatalf("Load on a fresh node returned a non-zero record: %+v", rec)
	}
}

func TestFileStoreSaveLoadRoundTrip(t *testing.T) {
	store := NewFileStore(t.TempDir())
	want := HeldCatalog{
		Show:       "halloween-2026",
		Generation: 3,
		Node:       "node-01",
		Revision:   "deadbeef",
		Entries: []cuecatalog.Entry{
			{CueID: "cue-a", CueRevision: 1, Outputs: cuecatalog.Outputs{Render: &cuecatalog.RenderOutput{Sequence: "seq-a", AssetHashes: []string{"h1"}}}},
		},
		ReceivedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatalf("Load after Save reported ok=false")
	}
	if got.Show != want.Show || got.Generation != want.Generation || got.Revision != want.Revision || len(got.Entries) != 1 {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestFileStoreSaveReplacesPriorRecord(t *testing.T) {
	store := NewFileStore(t.TempDir())
	if err := store.Save(HeldCatalog{Show: "christmas-2026", Generation: 1, Revision: "aaa"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := store.Save(HeldCatalog{Show: "christmas-2026", Generation: 2, Revision: "bbb"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after two Saves: ok=%v err=%v", ok, err)
	}
	if got.Generation != 2 || got.Revision != "bbb" {
		t.Fatalf("Save did not wholesale-replace the prior record: got %+v", got)
	}
}

func TestFileStoreDeleteIsIdempotent(t *testing.T) {
	store := NewFileStore(t.TempDir())
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete on an absent record: %v", err)
	}
	if err := store.Save(HeldCatalog{Show: "s", Generation: 1, Revision: "r"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := store.Load(); err != nil || ok {
		t.Fatalf("Load after Delete: ok=%v err=%v, want ok=false", ok, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("second Delete on an already-absent record: %v", err)
	}
}

func TestFileStoreLoadCorruptFileIsReportedNotAbsent(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	stateDir := filepath.Join(dir, stateSubdir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, fileName), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, ok, err := store.Load()
	if err == nil {
		t.Fatalf("Load on a corrupt file returned no error; corruption must never be reported as an honest fresh node")
	}
	if ok {
		t.Fatalf("Load on a corrupt file reported ok=true")
	}
}

func TestHeldCatalogKnownCueRevisions(t *testing.T) {
	h := HeldCatalog{Entries: []cuecatalog.Entry{
		{CueID: "cue-a", CueRevision: 1},
		{CueID: "cue-b", CueRevision: 4},
	}}
	got := h.KnownCueRevisions()
	if got["cue-a"] != 1 || got["cue-b"] != 4 || len(got) != 2 {
		t.Fatalf("KnownCueRevisions: got %v", got)
	}
}
