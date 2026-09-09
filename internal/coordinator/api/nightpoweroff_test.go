package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The presentation power-off removal-policy runtime (nightpoweroff.go):
// every scenario here runs against fakes (an in-memory *store.Store and
// fakeResolumeActionDispatcher), never a real relay, projector or
// Home Assistant instance.

// nightPowerOffFixture builds a stopped night session, pinned against a
// night.session revision carrying off, with a fakeResolumeActionDispatcher
// standing in for every configured show.action.
type nightPowerOffFixture struct {
	h   *handlers
	st  *store.Store
	res *fakeResolumeActionDispatcher
}

func newNightPowerOffFixture(t *testing.T, now *time.Time, off *config.NightPresentationPowerOff, results map[string]ResolumeActionResult, actionIDsToVerbs map[string]string) *nightPowerOffFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(func() time.Time { return *now }))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	res := &fakeResolumeActionDispatcher{results: results}
	deps := Dependencies{NightSessions: st, Config: st, ResolumeActions: res}.withDefaults()
	h := &handlers{deps: deps, clock: func() time.Time { return *now }, logger: testLogger()}

	for id, verb := range actionIDsToVerbs {
		putNightAction(t, st, id, config.ShowActionPayload{
			Show: "halloween-2026", Label: id, SafetyClass: config.ShowSafetyClassNone,
			Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: verb, Ref: map[string]any{}},
		})
	}

	payload := nightShutdownPayload()
	payload.SiteControl = &config.NightSiteControl{PresentationPowerOff: off}
	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision: %v", err)
	}

	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateStopped, StateEnteredAt: *now, Cycle: 1,
		AdmissionClosed: true, ShutdownIntent: "power-down", PowerPhase: nightPowerPhaseConfiguredNotDispatched,
	}
	if err := st.CreateNightSession(context.Background(), rec, *now); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return &nightPowerOffFixture{h: h, st: st, res: res}
}

func resolumeConfirmed(reason string, at time.Time) ResolumeActionResult {
	t := at
	return ResolumeActionResult{Outcome: ResolumeOutcomeConfirmed, Dispatched: true, DispatchedAt: &t, Reason: reason}
}

func resolumeUnconfirmed(reason string, at time.Time) ResolumeActionResult {
	t := at
	return ResolumeActionResult{Outcome: ResolumeOutcomeUnconfirmed, Dispatched: true, DispatchedAt: &t, Reason: reason}
}

// An after-actions policy with two prerequisites (a confirmed action and a
// delay) runs them in order and invokes power-off last, never before both
// have completed.
func TestNightPowerOff_AfterActionsRunsPrerequisitesInOrderThenPowerOffLast(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy: config.NightRemovalPolicyAfterActions,
		Prerequisites: []config.NightPrerequisite{
			{Kind: config.NightPrerequisiteKindAction, Action: "prereq-a-action", RequireConfirmation: true},
			{Kind: config.NightPrerequisiteKindDelay, DelayMs: 5000},
		},
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"prereq-a-action": "launchClip", "power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls after prereq-a alone = %d, want 1 (the delay must hold power-off back)", got)
	}
	if last, ok := f.res.lastCall(); !ok || last.action != "launchClip" {
		t.Fatalf("last resolume call = %+v, want the action prerequisite dispatched first", last)
	}
	mid := mustGetCurrentSession(t, f.st)
	if mid.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase after the first tick = %q, want still in progress (the delay has not elapsed)", mid.PowerPhase)
	}

	// The delay has not elapsed yet: a second tick right away must not
	// advance past it or touch power-off.
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls before the delay elapses = %d, want still 1", got)
	}

	now = now.Add(5 * time.Second)
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	if got := f.res.callCount(); got != 2 {
		t.Fatalf("resolume calls once the delay has elapsed = %d, want 2 (power-off dispatched last)", got)
	}
	if last, ok := f.res.lastCall(); !ok || last.action != "blackout" {
		t.Fatalf("last resolume call = %+v, want the power-off action dispatched last", last)
	}
	final := mustGetCurrentSession(t, f.st)
	if final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase never resolved to a terminal outcome")
	}
	if !strings.Contains(final.PowerPhase, "dispatched") {
		t.Fatalf("PowerPhase = %q, want it to record the power-off action's own dispatch", final.PowerPhase)
	}

	// A further tick after the sequence has resolved must never redispatch.
	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 2 {
		t.Fatalf("resolume calls after the sequence already resolved = %d, want still 2 (no redispatch)", got)
	}
}

