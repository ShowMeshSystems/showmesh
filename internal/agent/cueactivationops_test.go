package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file covers verifiedHashCache and hashFileCached: cue.activate's
// own assetPresent check must not re-hash a large asset whole on every
// single activation (the defect a 66 MB WAV cue file's own ~2.3s hash cost
// exposed), but a genuinely missing or altered file must still hash, and
// still fail, exactly as before this cache existed.

// TestAssetPresentCachedHitNeverReopensTheFile proves a second assetPresent
// call for an unchanged file answers from the cache rather than reopening
// it: the file's on-disk bytes are corrupted between the two calls WITHOUT
// touching its size or modification time, so a re-hash would see the
// corrupted content and disagree with the expected hash.
func TestAssetPresentCachedHitNeverReopensTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cue-song.wav")
	original := []byte("original sixteen")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	wantHash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}

	op := &cueActivationOperation{assetDir: dir, hashCache: newVerifiedHashCache()}
	if !op.assetPresent("cue-song.wav", []string{wantHash}) {
		t.Fatal("first assetPresent call = false, want true (priming the cache)")
	}

	// Same size, different content, and the ORIGINAL modification time
	// restored: a re-hash of these bytes would produce a different hash
	// than wantHash, so a cache hit is the only way the next call can
	// still agree.
	corrupted := []byte("corruptedbytes16")
	if len(corrupted) != len(original) {
		t.Fatalf("test fixture bug: corrupted length %d != original length %d", len(corrupted), len(original))
	}
	if err := os.WriteFile(path, corrupted, 0o644); err != nil {
		t.Fatalf("overwrite fixture: %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore modtime: %v", err)
	}

	if !op.assetPresent("cue-song.wav", []string{wantHash}) {
		t.Fatal("second assetPresent call = false, want true: it should have answered from the cache, not re-hashed the corrupted bytes")
	}
}

// TestAssetPresentRehashesOnSizeOrModTimeChange proves the cache miss path:
// a file whose size or modification time has changed since it was cached
// is re-hashed, so a genuinely altered asset is correctly reported as not
// matching its previously-declared hash.
func TestAssetPresentRehashesOnSizeOrModTimeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cue-song.wav")
	if err := os.WriteFile(path, []byte("version one"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	firstHash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}

	op := &cueActivationOperation{assetDir: dir, hashCache: newVerifiedHashCache()}
	if !op.assetPresent("cue-song.wav", []string{firstHash}) {
		t.Fatal("first assetPresent call = false, want true (priming the cache)")
	}

	// A different size AND a distinctly later modification time: a stale
	// cache entry would keep answering firstHash, which no longer matches
	// declared content on this Cue's next supersession.
	if err := os.WriteFile(path, []byte("version two has different bytes"), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("bump modtime: %v", err)
	}
	secondHash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hash rewritten fixture: %v", err)
	}
	if secondHash == firstHash {
		t.Fatalf("test fixture bug: secondHash == firstHash, the two versions must hash differently")
	}

	if op.assetPresent("cue-song.wav", []string{firstHash}) {
		t.Fatal("assetPresent still matched the stale firstHash after the file changed: it must have re-hashed, not answered from a stale cache entry")
	}
	if !op.assetPresent("cue-song.wav", []string{secondHash}) {
		t.Fatal("assetPresent did not match the rewritten file's own current hash")
	}
}

// TestAssetPresentMissingOrAlteredFileStillFailsWithCache proves the cache
// never masks a genuinely missing file: an absent file has nothing to
// cache and is reported not present exactly as before this cache existed.
func TestAssetPresentMissingOrAlteredFileStillFailsWithCache(t *testing.T) {
	dir := t.TempDir()
	op := &cueActivationOperation{assetDir: dir, hashCache: newVerifiedHashCache()}
	if op.assetPresent("never-uploaded.wav", []string{"sha256:" + string(make([]byte, 64))}) {
		t.Fatal("assetPresent = true for a file that was never written, want false")
	}
}
