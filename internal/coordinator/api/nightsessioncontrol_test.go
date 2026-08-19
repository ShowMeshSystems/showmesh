package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Test suite for the night-session lifecycle controller
// (nightsessioncontrol.go), driven through the real route table against a
// real *store.Store, per nightsession_test.go's established pattern.

func nightControlTestDeps(svc identity.Service, st *store.Store) (Dependencies, *fakeObservationLister) {
	deps := nightSessionTestDeps(svc, st)
	obs := &fakeObservationLister{}
	deps.Observations = obs
	deps.NightSessions = st
	return deps, obs
}

func mustActivateNightSession(t *testing.T, api *API, token, id string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"`+id+`"}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT night.session.active: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

// setupNightControlFixture returns a fully wired API with an active
// night.session ("halloween-main", referencing FPP instance "player-01"),
// an admin token, an operator token (night:command), a mutable clock
// advance function, and the fake observation lister run-readiness reads
// from.
func setupNightControlFixture(t *testing.T, maxAge time.Duration) (api *API, st *store.Store, operatorToken string, advance func(time.Duration), obs *fakeObservationLister) {
	t.Helper()
	advanceFn, now := mutableClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obsLister := nightControlTestDeps(svc, st)
	// Track F seam F3: run-readiness now also reads a real FSEQ asset
	// (rule 1) and reads the resting/show playlist definitions off a real
	// FPP endpoint (F0 §2) — see nightWireFPPForReadiness's own doc
	// comment. t.Cleanup closes the fake FPP server after the test.
	deps.FPP = nightWireFPPForReadiness(t)
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend

	api = New(deps, Options{Clock: now, Logger: testLogger(), NightReadinessMaxAge: maxAge})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	return api, st, opToken, advanceFn, obsLister
}

// nightWireFPPForReadiness starts a fake FPP HTTP server answering
// GET /api/playlist/halloween-resting and GET /api/playlist/halloween-show
// with F0 §2's own captured idle-read shape (one FSEQ-only item), and
// returns a [FPPLister] naming it as instance "player-01" — matching
// validNightSessionBody's own fppInstanceId. Without this, every seam F3
// readiness check that reads a playlist definition reports "unknown" for
// want of a configured instance, which is honest but makes every test
// below expecting readiness outcome "ready" fail for a reason unrelated to
// what it is testing.
func nightWireFPPForReadiness(t *testing.T) FPPLister {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/playlist/halloween-resting", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"halloween-resting","mainPlaylist":[{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"resting-loop.fseq"}]}`))
	})
	mux.HandleFunc("/api/playlist/halloween-show", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"halloween-show","mainPlaylist":[{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"halloween-show.fseq"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}}
}

// nightTestAssetBackend builds a real, disposable assetstore.VolumeBackend
// for the FSEQ duration checks (rule 1) to open bytes from — these tests
// verify the readiness surface's actual behavior against real FSEQ
// content, not a stub that never runs pkg/fseq's own parsing.
func nightTestAssetBackend(t *testing.T) assetstore.Backend {
	t.Helper()
	backend, err := assetstore.NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewVolumeBackend: %v", err)
	}
	return backend
}

// mustCreateNightSessionFSEQAsset is [mustCreateNightSessionAsset]
// (nightsession_test.go) with real, openable FSEQ bytes staged into
// backend — 6000 frames * 50ms = 300000ms, F0 §1's own worked example —
// rather than that helper's placeholder ContentHash/StorageKey, which
// name no actual blob.
func mustCreateNightSessionFSEQAsset(t *testing.T, st *store.Store, backend assetstore.Backend, show, sequence, target string) {
	t.Helper()
	raw := fseqtest.Build(4, 6000, 50)
	blob, err := backend.Put(context.Background(), bytes.NewReader(raw), int64(len(raw))+1)
	if err != nil {
		t.Fatalf("Put fseq bytes: %v", err)
	}
	_, _, err = st.CreateAsset(context.Background(), store.AssetRecord{
		ID: show + "-" + sequence + "-" + target, ShowID: show, SequenceID: sequence,
		TargetKind: store.AssetTargetKindNode, TargetID: target,
		MediaType: "fseq", ContentHash: blob.ContentHash, RuntimeFilename: sequence + ".fseq",
		SizeBytes: blob.SizeBytes, Backend: "volume", StorageKey: blob.ContentHash,
	})
	if err != nil {
		t.Fatalf("create asset %s/%s/%s: %v", show, sequence, target, err)
	}
}

func nightCommandRawBody(t *testing.T, api *API, token, command, body string) (*http.Response, []byte) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/"+command, body, map[string]string{"Authorization": "Bearer " + token})
	return doRawRequest(t, api.Handler, req)
}

func nightCommandRaw(t *testing.T, api *API, token, command string) (*http.Response, []byte) {
	t.Helper()
	return nightCommandRawBody(t, api, token, command, `{}`)
}

// mustNightCommand requires 202 and returns the decoded response.
func mustNightCommand(t *testing.T, api *API, token, command string) v1.NightCommandResponse {
	t.Helper()
	resp, body := nightCommandRaw(t, api, token, command)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST night/commands/%s: status = %d, want 202; body: %s", command, resp.StatusCode, body)
	}
	var out v1.NightCommandResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode night command response: %v; body: %s", err, body)
	}
	return out
}

