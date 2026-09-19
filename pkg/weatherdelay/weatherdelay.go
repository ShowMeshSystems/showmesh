// Package weatherdelay is ADR-053's shared vocabulary: the state type the
// coordinator persists and publishes, and the signed start request a node
// agent and an FPP plugin both verify (ADR-053 decisions 2, 8, 9). It
// carries no store access, no HTTP, and no MQTT client — the same
// coordinator/agent/CLI-neutral role pkg/coordsig and pkg/fallbackprogram
// already play for their own boundaries. This package holds no behavior:
// the night loop, the cue activation loop, the enforcement loop, and the
// plugin's output gate are all later work.
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

// The two members of ADR-053 decision 1's closed vocabulary: "Weather
// delay means the show will resume tonight. Cancel night means it will
// not." Both are wire and storage values, never translated into a second
// spelling downstream (CLAUDE.md's operator-copy standard is applied at
// render time, not here).
const (
	KindDelay       = "delay"
	KindCancelNight = "cancelNight"
)

// validKinds is the closed enum as a map, mirroring
// internal/coordinator/config's showModes.
var validKinds = map[string]bool{
	KindDelay:       true,
	KindCancelNight: true,
}

// ValidKind reports whether kind is a member of the closed vocabulary.
func ValidKind(kind string) bool { return validKinds[kind] }

// MaxSafeRevision is the largest integer a JS Number represents exactly
// (2^53 - 1). Revision must fit it because the Operator UI reads it as a
// JS Number — see docs/private context on the int64-revision-exceeds-JS-
// precision defect this project already shipped once with a UnixNano-
// derived revision. Revision here is a plain monotonic counter, never a
// timestamp, specifically so it never approaches this bound in practice.
const MaxSafeRevision = 1<<53 - 1

// State is the coordinator-persisted, node-published weather-delay state
// (ADR-053 decisions 1 and 2): whether a delay or cancel-night is
// currently active, which kind, who or what started it, when, and a
// monotonic revision. StartedBy is an operator principal id (a human
// press) or a trigger source name (ADR-053 decision 12: a configured
// weather or lightning feed) — never both, and never empty while Active.
type State struct {
	Active    bool      `json:"active"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"startedAt"`
	StartedBy string    `json:"startedBy"`
	Revision  int64     `json:"revision"`
}

// NotActive is the state reported when nothing has ever been written or a
// resume has cleared it (ADR-053 decision 11: "Resume ... clears the
// state"). Revision is 0, matching config_revisions' own "nothing written
// yet" convention this codebase already uses everywhere else.
var NotActive = State{}

// ErrInvalidState is wrapped by every error [State.Validate] returns.
var ErrInvalidState = errors.New("weatherdelay: invalid state")

// Validate reports whether s is well-formed: a non-negative Revision
// within [MaxSafeRevision], and, while Active, a Kind from the closed
// vocabulary, a non-zero StartedAt, and a non-empty StartedBy. An inactive
// state carries none of those three — a resumed/never-started state is
// the CLEARED state, not merely an active flag turned off with stale
// fields left behind.
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

// EncodeState marshals s after validating it, mirroring
// pkg/mqttproto.EncodeShowModeMessage's identical "never let a malformed
// value reach a retained topic or a stored row" rule.
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

// StartRequest is ADR-053 decision 8's pre-signed start: a small,
// deliberately narrow payload that can only ever mean "start a delay or a
// cancel-night", never a resume — there is no field, and no value of Kind,
// that expresses one. Nonce is a caller-chosen, fresh-per-signing value;
// this package does not track nonce reuse (a node's own dedup, ADR-053
// decision 8's "replaying a start can only cause darkness" already makes
// replay harmless, not merely detected).
type StartRequest struct {
	Kind     string    `json:"kind"`
	IssuedAt time.Time `json:"issuedAt"`
	Nonce    string    `json:"nonce"`
}

// ErrInvalidStartRequest is wrapped by every error [StartRequest.Validate]
// returns.
var ErrInvalidStartRequest = errors.New("weatherdelay: invalid start request")

// Validate reports whether r is well-formed: Kind from the closed
// vocabulary, a non-zero IssuedAt, and a non-empty Nonce.
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
	return nil
}

// CanonicalBytes serializes r as the RFC 8785 canonical JSON bytes a
// coordinator signs and [SignedStartRequest.Verify] checks against,
// mirroring [pkg/fallbackprogram.Program.CanonicalBytes]'s identical "one
// canonical serialization, never re-derived a second way" rule.
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

// SignedStartRequest is a [StartRequest] plus the coordinator's signature
// over its own canonical bytes — the complete, verifiable wire object
// ADR-053 decision 9 describes: "a signed weather delay start, verified
// with the coordinator key the node already holds (ADR-025)." Signing
// itself stays where every other coordinator-signed payload's signing
// stays, internal/coordinator/signingkey.Manager.Sign against
// [StartRequest.CanonicalBytes] — this package holds no private key and no
// signing call, mirroring [pkg/fallbackprogram.SignedProgram]'s identical
// division of labor.
type SignedStartRequest struct {
	Request   StartRequest       `json:"request"`
	Signature coordsig.Signature `json:"signature"`
}

// Verify reports whether sr's signature verifies against publicKey for
// sr.Request's own canonical bytes, and that sr.Request is itself
// well-formed. It never re-derives any field from Request's contents
// beyond that: freshness (nonce reuse, IssuedAt staleness) is a caller
// concern, not this method's, mirroring
// [pkg/fallbackprogram.SignedProgram.Verify]'s identical "signature check,
// not a freshness check" boundary.
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
