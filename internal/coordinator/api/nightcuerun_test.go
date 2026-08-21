package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F4: RESTING-MODE.md §7.1.1's commit boundary, its two
// crash windows, recovery, and the barrier. Each test's own doc comment
// names the rule it defends.

// putNightAction writes a show.action config object at revision 1,
// current — mirroring internal/coordinator/macro/testing_test.go's own
// putAction helper, duplicated here rather than imported because that
// package is a test-only helper unexported from a different package.
func putNightAction(t *testing.T, st *store.Store, id string, payload config.ShowActionPayload) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigObject(ctx, config.ShowActionConfigKind, id); err != nil {
		t.Fatalf("create action object %q: %v", id, err)
	}
	payloadJSON, err := config.EncodeShowActionPayload(payload)
	if err != nil {
		t.Fatalf("encode action payload %q: %v", id, err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActionConfigKind, ObjectID: id, Revision: 1, PayloadJSON: payloadJSON,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create action revision %q: %v", id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActionConfigKind, id, 1); err != nil {
		t.Fatalf("activate action revision %q: %v", id, err)
	}
}

func nightCueTestHandlers(t *testing.T) (*handlers, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	deps := Dependencies{NightSessions: st, Config: st, ResolumeActions: &fakeResolumeActionDispatcher{}}.withDefaults()
	return &handlers{deps: deps, clock: fixedClock(testNow), logger: testLogger()}, st
}

func mustCreateTransitionToShowSession(t *testing.T, st *store.Store, id string, cycle int64, stateEnteredAt time.Time) store.NightSessionRecord {
	t.Helper()
	rec := store.NightSessionRecord{
		ID: id, ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateTransitionToShow, StateEnteredAt: stateEnteredAt, Cycle: cycle,
	}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return rec
}

var testIssuer = FPPCommandIssuer{PrincipalID: "operator-1", PrincipalName: "Operator"}

func blackoutResolumeAction() config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "halloween", Label: "Blackout", SafetyClass: config.ShowSafetyClassBlackout,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: config.ShowActionResolumeBlackout, Ref: map[string]any{}},
	}
}

// --- classification: nightCueConfirmable / nightCueRetryableByIdentity ---

