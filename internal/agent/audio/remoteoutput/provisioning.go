package remoteoutput

import (
	"context"
	"errors"
	"fmt"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// ProvisioningState is the AUDIO-ENGINE section 8.1 generic evidence
// vocabulary, deliberately weaker than node-local readiness. An upload
// attempt is never Ready — no such member exists here, and a real
// integration that exposes one adds it on top of this package rather
// than inside it.
type ProvisioningState string

const (
	ProvisioningNotAttempted     ProvisioningState = "not_attempted"
	ProvisioningAttempted        ProvisioningState = "attempted"
	ProvisioningAcknowledged     ProvisioningState = "acknowledged"
	ProvisioningManuallyVerified ProvisioningState = "manually_verified"
	ProvisioningFailed           ProvisioningState = "failed"
	ProvisioningUnknown          ProvisioningState = "unknown"
)

var provisioningStates = map[ProvisioningState]struct{}{
	ProvisioningNotAttempted: {}, ProvisioningAttempted: {}, ProvisioningAcknowledged: {},
	ProvisioningManuallyVerified: {}, ProvisioningFailed: {}, ProvisioningUnknown: {},
}

// ErrUnknownProvisioningState is returned by [ProvisioningState.Validate]
// for a value outside the six-member vocabulary.
var ErrUnknownProvisioningState = errors.New("remoteoutput: provisioning state is not a member of this closed vocabulary")

// Validate reports whether s is one of the six reserved provisioning
// states.
func (s ProvisioningState) Validate() error {
	if _, ok := provisioningStates[s]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownProvisioningState, s)
	}
	return nil
}

// Trigger names the one of four events allowed to start advance
// provisioning. There is deliberately no trigger naming a playback
// command: AUDIO-ENGINE section 8.1 requires that provisioning never
// begins because start arrived, and [Provisioner.Provision] refuses any
// value outside this vocabulary.
type Trigger string

const (
	TriggerAssetIngested        Trigger = "asset_ingested"
	TriggerDestinationAssigned  Trigger = "destination_assigned"
	TriggerConfigurationChanged Trigger = "configuration_changed"
	TriggerRetry                Trigger = "retry"
)

var triggers = map[Trigger]struct{}{
	TriggerAssetIngested: {}, TriggerDestinationAssigned: {}, TriggerConfigurationChanged: {}, TriggerRetry: {},
}

// ErrUnknownTrigger is returned by [Trigger.Validate] for a value outside
// the four reserved provisioning triggers.
var ErrUnknownTrigger = errors.New("remoteoutput: provisioning trigger is not one of the four reserved triggers")

// Validate reports whether t is one of the four reserved triggers.
func (t Trigger) Validate() error {
	if _, ok := triggers[t]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTrigger, t)
	}
	return nil
}

// Destination identifies one remote destination instance at one
// immutable configuration revision or fingerprint. Provisioning evidence
// and manual verification are both keyed by the full Destination value,
// so a configuration change is a different key rather than an update to
// the old one — see AUDIO-ENGINE section 8.1's expiry rule.
type Destination struct {
	ID             string
	ConfigRevision string
}

// ErrDestinationIncomplete is returned by [Destination.Validate] when ID
// or ConfigRevision is empty.
var ErrDestinationIncomplete = errors.New("remoteoutput: destination is missing its id or configuration revision")

// Validate reports whether d carries both identity fields.
func (d Destination) Validate() error {
	if d.ID == "" || d.ConfigRevision == "" {
		return ErrDestinationIncomplete
	}
	return nil
}

// ProvisioningRecord is one destination's evidence for one exact content
// hash, keyed by [Destination] plus ContentHash.
type ProvisioningRecord struct {
	Destination   Destination
	ContentHash   string
	State         ProvisioningState
	RemoteMediaID string // optional: set only when the destination exposes one
	ObservedAt    time.Time
	Reason        string
}

// ManualVerification is an operator's attestation that one destination,
// at one configuration revision, audibly reproduced one exact content
// hash. Recording one produces a [ProvisioningRecord] with State
// [ProvisioningManuallyVerified] keyed by the same Destination and
// ContentHash, so it participates in coverage evaluation identically to
// any other evidence and expires the same way: a later Destination or
// ContentHash simply does not match this key.
type ManualVerification struct {
	Destination Destination
	ContentHash string
	Operator    string
	VerifiedAt  time.Time
	Note        string
}

// ErrManualVerificationIncomplete is returned by
// [ManualVerification.Validate] when Destination, ContentHash, or
// Operator is empty.
var ErrManualVerificationIncomplete = errors.New("remoteoutput: manual verification is missing destination, content hash, or operator")

// Validate reports whether v is well-formed.
func (v ManualVerification) Validate() error {
	if err := v.Destination.Validate(); err != nil {
		return err
	}
	if v.ContentHash == "" || v.Operator == "" {
		return ErrManualVerificationIncomplete
	}
	return nil
}

// Provisioner runs advance media provisioning: making a ShowMesh asset
// available to a destination before playback. Nothing in this interface
// accepts a playback command — see [PlayoutOutput] — so a type holding
// only a Provisioner cannot be driven by "start" and a type holding only
// a PlayoutOutput cannot provision.
type Provisioner interface {
	// Provision runs one provisioning attempt for media against dest,
	// caused by trigger. Provision refuses an invalid trigger, dest, or
	// media without contacting the destination.
	Provision(ctx context.Context, trigger Trigger, dest Destination, media pkgaudio.MediaRef) (ProvisioningRecord, error)

	// ProvisioningStatus returns the destination's current evidence for
	// dest and contentHash, or [ProvisioningNotAttempted] if none exists.
	// A destination with no status interface returns
	// [ProvisioningUnknown] with a reason rather than an error: absence
	// of a status API is supported behavior, not an adapter defect.
	ProvisioningStatus(ctx context.Context, dest Destination, contentHash string) (ProvisioningRecord, error)

	// RecordManualVerification stores v as current evidence for its
	// Destination and ContentHash.
	RecordManualVerification(ctx context.Context, v ManualVerification) error
}
