package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Track B seam B2b-front's own acceptance proof, following
// fppcommand_handler_test.go's established pattern exactly: a real
// store.Store, a real identity.Service over it, and a fake in place of
// the one genuinely external dependency (here, the MQTT publish) — never
// a hand-built v1 wire struct asserted against a fake.

// fakeRenderPublisher records every publish so a test can assert exactly
// one command was dispatched, to which topic, and decode its payload.
type fakeRenderPublisher struct {
	mu      sync.Mutex
	topics  []string
	payload []mqttCmdEnvelopeForTest
	err     error
}

// mqttCmdEnvelopeForTest decodes just enough of the published envelope for
// this file's own assertions (params + action), without importing
// pkg/mqttproto's own Envelope type into every test — the test's Publish
// signature already receives raw bytes, matching the real
// RenderPublisher interface exactly.
type mqttCmdEnvelopeForTest struct {
	Payload struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
	} `json:"payload"`
}

func (f *fakeRenderPublisher) Publish(_ context.Context, topic string, _ byte, _ bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.topics = append(f.topics, topic)
	var env mqttCmdEnvelopeForTest
	_ = json.Unmarshal(payload, &env)
	f.payload = append(f.payload, env)
	return nil
}

func (f *fakeRenderPublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.topics)
}

type renderDispatchTestSetup struct {
	st  *store.Store
	svc identity.Service
	obs *dynamicObservationLister
	pub *fakeRenderPublisher
}

func newRenderDispatchTestSetup(t *testing.T, now func() time.Time) *renderDispatchTestSetup {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &renderDispatchTestSetup{st: st, svc: svc, obs: &dynamicObservationLister{}, pub: &fakeRenderPublisher{}}
}

func (s *renderDispatchTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: s.obs,
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Commands: s.st, Config: s.st,
		AssetManifests: s.st, RenderPublisher: s.pub,
	}
}

func newRenderRequest(t *testing.T, method, path, body, bearerToken string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func renderPutSurface(t *testing.T, st *store.Store, surfaceID, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: surfaceID, Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode show.surface payload: %v", err)
	}
	renderPutConfig(t, st, config.ShowSurfaceConfigKind, surfaceID, payload)
}

func renderPutConfig(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "api"}); err != nil {
		t.Fatalf("create config revision %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate config revision %s/%s: %v", kind, id, err)
	}
}

func renderPutShow(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	renderPutConfig(t, st, config.ShowConfigKind, id, payload)
}

func renderPutActiveShow(t *testing.T, st *store.Store, showID string) {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	renderPutConfig(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, payload)
}

func renderCreateAsset(t *testing.T, st *store.Store, showID, sequenceID, targetKind, targetID, contentHash, filename string) store.AssetRecord {
	t.Helper()
	rec, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-" + targetKind + "-" + targetID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: targetKind, TargetID: targetID, MediaType: "fseq", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return rec
}

func surfacePipelineStateObs(surfaceID, state string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		observation.SignalID(renderSignalPipelineState), state, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("node-render"),
	))
}

// surfaceTransportAvailableObs mirrors surfacePipelineStateObs for
// surface.transport.available, this seam's own confirmation signal.
func surfaceTransportAvailableObs(surfaceID string, available bool, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		observation.SignalID(renderSignalTransportAvailable), available, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("node-render"),
	))
}

// --- Auth ---