// TestNightCueConfirmable defends RESTING-MODE.md §7.1.1's own gate: fpp
// and resolume are always confirmable (their adapters always attempt
// confirmation); mqtt is confirmable only with a real expect block.
// Mutation-checked: flipping the mqtt branch's Kind comparison to == (so
// "none" reports confirmable) fails the mqttNone case; deleting the fpp/
// resolume case entirely (falling to default false) fails those two.
func TestNightCueConfirmable(t *testing.T) {
	yes := "boolean"
	cases := []struct {
		name   string
		target config.ShowActionTarget
		want   bool
	}{
		{"fpp", config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, Primitive: "stopPlaylist"}, true},
		{"resolume", config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: config.ShowActionResolumeBlackout}, true},
		{"mqttNoExpect", config.ShowActionTarget{Integration: config.ShowActionIntegrationMQTT}, false},
		{"mqttExpectNone", config.ShowActionTarget{Integration: config.ShowActionIntegrationMQTT, Expect: &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone}}, false},
		{"mqttExpectBoolean", config.ShowActionTarget{Integration: config.ShowActionIntegrationMQTT, Expect: &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Value: &yes}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nightCueConfirmable(tc.target); got != tc.want {
				t.Errorf("nightCueConfirmable(%+v) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// TestNightCueRetryableByIdentity defends the seam spec's own conservative
// rule: only fpp carries a stable end-to-end retry identity today
// (dispatchFPPCommand's idempotency-first design); resolume and mqtt do
// not, whatever their confirmability. Mutation-checked: changing the
// switch's true case to include resolume fails the resolume case.
func TestNightCueRetryableByIdentity(t *testing.T) {
	cases := []struct {
		integration string
		want        bool
	}{
		{config.ShowActionIntegrationFPP, true},
		{config.ShowActionIntegrationResolume, false},
		{config.ShowActionIntegrationMQTT, false},
	}
	for _, tc := range cases {
		t.Run(tc.integration, func(t *testing.T) {
			target := config.ShowActionTarget{Integration: tc.integration}
			if got := nightCueRetryableByIdentity(target); got != tc.want {
				t.Errorf("nightCueRetryableByIdentity(%q) = %v, want %v", tc.integration, got, tc.want)
			}
		})
	}
}

// --- the atomic commit ---

// TestNightCommitFirstCueIsAtomic defends §7.1.1's own ordering: after
// nightCommitFirstCue returns, show_committed and the outbox row both
// exist, together — never one without the other. Mutation-checked: moving
// the InsertNightCueOutboxRow call outside the transaction (or dropping
// the ShowCommitted write) makes this fail, since the row/flag would then
// disagree the moment a reader observes them between the two writes; here
// it is asserted after the fact, which is what commit-then-read of a real
// SQLite transaction can prove structurally.
func TestNightCommitFirstCueIsAtomic(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)

	committed, err := h.nightCommitFirstCue(context.Background(), testNow, rec, nightPhaseEnterShow, "lights", 3)
	if err != nil {
		t.Fatalf("nightCommitFirstCue: %v", err)
	}
	if !committed {
		t.Fatal("committed = false, want true")
	}

	got, err := st.GetNightSession(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("GetNightSession: %v", err)
	}
	if !got.ShowCommitted {
		t.Error("ShowCommitted = false after nightCommitFirstCue, want true")
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "lights")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStatePending {
		t.Errorf("outbox row state = %q, want %q", row.State, nightCueStatePending)
	}
	if row.ActionRevision != 3 {
		t.Errorf("outbox row action revision = %d, want 3 (the pinned revision)", row.ActionRevision)
	}
}

// TestNightCommitFirstCueRefusesWhenSessionMoved defends §7.1.1's "if
// fade-out-night wins before commit, cancel the armed boundary": a
// concurrent write that moves the session out from under this tick
// (state no longer transitionToShow) must leave NEITHER show_committed
// NOR an outbox row behind. Mutation-checked: removing the state-match
// guard makes this fail (it would then commit anyway).
func TestNightCommitFirstCueRefusesWhenSessionMoved(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)

	// A concurrent fade-out-night moved the session to stopped.
	moved := rec
	moved.State = nightStateStopped
	moved.StateEnteredAt = testNow.Add(time.Second)
	if err := st.UpdateNightSession(context.Background(), moved, testNow.Add(time.Second)); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	committed, err := h.nightCommitFirstCue(context.Background(), testNow.Add(2*time.Second), rec, nightPhaseEnterShow, "lights", 1)
	if err != nil {
		t.Fatalf("nightCommitFirstCue: %v", err)
	}
	if committed {
		t.Fatal("committed = true, want false — the session moved out from under this tick")
	}

	got, err := st.GetNightSession(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("GetNightSession: %v", err)
	}
	if got.ShowCommitted {
		t.Error("ShowCommitted = true, want false — nothing should have been committed")
	}
	if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "lights"); !errors.Is(err, store.ErrNightCueOutboxNotFound) {
		t.Errorf("GetNightCueOutboxRow error = %v, want ErrNightCueOutboxNotFound", err)
	}
}

// --- the two crash windows, injected structurally ---

