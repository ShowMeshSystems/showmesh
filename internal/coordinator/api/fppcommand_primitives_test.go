package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Step 8's own acceptance-criteria proof, one seam over from
// fppcommand_handler_test.go (Step 7's, kept intact and unmodified in its
// assertions): every primitive docs/bench/fpp-command-vocabulary.md
// section 4 adds beyond stopPlaylist, the startPlaylist ifBusy guard
// (section 5), the replay-vs-conflict rule extended to params, and the
// generalized reconcile sweep. Reuses newFPPCommandTestSetup,
// newFakeFPPCommandServer, fppStatusObs and friends from
// fppcommand_handler_test.go (same package).

func fppPlaylistNameObs(instanceID, value string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppPlaylistNameSignal), value, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("fpp-rest"),
	))
}

func fppPlaylistIndexObs(instanceID string, value int64, observedAt, collectedAt time.Time) observation.Observation {
	return fppPlaylistIndexObsFromSource(instanceID, value, "fpp-rest", observedAt, collectedAt)
}

// fppPlaylistIndexObsFromSource is [fppPlaylistIndexObs] with an explicit
// source — Finding 9's own tests need to construct a baseline read and a
// confirming read that answer from DIFFERENT collector sources, the exact
// shape [fppStatusObsFromSource] already provides for fpp.status.
func fppPlaylistIndexObsFromSource(instanceID string, value int64, source string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppPlaylistIndexSignal), value, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource(source),
	))
}

func fppVolumeObs(instanceID string, value int64, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID(fppVolumeSignal), value, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("fpp-rest"),
	))
}

// newFakeFPPCommandServerWithSideEffect is [newFakeFPPCommandServer] with
// onRequest called SYNCHRONOUSLY when the request arrives, before the
// response is written. Used only by the nextPlaylistItem/prevPlaylistItem
// baseline-movement tests below: those primitives' own CaptureBaseline
// runs immediately before dispatch, but "immediately before" is still
// real wall-clock time after the AuditedWrite transaction's own SQLite
// I/O, which is not reliably faster than a fixed sleep on a loaded
// machine — a background goroutine racing a guessed sleep duration
// against that write is exactly the "genuinely flaky" test shape
// CLAUDE.md's own Step 4 lesson warns against. Updating the observation
// lister from INSIDE the fake FPP handler instead guarantees program
// order: this handler cannot run before this Client's own dispatch call,
// which cannot run before CaptureBaseline already returned.
func newFakeFPPCommandServerWithSideEffect(t *testing.T, status int, body string, onRequest func()) (*httptest.Server, *fakeFPPCommandServer) {
	t.Helper()
	f := &fakeFPPCommandServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.EscapedPath())
		f.requestBodies = append(f.requestBodies, string(raw))
		f.requestTimes = append(f.requestTimes, time.Now())
		f.mu.Unlock()
		onRequest()
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func fppCommandBody(action, idempotencyKey, paramsJSON string) string {
	if paramsJSON == "" {
		return `{"action":"` + action + `","idempotencyKey":"` + idempotencyKey + `"}`
	}
	return `{"action":"` + action + `","idempotencyKey":"` + idempotencyKey + `","params":` + paramsJSON + `}`
}

// --- startPlaylist: confirmation (identity, not just "playing") ---

func TestStartPlaylistConfirmsOnStatusAndNameMatch(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Idle at dispatch time: ifBusy=refuse must not refuse (nothing else
	// playing). Evidence catches up to playing+matching name only once
	// the fake FPP has RECEIVED the dispatch — a timed flip can win a
	// race against the pre-dispatch read under a slow scheduler (the
	// race detector measured it) and make the handler correctly send
	// ifNotRunning=true against this test's "idle at dispatch" premise.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow, testNow),
	})
	srv.onRequest = func() {
		setup.obs.setObs([]observation.Observation{
			fppStatusObs("bench-fpp", "playing", testNow, testNow),
			fppPlaylistNameObs("bench-fpp", "showmesh-test", testNow, testNow),
		})
	}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"; body: %s", cmd["outcome"], respBody)
	}
	wantBody := `{"command":"Start Playlist","args":["showmesh-test","false","false"]}`
	if got := srv.lastBody(); got != wantBody {
		t.Errorf("dispatch body = %q, want %q (ifNotRunning=false: nothing else was playing)", got, wantBody)
	}
	params, _ := cmd["params"].(map[string]any)
	if params["playlist"] != "showmesh-test" {
		t.Errorf("echoed params.playlist = %v, want \"showmesh-test\"", params["playlist"])
	}
	// Finding 6 (Step 8 review): see TestPausePlaylistConfirms's identical
	// assertion — evaluateStartPlaylistEvidence returned "" on its
	// confirmed branch before this fix.
	if reason, _ := cmd["outcomeReason"].(string); reason == "" {
		t.Errorf("outcomeReason is empty on a confirmed startPlaylist — want it to state the confirming evidence (Finding 6)")
	}
}

