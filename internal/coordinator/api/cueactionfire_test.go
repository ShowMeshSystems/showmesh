package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/sendsignal"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

const cueActionLaunchColumnBody = `{"show":"halloween-2026","label":"Song one column","safetyClass":"none",
	"target":{"integration":"resolume","action":"launchColumn","ref":{"column":"col-1","deck":"deck-1"}}}`

const cueActionBlackoutBody = `{"show":"halloween-2026","label":"Blackout","safetyClass":"blackout",
	"target":{"integration":"resolume","action":"blackout"}}`

// cueActionFixture is cueActivationDispatchTestFixture with the cue
// rewritten at revision 2 to also fire actionIDs.
func cueActionFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time, actionIDs ...string) (string, cueactivation.Activation) {
	t.Helper()
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putConfigForTest(t, setup.st, config.ShowActionConfigKind, "song-one-column", cueActionLaunchColumnBody)
	putConfigForTest(t, setup.st, config.ShowActionConfigKind, "blackout-now", cueActionBlackoutBody)
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: act.Show, Name: act.CueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + act.CueID}, Actions: actionIDs},
	})
	if err != nil {
		t.Fatalf("encode show.cue: %v", err)
	}
	ctx := context.Background()
	if _, err := setup.st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: act.CueID, Revision: 2, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create show.cue revision 2: %v", err)
	}
	if _, err := setup.st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, act.CueID, 2); err != nil {
		t.Fatalf("activate show.cue revision 2: %v", err)
	}
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CueRevision = 2
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)
	return nodeID, act
}

func newCueActionHandlers(setup *audioDispatchTestSetup, now time.Time, dispatcher *fakeResolumeActionDispatcher) *handlers {
	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.ResolumeActions = dispatcher
	return &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
}

func resolumeCallActions(d *fakeResolumeActionDispatcher) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.calls))
	for i, c := range d.calls {
		out[i] = c.action
	}
	return out
}

func TestDispatchCueActivationsFiresActionsInOrder(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchColumn": {Outcome: ResolumeOutcomeConfirmed},
		"blackout":     {Outcome: ResolumeOutcomeConfirmed},
	}}
	h := newCueActionHandlers(setup, now, dispatcher)
	act.ScheduledAtNs = new(int64)

	outcomes := h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()

	if len(outcomes) != 1 || !outcomes[0].Confirmed {
		t.Fatalf("node outcomes = %+v, want one confirmed", outcomes)
	}
	if got := resolumeCallActions(dispatcher); len(got) != 2 || got[0] != "launchColumn" || got[1] != "blackout" {
		t.Fatalf("resolume calls = %v, want [launchColumn blackout]", got)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(cueActionsActivationKey(act, 1), 0, "song-one-column"))
	if err != nil {
		t.Fatalf("first action's command row: %v", err)
	}
	if rec.Action != "action.invoke:resolume" || rec.State != "resolved" {
		t.Fatalf("command row = action %q state %q, want action.invoke:resolume resolved", rec.Action, rec.State)
	}
}

func TestDispatchCueActivationsFailingActionDoesNotStopNodeDispatch(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchColumn": {Outcome: ResolumeOutcomeFailed, Reason: "Resolume did not answer"},
		"blackout":     {Outcome: ResolumeOutcomeConfirmed},
	}}
	h := newCueActionHandlers(setup, now, dispatcher)
	act.ScheduledAtNs = new(int64)

	outcomes := h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()

	if len(outcomes) != 1 || !outcomes[0].Dispatched || !outcomes[0].Confirmed {
		t.Fatalf("node outcomes = %+v, want the node dispatch confirmed despite the failed action", outcomes)
	}
	if got := resolumeCallActions(dispatcher); len(got) != 2 {
		t.Fatalf("resolume calls = %v, want both actions attempted", got)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(cueActionsActivationKey(act, 1), 0, "song-one-column"))
	if err != nil {
		t.Fatalf("failed action's command row: %v", err)
	}
	if rec.OutcomeReason != "Resolume did not answer" {
		t.Fatalf("recorded reason = %q, want the action's own reason", rec.OutcomeReason)
	}
	o := cueActionOutcomeFromRecord("song-one-column", rec)
	if o.Outcome != outcomeWordFailed {
		t.Fatalf("recorded outcome = %q, want %q", o.Outcome, outcomeWordFailed)
	}
}

func TestDispatchCueActivationsSameActivationFiresActionsOnce(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchColumn": {Outcome: ResolumeOutcomeConfirmed},
		"blackout":     {Outcome: ResolumeOutcomeConfirmed},
	}}
	h := newCueActionHandlers(setup, now, dispatcher)
	act.ScheduledAtNs = new(int64)
	activations := map[string]cueactivation.Activation{nodeID: act}

	for i := 0; i < 2; i++ {
		h.dispatchCueActivationsWithActions(context.Background(), now, activations, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
		h.cueActivationFailToBlackWG.Wait()
	}
	if got := resolumeCallActions(dispatcher); len(got) != 2 {
		t.Fatalf("resolume calls = %v, want each action fired exactly once across two dispatches", got)
	}
}