// TestNightRunCue_CrashAfterCommitBeforeDispatch defends recovery's
// "pending, nothing sent yet" branch: aborting right after the atomic
// commit must leave the row pending with NO adapter call made, and a
// later resume (a fresh *handlers, matching a coordinator restart) must
// dispatch exactly once and resolve confirmed. Mutation-checked: deleting
// the hookAfterCommit call makes the first phase's assertion (dispatcher
// call count 0) fail, since the dispatch would run inline instead of
// waiting for the second, resuming call.
func TestNightRunCue_CrashAfterCommitBeforeDispatch(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.results = map[string]ResolumeActionResult{
		config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "layer is dark"},
	}
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "lights", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}

	h.nightCueHooks.afterCommit = func(string) bool { return true }
	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true); err != nil {
		t.Fatalf("nightRunCue (crashed): %v", err)
	}

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "lights")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStatePending {
		t.Fatalf("outbox row state after crash = %q, want %q", row.State, nightCueStatePending)
	}
	if got, err := st.GetNightSession(context.Background(), rec.ID); err != nil || !got.ShowCommitted {
		t.Fatalf("ShowCommitted after crash = %v (err %v), want true — the commit itself must have completed", got.ShowCommitted, err)
	}
	if n := dispatcher.callCount(); n != 0 {
		t.Fatalf("resolume dispatcher called %d times before resume, want 0", n)
	}

	// Resume, as a fresh coordinator process would: new *handlers, no hooks.
	h2, _ := nightCueTestHandlers(t)
	h2.deps = Dependencies{NightSessions: st, Config: st, ResolumeActions: dispatcher}.withDefaults()
	got, err := h2.nightRunCue(context.Background(), testNow.Add(time.Second), rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue (resume): %v", err)
	}
	if got.State != nightCueStateResolved || got.Outcome != nightCueOutcomeConfirmed {
		t.Errorf("resumed row = state %q outcome %q, want resolved/confirmed", got.State, got.Outcome)
	}
	if n := dispatcher.callCount(); n != 1 {
		t.Errorf("resolume dispatcher called %d times total, want exactly 1", n)
	}
}

// TestNightRunCue_CrashAfterDispatchBeforePersist_NonRetryableMarksAmbiguous
// defends the OTHER crash window and the conservative "not retryable by
// identity" rule together: a resolume dispatch may have already reached
// Resolume when the crash happens, so recovery must NOT call Dispatch
// again — it marks the cue ambiguous instead, which never satisfies the
// barrier. Mutation-checked: changing nightCueRetryableByIdentity to
// return true for resolume makes the dispatcher-call-count assertion fail
// (it would call Dispatch a second time instead of stopping at ambiguous).
func TestNightRunCue_CrashAfterDispatchBeforePersist_NonRetryableMarksAmbiguous(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.results = map[string]ResolumeActionResult{
		config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "layer is dark"},
	}
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "lights", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}

	h.nightCueHooks.afterDispatch = func(string) bool { return true }
	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true); err != nil {
		t.Fatalf("nightRunCue (crashed): %v", err)
	}
	if n := dispatcher.callCount(); n != 1 {
		t.Fatalf("resolume dispatcher called %d times before resume, want exactly 1 (the attempt that crashed)", n)
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "lights")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Fatalf("outbox row state after crash = %q, want %q", row.State, nightCueStateDispatched)
	}

	h2, _ := nightCueTestHandlers(t)
	h2.deps = Dependencies{NightSessions: st, Config: st, ResolumeActions: dispatcher}.withDefaults()
	got, err := h2.nightRunCue(context.Background(), testNow.Add(time.Second), rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue (resume): %v", err)
	}
	if got.State != nightCueStateAmbiguous {
		t.Errorf("resumed row state = %q, want %q", got.State, nightCueStateAmbiguous)
	}
	if n := dispatcher.callCount(); n != 1 {
		t.Errorf("resolume dispatcher called %d times total, want exactly 1 (never retried a non-retryable action)", n)
	}

	ok, reason, err := h2.nightBarrierSatisfied(context.Background(), testNow.Add(time.Second), testNow, rec, nightPhaseEnterShow, []config.NightSessionCue{cue})
	if err != nil {
		t.Fatalf("nightBarrierSatisfied: %v", err)
	}
	if ok {
		t.Errorf("barrier satisfied = true, want false — ambiguous never satisfies the barrier (reason: %s)", reason)
	}
}

