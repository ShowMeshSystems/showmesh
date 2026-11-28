package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const (
	nightShowMismatchBeforePrepare = "Night session Halloween main loop plays Halloween 2026, not the selected show Christmas 2026. Choose a night session for Christmas 2026, then run Prepare site."
	nightShowMismatchOpenNight     = "Night session Halloween main loop plays Halloween 2026, not the selected show Christmas 2026. Choose one for Christmas 2026, then End session and Prepare site."
	nightShowMismatchPinned        = "This night runs Halloween main loop for Halloween 2026, not the selected show Christmas 2026. Run End session, then Prepare site."
)

// nightShowMatchFixture is the night control fixture with named Halloween and
// Christmas shows, Halloween selected, and christmasSessions night sessions
// that play Christmas.
func nightShowMatchFixture(t *testing.T, christmasSessions ...string) (api *API, st *store.Store, opTok, adminTok string) {
	t.Helper()
	api, st, opTok, adminTok, _, obs := setupNightControlFixtureWithBody(t, time.Hour, validNightSessionBody)
	setHealthyFPPReachable(obs, testNow)
	mustPutShow(t, api, adminTok, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, adminTok, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustCreateNightSessionAsset(t, st, "christmas-2026", "resting-loop", "player-01")
	mustPutShowAction(t, api, adminTok, "xmas-lighting-fade-out", strings.ReplaceAll(validShowActionFPPBody, "halloween-2026", "christmas-2026"))
	for i, id := range christmasSessions {
		body := strings.ReplaceAll(validNightSessionBody, "halloween-2026", "christmas-2026")
		body = strings.Replace(body, `"action": "lighting-fade-out"`, `"action": "xmas-lighting-fade-out"`, 1)
		label := []string{"Xmas main", "Xmas backup"}[i]
		body = strings.Replace(body, `"Halloween main loop"`, `"`+label+`"`, 1)
		mustPutNightSession(t, api, adminTok, id, body)
	}
	mustPutShowActive(t, api, adminTok, "halloween-2026")
	return api, st, opTok, adminTok
}

func nightShowMatchCheck(t *testing.T, api *API) v1.NightReadinessCheck {
	t.Helper()
	for _, c := range mustGetNightSession(t, api).Session.Readiness.Checks {
		if c.Name == nightShowMatchCheckName {
			return c
		}
	}
	t.Fatalf("readiness has no %s check", nightShowMatchCheckName)
	return v1.NightReadinessCheck{}
}

func nightCommandProblemBody(t *testing.T, api *API, token, command, body string) v1.Problem {
	t.Helper()
	resp, raw := nightCommandRawBody(t, api, token, command, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST night/commands/%s: status = %d, want 409; body: %s", command, resp.StatusCode, raw)
	}
	var p v1.Problem
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode problem: %v; body: %s", err, raw)
	}
	return p
}

func TestNightShowMatch_MatchingShowPasses(t *testing.T) {
	api, _, opTok, _ := nightShowMatchFixture(t)
	mustNightCommand(t, api, opTok, "prepare-site")
	mustNightCommand(t, api, opTok, "run-readiness")
	check := nightShowMatchCheck(t, api)
	if check.State != "healthy" || check.Label != "Night session for the selected show" || check.NightSessionCandidate != nil {
		t.Fatalf("check = %+v, want healthy with its label and no candidate", check)
	}
}

func TestNightShowMatch_PrepareSiteRefusesMismatchEvenWithOverrides(t *testing.T) {
	api, _, opTok, adminTok := nightShowMatchFixture(t, "xmas-main-2026")
	mustPutShowActive(t, api, adminTok, "christmas-2026")

	p := nightCommandProblemBody(t, api, opTok, "prepare-site", `{"interlockOverrides":[{"rule":"any-rule","reason":"operator says go"}]}`)
	if p.Detail != nightShowMismatchBeforePrepare {
		t.Fatalf("detail = %q, want %q", p.Detail, nightShowMismatchBeforePrepare)
	}
	if mustGetNightSession(t, api).Session.ID != "" {
		t.Fatal("a refused prepare-site created a night")
	}

	mustActivateNightSession(t, api, adminTok, "xmas-main-2026")
	mustNightCommand(t, api, opTok, "prepare-site")
	mustNightCommand(t, api, opTok, "run-readiness")
	if check := nightShowMatchCheck(t, api); check.State != "healthy" {
		t.Fatalf("after activating the Christmas session, check = %+v, want healthy", check)
	}
}