// A prerequisite that requires confirmation and never gets it stops the
// sequence before power-off, leaves power on, and names the step that
// stopped it with a recovery that actually exists.
func TestNightPowerOff_PrerequisiteNeverConfirmingLeavesPowerOnAndNamesTheStep(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy: config.NightRemovalPolicyAfterActions,
		Prerequisites: []config.NightPrerequisite{
			{Kind: config.NightPrerequisiteKindAction, Action: "prereq-a-action", RequireConfirmation: true},
		},
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"launchClip": resolumeUnconfirmed("no evidence arrived before the deadline", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"prereq-a-action": "launchClip", "power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	for _, call := range f.res.calls {
		if call.action == "blackout" {
			t.Fatalf("power-off action was dispatched even though its prerequisite never confirmed")
		}
	}
	final := mustGetCurrentSession(t, f.st)
	if final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase never resolved to a terminal outcome")
	}
	if !strings.Contains(final.PowerPhase, "prerequisite 1 of 1") {
		t.Fatalf("PowerPhase = %q, want it to name which prerequisite stopped the sequence", final.PowerPhase)
	}
	if !strings.Contains(final.PowerPhase, "NOT removed") {
		t.Fatalf("PowerPhase = %q, want it to state power was not removed", final.PowerPhase)
	}
	if !strings.Contains(final.PowerPhase, "/api/v1/actions/") {
		t.Fatalf("PowerPhase = %q, want it to name a recovery that actually exists", final.PowerPhase)
	}
	if got := mapNightPowerPhase(final); got.State != v1.NightEvidenceRecorded {
		t.Fatalf("mapNightPowerPhase state = %v, want the terminal outcome reported as recorded", got.State)
	}
}

// A crash between one prerequisite and the next must resume without
// repeating the completed step: simulated here by re-driving the same
// durable store from a fresh *handlers and a fresh dispatcher, so a
// second call to the already-resolved prerequisite's own action would
// show up as a second dispatch if recovery ever repeated it.
func TestNightPowerOff_CrashRecoveryDoesNotRepeatACompletedPrerequisite(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy: config.NightRemovalPolicyAfterActions,
		Prerequisites: []config.NightPrerequisite{
			{Kind: config.NightPrerequisiteKindAction, Action: "prereq-a-action", RequireConfirmation: true},
			{Kind: config.NightPrerequisiteKindDelay, DelayMs: 5000},
		},
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"prereq-a-action": "launchClip", "power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls before the simulated crash = %d, want 1", got)
	}

	// "Restart": a brand-new handlers and dispatcher against the SAME
	// durable store - nothing carries over except what was persisted.
	freshRes := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}}
	freshDeps := Dependencies{NightSessions: f.st, Config: f.st, ResolumeActions: freshRes}.withDefaults()
	freshH := &handlers{deps: freshDeps, clock: func() time.Time { return now }, logger: testLogger()}

	freshH.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	for _, call := range freshRes.calls {
		if call.action == "launchClip" {
			t.Fatalf("the already-resolved prerequisite was dispatched again after recovery")
		}
	}

	now = now.Add(5 * time.Second)
	freshH.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))
	if got := freshRes.callCount(); got != 1 {
		t.Fatalf("resolume calls made by the post-recovery process = %d, want exactly 1 (only the power-off action)", got)
	}
	if last, ok := freshRes.lastCall(); !ok || last.action != "blackout" {
		t.Fatalf("last resolume call after recovery = %+v, want the power-off action", last)
	}
	final := mustGetCurrentSession(t, f.st)
	if final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase never resolved to a terminal outcome after recovery")
	}
}

// An immediate policy invokes the power-off action once, with no
// prerequisites to run first.
func TestNightPowerOff_ImmediatePolicyDispatchesTheActionOnce(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy:            config.NightRemovalPolicyImmediate,
		ImmediateSafeAttestation: true,
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"blackout": resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	if got := f.res.callCount(); got != 1 {
		t.Fatalf("resolume calls for an immediate policy = %d, want exactly 1", got)
	}
	final := mustGetCurrentSession(t, f.st)
	if final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase never resolved to a terminal outcome")
	}
}

