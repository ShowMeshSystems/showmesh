package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// newTestRenderOperations builds a renderOperations against dir, a fresh
// timeline driven by clock, and a discarding logger — every call site below
// used to call newRenderOperations(sup, store) directly before B3 added the
// asset dir / timeline / logger parameters.
func newTestRenderOperations(sup *pipeline.Supervisor, store *pipeline.AssignmentStore, dir string, clock *fakeClock) *renderOperations {
	return newRenderOperations(sup, store, dir, multisync.NewTimeline(clock.now, multisync.Config{}), discardLogger())
}

// newRenderTestSupervisor builds a Supervisor over fakeRenderStarter, whose
// process reaches "running" immediately and never exits on its own — the
// same fake this package's renderreport_test.go already uses.
func newRenderTestSupervisor(t *testing.T, clock *fakeClock) *pipeline.Supervisor {
	t.Helper()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})
	return sup
}

func renderCmd(action, commandID, idempotencyKey string, params map[string]any) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:          commandID,
		IdempotencyKey:     idempotencyKey,
		Action:             action,
		Target:             mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params:             params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
}

// TestHandleMessageRenderSurfaceApplyConfirmed proves the end-to-end path:
// a real render.surface.apply command dispatched through HandleMessage
// starts the pipeline (via the real Supervisor, not a mock of it), polls
// for evidence that post-dates the dispatch, reports OutcomeConfirmed, and
// persists the assignment to disk (build contract ruling 4).
func TestHandleMessageRenderSurfaceApplyConfirmed(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", map[string]any{"surfaceId": "surface-1"})
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (render.* has no extra observation publish like agent.echo)", len(calls))
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("Outcome = %q, want %q; reason = %q", result.Outcome, mqttproto.OutcomeConfirmed, result.Reason)
	}
	if result.ExecutedAt == nil {
		t.Fatalf("ExecutedAt is nil, want set")
	}
	if result.Evidence == nil || result.Evidence.ObservedAt == nil {
		t.Fatalf("Evidence.ObservedAt is nil, want set")
	}
	// The core requirement: evidence must post-date dispatch, never predate
	// or exactly equal it via a stale reading collapsed with the write.
	if result.Evidence.ObservedAt.Before(*result.ExecutedAt) {
		t.Fatalf("Evidence.ObservedAt %s is before ExecutedAt %s", result.Evidence.ObservedAt, result.ExecutedAt)
	}

	// The assignment must be durable: a fresh AssignmentStore instance
	// reading the same directory sees it.
	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].SurfaceID != "surface-1" {
		t.Fatalf("persisted assignments = %+v, want one entry for surface-1", reloaded)
	}
}

// TestHandleMessageRenderSurfaceApplyMissingSurfaceID proves params
// validation runs before anything is dispatched: a missing surfaceId is
// OutcomeFailed, and nothing is persisted or started.
func TestHandleMessageRenderSurfaceApplyMissingSurfaceID(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", map[string]any{})
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeFailed)
	}

	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 0 {
		t.Fatalf("persisted assignments = %+v, want none (invalid params must not persist)", reloaded)
	}
}

// TestHandleMessageRenderSurfaceApplyRejectsUnknownKey proves the
// reject-unknown-keys allowlist actually runs: a typo'd param key fails the
// command rather than being silently ignored.
func TestHandleMessageRenderSurfaceApplyRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", map[string]any{
		"surfaceId": "surface-1",
		"shwo":      "typo-of-show", // deliberate typo
	})
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	result := decodeResultFromCall(t, pub.snapshot()[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want %q for an unrecognized param key; reason = %q", result.Outcome, mqttproto.OutcomeFailed, result.Reason)
	}
}

// TestHandleMessageRenderSurfaceClearRemovesAssignment proves
// render.surface.clear both stops the pipeline and removes the persisted
// assignment, so a later boot does not resume it.
func TestHandleMessageRenderSurfaceClearRemovesAssignment(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	applyCmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", map[string]any{"surfaceId": "surface-1"})
	topic, payload := buildCmdMessage(t, clock, applyCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)
	if result := decodeResultFromCall(t, pub.snapshot()[0]); result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("setup apply Outcome = %q, want confirmed", result.Outcome)
	}

	clearCmd := renderCmd("render.surface.clear", "cmd-2", "idem-2", map[string]any{"surfaceId": "surface-1"})
	topic2, payload2 := buildCmdMessage(t, clock, clearCmd)
	h.HandleMessage(context.Background(), pub, topic2, payload2)

	result := decodeResultFromCall(t, pub.snapshot()[1])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("clear Outcome = %q, want confirmed; reason = %q", result.Outcome, result.Reason)
	}

	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 0 {
		t.Fatalf("persisted assignments after clear = %+v, want none", reloaded)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok || snap.State != pipeline.StateStopped {
		t.Fatalf("supervisor snapshot after clear = %+v (ok=%v), want StateStopped", snap, ok)
	}
}

