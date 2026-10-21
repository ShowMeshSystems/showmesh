package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// A power down that FPP never confirms still reaches stopped and runs its
// sequences, and a forced power down skips the wait (ADR-069).

func newPowerDownFallbackFixture(t *testing.T, now *time.Time, rec store.NightSessionRecord) (*nightShutdownFixture, *fakeResolumeActionDispatcher) {
	t.Helper()
	payload := nightShutdownPayload()
	payload.SiteActions = nightOnlySequences(config.ShutdownSequence{ID: "fm", Label: "FM transmitter", Steps: []config.ShutdownStep{powerCut("fm-off")}})
	f := newNightShutdownFixture(t, now, payload, rec)
	res := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{"blackout": resolumeConfirmed("fm off", *now)}}
	f.h.deps.ResolumeActions = res
	putNightAction(t, f.store, "fm-off", config.ShowActionPayload{
		Show: "halloween-2026", Label: "fm-off", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: "blackout", Ref: map[string]any{}},
	})
	return f, res
}

func stopUnconfirmedBoundary(t *testing.T, rec store.NightSessionRecord) (nightBoundary, bool) {
	t.Helper()
	b, ok := decodeNightBoundary(rec.BoundaryJSON)
	return b, ok && b.Kind == nightBoundaryKindStopUnconfirmed
}

func TestNightPowerDownFallback_DeadlineReachesStoppedRunsSequencesAndResendsStop(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f, res := newPowerDownFallbackFixture(t, &now, fadingOutSession(now, "power-down"))
	ctx := context.Background()

	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))
	now = now.Add(nightShutdownStopConfirmDeadline + time.Second)
	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateStopped || got.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("state = %q, powerPhase = %q, want stopped with the power down pending", got.State, got.PowerPhase)
	}
	if got.Degraded {
		t.Fatalf("the power down fallback degraded the session: %q", got.DegradedReason)
	}
	b, ok := stopUnconfirmedBoundary(t, got)
	if !ok || !strings.Contains(b.Reason, "did not confirm the stop") {
		t.Fatalf("boundary = %+v, want the unconfirmed stop recorded", b)
	}
	if v := mapNightBoundary(got); !strings.Contains(v.Reason, "did not confirm the stop") {
		t.Fatalf("operator-visible boundary reason = %q, want the unconfirmed stop", v.Reason)
	}

	f.h.nightAdvancePowerOff(ctx, now, got)
	if res.callCount() != 1 {
		t.Fatalf("sequence calls = %d, want the power down sequence started", res.callCount())
	}
	if cmds := f.sentCommands(); len(cmds) != 2 {
		t.Fatalf("FPP commands = %v, want the stop resent on the stopped tick", cmds)
	}

	now = now.Add(nightShutdownStopConfirmDeadline / 2)
	f.h.nightAdvancePowerOff(ctx, now, mustGetCurrentSession(t, f.store))
	if cmds := f.sentCommands(); len(cmds) != 2 {
		t.Fatalf("FPP commands = %v, want no resend inside the confirm deadline", cmds)
	}
	now = now.Add(nightShutdownStopConfirmDeadline)
	f.h.nightAdvancePowerOff(ctx, now, mustGetCurrentSession(t, f.store))
	if cmds := f.sentCommands(); len(cmds) != 3 {
		t.Fatalf("FPP commands = %v, want a third stop once the deadline passed again", cmds)
	}

	now = now.Add(time.Second)
	f.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now),
		playlistNameObservation("player-01", "", now),
	})
	f.h.nightAdvancePowerOff(ctx, now, mustGetCurrentSession(t, f.store))
	if _, ok := stopUnconfirmedBoundary(t, mustGetCurrentSession(t, f.store)); ok {
		t.Fatal("the unconfirmed stop is still recorded after FPP reported idle")
	}
	now = now.Add(2 * nightShutdownStopConfirmDeadline)
	f.h.nightAdvancePowerOff(ctx, now, mustGetCurrentSession(t, f.store))
	if cmds := f.sentCommands(); len(cmds) != 3 {
		t.Fatalf("FPP commands = %v, want no resend after FPP reported idle", cmds)
	}
}

func TestNightPowerDownFallback_StopThatNeverReachesFPPStillReachesStopped(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f, _ := newPowerDownFallbackFixture(t, &now, fadingOutSession(now, "power-down"))
	f.h.deps.FPP = &fakeFPPLister{}
	ctx := context.Background()

	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))
	now = now.Add(nightShutdownStopConfirmDeadline + time.Second)
	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	b, ok := stopUnconfirmedBoundary(t, got)
	if got.State != nightStateStopped || !ok || b.StopSentAt != nil {
		t.Fatalf("state = %q, boundary = %+v, want stopped with no stop sent yet", got.State, b)
	}
}

func TestNightPowerDownFallback_FadeOutOnlyNightStillDegradesInFadingOut(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f, _ := newPowerDownFallbackFixture(t, &now, fadingOutSession(now, "fade-out"))
	ctx := context.Background()

	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))
	now = now.Add(nightShutdownStopConfirmDeadline + time.Second)
	f.h.nightAdvanceFadingOut(ctx, now, mustGetCurrentSession(t, f.store))

	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateFadingOut || !got.Degraded {
		t.Fatalf("state = %q, degraded = %v, want a fade-out-only night still fading out and degraded", got.State, got.Degraded)
	}
}