func TestStartPlaylistUnconfirmedWhenPlayingButNameDiffers(t *testing.T) {
	// The exact mirror of Step 7's 179-microsecond defect: FPP's own
	// status reads "playing" but the NAME is a different playlist — FPP's
	// own scheduler may have started it. "playing" alone must never
	// confirm this command.
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	go func() {
		time.Sleep(30 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{
			fppStatusObs("bench-fpp", "playing", testNow, testNow),
			fppPlaylistNameObs("bench-fpp", "some-other-playlist", testNow, testNow),
		})
	}()

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — \"playing\" alone must not credit this command with FPP's own "+
			"scheduler starting a DIFFERENT playlist", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "DIFFERENT playlist") {
		t.Errorf("outcomeReason = %q, want it to name the mismatch (ADR-001)", reason)
	}
}

// --- startPlaylist: ifBusy (capture section 5) ---

func TestStartPlaylistIfNotRunningTrueWhenSamePlaylistAlreadyPlaying(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "showmesh-test", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — requesting the ALREADY playing playlist is not busy; body: %s", resp.StatusCode, respBody)
	}
	wantBody := `{"command":"Start Playlist","args":["showmesh-test","false","true"]}`
	if got := srv.lastBody(); got != wantBody {
		t.Errorf("dispatch body = %q, want %q (ifNotRunning=true so the running item is not restarted)", got, wantBody)
	}
}

func TestStartPlaylistRefusedWhenDifferentPlaylistPlaying(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0 — a busy refusal must dispatch nothing", srv.hitCount())
	}
	m := decodeMap(t, respBody)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "other-playlist") {
		t.Errorf("detail = %q, want it to name what is currently playing", detail)
	}
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a busy refusal must not create a commands row either", len(rows))
	}
}

// TestStartPlaylistRefusedWhenPausedWithDifferentPlaylist is Finding 5's
// own regression proof (Step 8 review, proved live against the bench):
// PreDispatchCheck originally treated ANY status other than "playing" as
// never busy, so a default (ifBusy=refuse) startPlaylist DISPATCHED over
// a host reading "paused" with a DIFFERENT playlist loaded — a show the
// operator deliberately paused. Capture section 5 requires startPlaylist
// to never silently replace a running show; "busy" is redefined here to
// mean "not idle" (this primitive's own doc comment), and paused is not
// idle.
func TestStartPlaylistRefusedWhenPausedWithDifferentPlaylist(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValuePaused, testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a PAUSED show with a different playlist loaded is busy; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0 — a busy refusal must dispatch nothing", srv.hitCount())
	}
}

// TestStartPlaylistRefusedWhenStoppingGracefullyWithDifferentPlaylist is
// Finding 5's own second regression proof: capture section 3.3 measured
// "stopping gracefully" holding the CURRENT item still playing,
// indefinitely, against a 120-second item — this is a running show by
// any operator-facing definition, and the original PreDispatchCheck
// treated it as never busy.
func TestStartPlaylistRefusedWhenStoppingGracefullyWithDifferentPlaylist(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValueStoppingGracefully, testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — \"stopping gracefully\" still holds the current item playing (capture "+
			"section 3.3); body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0 — a busy refusal must dispatch nothing", srv.hitCount())
	}
}

