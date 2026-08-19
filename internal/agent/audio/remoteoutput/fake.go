package remoteoutput

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// FakeDestination is a deterministic [RemoteOutput] double. It plays
// nothing and reaches no real service: it exists to prove ShowMesh's
// side of the AUDIO-ENGINE section 8.1 boundary, never to demonstrate
// that any destination accepts a format, completes an upload, finishes
// processing, stays synchronized, or plays on a phone.
type FakeDestination struct {
	now func() time.Time

	mu       sync.Mutex
	caps     Capabilities
	evidence *EvidenceStore
	revState *pkgaudio.RevisionState

	// NoStatusInterface, when true, makes Provision stop at
	// [ProvisioningAttempted]: the destination never returns an
	// acknowledgement, matching the "no status interface" profile in
	// AUDIO-ENGINE section 8.1.
	NoStatusInterface bool

	// FailTransfer, when true, makes every Provision call resolve
	// [ProvisioningFailed].
	FailTransfer bool

	lastApplied State
	log         []string
}

// NewFakeDestination returns a FakeDestination with caps as its
// capability profile, using now for evidence and observation timestamps.
func NewFakeDestination(now func() time.Time, caps Capabilities) *FakeDestination {
	return &FakeDestination{
		now:      now,
		caps:     caps,
		evidence: NewEvidenceStore(),
		revState: pkgaudio.NewRevisionState("remoteoutput.fake"),
	}
}

// CallLog returns every method call this fake has recorded, in order,
// for a test asserting what was and was not invoked (in particular, that
// no Provision call happened as a side effect of a playout command — see
// AUDIO-ENGINE section 8.1's provisioning-timing rule).
func (f *FakeDestination) CallLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.log))
	copy(cp, f.log)
	return cp
}

func (f *FakeDestination) record(entry string) {
	f.log = append(f.log, entry)
}

// SetCapabilities replaces this fake's declared capability profile.
func (f *FakeDestination) SetCapabilities(caps Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = caps
}

// Capabilities implements [PlayoutOutput].
func (f *FakeDestination) Capabilities() Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	caps := f.caps
	if caps.AcceptedFormats != nil {
		formats := make(map[string]Support, len(caps.AcceptedFormats))
		for k, v := range caps.AcceptedFormats {
			formats[k] = v
		}
		caps.AcceptedFormats = formats
	}
	return caps
}