func TestNightTick_DegradedStoppedPowerDownStillRunsItsSequences(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := fadingOutSession(now, "power-down")
	rec.State = nightStateStopped
	rec.PowerPhase = nightPowerPhaseConfiguredNotDispatched
	rec.Degraded, rec.DegradedReason = true, "FPP stopped reporting."
	f, res := newPowerDownFallbackFixture(t, &now, rec)

	f.h.nightTick(context.Background(), now)
	if res.callCount() != 1 {
		t.Fatalf("sequence calls on a degraded stopped power down = %d, want 1", res.callCount())
	}
}

func TestNightTick_DegradedStoppedFadeOutRunsNothing(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	rec := fadingOutSession(now, "fade-out")
	rec.State = nightStateStopped
	rec.Degraded, rec.DegradedReason = true, "FPP stopped reporting."
	f, res := newPowerDownFallbackFixture(t, &now, rec)

	f.h.nightTick(context.Background(), now)
	if res.callCount() != 0 || len(f.sentCommands()) != 0 {
		t.Fatalf("a degraded stopped fade-out night dispatched: sequence calls %d, FPP commands %v", res.callCount(), f.sentCommands())
	}
}

func TestNightPowerDownForce_LiveShowStopsAtOnceAndIsAudited(t *testing.T) {
	st, _, token, api := newSeededNightFixture(t, "live")

	resp, body := nightCommandRawBody(t, api, token, "power-down-presentation", `{"force":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("forced power down status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	var out v1.NightCommandResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Session.State != "stopped" || out.Session.PowerPhase.Reason == "" {
		t.Fatalf("forced power down: state = %q, powerPhase = %+v, want stopped with the power phase resolved", out.Session.State, out.Session.PowerPhase)
	}
	rec := mustGetCurrentSession(t, st)
	if b, ok := stopUnconfirmedBoundary(t, rec); !ok || !strings.Contains(b.Reason, "forced") {
		t.Fatalf("boundary = %+v, want the forced, unconfirmed stop recorded", b)
	}

	entries, err := st.ListAuditEntries(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var sawForce bool
	for _, e := range entries {
		if strings.Contains(e.Action, "power-down-presentation") && strings.Contains(e.ParamsJSON, `"force":true`) {
			sawForce = true
		}
	}
	if !sawForce {
		t.Fatalf("no power-down-presentation audit entry records force: %+v", entries)
	}
}

func TestNightPowerDownForce_InterlockStillRefuses(t *testing.T) {
	api, brokers, opToken, _, obs := setupNightInterlockRawFixture(t, nightBlockInterlockJSON("cooldown", "power-down-presentation"))
	mustNightCommand(t, api, opToken, "prepare-site")
	setHealthyFPPReachable(obs, testNow)
	brokers.msg = broker.Message{Payload: []byte("false")}
	mustNightCommand(t, api, opToken, "run-readiness")
	mustNightCommand(t, api, opToken, "start-preshow")

	resp, body := nightCommandRawBody(t, api, opToken, "power-down-presentation", `{"force":true}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("forced power down past a blocking interlock: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if got := mustGetNightSession(t, api); got.Session.State != "preshow" {
		t.Fatalf("a refused forced power down changed state to %q", got.Session.State)
	}
}

func TestNightCommandForceOnAnotherCommandIsRefused(t *testing.T) {
	_, _, token, api := newSeededNightFixture(t, "preshow")
	resp, body := nightCommandRawBody(t, api, token, "fade-out-night", `{"force":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("fade-out-night with force: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func seedPowerDownFadingOut(t *testing.T, st *store.Store) {
	t.Helper()
	rec := store.NightSessionRecord{ID: "seeded-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, State: nightStateFadingOut, StateEnteredAt: testNow, AdmissionClosed: true, ShutdownIntent: "power-down"}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestNightEndSession_ResolvesAPendingPowerDown(t *testing.T) {
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)
	seedPowerDownFadingOut(t, st)

	mustNightCommand(t, api, token, "end-session")
	got := mustGetCurrentSession(t, st)
	if got.State != nightStateStopped || got.PowerPhase == "" {
		t.Fatalf("end session on a pending power down: state = %q, powerPhase = %q, want stopped with the power phase resolved", got.State, got.PowerPhase)
	}
}

func TestNightEmergencyEndSession_LeavesThePowerPhaseUnresolved(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	f, _ := newPowerDownFallbackFixture(t, &now, fadingOutSession(now, "power-down"))

	f.h.nightEmergencyEndSession(context.Background(), now, identity.AuditEntry{Timestamp: now, PrincipalID: "op", PrincipalName: "op"})
	got := mustGetCurrentSession(t, f.store)
	if got.State != nightStateStopped || got.PowerPhase != "" {
		t.Fatalf("emergency end session: state = %q, powerPhase = %q, want stopped with the power phase untouched", got.State, got.PowerPhase)
	}
}
