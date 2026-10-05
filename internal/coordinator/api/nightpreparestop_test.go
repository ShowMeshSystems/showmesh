package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func preparingSession(now time.Time) store.NightSessionRecord {
	return store.NightSessionRecord{
		ID: "sess-1", ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: nightStatePreparing, StateEnteredAt: now,
	}
}

func TestNightStopFPPAtPrepareSite_ReportsAStopFPPConfirmedIdle(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))
	f.obs.set([]observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, now.Add(time.Second)),
		playlistNameObservation("player-01", "", now.Add(time.Second)),
	})

	got := f.h.nightStopFPPAtPrepareSite(context.Background(), now, identity.AuditEntry{PrincipalID: "op"}, "key-1", mustGetCurrentSession(t, f.store))

	if cmds := f.sentCommands(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent to FPP = %v, want exactly one %q", cmds, "Stop Now")
	}
	if want := `Stopped what FPP was playing on player-01.`; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestNightStopFPPAtPrepareSite_ReportsAStopFPPDoesNotConfirm(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))

	got := f.h.nightStopFPPAtPrepareSite(context.Background(), now, identity.AuditEntry{PrincipalID: "op"}, "key-1", mustGetCurrentSession(t, f.store))

	if !strings.Contains(got, "was told to stop but has not reported that it is idle") || !strings.Contains(got, "stop it by hand") {
		t.Fatalf("result = %q, want an unconfirmed-stop report with the action to take", got)
	}
	if strings.Contains(got, "Stopped what FPP was playing") {
		t.Fatalf("result = %q claims a stop with no idle evidence", got)
	}
}

const nightStopByHand = " Check FPP and stop it by hand if it is still playing."

func stopWithEndpoint(t *testing.T, f *nightShutdownFixture, views []FPPInstanceView) string {
	t.Helper()
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f.h.deps.FPP = &fakeFPPLister{views: views}
	return f.h.nightStopFPPAtPrepareSite(context.Background(), now, identity.AuditEntry{PrincipalID: "op"}, "key-1", mustGetCurrentSession(t, f.store))
}

func TestNightStopFPPAtPrepareSite_ReportsAStopFPPRefused(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(refusing.Close)

	got := stopWithEndpoint(t, f, []FPPInstanceView{{InstanceID: "player-01", Endpoint: refusing.URL}})

	if want := `FPP "player-01" refused the stop.` + nightStopByHand; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestNightStopFPPAtPrepareSite_ReportsAnUnreachableFPP(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))
	gone := httptest.NewServer(http.NotFoundHandler())
	endpoint := gone.URL
	gone.Close()

	got := stopWithEndpoint(t, f, []FPPInstanceView{{InstanceID: "player-01", Endpoint: endpoint}})

	if want := `FPP "player-01" could not be reached, so it was not stopped.` + nightStopByHand; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestNightStopFPPAtPrepareSite_ReportsAnInstanceTheCoordinatorCannotAskAs_NotAnFPPRefusal(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))

	got := stopWithEndpoint(t, f, nil)

	if want := `FPP "player-01" was not asked to stop: no FPP instance with id "player-01" is configured.` + nightStopByHand; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestNightStopFPPAtPrepareSite_StopsTheNightsInstancesTogether(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	payload := nightShutdownPayload()
	payload.ShowPlaylist.FPPInstanceID = "player-02"
	f := newNightShutdownFixture(t, &now, payload, preparingSession(now))
	f.h.fppCommandConfirmDeadline = 400 * time.Millisecond
	views := []FPPInstanceView{{InstanceID: "player-01", Endpoint: f.endpoint}, {InstanceID: "player-02", Endpoint: f.endpoint}}

	start := time.Now()
	got := stopWithEndpoint(t, f, views)
	elapsed := time.Since(start)

	if cmds := f.sentCommands(); len(cmds) != 2 {
		t.Fatalf("commands sent to FPP = %v, want one stop per instance", cmds)
	}
	if !strings.Contains(got, `FPP "player-01" was told to stop`) || !strings.Contains(got, `FPP "player-02" was told to stop`) {
		t.Fatalf("result = %q, want both instances reported", got)
	}
	if elapsed >= 750*time.Millisecond {
		t.Fatalf("stopping two instances took %s, want about one confirmation deadline (400ms), not two", elapsed)
	}
}

// preparedFixture is a real HTTP-driven night with a fake FPP that records
// every command and reports idle only when asked to.
type preparedFixture struct {
	api   *API
	st    *store.Store
	token string
	obs   *fakeObservationLister

	mu       sync.Mutex
	commands []string
}