func TestDispatchCueActivationsWithoutActionsFiresNothing(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{}
	h := newCueActionHandlers(setup, now, dispatcher)
	act.ScheduledAtNs = new(int64)

	h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()
	if n := dispatcher.callCount(); n != 0 {
		t.Fatalf("resolume calls = %d, want 0 for a cue with no actions", n)
	}
}

func TestHandleActivateCueReportsActionOutcomes(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"launchColumn": {Outcome: ResolumeOutcomeRefused, Reason: "No composition is loaded."},
		"blackout":     {Outcome: ResolumeOutcomeConfirmed},
	}}
	h := newCueActionHandlers(setup, now, dispatcher)

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "CueActivateResponse", rec.Body.Bytes())
	if len(resp.Actions) != 2 {
		t.Fatalf("actions = %+v, want two outcomes", resp.Actions)
	}
	if resp.Actions[0].ActionID != "song-one-column" || resp.Actions[0].Outcome != outcomeWordRefused || resp.Actions[0].OutcomeReason != "No composition is loaded." {
		t.Fatalf("first action outcome = %+v", resp.Actions[0])
	}
	if resp.Actions[1].ActionID != "blackout-now" || resp.Actions[1].Outcome != outcomeWordConfirmed {
		t.Fatalf("second action outcome = %+v", resp.Actions[1])
	}
	if len(resp.Nodes) != 1 || !resp.Nodes[0].Confirmed {
		t.Fatalf("nodes = %+v, want the node dispatch confirmed", resp.Nodes)
	}
	final := waitForCueActionsAudit(t, setup.svc, act.CueID)
	if final.Outcome != outcomeWordRefused {
		t.Fatalf("final actions audit outcome = %q, want the worst action outcome %q", final.Outcome, outcomeWordRefused)
	}
	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatches, outcomesAudited int
	for _, e := range entries {
		if e.Action != "action.invoke:resolume" {
			continue
		}
		if e.Params["cueId"] != act.CueID || e.PrincipalID != "operator-1" {
			t.Fatalf("audit entry = %+v, want the cue id and the operator who fired it", e)
		}
		switch e.Kind {
		case identity.AuditDispatch:
			dispatches++
		case identity.AuditOutcome:
			outcomesAudited++
		}
	}
	if dispatches != 2 || outcomesAudited != 2 {
		t.Fatalf("audited %d dispatches and %d outcomes, want 2 and 2", dispatches, outcomesAudited)
	}
}

// waitForCueActionsAudit returns the cue.activate outcome entry that carries
// a cue's final show action outcomes, written after the actions resolve.
func waitForCueActionsAudit(t *testing.T, svc identity.Service, cueID string) identity.AuditEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := svc.ListAudit(context.Background(), 0, 200)
		if err != nil {
			t.Fatalf("list audit: %v", err)
		}
		for _, e := range entries {
			if e.Action == "cue.activate" && e.Kind == identity.AuditOutcome && e.Target == "cue:"+cueID {
				return e
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no final show actions audit entry for cue %q", cueID)
	return identity.AuditEntry{}
}

func auditActionOutcomes(t *testing.T, e identity.AuditEntry) []v1.CueActionOutcome {
	t.Helper()
	raw, err := json.Marshal(e.Params["actions"])
	if err != nil {
		t.Fatalf("encode actions param: %v", err)
	}
	var out []v1.CueActionOutcome
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode actions param %s: %v", raw, err)
	}
	return out
}

func TestFireOneCueActionRecordsAnUnsentRefusalOnce(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, act := cueActionFixture(t, setup, now)
	dispatcher := &fakeResolumeActionDispatcher{}
	h := newCueActionHandlers(setup, now, dispatcher)
	key := cueActionsActivationKey(act, 1)

	o, fresh := h.fireOneCueAction(context.Background(), act, key, 0, "no-such-action", cueActivationIssuer{PrincipalID: "system:test"}, func() bool { return false })
	if o.Outcome != outcomeWordRefused || !fresh {
		t.Fatalf("outcome = %+v fresh=%v, want a fresh refusal", o, fresh)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(key, 0, "no-such-action"))
	if err != nil || rec.OutcomeReason != o.OutcomeReason {
		t.Fatalf("refusal row = %+v, err = %v; want the refusal recorded under the action's key", rec, err)
	}
	again, fresh := h.fireOneCueAction(context.Background(), act, key, 0, "no-such-action", cueActivationIssuer{PrincipalID: "system:test"}, func() bool { return false })
	if fresh || again.Outcome != outcomeWordRefused {
		t.Fatalf("second attempt = %+v fresh=%v, want the recorded refusal replayed", again, fresh)
	}
	if dispatcher.callCount() != 0 {
		t.Fatalf("resolume calls = %d, want 0", dispatcher.callCount())
	}
}

