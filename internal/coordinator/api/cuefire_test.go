package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file proves POST /api/v1/cues/{id}/activate's own three load-
// bearing shapes: the accepted path (a node's own result confirms it),
// a refusal reported with its reason, and the per-node outcome shape
// (Dispatched/Confirmed/Outcome/OutcomeReason) - reusing
// cueactivationdispatch_test.go's real-store-plus-fakeAudioPublisher
// fixtures, since this route is a thin HTTP front onto the identical
// dispatchOneCueActivation this package's own dispatch tests already
// exercise.

func newCueFireTestRequest(t *testing.T, cueID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cues/"+cueID+"/activate", nil)
	req.SetPathValue("id", cueID)
	ac := authContext{
		ok: true,
		result: identity.Authenticated{
			Principal: identity.Principal{ID: "operator-1", Name: "Test Operator"},
			Form:      identity.FormSession,
		},
	}
	return req.WithContext(withAuthContext(context.Background(), ac))
}

// TestHandleActivateCueAcceptedConfirmed proves the accepted path: a
// single-node Cue whose fixture is fully authorized, and whose fake node
// result reports authorized, renders as 202 with one node outcome
// carrying Dispatched=true, Confirmed=true, Outcome="confirmed".
func TestHandleActivateCueAcceptedConfirmed(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	if resp.CueID != act.CueID {
		t.Fatalf("cueId = %q, want %q", resp.CueID, act.CueID)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1; nodes = %+v", len(resp.Nodes), resp.Nodes)
	}
	got := resp.Nodes[0]
	if got.NodeID != nodeID {
		t.Fatalf("nodeId = %q, want %q", got.NodeID, nodeID)
	}
	if !got.Dispatched || !got.Confirmed {
		t.Fatalf("Dispatched/Confirmed = %v/%v, want true/true", got.Dispatched, got.Confirmed)
	}
	if got.Outcome != outcomeWordConfirmed {
		t.Fatalf("outcome = %q, want %q", got.Outcome, outcomeWordConfirmed)
	}
	if got.OutcomeReason != "" {
		t.Fatalf("outcomeReason = %q, want empty for a confirmed outcome", got.OutcomeReason)
	}
}

// TestHandleActivateCueNodeRefusalReportsReason proves a node's own
// refusal is reported with its reason, never collapsed into a bare
// "not confirmed" - the same evidence
// TestDispatchOneCueActivationRecordsNodeRefusalNotDispatchedSuccess
// proves one layer down, now read off the wire.
func TestHandleActivateCueNodeRefusalReportsReason(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	setup.pub.result = cueActivationNodeResultPayload(false, "stale-catalog")

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1; nodes = %+v", len(resp.Nodes), resp.Nodes)
	}
	got := resp.Nodes[0]
	if !got.Dispatched {
		t.Fatalf("Dispatched = false, want true (the publish itself succeeded)")
	}
	if got.Confirmed {
		t.Fatalf("Confirmed = true, want false: the node itself refused this activation")
	}
	if got.Outcome != outcomeWordRefused {
		t.Fatalf("outcome = %q, want %q", got.Outcome, outcomeWordRefused)
	}
	if got.OutcomeReason != "stale-catalog" {
		t.Fatalf("outcomeReason = %q, want %q (the node's own reported refusal)", got.OutcomeReason, "stale-catalog")
	}
}

// TestHandleActivateCueUnknownCueRefused proves an id naming no show.cue
// at all is refused with a stated reason, not silently accepted as
// "zero nodes participate" - those are two different facts.
func TestHandleActivateCueUnknownCueRefused(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	putShowForTest(t, setup.st, "halloween-2026", "Halloween 2026")
	putActiveShowForTest(t, setup.st, "halloween-2026")

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, "no-such-cue"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	var problem v1.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v; body = %s", err, rec.Body.String())
	}
	if problem.Detail == "" {
		t.Fatalf("problem.Detail is empty, want a stated reason")
	}
}

// TestHandleActivateCueNoActiveShowRefused proves a Fire click reaching
// zero nodes because no show is active at all is refused (400), never
// answered 202 with an empty nodes array: an explicit operator action
// that dispatches nothing is a refusal to act, not "nothing to report".
func TestHandleActivateCueNoActiveShowRefused(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	putShowForTest(t, setup.st, "halloween-2026", "Halloween 2026")
	putAudioOnlyCueForTest(t, setup.st, "cue-1", "halloween-2026")
	// Deliberately no putActiveShowForTest: no show is active at all.

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, "cue-1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var problem v1.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v; body = %s", err, rec.Body.String())
	}
	if problem.Detail == "" {
		t.Fatalf("problem.Detail is empty, want a stated reason")
	}
}