// TestStartPlaylistRefusedWhenStatusUnknown is Finding 5's third
// regression proof: FPP's own "unknown" status_name (capture section 3.1)
// means this coordinator cannot tell what is running, and "could not
// tell" must never be read as "not busy" — the same rule already applied
// to a stale/absent status reading, extended to FPP's own named "unknown"
// value.
func TestStartPlaylistRefusedWhenStatusUnknown(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValueUnknown, testNow, testNow),
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — fpp.status = \"unknown\" must refuse, never be read as \"not busy\"; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0", srv.hitCount())
	}
	m := decodeMap(t, respBody)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, fppStatusValueUnknown) {
		t.Errorf("detail = %q, want it to name the unknown status", detail)
	}
}

// TestStartPlaylistIfBusyReplaceDispatchesWhilePaused proves ifBusy
// still overrides busy correctly for the new "not idle" definition: a
// PAUSED host with a different playlist loaded, requested with
// ifBusy=replace, must still dispatch — the operator explicitly said
// interrupt.
func TestStartPlaylistIfBusyReplaceDispatchesWhilePaused(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValuePaused, testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test","ifBusy":"replace"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — ifBusy=replace must dispatch despite a paused show; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

func TestStartPlaylistIfBusyReplaceDispatchesOverRunningShow(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "other-playlist", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test","ifBusy":"replace"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — ifBusy=replace must dispatch despite a different playlist playing; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
	wantBody := `{"command":"Start Playlist","args":["showmesh-test","false","false"]}`
	if got := srv.lastBody(); got != wantBody {
		t.Errorf("dispatch body = %q, want %q", got, wantBody)
	}
}

func TestStartPlaylistRefusedWhenEvidenceNotCurrent(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// No observations at all: the guard must refuse rather than treat
	// "cannot tell" as "not busy".
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0", srv.hitCount())
	}
	m := decodeMap(t, respBody)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "CURRENT evidence") {
		t.Errorf("detail = %q, want it to say current evidence is required", detail)
	}
}

func TestStartPlaylistTraversalNameRejectedBefore400(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"../etc/passwd"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (traversal); body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0", srv.hitCount())
	}
}

// --- stopPlaylistGracefully: confirms on entering a stop state, never
// requires idle (capture section 3.3). ---

func TestStopPlaylistGracefullyConfirmsOnStoppingStateAndSaysNotStopped(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopping Gracefully")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValueStoppingGracefully, testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("stopPlaylistGracefully", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\" (FPP entered a stopping state)", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "NOT stopped") {
		t.Errorf("outcomeReason = %q, want it to say plainly the show has NOT stopped despite being confirmed", reason)
	}
}

func TestStopPlaylistGracefullyConfirmsOnIdle(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopping Gracefully")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValueIdle, testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("stopPlaylistGracefully", "key-1", `{"afterLoop":true}`), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "completed") {
		t.Errorf("outcomeReason = %q, want it to say the graceful stop completed", reason)
	}
}

func TestStopPlaylistGracefullyUnconfirmedWhilePlaying(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopping Gracefully")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("stopPlaylistGracefully", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — FPP never entered any stop state", cmd["outcome"])
	}
}

// --- stopPlaylist: Finding 6's own regression proof for the primitive
// Step 7 already shipped (fppcommand_handler_test.go owns stopPlaylist's
// OTHER acceptance criteria; this one test lives here, in this task's own
// seam, rather than in that file). ---

// TestStopPlaylistConfirmedReasonIsNonEmpty is Finding 6's own regression
// proof (Step 8 review): api/openapi.yaml and v1/commands.go both
// document outcomeReason as non-empty whenever outcome is confirmed OR
// unconfirmed, but evaluateFPPStatusEvidence (which stopPlaylist,
// pausePlaylist, and resumePlaylist all share) returned "" on a confirmed
// outcome. Verified: reverting evaluateFPPStatusEvidence's confirmed
// branch to `return true, state, ""` makes this test fail.
func TestStopPlaylistConfirmedReasonIsNonEmpty(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	if reason, _ := cmd["outcomeReason"].(string); reason == "" {
		t.Errorf("outcomeReason is empty on a confirmed stopPlaylist — want it to state the confirming evidence (Finding 6)")
	}
}

// --- pausePlaylist / resumePlaylist: plain single-signal confirmation. ---

