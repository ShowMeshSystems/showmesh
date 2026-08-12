package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost parameters, fixed by ADR-024 decision 1: "memory 64 MiB,
// time cost 2, parallelism 1". These are NOT tunable at runtime — the
// decision's own reasoning is that memory cost on a Pi-class coordinator
// running the broker and the UI alongside it is a capacity decision, and
// decision 8's concurrency-limiter sizing depends on this exact figure
// being fixed and known rather than configurable per deployment. A future
// change to these numbers is a new decision, not a flag.
const (
	argon2Memory      = 64 * 1024 // KiB (64 MiB), per argon2.IDKey's units
	argon2Time        = 2
	argon2Parallelism = 1
	argon2SaltLen     = 16
	argon2KeyLen      = 32
)

// ErrMalformedPasswordHash is returned by [VerifyPassword] when hash is
// not a well-formed argon2id PHC string this package produced — a
// corrupt or hand-edited principals.password_hash column, or a row
// created before this format existed. Distinct from a wrong-password
// verification failure so a caller can log the distinction (this is a
// data problem, not a guessed password), but the caller-visible outcome
// through [Service.AuthenticatePassword] is [ErrInvalidCredential] either
// way — see that method's doc comment.
var ErrMalformedPasswordHash = errors.New("identity: malformed argon2id password hash")

// HashPassword hashes password with argon2id at the ADR-024 decision 1
// parameters, using a fresh random salt from crypto/rand, and encodes the
// result as a standard PHC string:
//
//	$argon2id$v=19$m=65536,t=2,p=1$<base64 salt>$<base64 hash>
//
// Encoding the parameters into the string (rather than relying on package
// constants alone) is what lets a future parameter change roll forward
// without a flag day: an old hash still states the parameters it was
// created with, so [VerifyPassword] always re-derives using the hash's
// own recorded cost rather than this build's current constants, and a
// verified-but-outdated hash can be re-hashed at login time by a caller
// that wants to upgrade it opportunistically (Step 6 does not do this
// itself — no endpoint calls it — but the format supports it without a
// migration).
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Parallelism, argon2KeyLen)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches hash, a PHC string
// previously produced by [HashPassword]. It re-derives a key using the
// PARAMETERS RECORDED IN hash (not this build's current
// argon2Memory/Time/Parallelism constants — see [HashPassword]'s doc
// comment for why) and compares in constant time via
// subtle.ConstantTimeCompare, so neither a length mismatch nor a byte
// mismatch is observable through timing.
//
// A malformed hash returns (false, [ErrMalformedPasswordHash]) rather
// than panicking or silently reporting a mismatch with no explanation —
// see that error's doc comment for why [Service.AuthenticatePassword]
// still collapses this to [ErrInvalidCredential] at its own boundary.
func VerifyPassword(hash, password string) (bool, error) {
	memory, time_, parallelism, salt, key, err := parsePHC(hash)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(password), salt, time_, memory, parallelism, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1, nil
}

// parsePHC parses the PHC string [HashPassword] produces. Only the exact
// shape that function writes is accepted — this is not a general PHC
// parser for every argon2 variant or every field ordering the format
// technically allows, because nothing in this codebase ever needs to read
// a hash this package did not itself write.
func parsePHC(hash string) (memory uint32, time_ uint32, parallelism uint8, salt, key []byte, err error) {
	parts := strings.Split(hash, "$")
	// strings.Split("$argon2id$v=19$m=...$salt$key", "$") yields
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", "salt", "key"] — six
	// elements, the leading "" from the string's leading '$'.
	if len(parts) != 6 || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: unexpected format", ErrMalformedPasswordHash)
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: missing version field", ErrMalformedPasswordHash)
	}
	// The version field is validated for shape but not enforced against
	// argon2.Version: a hash written by a future, newer argon2 revision
	// with a higher version number should fail verification (wrong key,
	// legitimately) rather than this parser refusing to even attempt it.
	if _, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v=")); err != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: malformed version field", ErrMalformedPasswordHash)
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: malformed parameter field", ErrMalformedPasswordHash)
	}
	m, errM := parsePHCParam(params[0], "m=")
	t, errT := parsePHCParam(params[1], "t=")
	p, errP := parsePHCParam(params[2], "p=")
	if errM != nil || errT != nil || errP != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: malformed parameter field", ErrMalformedPasswordHash)
	}

	saltBytes, errSalt := base64.RawStdEncoding.DecodeString(parts[4])
	keyBytes, errKey := base64.RawStdEncoding.DecodeString(parts[5])
	if errSalt != nil || errKey != nil {
		return 0, 0, 0, nil, nil, fmt.Errorf("%w: malformed salt or key encoding", ErrMalformedPasswordHash)
	}

	return uint32(m), uint32(t), uint8(p), saltBytes, keyBytes, nil
}

func parsePHCParam(s, prefix string) (int, error) {
	if !strings.HasPrefix(s, prefix) {
		return 0, fmt.Errorf("missing prefix %q", prefix)
	}
	return strconv.Atoi(strings.TrimPrefix(s, prefix))
}