// Provision implements [Provisioner]. It never runs because a playback
// command arrived — nothing in this fake's Apply/Observe path calls it.
func (f *FakeDestination) Provision(ctx context.Context, trigger Trigger, dest Destination, media pkgaudio.MediaRef) (ProvisioningRecord, error) {
	if err := trigger.Validate(); err != nil {
		return ProvisioningRecord{}, err
	}
	if err := dest.Validate(); err != nil {
		return ProvisioningRecord{}, err
	}
	if err := media.Validate(); err != nil {
		return ProvisioningRecord{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("provision")

	rec := ProvisioningRecord{
		Destination: dest,
		ContentHash: media.ContentHash,
		ObservedAt:  f.now(),
	}
	switch {
	case f.FailTransfer:
		rec.State = ProvisioningFailed
		rec.Reason = "transfer failed"
	case f.NoStatusInterface:
		rec.State = ProvisioningAttempted
		rec.Reason = "no status interface: destination cannot confirm receipt"
	default:
		rec.State = ProvisioningAcknowledged
		rec.RemoteMediaID = fmt.Sprintf("fake-%s-%s", dest.ID, media.ContentHash)
	}
	f.evidence.Record(rec)
	return rec, nil
}

// ProvisioningStatus implements [Provisioner], returning
// [ProvisioningNotAttempted] when nothing has been recorded for dest and
// contentHash.
func (f *FakeDestination) ProvisioningStatus(ctx context.Context, dest Destination, contentHash string) (ProvisioningRecord, error) {
	if err := dest.Validate(); err != nil {
		return ProvisioningRecord{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("provisioning_status")
	if rec, ok := f.evidence.Get(dest, contentHash); ok {
		return rec, nil
	}
	return ProvisioningRecord{Destination: dest, ContentHash: contentHash, State: ProvisioningNotAttempted, ObservedAt: f.now()}, nil
}

// RecordManualVerification implements [Provisioner].
func (f *FakeDestination) RecordManualVerification(ctx context.Context, v ManualVerification) error {
	if err := v.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("manual_verification")
	f.evidence.Record(ProvisioningRecord{
		Destination: v.Destination,
		ContentHash: v.ContentHash,
		State:       ProvisioningManuallyVerified,
		ObservedAt:  v.VerifiedAt,
		Reason:      v.Note,
	})
	return nil
}

// Evidence returns the [EvidenceStore] backing this fake, for a test
// evaluating [EvidenceStore.Coverage] directly.
func (f *FakeDestination) Evidence() *EvidenceStore {
	return f.evidence
}

// checkCapabilities reports the dispatch outcome for st against this
// fake's declared capabilities: [pkgaudio.OutcomeRefused] for the first
// requested behavior whose capability is not [SupportSupported],
// otherwise [pkgaudio.OutcomeStarted]. unsupported and unknown are
// refused identically, per [Support.RequireSupported].
func checkCapabilities(caps Capabilities, st State) pkgaudio.OutcomeResult {
	refuse := func(capability string, s Support) (pkgaudio.OutcomeResult, bool) {
		if err := s.RequireSupported(capability); err != nil {
			return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: err.Error()}, true
		}
		return pkgaudio.OutcomeResult{}, false
	}

	if st.Playlist != nil {
		if r, refused := refuse("playlists", caps.Playlists); refused {
			return r
		}
		switch st.Playlist.RequestedTransition {
		case pkgaudio.ItemTransitionGapless:
			if r, refused := refuse("gapless", caps.Gapless); refused {
				return r
			}
		case pkgaudio.ItemTransitionCrossfade:
			if r, refused := refuse("crossfade", caps.Crossfade); refused {
				return r
			}
		default:
			if r, refused := refuse("sequential", caps.Sequential); refused {
				return r
			}
		}
	}
	if st.SourceRole == pkgaudio.SourceRoleAnnouncement {
		if r, refused := refuse("announcements", caps.Announcements); refused {
			return r
		}
	}
	if st.Loop != pkgaudio.RepeatNone {
		if r, refused := refuse("looping", caps.Looping); refused {
			return r
		}
	}
	if st.Fade != nil {
		if r, refused := refuse("gain_fades", caps.GainFades); refused {
			return r
		}
	}
	if st.Ducking {
		if r, refused := refuse("ducking", caps.Ducking); refused {
			return r
		}
	}
	if st.Mixing {
		if r, refused := refuse("mixing", caps.Mixing); refused {
			return r
		}
	}
	if st.Seek != nil {
		if r, refused := refuse("seeking", caps.Seeking); refused {
			return r
		}
	}
	return pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeStarted}
}

// Apply implements [PlayoutOutput]. It never provisions anything.
func (f *FakeDestination) Apply(ctx context.Context, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, st State) (pkgaudio.RevisionDecision, Observation, error) {
	decision := f.revState.Apply(invocation, revision)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("apply")

	if !decision.Accepted {
		return decision, Observation{State: f.lastApplied, ObservedAt: f.now(), Result: *decision.Result}, nil
	}

	if err := st.Validate(); err != nil {
		return decision, Observation{}, err
	}

	result := checkCapabilities(f.caps, st)
	f.lastApplied = st
	return decision, Observation{State: st, ObservedAt: f.now(), Result: result}, nil
}

// Observe implements [PlayoutOutput]. When [Capabilities.PositionReporting]
// is not [SupportSupported] it reports position as zero and
// [pkgaudio.OutcomeUnconfirmable], because a destination that cannot
// report position must never have one attributed to it.
func (f *FakeDestination) Observe(ctx context.Context) (Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("observe")

	st := f.lastApplied
	if f.caps.PositionReporting != SupportSupported {
		st.Position = 0
		return Observation{
			State:      st,
			ObservedAt: f.now(),
			Result:     pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeUnconfirmable, Reason: "position reporting is not supported"},
		}, nil
	}
	return Observation{State: st, ObservedAt: f.now(), Result: pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomePosition}}, nil
}
