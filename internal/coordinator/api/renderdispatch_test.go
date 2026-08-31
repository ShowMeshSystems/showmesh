package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
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

	// onPublish, when set, runs synchronously right after a publish is
	// recorded - a test's only way to inject an event (such as canceling
	// the caller's own request context) at the exact moment a real
	// publish would have already reached the wire, without a real
	// broker.
	onPublish func()
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
	if f.onPublish != nil {
		f.onPublish()
	}
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
	ctx := context.Background()
	rev := int64(1)
	if obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID); err == nil {
		rev = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActiveConfigKind, ObjectID: config.ShowActiveObjectID, Revision: rev, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.active/active: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, rev); err != nil {
		t.Fatalf("activate config revision show.active/active: %v", err)
	}
}

func renderCreateAsset(t *testing.T, st *store.Store, showID, sequenceID, targetKind, targetID, contentHash, filename string) store.AssetRecord {
	t.Helper()
	rec, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-" + targetKind + "-" + targetID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: targetKind, TargetID: targetID, MediaType: "fseq", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return rec
}

// surfacePipelineStateObs builds evidence stamped as coming from nodeID —
// [noderender.SourceFor], not the bare collector id — matching what a real
// noderender.Collector.Poll actually writes to the store (see that
// package's SourceFor doc comment).
func surfacePipelineStateObs(nodeID, surfaceID, state string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		noderender.SignalSurfacePipelineState, state, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource(noderender.SourceFor(nodeID)),
	))
}

// surfaceContentFSEQFilenameObs builds the surface.content.fseq_filename
// evidence noderender.Collector.Poll emits whenever a node's persisted
// assignment for surfaceID carries a real fseqFilename (internal/agent/
// renderreport.go's applyContentIdentity) — i.e. the surface is rendering
// REAL content, not establishRenderAssignment's own no-sequence
// placeholder. Tests use this to distinguish the two "currently assigned"
// cases renderSurfaceHasRealContent exists to tell apart.
func surfaceContentFSEQFilenameObs(nodeID, surfaceID, filename string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		noderender.SignalSurfaceContentFSEQFilename, filename, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource(noderender.SourceFor(nodeID)),
	))
}

// surfaceDroppedAbsenceObs builds the exact absence evidence
// noderender.Collector.Poll emits for a surface a node stops reporting —
// see that package's Poll doc comment — so this file's tests can prove the
// confirmation path against the real shape rather than a hand-picked one.
func surfaceDroppedAbsenceObs(nodeID, surfaceID string, collectedAt time.Time) observation.Observation {
	return mustObs(observation.NotCollected(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		noderender.SignalSurfacePipelineState,
		"node "+nodeID+" no longer reports this surface",
		observation.WithCollectedAt(collectedAt), observation.WithSource(noderender.SourceFor(nodeID)),
	))
}

// surfaceTransportAvailableObs mirrors surfacePipelineStateObs for
// surface.transport.available, this seam's own confirmation signal — stamped
// as coming from nodeID for the identical reason surfacePipelineStateObs is.
func surfaceTransportAvailableObs(nodeID, surfaceID string, available bool, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID},
		observation.SignalID(renderSignalTransportAvailable), available, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource(noderender.SourceFor(nodeID)),
	))
}

// errInjectingConfigStore wraps a real *store.Store, satisfying ConfigStore
// via embedding, but lets a test force GetConfigRevision to fail as a
// stand-in for a transient coordinator-side store error (SQLite busy, disk
// full, and the like) — the exact case Finding 5 is about: this must never
// be reported as a node-side rejection.
type errInjectingConfigStore struct {
	*store.Store
	getConfigRevisionErr error
}