// TestNightRunCue_FPPCrashAfterDispatchBeforePersist_RetriesWithoutDuplicateSend
// proves the fpp side of the SAME crash window against the real
// dispatchFPPCommand path (not a fake): a crash after the fpp command
// fully dispatched and confirmed, but before this package recorded that
// into night_cue_outbox, must resolve on resume WITHOUT a second HTTP
// request reaching FPP — dispatchFPPCommand's own idempotency-first
// replay is what nightCueRetryableByIdentity trusts for fpp. Mutation-
// checked: passing a freshly-minted idempotency key on resume (instead of
// the derived, stable one) makes the hit-count assertion fail (2 requests
// instead of 1).
func TestNightRunCue_FPPCrashAfterDispatchBeforePersist_RetriesWithoutDuplicateSend(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	deps := setup.deps()
	deps.NightSessions = setup.st
	deps.Config = setup.st
	deps = deps.withDefaults()

	h := &handlers{
		deps: deps, clock: fixedClock(testNow), logger: testLogger(),
		fppCommandConfirmDeadline: 200 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond,
	}

	putNightAction(t, setup.st, "act-stop", config.ShowActionPayload{
		Show: "halloween", Label: "Stop", SafetyClass: config.ShowSafetyClassStop,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: "bench-fpp", Primitive: "stopPlaylist", Params: map[string]any{}},
	})
	rec := mustCreateTransitionToShowSession(t, setup.st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "stop", Role: config.NightSessionCueRoleOther, Action: "act-stop", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}

	h.nightCueHooks.afterDispatch = func(string) bool { return true }
	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true); err != nil {
		t.Fatalf("nightRunCue (crashed): %v", err)
	}
	if n := srv.hitCount(); n != 1 {
		t.Fatalf("FPP received %d requests before resume, want exactly 1", n)
	}
	row, err := setup.st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "stop")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Fatalf("outbox row state after crash = %q, want %q", row.State, nightCueStateDispatched)
	}

	h2 := &handlers{deps: deps, clock: fixedClock(testNow.Add(time.Second)), logger: testLogger(),
		fppCommandConfirmDeadline: 200 * time.Millisecond, fppCommandPollInterval: 10 * time.Millisecond}
	got, err := h2.nightRunCue(context.Background(), testNow.Add(time.Second), rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue (resume): %v", err)
	}
	if got.State != nightCueStateResolved || got.Outcome != nightCueOutcomeConfirmed {
		t.Errorf("resumed row = state %q outcome %q, want resolved/confirmed", got.State, got.Outcome)
	}
	if n := srv.hitCount(); n != 1 {
		t.Errorf("FPP received %d requests total, want exactly 1 (the resume must replay, never re-send)", n)
	}
}

// --- the barrier ---

// TestNightBarrierSatisfied defends nightBarrierSatisfied's own three-way
// rule: a non-barrier cue never blocks; a barrier cue not yet in the
// outbox, or pending/dispatched/ambiguous, blocks; only resolved
// satisfies it — regardless of the resolved cue's own outcome value.
// Mutation-checked: removing the `!cue.Barrier { continue }` guard fails
// the non-barrier case (it would then block on an untouched cue).
func TestNightBarrierSatisfied(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	ctx := context.Background()
	sessionID, cycle := "sess-1", int64(1)
	rec := store.NightSessionRecord{ID: sessionID, Cycle: cycle}

	mustRow := func(name, state, outcome string) {
		t.Helper()
		var resolvedAt *time.Time
		if state == nightCueStateResolved || state == nightCueStateAmbiguous {
			r := testNow
			resolvedAt = &r
		}
		if err := st.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
			ID: name, SessionID: sessionID, Cycle: cycle, Phase: nightPhaseEnterShow, CueName: name, ActionRevision: 1,
			State: state, Outcome: outcome, ResolvedAt: resolvedAt,
		}, testNow); err != nil {
			t.Fatalf("insert row %q: %v", name, err)
		}
	}
	mustRow("pending-barrier", nightCueStatePending, "")
	mustRow("dispatched-barrier", nightCueStateDispatched, "")
	mustRow("ambiguous-barrier", nightCueStateAmbiguous, nightCueOutcomeAmbiguous)
	mustRow("resolved-failed-barrier", nightCueStateResolved, nightCueOutcomeFailed)
	mustRow("resolved-confirmed-barrier", nightCueStateResolved, nightCueOutcomeConfirmed)

	cue := func(name string, barrier bool) config.NightSessionCue {
		return config.NightSessionCue{Name: name, Barrier: barrier}
	}

	cases := []struct {
		name string
		cues []config.NightSessionCue
		want bool
	}{
		{"non-barrier cue never blocks, even with no row at all", []config.NightSessionCue{cue("never-run", false)}, true},
		{"no row yet blocks", []config.NightSessionCue{cue("no-such-row", true)}, false},
		{"pending blocks", []config.NightSessionCue{cue("pending-barrier", true)}, false},
		{"dispatched blocks", []config.NightSessionCue{cue("dispatched-barrier", true)}, false},
		{"ambiguous blocks", []config.NightSessionCue{cue("ambiguous-barrier", true)}, false},
		{"resolved-but-failed still satisfies (onFailure=continue is a policy question, not ambiguity)", []config.NightSessionCue{cue("resolved-failed-barrier", true)}, true},
		{"resolved-confirmed satisfies", []config.NightSessionCue{cue("resolved-confirmed-barrier", true)}, true},
		{"one blocking cue blocks the whole barrier", []config.NightSessionCue{cue("resolved-confirmed-barrier", true), cue("pending-barrier", true)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, err := h.nightBarrierSatisfied(ctx, testNow, testNow, rec, nightPhaseEnterShow, tc.cues)
			if err != nil {
				t.Fatalf("nightBarrierSatisfied: %v", err)
			}
			if ok != tc.want {
				t.Errorf("nightBarrierSatisfied = %v (reason %q), want %v", ok, reason, tc.want)
			}
		})
	}
}

