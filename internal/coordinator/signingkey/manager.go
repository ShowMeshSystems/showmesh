package signingkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
)

// FileName is the file [LoadOrGenerate] persists the coordinator's Ed25519
// private signing key to, inside the data directory the coordinator was
// constructed with (ADR-025 decision 2: "the private key never leaves the
// coordinator's data volume"). Exported for the same reason
// identity.BootstrapFileName is: an operator's documentation, or an
// export bundle once RES-008 D4's mechanism exists, should reference this
// name rather than a hand-copied literal.
const FileName = "coordinator-signing-key"

// tmpFileSuffix names the temporary file [generateAndPersist] writes and
// fsyncs a freshly generated key to before publishing it as FileName. This
// is ONE fixed name, not a random one, so that os.O_EXCL on it lets a
// caller skip wasted generation work when another caller is already mid-
// generation under this exact name. It is NOT, by itself, what makes two
// concurrent first runs agree on one key: os.O_EXCL here only stops two
// callers from generating under the identical temporary name at the
// identical moment, and a straggler that started before an earlier
// caller finished and freed this name could still generate its own key
// afterward. The real, permanent guarantee is [generateAndPersist]'s
// os.Link publish step, which fails rather than overwrites when FileName
// already exists.
const tmpFileSuffix = ".tmp"

// maxConcurrentStartAttempts and concurrentStartRetryDelay bound how long
// [LoadOrGenerate] waits for a DIFFERENT process's first-run key
// generation to finish before giving up. Generating, fsyncing, and
// publishing are all sub-millisecond in practice, so 500 attempts at 2ms
// (at most one second total) is generous for the race this exists to
// resolve. It also bounds the wait for the other case that produces the
// identical symptom: a stale tmpFileSuffix left behind by a process that
// crashed mid-write, which is now a reported timeout instead of a wait
// forever.
const (
	maxConcurrentStartAttempts = 500
	concurrentStartRetryDelay  = 2 * time.Millisecond
)

// dataDirPerm matches identity's writeBootstrapFile precedent for the
// data directory itself.
const dataDirPerm = 0o755

// filePerm is the mode [generateAndPersist] creates FileName (and its
// temporary file) with: owner read/write only, matching
// internal/coordinator/identity's writeBootstrapFile precedent for "a
// private-key-class secret, not a config file". ADR-025 decision 4
// requires the pinned key be "readable, never writable, by the account
// [that does not own it]"; applied on the coordinator side, where this
// process is both the sole writer and the sole legitimate reader, that
// intent becomes "no other account on this host can read or write it at
// all," which 0o600 delivers. See checkFilePermissions for the check that
// this has not since been loosened, run after every generate and every
// load.
const filePerm = 0o600

// ErrCorruptKeyFile reports that FileName exists but its contents are not
// a valid Ed25519 private key: either the wrong length, or exactly
// ed25519.PrivateKeySize bytes whose trailing 32 bytes do not match the
// public key ed25519.NewKeyFromSeed derives from its own leading 32-byte
// seed (for example, a single flipped bit from partial disk corruption).
// This is deliberately never treated as "no key yet": auto-regenerating
// on a read failure would silently invalidate every node cache and
// fallback program this deployment ever signed (ADR-025 consequences:
// re-provisioning "generates a new key and invalidates every node's
// cache" is meant to be a deliberate operator action, never a side effect
// of a corrupt read). The caller must resolve this by hand.
var ErrCorruptKeyFile = errors.New("signingkey: key file exists but is not a valid ed25519 private key")

// errConcurrentGenerationInProgress is [generateAndPersist]'s own internal
// signal to [LoadOrGenerate] that a DIFFERENT caller is generating the
// deployment's first key: either it currently holds tmpFileSuffix, or it
// has already published FileName by the time this call reached its own
// os.Link. Either way, this call's own keypair (if it got far enough to
// generate one) was never written anywhere and must be discarded; the
// caller should read FileName instead of trusting it. It never escapes
// LoadOrGenerate.
var errConcurrentGenerationInProgress = errors.New("signingkey: a concurrent first-run key generation is in progress")

