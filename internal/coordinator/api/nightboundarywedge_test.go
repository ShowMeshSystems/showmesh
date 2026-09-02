package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Investigation, then regression suite, for nightAdvanceRestingIntershow's
// invalid-boundary handling (nightloop.go's own persisted-invalid check,
// originally at line 345 before this file's own fix added the retry it now
// gates). The original question: is that branch reachable permanently, and
// by only one of the two ways a boundary becomes invalid?
//
// Correction to the original claim, found while building the first version
// of this test: the early return did NOT fire on every tick forever. Once
// nightDegradeSession sets Degraded, nightTick's own top-level guard
// (nightloop.go:115, "if rec.Degraded && rec.State != nightStateFadingOut
// { return }") stops nightAdvanceRestingIntershow from being called again
// at all, so the persisted-invalid check fired exactly once.
//
// A second correction, found only by mutating the test itself: the guard was
// NOT what made the wedge permanent. Removing it (before this fix existed)
// left this test still passing, because nothing else re-derived the
// boundary (ObservedAt stayed set) and nothing cleared Degraded. Permanence
// came from that absence, not from the guard. What the guard actually
// contributes is breadth: with it in place, a degraded session advances
// NOTHING at all, not just resting-intershow's own recheck - proven
// separately by TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction,
// the one test in this file whose mutation-tested failure genuinely depends
// on the guard.
//
// THE FIX this file now also regression-tests: a derivation-kind invalid
// boundary (deriveNightBoundary's own arithmetic came back invalid; nothing
// contradicted anything) gets nightDerivationInvalidRetryLimit retries from
// fresh observation before degrading for real. A contradiction-kind
// invalidation, and an unclassified one (Kind empty, which is what every
// boundary persisted before this fix decodes to), still never retries -
// exactly the prior behavior, preserved on purpose. Of the three tests in
// this file: TestNightAdvanceRestingIntershow_ContradictionInvalidationSelfHeals
// and TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction are
// UNCHANGED by the fix and still pass unmodified, because neither touches
// the derivation retry path. TestNightAdvanceRestingIntershow_DerivedInvalidBoundaryRetriesThenWedgesWhenEvidenceNeverAgrees
// (renamed from ...WedgesSessionPermanently, which no longer described what
// it asserts once retries exist) is the one MODIFIED test: its fixture is
// unchanged, still a position permanently past duration, so retrying
// genuinely accomplishes nothing, but its tick script now drives through
// every retry before the same eventual wedge, rather than asserting an
// immediate one.

// sequenceNameObservation reports fpp.sequence.name (the currently playing
// item), matching statusObservation/playlistNameObservation's own shape.
// No existing test file needed this signal on its own before.
func sequenceNameObservation(instanceID, item string, collectedAt time.Time) observation.Observation {
	observedAt := collectedAt
	return observation.Observation{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		Signal:   observation.SignalID(nightSignalSequenceName), Value: item,
		ObservedAt: &observedAt, CollectedAt: collectedAt, Source: "fpp-rest",
		Quality: observation.QualityDirect, ValidFor: time.Minute,
	}
}