func TestPausePlaylistConfirms(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Paused")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "paused", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("pausePlaylist", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	// Finding 6 (Step 8 review): outcomeReason must be non-empty on a
	// confirmed outcome too — api/openapi.yaml and v1/commands.go both
	// document it as non-empty whenever outcome is confirmed OR
	// unconfirmed. pausePlaylist's own predicate (evaluateFPPStatusEvidence)
	// returned "" here before this fix.
	if reason, _ := cmd["outcomeReason"].(string); reason == "" {
		t.Errorf("outcomeReason is empty on a confirmed pausePlaylist — want it to state the confirming evidence (Finding 6)")
	}
}

func TestResumePlaylistConfirms(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Restarted")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("resumePlaylist", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	// Finding 6 (Step 8 review): see TestPausePlaylistConfirms's identical
	// assertion.
	if reason, _ := cmd["outcomeReason"].(string); reason == "" {
		t.Errorf("outcomeReason is empty on a confirmed resumePlaylist — want it to state the confirming evidence (Finding 6)")
	}
}

// --- nextPlaylistItem / prevPlaylistItem: pre-dispatch baseline. ---

func TestNextPlaylistItemConfirmsOnIndexMove(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.obs.setObs([]observation.Observation{
		fppPlaylistIndexObs("bench-fpp", 1, testNow, testNow),
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
	})
	// The obs update happens INSIDE the fake FPP handler — guaranteed to
	// run only after CaptureBaseline has already read index=1 — see
	// [newFakeFPPCommandServerWithSideEffect]'s own doc comment for why
	// this replaces a sleep-based goroutine race.
	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, http.StatusOK, "Next Item Playing", func() {
		setup.obs.setObs([]observation.Observation{
			fppPlaylistIndexObs("bench-fpp", 2, testNow, testNow),
			fppStatusObs("bench-fpp", "playing", testNow, testNow),
		})
	})
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("nextPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\" once the index moved from the pre-dispatch baseline; reason=%v", cmd["outcome"], cmd["outcomeReason"])
	}
}

// TestNextPlaylistItemConfirmsOnIdleFallbackAtLastItem is Finding 1's own
// positive control, rewritten (Step 8 review) to isolate the idle branch
// from index movement rather than changing both simultaneously: this
// test's ORIGINAL form set fpp.playlist.index=3 pre-dispatch and then
// changed it to 0 alongside fpp.status going idle, on a background
// goroutine racing this request's own bcrypt-backed principal/token
// creation against a fixed 60ms sleep — indistinguishable, from this
// test alone, from the index-MOVEMENT branch confirming it instead of
// the idle branch (index 3 -> 0 is itself a move), and in practice
// FLAKY against Finding 1's fix: whichever branch's evidence CaptureBaseline
// happened to observe first (a real wall-clock race against bcrypt
// hashing) decided which reason string came back. This form instead
// omits any pre-dispatch fpp.playlist.index observation at all, so
// baseline.IndexKnown is false and the movement branch cannot fire
// regardless of timing — isolating the idle branch on its own, which
// capture section 3.5 requires independently (see
// TestNextPlaylistItemConfirmsOnIndexMove for the movement-only case).
// fpp.status reads "playing" (not idle) at baseline-capture time,
// satisfying Finding 1's gate that the idle branch may confirm only when
// the host was NOT already idle before dispatch. The obs update happens
// INSIDE the fake FPP handler — guaranteed to run only after
// CaptureBaseline already read status="playing" — see
// [newFakeFPPCommandServerWithSideEffect]'s own doc comment.
func TestNextPlaylistItemConfirmsOnIdleFallbackAtLastItem(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
	})
	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, http.StatusOK, "Next Item Playing", func() {
		setup.obs.setObs([]observation.Observation{
			fppPlaylistIndexObs("bench-fpp", 0, testNow, testNow),
			fppStatusObs("bench-fpp", "idle", testNow, testNow),
		})
	})
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("nextPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\" via the idle fallback (Next past the last item ends the playlist); reason=%v", cmd["outcome"], cmd["outcomeReason"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "ends the playlist") {
		t.Errorf("outcomeReason = %q, want it to explain the idle-fallback reasoning", reason)
	}
}