func (e *errInjectingConfigStore) GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error) {
	if e.getConfigRevisionErr != nil {
		return store.ConfigRevisionRecord{}, e.getConfigRevisionErr
	}
	return e.Store.GetConfigRevision(ctx, kind, id, revision)
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

// TestRenderApplyCoordinatorStoreFailureIsInternalErrorNeverDispatched
// proves Finding 5's fix: a transient coordinator-side store error while
// resolving render.surface.apply's params (here, GetConfigRevision) must
// become a real 500, and must never dispatch a command carrying
// "params": null that a node would refuse for reasons the coordinator
// never actually experienced. Revert resolveRenderApplyParams's
// GetConfigRevision path to `return nil, nil` and this test fails: the
// handler proceeds to dispatch a nil-params command and returns 200.
func TestRenderApplyCoordinatorStoreFailureIsInternalErrorNeverDispatched(t *testing.T) {
	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	renderPutShow(t, setup.st, "halloween-2026", "Halloween 2026")
	renderPutActiveShow(t, setup.st, "halloween-2026")
	renderPutSurface(t, setup.st, "wall-1", "halloween-2026", "media-01")
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", "hash-a", "opener.fseq")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	deps := setup.deps()
	deps.Config = &errInjectingConfigStore{Store: setup.st, getConfigRevisionErr: errors.New("simulated transient sqlite error")}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a coordinator-side store failure is not a caller-attributable refusal); body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — a coordinator-side failure to resolve params must never dispatch a command with null params", setup.pub.count())
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
		setup.obs.setObs([]observation.Observation{surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second))})
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

// TestRenderDispatchOutcomeSurvivesClientDisconnect proves this route's
// post-dispatch bookkeeping (recording dispatchedAt, polling for
// confirmation evidence, recording the resolved outcome, and writing the
// outcome audit entry) is not cancellable by a client that walks away
// mid-request - matching TestFPPCommandOutcomeSurvivesClientDisconnect's
// and audiodispatch.go's own bgCtx cutover. The request's own context is
// canceled from inside the fake publisher right after the publish that
// dispatch already recorded, simulating an abandoned HTTP client; despite
// that, the command must still resolve in the store with dispatchedAt,
// resolvedAt, and a real outcome audit entry.
func TestRenderDispatchOutcomeSurvivesClientDisconnect(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	setup.pub.onPublish = func() {
		cancel()
		// Evidence arrives after the client disconnects but before the
		// confirm deadline — proves the confirmation wait keeps running
		// server-side past the disconnect rather than merely recording
		// an already-decided outcome.
		go func() {
			time.Sleep(50 * time.Millisecond)
			setup.obs.setObs([]observation.Observation{surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second))})
		}()
	}

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req.WithContext(ctx))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ServeHTTP still completes even though the request's own context was canceled mid-wait); body: %s", resp.StatusCode, body)
	}

	var result struct {
		Command struct {
			CommandID string `json:"commandId"`
			Outcome   string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed — the client's own disconnect must not abort the server-side confirmation wait; body: %s", result.Command.Outcome, body)
	}

	rec, err := setup.st.GetCommand(context.Background(), result.Command.CommandID)
	if err != nil {
		t.Fatalf("GetCommand: %v", err)
	}
	if rec.State != "resolved" {
		t.Fatalf("stored command state = %q, want resolved (the request's own cancellation must not leave it stuck)", rec.State)
	}
	if rec.DispatchedAt == nil {
		t.Fatalf("stored command dispatchedAt is nil, want set")
	}
	if rec.ResolvedAt == nil {
		t.Fatalf("stored command resolvedAt is nil, want set")
	}
}

// TestRenderApplyDefaultsIdleOutputToBlackWhenUnconfigured proves
// resolveRenderApplyParams resolves render.settings.idleOutput to a
// CONCRETE value even when nothing has ever been written for that kind —
// the node is told black, the built-in default, and never has to know the
// coordinator's own "nothing configured" posture.
func TestRenderApplyDefaultsIdleOutputToBlackWhenUnconfigured(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
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

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	_, body := doRawRequest(t, api.Handler, req)

	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1; body: %s", setup.pub.count(), body)
	}
	if got := setup.pub.payload[0].Payload.Params["idleOutput"]; got != config.RenderIdleOutputDefault {
		t.Fatalf("dispatched params[idleOutput] = %v, want %q (the built-in default; render.settings was never written)", got, config.RenderIdleOutputDefault)
	}
}