// TestNightAdvanceRestingIntershow_DerivedInvalidBoundaryRetriesThenWedgesWhenEvidenceNeverAgrees
// is the claim's own scenario: an anchor re-derived from fresh observed
// evidence whose position is already past the resolved duration
// (deriveNightBoundary's third invalid route). The anchor is seeded with
// its own DurationMS already known (as a real anchor invalidated by a
// contradiction, or one carried over a purpose match, would be) so the
// re-derive branch (nightloop.go:314-317) never needs to resolve an FSEQ
// asset - this test is only about what happens once that pair persists.
// The observation lister is never updated with new evidence, so every
// retry sees the identical, still-invalid position: this proves the bounded
// retry genuinely gives up rather than spinning forever, not that it
// recovers (TestNightAdvanceRestingIntershow_DerivationRetryRecoversWhenEvidenceLaterAgrees,
// below, is the recovery half).
func TestNightAdvanceRestingIntershow_DerivedInvalidBoundaryRetriesThenWedgesWhenEvidenceNeverAgrees(t *testing.T) {
	h, st, _, obsLister := nightBackgroundAudioTestHandlers(t)
	var logBuf bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logBuf, nil))

	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "bed-node", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "bed-node", "asset-2")
	ba := twoItemBackgroundAudioConfig("bed-node", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "bed-node", ba, nightStateRestingIntershow)

	t0 := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	pending := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "fpp-main", Playlist: "halloween-resting",
		DurationMS: 60000, DispatchedAt: t0, // ObservedAt deliberately zero: dispatched, not yet observed.
	}
	rec.StateEnteredAt = t0
	rec.ContentAnchorJSON = encodeNightContentAnchor(pending)
	if err := st.UpdateNightSession(context.Background(), rec, t0); err != nil {
		t.Fatalf("seed pending anchor: %v", err)
	}

	// Tick 1: fresh evidence arrives with the position already past the
	// 60s duration (70s in). This is deriveNightBoundary's third invalid
	// route, reached through the re-derive branch, not a contradiction.
	tick1 := t0.Add(2 * time.Second)
	obsLister.setObs([]observation.Observation{
		statusObservation("fpp-main", fppStatusValuePlaying, tick1),
		playlistNameObservation("fpp-main", "halloween-resting", tick1),
		positionMSObservation("fpp-main", 70000, tick1),
	})
	h.nightTick(context.Background(), tick1)

	got := mustGetCurrentSession(t, st)
	anchor, ok := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !ok || anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor after tick 1 = %+v, ok=%v; claim 2 requires ObservedAt to be set", anchor, ok)
	}
	if !anchor.ObservedAt.Equal(tick1) {
		t.Fatalf("anchor.ObservedAt = %v, want %v", anchor.ObservedAt, tick1)
	}
	boundary, ok := decodeNightBoundary(got.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary after tick 1 = %+v, ok=%v, want state=invalid; claim 2 refuted if this pair cannot be produced", boundary, ok)
	}
	if !strings.Contains(boundary.Reason, "past the asset's own duration") {
		t.Fatalf("boundary.Reason = %q, want it to name the past-duration route", boundary.Reason)
	}
	if boundary.Kind != nightBoundaryKindDerivation {
		t.Fatalf("boundary.Kind = %q, want %q", boundary.Kind, nightBoundaryKindDerivation)
	}
	if got.Degraded {
		t.Fatal("session degraded on the same tick the boundary was derived invalid; want degrade to wait for the NEXT tick's own check")
	}

	// Ticks 2 through 2*limit+1: the persisted-invalid check now retries a
	// derivation-kind boundary before degrading. Each retry is two ticks -
	// one that clears ObservedAt to force fresh evidence, one that
	// re-derives from it. The observation lister is refreshed before each
	// re-derive tick with the SAME still-past-duration position, but a
	// current CollectedAt - a real retry receives genuinely fresh telemetry
	// each poll, and this must come back invalid because that position
	// truly does not fit, not because a stale reading (ValidFor: time.Minute
	// on the original observations) silently aged out partway through the
	// loop.
	tick := tick1
	for attempt := 1; attempt <= nightDerivationInvalidRetryLimit; attempt++ {
		// The retry tick: attempts increments, ObservedAt clears, boundary
		// reads unknown while fresh evidence is pending - not degraded.
		tick = tick.Add(30 * time.Second)
		h.nightTick(context.Background(), tick)

		got = mustGetCurrentSession(t, st)
		if got.Degraded {
			t.Fatalf("session degraded during retry attempt %d of %d, want it to keep retrying first", attempt, nightDerivationInvalidRetryLimit)
		}
		anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
		if !anchor.ObservedAt.IsZero() {
			t.Fatalf("anchor.ObservedAt = %v on retry attempt %d, want zeroed (fresh evidence pending)", anchor.ObservedAt, attempt)
		}
		if anchor.DerivationInvalidAttempts != attempt {
			t.Fatalf("DerivationInvalidAttempts = %d after retry attempt %d, want %d", anchor.DerivationInvalidAttempts, attempt, attempt)
		}
		boundary, _ = decodeNightBoundary(got.BoundaryJSON)
		if boundary.State != nightBoundaryStateUnknown {
			t.Fatalf("boundary state = %q during retry attempt %d, want unknown while fresh evidence is pending", boundary.State, attempt)
		}

		// The re-derive tick: fresh evidence is the SAME bad position, so
		// this comes back invalid again, still derivation-kind, still not
		// degraded - the retry decision is made on the NEXT tick, not this
		// one.
		tick = tick.Add(2 * time.Second)
		obsLister.setObs([]observation.Observation{
			statusObservation("fpp-main", fppStatusValuePlaying, tick),
			playlistNameObservation("fpp-main", "halloween-resting", tick),
			positionMSObservation("fpp-main", 70000, tick),
		})
		h.nightTick(context.Background(), tick)

		got = mustGetCurrentSession(t, st)
		if got.Degraded {
			t.Fatalf("session degraded on the re-derive tick of attempt %d, want the retry decision deferred to the NEXT tick", attempt)
		}
		anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
		if !anchor.ObservedAt.Equal(tick) {
			t.Fatalf("anchor.ObservedAt = %v after re-deriving on attempt %d, want %v", anchor.ObservedAt, attempt, tick)
		}
		if anchor.DerivationInvalidAttempts != attempt {
			t.Fatalf("DerivationInvalidAttempts = %d after re-deriving on attempt %d, want it carried forward unchanged at %d", anchor.DerivationInvalidAttempts, attempt, attempt)
		}
		boundary, _ = decodeNightBoundary(got.BoundaryJSON)
		if boundary.State != nightBoundaryStateInvalid || boundary.Kind != nightBoundaryKindDerivation {
			t.Fatalf("boundary after re-deriving on attempt %d = %+v, want state=invalid kind=%q", attempt, boundary, nightBoundaryKindDerivation)
		}
	}
	// The last re-derive tick's own ObservedAt: the degrade tick below
	// never re-derives, so this is the value that must survive unchanged
	// from here on.
	lastObservedAt := tick

	// One more tick: attempts now equals the limit, so this is the tick
	// that finally gives up and degrades - the honest end state for
	// evidence that genuinely never agrees.
	tick = tick.Add(30 * time.Second)
	h.nightTick(context.Background(), tick)

	got = mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatalf("session not degraded after exhausting all %d retries", nightDerivationInvalidRetryLimit)
	}
	degradedReason := got.DegradedReason
	if !strings.Contains(degradedReason, "invalid") {
		t.Fatalf("DegradedReason = %q, want it to name the invalid boundary", degradedReason)
	}
	limitText := fmt.Sprintf("%d automatic re-derive attempts", nightDerivationInvalidRetryLimit)
	if !strings.Contains(degradedReason, limitText) {
		t.Fatalf("DegradedReason = %q, want it to name the exhausted retry count (%q)", degradedReason, limitText)
	}
	anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !anchor.ObservedAt.Equal(lastObservedAt) {
		t.Fatalf("anchor.ObservedAt = %v after degrade, want unchanged at %v (a degraded session never re-derives)", anchor.ObservedAt, lastObservedAt)
	}
	boundary, _ = decodeNightBoundary(got.BoundaryJSON)
	if boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %q after degrade, want still invalid", boundary.State)
	}

	// Across a simulated long gap: the session stays wedged with no path
	// out, now correctly earned only after genuinely exhausting its
	// retries. (A background-audio dispatch count was tried here as a
	// canary for "the guard blocks everything, not just resting-intershow's
	// own recheck" and dropped: measured against a temporarily-disabled
	// guard, the count stayed flat either way, because this fixture's
	// background audio reaches a static per-node state after its first
	// dispatch regardless of Degraded. See
	// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction below for
	// the assertion that actually discriminates the guard.)
	for _, gap := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour} {
		tick = tick.Add(gap)
		h.nightTick(context.Background(), tick)

		got = mustGetCurrentSession(t, st)
		if got.State != nightStateRestingIntershow {
			t.Fatalf("state = %q at %v, want still resting-intershow (no path out)", got.State, tick)
		}
		if !got.Degraded || got.DegradedReason != degradedReason {
			t.Fatalf("degraded=%v reason=%q at %v, want unchanged degraded=true reason=%q", got.Degraded, got.DegradedReason, tick, degradedReason)
		}
		anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
		if !anchor.ObservedAt.Equal(lastObservedAt) {
			t.Fatalf("anchor.ObservedAt = %v at %v, want unchanged at %v", anchor.ObservedAt, tick, lastObservedAt)
		}
		boundary, _ = decodeNightBoundary(got.BoundaryJSON)
		if boundary.State != nightBoundaryStateInvalid {
			t.Fatalf("boundary state = %q at %v, want still invalid", boundary.State, tick)
		}
	}

	// nightDegradeSession is one-shot, so "session degraded" appears exactly
	// once, on the tick that exhausts the retry budget - not once per retry,
	// and not once per tick in the long-gap loop above. Note what this does
	// and does not show: it cannot tell a guard-blocked path apart from a
	// silently idempotent re-degrade. Once attempts reaches the limit, that
	// count itself (persisted on the anchor) is what keeps every later tick
	// from retrying again, with or without nightloop.go:115's own guard -
	// the guard's own contribution is breadth (it blocks every OTHER
	// action too, not just this one), which
	// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction is what
	// actually proves.
	if n := strings.Count(logBuf.String(), "session degraded"); n != 1 {
		t.Fatalf("\"session degraded\" logged %d times across all ticks, want exactly 1", n)
	}
}

