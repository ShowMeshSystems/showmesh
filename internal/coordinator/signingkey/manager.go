package signingkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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

// dataDirPerm matches identity's writeBootstrapFile precedent for the
// data directory itself.
const dataDirPerm = 0o755

// filePerm is the mode [LoadOrGenerate] writes FileName with: owner
// read/write only, matching internal/coordinator/identity's
// writeBootstrapFile precedent for "a private-key-class secret, not a
// config file". ADR-025 decision 4 requires the pinned key be "readable,
// never writable, by the account [that does not own it]"; applied on the
// coordinator side, where this process is both the sole writer and the
// sole legitimate reader, that intent becomes "no other account on this
// host can read or write it at all," which 0o600 delivers. See
// checkFilePermissions for the load-time check that this has not since
// been loosened.
const filePerm = 0o600

// ErrCorruptKeyFile reports that FileName exists but its contents are not
// a valid Ed25519 private key (wrong length). This is deliberately never
// treated as "no key yet": auto-regenerating on a read failure would
// silently invalidate every node cache and fallback program this
// deployment ever signed (ADR-025 consequences: re-provisioning "generates
// a new key and invalidates every node's cache" is meant to be a
// deliberate operator action, never a side effect of a corrupt read). The
// caller must resolve this by hand.
var ErrCorruptKeyFile = errors.New("signingkey: key file exists but is not a valid ed25519 private key")

// Manager owns the coordinator's Ed25519 signing key for its lifetime in
// this process: generation on first run, on-disk persistence, and
// signing. A Manager is read-only after construction and safe for
// concurrent use (ed25519 signing has no mutable state).
//
// Verification is deliberately NOT a Manager method — see this package's
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
// The on-disk permission and ownership check (checkFilePermissions) is
// run and logged as a warning, never as a reason to fail construction:
// this key must remain usable for signing even when its file protection
// has been loosened by something outside this process. That mirrors
// ADR-025 decision 6's instinct that a trust-anchor problem degrades and
// reports rather than stopping something that does not need to stop —
// applied here as "keep signing, but say loudly that the anchor's
// on-disk protection needs attention," never as a startup failure.
func LoadOrGenerate(dataDir string, opts ...Option) (*Manager, error) {
	o := options{logger: slog.Default()}
	for _, opt := range opts {
		opt(&o)
	}

	if err := os.MkdirAll(dataDir, dataDirPerm); err != nil {
		return nil, fmt.Errorf("signingkey: create data directory %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, FileName)

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		pub, priv, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, fmt.Errorf("signingkey: generate ed25519 keypair: %w", genErr)
		}
		if writeErr := os.WriteFile(path, priv, filePerm); writeErr != nil {
			return nil, fmt.Errorf("signingkey: write key file: %w", writeErr)
		}
		o.logger.Info("generated a new coordinator signing key (ADR-025 decision 2, first run for this data volume)",
			"path", path, "public_key", base64.StdEncoding.EncodeToString(pub))
		return &Manager{private: priv, public: pub}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("signingkey: read key file: %w", err)
	}

	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: %q is %d bytes, want %d", ErrCorruptKeyFile, path, len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrCorruptKeyFile, path)
	}

	if permErr := checkFilePermissions(path); permErr != nil {
		o.logger.Warn("coordinator signing key file does not meet ADR-025 decision 4's on-disk protection intent; "+
			"the key remains in use for signing, but its file protection should be corrected",
			"path", path, "error", permErr)
	}

	return &Manager{private: priv, public: pub}, nil
}

// PublicKey returns the coordinator's verifying key. ADR-025 decision 3:
// delivering it to a node is enrollment's job (Track J seam J3); this
// method only exposes it for that future caller to read.
func (m *Manager) PublicKey() ed25519.PublicKey {
	return m.public
}

// Sign signs payload with the coordinator's private key. It never panics:
// a Manager whose private key is not exactly ed25519.PrivateKeySize bytes
// — which [LoadOrGenerate] never itself returns, but which this method
// still guards against rather than trusting that invariant forever — is
// reported as [ErrCorruptKeyFile] instead of being passed to
// crypto/ed25519.Sign, which panics on that input.
func (m *Manager) Sign(payload []byte) (coordsig.Signature, error) {
	if len(m.private) != ed25519.PrivateKeySize {
		return nil, ErrCorruptKeyFile
	}
	return coordsig.Signature(ed25519.Sign(m.private, payload)), nil
}