// TestHandleMessageRenderPipelineRestart proves render.pipeline.restart
// dispatches through the supervisor and confirms a fresh running state.
func TestHandleMessageRenderPipelineRestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	applyCmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", map[string]any{"surfaceId": "surface-1"})
	topic, payload := buildCmdMessage(t, clock, applyCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	restartCmd := renderCmd("render.pipeline.restart", "cmd-2", "idem-2", map[string]any{"surfaceId": "surface-1"})
	topic2, payload2 := buildCmdMessage(t, clock, restartCmd)
	h.HandleMessage(context.Background(), pub, topic2, payload2)

	result := decodeResultFromCall(t, pub.snapshot()[1])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("restart Outcome = %q, want confirmed; reason = %q", result.Outcome, result.Reason)
	}
}

// TestHandleMessageRenderTriggerSignalsOnlyForRenderActions proves
// isRenderAction gates the renderTrigger correctly: an agent.echo command
// must not fire it, and a render.* command must.
func TestHandleMessageRenderTriggerSignalsOnlyForRenderActions(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	trigger := make(chan struct{}, 1)
	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, trigger, clock.now, discardLogger())
	pub := newFakePublisher()

	echoCmd := baseEchoCmd("cmd-1", "idem-1")
	topic, payload := buildCmdMessage(t, clock, echoCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	select {
	case <-trigger:
		t.Fatalf("renderTrigger fired for a non-render action")
	default:
	}

	applyCmd := renderCmd("render.surface.apply", "cmd-2", "idem-2", map[string]any{"surfaceId": "surface-1"})
	topic2, payload2 := buildCmdMessage(t, clock, applyCmd)
	h.HandleMessage(context.Background(), pub, topic2, payload2)

	select {
	case <-trigger:
	default:
		t.Fatalf("renderTrigger did not fire for render.surface.apply")
	}
}

// TestParseSurfaceIDRejectsUnsafeCharacters is a direct unit test on the
// path-safety boundary: surfaceId is joined into a filesystem path by
// pipeline.AssignmentStore, so a path separator or ".." must be rejected
// here, before it ever reaches a path.Join call — mirroring assets.go's
// validateAssetFilename requirement one level up.
func TestParseSurfaceIDRejectsUnsafeCharacters(t *testing.T) {
	cases := []string{"../escape", "a/b", "", "a b", "..", "."}
	for _, c := range cases {
		if _, err := parseSurfaceID("render.surface.apply", map[string]any{"surfaceId": c}); err == nil {
			t.Errorf("parseSurfaceID(%q): want error, got nil", c)
		}
	}
}

// TestRenderOperationsRoundTripJSONParams is a sanity check that params
// (decoded from JSON as map[string]any, exactly as command.go's
// DecodeCmdPayload produces) survive the persistence round trip used by
// applySurface, since encoding/json's float64-for-every-number behaviour is
// a standing hazard in this codebase (see assets.go's parseAssetFetchParams
// doc comment on the same issue).
func TestRenderOperationsRoundTripJSONParams(t *testing.T) {
	raw := []byte(`{"surfaceId":"surface-1","frameRate":40}`)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := params["frameRate"].(float64); !ok {
		t.Fatalf("frameRate decoded as %T, want float64 (sanity check on the test fixture itself)", params["frameRate"])
	}

	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.applySurface(context.Background(), params, clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assignments, want 1", len(got))
	}
	var reparsed map[string]any
	if err := json.Unmarshal(got[0].RawParams, &reparsed); err != nil {
		t.Fatalf("Unmarshal(persisted RawParams): %v", err)
	}
	if reparsed["frameRate"] != float64(40) {
		t.Fatalf("persisted frameRate = %v, want 40", reparsed["frameRate"])
	}
}