// gatedResolumeDispatcher holds one Resolume action until released,
// optionally reporting it sent first.
type gatedResolumeDispatcher struct {
	fakeResolumeActionDispatcher
	gated              string
	signalSent         bool
	signalAfterRelease bool
	release            chan struct{}
	secondStart        chan struct{}
}

func (g *gatedResolumeDispatcher) Dispatch(ctx context.Context, action string, params map[string]any, now time.Time) (ResolumeActionResult, error) {
	if action == g.gated {
		if g.signalSent {
			sendsignal.Sent(ctx)
		}
		<-g.release
		if g.signalAfterRelease {
			sendsignal.Sent(ctx)
		}
	} else {
		close(g.secondStart)
	}
	return g.fakeResolumeActionDispatcher.Dispatch(ctx, action, params, now)
}

func TestDispatchCueActivationsSentFirstActionDoesNotHoldTheNext(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	g := &gatedResolumeDispatcher{
		fakeResolumeActionDispatcher: fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
			"launchColumn": {Outcome: ResolumeOutcomeConfirmed}, "blackout": {Outcome: ResolumeOutcomeConfirmed},
		}},
		gated: "launchColumn", signalSent: true, release: make(chan struct{}), secondStart: make(chan struct{}),
	}
	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.ResolumeActions = g
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	act.ScheduledAtNs = new(int64)

	started := time.Now()
	outcomes := h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	if waited := time.Since(started); waited >= cueActionsSendBudget {
		t.Fatalf("node dispatch waited %s, want it to start once both actions were sent", waited)
	}
	select {
	case <-g.secondStart:
	default:
		t.Fatal("the second action had not started while the first was still waiting for its confirmation")
	}
	if len(outcomes) != 1 || !outcomes[0].Confirmed {
		t.Fatalf("node outcomes = %+v, want the node dispatch confirmed", outcomes)
	}
	close(g.release)
	h.cueActivationFailToBlackWG.Wait()
}

func TestDispatchCueActivationsSlowUnsentFirstActionIsCappedAndTheRestAreLate(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	g := &gatedResolumeDispatcher{
		fakeResolumeActionDispatcher: fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
			"launchColumn": {Outcome: ResolumeOutcomeConfirmed}, "blackout": {Outcome: ResolumeOutcomeConfirmed},
		}},
		gated: "launchColumn", release: make(chan struct{}), secondStart: make(chan struct{}),
	}
	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.ResolumeActions = g
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	act.ScheduledAtNs = new(int64)
	defer func(old time.Duration) { cueActionsSendBudget = old }(cueActionsSendBudget)
	cueActionsSendBudget = 50 * time.Millisecond

	started := time.Now()
	outcomes := h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	if waited := time.Since(started); waited > cueFireHTTPWriteDeadlineMargin {
		t.Fatalf("node dispatch waited %s behind an action that was never sent", waited)
	}
	if len(outcomes) != 1 || !outcomes[0].Dispatched {
		t.Fatalf("node outcomes = %+v, want the node dispatch to go ahead", outcomes)
	}
	close(g.release)
	h.cueActivationFailToBlackWG.Wait()

	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(cueActionsActivationKey(act, 1), 1, "blackout-now"))
	if err != nil {
		t.Fatalf("second action's row: %v", err)
	}
	if !strings.HasPrefix(rec.OutcomeReason, cueActionLateReason) {
		t.Fatalf("second action reason = %q, want it reported late", rec.OutcomeReason)
	}
	nodeRec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), act.ActivationID)
	if err != nil {
		t.Fatalf("node activation row: %v", err)
	}
	var res cueActivationResultPayload
	if err := json.Unmarshal([]byte(nodeRec.ResultJSON), &res); err != nil || len(res.Actions) != 2 || res.Actions[0].Outcome != outcomeWordConfirmed {
		t.Fatalf("activation record actions = %+v (err %v), want both action outcomes on it", res.Actions, err)
	}
}

func TestDispatchCueActivationsNodeJoiningMidEntryDoesNotRefire(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{"launchColumn": {Outcome: ResolumeOutcomeConfirmed}}}
	h := newCueActionHandlers(setup, now, dispatcher)
	act.ScheduledAtNs = new(int64)

	h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()
	joined := act
	joined.ActivationID = "cueact-test-joined"
	h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act, "aaa-first-node": joined}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()
	if n := dispatcher.callCount(); n != 1 {
		t.Fatalf("resolume calls = %d, want the action fired once despite a node joining", n)
	}
}