// TestNightAdvanceRestingIntershow_DerivationRetryRecoversWhenEvidenceLaterAgrees
// is the actual proof the ruling asked for: a derivation-invalid boundary
// whose SECOND observation, still inside the retry budget, is corrected
// evidence rather than the same bad reading. The session must recover to
// armed and never degrade at all - this is what "build the automatic
// recovery for the derivation-invalid case" means in practice, not just
// "retry before giving up."
func TestNightAdvanceRestingIntershow_DerivationRetryRecoversWhenEvidenceLaterAgrees(t *testing.T) {
	h, st, _, obsLister := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "bed-node", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "bed-node", "asset-2")
	ba := twoItemBackgroundAudioConfig("bed-node", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "bed-node", ba, nightStateRestingIntershow)

	t0 := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	pending := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "fpp-main", Playlist: "halloween-resting",
		DurationMS: 60000, DispatchedAt: t0,
	}
	rec.StateEnteredAt = t0
	rec.ContentAnchorJSON = encodeNightContentAnchor(pending)
	if err := st.UpdateNightSession(context.Background(), rec, t0); err != nil {
		t.Fatalf("seed pending anchor: %v", err)
	}

	// Tick 1: the same bad reading as the wedge test above, position past
	// the 60s duration - derives invalid.
	tick1 := t0.Add(2 * time.Second)
	obsLister.setObs([]observation.Observation{
		statusObservation("fpp-main", fppStatusValuePlaying, tick1),
		playlistNameObservation("fpp-main", "halloween-resting", tick1),
		positionMSObservation("fpp-main", 70000, tick1),
	})
	h.nightTick(context.Background(), tick1)

	got := mustGetCurrentSession(t, st)
	boundary, ok := decodeNightBoundary(got.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateInvalid || boundary.Kind != nightBoundaryKindDerivation {
		t.Fatalf("boundary after tick 1 = %+v, ok=%v, want state=invalid kind=%q", boundary, ok, nightBoundaryKindDerivation)
	}

	// Tick 2: the retry-decision tick. Clears ObservedAt, attempts becomes
	// 1, not degraded - identical to the wedge test up to this point.
	tick2 := tick1.Add(30 * time.Second)
	h.nightTick(context.Background(), tick2)

	got = mustGetCurrentSession(t, st)
	if got.Degraded {
		t.Fatal("session degraded on the retry-decision tick, want it retrying first")
	}
	anchor, _ := decodeNightContentAnchor(got.ContentAnchorJSON)
	if anchor.DerivationInvalidAttempts != 1 {
		t.Fatalf("DerivationInvalidAttempts = %d after tick 2, want 1", anchor.DerivationInvalidAttempts)
	}

	// Tick 3: still inside the retry budget (limit is 3; this is attempt
	// 1's own re-derive), but the evidence has genuinely changed - whatever
	// was wrong resolved on its own, and the position now reported is well
	// within the 60s duration.
	tick3 := tick2.Add(2 * time.Second)
	obsLister.setObs([]observation.Observation{
		statusObservation("fpp-main", fppStatusValuePlaying, tick3),
		playlistNameObservation("fpp-main", "halloween-resting", tick3),
		positionMSObservation("fpp-main", 15000, tick3),
	})
	h.nightTick(context.Background(), tick3)

	got = mustGetCurrentSession(t, st)
	boundary, ok = decodeNightBoundary(got.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateArmed {
		t.Fatalf("boundary after corrected evidence = %+v, ok=%v, want state=armed; a derivation retry must recover when fresh evidence agrees", boundary, ok)
	}
	anchor, ok = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !ok || anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor after recovery = %+v, ok=%v, want a fresh ObservedAt", anchor, ok)
	}
	if !anchor.ObservedAt.Equal(tick3) {
		t.Fatalf("anchor.ObservedAt = %v, want %v", anchor.ObservedAt, tick3)
	}
	if anchor.DerivationInvalidAttempts != 0 {
		t.Fatalf("DerivationInvalidAttempts = %d after a successful re-derivation, want reset to 0", anchor.DerivationInvalidAttempts)
	}
	if got.Degraded {
		t.Fatal("session degraded somewhere on the recovery path; a derivation retry that succeeds must never degrade")
	}

	// One more tick, well past the retry decision point: the session stays
	// armed and healthy, exactly like any other ordinary resting-intershow
	// tick - recovery is not a one-tick fluke.
	tick4 := tick3.Add(time.Minute)
	obsLister.setObs([]observation.Observation{
		statusObservation("fpp-main", fppStatusValuePlaying, tick4),
		playlistNameObservation("fpp-main", "halloween-resting", tick4),
		positionMSObservation("fpp-main", 45000, tick4),
	})
	h.nightTick(context.Background(), tick4)

	got = mustGetCurrentSession(t, st)
	if got.Degraded {
		t.Fatal("session degraded on a later, ordinary tick after recovery")
	}
	boundary, _ = decodeNightBoundary(got.BoundaryJSON)
	if boundary.State != nightBoundaryStateArmed {
		t.Fatalf("boundary state = %q on a later ordinary tick, want still armed", boundary.State)
	}
}