// TestNightRunCue_UnconfirmableActionRefusedAsFirstCue defends the
// runtime, defense-in-depth half of RESTING-MODE.md §7.1.1's own rule
// (readiness is the primary gate — nightcue_readiness_test.go): an mqtt
// action with no expect block cannot become the first outward-facing cue,
// and nothing is committed when it is refused. Mutation-checked: removing
// the `isFirstOutwardCue && !nightCueConfirmable(...)` guard makes this
// fail (it would commit and attempt to dispatch an unconfirmable action as
// the show's own commit boundary).
func TestNightRunCue_UnconfirmableActionRefusedAsFirstCue(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	putNightAction(t, st, "act-mqtt", config.ShowActionPayload{
		Show: "halloween", Label: "Notify", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationMQTT, Broker: "home",
			Publish: &config.ShowActionMQTTPublish{Topic: "showmesh/notify", Payload: "go"},
			Expect:  &config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindNone},
		},
	})
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "notify", Role: config.NightSessionCueRoleAnnouncement, Action: "act-mqtt", OnFailure: config.NightSessionCueOnFailureContinue}

	_, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true)
	if !errors.Is(err, errNightCueNotConfirmableForFirst) {
		t.Fatalf("nightRunCue error = %v, want errNightCueNotConfirmableForFirst", err)
	}
	if got, err := st.GetNightSession(context.Background(), rec.ID); err != nil || got.ShowCommitted {
		t.Fatalf("ShowCommitted = %v (err %v), want false — nothing may commit on refusal", got.ShowCommitted, err)
	}
	if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, 1, nightPhaseEnterShow, "notify"); !errors.Is(err, store.ErrNightCueOutboxNotFound) {
		t.Errorf("GetNightCueOutboxRow error = %v, want ErrNightCueOutboxNotFound", err)
	}
}

// --- review round 2 findings ---

// TestNightDispatchCueResolume_ErrorResolvesFailedNeverStuckDispatched
// defends finding 1: an adapter error that provably sent nothing
// (Dispatch's own error contract) must resolve failed immediately, never
// leave the row dispatched for a later tick to misread as ambiguous.
// Mutation-checked: setting resolved:false on the error path (the
// pre-fix behavior) makes this fail.
func TestNightDispatchCueResolume_ErrorResolvesFailedNeverStuckDispatched(t *testing.T) {
	h, _ := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.err = errors.New("resolume manager not configured")

	got := h.nightDispatchCueResolume(context.Background(), testNow, config.ShowActionTarget{Integration: config.ShowActionIntegrationResolume, Action: config.ShowActionResolumeBlackout})
	if !got.resolved {
		t.Fatal("resolved = false, want true — an adapter error that sent nothing must resolve immediately")
	}
	if got.dispatched {
		t.Error("dispatched = true, want false — nothing was sent")
	}
	if got.outcome != nightCueOutcomeFailed {
		t.Errorf("outcome = %q, want %q", got.outcome, nightCueOutcomeFailed)
	}
}