func newGatedCueActionHandlers(setup *audioDispatchTestSetup, now time.Time, g *gatedResolumeDispatcher) *handlers {
	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.ResolumeActions = g
	return &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
}

func newGatedLaunchColumn(signalSent, signalAfterRelease bool) *gatedResolumeDispatcher {
	return &gatedResolumeDispatcher{
		fakeResolumeActionDispatcher: fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
			"launchColumn": {Outcome: ResolumeOutcomeConfirmed}, "blackout": {Outcome: ResolumeOutcomeConfirmed},
		}},
		gated: "launchColumn", signalSent: signalSent, signalAfterRelease: signalAfterRelease,
		release: make(chan struct{}), secondStart: make(chan struct{}),
	}
}

func TestCueActionsRunSentActionReadsAsWaitingUntilSettled(t *testing.T) {
	run := newCueActionsRun([]string{"a"}, time.Now().Add(time.Hour))
	run.markSent(0, time.Now())
	if got := run.snapshot()[0]; got.Outcome != outcomeWordUnconfirmed || got.OutcomeReason != "This action was sent and is waiting for its confirmation." {
		t.Fatalf("sent action = %+v, want unconfirmed and waiting for its confirmation", got)
	}
	run.set(0, v1.CueActionOutcome{ActionID: "a", Outcome: outcomeWordConfirmed}, true)
	run.markSent(0, time.Now())
	if got := run.snapshot()[0]; got.Outcome != outcomeWordConfirmed || got.OutcomeReason != "" {
		t.Fatalf("settled action = %+v, want the final outcome kept", got)
	}
}

func TestDispatchCueActivationsActionSentAfterTheCapIsLate(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	g := newGatedLaunchColumn(false, true)
	h := newGatedCueActionHandlers(setup, now, g)
	act.ScheduledAtNs = new(int64)
	defer func(old time.Duration) { cueActionsSendBudget = old }(cueActionsSendBudget)
	cueActionsSendBudget = 50 * time.Millisecond

	h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	close(g.release)
	h.cueActivationFailToBlackWG.Wait()

	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(cueActionsActivationKey(act, 1), 0, "song-one-column"))
	if err != nil {
		t.Fatalf("action row: %v", err)
	}
	if !strings.HasPrefix(rec.OutcomeReason, cueActionLateReason) {
		t.Fatalf("action reason = %q, want an action in flight at the cap and sent after it reported late", rec.OutcomeReason)
	}
	final := waitForCueActionsAudit(t, setup.svc, act.CueID)
	if got := auditActionOutcomes(t, final); len(got) != 1 || got[0].Outcome != outcomeWordConfirmed || !strings.HasPrefix(got[0].OutcomeReason, cueActionLateReason) {
		t.Fatalf("final audit actions = %+v, want the late confirmed outcome", got)
	}
}

func TestDispatchCueActivationsEmergencyStopHoldsQueuedActions(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActionFixture(t, setup, now, "song-one-column", "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	g := newGatedLaunchColumn(false, false)
	h := newGatedCueActionHandlers(setup, now, g)
	act.ScheduledAtNs = new(int64)
	defer func(old time.Duration) { cueActionsSendBudget = old }(cueActionsSendBudget)
	cueActionsSendBudget = 50 * time.Millisecond

	h.dispatchCueActivationsWithActions(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, 1, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.emergencyStops.Add(1)
	close(g.release)
	h.cueActivationFailToBlackWG.Wait()

	if got := resolumeCallActions(&g.fakeResolumeActionDispatcher); len(got) != 1 || got[0] != "launchColumn" {
		t.Fatalf("resolume calls = %v, want only the action already in flight before the stop", got)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(cueActionsActivationKey(act, 1), 1, "blackout-now"))
	if err != nil {
		t.Fatalf("held action's row: %v", err)
	}
	if o := cueActionOutcomeFromRecord("blackout-now", rec); o.Outcome != outcomeWordRefused || o.OutcomeReason != cueActionStoppedReason {
		t.Fatalf("held action = %+v, want refused because the show was stopped", o)
	}
}

func TestHandleActivateCueReturnsBeforeActionsConfirm(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, act := cueActionFixture(t, setup, now, "song-one-column")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	g := newGatedLaunchColumn(true, false)
	h := newGatedCueActionHandlers(setup, now, g)

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].Outcome != outcomeWordUnconfirmed || resp.Actions[0].OutcomeReason != "This action was sent and is waiting for its confirmation." {
		t.Fatalf("actions = %+v, want the sent action still waiting for its confirmation", resp.Actions)
	}
	close(g.release)
	final := waitForCueActionsAudit(t, setup.svc, act.CueID)
	if got := auditActionOutcomes(t, final); len(got) != 1 || got[0].Outcome != outcomeWordConfirmed {
		t.Fatalf("final audit actions = %+v, want the confirmed outcome", got)
	}
}