// TestRenderApplyResolvesIdleOutputFromRenderSettings proves the OTHER
// half: once render.settings has been written, resolveRenderApplyParams
// resolves and sends THAT value, not the built-in default.
func TestRenderApplyResolvesIdleOutputFromRenderSettings(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
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

	settingsPayload, err := config.EncodeRenderSettingsPayload(config.RenderSettingsPayload{
		IdleOutput: config.RenderIdleOutputDiagnostic,
		RestartPolicy: config.RenderRestartPolicy{
			InitialDelaySeconds: 1, MaxDelaySeconds: 30, MaxConsecutiveFastFailures: 5,
		},
	})
	if err != nil {
		t.Fatalf("encode render.settings payload: %v", err)
	}
	renderPutConfig(t, setup.st, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID, settingsPayload)

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
	_, body := doRawRequest(t, api.Handler, req)

	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1; body: %s", setup.pub.count(), body)
	}
	if got := setup.pub.payload[0].Payload.Params["idleOutput"]; got != config.RenderIdleOutputDiagnostic {
		t.Fatalf("dispatched params[idleOutput] = %v, want %q (the value written to render.settings)", got, config.RenderIdleOutputDiagnostic)
	}

	var result struct {
		Command struct {
			IdleOutput string `json:"idleOutput"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.IdleOutput != config.RenderIdleOutputDiagnostic {
		t.Fatalf("response command.idleOutput = %q, want %q — the resolved value must also be surfaced to the caller", result.Command.IdleOutput, config.RenderIdleOutputDiagnostic)
	}
}

// TestRenderApplyIgnoresEvidenceSnapshottedBeforeDispatchEvenIfReceivedAfter
// proves Finding 4's core scenario directly, independent of the restart
// counter: a report the node SNAPSHOTTED (ObservedAt) before this command
// was even dispatched, but which the coordinator only RECEIVED
// (CollectedAt) afterward — an ordinary race on the agent's periodic
// publish ticker — must not confirm. The old bug fenced on CollectedAt,
// which this evidence clears; the fix fences on ObservedAt, which it does
// not.
func TestRenderApplyIgnoresEvidenceSnapshottedBeforeDispatchEvenIfReceivedAfter(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
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

	go func() {
		time.Sleep(20 * time.Millisecond)
		// ObservedAt (node's own sample time) is BEFORE dispatch (testNow);
		// CollectedAt (coordinator receipt) is AFTER it.
		setup.obs.setObs([]observation.Observation{
			surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(-time.Millisecond), testNow.Add(time.Millisecond)),
		})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
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
		t.Fatalf("outcome = %q, want unconfirmed — the evidence was snapshotted by the node BEFORE dispatch, only received after; body: %s", result.Command.Outcome, body)
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
		Command struct {
			CommandID string `json:"commandId"`
			Replay    bool   `json:"replay"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body2), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Command.Replay {
		t.Fatalf("replay = false, want true on the second request")
	}
	if n := countRenderAuditReplayEntries(t, setup.svc, result.Command.CommandID); n != 1 {
		t.Fatalf("AuditReplay entries for command %s = %d, want 1", result.Command.CommandID, n)
	}

	// The stored value must carry store.CallerIntentRenderRequest's own
	// tag, not the bare identity JSON: an untagged pair of writer and
	// replay-reader round-trips fine on its own and would not catch a
	// future writer silently dropping the tag, which is exactly the
	// ambiguity this column's rename exists to close.
	rec, err := setup.st.GetCommand(context.Background(), result.Command.CommandID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	const wantCallerIntent = `render-request:{"action":"render.surface.clear","node":"media-01","surface":"wall-1","sequenceId":""}`
	if rec.CallerIntent != wantCallerIntent {
		t.Errorf("commands.caller_intent = %q, want %q", rec.CallerIntent, wantCallerIntent)
	}
}

// countRenderAuditReplayEntries counts identity.AuditReplay entries
// recorded against commandID.
func countRenderAuditReplayEntries(t *testing.T, svc identity.Service, commandID string) int {
	t.Helper()
	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.CommandID == commandID && e.Kind == identity.AuditReplay {
			n++
		}
	}
	return n
}

