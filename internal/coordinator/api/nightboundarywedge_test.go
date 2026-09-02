package api

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Investigation for a design ruling on nightAdvanceRestingIntershow's
// invalid-boundary early return (nightloop.go:345). The question: is that
// branch reachable permanently, and by only one of the two ways a boundary
// becomes invalid?
//
// Correction to the original claim, found while building this test: the
// early return at line 345 does NOT fire on every tick forever. Once
// nightDegradeSession sets Degraded, nightTick's own top-level guard
// (nightloop.go:115, "if rec.Degraded && rec.State != nightStateFadingOut
// { return }") stops nightAdvanceRestingIntershow from being called again
// at all, so line 345 fires exactly once.
//
// A second correction, found only by mutating the test itself: the guard is
// NOT what makes the wedge permanent. Remove it and this test still passes,
// because nothing else re-derives the boundary (ObservedAt stays set, so
// the anchor never again takes the branch above line 345) and nothing
// clears Degraded. Permanence comes from that absence, not from the guard.
// What the guard actually contributes is breadth: with it in place, a
// degraded session advances NOTHING at all, not just resting-intershow's
// own recheck - proven separately below by
// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction, the one
// test in this file whose mutation-tested failure genuinely depends on the
// guard.

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

// TestNightAdvanceRestingIntershow_DerivedInvalidBoundaryWedgesSessionPermanently
// is the claim's own scenario: an anchor re-derived from fresh observed
// evidence whose position is already past the resolved duration
// (deriveNightBoundary's third invalid route). The anchor is seeded with
// its own DurationMS already known (as a real anchor invalidated by a
// contradiction, or one carried over a purpose match, would be) so the
// re-derive branch (nightloop.go:314-317) never needs to resolve an FSEQ
// asset - this test is only about what happens once that pair persists.
func TestNightAdvanceRestingIntershow_DerivedInvalidBoundaryWedgesSessionPermanently(t *testing.T) {
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
	if got.Degraded {
		t.Fatal("session degraded on the same tick the boundary was derived invalid; want degrade to wait for the NEXT tick's own check")
	}

	// Tick 2: this is the tick that actually hits line 345, since the
	// anchor now matches purpose+ObservedAt-set and the persisted boundary
	// reads invalid. Background audio still advances on this exact tick,
	// because nightTick's top-level guard is evaluated once, from the rec
	// read at THIS tick's own start (Degraded was still false then) - the
	// guard only bites starting the tick after this one.
	tick2 := tick1.Add(30 * time.Second)
	h.nightTick(context.Background(), tick2)

	got = mustGetCurrentSession(t, st)
	if !got.Degraded {
		t.Fatal("session not degraded after the tick that re-checks an already-invalid persisted boundary")
	}
	degradedReason := got.DegradedReason
	if !strings.Contains(degradedReason, "invalidated") {
		t.Fatalf("DegradedReason = %q, want it to name the invalidated boundary", degradedReason)
	}
	anchor, _ = decodeNightContentAnchor(got.ContentAnchorJSON)
	if !anchor.ObservedAt.Equal(tick1) {
		t.Fatalf("anchor.ObservedAt = %v after degrade, want unchanged at %v (line 345 never re-derives)", anchor.ObservedAt, tick1)
	}
	boundary, _ = decodeNightBoundary(got.BoundaryJSON)
	if boundary.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %q after degrade, want still invalid", boundary.State)
	}
	// Ticks 3-5, across a simulated long gap: the session stays wedged with
	// no path out. (A background-audio dispatch count was tried here as a
	// canary for "the guard blocks everything, not just resting-intershow's
	// own recheck" and dropped: measured against a temporarily-disabled
	// guard, the count stayed flat either way, because this fixture's
	// background audio reaches a static per-node state after its first
	// dispatch regardless of Degraded. See
	// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction below for
	// the assertion that actually discriminates the guard.)
	tick := tick2
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
		if !anchor.ObservedAt.Equal(tick1) {
			t.Fatalf("anchor.ObservedAt = %v at %v, want unchanged at %v", anchor.ObservedAt, tick, tick1)
		}
		boundary, _ = decodeNightBoundary(got.BoundaryJSON)
		if boundary.State != nightBoundaryStateInvalid {
			t.Fatalf("boundary state = %q at %v, want still invalid", boundary.State, tick)
		}
	}

	// nightDegradeSession is one-shot, so "session degraded" appears exactly
	// once. Note what this does and does not show: it cannot tell a
	// guard-blocked path apart from a silently idempotent re-degrade, and
	// permanence does not depend on the guard at all. With the guard removed
	// this test still passes, because nothing re-derives (ObservedAt stays
	// set, so the first branch above is never taken again) and nothing
	// clears Degraded. The guard's own contribution is breadth: it blocks
	// every OTHER action too, which
	// TestNightTick_DegradedSessionDispatchesNothingOnAFreshAction is what
	// actually proves.
	if n := strings.Count(logBuf.String(), "session degraded"); n != 1 {
		t.Fatalf("\"session degraded\" logged %d times across all ticks, want exactly 1", n)
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
