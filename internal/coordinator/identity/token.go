package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// TokenPrefix is the fixed, identifiable prefix every API token carries
// (ADR-024 decision 1). It is load-bearing rather than cosmetic: the API
// layer rejects any request whose query string contains it (the "never
// carried in a URL" rule decision 1 states, and the reason Step 5's
// "GET-only is not read-only" lesson is cited by name in that decision).
// This package does not itself implement that query-string rejection —
// it is an API-layer request-inspection concern, not a credential-storage
// one — but exports TokenPrefix so that check has one authoritative
// constant to look for rather than a hand-copied literal.
const TokenPrefix = "smsh_"

// tokenRandomBytes is how many random bytes back a token's non-prefix
// portion: 20 bytes = 160 bits, comfortably above ADR-024 decision 1's
// "at least 128 bits" floor. Base64url-encodes to 27 characters with no
// padding.
const tokenRandomBytes = 20

// tokenHintChars is how many characters of the encoded random portion
// [GenerateToken] exposes as [Token.Hint] — a short, NON-secret slice an
// operator can use to tell two tokens with the same label apart in a
// listing (see store.TokenRecord.Hint's doc comment), long enough to be
// useful, short enough that revealing it costs little of the token's
// total entropy: 6 base64url characters is 36 of 160 bits, leaving 124
// bits unrevealed — still far beyond any brute-force concern.
const tokenHintChars = 6

// Token is what [GenerateToken] returns: the full secret string to
// display to the caller exactly once, its SHA-256 digest for storage, and
// its non-secret display hint.
type Token struct {
	// ID is the stored row's own id — empty on the value [GenerateToken]
	// alone returns, and set by [Service.IssueToken] after it has created
	// the row, so a caller (Track G seam G-5's API and CLI surfaces) can
	// name this exact token for a later list or revoke without a second
	// lookup keyed on Hint.
	ID string

	// Value is the full token string, TokenPrefix followed by the random
	// portion — this is what a caller presents as
	// "Authorization: Bearer <Value>" and what [Service] never returns
	// again after the moment of creation. ADR-024 decision 1: "displayed
	// exactly once at creation".
	Value string

	// Digest is the SHA-256 hex digest of Value, what is actually stored
	// (principal_tokens.digest) and what [Service.AuthenticateToken] looks
	// up by. Never the reverse — Digest cannot be turned back into Value.
	Digest string

	// Hint is the first tokenHintChars characters of Value's random
	// portion (after TokenPrefix), safe to store and to display in a
	// listing.
	Hint string
}

// GenerateToken creates a new random API token: TokenPrefix followed by
// tokenRandomBytes of crypto/rand entropy, base64url-encoded with no
// padding (URL-safe and Authorization-header-safe with no further
// escaping).
func GenerateToken() (Token, error) {
	raw := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, fmt.Errorf("identity: generate token: %w", err)
	}
	random := base64.RawURLEncoding.EncodeToString(raw)
	value := TokenPrefix + random

	hint := random
	if len(hint) > tokenHintChars {
		hint = hint[:tokenHintChars]
	}

	return Token{
		Value:  value,
		Digest: HashToken(value),
		Hint:   hint,
	}, nil
}

// HashToken returns the SHA-256 hex digest of the full presented token
// string — what [store.Store.GetTokenByDigest] looks up by. Exported so
// [Service.AuthenticateToken] and any test can compute the identical
// digest a stored [Token.Digest] was created with.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokensEqualConstantTime compares two already-computed digest strings in
// constant time. [Service.AuthenticateToken] looks a token up by digest
// via an indexed SQL equality (database/sql gives no constant-time
// guarantee over that path, and a SHA-256 digest's own preimage
// resistance is what actually protects the secret from a timing
// side-channel on the lookup), then re-compares the computed digest
// against the row's stored digest with this function as defense in depth
// — ADR-024 decision 1's "compared in constant time" is enforced here,
// literally, rather than only implied by "we hashed it first".
func tokensEqualConstantTime(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