// TestNextPlaylistItemUnconfirmedWhenAlreadyIdleAtBaseline is Finding 1's
// own regression proof (Step 8 review), reproducing the defect verified
// live against the bench fppd on 2026-08-13: idle before dispatch AND
// idle after must never report confirmed. Capture section 2 measured
// "Next Playlist Item" while idle as FPP's own documented no-op (200
// "Next Item Playing", nothing changes); the original predicate accepted
// fpp.status == "idle" post-dispatch with no check that the host was NOT
// already idle pre-dispatch, so it could not tell that shape apart from
// "Next ended the show" (section 3.5) and reported a command that
// provably did nothing as confirmed.
func TestNextPlaylistItemUnconfirmedWhenAlreadyIdleAtBaseline(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Next Item Playing")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Idle before AND after dispatch — the exact shape capture section 2
	// measured as a no-op, and the exact shape that live-reproduced
	// Finding 1. Nothing changes across the confirmation window.
	setup.obs.setObs([]observation.Observation{
		fppPlaylistIndexObs("bench-fpp", 0, testNow, testNow),
		fppStatusObs("bench-fpp", "idle", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("nextPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — the host was ALREADY idle before dispatch, so fpp.status still "+
			"reading idle afterwards is FPP's own documented no-op (capture section 2), not evidence this command did "+
			"anything", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "ALREADY idle") {
		t.Errorf("outcomeReason = %q, want it to say the host was already idle before dispatch", reason)
	}
}

// TestNextPlaylistItemDoesNotConfirmOnSourceFlip is Finding 9's own
// regression proof (Step 8 review): fppCaptureIndexBaseline and
// evaluateNextItemEvidence's own confirming read are two INDEPENDENT
// calls into ResolveObservations, and both fpp-rest and fpp-mqtt emit
// fpp.playlist.index — so a DIFFERENT source can win the confirming read
// than won the baseline read, which is a collector disagreement, not
// FPP's own counter moving. Proved live: baseline fpp-rest index 2,
// confirming fpp-mqtt index 5 wins, and the original code reported "moved
// from 2 to 5" though the index never moved on FPP.
func TestNextPlaylistItemDoesNotConfirmOnSourceFlip(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.obs.setObs([]observation.Observation{
		fppPlaylistIndexObsFromSource("bench-fpp", 2, "fpp-rest", testNow, testNow),
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
	})
	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, http.StatusOK, "Next Item Playing", func() {
		// A DIFFERENT collector source now wins ResolveObservations'
		// precedence for the SAME signal — the index VALUE differs (2 ->
		// 5), but the two readings came from different sources and must
		// never render as movement.
		setup.obs.setObs([]observation.Observation{
			fppPlaylistIndexObsFromSource("bench-fpp", 5, "fpp-mqtt", testNow, testNow),
			fppStatusObs("bench-fpp", "playing", testNow, testNow),
		})
	})
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("nextPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — the index went from 2 (fpp-rest) to 5 (fpp-mqtt), a SOURCE "+
			"FLIP, not FPP's own counter moving; a different winning collector between the baseline read and the "+
			"confirming read must never render as movement", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "different source") {
		t.Errorf("outcomeReason = %q, want it to name the source flip", reason)
	}
}

func TestNextPlaylistItemUnconfirmedWithNoBaseline(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Next Item Playing")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// No fpp.playlist.index observation at all: no baseline. Status stays
	// "playing" (never idle) throughout.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("nextPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — no baseline was available and status never reached idle", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "movement can't be evaluated") {
		t.Errorf("outcomeReason = %q, want it to say no baseline was available", reason)
	}
}

func TestPrevPlaylistItemConfirmsOnIndexMove(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.obs.setObs([]observation.Observation{fppPlaylistIndexObs("bench-fpp", 3, testNow, testNow)})
	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, http.StatusOK, "Prev Item Playing", func() {
		setup.obs.setObs([]observation.Observation{fppPlaylistIndexObs("bench-fpp", 2, testNow, testNow)})
	})
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("prevPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
}

// TestPrevPlaylistItemDoesNotConfirmOnSourceFlip is
// [TestNextPlaylistItemDoesNotConfirmOnSourceFlip]'s sibling for Finding
// 9 (Step 8 review): evaluatePrevItemEvidence shares the identical
// baseline-vs-confirming-read source mismatch bug.
func TestPrevPlaylistItemDoesNotConfirmOnSourceFlip(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.obs.setObs([]observation.Observation{
		fppPlaylistIndexObsFromSource("bench-fpp", 3, "fpp-rest", testNow, testNow),
	})
	fppSrv, _ := newFakeFPPCommandServerWithSideEffect(t, http.StatusOK, "Prev Item Playing", func() {
		setup.obs.setObs([]observation.Observation{
			fppPlaylistIndexObsFromSource("bench-fpp", 1, "fpp-mqtt", testNow, testNow),
		})
	})
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("prevPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — the index went from 3 (fpp-rest) to 1 (fpp-mqtt), a SOURCE "+
			"FLIP, not FPP's own counter moving", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "different source") {
		t.Errorf("outcomeReason = %q, want it to name the source flip", reason)
	}
}

func TestPrevPlaylistItemUnconfirmedWithNoBaseline(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Prev Item Playing")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 100 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("prevPlaylistItem", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — no baseline was available", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "movement can't be evaluated") {
		t.Errorf("outcomeReason = %q, want it to say no baseline was available", reason)
	}
}

