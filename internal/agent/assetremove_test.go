package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAssetRemoveOperationHappyPathDeletesVerifiedFile(t *testing.T) {
	content := []byte("bytes this test expects asset.remove to actually delete")
	hash := sha256Hash(content)

	dir := t.TempDir()
	path := filepath.Join(dir, "Opening.fseq")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("seeding held file: %v", err)
	}

	op := assetRemoveOperation{dir: dir}
	clock := &fakeClock{t: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}

	result, err := op.run(context.Background(), map[string]any{"contentHash": hash, "filename": "Opening.fseq"}, clock.now)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !result.Confirmed {
		t.Fatalf("Confirmed = false, want true; result = %+v", result)
	}
	if result.Signal != "node.asset.removed" {
		t.Fatalf("Signal = %q, want %q", result.Signal, "node.asset.removed")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("file still exists after a confirmed remove, stat err = %v", statErr)
	}
}

func TestAssetRemoveOperationAlreadyAbsentIsIdempotentSuccess(t *testing.T) {
	dir := t.TempDir()
	op := assetRemoveOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	result, err := op.run(context.Background(), map[string]any{"contentHash": "sha256:gone", "filename": "Never.fseq"}, clock.now)
	if err != nil {
		t.Fatalf("run() error = %v, want nil: an already-absent file is a success, not a failure", err)
	}
	if !result.Confirmed || result.Signal != "node.asset.removed" {
		t.Fatalf("result = %+v, want Confirmed=true Signal=node.asset.removed for an already-absent file", result)
	}
}

// TestAssetRemoveOperationHashMismatchRefusesAndKeepsFile proves the safety
// property this operation exists to guarantee: it never deletes by filename
// alone. A file at the requested filename whose on-disk content does not
// match the requested contentHash (a different asset sharing the runtime
// filename, or a fetch mid-flight) is left untouched and the call fails.
func TestAssetRemoveOperationHashMismatchRefusesAndKeepsFile(t *testing.T) {
	content := []byte("this content does not match the hash the command names")

	dir := t.TempDir()
	path := filepath.Join(dir, "Opening.fseq")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("seeding held file: %v", err)
	}

	op := assetRemoveOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	_, err := op.run(context.Background(), map[string]any{"contentHash": "sha256:not-the-real-hash", "filename": "Opening.fseq"}, clock.now)
	if err == nil {
		t.Fatal("run() error = nil, want a refusal on a content hash mismatch")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was removed despite a hash mismatch: stat err = %v", statErr)
	}
}

func TestAssetRemoveOperationRejectsUnsafeFilename(t *testing.T) {
	op := assetRemoveOperation{dir: t.TempDir()}
	clock := &fakeClock{t: time.Now()}

	_, err := op.run(context.Background(), map[string]any{"contentHash": "sha256:aaa", "filename": "../escape.fseq"}, clock.now)
	if err == nil {
		t.Fatal("run() error = nil, want a refusal for an unsafe filename")
	}
}

func TestAssetRemoveOperationMissingParams(t *testing.T) {
	op := assetRemoveOperation{dir: t.TempDir()}
	clock := &fakeClock{t: time.Now()}

	if _, err := op.run(context.Background(), map[string]any{"filename": "Opening.fseq"}, clock.now); err == nil {
		t.Fatal("run() error = nil, want a refusal for a missing contentHash param")
	}
	if _, err := op.run(context.Background(), map[string]any{"contentHash": "sha256:aaa"}, clock.now); err == nil {
		t.Fatal("run() error = nil, want a refusal for a missing filename param")
	}
}