// TestRenderDispatchReplayDifferentNodeIsConflict: the same idempotencyKey
// reused against a different node is refused as a 409, not silently
// replayed under a target this request never named.
func TestRenderDispatchReplayDifferentNodeIsConflict(t *testing.T) {
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

	body := `{"idempotencyKey":"conflict-key-node"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", body, token)
	resp1, respBody1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp1.StatusCode, respBody1)
	}
	var first struct {
		Command struct {
			CommandID string `json:"commandId"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody1), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-02/render/surfaces/wall-1/clear", body, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch (different node) status = %d, want 409; body: %s", resp2.StatusCode, respBody2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a conflicting idempotency key must never dispatch", setup.pub.count())
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(respBody2), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "media-01") || !strings.Contains(p.Detail, "media-02") {
		t.Fatalf("detail = %q, want it to name both the existing and the requested node", p.Detail)
	}
	if n := countRenderAuditReplayEntries(t, setup.svc, first.Command.CommandID); n != 1 {
		t.Fatalf("AuditReplay entries for command %s = %d, want 1", first.Command.CommandID, n)
	}
}

// TestRenderDispatchReplayDifferentSurfaceIsParamsConflict: surfaceId lives
// inside params, so a reused key naming a different surface on the same
// node must be caught by the params comparison.
func TestRenderDispatchReplayDifferentSurfaceIsParamsConflict(t *testing.T) {
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

	body := `{"idempotencyKey":"conflict-key-surface"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", body, token)
	resp1, respBody1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp1.StatusCode, respBody1)
	}
	var first struct {
		Command struct {
			CommandID string `json:"commandId"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody1), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-2/clear", body, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch (different surface) status = %d, want 409; body: %s", resp2.StatusCode, respBody2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a conflicting idempotency key must never dispatch", setup.pub.count())
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(respBody2), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "wall-1") || !strings.Contains(p.Detail, "wall-2") {
		t.Fatalf("detail = %q, want it to name both the existing and the requested params", p.Detail)
	}
	if n := countRenderAuditReplayEntries(t, setup.svc, first.Command.CommandID); n != 1 {
		t.Fatalf("AuditReplay entries for command %s = %d, want 1", first.Command.CommandID, n)
	}
}

