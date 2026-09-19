// Package weatherdelay holds the weather delay state and the signed start
// request that the coordinator, node agent and FPP plugin share (ADR-053).
// It has no store, HTTP or MQTT access and no behavior.
package weatherdelay

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// The two kinds of weather delay (ADR-053 decision 1). Both are wire and
// storage values.
const (
	KindDelay       = "delay"
	KindCancelNight = "cancelNight"
)

var validKinds = map[string]bool{
	KindDelay:       true,
	KindCancelNight: true,
}

// ValidKind reports whether kind is a member of the closed vocabulary.
func ValidKind(kind string) bool { return validKinds[kind] }

// MaxSafeRevision is 2^53 - 1, the largest integer a browser reads exactly.
// Revision is a counter, never a timestamp, so it stays far below this.
const MaxSafeRevision = 1<<53 - 1

// State is the weather delay state the coordinator persists and publishes.
// StartedBy is an operator principal id or a trigger source name. Revision
// increases on every change, including a resume.
type State struct {
	Active    bool      `json:"active"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"startedAt"`
	StartedBy string    `json:"startedBy"`
	Revision  int64     `json:"revision"`
}

// NotActive is the state reported when nothing has ever been written. A
// resume writes an inactive state with the next revision, not this value.
var NotActive = State{}

// ErrInvalidState is wrapped by every error [State.Validate] returns.
var ErrInvalidState = errors.New("weatherdelay: invalid state")

// Validate checks Revision is in [0, MaxSafeRevision] and that Kind,
// StartedAt and StartedBy are all set while Active and all unset otherwise.
func (s State) Validate() error {
	if s.Revision < 0 || s.Revision > MaxSafeRevision {
		return fmt.Errorf("%w: revision %d is out of range [0, %d]", ErrInvalidState, s.Revision, MaxSafeRevision)
	}
	if s.Active {
		if !ValidKind(s.Kind) {
			return fmt.Errorf("%w: kind %q must be one of %q or %q", ErrInvalidState, s.Kind, KindDelay, KindCancelNight)
		}
		if s.StartedAt.IsZero() {
			return fmt.Errorf("%w: startedAt is zero while active", ErrInvalidState)
		}
		if s.StartedBy == "" {
			return fmt.Errorf("%w: startedBy is empty while active", ErrInvalidState)
		}
		return nil
	}
	if s.Kind != "" || !s.StartedAt.IsZero() || s.StartedBy != "" {
		return fmt.Errorf("%w: kind, startedAt, and startedBy must all be unset while not active", ErrInvalidState)
	}
	return nil
}

// EncodeState marshals s after validating it.
func EncodeState(s State) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("weatherdelay: encode state: %w", err)
	}
	return b, nil
}

// DecodeState parses data and validates the result.
func DecodeState(data []byte) (State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	if err := s.Validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

// MaxPresignValidDays is the longest a minted NotAfter may extend past
// IssuedAt. A node refuses a request whose NotAfter exceeds this, and the
// presigned-start endpoint refuses to mint one that would.
const MaxPresignValidDays = 400

// StartRequest is the pre-signed start (ADR-053 decision 8). No field or
// Kind value expresses a resume. Replay is not tracked here because
// replaying a start can only cause darkness. NotAfter is optional: the
// zero value means the node applies its own fixed-age rule instead of an
// explicit expiry, so a request minted before NotAfter existed keeps
// working unchanged.
type StartRequest struct {
	Kind     string    `json:"kind"`
	IssuedAt time.Time `json:"issuedAt"`
	Nonce    string    `json:"nonce"`
	NotAfter time.Time `json:"notAfter,omitzero"`
}

// ErrInvalidStartRequest is wrapped by every error [StartRequest.Validate]
// returns.
var ErrInvalidStartRequest = errors.New("weatherdelay: invalid start request")

// Validate checks Kind, IssuedAt and Nonce are all set and Kind is known,
// and, when NotAfter is set, that it is after IssuedAt and no more than
// [MaxPresignValidDays] past it.
func (r StartRequest) Validate() error {
	if !ValidKind(r.Kind) {
		return fmt.Errorf("%w: kind %q must be one of %q or %q", ErrInvalidStartRequest, r.Kind, KindDelay, KindCancelNight)
	}
	if r.IssuedAt.IsZero() {
		return fmt.Errorf("%w: issuedAt is zero", ErrInvalidStartRequest)
	}
	if r.Nonce == "" {
		return fmt.Errorf("%w: nonce is empty", ErrInvalidStartRequest)
	}
	if !r.NotAfter.IsZero() {
		if !r.NotAfter.After(r.IssuedAt) {
			return fmt.Errorf("%w: notAfter %s must be after issuedAt %s", ErrInvalidStartRequest, r.NotAfter, r.IssuedAt)
		}
		if r.NotAfter.After(r.IssuedAt.Add(MaxPresignValidDays * 24 * time.Hour)) {
			return fmt.Errorf("%w: notAfter must be no more than %d days after issuedAt", ErrInvalidStartRequest, MaxPresignValidDays)
		}
	}
	return nil
}

// CanonicalBytes returns the RFC 8785 JSON bytes the coordinator signs and
// [SignedStartRequest.Verify] checks.
func (r StartRequest) CanonicalBytes() ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("weatherdelay: marshal start request: %w", err)
	}
	canonical, _, err := fppidentity.HashCanonical(raw)
	if err != nil {
		return nil, fmt.Errorf("weatherdelay: canonicalize start request: %w", err)
	}
	return canonical, nil
}

// SignedStartRequest is a [StartRequest] signed with the coordinator key a
// node already holds (ADR-025). Signing happens in
// internal/coordinator/signingkey over [StartRequest.CanonicalBytes].
type SignedStartRequest struct {
	Request   StartRequest       `json:"request"`
	Signature coordsig.Signature `json:"signature"`
}

// Verify checks sr.Request is well formed and its signature verifies under
// publicKey. It does not check freshness.
func (sr SignedStartRequest) Verify(publicKey ed25519.PublicKey) error {
	if err := sr.Request.Validate(); err != nil {
		return err
	}
	payload, err := sr.Request.CanonicalBytes()
	if err != nil {
		return err
	}
	return sr.Signature.Verify(payload, publicKey)
}

// Signer produces a [coordsig.Signature] over a payload.
// [internal/coordinator/signingkey.Manager]'s Sign method already
// satisfies this with no adapter.
type Signer interface {
	Sign(payload []byte) (coordsig.Signature, error)
}

// Sign builds a [SignedStartRequest] over req, signed with signer. req must
// already be valid; a caller building it fresh should set IssuedAt and a
// freshly generated Nonce.
func Sign(req StartRequest, signer Signer) (SignedStartRequest, error) {
	if err := req.Validate(); err != nil {
		return SignedStartRequest{}, err
	}
	payload, err := req.CanonicalBytes()
	if err != nil {
		return SignedStartRequest{}, err
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return SignedStartRequest{}, fmt.Errorf("weatherdelay: sign start request: %w", err)
	}
	return SignedStartRequest{Request: req, Signature: sig}, nil
}
