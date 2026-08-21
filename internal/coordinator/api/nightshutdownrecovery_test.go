package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The shutdown path has to keep working in exactly the conditions that
// make an operator reach for it: a degraded session, and an FPP that is
// not answering.

// A degraded session still fades out. The three shutdown commands are
// accepted while degraded so the operator can end the night through the
// session; parking in fading-out without ever issuing the stop would take
// that back and leave the display running.
func TestNightFadingOut_DegradedSessionStillIssuesTheStop(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := fadingOutSession(now, "fade-out")
	rec.Degraded = true
	rec.DegradedReason = "coordinator restarted mid-transition"
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), rec)

	f.h.nightTick(context.Background(), now)

	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent for a degraded fading-out session = %v, want one %q", cmds, "Stop Now")
	}

	now = now.Add(5 * time.Second)
	f.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	})
	f.h.nightTick(context.Background(), now)

	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateStopped {
		t.Fatalf("state = %q, want stopped: a degraded session must still be able to reach stopped", got.State)
	}
	if !got.Degraded {
		t.Error("reaching stopped cleared the degraded record; it is history, not a resumable condition")
	}
}

// Every other state stays frozen while degraded.
func TestNightTick_DegradedSessionAdvancesNothingButFadingOut(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := fadingOutSession(now, "")
	rec.State = nightStatePreshow
	rec.AdmissionClosed = false
	rec.Degraded = true
	rec.DegradedReason = "ambiguous evidence"
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), rec)

	f.h.nightTick(context.Background(), now)

	if cmds := f.sentCommands(); len(cmds) != 0 {
		t.Fatalf("a degraded pre-show session dispatched %v; only fading-out may advance while degraded", cmds)
	}
}

// An FPP that does not answer must not look like a dispatch whose evidence
// is merely late: nothing reached the wire, so the stop is retried under a
// new command identity rather than replaying the failed one.
func TestNightFadingOut_UnreachableFPPRetriesUnderANewIdentity(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), fadingOutSession(now, "fade-out"))
	// Point the instance at a port nothing is listening on.
	f.h.deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://127.0.0.1:1"}}}

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	anchor, ok := decodeNightContentAnchor(mustGetCurrentSession(t, f.store).ContentAnchorJSON)
	if !ok {
		t.Fatal("no shutdown anchor was recorded")
	}
	if !anchor.DispatchedAt.IsZero() {
		t.Fatalf("DispatchedAt = %v for a stop FPP never accepted, want zero", anchor.DispatchedAt)
	}
	if anchor.Attempts != 1 {
		t.Fatalf("Attempts = %d after one failed send, want 1", anchor.Attempts)
	}
	if !strings.Contains(anchor.Source, "did not reach FPP") {
		t.Fatalf("anchor.Source = %q, want it to say the stop never reached FPP", anchor.Source)
	}

	// The retry takes a new key, so it is a real send and not a replay of
	// the failed command.
	first := nightShutdownStopIdempotencyKey(mustGetCurrentSession(t, f.store), 0)
	second := nightShutdownStopIdempotencyKey(mustGetCurrentSession(t, f.store), anchor.Attempts)
	if first == second {
		t.Fatalf("the retry reuses idempotency key %q, so it would replay the failed command and send nothing", first)
	}

	// Past the backoff, with FPP answering again, the stop lands.
	now = now.Add(nightShutdownStopRetryBackoff + time.Second)
	f.h.deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: f.endpoint}}}
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))
	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent after FPP came back = %v, want one %q", cmds, "Stop Now")
	}
}

// An unconfirmed stop degrades, and then tries again: the display is still
// running, so giving up is not an outcome.
func TestNightFadingOut_UnconfirmedStopDegradesAndReArms(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), fadingOutSession(now, "fade-out"))

	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))
	if len(f.sentCommands()) != 1 {
		t.Fatalf("the first stop was not sent: %v", f.sentCommands())
	}

	now = now.Add(nightShutdownStopConfirmDeadline + time.Second)
	f.h.nightAdvanceFadingOut(context.Background(), now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if !got.Degraded {
		t.Fatal("an unconfirmed stop past its deadline did not degrade the session")
	}
	anchor, _ := decodeNightContentAnchor(got.ContentAnchorJSON)
	if !anchor.DispatchedAt.IsZero() || anchor.Attempts != 1 {
		t.Fatalf("the stop was not re-armed after degrading: %+v", anchor)
	}

	now = now.Add(nightShutdownStopRetryBackoff + time.Second)
	f.h.nightTick(context.Background(), now)
	if len(f.sentCommands()) != 2 {
		t.Fatalf("commands sent = %v, want a second stop attempt after the deadline", f.sentCommands())
	}
}