// TestRenderDispatchReplaySameTargetDifferentActionIsConflict: same node
// and surface, but a different action, is still a conflict.
func TestRenderDispatchReplaySameTargetDifferentActionIsConflict(t *testing.T) {
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

	body := `{"idempotencyKey":"conflict-key-action"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", body, token)
	if resp, b := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp.StatusCode, b)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/restart", body, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch (different action) status = %d, want 409; body: %s", resp2.StatusCode, respBody2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a conflicting idempotency key must never dispatch", setup.pub.count())
	}
	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(respBody2), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
}

// TestRenderApplyReplayDifferentSequenceIsParamsConflict: apply's resolved
// params are non-trivial (the FSEQ identity, not just surfaceId), so this
// proves the params comparison catches a reused key naming a different
// sequenceId there too.
func TestRenderApplyReplayDifferentSequenceIsParamsConflict(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
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
	renderCreateAsset(t, setup.st, "halloween-2026", "closer", store.AssetTargetKindNode, "media-01", "hash-b", "closer.fseq")

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"conflict-key-apply"}`, token)
	if resp, b := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp.StatusCode, b)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"closer","idempotencyKey":"conflict-key-apply"}`, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch (different sequenceId) status = %d, want 409; body: %s", resp2.StatusCode, respBody2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a conflicting idempotency key must never dispatch", setup.pub.count())
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(respBody2), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "opener") || !strings.Contains(p.Detail, "closer") {
		t.Fatalf("detail = %q, want it to name both the existing and the requested sequenceId", p.Detail)
	}
}

// TestRenderApplyReplaySameSequenceReturnsOriginalResultAfterAssetSuperseded
// proves a replay must be judged against the CALLER's own request
// identity (surfaceId, sequenceId), never against resolveRenderApplyParams's
// MUTABLE resolution. Between the two identical requests the current asset
// for "opener" is superseded by a new upload with a different content hash —
// the same caller input would now resolve to different params. The replay
// must still return the FIRST dispatch's own result (hash-a), never
// re-resolve and never dispatch a second command.
func TestRenderApplyReplaySameSequenceReturnsOriginalResultAfterAssetSuperseded(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
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

	body := `{"sequenceId":"opener","idempotencyKey":"replay-state-changed"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply", body, token)
	resp1, respBody1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp1.StatusCode, respBody1)
	}
	var first struct {
		Command struct {
			CommandID string `json:"commandId"`
			Outcome   string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody1), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	// Supersede the current asset for "opener" — the same caller input now
	// resolves to a DIFFERENT fseqContentHash.
	renderCreateAsset(t, setup.st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", "hash-b", "opener-v2.fseq")

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply", body, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (identical caller input must replay the original result); body: %s", resp2.StatusCode, respBody2)
	}
	var second struct {
		Command struct {
			CommandID string `json:"commandId"`
			Replay    bool   `json:"replay"`
			Outcome   string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody2), &second); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !second.Command.Replay {
		t.Fatalf("replay = false, want true")
	}
	if second.Command.CommandID != first.Command.CommandID {
		t.Fatalf("replay commandId = %q, want the original command %q", second.Command.CommandID, first.Command.CommandID)
	}
	if second.Command.Outcome != first.Command.Outcome {
		t.Fatalf("replay outcome = %q, want the original outcome %q", second.Command.Outcome, first.Command.Outcome)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a replay must never dispatch a second command", setup.pub.count())
	}
	if got := setup.pub.payload[0].Payload.Params["fseqContentHash"]; got != "hash-a" {
		t.Fatalf("dispatched fseqContentHash = %v, want the ORIGINAL hash-a — only one command was ever dispatched", got)
	}
}

// TestRenderApplyReplaySameSequenceReturnsOriginalResultEvenWhenResolutionWouldNowFail
// proves a second wrong behaviour a naive replay could have: between the
// two identical requests the active show changes out from under the
// surface, so a FRESH resolution of
// this exact same caller input would now be refused outright ("not the
// active show"). The replay must still return the first dispatch's own
// result, never attempt to re-resolve and never surface that refusal.
func TestRenderApplyReplaySameSequenceReturnsOriginalResultEvenWhenResolutionWouldNowFail(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
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

	body := `{"sequenceId":"opener","idempotencyKey":"replay-resolution-would-fail"}`
	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply", body, token)
	resp1, respBody1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp1.StatusCode, respBody1)
	}
	var first struct {
		Command struct {
			CommandID string `json:"commandId"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody1), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	// The active show changes: a FRESH resolution of "wall-1"/"opener" would
	// now be refused ("belongs to show ... not the active show").
	renderPutActiveShow(t, setup.st, "christmas-2026")

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply", body, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (a replay must return the original result, never re-resolve and fail); body: %s", resp2.StatusCode, respBody2)
	}
	var second struct {
		Command struct {
			CommandID string `json:"commandId"`
			Replay    bool   `json:"replay"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(respBody2), &second); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !second.Command.Replay {
		t.Fatalf("replay = false, want true")
	}
	if second.Command.CommandID != first.Command.CommandID {
		t.Fatalf("replay commandId = %q, want the original command %q", second.Command.CommandID, first.Command.CommandID)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a replay must never dispatch a second command", setup.pub.count())
	}
}

