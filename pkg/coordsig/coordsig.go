// Package coordsig is the shared wire shape and verification function for
// a payload signed by the coordinator's Ed25519 signing key (ADR-025
// decisions 1 and 2). It carries no key generation, no storage, and no
// signing: those stay in internal/coordinator/signingkey, which owns the
// private key and never lets it leave the coordinator's data volume. This
// package exists ONLY so the coordinator and the node agent can each call
// the identical [Signature.Verify] function against the identical wire
// shape, the same role pkg/cuecatalog and pkg/fppidentity already play
// for other coordinator/agent boundaries (see pkg/cuecatalog's own
// package comment). internal/agent must never import
// internal/coordinator (or vice versa) — this package is the deliberate
// third place both sides depend on instead.
//
// A verifying key's delivery and pinning on a node (ADR-025 decisions 3,
// 4, 5, and 7) are Track J seam J3, not this package. This package's job
// ends at "does this signature verify against this public key for this
// payload" — it holds no opinion about what a failed verification means
// to its caller.
package coordsig

import (
	"crypto/ed25519"
	"errors"
)

// ErrKeySize reports that a public key handed to [Signature.Verify] is
// not exactly ed25519.PublicKeySize bytes. crypto/ed25519.Verify panics on
// a wrong-size key; every exported function in this package reports this
// error instead, so a caller fed missing or corrupt key material gets a
// reported condition, never a panic.
var ErrKeySize = errors.New("coordsig: public key is not a valid ed25519 public key")

// ErrSignatureSize reports that a Signature is not exactly
// ed25519.SignatureSize bytes, for the identical reason [ErrKeySize]
// exists: crypto/ed25519.Verify panics on a wrong-size signature.
var ErrSignatureSize = errors.New("coordsig: signature is not a valid ed25519 signature")

// ErrSignatureInvalid reports that a structurally valid signature did not
// verify: a tampered payload, a signature made with a different key, or a
// wrong key presented at verification time all report this identical
// error, by design — ADR-025's threat model (a cloned card, a wrong
// restore, a cache from another installation) does not need or want a
// caller to distinguish which of those happened, only that the payload is
// not trustworthy under this key.
var ErrSignatureInvalid = errors.New("coordsig: signature does not verify")

// Signature is the wire shape of one Ed25519 signature: raw bytes,
// ed25519.SignatureSize (64) long. A caller embeds Signature directly as a
// field in whatever larger payload it signs (for example, Track J seam
// J1's fallback program) rather than this package prescribing an envelope
// shape it does not own.
type Signature []byte

// Verify reports whether sig is a valid Ed25519 signature of payload under
// publicKey. It never panics, unlike crypto/ed25519.Verify: a publicKey or
// sig of the wrong length is reported as [ErrKeySize] or
// [ErrSignatureSize] instead of being passed to the stdlib function that
// panics on that input.
func (sig Signature) Verify(payload []byte, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrKeySize
	}
	if len(sig) != ed25519.SignatureSize {
		return ErrSignatureSize
	}
	if !ed25519.Verify(publicKey, payload, []byte(sig)) {
		return ErrSignatureInvalid
	}
	return nil
}
