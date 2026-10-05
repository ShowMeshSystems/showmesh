// Package fallbackactivation is the wire shape of ADR-048 decision 3's
// fallback activation: the request an FPP host signs with its executor
// key, the outcome vocabulary a node answers with, and the one function
// both sides derive the signed bytes with. See section 5 of
// docs/build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md.
package fallbackactivation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// SchemaVersion is the only request schema version a node accepts.
const SchemaVersion = 1

// Runner is the runner name a fallback activation carries into the
// normal Cue activation path.
const Runner = "fpp-fallback"

// The two node listener paths. ProgramPathPrefix is followed by the FPP
// instance UUID.
const (
	ProgramPathPrefix = "/showmesh/v1/fallback/programs/"
	ActivationPath    = "/showmesh/v1/fallback/activations"
)

// Body size limits for the two routes.
const (
	MaxActivationBodyBytes = 4 << 10
	MaxProgramBodyBytes    = 1 << 20
)

// Outcome is one word of the response vocabulary.
type Outcome string

const (
	OutcomeInstalled               Outcome = "installed"
	OutcomeAuthorized              Outcome = "authorized"
	OutcomeMalformedRequest        Outcome = "malformed-request"
	OutcomeTooLarge                Outcome = "too-large"
	OutcomeRateLimited             Outcome = "rate-limited"
	OutcomeNoCoordinatorKey        Outcome = "no-coordinator-key"
	OutcomeStorageUnavailable      Outcome = "storage-unavailable"
	OutcomeNotReady                Outcome = "not-ready"
	OutcomeProgramSignatureInvalid Outcome = "program-signature-invalid"
	OutcomeProgramUnsupported      Outcome = "program-unsupported"
	OutcomeWrongFPPHost            Outcome = "wrong-fpp-host"
	OutcomeWrongTarget             Outcome = "wrong-target"
	OutcomeProgramExpired          Outcome = "program-expired"
	OutcomeProgramSuperseded       Outcome = "program-superseded"
	OutcomeProgramNotInstalled     Outcome = "program-not-installed"
	OutcomeProgramNotCurrent       Outcome = "program-not-current"
	OutcomeExecutorNotEnrolled     Outcome = "executor-not-enrolled"
	OutcomeSignatureInvalid        Outcome = "signature-invalid"
	OutcomeUnknownEntry            Outcome = "unknown-entry"
	OutcomeCueNotAuthorized        Outcome = "cue-not-authorized"
	OutcomeStaleGeneration         Outcome = "stale-generation"
	OutcomeStaleCatalog            Outcome = "stale-catalog"
	OutcomeReplayedExecution       Outcome = "replayed-execution"
	OutcomeApplyFailed             Outcome = "apply-failed"
)

// Request is what the FPP host signs. Every member is required.
type Request struct {
	SchemaVersion    int       `json:"schemaVersion"`
	ExecutionID      string    `json:"executionId"`
	FPPInstanceUUID  string    `json:"fppInstanceUuid"`
	PackageID        string    `json:"packageId"`
	PackageRevision  string    `json:"packageRevision"`
	ProgramExpiresAt time.Time `json:"programExpiresAt"`
	Generation       int64     `json:"generation"`
	CatalogRevision  string    `json:"catalogRevision"`
	EntryKey         string    `json:"entryKey"`
	CueID            string    `json:"cueId"`
	CueRevision      int64     `json:"cueRevision"`
	NodeID           string    `json:"nodeId"`
}

// requestMembers is every member a request must carry, and the only ones
// it may.
var requestMembers = []string{
	"schemaVersion", "executionId", "fppInstanceUuid", "packageId", "packageRevision", "programExpiresAt",
	"generation", "catalogRevision", "entryKey", "cueId", "cueRevision", "nodeId",
}

// CanonicalBytes is the RFC 8785 canonical form of r, the bytes a signer
// signs.
func (r Request) CanonicalBytes() ([]byte, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("fallbackactivation: marshal request: %w", err)
	}
	canonical, _, err := fppidentity.HashCanonical(raw)
	if err != nil {
		return nil, fmt.Errorf("fallbackactivation: canonicalize request: %w", err)
	}
	return canonical, nil
}

// Sign returns the request body an FPP host sends: r and the executor
// key's signature over its canonical bytes.
func Sign(r Request, executorKey ed25519.PrivateKey) ([]byte, error) {
	canonical, err := r.CanonicalBytes()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Request   json.RawMessage    `json:"request"`
		Signature coordsig.Signature `json:"signature"`
	}{Request: canonical, Signature: ed25519.Sign(executorKey, canonical)})
}

// Received is a decoded request body. Canonical is the canonical form of
// the "request" object exactly as it arrived, which is what the
// signature must be checked against.
type Received struct {
	Request   Request
	Canonical []byte
	Signature coordsig.Signature
}

// ErrMalformed marks every way a body can fail to be the fixed shape.
var ErrMalformed = errors.New("fallbackactivation: malformed request")

// Decode reads a request body, refusing any member this package does not
// name, any missing member, and any schema version but [SchemaVersion].
// It checks no signature.
func Decode(body []byte) (Received, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Received{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	rawRequest, hasRequest := envelope["request"]
	rawSignature, hasSignature := envelope["signature"]
	if len(envelope) != 2 || !hasRequest || !hasSignature {
		return Received{}, fmt.Errorf("%w: the body must have exactly request and signature", ErrMalformed)
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(rawRequest, &members); err != nil {
		return Received{}, fmt.Errorf("%w: request is not an object", ErrMalformed)
	}
	for _, name := range requestMembers {
		if _, ok := members[name]; !ok {
			return Received{}, fmt.Errorf("%w: request is missing %s", ErrMalformed, name)
		}
	}
	if len(members) != len(requestMembers) {
		return Received{}, fmt.Errorf("%w: request has a member this route does not accept", ErrMalformed)
	}

	var out Received
	dec := json.NewDecoder(bytes.NewReader(rawRequest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out.Request); err != nil {
		return Received{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if err := out.Request.validate(); err != nil {
		return Received{}, err
	}
	if err := json.Unmarshal(rawSignature, &out.Signature); err != nil {
		return Received{}, fmt.Errorf("%w: signature is not base64", ErrMalformed)
	}
	canonical, _, err := fppidentity.HashCanonical(rawRequest)
	if err != nil {
		return Received{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	out.Canonical = canonical
	return out, nil
}

func (r Request) validate() error {
	switch {
	case r.SchemaVersion != SchemaVersion:
		return fmt.Errorf("%w: schemaVersion must be %d", ErrMalformed, SchemaVersion)
	case !validExecutionID(r.ExecutionID):
		return fmt.Errorf("%w: executionId must be a lowercase UUID", ErrMalformed)
	case r.FPPInstanceUUID == "", r.PackageID == "", r.PackageRevision == "",
		r.CatalogRevision == "", r.EntryKey == "", r.CueID == "", r.NodeID == "":
		return fmt.Errorf("%w: a required member is empty", ErrMalformed)
	case r.ProgramExpiresAt.IsZero():
		return fmt.Errorf("%w: programExpiresAt is required", ErrMalformed)
	case r.Generation < 1 || r.CueRevision < 1:
		return fmt.Errorf("%w: generation and cueRevision must be positive", ErrMalformed)
	}
	return nil
}

// Verify checks the signature against the executor public key.
func (r Received) Verify(executorKey ed25519.PublicKey) error {
	return r.Signature.Verify(r.Canonical, executorKey)
}

// validExecutionID reports whether id is a lowercase UUID in its usual
// 8-4-4-4-12 written form.
func validExecutionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