// TestNightRunCue_AdapterErrorResolvesFailedAndSatisfiesContinueBarrier is
// the integration-level proof: a first cue whose adapter errors still
// commits (§7.1.1's own irreversibility), resolves failed rather than
// getting stuck, and — with onFailure continue — satisfies the barrier
// rather than deadlocking the transition forever. Mutation-checked by the
// unit test above; this proves the whole path agrees.
func TestNightRunCue_AdapterErrorResolvesFailedAndSatisfiesContinueBarrier(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.err = errors.New("resolume manager not configured")
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "lights", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}

	row, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, true)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeFailed {
		t.Fatalf("row = state %q outcome %q, want resolved/failed", row.State, row.Outcome)
	}
	if got, err := st.GetNightSession(context.Background(), rec.ID); err != nil || !got.ShowCommitted {
		t.Fatalf("ShowCommitted = %v (err %v), want true — the commit happened before dispatch was attempted", got.ShowCommitted, err)
	}
	ok, reason, err := h.nightBarrierSatisfied(context.Background(), testNow, testNow, rec, nightPhaseEnterShow, []config.NightSessionCue{cue})
	if err != nil {
		t.Fatalf("nightBarrierSatisfied: %v", err)
	}
	if !ok {
		t.Errorf("barrier satisfied = false (reason %q), want true — onFailure continue must not deadlock the transition", reason)
	}
}

// TestNightRunCue_SameCueNameDifferentPhaseAreIndependent defends finding
// 4: enterShow and enterResting are validated separately and may share a
// cue name; the outbox must never let one resolve the other's row.
// Mutation-checked: dropping Phase from the outbox identity (reverting to
// the pre-fix (session, cycle, cue) key) makes this fail — the second
// nightRunCue call would find the first phase's already-resolved row and
// return it unchanged, and the dispatcher would be called only once.
func TestNightRunCue_SameCueNameDifferentPhaseAreIndependent(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	dispatcher := h.deps.ResolumeActions.(*fakeResolumeActionDispatcher)
	dispatcher.results = map[string]ResolumeActionResult{
		config.ShowActionResolumeBlackout: {Outcome: ResolumeOutcomeConfirmed, Dispatched: true, Reason: "dark"},
	}
	putNightAction(t, st, "act-blackout", blackoutResolumeAction())
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow)
	cue := config.NightSessionCue{Name: "blackout", Role: config.NightSessionCueRoleLighting, Action: "act-blackout", OnFailure: config.NightSessionCueOnFailureContinue}

	if _, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterShow, cue, testIssuer, false); err != nil {
		t.Fatalf("nightRunCue (enterShow): %v", err)
	}
	if n := dispatcher.callCount(); n != 1 {
		t.Fatalf("dispatcher called %d times after enterShow cue, want 1", n)
	}

	row, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterResting, cue, testIssuer, false)
	if err != nil {
		t.Fatalf("nightRunCue (enterResting): %v", err)
	}
	if n := dispatcher.callCount(); n != 2 {
		t.Errorf("dispatcher called %d times total, want 2 — the enterResting cue must dispatch independently, not reuse enterShow's row", n)
	}
	if row.Phase != nightPhaseEnterResting {
		t.Errorf("row.Phase = %q, want %q", row.Phase, nightPhaseEnterResting)
	}
}