// TestRenderApplyReplayDifferentSequenceIsConflictEvenWhenNewSequenceFailsToResolve
// proves the idempotency check runs BEFORE apply's mutable resolution: the
// second request names a sequenceId with NO asset at all, which — resolved
// on its own — would be a 400 ("no asset found"). Reused against an
// idempotency key already bound to a genuinely different sequenceId, it
// must be answered as a 409 conflict naming the mismatch, and must never
// even attempt to resolve or dispatch.
func TestRenderApplyReplayDifferentSequenceIsConflictEvenWhenNewSequenceFailsToResolve(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
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
	// Deliberately no asset for "missing-sequence".

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req1 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"conflict-key-unresolvable"}`, token)
	if resp, b := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d, want 200; body: %s", resp.StatusCode, b)
	}

	req2 := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"missing-sequence","idempotencyKey":"conflict-key-unresolvable"}`, token)
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch (different, unresolvable sequenceId) status = %d, want 409 (never a 400 from an attempted resolution); body: %s", resp2.StatusCode, respBody2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1 — a conflicting idempotency key must never dispatch", setup.pub.count())
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(respBody2), &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "opener") || !strings.Contains(p.Detail, "missing-sequence") {
		t.Fatalf("detail = %q, want it to name both the existing and the requested sequenceId", p.Detail)
	}
}

// TestRenderNodeSourceForMatchesNodeRenderPackage pins renderNodeSourceFor's
// duplicated format against noderender.SourceFor's real one: this package
// cannot import that collector package (TestPackageNeverImportsACollector),
// so the two must be kept in sync by a test rather than the compiler.
func TestRenderNodeSourceForMatchesNodeRenderPackage(t *testing.T) {
	for _, nodeID := range []string{"media-01", "render-a", "x"} {
		if got, want := renderNodeSourceFor(nodeID), noderender.SourceFor(nodeID); got != want {
			t.Errorf("renderNodeSourceFor(%q) = %q, want %q (noderender.SourceFor)", nodeID, got, want)
		}
	}
}

