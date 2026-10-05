package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
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

func TestNightStopFPPAtPrepareSite_ReportsAStopFPPRefuses(t *testing.T) {
	now := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	f := newNightShutdownFixture(t, &now, nightShutdownPayload(), preparingSession(now))
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(refusing.Close)
	f.h.deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: refusing.URL}}}

	got := f.h.nightStopFPPAtPrepareSite(context.Background(), now, identity.AuditEntry{PrincipalID: "op"}, "key-1", mustGetCurrentSession(t, f.store))

	if !strings.Contains(got, "was not stopped") && !strings.Contains(got, "refused") {
		t.Fatalf("result = %q, want a report that FPP was not stopped", got)
	}
	if strings.Contains(got, "Stopped what FPP was playing") {
		t.Fatalf("result = %q claims a stop FPP refused", got)
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
