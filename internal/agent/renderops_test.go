package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
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
	return newRenderOperations(sup, store, dir, multisync.NewTimeline(clock.now, multisync.Config{}), nil, "", discardLogger())
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

// minimalRenderApplyParams returns the render.surface.apply params map a
// real caller always sends: complete geometry, no FSEQ content.
// buildFSEQAssignment (renderops.go) now requires channelRange, geometry,
// and frameRate unconditionally — a bare or partial-geometry request is
// refused, not defaulted — because every real dispatcher (the
// coordinator's catalog-deploy establishment and a resolved sequence apply
// alike, internal/coordinator/api/renderdispatch.go) already sends a
// surface's complete geometry whether or not a sequence is assigned yet.
// Tests that only care about some OTHER aspect of an apply (auth tuple,
// output sink, unknown-key rejection, ...) start from this rather than a
// bare {"surfaceId": ...} map, which no real caller can produce.
func minimalRenderApplyParams(surfaceID string) map[string]any {
	return map[string]any{
		"surfaceId": surfaceID,
		"channelRange": map[string]any{
			"startChannel": float64(1),
			"channelCount": float64(12),
		},
		"geometry": map[string]any{
			"width":       float64(2),
			"height":      float64(2),
			"pixelFormat": "rgb",
		},
		"frameRate": float64(40),
	}
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

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", minimalRenderApplyParams("surface-1"))
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

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
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

// TestHandleMessageRenderSurfaceApplyRefusesIncompleteGeometry proves a
// bare or partial-geometry render.surface.apply is refused visibly
// (OutcomeFailed, a stated reason, nothing persisted, nothing started)
// rather than silently accepted or defaulted. No real caller ever omits
// channelRange, geometry, or frameRate — internal/coordinator/api/
// renderdispatch.go resolves a surface's complete geometry before every
// dispatch, content assigned or not — so buildFSEQAssignment now requires
// them unconditionally instead of deferring their validation to "once
// B3/B4 exist," which they now do.
func TestHandleMessageRenderSurfaceApplyRefusesIncompleteGeometry(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no geometry at all", map[string]any{"surfaceId": "surface-1"}},
		{"channelRange but no geometry or frameRate", map[string]any{
			"surfaceId":    "surface-1",
			"channelRange": map[string]any{"startChannel": float64(1), "channelCount": float64(12)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clock := &fakeClock{t: time.Now()}
			sup := newRenderTestSupervisor(t, clock)
			store := pipeline.NewAssignmentStore(dir)
			renderOps := newTestRenderOperations(sup, store, dir, clock)

			h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
			pub := newFakePublisher()

			cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", tc.params)
			topic, payload := buildCmdMessage(t, clock, cmd)
			h.HandleMessage(context.Background(), pub, topic, payload)

			result := decodeResultFromCall(t, pub.snapshot()[0])
			if result.Outcome != mqttproto.OutcomeFailed {
				t.Fatalf("Outcome = %q, want %q for %s; reason = %q", result.Outcome, mqttproto.OutcomeFailed, tc.name, result.Reason)
			}
			if result.Reason == "" {
				t.Fatalf("Reason is empty for a refused incomplete-geometry apply, want a stated reason")
			}

			reloaded, err := store.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(reloaded) != 0 {
				t.Fatalf("persisted assignments = %+v, want none (an incomplete-geometry apply must not persist)", reloaded)
			}
			if _, ok := sup.Snapshot("surface-1"); ok {
				t.Fatalf("a supervisor snapshot exists for surface-1, want none (nothing should have started)")
			}
		})
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

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
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

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	applyCmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", minimalRenderApplyParams("surface-1"))
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

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	applyCmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", minimalRenderApplyParams("surface-1"))
	topic, payload := buildCmdMessage(t, clock, applyCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)
	if result := decodeResultFromCall(t, pub.snapshot()[0]); result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("setup apply Outcome = %q, want confirmed; reason = %q", result.Outcome, result.Reason)
	}

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
	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, trigger, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	echoCmd := baseEchoCmd("cmd-1", "idem-1")
	topic, payload := buildCmdMessage(t, clock, echoCmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	select {
	case <-trigger:
		t.Fatalf("renderTrigger fired for a non-render action")
	default:
	}

	applyCmd := renderCmd("render.surface.apply", "cmd-2", "idem-2", minimalRenderApplyParams("surface-1"))
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
	raw := []byte(`{"surfaceId":"surface-1","channelRange":{"startChannel":1,"channelCount":12},"geometry":{"width":2,"height":2,"pixelFormat":"rgb"},"frameRate":40}`)
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

// TestApplySurfaceWithUnsupportedTransportNeverReportsSilentRunning is this
// seam's own regression test for the defect review found: a surface
// configured for a transport this build cannot actually attach a real sink
// for (hdmi — B4 implements ndi only) falls back to a diagnostic fakesink,
// which genuinely reaches PLAYING, but that must NEVER read as a plain,
// silent success (ADR-029). Both channels the review named must carry the
// gap: pipeline.state's own Reason (non-empty even though state is
// "running"), and surface.transport.available (false, with a stated
// reason) — proactively, from the apply itself, with no explicit
// render.transport.probe required.
func TestApplySurfaceWithUnsupportedTransportNeverReportsSilentRunning(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	params := minimalRenderApplyParams("surface-1")
	params["output"] = map[string]any{
		"transport": "hdmi",
		"hdmi":      map[string]any{"display": "HDMI-1"},
	}
	result, err := renderOps.applySurface(context.Background(), params, clock.now)
	if err != nil {
		t.Fatalf("applySurface: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("Confirmed = false, want true (the diagnostic pipeline genuinely reaches running); result = %+v", result)
	}
	val, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("Value = %#v, want a map", result.Value)
	}
	if val["state"] != "running" {
		t.Fatalf(`Value["state"] = %v, want "running" (the process really is in PLAYING)`, val["state"])
	}
	reason, _ := val["reason"].(string)
	if reason == "" {
		t.Fatalf(`Value["reason"] is empty for a "running" state whose sink is a diagnostic fallback — a silent success is exactly what ADR-029 forbids`)
	}
	if !strings.Contains(reason, "hdmi") {
		t.Errorf("Value[\"reason\"] = %q, want it to name hdmi specifically", reason)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot recorded for surface-1")
	}
	if snap.Reason == "" {
		t.Errorf("snap.Reason is empty for a degraded-output surface reporting running, want a real reason")
	}
	if snap.Transport != "hdmi" {
		t.Errorf("snap.Transport = %q, want hdmi", snap.Transport)
	}
	if snap.TransportAvailable == nil || *snap.TransportAvailable {
		t.Fatalf("snap.TransportAvailable = %v, want a non-nil false — this is known false from the pipeline's own construction, no probe needed", snap.TransportAvailable)
	}
	if snap.TransportReason == "" {
		t.Errorf("snap.TransportReason is empty, want a real reason")
	}
}

// TestApplySurfaceWithRealNDISinkNeverTouchesTransportAvailable proves the
// OTHER half: a surface whose spec genuinely got a real ndi sink must NOT
// have this apply-time mechanism claim transport.available is true (or
// touch it at all) — only a real [pipeline.ProbeNDISend] result (or the
// pipeline's own future evidence) is entitled to say NDI actually works;
// building a real sink is necessary but not sufficient evidence (ADR-026
// decision 6 — element/spec presence is not runtime presence).
func TestApplySurfaceWithRealNDISinkNeverTouchesTransportAvailable(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	params := minimalRenderApplyParams("surface-1")
	params["output"] = map[string]any{
		"transport": "ndi",
		"ndi":       map[string]any{"sourceName": "garage-window"},
	}
	if _, err := renderOps.applySurface(context.Background(), params, clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot recorded for surface-1")
	}
	if snap.Reason != "" {
		t.Errorf("snap.Reason = %q for a real ndi sink, want empty", snap.Reason)
	}
	if snap.TransportAvailable != nil {
		t.Errorf("snap.TransportAvailable = %v for a real ndi sink with no probe run, want nil (unprobed, never assumed true)", snap.TransportAvailable)
	}
}

// fakeProbeProcess is a [pipeline.ProcessHandle] that returns exactly one
// pre-loaded [pipeline.ExitResult] from Wait, for probeTransport's own
// tests — a different, simpler shape than fakeRenderStarter (above),
// which fires onRunningMarker for a long-lived supervised pipeline;
// [pipeline.ProbeNDISend] reads SawRunningMarker off ExitResult directly
// and never registers an onRunningMarker callback at all (see
// probe.go's runProbe), so this fake needs no callback handling.
type fakeProbeProcess struct{ exitCh chan pipeline.ExitResult }

func (p *fakeProbeProcess) Wait() pipeline.ExitResult { return <-p.exitCh }
func (p *fakeProbeProcess) Kill() error               { return nil }
func (p *fakeProbeProcess) Pid() int                  { return 1 }
func (p *fakeProbeProcess) Stdin() (io.Writer, error) { return nil, pipeline.ErrNoStdin }

// fakeProbeStarter returns a [pipeline.ProcessStarter] whose every process
// immediately reports result from Wait.
func fakeProbeStarter(result pipeline.ExitResult) pipeline.ProcessStarter {
	return func(_ context.Context, _ string, _ []string, _ func()) (pipeline.ProcessHandle, error) {
		p := &fakeProbeProcess{exitCh: make(chan pipeline.ExitResult, 1)}
		p.exitCh <- result
		return p, nil
	}
}

// TestProbeTransportOnNeverAppliedSurfaceRefusesAndCreatesNoPhantom is
// Finding 18: showmeshctl render probe <node> <typo'd-surface-id> used to
// silently manufacture a permanent phantom `surface` resource (Supervisor.
// SetTransportProbe's own runnerFor create-on-demand). probeTransport must
// now refuse outright, naming the surface, WITHOUT even running the probe
// process (the fake starter here would panic-free either way, but a real
// gst-launch-1.0 subprocess should never spin up for a surface nobody
// configured) and without leaving anything for sup.SnapshotAll to report.
func TestProbeTransportOnNeverAppliedSurfaceRefusesAndCreatesNoPhantom(t *testing.T) {
	t.Setenv("SHOWMESH_GST_LAUNCH", "/bin/true")

	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)
	probeRan := false
	renderOps.probeStarter = func(ctx context.Context, path string, argv []string, onRunning func()) (pipeline.ProcessHandle, error) {
		probeRan = true
		return fakeProbeStarter(pipeline.ExitResult{SawRunningMarker: true})(ctx, path, argv, onRunning)
	}

	_, err := renderOps.probeTransport(context.Background(), map[string]any{"surfaceId": "wal-01"}, clock.now)
	if err == nil {
		t.Fatalf("probeTransport on a never-applied surface: err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "wal-01") || !strings.Contains(err.Error(), "never been applied") {
		t.Fatalf("probeTransport error = %q, want it to name the surface and say it was never applied", err.Error())
	}
	if probeRan {
		t.Errorf("the probe process ran for a surface this node never applied; it must refuse before probing")
	}
	if snaps := sup.SnapshotAll(); len(snaps) != 0 {
		t.Fatalf("SnapshotAll() after a refused probe = %+v, want empty — no phantom surface", snaps)
	}
}

// TestProbeTransportAvailableRecordsOnSupervisorSnapshot proves
// probeTransport's wiring end to end: a real state-transition-confirmed
// probe (the fake starter stands in for the real gst-launch-1.0 subprocess;
// pipeline/probe_test.go covers the probe mechanism itself) records
// Transport/TransportAvailable on the named surface's snapshot, which is
// exactly what renderreport.go reads to build the next render report — and
// reports Confirmed: true with the matching Value.
func TestProbeTransportAvailableRecordsOnSupervisorSnapshot(t *testing.T) {
	t.Setenv("SHOWMESH_GST_LAUNCH", "/bin/true") // ResolveGstLaunch must succeed; the fake starter stands in for the rest.

	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)
	renderOps.probeStarter = fakeProbeStarter(pipeline.ExitResult{SawRunningMarker: true})

	// Finding 18: probeTransport now refuses a surface this node has never
	// applied, so this surface must exist first — matching every real
	// caller (an operator can only ever probe a surface that has at least
	// been assigned once).
	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("setup Apply: %v", err)
	}

	result, err := renderOps.probeTransport(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now)
	if err != nil {
		t.Fatalf("probeTransport: %v", err)
	}
	if !result.Confirmed {
		t.Errorf("Confirmed = false, want true")
	}
	val, ok := result.Value.(map[string]any)
	if !ok || val["available"] != true {
		t.Errorf("Value = %#v, want available=true", result.Value)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot recorded for surface-1")
	}
	if snap.Transport != "ndi" {
		t.Errorf("snap.Transport = %q, want ndi", snap.Transport)
	}
	if snap.TransportAvailable == nil || !*snap.TransportAvailable {
		t.Errorf("snap.TransportAvailable = %v, want true", snap.TransportAvailable)
	}
}

// TestProbeTransportUnavailableRecordsReason proves the false path carries
// a real, non-empty reason both in the OperationResult and on the
// supervisor's snapshot (mqttproto.RenderPayload.Validate requires
// TransportReason whenever TransportAvailable is false — this is what
// prevents renderreport.go from ever publishing a payload that fails its
// own wire-boundary validation).
func TestProbeTransportUnavailableRecordsReason(t *testing.T) {
	t.Setenv("SHOWMESH_GST_LAUNCH", "/bin/true")

	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)
	renderOps.probeStarter = fakeProbeStarter(pipeline.ExitResult{
		SawRunningMarker: false,
		StderrTail:       "ERROR: from element ...: Failed loading NDI SDK\n",
	})

	// Finding 18: probeTransport now refuses a surface this node has never
	// applied — see the identical setup step in
	// TestProbeTransportAvailableRecordsOnSupervisorSnapshot.
	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("setup Apply: %v", err)
	}

	result, err := renderOps.probeTransport(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now)
	if err != nil {
		t.Fatalf("probeTransport: %v", err)
	}
	if result.Confirmed {
		t.Errorf("Confirmed = true, want false")
	}
	val, ok := result.Value.(map[string]any)
	if !ok || val["available"] != false {
		t.Errorf("Value = %#v, want available=false", result.Value)
	}
	if reason, _ := val["reason"].(string); reason == "" {
		t.Errorf("Value[\"reason\"] is empty, want a real reason")
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot recorded for surface-1")
	}
	if snap.TransportAvailable == nil || *snap.TransportAvailable {
		t.Errorf("snap.TransportAvailable = %v, want false", snap.TransportAvailable)
	}
	if snap.TransportReason == "" {
		t.Errorf("snap.TransportReason is empty, want a real reason")
	}
}

// TestHandleMessageRenderTransportProbeFiresRenderTrigger proves
// render.transport.probe joins the other three render.* operations in
// isRenderAction, so a probe dispatched through the real command path
// republishes the render report immediately rather than waiting out the
// next tick.
func TestHandleMessageRenderTransportProbeFiresRenderTrigger(t *testing.T) {
	t.Setenv("SHOWMESH_GST_LAUNCH", "/bin/true")

	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)
	renderOps.probeStarter = fakeProbeStarter(pipeline.ExitResult{SawRunningMarker: true})

	// Finding 18: probeTransport now refuses a surface this node has never
	// applied — see the identical setup step in
	// TestProbeTransportAvailableRecordsOnSupervisorSnapshot.
	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("setup Apply: %v", err)
	}

	renderTrigger := make(chan struct{}, 1)
	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, renderTrigger, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := renderCmd("render.transport.probe", "cmd-1", "idem-1", map[string]any{"surfaceId": "surface-1"})
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	select {
	case <-renderTrigger:
	default:
		t.Fatalf("renderTrigger did not fire for render.transport.probe")
	}
}
