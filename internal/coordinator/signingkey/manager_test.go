package signingkey

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
)

func TestLoadOrGenerate_FirstRunGeneratesAndPersists(t *testing.T) {
	dataDir := t.TempDir()

	mgr, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}
	if len(mgr.PublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("PublicKey() length = %d, want %d", len(mgr.PublicKey()), ed25519.PublicKeySize)
	}

	path := filepath.Join(dataDir, FileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != filePerm {
		t.Fatalf("key file mode = %s, want %s", perm, os.FileMode(filePerm))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		t.Fatalf("key file length = %d, want %d", len(raw), ed25519.PrivateKeySize)
	}
}

func TestLoadOrGenerate_SubsequentRunLoadsSameKey(t *testing.T) {
	dataDir := t.TempDir()

	first, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("first LoadOrGenerate() = %v, want nil", err)
	}

	second, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("second LoadOrGenerate() = %v, want nil", err)
	}

	if !bytes.Equal(first.PublicKey(), second.PublicKey()) {
		t.Fatalf("second run produced a different public key: %x vs %x", first.PublicKey(), second.PublicKey())
	}
}

func TestLoadOrGenerate_CorruptFileReportsErrorNotPanic(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, FileName)
	if err := os.WriteFile(path, []byte("not a real key"), filePerm); err != nil {
		t.Fatalf("seed corrupt key file: %v", err)
	}

	_, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if !errors.Is(err, ErrCorruptKeyFile) {
		t.Fatalf("LoadOrGenerate() = %v, want ErrCorruptKeyFile", err)
	}
}

func TestLoadOrGenerate_SemanticallyCorruptFileReportsErrorNotSign(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, FileName)

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// A structurally valid 64-byte key whose stored public half (bytes
	// 32:64) has been corrupted by a single flipped bit. The length check
	// alone accepts this; only comparing against ed25519.NewKeyFromSeed's
	// own derivation catches it.
	corrupt := append([]byte(nil), priv...)
	corrupt[len(corrupt)-1] ^= 0x01

	if err := os.WriteFile(path, corrupt, filePerm); err != nil {
		t.Fatalf("seed semantically corrupt key file: %v", err)
	}

	_, err = LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if !errors.Is(err, ErrCorruptKeyFile) {
		t.Fatalf("LoadOrGenerate() = %v, want ErrCorruptKeyFile for a 64-byte file with a corrupted public half", err)
	}
}

func TestLoadOrGenerate_ZeroLengthFileReportsError(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, FileName)
	if err := os.WriteFile(path, []byte{}, filePerm); err != nil {
		t.Fatalf("seed zero-length key file: %v", err)
	}

	_, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if !errors.Is(err, ErrCorruptKeyFile) {
		t.Fatalf("LoadOrGenerate() = %v, want ErrCorruptKeyFile for a zero-length key file", err)
	}
}

func TestLoadOrGenerate_ConcurrentFirstStartProducesOneKey(t *testing.T) {
	dataDir := t.TempDir()

	const concurrency = 8
	var wg sync.WaitGroup
	results := make([]*Manager, concurrency)
	errs := make([]error, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: LoadOrGenerate() = %v, want nil", i, err)
		}
	}

	want := results[0].PublicKey()
	for i, mgr := range results {
		if !bytes.Equal(mgr.PublicKey(), want) {
			t.Fatalf("goroutine %d produced public key %x, want %x (every concurrent first start must agree on one key)",
				i, mgr.PublicKey(), want)
		}
	}

	if _, err := os.Stat(filepath.Join(dataDir, FileName+tmpFileSuffix)); !os.IsNotExist(err) {
		t.Fatalf("leftover temporary key file after a concurrent first start: stat error = %v", err)
	}
}

func TestLoadOrGenerate_WarnsOnGeneratePathWhenPermissionCheckFails(t *testing.T) {
	dataDir := t.TempDir()

	original := checkFilePermissionsFn
	checkFilePermissionsFn = func(string) error {
		return errors.New("forced failure for this test")
	}
	t.Cleanup(func() { checkFilePermissionsFn = original })

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	if _, err := LoadOrGenerate(dataDir, WithLogger(logger)); err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil (a failing permission check must warn, not fail construction)", err)
	}

	if !strings.Contains(buf.String(), "on-disk protection") {
		t.Fatalf("expected a warning naming the on-disk protection problem on the FIRST run (the generate path), got log output:\n%s", buf.String())
	}
}

func TestLoadOrGenerate_MissingDataDirIsCreated(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "data")

	if _, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler))); err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, FileName)); err != nil {
		t.Fatalf("key file was not created under the new data directory: %v", err)
	}
}

func TestSign_ThenCoordsigVerify_RoundTrips(t *testing.T) {
	dataDir := t.TempDir()
	mgr, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}

	payload := []byte("a fallback program's canonical bytes")
	sig, err := mgr.Sign(payload)
	if err != nil {
		t.Fatalf("Sign() = %v, want nil", err)
	}

	if err := sig.Verify(payload, mgr.PublicKey()); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

func TestSign_TamperedPayloadFailsVerify(t *testing.T) {
	dataDir := t.TempDir()
	mgr, err := LoadOrGenerate(dataDir, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}

	sig, err := mgr.Sign([]byte("original"))
	if err != nil {
		t.Fatalf("Sign() = %v, want nil", err)
	}

	if err := sig.Verify([]byte("tampered"), mgr.PublicKey()); !errors.Is(err, coordsig.ErrSignatureInvalid) {
		t.Fatalf("Verify() = %v, want coordsig.ErrSignatureInvalid", err)
	}
}

func TestSign_WrongKeyFailsVerify(t *testing.T) {
	dataDirA := t.TempDir()
	mgrA, err := LoadOrGenerate(dataDirA, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}
	dataDirB := t.TempDir()
	mgrB, err := LoadOrGenerate(dataDirB, WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatalf("LoadOrGenerate() = %v, want nil", err)
	}

	payload := []byte("a payload signed by mgrA")
	sig, err := mgrA.Sign(payload)
	if err != nil {
		t.Fatalf("Sign() = %v, want nil", err)
	}

	if err := sig.Verify(payload, mgrB.PublicKey()); !errors.Is(err, coordsig.ErrSignatureInvalid) {
		t.Fatalf("Verify() with the wrong deployment's key = %v, want coordsig.ErrSignatureInvalid", err)
	}
}

func TestSign_CorruptManagerReportsErrorNotPanic(t *testing.T) {
	mgr := &Manager{private: []byte("too short to be an ed25519 private key")}

	_, err := mgr.Sign([]byte("payload"))
	if !errors.Is(err, ErrCorruptKeyFile) {
		t.Fatalf("Sign() = %v, want ErrCorruptKeyFile", err)
	}
}