func mustNightCommandWithKey(t *testing.T, api *API, token, command, idempotencyKey string) v1.NightCommandResponse {
	t.Helper()
	resp, body := nightCommandRawBody(t, api, token, command, `{"idempotencyKey":"`+idempotencyKey+`"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST night/commands/%s: status = %d, want 202; body: %s", command, resp.StatusCode, body)
	}
	var out v1.NightCommandResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode night command response: %v; body: %s", err, body)
	}
	return out
}

// nightCommandProblem requires a non-2xx response and returns its decoded
// problem.
func nightCommandProblem(t *testing.T, api *API, token, command string) (int, v1.Problem) {
	t.Helper()
	resp, body := nightCommandRaw(t, api, token, command)
	if resp.StatusCode < 400 {
		t.Fatalf("POST night/commands/%s: status = %d, want an error; body: %s", command, resp.StatusCode, body)
	}
	var p v1.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v; body: %s", err, body)
	}
	return resp.StatusCode, p
}

func mustGetNightSession(t *testing.T, api *API) v1.NightSessionResponse {
	t.Helper()
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/night/session", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET night/session: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var out v1.NightSessionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode night session response: %v; body: %s", err, body)
	}
	return out
}

// setHealthyFPPReachable seeds obs with a single current, healthy
// fpp.reachable observation for "player-01" (fakeObservationLister
// ignores the filter and returns this slice verbatim).
func setHealthyFPPReachable(obs *fakeObservationLister, now time.Time) {
	observedAt := now
	obs.obs = []observation.Observation{{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
		Signal:   "fpp.reachable", Value: true, ObservedAt: &observedAt, CollectedAt: now,
		Source: "fpp-rest", Quality: observation.QualityDirect, ValidFor: time.Minute,
	}}
}

// runToPreshow drives prepare-site -> run-readiness -> start-preshow, the
// common prefix most tests below need, and returns the session id.
func runToPreshow(t *testing.T, api *API, token string, obs *fakeObservationLister, now time.Time) string {
	t.Helper()
	setHealthyFPPReachable(obs, now)
	prep := mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-preshow")
	return prep.Session.ID
}

// --- invariant 1: shutdown intent is monotonic ---

func TestInvariant1_FadeOutThenPowerDown_NeverDowngrades(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	out := mustNightCommand(t, api, token, "fade-out-night")
	if out.Session.ShutdownIntent != "fade-out" {
		t.Fatalf("shutdownIntent after fade-out-night = %q, want fade-out", out.Session.ShutdownIntent)
	}
	out = mustNightCommand(t, api, token, "power-down-presentation")
	if out.Session.ShutdownIntent != "power-down" {
		t.Fatalf("shutdownIntent after power-down-presentation = %q, want power-down (upgrade, never a downgrade)", out.Session.ShutdownIntent)
	}
	// The power phase resolves when playback has been observed stopped,
	// not at the moment the command is accepted.
	if out.Session.PowerPhase.State != v1.NightEvidenceUnknown {
		t.Fatalf("powerPhase.state while still fading out = %q, want unknown", out.Session.PowerPhase.State)
	}
	if !strings.Contains(out.Session.PowerPhase.Reason, "deferred") {
		t.Fatalf("powerPhase.reason = %q, want it to say the phase is deferred", out.Session.PowerPhase.Reason)
	}
}

func TestInvariant1_PowerDownThenFadeOut_ReversedOrderNeverDowngrades(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	out := mustNightCommand(t, api, token, "power-down-presentation")
	if out.Session.State != nightStateFadingOut || out.Session.ShutdownIntent != "power-down" {
		t.Fatalf("after power-down-presentation from preshow with no power config: state=%q shutdownIntent=%q, want fading-out/power-down", out.Session.State, out.Session.ShutdownIntent)
	}

	out2 := mustNightCommand(t, api, token, "fade-out-night")
	if out2.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("fade-out-night after power-down entered the fade path: outcome = %q, want idempotent_no_op", out2.Command.Outcome)
	}
	if out2.Session.ShutdownIntent != "power-down" || out2.Session.State != nightStateFadingOut {
		t.Fatalf("fade-out-night after fading-out changed state: %+v", out2.Session)
	}
}

func TestInvariant1_RequestFinalShowCannotReopenAdmission(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	fadeOut := mustNightCommand(t, api, token, "fade-out-night")
	if !fadeOut.Session.AdmissionClosed {
		t.Fatalf("admission not closed after fade-out-night")
	}
	if fadeOut.Session.State != nightStateFadingOut {
		t.Fatalf("fade-out-night from preshow (uncommitted) = %q, want fading-out", fadeOut.Session.State)
	}

	final := mustNightCommand(t, api, token, "request-final-show")
	if !final.Session.AdmissionClosed {
		t.Fatalf("admission reopened by a late request-final-show")
	}
	if final.Session.State != nightStateFadingOut {
		t.Fatalf("request-final-show after fade-out-night changed state to %q, want it to stay fading-out", final.Session.State)
	}
}

func TestInvariant1_Duplicate(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	first := mustNightCommand(t, api, token, "fade-out-night")
	second := mustNightCommand(t, api, token, "fade-out-night")
	if second.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("second fade-out-night outcome = %q, want idempotent_no_op", second.Command.Outcome)
	}
	firstAt, err := time.Parse(time.RFC3339Nano, *first.Session.AdmissionClosedAt)
	if err != nil {
		t.Fatalf("parse first admissionClosedAt: %v", err)
	}
	secondAt, err := time.Parse(time.RFC3339Nano, *second.Session.AdmissionClosedAt)
	if err != nil {
		t.Fatalf("parse second admissionClosedAt: %v", err)
	}
	if !firstAt.Equal(secondAt) {
		t.Fatalf("admissionClosedAt changed on a duplicate: %s -> %s", firstAt, secondAt)
	}
}

// --- invariant 2: an epoch is consumed, not reused ---

func TestInvariant2_StartNightRejectsWithoutReadiness(t *testing.T) {
	api, _, token, _, _ := setupNightControlFixture(t, time.Hour)
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "start-preshow")

	status, p := nightCommandProblem(t, api, token, "start-night")
	if status != http.StatusConflict || p.Type != ProblemTypeNightNotReady {
		t.Fatalf("start-night with no readiness: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightNotReady)
	}
}

func TestInvariant2_StartNightRejectsStaleReadiness(t *testing.T) {
	api, _, token, advance, obs := setupNightControlFixture(t, time.Minute)
	runToPreshow(t, api, token, obs, testNow)
	advance(2 * time.Minute)

	status, p := nightCommandProblem(t, api, token, "start-night")
	if status != http.StatusConflict || p.Type != ProblemTypeNightNotReady {
		t.Fatalf("start-night with stale readiness: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightNotReady)
	}
}

func TestInvariant2_ReadinessFromPriorEpochNeverAdopted(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "power-down-presentation")
	// The fade path holds the session until FPP is observed stopped, so
	// the operator's documented recovery is end-session, then prepare-site.
	mustNightCommand(t, api, token, "end-session")

	second := mustNightCommand(t, api, token, "prepare-site")
	if second.Command.Outcome != "applied" {
		t.Fatalf("second prepare-site outcome = %q, want applied (a fresh epoch)", second.Command.Outcome)
	}
	mustNightCommand(t, api, token, "start-preshow")

	status, p := nightCommandProblem(t, api, token, "start-night")
	if status != http.StatusConflict || p.Type != ProblemTypeNightNotReady {
		t.Fatalf("start-night reusing a prior epoch's readiness: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightNotReady)
	}
}

func TestInvariant2_DelayedStartAfterShutdownIsRejected(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "power-down-presentation")

	if status, p := nightCommandProblem(t, api, token, "start-preshow"); status != http.StatusConflict || p.Type != ProblemTypeNightStateRejected {
		t.Fatalf("delayed start-preshow after shutdown: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightStateRejected)
	}
	if status, p := nightCommandProblem(t, api, token, "start-night"); status != http.StatusConflict || p.Type != ProblemTypeNightStateRejected {
		t.Fatalf("delayed start-night after shutdown: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightStateRejected)
	}
}

// TestInvariant2_ConfigRevisionStaysPinnedAcrossAnEditMidSession is the
// spec's own named test: writing a new night.session revision mid-session
// must not change what the running session reports.
func TestInvariant2_ConfigRevisionStaysPinnedAcrossAnEditMidSession(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")
	setHealthyFPPReachable(obs, testNow)

	prep := mustNightCommand(t, api, opToken, "prepare-site")
	if prep.Session.ConfigRevision != 1 {
		t.Fatalf("configRevision after prepare-site = %d, want 1", prep.Session.ConfigRevision)
	}

	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody) // -> revision 2

	got := mustGetNightSession(t, api)
	if got.Session.ConfigRevision != 1 {
		t.Fatalf("configRevision after an unrelated mid-session edit = %d, want the PINNED revision 1, not the newly-written 2", got.Session.ConfigRevision)
	}
}

// --- invariant 3: a duplicate is an idempotent no-op ---

func TestInvariant3_PrepareSiteDuplicateDoesNotManufactureASecondEpoch(t *testing.T) {
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)
	first := mustNightCommand(t, api, token, "prepare-site")
	second := mustNightCommand(t, api, token, "prepare-site")

	if second.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("second prepare-site outcome = %q, want idempotent_no_op", second.Command.Outcome)
	}
	if first.Session.ID != second.Session.ID {
		t.Fatalf("duplicate prepare-site minted a second session/epoch: %s != %s", first.Session.ID, second.Session.ID)
	}
	if _, err := st.GetNightSession(context.Background(), first.Session.ID); err != nil {
		t.Fatalf("get session: %v", err)
	}
}

func TestInvariant3_StartNightDuplicateDoesNotReArmOrIncrementCycle(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")

	first := mustNightCommand(t, api, token, "start-night")
	if first.Session.State != "transition-to-show" || first.Command.Outcome != "applied" {
		t.Fatalf("first start-night: state=%q outcome=%q, want transition-to-show/applied", first.Session.State, first.Command.Outcome)
	}
	second := mustNightCommand(t, api, token, "start-night")
	if second.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("second start-night outcome = %q, want idempotent_no_op", second.Command.Outcome)
	}
	if second.Session.ArmedShowID != first.Session.ArmedShowID {
		t.Fatalf("duplicate start-night re-armed a new show: %s != %s", first.Session.ArmedShowID, second.Session.ArmedShowID)
	}
	if second.Session.Cycle != first.Session.Cycle {
		t.Fatalf("duplicate start-night incremented cycle: %d != %d", first.Session.Cycle, second.Session.Cycle)
	}
}

// --- invariant 4: an ambiguous restart never launches a show by guess ---

func TestInvariant4_ReconcileMarksMidFlightSessionDegraded(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-night")

	if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := mustGetNightSession(t, api)
	if !got.Session.Degraded {
		t.Fatalf("session left in transition-to-show was not marked degraded on reconcile")
	}
	if got.Session.DegradedReason == "" {
		t.Fatalf("degraded session carries no reason")
	}

	status, p := nightCommandProblem(t, api, token, "start-preshow")
	if status != http.StatusConflict || p.Type != ProblemTypeNightAmbiguous {
		t.Fatalf("command against a degraded session: status=%d type=%q, want 409/%s", status, p.Type, ProblemTypeNightAmbiguous)
	}
}

func TestInvariant4_ReconcileLeavesSafeStatesAlone(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := mustGetNightSession(t, api)
	if got.Session.Degraded {
		t.Fatalf("reconcile degraded a session sitting in a safe state (preshow)")
	}
}

// --- finding 1: the degraded gate exempts three commands, and end-session ---

func TestFinding1_DegradedSessionRefusesTheFourGatedCommands(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-night")
	if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, cmd := range []string{"prepare-site", "run-readiness", "start-preshow", "start-night"} {
		status, p := nightCommandProblem(t, api, token, cmd)
		if status != http.StatusConflict || p.Type != ProblemTypeNightAmbiguous {
			t.Errorf("%s against a degraded session: status=%d type=%q, want 409/%s", cmd, status, p.Type, ProblemTypeNightAmbiguous)
		}
	}
}

// TestFinding1_DegradedSessionStillAcceptsTheThreeExemptCommands is
// finding 1's own core claim: a refusal here would be strictly worse than
// no coordinator at all.
func TestFinding1_DegradedSessionStillAcceptsTheThreeExemptCommands(t *testing.T) {
	for _, cmd := range []string{"fade-out-night", "power-down-presentation", "request-final-show"} {
		t.Run(cmd, func(t *testing.T) {
			api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
			runToPreshow(t, api, token, obs, testNow)
			mustNightCommand(t, api, token, "run-readiness")
			mustNightCommand(t, api, token, "start-night")
			if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			resp, body := nightCommandRaw(t, api, token, cmd)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("%s against a degraded session: status = %d, want 202 (exempt); body: %s", cmd, resp.StatusCode, body)
			}
		})
	}
}

func TestFinding1_EndSessionReachableFromDegradedSession(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-night")
	if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	out := mustNightCommand(t, api, token, "end-session")
	if out.Session.State != "stopped" {
		t.Fatalf("end-session from a degraded session: state = %q, want stopped", out.Session.State)
	}
	if !out.Session.Degraded {
		t.Fatalf("end-session cleared Degraded; it must stay true (historical record, not a resume)")
	}
}

// End-session must also be reachable from fading-out, where a session sits
// while its stop is unconfirmed.
func TestFinding1_EndSessionReachableFromFadingOut(t *testing.T) {
	_, st, token, api := newSeededNightFixture(t, "fading-out")
	out := mustNightCommand(t, api, token, "end-session")
	if out.Session.State != "stopped" {
		t.Fatalf("end-session from fading-out: state = %q, want stopped", out.Session.State)
	}
	_ = st
}

// TestFinding1_EndSessionThenPrepareSiteRecovers is the whole point:
// end-session, then an ordinary prepare-site, must succeed even though
// the ended session's own Degraded flag stays true forever.
func TestFinding1_EndSessionThenPrepareSiteRecovers(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-night")
	if err := ReconcileNightSessionOnStartup(context.Background(), Dependencies{NightSessions: st}, func() time.Time { return testNow }, testLogger()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	mustNightCommand(t, api, token, "end-session")

	next := mustNightCommand(t, api, token, "prepare-site")
	if next.Command.Outcome != "applied" {
		t.Fatalf("prepare-site after end-session: outcome = %q, want applied (a fresh epoch)", next.Command.Outcome)
	}
	if next.Session.Degraded {
		t.Fatalf("the NEW session created by prepare-site reports Degraded=true; it must be a fresh, undegraded session")
	}
}

// newSeededNightFixture opens a store, creates a night.session config and
// activates it, and inserts a night_sessions row directly in the given
// state — for states no command in this seam reaches (fading-out, live,
// committed transition-to-show).
func newSeededNightFixture(t *testing.T, state string) (*store.Store, *store.Store, string, *API) {
	t.Helper()
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)
	rec := store.NightSessionRecord{ID: "seeded-1", ConfigObjectID: "halloween-main", ConfigRevision: 1, State: state, StateEnteredAt: testNow}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("seed night session in state %q: %v", state, err)
	}
	return st, st, token, api
}

// --- finding 2: read-decide-write races ---

func TestFinding2_ConcurrentStartNightAndFadeOutFromPreshow_AdmissionAlwaysClosed(t *testing.T) {
	const iterations = 20
	for i := 0; i < iterations; i++ {
		api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
		runToPreshow(t, api, token, obs, testNow)
		mustNightCommand(t, api, token, "run-readiness")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); nightCommandRaw(t, api, token, "start-night") }()
		go func() { defer wg.Done(); nightCommandRaw(t, api, token, "fade-out-night") }()
		wg.Wait()

		got := mustGetNightSession(t, api)
		if !got.Session.AdmissionClosed {
			t.Fatalf("iteration %d: admission is OPEN after a concurrent start-night/fade-out-night pair from preshow; fade-out-night was submitted and must never be lost", i)
		}
	}
}

func TestFinding2_ConcurrentPrepareSite_NeverCreatesTwoSessions(t *testing.T) {
	const iterations = 20
	for i := 0; i < iterations; i++ {
		api, st, token, _, _ := setupNightControlFixture(t, time.Hour)

		var wg sync.WaitGroup
		results := make([]v1.NightCommandResponse, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			results[0] = mustNightCommand(t, api, token, "prepare-site")
		}()
		go func() {
			defer wg.Done()
			results[1] = mustNightCommand(t, api, token, "prepare-site")
		}()
		wg.Wait()

		if results[0].Session.ID != results[1].Session.ID {
			t.Fatalf("iteration %d: concurrent prepare-site produced two different sessions: %s != %s", i, results[0].Session.ID, results[1].Session.ID)
		}
		// Belt and suspenders: GetCurrentNightSession must also see
		// exactly the ONE session both responses agreed on, not a THIRD
		// row a race could have left behind unreturned to either caller.
		current, ok, err := st.GetCurrentNightSession(context.Background())
		if err != nil || !ok {
			t.Fatalf("iteration %d: get current night session: ok=%v err=%v", i, ok, err)
		}
		if current.ID != results[0].Session.ID {
			t.Fatalf("iteration %d: current session %s does not match either concurrent response (%s)", i, current.ID, results[0].Session.ID)
		}
	}
}

// --- finding 5: readiness rendering distinguishes not-found, a store
// error, and a corrupt checks payload ---

// nightSessionStoreWithReadinessErr wraps a real *store.Store, overriding
// only GetLatestNightReadiness to return an arbitrary, non-not-found
// error — used to prove [mapNightReadiness] never reports a store failure
// as "no readiness result recorded" (finding 5).
type nightSessionStoreWithReadinessErr struct {
	*store.Store
	err error
}

func (n *nightSessionStoreWithReadinessErr) GetLatestNightReadiness(context.Context, string) (store.NightReadinessRecord, error) {
	return store.NightReadinessRecord{}, n.err
}

func TestFinding5_StoreErrorReadingReadinessIsNotReportedAsNotFound(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	injectedErr := errors.New("injected: disk full")
	deps, _ := nightControlTestDeps(nil, st)
	deps.NightSessions = &nightSessionStoreWithReadinessErr{Store: st, err: injectedErr}
	brokenAPI := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	got := mustGetNightSession(t, brokenAPI)
	if got.Session.Readiness.State != v1.NightEvidenceUnknown {
		t.Fatalf("readiness.state with a store error = %q, want unknown", got.Session.Readiness.State)
	}
	if strings.Contains(got.Session.Readiness.Reason, "no readiness result recorded") {
		t.Fatalf("readiness.reason on a STORE ERROR claims \"no readiness result recorded\", which was never established; reason: %q", got.Session.Readiness.Reason)
	}
	if !strings.Contains(got.Session.Readiness.Reason, injectedErr.Error()) {
		t.Fatalf("readiness.reason = %q, want it to name the actual store error", got.Session.Readiness.Reason)
	}
}

func TestFinding5_CorruptChecksJSONStatesTheDecodeFailure(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	sessionID := runToPreshow(t, api, token, obs, testNow)
	first := mustNightCommand(t, api, token, "run-readiness")
	_ = first

	// A LATER readiness row with an invalid checks_json payload, inserted
	// directly (no store method can be asked to produce invalid JSON) so
	// it becomes the latest result [mapNightReadiness] reads.
	if err := st.CreateNightReadiness(context.Background(), store.NightReadinessRecord{
		ID: "corrupt-1", SessionID: sessionID, EpochID: sessionID,
		CompletedAt: testNow.Add(time.Second), Outcome: "ready", ChecksJSON: "not-json",
	}); err != nil {
		t.Fatalf("seed corrupt readiness: %v", err)
	}

	got := mustGetNightSession(t, api)
	if got.Session.Readiness.State != v1.NightEvidenceRecorded {
		t.Fatalf("readiness.state with a corrupt checks payload = %q, want recorded (the result itself IS recorded)", got.Session.Readiness.State)
	}
	if len(got.Session.Readiness.Checks) != 0 {
		t.Fatalf("readiness.checks with a corrupt payload = %v, want empty", got.Session.Readiness.Checks)
	}
	if got.Session.Readiness.Reason == "" || !strings.Contains(got.Session.Readiness.Reason, "decoded") {
		t.Fatalf("readiness.reason with a corrupt checks payload = %q, want it to state the decode failure rather than silently rendering zero checks as fine", got.Session.Readiness.Reason)
	}
}

// --- finding 6: powerPhase.reason must not contradict shutdownIntent ---

func TestFinding6_DeferredPowerDownStatesItWasRequested(t *testing.T) {
	st, _, token, api := newSeededNightFixture(t, "live")
	// power-down-presentation on a LIVE session defers (applyNightShutdownEffect).
	out := mustNightCommand(t, api, token, "power-down-presentation")
	if out.Session.State != "live" {
		t.Fatalf("power-down-presentation while live: state = %q, want live (deferred, not cancelled)", out.Session.State)
	}
	if out.Session.ShutdownIntent != "power-down" {
		t.Fatalf("shutdownIntent after a deferred power-down = %q, want power-down", out.Session.ShutdownIntent)
	}
	if strings.Contains(out.Session.PowerPhase.Reason, "has not been requested") {
		t.Fatalf("powerPhase.reason = %q, contradicts shutdownIntent=power-down: it WAS requested, just deferred", out.Session.PowerPhase.Reason)
	}
	if !strings.Contains(out.Session.PowerPhase.Reason, "deferred") && !strings.Contains(out.Session.PowerPhase.Reason, "pending") {
		t.Fatalf("powerPhase.reason = %q, want it to say the request is pending/deferred", out.Session.PowerPhase.Reason)
	}
	_ = st
}

// --- finding 7: idempotencyKey is honored, narrowly, by prepare-site ---

func TestFinding7_PrepareSiteIdempotencyKeyReplaysOriginalSession(t *testing.T) {
	api, _, token, _, _ := setupNightControlFixture(t, time.Hour)
	first := mustNightCommandWithKey(t, api, token, "prepare-site", "retry-key-1")

	mustNightCommand(t, api, token, "end-session") // -> stopped, so state-based idempotency no longer applies

	replay := mustNightCommandWithKey(t, api, token, "prepare-site", "retry-key-1")
	if replay.Command.Outcome != "idempotent_no_op" {
		t.Fatalf("prepare-site replay outcome = %q, want idempotent_no_op", replay.Command.Outcome)
	}
	if replay.Session.ID != first.Session.ID {
		t.Fatalf("prepare-site replay minted a NEW session (%s) instead of returning the original (%s)", replay.Session.ID, first.Session.ID)
	}
}

func TestFinding7_PrepareSiteWithoutIdempotencyKeyIsUnaffected(t *testing.T) {
	api, _, token, _, _ := setupNightControlFixture(t, time.Hour)
	first := mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "end-session")
	second := mustNightCommand(t, api, token, "prepare-site")
	if second.Session.ID == first.Session.ID {
		t.Fatalf("prepare-site with NO idempotency key returned the OLD session after end-session; a fresh epoch was expected")
	}
}

// --- finding 8: a committed transition-to-show defers, never cancels ---

func TestFinding8_CommittedTransitionToShowDefersRatherThanCancels(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)
	mustNightCommand(t, api, token, "run-readiness")
	started := mustNightCommand(t, api, token, "start-night")
	if started.Session.State != "transition-to-show" {
		t.Fatalf("start-night: state = %q, want transition-to-show", started.Session.State)
	}

	// Set ShowCommitted directly (Track F seam F4's own flag; nothing in
	// this seam ever sets it, which is exactly why this branch is
	// otherwise unreachable today).
	rec, err := st.GetNightSession(context.Background(), started.Session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	rec.ShowCommitted = true
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("set ShowCommitted: %v", err)
	}

	out := mustNightCommand(t, api, token, "fade-out-night")
	if out.Session.State != "transition-to-show" {
		t.Fatalf("fade-out-night against a COMMITTED transition-to-show: state = %q, want transition-to-show (deferred, never cancelled)", out.Session.State)
	}
	if !out.Session.FinalShowRequested {
		t.Fatalf("fade-out-night against a committed transition: finalShowRequested = false, want true (the show becomes final)")
	}
	if out.Session.ArmedShowID == "" {
		t.Fatalf("fade-out-night against a committed transition cleared ArmedShowID; a committed show must not be cancelled")
	}
}

// --- finding 9: fail-closed for the four admission-opening commands ---

func TestFinding9_PrepareSiteRefusedWhenAuditWriteFails(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, _ := nightControlTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")

	installFailAuditTrigger(t, storeDir)

	status, p := nightCommandProblem(t, api, opToken, "prepare-site")
	if status != http.StatusServiceUnavailable || p.Type != ProblemTypeNightAuditUnavailable {
		t.Fatalf("prepare-site with a failing audit write: status=%d type=%q, want 503/%s", status, p.Type, ProblemTypeNightAuditUnavailable)
	}
	_, ok, err := st.GetCurrentNightSession(context.Background())
	if err != nil {
		t.Fatalf("get current night session: %v", err)
	}
	if ok {
		t.Fatalf("prepare-site refused for want of audit still created a session; the whole transaction must have rolled back")
	}
}

func TestFinding9_AttributionDegradedIsPopulatedOnTheExemptPath(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")
	runToPreshow(t, api, opToken, obs, testNow)

	installFailAuditTrigger(t, storeDir)

	out := mustNightCommand(t, api, opToken, "fade-out-night")
	if out.Session.State != nightStateFadingOut {
		t.Fatalf("fade-out-night with a failing audit write: state = %q, want fading-out (never refused)", out.Session.State)
	}
	if !out.Command.AttributionDegraded {
		t.Fatalf("command.attributionDegraded = false, want true (the audit write failed and the field exists precisely to say so)")
	}
	if !out.Session.AttributionDegraded {
		t.Fatalf("session.attributionDegraded = false, want true")
	}
}

// TestInvariant8Both proves invariant 8 for BOTH exempt commands the spec
// names, not only fade-out-night.
func TestInvariant8Both(t *testing.T) {
	for _, cmd := range []string{"fade-out-night", "power-down-presentation"} {
		t.Run(cmd, func(t *testing.T) {
			svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
			admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
			adminToken := mustIssueToken(t, svc, admin.ID)
			operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
			opToken := mustIssueToken(t, svc, operator.ID)

			deps, obs := nightControlTestDeps(svc, st)
			api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

			mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
			mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
			mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
			mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
			mustActivateNightSession(t, api, adminToken, "halloween-main")
			runToPreshow(t, api, opToken, obs, testNow)

			installFailAuditTrigger(t, storeDir)

			out := mustNightCommand(t, api, opToken, cmd)
			if out.Session.State != nightStateFadingOut {
				t.Fatalf("%s with a failing audit write: state = %q, want fading-out (never refused)", cmd, out.Session.State)
			}
		})
	}
}

// --- invariant 5: missing evidence is unknown ---

func TestInvariant5_ReadinessWithNoEvidenceIsUnknownNotFailure(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	obs.obs = nil
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	got := mustGetNightSession(t, api)
	if got.Session.Readiness.Outcome != "unknown" {
		t.Fatalf("readiness outcome with no evidence = %q, want unknown (never ready, never not_ready)", got.Session.Readiness.Outcome)
	}
}

// resting:asset-exact-variant is
// structurally unverifiable (FPP exposes no content hash) and is excluded
// from the aggregate outcome for exactly that reason (ADR-029 decision
// 4's rule: an indicator that can never read anything but one colour
// teaches the operator to ignore it) — it stays listed in the checks
// array, but never prevents "ready". This test proves both halves: ready
// is reachable with every checkable check passing, and (invariant 5,
// still true either way) that readiness never blocks start-night by
// itself regardless.
func TestInvariant5_ReadinessWithHealthyEvidenceIsReady(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	setHealthyFPPReachable(obs, testNow)
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	got := mustGetNightSession(t, api)
	if got.Session.Readiness.Outcome != "ready" {
		t.Fatalf("readiness outcome with healthy evidence and every checkable check passing = %q, want ready (resting:asset-exact-variant is not_verifiable and must not hold this back)", got.Session.Readiness.Outcome)
	}
	foundNotVerifiable := false
	for _, c := range got.Session.Readiness.Checks {
		if c.Name == "resting:asset-exact-variant:"+"halloween-resting" {
			foundNotVerifiable = true
			if c.State != "not_verifiable" {
				t.Fatalf("resting:asset-exact-variant state = %q, want not_verifiable", c.State)
			}
		}
	}
	if !foundNotVerifiable {
		t.Fatal("expected resting:asset-exact-variant to still be listed even though it never affects outcome")
	}

	mustNightCommand(t, api, token, "start-preshow")
	mustNightCommand(t, api, token, "start-night")
}

func TestInvariant5_ReadinessWithFailingEvidenceIsNotReady(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	observedAt := testNow
	obs.obs = []observation.Observation{{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
		Signal:   "fpp.reachable", Value: false, ObservedAt: &observedAt, CollectedAt: testNow,
		Source: "fpp-rest", Quality: observation.QualityDirect, ValidFor: time.Minute,
	}}
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	got := mustGetNightSession(t, api)
	if got.Session.Readiness.Outcome != "not_ready" {
		t.Fatalf("readiness outcome with reachable=false = %q, want not_ready", got.Session.Readiness.Outcome)
	}
}

func TestInvariant5_UnknownReadinessNeverBlocksStartNight(t *testing.T) {
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	obs.obs = nil
	mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")
	mustNightCommand(t, api, token, "start-preshow")

	out := mustNightCommand(t, api, token, "start-night")
	if out.Session.State != "transition-to-show" {
		t.Fatalf("start-night with unknown readiness: state = %q, want transition-to-show (readiness evidence does not block by itself)", out.Session.State)
	}
}

// --- invariant 6: power-down with no power configuration ---

func TestInvariant6_PowerDownWithNoPowerConfigReachesStoppedWithoutError(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	runToPreshow(t, api, token, obs, testNow)

	out := mustNightCommand(t, api, token, "power-down-presentation")
	if out.Session.State != nightStateFadingOut {
		t.Fatalf("power-down-presentation with no power config: state = %q, want fading-out", out.Session.State)
	}

	// The optional power phase resolves not_configured at the moment the
	// session actually reaches stopped, never before.
	rec := mustGetCurrentSession(t, st)
	h := &handlers{
		deps:  Dependencies{NightSessions: st, Observations: obs}.withDefaults(),
		clock: fixedClock(testNow), logger: testLogger(),
	}
	h.nightReachStopped(context.Background(), testNow, rec)

	final := mustGetCurrentSession(t, st)
	if final.State != nightStateStopped {
		t.Fatalf("after the stop was observed: state = %q, want stopped", final.State)
	}
	if got := mapNightPowerPhase(final); got.State != v1.NightEvidenceNotConfigured {
		t.Fatalf("power-down-presentation with no power config: powerPhase.state = %q, want not_configured", got.State)
	}
}

// --- invariant 7: submission is asynchronous ---

func TestInvariant7_CommandResponseCarriesBothAcceptanceAndSessionStateInOneRoundTrip(t *testing.T) {
	api, _, token, _, _ := setupNightControlFixture(t, time.Hour)
	start := time.Now()
	out := mustNightCommand(t, api, token, "prepare-site")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("prepare-site took %s to answer; this layer must never hold a request open waiting on a downstream outcome", elapsed)
	}
	if out.Session.ID == "" || out.Command.Outcome == "" {
		t.Fatalf("response missing session state or command outcome in the SAME round trip: %+v", out)
	}
}

// --- RESTING-MODE.md §4.4's start-night table, one test per row ---
//
// Each test seeds a real store with a session in the named state and
// drives nightStartNightTx inside one real transaction, matching the
// table's own row names.

func seedNightSession(t *testing.T, st *store.Store, state string) store.NightSessionRecord {
	t.Helper()
	rec := store.NightSessionRecord{ID: "s1", State: state, StateEnteredAt: testNow}
	if err := st.CreateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("seed night session in state %q: %v", state, err)
	}
	return rec
}

func newTableTestHandlers(t *testing.T) (*handlers, *store.Store) {
	t.Helper()
	_, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	h := &handlers{deps: Dependencies{NightSessions: st}.withDefaults(), clock: fixedClock(testNow), nightReadinessMaxAge: time.Hour}
	return h, st
}

func runStartNightTx(t *testing.T, h *handlers, st *store.Store, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem) {
	t.Helper()
	var out nightCommandOutcome
	var problem *v1.Problem
	err := st.InTx(context.Background(), func(ctx context.Context, tx *store.Tx) error {
		var derr error
		out, problem, derr = h.nightStartNightTx(ctx, tx, testNow, current)
		return derr
	})
	if err != nil {
		t.Fatalf("nightStartNightTx: %v", err)
	}
	return out, problem
}

func TestStartNightTable_PreparedPreshow_ConsumesEpochAndStartsNewNightSession(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "preshow")
	if err := st.CreateNightReadiness(context.Background(), store.NightReadinessRecord{ID: "r1", SessionID: "s1", EpochID: "s1", CompletedAt: testNow, Outcome: "ready", ChecksJSON: "[]"}); err != nil {
		t.Fatalf("seed readiness: %v", err)
	}
	out, problem := runStartNightTx(t, h, st, &session)
	if problem != nil {
		t.Fatalf("prepared preshow: problem=%v, want success", problem)
	}
	if out.result.State != "transition-to-show" || out.outcome != "applied" || out.result.Cycle != 1 {
		t.Fatalf("prepared preshow result = %+v, want state=transition-to-show outcome=applied cycle=1", out.result)
	}
}

func TestStartNightTable_RestingIntershow_IdempotentNoOp(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "resting-intershow")
	out, problem := runStartNightTx(t, h, st, &session)
	if problem != nil || out.outcome != "idempotent_no_op" {
		t.Fatalf("resting-intershow: out=%+v problem=%v, want idempotent_no_op", out, problem)
	}
}

func TestStartNightTable_TransitionToShow_IdempotentNoOp(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "transition-to-show")
	out, problem := runStartNightTx(t, h, st, &session)
	if problem != nil || out.outcome != "idempotent_no_op" {
		t.Fatalf("transition-to-show: out=%+v problem=%v, want idempotent_no_op", out, problem)
	}
}

func TestStartNightTable_TransitionToResting_IdempotentNoOp(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "transition-to-resting")
	out, problem := runStartNightTx(t, h, st, &session)
	if problem != nil || out.outcome != "idempotent_no_op" {
		t.Fatalf("transition-to-resting: out=%+v problem=%v, want idempotent_no_op", out, problem)
	}
}

func TestStartNightTable_Live_IdempotentNoOp(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "live")
	out, problem := runStartNightTx(t, h, st, &session)
	if problem != nil || out.outcome != "idempotent_no_op" {
		t.Fatalf("live: out=%+v problem=%v, want idempotent_no_op", out, problem)
	}
}

func TestStartNightTable_EndOfNightResting_Rejected(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "end-of-night-resting")
	_, problem := runStartNightTx(t, h, st, &session)
	if problem == nil || problem.Type != ProblemTypeNightStateRejected {
		t.Fatalf("end-of-night-resting: problem=%v, want %s", problem, ProblemTypeNightStateRejected)
	}
}

func TestStartNightTable_FadingOut_Rejected(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "fading-out")
	_, problem := runStartNightTx(t, h, st, &session)
	if problem == nil || problem.Type != ProblemTypeNightStateRejected {
		t.Fatalf("fading-out: problem=%v, want %s", problem, ProblemTypeNightStateRejected)
	}
}

func TestStartNightTable_Stopped_Rejected(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "stopped")
	_, problem := runStartNightTx(t, h, st, &session)
	if problem == nil || problem.Type != ProblemTypeNightStateRejected {
		t.Fatalf("stopped: problem=%v, want %s", problem, ProblemTypeNightStateRejected)
	}
}

func TestStartNightTable_Preparing_RejectedAsNotReady(t *testing.T) {
	h, st := newTableTestHandlers(t)
	session := seedNightSession(t, st, "preparing")
	_, problem := runStartNightTx(t, h, st, &session)
	if problem == nil || problem.Type != ProblemTypeNightNotReady {
		t.Fatalf("preparing: problem=%v, want %s", problem, ProblemTypeNightNotReady)
	}
}

func TestStartNightTable_Inactive_RejectedAsNotReady(t *testing.T) {
	h, st := newTableTestHandlers(t)
	out, problem := runStartNightTx(t, h, st, nil)
	if problem == nil || problem.Type != ProblemTypeNightNotReady {
		t.Fatalf("inactive (no session): problem=%v, want %s", problem, ProblemTypeNightNotReady)
	}
	_ = out
}

// --- RESTING-MODE.md §4.5's request-final-show table, one test per row ---
// nightRequestFinalShow takes no tx, so these call it directly.

func newFinalShowTestHandlers() *handlers {
	return &handlers{clock: func() time.Time { return testNow }, nightReadinessMaxAge: time.Hour}
}

func TestRequestFinalShowTable_Live_MarksFinal(t *testing.T) {
	session := store.NightSessionRecord{ID: "s1", State: "live", StateEnteredAt: testNow}
	h := newFinalShowTestHandlers()
	out, problem, err := h.nightRequestFinalShow(testNow, &session)
	if err != nil || problem != nil || !out.result.FinalShowRequested || out.outcome != "applied" {
		t.Fatalf("live: out=%+v problem=%v err=%v, want finalShowRequested=true/applied", out, problem, err)
	}
}

func TestRequestFinalShowTable_RestingIntershow_MarksFinal(t *testing.T) {
	session := store.NightSessionRecord{ID: "s1", State: "resting-intershow", StateEnteredAt: testNow}
	h := newFinalShowTestHandlers()
	out, problem, err := h.nightRequestFinalShow(testNow, &session)
	if err != nil || problem != nil || !out.result.FinalShowRequested {
		t.Fatalf("resting-intershow: out=%+v problem=%v err=%v, want finalShowRequested=true", out, problem, err)
	}
}

func TestRequestFinalShowTable_Preshow_MarksFinal(t *testing.T) {
	session := store.NightSessionRecord{ID: "s1", State: "preshow", StateEnteredAt: testNow}
	h := newFinalShowTestHandlers()
	out, problem, err := h.nightRequestFinalShow(testNow, &session)
	if err != nil || problem != nil || !out.result.FinalShowRequested {
		t.Fatalf("preshow: out=%+v problem=%v err=%v, want finalShowRequested=true", out, problem, err)
	}
}

func TestRequestFinalShowTable_TransitionToShow_MarksFinal(t *testing.T) {
	session := store.NightSessionRecord{ID: "s1", State: "transition-to-show", StateEnteredAt: testNow}
	h := newFinalShowTestHandlers()
	out, problem, err := h.nightRequestFinalShow(testNow, &session)
	if err != nil || problem != nil || !out.result.FinalShowRequested {
		t.Fatalf("transition-to-show: out=%+v problem=%v err=%v, want finalShowRequested=true", out, problem, err)
	}
}

func TestRequestFinalShowTable_TransitionToResting_MarksFinal(t *testing.T) {
	session := store.NightSessionRecord{ID: "s1", State: "transition-to-resting", StateEnteredAt: testNow}
	h := newFinalShowTestHandlers()
	out, problem, err := h.nightRequestFinalShow(testNow, &session)
	if err != nil || problem != nil || !out.result.FinalShowRequested {
		t.Fatalf("transition-to-resting: out=%+v problem=%v err=%v, want finalShowRequested=true", out, problem, err)
	}
}

func TestRequestFinalShowTable_EndOfNightOrLater_IdempotentNoOp(t *testing.T) {
	for _, state := range []string{"end-of-night-resting", "fading-out", "stopped"} {
		session := store.NightSessionRecord{ID: "s1", State: state, StateEnteredAt: testNow}
		h := newFinalShowTestHandlers()
		out, problem, err := h.nightRequestFinalShow(testNow, &session)
		if err != nil || problem != nil || out.outcome != "idempotent_no_op" {
			t.Fatalf("state=%s: out=%+v problem=%v err=%v, want idempotent_no_op with evidence, no error", state, out, problem, err)
		}
	}
}