// Resting playback that stops early leaves nothing to re-observe, so the
// session must degrade rather than wait forever for playback that is gone.
func TestNightAdvanceRestingIntershow_EarlyIdleDegradesRatherThanWaitingForever(t *testing.T) {
	dispatchedAt := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	anchor := nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "player-01", Playlist: "halloween-resting",
		Item: "halloween-resting.fseq", DurationMS: 300000, PositionMS: 0, PositionMSKnown: true,
		DispatchedAt: dispatchedAt, ObservedAt: dispatchedAt,
	}
	expectedE := *deriveNightBoundary(anchor).ExpectedAt
	now := expectedE.Add(-2 * time.Minute)

	rec := fadingOutSession(dispatchedAt, "")
	rec.State = nightStateRestingIntershow
	rec.AdmissionClosed = false
	rec.ContentAnchorJSON = encodeNightContentAnchor(anchor)
	rec.BoundaryJSON = encodeNightBoundary(deriveNightBoundary(anchor))
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), rec)
	f.obs.set(idleObservation("player-01", now))

	f.h.nightAdvanceRestingIntershow(context.Background(), now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if !got.Degraded {
		t.Fatal("resting playback stopped early and the session held silently; nothing would ever re-arm it")
	}
	if !strings.Contains(got.DegradedReason, "end-session") {
		t.Fatalf("degradedReason = %q, want it to name the recovery action", got.DegradedReason)
	}
	if b, _ := decodeNightBoundary(got.BoundaryJSON); b.State != nightBoundaryStateInvalid {
		t.Fatalf("boundary state = %q, want invalid", b.State)
	}
}

// A barrier cue's deadline runs from when that cue becomes due, not from
// the boundary: an offset cue is legitimately undispatched until then, and
// recording it as failed would both fabricate a failure and stop the cue
// ever running.
func TestNightBarrierSatisfied_DeadlineRunsFromTheCuesOwnOffset(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	offset := 3 * nightBarrierResolutionDeadline
	cues := []config.NightSessionCue{{
		Name: "late-barrier", Barrier: true, Action: "act-late",
		OffsetMs: int(offset / time.Millisecond), OnFailure: config.NightSessionCueOnFailureContinue,
	}}
	referenceE := testNow

	// Past the deadline measured from E, but well before the cue is due.
	at := referenceE.Add(nightBarrierResolutionDeadline + time.Second)
	ok, reason, err := h.nightBarrierSatisfied(context.Background(), at, referenceE, rec, nightPhaseEnterShow, cues)
	if err != nil {
		t.Fatalf("nightBarrierSatisfied: %v", err)
	}
	if ok {
		t.Fatal("the barrier released before its cue was even due")
	}
	if !strings.Contains(reason, "has not been dispatched yet") {
		t.Fatalf("reason = %q, want the not-yet-dispatched reason", reason)
	}
	if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "late-barrier"); err == nil {
		t.Fatal("a failure was recorded for a cue that is not due yet; it could then never dispatch")
	}

	// Past the deadline measured from the cue's own due time: now it is a
	// real failure.
	at = referenceE.Add(offset + nightBarrierResolutionDeadline + time.Second)
	if _, _, err := h.nightBarrierSatisfied(context.Background(), at, referenceE, rec, nightPhaseEnterShow, cues); err != nil {
		t.Fatalf("nightBarrierSatisfied past the cue's own deadline: %v", err)
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterShow, "late-barrier")
	if err != nil {
		t.Fatalf("the overdue cue was never recorded: %v", err)
	}
	if row.Outcome != nightCueOutcomeFailed {
		t.Fatalf("recorded outcome = %q, want failed", row.Outcome)
	}
}
