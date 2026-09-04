package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// errFakeResolumeDispatch is this file's own injected follow-up failure,
// for TestEmergencyStopFollowUpFailureDoesNotFailTheStop.
var errFakeResolumeDispatch = errors.New("emergencystop_test: injected resolume dispatch failure")

// This file is the emergency-stop feature's own end-to-end coverage: a real store, a real
// identity service, a real FPP command endpoint (httptest.NewServer,
// nightShutdownFixture's own pattern), and a fake Resolume dispatcher for
// follow-up actions, never a hand-built v1 struct asserted against
// itself.

type emergencyStopFixture struct {
	api         *API
	st          *store.Store
	svc         identity.Service
	adminToken  string
	viewer      identity.Principal
	viewerToken string
	resolume    *fakeResolumeActionDispatcher

	mu       sync.Mutex
	commands []string
}

func (f *emergencyStopFixture) sentCommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func newEmergencyStopFixture(t *testing.T, now time.Time) *emergencyStopFixture {
	t.Helper()
	return newEmergencyStopFixtureWithDeps(t, now, nil, nil, nil, nil, "player-01")
}

// newEmergencyStopFixtureWithInstances builds count configured FPP
// instances, all pointing at the SAME fake server, for exercising the
// concurrent dispatch path in emergencyStopAllInstances.
func newEmergencyStopFixtureWithInstances(t *testing.T, now time.Time, count int) *emergencyStopFixture {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("player-%02d", i+1)
	}
	return newEmergencyStopFixtureWithDeps(t, now, nil, nil, nil, nil, ids...)
}

// newEmergencyStopFixtureWithDeps builds the fixture with caller-supplied
// FPPLister/NightSessionStore/ResolumeLister/AudioSessionPublisher
// overrides (e.g. one that errors, for this file's own blocking-1/
// blocking-2 coverage, or a fakeResolumeLister/fakeAudioPublisher
// carrying configured Resolume instances or canned node results for this
// file's own three-target-kind coverage), or, when any is nil, that
// dependency's own no-op default (Dependencies.withDefaults' "nothing
// configured" behavior - a real, honest empty result, never an error): a
// fake FPP pointing at a fresh httptest server with one FPPInstanceView
// per id in instanceIDs, and the real store for night sessions.
func newEmergencyStopFixtureWithDeps(t *testing.T, now time.Time, fpp FPPLister, nightSessions NightSessionStore, resolumeLister ResolumeLister, audioPub AudioSessionPublisher, instanceIDs ...string) *emergencyStopFixture {
	t.Helper()
	f := &emergencyStopFixture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.commands = append(f.commands, r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(srv.Close)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	f.adminToken = mustIssueToken(t, svc, admin.ID)
	f.viewer = mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	f.viewerToken = mustIssueToken(t, svc, f.viewer.ID)

	f.resolume = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}

	workingFPP := &fakeFPPLister{}
	if len(instanceIDs) > 0 {
		views := make([]FPPInstanceView, len(instanceIDs))
		for i, id := range instanceIDs {
			views[i] = FPPInstanceView{InstanceID: id, Endpoint: srv.URL}
		}
		workingFPP.views = views
	}
	if fpp == nil {
		fpp = workingFPP
	}
	if nightSessions == nil {
		nightSessions = st
	}

	// Setup (creating the show and its own follow-up action) runs against
	// a SEPARATE handlers instance with a known-working FPP lister,
	// sharing the SAME store: show.action's own PUT validates any
	// action's instanceId against the configured FPP endpoint list
	// regardless of that action's own integration, so an intentionally
	// erroring fpp/nightSessions override for the test under study must
	// never be on the path setup itself depends on.
	setupAPI := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		FPP:      workingFPP,
		Identity: svc, Config: st, Commands: st, NightSessions: st,
		Macros: &fakeMacroRunner{}, ResolumeActions: f.resolume,
		Resolume: resolumeLister, AudioPublisher: audioPub,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	mustPutShow(t, setupAPI, f.adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, setupAPI, f.adminToken, "worklights-on", validShowActionResolumeBlackoutBody)

	deps := Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		FPP:      fpp,
		Identity: svc, Config: st, Commands: st, NightSessions: nightSessions,
		Macros: &fakeMacroRunner{}, ResolumeActions: f.resolume,
		Resolume: resolumeLister, AudioPublisher: audioPub,
	}.withDefaults()

	f.api = New(deps, Options{
		Clock: fixedClock(now), Logger: testLogger(),
		FPPCommandConfirmDeadline: 50 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	f.st = st
	f.svc = svc
	return f
}

// erroringNightSessionStore embeds a real *store.Store and overrides ONLY
// GetCurrentNightSession, so every OTHER NightSessionStore method (InTx
// included) still works exactly as the real store would: this file's own
// blocking-2 coverage needs a failing READ, not a fake reimplementing the
// whole interface.
type erroringNightSessionStore struct {
	*store.Store
	err error
}

func (e *erroringNightSessionStore) GetCurrentNightSession(ctx context.Context) (store.NightSessionRecord, bool, error) {
	return store.NightSessionRecord{}, false, e.err
}

func (f *emergencyStopFixture) putConfig(t *testing.T, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop", body,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, respBody := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.emergencystop: status = %d; body: %s", resp.StatusCode, respBody)
	}
}