// Manager owns the coordinator's Ed25519 signing key for its lifetime in
// this process: generation on first run, on-disk persistence, and
// signing. A Manager is read-only after construction and safe for
// concurrent use (ed25519 signing has no mutable state).
//
// Verification is deliberately NOT a Manager method; see this package's
// doc comment. Call [coordsig.Signature.Verify] directly.
type Manager struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

type options struct {
	logger *slog.Logger
}

// Option configures [LoadOrGenerate].
type Option func(*options)

// WithLogger sets the logger [LoadOrGenerate] reports first-run
// generation and the on-disk permission check on. Defaults to
// slog.Default() when omitted.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// LoadOrGenerate loads the coordinator's signing key from
// dataDir/[FileName], generating and persisting a fresh Ed25519 keypair on
// first run (the file does not exist yet) per ADR-025 decision 2. A file
// that exists but is not a valid Ed25519 private key is
// [ErrCorruptKeyFile], never silently regenerated. dataDir is created if
// it does not already exist, matching identity's writeBootstrapFile
// precedent.
//
// Two coordinators started against the SAME dataDir at the same time (an
// operator's first run on a freshly mounted data volume, or a
// misconfiguration that points two processes at one directory) do not
// each generate their own key: [generateAndPersist] resolves the race so
// exactly one of them generates and writes, and every other caller here
// loops back and loads what the winner wrote.
func LoadOrGenerate(dataDir string, opts ...Option) (*Manager, error) {
	o := options{logger: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}

	if err := os.MkdirAll(dataDir, dataDirPerm); err != nil {
		return nil, fmt.Errorf("signingkey: create data directory %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, FileName)

	for attempt := 0; ; attempt++ {
		raw, err := os.ReadFile(path)
		if err == nil {
			return loadFromBytes(raw, path, o)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("signingkey: read key file: %w", err)
		}

		mgr, genErr := generateAndPersist(path, o)
		if genErr == nil {
			return mgr, nil
		}
		if !errors.Is(genErr, errConcurrentGenerationInProgress) {
			return nil, genErr
		}
		if attempt >= maxConcurrentStartAttempts {
			return nil, fmt.Errorf("signingkey: timed out waiting for a concurrent coordinator to finish generating %q (a stale %q left over from a crashed process would also cause this)",
				path, path+tmpFileSuffix)
		}
		time.Sleep(concurrentStartRetryDelay)
	}
}

// generateAndPersist generates a fresh Ed25519 keypair and durably,
// exclusively publishes it at path. The private key is written to and
// fsynced on path+tmpFileSuffix first, so a crash or a full disk before
// that finishes never leaves path itself holding a short, truncated key
// the way a bare os.WriteFile to path directly would. It is then
// published with os.Link, not os.Rename: Link fails with os.ErrExist
// instead of silently overwriting when path already exists, which is
// what actually gives exactly one caller (of any number racing across
// however many processes share dataDir) a key that ends up on disk. Every
// other caller, whether it lost the initial os.O_EXCL on the temporary
// name or lost this Link, returns [errConcurrentGenerationInProgress] so
// [LoadOrGenerate] loops back and loads the winner's key instead of
// trusting a keypair this call generated but never actually published.
func generateAndPersist(path string, o options) (*Manager, error) {
	tmpPath := path + tmpFileSuffix

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errConcurrentGenerationInProgress
		}
		return nil, fmt.Errorf("signingkey: create temporary key file %q: %w", tmpPath, err)
	}

	pub, priv, genErr := ed25519.GenerateKey(rand.Reader)
	if genErr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("signingkey: generate ed25519 keypair: %w", genErr)
	}
	if _, writeErr := f.Write(priv); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("signingkey: write temporary key file %q: %w", tmpPath, writeErr)
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("signingkey: sync temporary key file %q: %w", tmpPath, syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("signingkey: close temporary key file %q: %w", tmpPath, closeErr)
	}

	// os.Link, not os.Rename, publishes tmpPath as path. Rename always
	// succeeds and overwrites even when path already exists, which is
	// exactly wrong here: tmpPath's own O_EXCL only stops two callers
	// generating under the SAME temporary name at the SAME time, but it
	// does nothing once the winner's rename frees that name again, so a
	// straggler that started before the winner published could still
	// generate its own, different key and re-win with a Rename, silently
	// disagreeing with a Manager another goroutine already built from the
	// first winner's content. Link fails atomically with os.ErrExist
	// instead of overwriting, so it is the operation that actually grants
	// "the first publisher of path wins, permanently."
	if linkErr := os.Link(tmpPath, path); linkErr != nil {
		_ = os.Remove(tmpPath)
		if errors.Is(linkErr, os.ErrExist) {
			return nil, errConcurrentGenerationInProgress
		}
		return nil, fmt.Errorf("signingkey: publish key file %q: %w", path, linkErr)
	}
	_ = os.Remove(tmpPath)

	warnIfPermissionsLoose(path, o)

	o.logger.Info("generated a new coordinator signing key (ADR-025 decision 2, first run for this data volume)",
		"path", path, "public_key", base64.StdEncoding.EncodeToString(pub))
	return &Manager{private: priv, public: pub}, nil
}