func TestRenderDispatchRefusedUnauthenticated(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{}`, "")
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — an unauthenticated request must never reach dispatch", setup.pub.count())
	}
}

func TestRenderDispatchRefusedForbiddenViewer(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "render:command") {
		t.Fatalf("body = %q, want it to name the missing scope render:command", body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — a forbidden request must never reach dispatch", setup.pub.count())
	}
}

// --- render.surface.apply asset resolution refusal (build contract
// ruling 4) ---

func TestRenderApplyRefusesWhenNoAssetResolves(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")
	// Deliberately no asset created for sequence "opener".

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	var problem struct{ Detail string }
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem response: %v", err)
	}
	if !strings.Contains(problem.Detail, `no asset found for surface "wall-1"`) || !strings.Contains(problem.Detail, `sequence "opener"`) {
		t.Fatalf("detail = %q, want it to name the unresolved surface and sequence explicitly", problem.Detail)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — a refusal must never dispatch a partial assignment", setup.pub.count())
	}
}

func TestRenderApplyRefusesOnAmbiguousAsset(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")
	// Two CURRENT assets for the same sequence: one targeted at the node,
	// one show-wide — ExpectedAssetsForNode returns both, which this
	// handler must refuse rather than guess between.
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", "hash-a", "opener-a.fseq")
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindShow, "", "hash-b", "opener-b.fseq")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "ambiguous") {
		t.Fatalf("body = %q, want it to say ambiguous", body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0", setup.pub.count())
	}
}

// TestRenderApplyDispatchesCompleteAssignmentAndConfirms is this seam's
// own end-to-end proof: a resolvable apply publishes ONE command whose
// params carry every field renderApplyKnownKeys
// (internal/agent/renderops.go) requires, including the resolved
// fseqFilename/fseqContentHash, and confirms once surface.pipeline.state
// evidence dated after dispatch reports "running".
func TestRenderApplyDispatchesCompleteAssignmentAndConfirms(t *testing.T) {
	renderCommandConfirmDeadline = 2 * time.Second
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", "hash-a", "opener.fseq")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Evidence arrives shortly after dispatch, dated after testNow — proves
	// the confirm loop actually polls rather than reading a stale snapshot.
	go func() {
		time.Sleep(50 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{surfacePipelineStateObs("wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second))})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1", setup.pub.count())
	}
	env := setup.pub.payload[0]
	if env.Payload.Action != "render.surface.apply" {
		t.Fatalf("dispatched action = %q, want render.surface.apply", env.Payload.Action)
	}
	for _, key := range []string{"surfaceId", "show", "name", "node", "channelRange", "geometry", "frameRate", "output", "fseqFilename", "fseqContentHash"} {
		if _, ok := env.Payload.Params[key]; !ok {
			t.Errorf("dispatched params missing key %q — build contract ruling 4 requires a COMPLETE self-contained assignment", key)
		}
	}
	if got := env.Payload.Params["fseqFilename"]; got != "opener.fseq" {
		t.Errorf("fseqFilename = %v, want opener.fseq", got)
	}
	if got := env.Payload.Params["fseqContentHash"]; got != "hash-a" {
		t.Errorf("fseqContentHash = %v, want hash-a", got)
	}

	var result struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
}

// TestRenderDispatchReportsUnconfirmedWithoutEvidence proves the OTHER
// half of ADR-003: no evidence ever arrives, so the deadline elapses and
// this reports unconfirmed rather than assuming the publish succeeded.
func TestRenderDispatchReportsUnconfirmedWithoutEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1", setup.pub.count())
	}
	var result struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "unconfirmed" {
		t.Fatalf("outcome = %q, want unconfirmed", result.Command.Outcome)
	}
}

// TestRenderDispatchReplayReturnsExistingOutcomeWithoutRepublishing proves
// idempotency: the same key dispatched twice publishes exactly once.
func TestRenderDispatchReplayReturnsExistingOutcomeWithoutRepublishing(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"idempotencyKey":"replay-key"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", body, token)
	resp1, _ := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200", resp1.StatusCode)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a replayed idempotency key must not dispatch a second time", setup.pub.count())
	}
	var result struct {
		Command struct{ Replay bool } `json:"command"`
	}
	if err := json.Unmarshal([]byte(body2), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Command.Replay {
		t.Fatalf("replay = false, want true on the second request")
	}
}

// --- render.transport.probe ---

// TestRenderTransportProbeDispatchesAndConfirmsOnUnavailableEvidence is
// this operation's own named requirement: a probe that correctly reports
// the NDI runtime ABSENT is just as confirmed as one reporting it present
// — evaluateRenderTransportProbe confirms on evidence FRESHNESS, never on
// evidence VALUE. If this regressed to matching a desired boolean, this
// test would report unconfirmed instead.
func TestRenderTransportProbeDispatchesAndConfirmsOnUnavailableEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 2 * time.Second
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	go func() {
		time.Sleep(50 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{
			surfaceTransportAvailableObs("wall-1", false, testNow.Add(time.Second), testNow.Add(time.Second)),
		})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/transport-probe",
		`{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1", setup.pub.count())
	}
	if action := setup.pub.payload[0].Payload.Action; action != "render.transport.probe" {
		t.Fatalf("dispatched action = %q, want render.transport.probe", action)
	}

	var result struct {
		Command struct {
			Outcome       string `json:"outcome"`
			OutcomeReason string `json:"outcomeReason"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed (a fresh false reading is a genuine answer, not a failure to answer); body: %s", result.Command.Outcome, body)
	}
	if !strings.Contains(result.Command.OutcomeReason, "false") {
		t.Fatalf("outcomeReason = %q, want it to name the actual (false) reading", result.Command.OutcomeReason)
	}
}

// TestRenderTransportProbeReportsUnconfirmedWithoutFreshEvidence proves
// the other half: a pre-dispatch reading (or none at all) does not confirm
// — evaluateRenderTransportProbe's freshness fence, mirroring
// evaluateRenderSurfaceState's identical rule for pipeline state.
func TestRenderTransportProbeReportsUnconfirmedWithoutFreshEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	// A STALE reading, dated well before dispatch: must not confirm.
	setup.obs.setObs([]observation.Observation{
		surfaceTransportAvailableObs("wall-1", true, testNow.Add(-time.Hour), testNow.Add(-time.Hour)),
	})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/transport-probe",
		`{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var result struct {
		Command struct{ Outcome string } `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "unconfirmed" {
		t.Fatalf("outcome = %q, want unconfirmed (only a pre-dispatch reading exists); body: %s", result.Command.Outcome, body)
	}
}

// TestRenderTransportProbeNotReachableByGET proves ADR-024's rule directly:
// no state change is reachable by GET. net/http.ServeMux's own
// method-mismatch handling (the route is registered "POST ...") is what
// enforces this; this test exists so a future accidental re-registration
// as a bare pattern (dropping the "POST " prefix) fails loudly here rather
// than silently reopening a command as a GET.
func TestRenderTransportProbeNotReachableByGET(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodGet, "/api/v1/nodes/media-01/render/surfaces/wall-1/transport-probe", "", token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("GET transport-probe status = 200, want anything but 200: no state change may be reachable by GET (ADR-024); body: %s", body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — a GET must never dispatch a command", setup.pub.count())
	}
}
