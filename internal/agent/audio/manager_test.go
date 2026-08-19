package audio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// staticDecoder is a canned [Decoder] reporting every file as a ready,
// two-second audio asset — this package's tests exercise the session
// state machine, not [ProbeAsset]'s own decode classification (that is
// mediaprobe_test.go's job).
type staticDecoder struct{ duration time.Duration }

func (d staticDecoder) Decode(_ context.Context, _ string) DecodeResult {
	return DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: DiscovererEvidence{Ran: true, Duration: d.duration},
	}
}

// writeTestAsset writes content under dir and returns a [pkgaudio.MediaRef]
// whose identity matches it, so [ProbeAsset]'s real identity check
// ([checkIdentity]) passes.
func writeTestAsset(t *testing.T, dir, filename, assetID string, content []byte) pkgaudio.MediaRef {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), content, 0o644); err != nil {
		t.Fatalf("write test asset: %v", err)
	}
	sum := sha256.Sum256(content)
	return pkgaudio.MediaRef{
		AssetID: assetID, ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)), RuntimeFilename: filename,
	}
}

// clock is an injectable, monotonically-advanceable time source shared by
// [Manager], [FakeEngine], and every test in this file.
type clock struct{ t time.Time }

func newClock(start time.Time) *clock { return &clock{t: start} }
func (c *clock) now() time.Time       { return c.t }
func (c *clock) advance(d time.Duration) time.Time {
	c.t = c.t.Add(d)
	return c.t
}

func newTestManager(t *testing.T, c *clock) *Manager {
	t.Helper()
	dir := t.TempDir()
	return NewManager(NewFakeEngine(c.now), NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
}

// failingSessionStore wraps a real [SessionStore] and lets a test force
// the next N Save calls to fail — for finding 10 (a failed persist must
// not report success) and finding 17 (a malformed persisted file must
// not be silently skipped), neither of which is reachable against a
// SessionStore that never fails.
type failingSessionStore struct {
	SessionStore
	mu        sync.Mutex
	failSaves int
	saveErr   error
}

func (f *failingSessionStore) Save(id pkgaudio.SessionID, rec PersistedSession) error {
	f.mu.Lock()
	if f.failSaves > 0 {
		f.failSaves--
		err := f.saveErr
		if err == nil {
			err = errors.New("failingSessionStore: simulated save failure")
		}
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	return f.SessionStore.Save(id, rec)
}

func (f *failingSessionStore) armSaveFailures(n int, err error) {
	f.mu.Lock()
	f.failSaves, f.saveErr = n, err
	f.mu.Unlock()
}