// TestRenderApplyIgnoresAnotherNodesConflictingEvidence is the two-node
// collision case (CLAUDE.md Track B seam B2b review finding): a DIFFERENT
// node ("other-node") already reports surface "wall-1" as "running" with
// fresh evidence, while the dispatch target "media-01" has reported
// nothing about it yet. A confirmation loop that resolved across every
// source ([ResolveObservations], which a nodeless caller legitimately
// wants) would pick other-node's "running" reading and falsely confirm a
// command dispatched to media-01. It must instead time out unconfirmed,
// because media-01 itself has never reported this surface.
func TestRenderApplyIgnoresAnotherNodesConflictingEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
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

	// A DIFFERENT node's fresher, matching evidence for the SAME surface
	// id — must never be read as media-01's own.
	setup.obs.setObs([]observation.Observation{
		surfacePipelineStateObs("other-node", "wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second)),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
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
		t.Fatalf("outcome = %q, want %q — media-01 itself never reported this surface, so another node's matching evidence must not confirm it",
			result.Command.Outcome, "unconfirmed")
	}
}

// --- render.surface.clear confirms on the dropped-surface absence too ---
//
// Supervisor.Clear (internal/agent/pipeline) no longer reports a cleared
// surface as "stopped" forever — it stops reporting it entirely, and
// noderender.Collector.Poll turns that into an explicit absence
// observation (see collector.go's Poll doc comment). A clear command's
// confirmation must accept that absence as confirming evidence, or a real
// clear can never confirm again — see evaluateRenderSurfaceState's own
// doc comment for the ADR-003 argument.

// TestRenderClearConfirmsOnDroppedSurfaceAbsence proves the fix: a
// clear dispatched to media-01, confirmed purely by noderender's absence
// observation for wall-1 arriving after dispatch — never a "stopped"
// value observation at all.
func TestRenderClearConfirmsOnDroppedSurfaceAbsence(t *testing.T) {
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

	// Evidence arrives shortly after dispatch, dated after testNow —
	// proves the confirm loop actually polls for it.
	go func() {
		time.Sleep(50 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{surfaceDroppedAbsenceObs("media-01", "wall-1", testNow.Add(time.Second))})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
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
		t.Fatalf("outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
	// showmeshctl prints OutcomeReason as plain text, not OutcomeState
	// (cmd_render_command.go's reportRenderCommandResult), and
	// test/integration/render_dispatch_test.go's own CLI-level assertion
	// checks THAT text for a literal `"stopped"` — pinned here too, against
	// the DECODED field (not the raw JSON body, where the same quotes are
	// escaped as \"stopped\"), so a regression is caught at the unit level
	// before the much slower integration suite has to find it.
	if !strings.Contains(result.Command.OutcomeReason, `"stopped"`) {
		t.Errorf(`outcomeReason = %q, want it to name the confirmed "stopped" state even though it was reached via absence`, result.Command.OutcomeReason)
	}
}

// TestRenderClearIgnoresAbsenceThatPredatesDispatch is the ADR-003 fence
// this project has paid for before (CLAUDE.md: a command confirmed 179
// microseconds after its own dispatch, off a pre-dispatch reading): an
// absence observation that predates the clear dispatch must not confirm
// it, even though it is the right surface, the right node, and an absence.
func TestRenderClearIgnoresAbsenceThatPredatesDispatch(t *testing.T) {
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

	// Stale: collected well BEFORE this request's own dispatch instant
	// (testNow), e.g. left over from an earlier, unrelated clear.
	setup.obs.setObs([]observation.Observation{surfaceDroppedAbsenceObs("media-01", "wall-1", testNow.Add(-time.Hour))})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/clear", `{"idempotencyKey":"key-1"}`, token)
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
		t.Fatalf("outcome = %q, want unconfirmed — the only evidence on record predates this dispatch; body: %s", result.Command.Outcome, body)
	}
}

// TestRenderApplyDoesNotConfirmOnAbsence proves the absence-confirms
// exception is scoped to render.surface.clear's "stopped" desired state
// only: an apply (wanting "running") dispatched while the only evidence is
// a fresh, post-dispatch absence must NOT confirm — an absent surface is
// never evidence that it is running.
func TestRenderApplyDoesNotConfirmOnAbsence(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
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

	go func() {
		time.Sleep(20 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{surfaceDroppedAbsenceObs("media-01", "wall-1", testNow.Add(time.Second))})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/apply",
		`{"sequenceId":"opener","idempotencyKey":"key-1"}`, token)
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
		t.Fatalf("outcome = %q, want unconfirmed — an absence is never evidence the surface reached \"running\"; body: %s", result.Command.Outcome, body)
	}
}

// --- render.pipeline.restart's own confirmation shape (Finding 4) ---
//
// wantState "running" is exactly what a healthy, never-restarted surface
// already reports, so a fence keyed to the coordinator's receipt time (the
// pre-fix bug) could trivially confirm off a pre-existing reading. An
// EARLIER version of this fix additionally required
// surface.pipeline.restart_count to move past a pre-dispatch baseline —
// reverted after TestRenderApplyClearRestartAgainstRealAgent
// (test/integration/render_dispatch_test.go) proved it against a real
// agent: internal/agent/pipeline.runner's cmdRestart branch preserves the
// existing restartCount (restartCount only increments on the crash-exit
// branch, never on an operator-issued restart), so requiring it to
// increase made every real restart command time out. The ObservedAt fence
// alone is sufficient: runner.setState only stamps a fresh ObservedAt on a
// REAL transition, and cmdRestart unconditionally runs
// stopCurrent+attemptStart (Starting, then Running), so a stale
// pre-dispatch "running" reading can never satisfy it.

// TestRenderRestartIgnoresPreDispatchEvidence proves the ObservedAt fence
// (shared with apply/clear via evaluateRenderSurfaceState) also governs
// restart: a "running" reading whose node-reported ObservedAt predates
// dispatch must not confirm — the classic 179-microsecond shape, reused
// here for the newest command.
func TestRenderRestartIgnoresPreDispatchEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 100 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	// A pre-existing "running" reading that predates dispatch — the exact
	// state a restart wants, but stale evidence of it.
	setup.obs.setObs([]observation.Observation{
		surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(-time.Hour), testNow.Add(-time.Hour)),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/restart", `{"idempotencyKey":"key-1"}`, token)
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
		t.Fatalf("outcome = %q, want unconfirmed — the only evidence on record predates this dispatch; body: %s", result.Command.Outcome, body)
	}
}

