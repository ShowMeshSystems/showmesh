package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BootstrapFileName is the file [Service] writes the single-use bootstrap
// code to, inside the data directory the coordinator was constructed
// with. ADR-024 decision 9: the code is written ONLY here — never to a
// log line (OBSERVABILITY §13), never to the database in the clear (see
// migrations.go's schemaV5 doc comment in the store package) — and this
// exported name is what a deployment's documentation or an operator's
// `cat` command should reference, rather than a hand-copied literal.
const BootstrapFileName = "bootstrap-code.txt"

// bootstrapCodeBytes is how much entropy backs the bootstrap code: 20
// random bytes (160 bits), matching [tokenRandomBytes] and
// [sessionSecretBytes] — this credential grants exactly as much
// (temporarily, single-use) as an admin token would, so it gets the
// identical floor.
const bootstrapCodeBytes = 20

// DefaultBootstrapCodeTTL is how long a generated bootstrap code remains
// claimable before [Service.ClaimBootstrap] refuses it as expired.
//
// SHOWMESH HYPOTHESIS, NOT DERIVED FROM ANY MEASUREMENT — labeled exactly
// as retention.go's DefaultMaxEventAge/DefaultMaxAuditAge are, for the
// identical reason: ADR-024 decision 9 requires the code to "carry an
// expiry" but names no duration. 24 hours is picked only as "long enough
// that an operator setting the coordinator up does not race a shipping
// delay between generating the code and reading the file (e.g. first boot
// happening unattended overnight before the operator arrives to finish
// setup the next day), short enough that a bootstrap code nobody claimed
// does not sit valid indefinitely." A future record measuring real setup
// timing owns the real answer; nothing here treats this as settled.
const DefaultBootstrapCodeTTL = 24 * time.Hour

// bootstrapCodeAlphabet excludes visually ambiguous characters (0/O,
// 1/I/L) — base32's standard alphabet already does this by construction
// (RFC 4648 uses A-Z and 2-7), which is why this package encodes with it
// rather than base64url: a bootstrap code, unlike a token or session
// secret, is meant to be read off a file by a human and possibly
// transcribed, not only copy-pasted, so avoiding characters a tired
// operator could misread at a keyboard outdoors at night is worth the
// slightly less dense encoding.
func generateBootstrapCode() (string, error) {
	raw := make([]byte, bootstrapCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: generate bootstrap code: %w", err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="), nil
}

// hashBootstrapCode returns the SHA-256 hex digest of a bootstrap code —
// what bootstrap.code_digest stores, matching [HashToken]/
// [hashSessionSecret]'s identical pattern applied to the third and last
// credential-shaped secret this package handles. The raw code is never
// stored anywhere but the file [writeBootstrapFile] writes; see
// migrations.go's schemaV5 doc comment in the store package, rule 1.
func hashBootstrapCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// bootstrapFilePath returns where [BootstrapFileName] lives under
// dataDir.
func bootstrapFilePath(dataDir string) string {
	return filepath.Join(dataDir, BootstrapFileName)
}

// writeBootstrapFile writes code to [bootstrapFilePath] with permissions
// restrictive enough that only the coordinator process's own user can
// read it (0o600 — matching a private-key-class secret, not a config
// file), creating dataDir first if it does not already exist. The file's
// content is exactly the code and nothing else: no expiry, no principal
// name, no formatting an operator would have to parse — matching ADR-024
// decision 9's "written ONLY to a file" being the whole artifact, kept as
// simple as `cat`-and-paste requires.
func writeBootstrapFile(dataDir, code string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("identity: create data directory %q for bootstrap file: %w", dataDir, err)
	}
	if err := os.WriteFile(bootstrapFilePath(dataDir), []byte(code+"\n"), 0o600); err != nil {
		return fmt.Errorf("identity: write bootstrap file: %w", err)
	}
	return nil
}

// deleteBootstrapFile removes [bootstrapFilePath] from dataDir. A
// not-exist error is not an error here: [Service.ClaimBootstrap] calls
// this after a successful claim, and an operator (or a previous, crashed
// claim attempt — see store.Store.ClaimBootstrapAndCreatePrincipal's race
// handling) may already have removed it.
func deleteBootstrapFile(dataDir string) error {
	err := os.Remove(bootstrapFilePath(dataDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("identity: delete bootstrap file: %w", err)
	}
	return nil
}