// TestNightAdvanceRestingIntershow_UnclassifiedInvalidBoundaryNeverRetries
// pins the ruling's own conservative default: a boundary persisted before
// Kind existed (or one this coordinator otherwise declined to classify)
// decodes with Kind at its Go zero value, "". That must read exactly like a
// contradiction - never retried, degrading on the very next tick - because
// every contradiction site clears the anchor's ObservedAt in the same
// commit as its invalid boundary, so an ambiguous boundary sitting here
// with ObservedAt still set is, in the fleet, overwhelmingly likely to BE a
// derivation this coordinator simply predates classifying, and inferring
// that from shape rather than an explicit stamp is exactly the guessing
// rule 3 exists to forbid. Flipping nightBoundaryRetryEligible's own
// default (treating "" as eligible) must make this test fail - verified by
// mutation, not merely asserted, before this was reported done.
func TestNightAdvanceRestingIntershow_UnclassifiedInvalidBoundaryNeverRetries(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(10 * time.Second)

	obs := &mutableObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}},
	}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	// An anchor with ObservedAt set, paired with an invalid boundary whose
	// Kind is left at the Go zero value - exactly what any boundary
	// persisted before this field existed decodes to.
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionMS: 400000, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: dispatchedAt, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		BoundaryJSON:      encodeNightBoundary(nightBoundary{State: nightBoundaryStateInvalid, Reason: "a pre-upgrade invalidation this test does not classify"}),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	seedNightSessionRevision(t, st, nightShutdownPayload())

	h.nightTick(context.Background(), now)

	got := mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatal("an unclassified invalid boundary was retried instead of degrading immediately; absent Kind must read as conservative")
	}
	anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !anchor.ObservedAt.Equal(dispatchedAt) {
		t.Fatalf("anchor.ObservedAt = %v, want unchanged at %v (never retried, so never cleared)", anchor.ObservedAt, dispatchedAt)
	}
	if anchor.DerivationInvalidAttempts != 0 {
		t.Fatalf("DerivationInvalidAttempts = %d, want 0 (never entered the retry path at all)", anchor.DerivationInvalidAttempts)
	}
}