func emergencyStopEmptyLevelsBody() string {
	return `{"stop":{"actions":[]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`
}

func emergencyStopWorklightsOnStopBody() string {
	return `{"stop":{"actions":["worklights-on"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`
}

// --- level 1: stop ---

func TestEmergencyStopDispatchesFPPStopAndFollowUp(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopWorklightsOnStopBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	stopOutcomes := result["stopOutcomes"].([]any)
	if len(stopOutcomes) != 1 {
		t.Fatalf("stopOutcomes has %d entries, want 1", len(stopOutcomes))
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1", got)
	}
	followUps := result["followUps"].([]any)
	if len(followUps) != 1 {
		t.Fatalf("followUps has %d entries, want 1", len(followUps))
	}
	if f.resolume.callCount() != 1 {
		t.Fatalf("resolume dispatch calls = %d, want 1 (the configured follow-up)", f.resolume.callCount())
	}
	if _, present := result["nightSession"]; present {
		t.Error("level 1 (stop) response carries a nightSession field; level 1 must never touch night-session state")
	}
}

// A follow-up action's own failure must never fail the stop. This is the
// degrade-safely rule this build's PR body states as its own top
// priority.
func TestEmergencyStopFollowUpFailureDoesNotFailTheStop(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.resolume.err = errFakeResolumeDispatch
	f.putConfig(t, emergencyStopWorklightsOnStopBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a follow-up failure must not turn a successful stop into an HTTP error); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	stopOutcomes := result["stopOutcomes"].([]any)
	if outcome := stopOutcomes[0].(map[string]any)["outcome"]; outcome != "confirmed" && outcome != "unconfirmed" {
		t.Fatalf("stop outcome = %v, want a real dispatch outcome unaffected by the follow-up's own failure", outcome)
	}
	followUps := result["followUps"].([]any)
	if reason := followUps[0].(map[string]any)["outcomeReason"]; reason == "" {
		t.Error("failed follow-up's own outcomeReason is empty")
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1 (the follow-up's failure must not prevent or duplicate the stop)", got)
	}
}

func TestEmergencyStopWithNoActionsConfiguredHasEmptyFollowUps(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if followUps := result["followUps"].([]any); len(followUps) != 0 {
		t.Fatalf("followUps = %v, want empty: a level with nothing configured must work exactly as well as a fully configured one", followUps)
	}
	if f.resolume.callCount() != 0 {
		t.Fatalf("resolume dispatch calls = %d, want 0", f.resolume.callCount())
	}
}

// --- three target kinds, concurrently ---

// stopOutcomesByTargetKind decodes result.stopOutcomes and indexes it by
// targetKind, failing the test if any kind carries more or less than
// exactly one entry - every test in this section configures exactly one
// target per kind, so "more than one" would itself be a defect worth
// failing loudly on rather than silently taking the first.
func stopOutcomesByTargetKind(t *testing.T, result map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range result["stopOutcomes"].([]any) {
		entry := raw.(map[string]any)
		kind, _ := entry["targetKind"].(string)
		if _, dup := out[kind]; dup {
			t.Fatalf("stopOutcomes carries more than one entry for targetKind %q: %v", kind, result["stopOutcomes"])
		}
		out[kind] = entry
	}
	return out
}

func audioNodeSilenceConfirmedEvidence() mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio.silence",
			Value:  map[string]any{"sessionsFound": float64(0), "sessions": []any{}},
		},
	}
}