// The non-presentation domain refusal must still hold on this dispatch
// path: a pinned binding somehow carrying an environmental domain (never
// producible through write-time validation, constructed directly here to
// prove the runtime's own defense in depth) is refused rather than
// dispatched, so enclosure heating, a thermostat, sensors or air
// circulation are never at risk of coming down with presentation power.
func TestNightPowerOff_NonPresentationDomainBindingIsRefused(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainEnvironmental, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy:            config.NightRemovalPolicyImmediate,
		ImmediateSafeAttestation: true,
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"blackout": resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"power-off-action": "blackout"})

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	if got := f.res.callCount(); got != 0 {
		t.Fatalf("resolume calls for an environmental-domain binding = %d, want 0: this path must never dispatch it", got)
	}
	final := mustGetCurrentSession(t, f.st)
	if !strings.Contains(final.PowerPhase, "environmental") {
		t.Fatalf("PowerPhase = %q, want it to name the refused domain", final.PowerPhase)
	}
	if !strings.Contains(final.PowerPhase, "NOT removed") {
		t.Fatalf("PowerPhase = %q, want it to state power was not removed", final.PowerPhase)
	}
}

// A crash between marking a prerequisite dispatched and recording its
// outcome must never be guessed at as either "ran" or "did not run": for
// an integration with no stable retry identity (resolume here, like the
// mqtt actions a real power-off binding uses), the outbox row surfaces
// this as its own third, ambiguous state, and the sequence stops on it
// rather than assuming success or silently retrying a possibly-already-
// applied action.
func TestNightPowerOff_CrashMidDispatchIsReportedAmbiguousNotGuessed(t *testing.T) {
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	off := &config.NightPresentationPowerOff{
		NightPowerBinding: config.NightPowerBinding{
			Action: "power-off-action", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared,
		},
		RemovalPolicy: config.NightRemovalPolicyAfterActions,
		Prerequisites: []config.NightPrerequisite{
			{Kind: config.NightPrerequisiteKindAction, Action: "prereq-a-action", RequireConfirmation: true},
		},
	}
	f := newNightPowerOffFixture(t, &now, off, map[string]ResolumeActionResult{
		"launchClip": resolumeConfirmed("projectors confirmed off", now),
		"blackout":   resolumeConfirmed("power relay confirmed open", now),
	}, map[string]string{"prereq-a-action": "launchClip", "power-off-action": "blackout"})

	// Simulate the crash window nightDispatchAndPersistCue's own doc
	// comment names: the row was marked dispatched before the adapter was
	// ever called, and the process died before any outcome was recorded.
	dispatchedAt := now
	if err := f.st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: "sess-1", Cycle: 1, Phase: nightPhasePowerOffPrereq, CueName: nightPowerOffPrereqName(0),
		ActionRevision: 1, State: nightCueStateDispatched, DispatchedAt: &dispatchedAt,
	}, now); err != nil {
		t.Fatalf("insert dispatched-but-unresolved outbox row: %v", err)
	}

	f.h.nightAdvancePowerOff(context.Background(), now, mustGetCurrentSession(t, f.st))

	if got := f.res.callCount(); got != 0 {
		t.Fatalf("resolume calls after recovering an ambiguous, non-retryable row = %d, want 0: it must never be redispatched", got)
	}
	row, err := f.st.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhasePowerOffPrereq, nightPowerOffPrereqName(0))
	if err != nil {
		t.Fatalf("get outbox row: %v", err)
	}
	if row.State != nightCueStateAmbiguous {
		t.Fatalf("outbox row state after recovery = %q, want %q: crash recovery must land on the distinct ambiguous state, never confirmed or pending", row.State, nightCueStateAmbiguous)
	}
	final := mustGetCurrentSession(t, f.st)
	if final.PowerPhase == nightPowerPhaseConfiguredNotDispatched {
		t.Fatalf("PowerPhase never resolved to a terminal outcome")
	}
	if !strings.Contains(final.PowerPhase, "NOT removed") {
		t.Fatalf("PowerPhase = %q, want it to state power was not removed rather than assume the ambiguous step succeeded", final.PowerPhase)
	}
}