// TestHandleActivateCueReachesTheSharedSchedulingStep proves the direct-
// fire route reaches the IDENTICAL ADR-049 decision 3 scheduling step the
// Playlist path's dispatchCueActivations wraps (cueactivationschedule_test.go's
// own TestScheduleCueActivations* tests exercise that step directly with
// two real audio-bearing nodes; show.cue's audio output is still
// single-target pre-merge, so a Fire click against ONE cueId can only
// ever resolve ONE audio-bearing node, see that file's own doc comment).
// A single-audio-node Cue never attempted scheduling at all, so this
// proves the trivial, wire-visible half of ADR-049 decision 5: Aligned
// reports true with no instant and no reason, end to end through the real
// HTTP response, not merely by code inspection.
func TestHandleActivateCueReachesTheSharedSchedulingStep(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, rec.Body.String())
	}
	if !resp.Aligned {
		t.Fatalf("Aligned = false, want true: a single audio-bearing node never attempts scheduling and is never reported as an unaligned failure it did not attempt")
	}
	if resp.UnalignedReason != "" {
		t.Fatalf("UnalignedReason = %q, want empty", resp.UnalignedReason)
	}
	if resp.ScheduledAtNs != nil {
		t.Fatalf("ScheduledAtNs = %v, want nil: no reading round was attempted for one audio-bearing node", resp.ScheduledAtNs)
	}
}

// TestHandleActivateCueNoParticipatingNodeRefused proves a Fire click
// against a real, active show whose Cue catalog resolves cueID on ZERO
// nodes (no node is declared at all) is refused (400) with a reason
// distinguishable from TestHandleActivateCueNoActiveShowRefused's own -
// the same "an explicit operator action that dispatches nothing is a
// refusal to act" rule, for the OTHER cause an empty activations map can
// have.
func TestHandleActivateCueNoParticipatingNodeRefused(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	putShowForTest(t, setup.st, "halloween-2026", "Halloween 2026")
	putAudioOnlyCueForTest(t, setup.st, "cue-1", "halloween-2026")
	putActiveShowForTest(t, setup.st, "halloween-2026")
	// Deliberately no declared node at all: st.ListNodes returns none, so
	// resolveActivationsForCue has nothing to iterate.

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, "cue-1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var problem v1.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v; body = %s", err, rec.Body.String())
	}
	if problem.Detail == "" {
		t.Fatalf("problem.Detail is empty, want a stated reason")
	}
}

// TestCueFireSurvivesServerWriteTimeout proves a Fire slower than a real
// server's own short WriteTimeout still writes its JSON body, via
// handleActivateCue's own SetWriteDeadline extension.
func TestCueFireSurvivesServerWriteTimeout(t *testing.T) {
	// A REAL current time, not a fixed testNow: SetWriteDeadline sets an
	// absolute deadline anchored to h.now(), so a fixed-in-the-past clock
	// would make that deadline already elapsed before this test's real
	// wall-clock write happens.
	now := time.Now()
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// No reply ever arrives, paced past the server's own short
	// WriteTimeout below, while staying comfortably inside
	// cueFireHTTPWriteDeadline.
	setup.pub.awaitErr = broker.ErrResponseDeadlineExceeded
	setup.pub.onAwaitResponse = func() { time.Sleep(300 * time.Millisecond) }

	deps := setup.deps()
	deps.AssetManifests = setup.st
	api := New(deps, Options{Clock: fixedClock(now), Logger: testLogger()})

	status, body := postThroughShortWriteTimeoutServer(t, api.Handler, "/api/v1/cues/"+act.CueID+"/activate", "", auth)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (a dispatch slower than the server's own WriteTimeout must still succeed); body: %s", status, http.StatusAccepted, body)
	}
	var resp v1.CueActivateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, body)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1; body: %s", len(resp.Nodes), body)
	}
	if resp.Nodes[0].Outcome != outcomeWordUnconfirmed {
		t.Fatalf("outcome = %q, want %q: the connection surviving the server's short WriteTimeout must have delivered the real unconfirmed body; body: %s", resp.Nodes[0].Outcome, outcomeWordUnconfirmed, body)
	}
}