// TestEmergencyStopDispatchesToAllThreeTargetKindsConcurrently is this
// build's own three-family acceptance proof: one configured FPP instance,
// one declared audio.node, and one configured Resolume instance each get
// their own stop dispatched, and each outcome carries the RIGHT
// targetKind - never one kind's outcome mislabeled as another's (mutation
// M2's own target).
func TestEmergencyStopDispatchesToAllThreeTargetKindsConcurrently(t *testing.T) {
	now := time.Now()
	resolumeLister := &fakeResolumeLister{views: []ResolumeInstanceView{{InstanceID: "arena-1"}}}
	audioPub := &fakeAudioPublisher{result: audioNodeSilenceConfirmedEvidence()}
	f := newEmergencyStopFixtureWithDeps(t, now, nil, nil, resolumeLister, audioPub, "player-01")
	mustPutAudioNodeDirect(t, f.st, "node-a")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	stopOutcomes := result["stopOutcomes"].([]any)
	if len(stopOutcomes) != 3 {
		t.Fatalf("stopOutcomes has %d entries, want 3 (one fpp, one node, one resolume); got %v", len(stopOutcomes), stopOutcomes)
	}
	byKind := stopOutcomesByTargetKind(t, result)

	fpp, ok := byKind["fpp"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; got %v", "fpp", stopOutcomes)
	}
	if fpp["instanceId"] != "player-01" {
		t.Errorf("fpp entry instanceId = %v, want %q", fpp["instanceId"], "player-01")
	}

	node, ok := byKind["node"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; got %v", "node", stopOutcomes)
	}
	if node["instanceId"] != "node-a" {
		t.Errorf("node entry instanceId = %v, want %q", node["instanceId"], "node-a")
	}
	if node["outcome"] != "confirmed" {
		t.Errorf("node entry outcome = %v, want %q", node["outcome"], "confirmed")
	}

	resolume, ok := byKind["resolume"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; got %v", "resolume", stopOutcomes)
	}
	if resolume["instanceId"] != "arena-1" {
		t.Errorf("resolume entry instanceId = %v, want %q", resolume["instanceId"], "arena-1")
	}
	if resolume["outcome"] != "confirmed" {
		t.Errorf("resolume entry outcome = %v, want %q", resolume["outcome"], "confirmed")
	}
	if audioPub.count() != 1 {
		t.Fatalf("audio.node.silence publishes = %d, want exactly 1", audioPub.count())
	}
	if got := f.resolume.callCount(); got != 1 {
		t.Fatalf("resolume dispatch calls = %d, want exactly 1 (the emergency stop's own blackout - no follow-up is configured in this test)", got)
	}
}

// TestEmergencyStopFailedResolumeBlackoutIsReportedAsAFailedOutcome is
// mutation M1's own target: a Resolume instance whose blackout dispatch
// fails must appear in result.stopOutcomes as a "failed" resolume entry,
// never dropped or silently forced to a success outcome the way a
// follow-up action's own failure is allowed to be (this build's own
// degrade-safely rule applies to follow-ups, deliberately NOT to a target
// kind's own dispatch).
func TestEmergencyStopFailedResolumeBlackoutIsReportedAsAFailedOutcome(t *testing.T) {
	now := time.Now()
	resolumeLister := &fakeResolumeLister{views: []ResolumeInstanceView{{InstanceID: "arena-1"}}}
	f := newEmergencyStopFixtureWithDeps(t, now, nil, nil, resolumeLister, nil, "player-01")
	f.resolume.results["blackout"] = ResolumeActionResult{Outcome: ResolumeOutcomeFailed, Reason: "transport error: connection refused"}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a failed target outcome is reported, never turned into an HTTP error); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	byKind := stopOutcomesByTargetKind(t, result)
	resolume, ok := byKind["resolume"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; got %v", "resolume", result["stopOutcomes"])
	}
	if resolume["outcome"] != "failed" {
		t.Fatalf("resolume entry outcome = %v, want %q; the failed blackout must be reported, not dropped or forced to success", resolume["outcome"], "failed")
	}
	if resolume["outcomeReason"] != "transport error: connection refused" {
		t.Errorf("resolume entry outcomeReason = %v, want the real dispatcher-reported reason", resolume["outcomeReason"])
	}
	if resolume["instanceId"] != "arena-1" {
		t.Errorf("resolume entry instanceId = %v, want %q", resolume["instanceId"], "arena-1")
	}
}