// TestNightBoundaryRetryEligible pins the classification rule at the
// pure-function level, isolated from the full tick wiring above: only an
// explicit derivation stamp is eligible, and that includes every other kind
// this file writes, not just the empty/absent case.
func TestNightBoundaryRetryEligible(t *testing.T) {
	cases := []struct {
		name string
		kind string
		want bool
	}{
		{"derivation", nightBoundaryKindDerivation, true},
		{"contradiction", nightBoundaryKindContradiction, false},
		{"unresolved asset", nightBoundaryKindUnresolvedAsset, false},
		{"absent (pre-upgrade)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nightBoundaryRetryEligible(nightBoundary{State: nightBoundaryStateInvalid, Kind: tc.kind})
			if got != tc.want {
				t.Fatalf("nightBoundaryRetryEligible(Kind=%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction generalizes
// the wedge beyond resting-intershow's own recheck: nightTick's guard
// (nightloop.go:115) sits above its ENTIRE switch, including the separate
// background-audio switch, in every state, not only resting-intershow. A
// fresh preshow session with a never-yet-applied background-audio bed is
// TestNightTick_PreshowStartsBackgroundAudio's own proven case: its very
// first tick always dispatches (apply), which is what makes it usable as a
// discriminator here - unlike a canary measured many ticks into an
// already-applied session (tried in the test above and dropped: a
// background-audio bed that already reached its own per-node steady state
// stops dispatching on its own, with or without Degraded, so it cannot
// tell the two apart). Comparing the identical fixture with only Degraded
// flipped is what actually isolates the guard's effect.
func TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
	rec.Degraded = true
	rec.DegradedReason = "test: forced degraded to prove the top-level guard blocks background audio too"
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("seed degraded session: %v", err)
	}

	h.nightTick(context.Background(), testNow)

	if n := pub.count(); n != 0 {
		t.Fatalf("publish count = %d, want 0; a degraded session (not fading-out) must dispatch nothing on its very first eligible action, matching TestNightTick_PreshowStartsBackgroundAudio's identical, non-degraded fixture dispatching exactly 1", n)
	}
}

// TestNightAdvanceRestingIntershow_ContradictionInvalidationSelfHeals is the
// control: a CONTRADICTION invalidation (item mismatch), with playback
// still running so this is not the idle-stop case, must behave differently
// from the derivation-invalid case above - it must never degrade the
// session, and it must recover on a later tick once evidence agrees again.
// If either half fails, the distinction claim 1 rests on collapses.
func TestNightAdvanceRestingIntershow_ContradictionInvalidationSelfHeals(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(10 * time.Second)

	obs := &mutableObservationLister{}
	svc, st, _ := newTestIdentityServiceWithStore(t, func() time.Time { return now })
	deps := Dependencies{
		NightSessions: st, Observations: obs, Identity: svc, Config: st,
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}},
	}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return now }, logger: testLogger()}

	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionMS: 0, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateRestingIntershow, StateEnteredAt: dispatchedAt, Cycle: 1,
		ContentAnchorJSON: encodeNightContentAnchor(anchor),
		BoundaryJSON:      encodeNightBoundary(deriveNightBoundary(anchor)),
		Issuer:            store.NightSessionIssuer{PrincipalID: "p-1", PrincipalName: "operator-1"},
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	seedNightSessionRevision(t, st, nightShutdownPayload())

	// Tick 1: a different item now reports playing, but status is still
	// "playing" - a contradiction, and NOT the idle-stop case.
	now = dispatchedAt.Add(20 * time.Second)
	obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now),
		playlistNameObservation("player-01", "halloween-resting", now),
		sequenceNameObservation("player-01", "some-other-item.fseq", now),
	})
	h.nightTick(context.Background(), now)

	got := mustGetCurrentSession(t, st)
	boundary, ok := decodeNightBoundary(got.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary after item-mismatch contradiction = %+v, ok=%v, want state=invalid", boundary, ok)
	}
	if got.Degraded {
		t.Fatal("a contradiction with playback still running must never degrade the session; if it does, contradiction invalidations wedge too and the two kinds no longer differ")
	}
	anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor.ObservedAt = %v after invalidation, want zeroed (nightInvalidateAnchor)", anchor.ObservedAt)
	}

	// Tick 2: evidence agrees again - the original item, still playing,
	// position advancing from the cleared (zeroed) baseline.
	now = now.Add(10 * time.Second)
	obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValuePlaying, now),
		playlistNameObservation("player-01", "halloween-resting", now),
		sequenceNameObservation("player-01", "halloween-resting.fseq", now),
		positionMSObservation("player-01", 40000, now),
	})
	h.nightTick(context.Background(), now)

	got = mustGetCurrentSession(t, st)
	boundary, ok = decodeNightBoundary(got.BoundaryJSON)
	if !ok || boundary.State != nightBoundaryStateArmed {
		t.Fatalf("boundary after agreeing evidence = %+v, ok=%v, want state=armed; a contradiction invalidation must self-heal", boundary, ok)
	}
	anchor, ok = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !ok || anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor after recovery = %+v, ok=%v, want a fresh ObservedAt", anchor, ok)
	}
	if got.Degraded {
		t.Fatal("session degraded somewhere on the self-healing path; a contradiction with playback running must never degrade")
	}
}
