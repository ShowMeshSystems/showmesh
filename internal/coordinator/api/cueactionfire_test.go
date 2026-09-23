package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
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

	outcomes := h.dispatchCueActivations(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()

	if len(outcomes) != 1 || !outcomes[0].Confirmed {
		t.Fatalf("node outcomes = %+v, want one confirmed", outcomes)
	}
	if got := resolumeCallActions(dispatcher); len(got) != 2 || got[0] != "launchColumn" || got[1] != "blackout" {
		t.Fatalf("resolume calls = %v, want [launchColumn blackout]", got)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(act.ActivationID, 0, "song-one-column"))
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

	outcomes := h.dispatchCueActivations(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, cueActivationIssuer{PrincipalID: "system:test"}, nil)
	h.cueActivationFailToBlackWG.Wait()

	if len(outcomes) != 1 || !outcomes[0].Dispatched || !outcomes[0].Confirmed {
		t.Fatalf("node outcomes = %+v, want the node dispatch confirmed despite the failed action", outcomes)
	}
	if got := resolumeCallActions(dispatcher); len(got) != 2 {
		t.Fatalf("resolume calls = %v, want both actions attempted", got)
	}
	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), cueActionIdempotencyKey(act.ActivationID, 0, "song-one-column"))
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
		h.dispatchCueActivations(context.Background(), now, activations, cueActivationIssuer{PrincipalID: "system:test"}, nil)
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

	h.dispatchCueActivations(context.Background(), now, map[string]cueactivation.Activation{nodeID: act}, cueActivationIssuer{PrincipalID: "system:test"}, nil)
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

func TestFireOneCueActionRefusesDeletedActionWithoutDispatch(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, act := cueActionFixture(t, setup, now)
	dispatcher := &fakeResolumeActionDispatcher{}
	h := newCueActionHandlers(setup, now, dispatcher)

	o, fresh := h.fireOneCueAction(context.Background(), act, 0, "no-such-action", cueActivationIssuer{PrincipalID: "system:test"})
	if o.Outcome != outcomeWordRefused || !fresh {
		t.Fatalf("outcome = %+v fresh=%v, want a fresh refusal", o, fresh)
	}
	if _, again := h.fireOneCueAction(context.Background(), act, 0, "no-such-action", cueActivationIssuer{PrincipalID: "system:test"}); again {
		t.Fatalf("second attempt reported fresh, want it recognized as a repeat")
	}
	if dispatcher.callCount() != 0 {
		t.Fatalf("resolume calls = %d, want 0", dispatcher.callCount())
	}
}