// loadFromBytes validates raw as a genuine, internally consistent Ed25519
// private key and builds a Manager from it. The length check alone is not
// enough to catch corruption: ed25519.PrivateKey.Public() only slices
// bytes 32:64 back out verbatim, so it cannot detect a flipped bit
// anywhere in raw, while ed25519.Sign derives the real public half from
// the seed in bytes 0:32. A raw value whose stored public half does not
// match what ed25519.NewKeyFromSeed derives from its own seed would
// therefore load with no error and sign silently under an identity
// nothing verifies against; it is rejected here instead, as
// [ErrCorruptKeyFile].
func loadFromBytes(raw []byte, path string, o options) (*Manager, error) {
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: %q is %d bytes, want %d", ErrCorruptKeyFile, path, len(raw), ed25519.PrivateKeySize)
	}

	derived := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if !bytes.Equal(derived, raw) {
		return nil, fmt.Errorf("%w: %q's stored public half does not match the key derived from its own seed", ErrCorruptKeyFile, path)
	}

	priv := ed25519.PrivateKey(raw)
	pub := priv.Public().(ed25519.PublicKey)

	warnIfPermissionsLoose(path, o)

	return &Manager{private: priv, public: pub}, nil
}

// checkFilePermissionsFn is [checkFilePermissions], indirected through a
// package variable so a test can substitute a failing check to prove
// [warnIfPermissionsLoose] is actually reached from both call sites: the
// real check's ownership half needs root or a second uid to genuinely
// fail, neither of which a test process can forge, so this indirection
// is what lets a test verify the wiring without needing real root or a
// second uid. Production code never reassigns it.
var checkFilePermissionsFn = checkFilePermissions

// warnIfPermissionsLoose runs checkFilePermissionsFn and logs a warning,
// never a failure, when it reports a problem: this key must remain usable
// for signing even when its file protection has been loosened by
// something outside this process. That mirrors ADR-025 decision 6's
// instinct that a trust-anchor problem degrades and reports rather than
// stopping something that does not need to stop, applied here as "keep
// signing, but say loudly that the anchor's on-disk protection needs
// attention," never as a failure to load or generate. Called from both
// [generateAndPersist] and [loadFromBytes], so a first run is checked
// exactly as a later load is, not only from the second start onward.
func warnIfPermissionsLoose(path string, o options) {
	if permErr := checkFilePermissionsFn(path); permErr != nil {
		o.logger.Warn("coordinator signing key file does not meet ADR-025 decision 4's on-disk protection intent; "+
			"the key remains in use for signing, but its file protection should be corrected",
			"path", path, "error", permErr)
	}
}

// PublicKey returns the coordinator's verifying key. ADR-025 decision 3
// makes delivering it to a node an enrollment concern; this method only
// exposes it for that future caller to read.
func (m *Manager) PublicKey() ed25519.PublicKey {
	return m.public
}

// Sign signs payload with the coordinator's private key. It never panics:
// a Manager whose private key is not exactly ed25519.PrivateKeySize bytes,
// which [LoadOrGenerate] never itself returns but which this method still
// guards against rather than trusting that invariant forever, is reported
// as [ErrCorruptKeyFile] instead of being passed to crypto/ed25519.Sign,
// which panics on that input.
func (m *Manager) Sign(payload []byte) (coordsig.Signature, error) {
	if len(m.private) != ed25519.PrivateKeySize {
		return nil, ErrCorruptKeyFile
	}
	return coordsig.Signature(ed25519.Sign(m.private, payload)), nil
}