// --- setVolume ---

func TestSetVolumeConfirmsOnMatchingValue(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Volume Set")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppVolumeObs("bench-fpp", 55, testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("setVolume", "key-1", `{"volume":55}`), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	wantBody := `{"command":"Volume Set","args":["55"]}`
	if got := srv.lastBody(); got != wantBody {
		t.Errorf("dispatch body = %q, want %q", got, wantBody)
	}
	// Finding 6 (Step 8 review): outcomeReason must be non-empty on a
	// confirmed setVolume too — see TestPausePlaylistConfirms's identical
	// assertion.
	if reason, _ := cmd["outcomeReason"].(string); reason == "" {
		t.Errorf("outcomeReason is empty on a confirmed setVolume — want it to state the confirming evidence (Finding 6)")
	}
}

func TestSetVolumeUnconfirmedOnMismatch(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Volume Set")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppVolumeObs("bench-fpp", 10, testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("setVolume", "key-1", `{"volume":55}`), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\"", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "want 55") {
		t.Errorf("outcomeReason = %q, want it to name the requested value", reason)
	}
}

func TestSetVolumeRejectsOutOfRange(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Volume Set")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("setVolume", "key-1", `{"volume":150}`), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (out of range); body: %s", resp.StatusCode, respBody)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0", srv.hitCount())
	}
}

// --- Unsupported action lists the full vocabulary. ---

func TestFPPCommandUnsupportedActionListsFullVocabulary(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "OK")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("bogusAction", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	detail, _ := m["detail"].(string)
	for _, action := range fppCommandWireActions() {
		if !strings.Contains(detail, action) {
			t.Errorf("detail = %q, want it to name every supported action, missing %q", detail, action)
		}
	}
}

// --- Replay vs. conflict, extended to params (Step 8 seam). ---