func (f *preparedFixture) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func setupPreparedFixture(t *testing.T) *preparedFixture {
	t.Helper()
	f := &preparedFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Command != "" {
			f.mu.Lock()
			f.commands = append(f.commands, body.Command)
			f.mu.Unlock()
		}
		_, _ = w.Write([]byte("OK"))
	}))
	t.Cleanup(srv.Close)

	_, now := mutableClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	f.token = mustIssueToken(t, svc, operator.ID)

	deps, obs := nightControlTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: srv.URL}}}
	backend := nightTestAssetBackend(t)
	deps.AssetBackend = backend
	f.api = New(deps, Options{
		Clock: now, Logger: testLogger(), NightReadinessMaxAge: time.Hour,
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})
	f.st, f.obs = st, obs

	mustPutShow(t, f.api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShowAction(t, f.api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionFSEQAsset(t, st, backend, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, f.api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, f.api, adminToken, "halloween-main")
	return f
}

func (f *preparedFixture) reportIdle() {
	at := testNow.Add(time.Second)
	f.obs.obs = []observation.Observation{
		statusObservation("player-01", fppStatusValueIdle, at),
		playlistNameObservation("player-01", "", at),
	}
}

func TestNightPrepareSite_StopFppPlayback_StopsFPPThroughTheHandler(t *testing.T) {
	f := setupPreparedFixture(t)
	f.reportIdle()

	resp := mustNightCommandBody(t, f.api, f.token, "prepare-site", `{"stopFppPlayback":true}`)

	if resp.Command.Outcome != "applied" {
		t.Fatalf("outcome = %q, want applied", resp.Command.Outcome)
	}
	if cmds := f.sent(); len(cmds) != 1 || cmds[0] != "Stop Now" {
		t.Fatalf("commands sent to FPP = %v, want exactly one %q", cmds, "Stop Now")
	}
	if want := "Stopped what FPP was playing on player-01."; resp.Command.Reason != want {
		t.Fatalf("reason = %q, want %q", resp.Command.Reason, want)
	}
}

func TestNightPrepareSite_StopFppPlayback_SlowStopIsReportedAndPrepareSiteIsDone(t *testing.T) {
	f := setupPreparedFixture(t)

	resp := mustNightCommandBody(t, f.api, f.token, "prepare-site", `{"stopFppPlayback":true}`)

	if resp.Command.Outcome != "applied" || resp.Session.State != nightStatePreparing {
		t.Fatalf("outcome = %q, session state = %q, want applied and preparing", resp.Command.Outcome, resp.Session.State)
	}
	if want := `FPP "player-01" was told to stop but has not reported that it is idle.` + nightStopByHand; resp.Command.Reason != want {
		t.Fatalf("reason = %q, want %q", resp.Command.Reason, want)
	}
}

func TestNightPrepareSite_StopFppPlayback_NeverStopsFPPForAnAlreadyOpenSession(t *testing.T) {
	for _, state := range []string{nightStatePreparing, nightStatePreshow, nightStateTransitionToShow, nightStateLive, nightStateTransitionToResting, nightStateRestingIntershow, nightStateEndOfNightResting} {
		t.Run(state, func(t *testing.T) {
			f := setupPreparedFixture(t)
			f.reportIdle()
			rec := store.NightSessionRecord{
				ID: "sess-open", ConfigObjectID: "halloween-main", ConfigRevision: 1,
				State: state, StateEnteredAt: testNow, Cycle: 1,
			}
			if err := f.st.CreateNightSession(context.Background(), rec, testNow); err != nil {
				t.Fatalf("create night session: %v", err)
			}

			resp := mustNightCommandBody(t, f.api, f.token, "prepare-site", `{"stopFppPlayback":true}`)

			if resp.Command.Outcome != "idempotent_no_op" {
				t.Fatalf("outcome = %q, want idempotent_no_op for state %s", resp.Command.Outcome, state)
			}
			if cmds := f.sent(); len(cmds) != 0 {
				t.Fatalf("commands sent to FPP = %v, want none against an open %s session", cmds, state)
			}
			if resp.Command.Reason != nightPrepareSiteAlreadyPreparedReason {
				t.Fatalf("reason = %q, want %q", resp.Command.Reason, nightPrepareSiteAlreadyPreparedReason)
			}
		})
	}
}

func TestNightPrepareSite_StopFppPlayback_FlagValues(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantReason bool
	}{
		{"absent", `{}`, false},
		{"false", `{"stopFppPlayback":false}`, false},
		{"true", `{"stopFppPlayback":true}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, _, token, _, _ := setupNightControlFixture(t, time.Hour)

			resp := mustNightCommandBody(t, api, token, "prepare-site", tc.body)

			if resp.Command.Outcome != "applied" {
				t.Fatalf("outcome = %q, want applied: a stop FPP cannot perform must not fail prepare-site", resp.Command.Outcome)
			}
			if (resp.Command.Reason != "") != tc.wantReason {
				t.Fatalf("reason = %q, want a reason: %v", resp.Command.Reason, tc.wantReason)
			}
		})
	}
}

func TestNightStopFppPlayback_IsIgnoredByEveryOtherCommand(t *testing.T) {
	api, _, token, _, _ := setupNightControlFixture(t, time.Hour)
	mustNightCommand(t, api, token, "prepare-site")

	resp := mustNightCommandBody(t, api, token, "end-session", `{"stopFppPlayback":true}`)

	if resp.Command.Reason != "" {
		t.Fatalf("end-session reason = %q, want none: only prepare-site stops FPP", resp.Command.Reason)
	}
}

func mustNightCommandBody(t *testing.T, api *API, token, command, body string) v1.NightCommandResponse {
	t.Helper()
	resp, raw := nightCommandRawBody(t, api, token, command, body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST night/commands/%s: status = %d, want 202; body: %s", command, resp.StatusCode, raw)
	}
	var out v1.NightCommandResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode night command response: %v; body: %s", err, raw)
	}
	return out
}