// TestEmergencyStopNodeWithAgentPredatingSilenceIsRefusedWithItsOwnReason
// mirrors TestAudioNodeSilenceDispatchSurfacesAgentRefusalReason
// (audionodesilence_test.go) at the emergency-stop level: an agent build
// that predates audio.node.silence refuses it through its own allowlist,
// and that refusal reason must reach result.stopOutcomes verbatim, never
// flattened into a generic failure or "unconfirmable".
func TestEmergencyStopNodeWithAgentPredatingSilenceIsRefusedWithItsOwnReason(t *testing.T) {
	now := time.Now()
	const agentReason = `operation "audio.node.silence" is not on the agent's allowlist`
	audioPub := &fakeAudioPublisher{result: mqttproto.ResultPayload{Outcome: mqttproto.OutcomeRefused, Reason: agentReason}}
	f := newEmergencyStopFixtureWithDeps(t, now, nil, nil, nil, audioPub, "player-01")
	mustPutAudioNodeDirect(t, f.st, "node-old")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	byKind := stopOutcomesByTargetKind(t, result)
	node, ok := byKind["node"]
	if !ok {
		t.Fatalf("no stopOutcomes entry carries targetKind %q; got %v", "node", result["stopOutcomes"])
	}
	if node["outcome"] != "refused" {
		t.Fatalf("node entry outcome = %v, want %q", node["outcome"], "refused")
	}
	if node["outcomeReason"] != agentReason {
		t.Errorf("node entry outcomeReason = %v, want the agent's own refusal reason %q", node["outcomeReason"], agentReason)
	}
	if node["instanceId"] != "node-old" {
		t.Errorf("node entry instanceId = %v, want %q", node["instanceId"], "node-old")
	}
}

// --- scope gating ---

func TestEmergencyStopRequiresScope(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	for _, path := range []string{
		"/api/v1/emergency-stop/stop", "/api/v1/emergency-stop/stop-power-down",
		"/api/v1/emergency-stop/hard-stop/arm", "/api/v1/emergency-stop/hard-stop/fire",
	} {
		req := newJSONRequest(t, http.MethodPost, path, `{"idempotencyKey":"key-viewer","armToken":"whatever"}`,
			map[string]string{"Authorization": "Bearer " + f.viewerToken})
		resp, body := doRawRequest(t, f.api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as viewer: status = %d, want 403; body: %s", path, resp.StatusCode, body)
		}
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0 (every request above must have been refused before dispatch)", got)
	}
}

// --- level 2: stop-power-down ---

func TestEmergencyStopPowerDownForcesALiveNightSessionImmediately(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	st := f.st
	if _, err := st.CreateConfigObject(context.Background(), config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create night.session config object: %v", err)
	}
	payloadJSON, err := config.EncodeNightSessionPayload(config.NightSessionPayload{
		Show: "halloween-2026", Label: "test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "player-01", Playlist: "halloween-show"},
		Resting:      config.NightSessionResting{FPPInstanceID: "player-01", Playlist: "halloween-resting", EndOfNightPlaylist: "halloween-resting"},
	})
	if err != nil {
		t.Fatalf("encode night.session payload: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create night.session config revision: %v", err)
	}
	rec := store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStateLive, StateEnteredAt: now, Cycle: 1,
	}
	if err := st.CreateNightSession(context.Background(), rec, now); err != nil {
		t.Fatalf("create night session: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	ns, ok := result["nightSession"].(map[string]any)
	if !ok {
		t.Fatalf("result.nightSession missing or wrong shape: %v", result["nightSession"])
	}
	if present, _ := ns["present"].(bool); !present {
		t.Fatalf("nightSession.present = %v, want true (a night session WAS active)", ns["present"])
	}

	updated, _, err := st.GetCurrentNightSession(context.Background())
	if err != nil {
		t.Fatalf("get current night session: %v", err)
	}
	if updated.State != nightStateFadingOut {
		t.Fatalf("night session state = %q, want %q: an emergency stop must force a LIVE session into fading-out immediately, never defer it the way an ordinary power-down-presentation would", updated.State, nightStateFadingOut)
	}
	if updated.FinalShowRequested {
		t.Error("night session has FinalShowRequested set, which is the ORDINARY deferring path's own effect. Force must bypass it entirely")
	}
}

func TestEmergencyStopPowerDownWithNoActiveNightSessionReportsNotPresent(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	ns := result["nightSession"].(map[string]any)
	if present, _ := ns["present"].(bool); present {
		t.Fatalf("nightSession.present = %v, want false: a real, valid, non-degraded outcome when no session is active", ns["present"])
	}
}

// --- level 3: hard-stop arm/fire ---

func TestEmergencyStopHardStopArmThenFireSucceeds(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	armResp, armBody := doRawRequest(t, f.api.Handler, armReq)
	if armResp.StatusCode != http.StatusOK {
		t.Fatalf("arm: status = %d, want 200; body: %s", armResp.StatusCode, armBody)
	}
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)
	if armToken == "" {
		t.Fatal("arm response carries no armToken")
	}

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	fireResp, fireBody := doRawRequest(t, f.api.Handler, fireReq)
	if fireResp.StatusCode != http.StatusOK {
		t.Fatalf("fire: status = %d, want 200; body: %s", fireResp.StatusCode, fireBody)
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1", got)
	}
}