func TestFPPCommandReplaySameNormalizedParamsIsAReplay(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 100 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// First request: params sent EXPLICITLY, matching every default.
	first := newFPPCommandRequest(t, "bench-fpp",
		fppCommandBody("startPlaylist", "shared-key", `{"playlist":"showmesh-test","repeat":false,"ifBusy":"refuse"}`), token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want 1", srv.hitCount())
	}

	// Second request: SAME key, but params OMIT the defaulted fields
	// entirely. Must normalize identically and be treated as a replay.
	second := newFPPCommandRequest(t, "bench-fpp",
		fppCommandBody("startPlaylist", "shared-key", `{"playlist":"showmesh-test"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the replay, want still 1 — a replay must dispatch nothing", srv.hitCount())
	}
	m2 := decodeMap(t, body2)
	cmd2, _ := m2["command"].(map[string]any)
	if cmd2["replay"] != true {
		t.Errorf("replay = %v, want true (omitting a defaulted field must normalize identically to sending it explicitly)", cmd2["replay"])
	}
	params, _ := cmd2["params"].(map[string]any)
	if params["repeat"] != false || params["ifBusy"] != fppIfBusyRefuse {
		t.Errorf("replay params = %v, want the original's defaults echoed back", params)
	}
}

func TestFPPCommandReplayParamsConflict(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 100 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	first := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("startPlaylist", "shared-key", `{"playlist":"playlist-a"}`), token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want 1", srv.hitCount())
	}
	m1 := decodeMap(t, body1)
	cmd1, _ := m1["command"].(map[string]any)

	second := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("startPlaylist", "shared-key", `{"playlist":"playlist-b"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the conflicting replay, want still 1 — a params conflict must dispatch nothing", srv.hitCount())
	}
	m2 := decodeMap(t, body2)
	if _, ok := m2["command"]; ok {
		t.Errorf("conflict response unexpectedly carries a \"command\" object: %v", m2["command"])
	}
	detail, _ := m2["detail"].(string)
	if !strings.Contains(detail, `"playlist":"playlist-a"`) || !strings.Contains(detail, `"playlist":"playlist-b"`) {
		t.Errorf("detail = %q, want it to name both the original and requested params", detail)
	}
	if !strings.Contains(detail, cmd1["id"].(string)) {
		t.Errorf("detail = %q, want it to name the original command id", detail)
	}
}

// --- Reconcile, generalized past stopPlaylist. ---

func TestReconcileStrandedFPPCommandsResolvesNonStopPlaylistPrimitive(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	dispatchedAt := testNow.Add(-time.Minute)
	rec, err := setup.st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "stranded-volume", IdempotencyKey: "key-stranded-volume", Action: "fpp.set_volume",
		TargetKind: "fpp", TargetID: "bench-fpp", ParamsJSON: `{"volume":42}`,
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("insert stranded command: %v", err)
	}
	dispatchedState := "dispatched"
	if err := setup.st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchedState,
	}); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}

	setup.obs.setObs([]observation.Observation{fppVolumeObs("bench-fpp", 42, testNow, testNow)})

	resolved, err := ReconcileStrandedFPPCommands(context.Background(), setup.deps(), fixedClock(testNow), testLogger())
	if err != nil {
		t.Fatalf("ReconcileStrandedFPPCommands: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	got, err := setup.st.GetCommand(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if got.State != "resolved" {
		t.Errorf("state = %q, want \"resolved\"", got.State)
	}
	if got.OutcomeState != string(observation.StateCurrent) {
		t.Errorf("outcome_state = %q, want %q", got.OutcomeState, observation.StateCurrent)
	}
}

// --- Client timeout budget: every primitive's own confirm deadline stays
// bounded by command.MaxFPPCommandConfirmDeadline. ---

// TestFPPMaxConfirmDeadlineTracksConfiguredBase proves fppMaxConfirmDeadline
// (which sizes handleFPPCommand's own HTTP write deadline) scales with an
// OPERATOR-CONFIGURED base rather than silently assuming
// command.DefaultFPPCommandConfirmDeadline — a deployment that configures
// Options.FPPCommandConfirmDeadline larger than the default must not have
// its own write deadline expire before its own confirmation wait can
// finish (the exact defect Step 7's "finding 1.1" fixed for the SSE
// stream, reachable here again if this function silently used the fixed
// constant instead of its own argument).
func TestFPPMaxConfirmDeadlineTracksConfiguredBase(t *testing.T) {
	base := command.DefaultFPPCommandConfirmDeadline + time.Minute
	got := fppMaxConfirmDeadline(base)
	if got < base {
		t.Errorf("fppMaxConfirmDeadline(%s) = %s, want at least %s (an operator-configured base must never be shrunk to the fixed default)", base, got, base)
	}
}

func TestNoFPPCommandPrimitiveDeadlineExceedsMaxConfirmDeadline(t *testing.T) {
	for _, p := range fppCommandPrimitives {
		got := p.ConfirmDeadline(command.DefaultFPPCommandConfirmDeadline)
		if got > command.MaxFPPCommandConfirmDeadline {
			t.Errorf("primitive %q's own confirm deadline (%s) exceeds command.MaxFPPCommandConfirmDeadline (%s) — "+
				"a client timeout budget derived only from the max would abort before this primitive's own server-side "+
				"deadline could ever be observed", p.WireAction, got, command.MaxFPPCommandConfirmDeadline)
		}
	}
}

// --- ADR-024 decision 11's safety class: this task's own regression
// guard against the exact defect it fixes (Step 8 had inherited Step 7's
// one-primitive exemption onto all eight primitives with no review,
// because [fppPrimitive] carried no field to force the decision at all). ---