// TestRenderRestartConfirmsOnFreshPostDispatchEvidence proves the other
// half: once a "running" reading whose ObservedAt post-dates dispatch
// arrives — the shape a genuine restart's Starting-then-Running transition
// produces — the command confirms.
func TestRenderRestartConfirmsOnFreshPostDispatchEvidence(t *testing.T) {
	renderCommandConfirmDeadline = 2 * time.Second
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	// Stale pre-dispatch "running", exactly what a restart's target
	// surface already reports before the command is even sent.
	setup.obs.setObs([]observation.Observation{
		surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(-time.Minute), testNow.Add(-time.Minute)),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	go func() {
		time.Sleep(50 * time.Millisecond)
		// A fresh reading, ObservedAt after dispatch — the real restart's
		// eventual re-entry into "running".
		setup.obs.setObs([]observation.Observation{
			surfacePipelineStateObs("media-01", "wall-1", "running", testNow.Add(time.Second), testNow.Add(time.Second)),
		})
	}()

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/restart", `{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1", setup.pub.count())
	}
	if action := setup.pub.payload[0].Payload.Action; action != "render.pipeline.restart" {
		t.Fatalf("dispatched action = %q, want render.pipeline.restart", action)
	}
	var result struct {
		Command struct{ Outcome string } `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
}

// TestRenderRestartUnconfirmedWithFailedPipelineSetsPipelineFailed is
// Finding 15's own coordinator-side proof, against the real HTTP handler
// and the real evaluateRenderSurfaceState/confirmRenderCommand path (only
// the observation store is a fake, exactly like every other test in this
// file): when the freshest post-dispatch surface.pipeline.state evidence
// is itself the pipeline's own reported "failed" value, the response's
// PipelineFailed field is true, never inferred by showmeshctl (or any
// other caller) from OutcomeState, which never equals "failed" — see
// evaluateRenderSurfaceState's own doc comment on the branch that sets
// this. Revert that branch (or the pipelineFailed plumbing through
// confirmRenderCommand/executeRenderDispatch) and this test's assertion on
// pipelineFailed fails while Outcome/OutcomeState stay unaffected — this
// is the coordinator-side half of the claim
// TestRenderRestartUnconfirmedWithFailedPipelineExits23
// (test/integration/render_pipeline_failed_test.go) proves end to end
// through the real showmeshctl binary.
func TestRenderRestartUnconfirmedWithFailedPipelineSetsPipelineFailed(t *testing.T) {
	renderCommandConfirmDeadline = 200 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
	// Fresh, post-dispatch evidence reporting the pipeline's own "failed"
	// state — never confirms "running", and never times out on absence
	// either, so this exercises the "current but wrong value" branch,
	// never the not_collected/stale ones Finding 17's own fix covers.
	setup.obs.setObs([]observation.Observation{
		surfacePipelineStateObs("media-01", "wall-1", "failed", testNow.Add(time.Second), testNow.Add(time.Second)),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newRenderRequest(t, http.MethodPost, "/api/v1/nodes/media-01/render/surfaces/wall-1/restart", `{"idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var result struct {
		Command struct {
			Outcome        string `json:"outcome"`
			OutcomeState   string `json:"outcomeState"`
			PipelineFailed bool   `json:"pipelineFailed"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Command.Outcome != "unconfirmed" {
		t.Fatalf("outcome = %q, want unconfirmed; body: %s", result.Command.Outcome, body)
	}
	if result.Command.OutcomeState != "current" {
		t.Fatalf("outcomeState = %q, want %q — the evidence itself is fresh, only its VALUE is wrong; "+
			"OutcomeState must never be used to carry a pipeline state; body: %s", result.Command.OutcomeState, "current", body)
	}
	if !result.Command.PipelineFailed {
		t.Fatalf("pipelineFailed = false, want true; body: %s", body)
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
			surfaceTransportAvailableObs("media-01", "wall-1", false, testNow.Add(time.Second), testNow.Add(time.Second)),
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
		surfaceTransportAvailableObs("media-01", "wall-1", true, testNow.Add(-time.Hour), testNow.Add(-time.Hour)),
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