// TestNightBarrierSatisfied_PendingPastDeadlineTimesOutToFailed defends
// finding 5: a barrier cue stuck pending (never dispatched) must not
// block the launch forever despite onFailure continue. Mutation-checked:
// excluding nightCueStatePending from the timeout condition (the pre-fix
// behavior) makes this fail.
func TestNightBarrierSatisfied_PendingPastDeadlineTimesOutToFailed(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := store.NightSessionRecord{ID: "sess-1", Cycle: 1}
	if err := st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "row-1", SessionID: "sess-1", Cycle: 1, Phase: nightPhaseEnterShow, CueName: "stuck",
		ActionRevision: 1, State: nightCueStatePending,
	}, testNow); err != nil {
		t.Fatalf("seed pending row: %v", err)
	}
	cue := config.NightSessionCue{Name: "stuck", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}

	referenceE := testNow
	before := testNow.Add(nightBarrierResolutionDeadline - time.Second)
	ok, _, err := h.nightBarrierSatisfied(context.Background(), before, referenceE, rec, nightPhaseEnterShow, []config.NightSessionCue{cue})
	if err != nil {
		t.Fatalf("nightBarrierSatisfied (before deadline): %v", err)
	}
	if ok {
		t.Fatal("barrier satisfied before the deadline, want still blocked")
	}

	after := testNow.Add(nightBarrierResolutionDeadline + time.Second)
	ok, reason, err := h.nightBarrierSatisfied(context.Background(), after, referenceE, rec, nightPhaseEnterShow, []config.NightSessionCue{cue})
	if err != nil {
		t.Fatalf("nightBarrierSatisfied (after deadline): %v", err)
	}
	if !ok {
		t.Fatalf("barrier satisfied = false (reason %q), want true — onFailure continue after the deadline", reason)
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhaseEnterShow, "stuck")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeFailed {
		t.Errorf("row = state %q outcome %q, want resolved/failed (never dispatched)", row.State, row.Outcome)
	}
}

// TestNightBarrierSatisfied_PendingPastDeadlineWithAbortStillBlocks is
// the abort-policy half of finding 5: the pending cue still resolves
// (never left hanging), but onFailure abort still holds the barrier.
func TestNightBarrierSatisfied_PendingPastDeadlineWithAbortStillBlocks(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := store.NightSessionRecord{ID: "sess-1", Cycle: 1}
	if err := st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "row-1", SessionID: "sess-1", Cycle: 1, Phase: nightPhaseEnterShow, CueName: "stuck",
		ActionRevision: 1, State: nightCueStatePending,
	}, testNow); err != nil {
		t.Fatalf("seed pending row: %v", err)
	}
	cue := config.NightSessionCue{Name: "stuck", Barrier: true, OnFailure: config.NightSessionCueOnFailureAbort}

	after := testNow.Add(nightBarrierResolutionDeadline + time.Second)
	ok, reason, err := h.nightBarrierSatisfied(context.Background(), after, testNow, rec, nightPhaseEnterShow, []config.NightSessionCue{cue})
	if err != nil {
		t.Fatalf("nightBarrierSatisfied: %v", err)
	}
	if ok {
		t.Errorf("barrier satisfied = true, want false — onFailure abort (reason: %s)", reason)
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), "sess-1", 1, nightPhaseEnterShow, "stuck")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeFailed {
		t.Errorf("row = state %q outcome %q, want resolved/failed even though it still blocks", row.State, row.Outcome)
	}
}

// TestNightAdvanceCueList_EnterRestingNeverEvaluatesBarrier defends
// finding 9: enterResting is fire-and-forget and must never evaluate a
// barrier at all, even one dispatched cue past the deadline. Mutation-
// checked: removing the `if !isEnterShow { return true, "" }` early
// return makes this fail (the stalled row gets timed out).
func TestNightAdvanceCueList_EnterRestingNeverEvaluatesBarrier(t *testing.T) {
	h, st := nightCueTestHandlers(t)
	rec := mustCreateTransitionToShowSession(t, st, "sess-1", 1, testNow) // state irrelevant here
	dispatchedAt := testNow.Add(-2 * nightBarrierResolutionDeadline)
	if err := st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "row-1", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterResting, CueName: "fade-up",
		ActionRevision: 1, State: nightCueStateDispatched, DispatchedAt: &dispatchedAt,
	}, dispatchedAt); err != nil {
		t.Fatalf("seed dispatched row: %v", err)
	}
	cues := []config.NightSessionCue{{Name: "fade-up", Barrier: true, OnFailure: config.NightSessionCueOnFailureContinue}}

	h.nightAdvanceCueList(context.Background(), testNow, rec, dispatchedAt, nightPhaseEnterResting, cues)

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterResting, "fade-up")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Errorf("row.State = %q, want still %q — enterResting has no barrier to evaluate", row.State, nightCueStateDispatched)
	}
}