func TestEmergencyStopHardStopFireWithoutArmIsRefused(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"never-armed"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	fireResp, fireBody := doRawRequest(t, f.api.Handler, fireReq)
	if fireResp.StatusCode != http.StatusConflict {
		t.Fatalf("fire without arm: status = %d, want 409; body: %s", fireResp.StatusCode, fireBody)
	}
	m := decodeMap(t, fireBody)
	if typ, _ := m["type"].(string); typ != ProblemTypeEmergencyStopHardStopNotArmed {
		t.Errorf("problem type = %q, want %q", typ, ProblemTypeEmergencyStopHardStopNotArmed)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0 (an unarmed fire must dispatch nothing)", got)
	}
}

// THE core anti-double-fire property: firing the SAME token twice, the
// exact shape of a redelivered command or an accidental retry, must
// dispatch the underlying stop AT MOST ONCE.
func TestEmergencyStopHardStopFiringTheSameTokenTwiceNeverDoubleFires(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	fireBody := `{"idempotencyKey":"fire-1","armToken":"` + armToken + `"}`
	fireReq1 := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire", fireBody,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp1, body1 := doRawRequest(t, f.api.Handler, fireReq1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first fire: status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	// A second fire presenting the IDENTICAL token, as a redelivered
	// command or a manual retry would.
	fireReq2 := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire", fireBody,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp2, body2 := doRawRequest(t, f.api.Handler, fireReq2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second fire (same token): status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	m := decodeMap(t, body2)
	if typ, _ := m["type"].(string); typ != ProblemTypeConflict {
		t.Errorf("second fire problem type = %q, want %q (the compare-and-swap-race class, distinct from not-armed)", typ, ProblemTypeConflict)
	}

	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1: firing the same token twice must never dispatch the stop twice", got)
	}
}

func TestEmergencyStopHardStopArmingAgainInvalidatesThePreviousArmForFire(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq1 := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody1 := doRawRequest(t, f.api.Handler, armReq1)
	firstToken, _ := decodeMap(t, armBody1)["armToken"].(string)

	armReq2 := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-2"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	doRawRequest(t, f.api.Handler, armReq2)

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+firstToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, fireReq)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("fire with the SUPERSEDED first token: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0", got)
	}
}

// TestPutEmergencyStopConfigRevisionPreconditionWiring is a smoke test
// proving handlePutEmergencyStopConfig actually threads the shared
// revision precondition through to its own call site. The full
// behavioural matrix lives once, on the representative kind
// fpp.endpoints (config_test.go).
func TestPutEmergencyStopConfigRevisionPreconditionWiring(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)

	put := func(body string, headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + f.adminToken}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop", body, h)
		return doRawRequest(t, f.api.Handler, req)
	}

	if resp, body := put(emergencyStopEmptyLevelsBody(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional write: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := put(emergencyStopWorklightsOnStopBody(), map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	resp, body := put(emergencyStopEmptyLevelsBody(), map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.emergencystop", map[string]string{"Authorization": "Bearer " + f.adminToken})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d; body: %s", getResp.StatusCode, getBody)
	}
	if !containsAll(string(getBody), `"worklights-on"`) {
		t.Errorf("the matching-If-Match writer's action should have survived the refused stale write; body: %s", getBody)
	}
}