func TestNightShowMatch_ReadinessReportsOneCandidateOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions []string
		want     *v1.NightSessionCandidate
	}{
		{"zero", nil, nil},
		{"one", []string{"xmas-main-2026"}, &v1.NightSessionCandidate{ID: "xmas-main-2026", Label: "Xmas main"}},
		{"two", []string{"xmas-main-2026", "xmas-backup-2026"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _, opTok, adminTok := nightShowMatchFixture(t, tc.sessions...)
			mustNightCommand(t, api, opTok, "prepare-site")
			mustPutShowActive(t, api, adminTok, "christmas-2026")
			mustNightCommand(t, api, opTok, "run-readiness")

			check := nightShowMatchCheck(t, api)
			if check.State != "failed" || len(check.Reasons) != 1 || check.Reasons[0] != nightShowMismatchOpenNight {
				t.Fatalf("check = %+v, want failed with %q", check, nightShowMismatchOpenNight)
			}
			if (check.NightSessionCandidate == nil) != (tc.want == nil) || (tc.want != nil && *check.NightSessionCandidate != *tc.want) {
				t.Fatalf("candidate = %+v, want %+v", check.NightSessionCandidate, tc.want)
			}
		})
	}
}

func TestNightShowMatch_ActivatingTheMatchStillNeedsANewPrepare(t *testing.T) {
	api, _, opTok, adminTok := nightShowMatchFixture(t, "xmas-main-2026")
	mustNightCommand(t, api, opTok, "prepare-site")
	mustPutShowActive(t, api, adminTok, "christmas-2026")
	mustActivateNightSession(t, api, adminTok, "xmas-main-2026")
	mustNightCommand(t, api, opTok, "run-readiness")

	check := nightShowMatchCheck(t, api)
	if check.State != "failed" || check.Reasons[0] != nightShowMismatchPinned || check.NightSessionCandidate != nil {
		t.Fatalf("check = %+v, want failed with %q and no candidate", check, nightShowMismatchPinned)
	}
	if p := nightCommandProblemBody(t, api, opTok, "start-preshow", `{}`); p.Detail != nightShowMismatchPinned {
		t.Fatalf("start-preshow detail = %q, want %q", p.Detail, nightShowMismatchPinned)
	}

	mustNightCommand(t, api, opTok, "end-session")
	mustNightCommand(t, api, opTok, "prepare-site")
	mustNightCommand(t, api, opTok, "run-readiness")
	if check := nightShowMatchCheck(t, api); check.State != "healthy" {
		t.Fatalf("after End session and Prepare site, check = %+v, want healthy", check)
	}
}

func TestNightShowMatch_StartNightRefusesAfterTheSelectedShowChanges(t *testing.T) {
	api, _, opTok, adminTok := nightShowMatchFixture(t)
	mustNightCommand(t, api, opTok, "prepare-site")
	mustNightCommand(t, api, opTok, "run-readiness")
	mustNightCommand(t, api, opTok, "start-preshow")
	mustPutShowActive(t, api, adminTok, "christmas-2026")

	p := nightCommandProblemBody(t, api, opTok, "start-night", `{"interlockOverrides":[{"rule":"any-rule","reason":"operator says go"}]}`)
	if p.Detail != nightShowMismatchPinned {
		t.Fatalf("start-night detail = %q, want %q", p.Detail, nightShowMismatchPinned)
	}
	if got := mustGetNightSession(t, api).Session.State; got != nightStatePreshow {
		t.Fatalf("state = %q, want %q", got, nightStatePreshow)
	}
}
