package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// sessionSecretBytes mirrors tokenRandomBytes exactly (20 bytes = 160
// bits): a session cookie is exactly as sensitive as a bearer token — it
// authenticates the identical set of actions — so it gets the identical
// entropy floor, comfortably above ADR-024 decision 5's "opaque, high
// entropy" and decision 1's 128-bit floor for tokens, applied here by the
// same reasoning even though decision 5 does not restate a number.
const sessionSecretBytes = 20

// generateSessionSecret creates a new random session cookie value:
// base64url-encoded crypto/rand entropy, no prefix (unlike a [Token] —
// there is nothing analogous to TokenPrefix's URL-in-query-string hazard
// for a cookie, which the browser attaches automatically rather than a
// caller ever typing or pasting into a URL).
func generateSessionSecret() (string, error) {
	raw := make([]byte, sessionSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: generate session secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// hashSessionSecret returns the SHA-256 hex digest of a session cookie
// value — what principal_sessions.digest stores and what
// [Store.GetSessionByDigest] looks up by, exactly mirroring [HashToken]'s
// reasoning applied to sessions instead of tokens.
func hashSessionSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