// TestEveryFPPCommandPrimitiveDeclaresSafetyClass fails the moment any
// primitive in the registry carries fppSafetyClassUndeclared (the zero
// value) — see [fppSafetyClass]'s own doc comment for why the zero value
// is deliberately invalid rather than defaulting to either membership
// meaning. A future ninth primitive that forgets to set SafetyClass fails
// THIS test, not silently inherits the previous primitive's decision (a
// bare bool could not make that distinction; this type exists so it can).
func TestEveryFPPCommandPrimitiveDeclaresSafetyClass(t *testing.T) {
	if len(fppCommandPrimitives) == 0 {
		t.Fatal("fppCommandPrimitives is empty; this test cannot prove anything")
	}
	for _, p := range fppCommandPrimitives {
		if p.SafetyClass == fppSafetyClassUndeclared {
			t.Errorf("primitive %q has no explicit SafetyClass (fppSafetyClassUndeclared, the zero value) — every "+
				"primitive registered in fppCommandPrimitives must explicitly set fppSafetyClassExempt or "+
				"fppSafetyClassNotExempt (ADR-024 decision 11); see fppcommand_primitives.go's fppSafetyClass doc "+
				"comment for the membership decision", p.WireAction)
		}
	}
}

// TestFPPCommandSafetyClassMembershipIsExactlyStopPlaylistPair pins the
// actual membership decision (fppcommand_primitives.go's [fppSafetyClass]
// doc comment) against silent addition or removal: exactly stopPlaylist
// and stopPlaylistGracefully — decision 11's own named "stop" — are
// exempt, and every other registered primitive, including pausePlaylist
// (which preserves playback state and is deliberately NOT decision 11's
// stop path), is not.
func TestFPPCommandSafetyClassMembershipIsExactlyStopPlaylistPair(t *testing.T) {
	wantExempt := map[string]bool{
		"stopPlaylist":           true,
		"stopPlaylistGracefully": true,
	}
	for _, p := range fppCommandPrimitives {
		got := p.SafetyClass == fppSafetyClassExempt
		want := wantExempt[p.WireAction]
		if got != want {
			t.Errorf("primitive %q: SafetyClass exempt = %v, want %v (ADR-024 decision 11's safety class is exactly "+
				"stopPlaylist and stopPlaylistGracefully)", p.WireAction, got, want)
		}
	}
}

// TestStopPlaylistGracefullyProceedsWithAuditStoreFailing is
// [fppSafetyClassExempt]'s SECOND member, proven independently of
// fppcommand_handler_test.go's TestFPPCommandSucceedsWithAuditStoreFailing
// (stopPlaylist, Step 7's original acceptance criterion, deliberately left
// untouched by this task). ADR-024 decision 11's blackout/stop/power-off
// safety class is "stop", not "the one primitive named stopPlaylist" —
// stopPlaylistGracefully is decision 11's OTHER stop primitive and must
// proceed on a pre-dispatch audit-write failure exactly like stopPlaylist
// does, with degraded attribution, never refused.
func TestStopPlaylistGracefullyProceedsWithAuditStoreFailing(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopping Gracefully")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", fppStatusValueIdle, testNow, testNow),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// Installed AFTER principal/token creation — see
	// TestFPPCommandSucceedsWithAuditStoreFailing's identical comment:
	// neither CreatePrincipal nor IssueToken writes to audit_log, so this
	// is the exact moment every SUBSEQUENT audit write starts failing.
	installFailAuditTrigger(t, setup.storeDir)

	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	req := newFPPCommandRequest(t, "bench-fpp", fppCommandBody("stopPlaylistGracefully", "key-1", ""), token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — stopPlaylistGracefully is in ADR-024 decision 11's blackout/stop/power-off "+
			"safety class and must proceed regardless of an audit-write failure; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\" — the command itself must still run and confirm normally", cmd["outcome"])
	}
	if cmd["attributionDegraded"] != true {
		t.Errorf("attributionDegraded = %v, want true (the audit write did fail)", cmd["attributionDegraded"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1 — the command must actually dispatch, not merely appear to", srv.hitCount())
	}
}
