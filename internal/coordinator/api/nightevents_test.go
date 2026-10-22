package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func nightEventsByCategory(t *testing.T, st *store.Store, category string) []store.EventRecord {
	t.Helper()
	all, _, err := st.ListEvents(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []store.EventRecord
	for _, ev := range all {
		if ev.Category == category {
			out = append(out, ev)
		}
	}
	return out
}

func eventDetails(t *testing.T, ev store.EventRecord) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(ev.Details, &d); err != nil {
		t.Fatalf("decode event details %q: %v", ev.Details, err)
	}
	return d
}

func TestNightEvents_RefusedStartNightRecordsOneEventWithTheOperatorsReason(t *testing.T) {
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)
	prep := mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "start-preshow")

	_, p := nightCommandProblem(t, api, token, "start-night")

	refused := nightEventsByCategory(t, st, eventCategoryNightCommandRefused)
	if len(refused) != 1 {
		t.Fatalf("refused events = %d, want exactly 1", len(refused))
	}
	ev := refused[0]
	if ev.Severity != "warning" || ev.Summary != p.Detail {
		t.Fatalf("refused event severity=%q summary=%q, want warning and the operator's reason %q", ev.Severity, ev.Summary, p.Detail)
	}
	d := eventDetails(t, ev)
	if d["command"] != "start-night" || d["sessionId"] != prep.Session.ID || d["reason"] != p.Detail {
		t.Fatalf("refused event details = %v, want command start-night, session %s, reason %q", d, prep.Session.ID, p.Detail)
	}
}

func TestNightEvents_RefusedCommandWithNoSessionHasAnEmptySessionID(t *testing.T) {
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)

	nightCommandProblem(t, api, token, "run-readiness")

	refused := nightEventsByCategory(t, st, eventCategoryNightCommandRefused)
	if len(refused) != 1 {
		t.Fatalf("refused events = %d, want 1", len(refused))
	}
	if d := eventDetails(t, refused[0]); d["command"] != "run-readiness" || d["sessionId"] != "" {
		t.Fatalf("refused event details = %v, want command run-readiness and an empty sessionId", d)
	}
}

func TestNightEvents_AppliedCommandRecordsAnEvent(t *testing.T) {
	api, st, token, _, _ := setupNightControlFixture(t, time.Hour)

	out := mustNightCommand(t, api, token, "prepare-site")

	applied := nightEventsByCategory(t, st, eventCategoryNightCommandApplied)
	if len(applied) != 1 {
		t.Fatalf("applied events = %d, want 1", len(applied))
	}
	if applied[0].Severity != "informational" {
		t.Fatalf("applied severity = %q, want informational", applied[0].Severity)
	}
	if d := eventDetails(t, applied[0]); d["command"] != "prepare-site" || d["sessionId"] != out.Session.ID || d["outcome"] != "applied" {
		t.Fatalf("applied details = %v, want prepare-site, session %s, outcome applied", d, out.Session.ID)
	}
	if got := len(nightEventsByCategory(t, st, eventCategoryNightCommandRefused)); got != 0 {
		t.Fatalf("refused events after an applied command = %d, want 0", got)
	}
}

func TestNightEvents_ReadinessNamesTheFailingChecks(t *testing.T) {
	api, st, token, _, obs := setupNightControlFixture(t, time.Hour)
	observedAt := testNow
	obs.obs = []observation.Observation{{
		Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
		Signal:   "fpp.reachable", Value: false, ObservedAt: &observedAt, CollectedAt: testNow,
		Source: "fpp-rest", Quality: observation.QualityDirect, ValidFor: time.Minute,
	}}
	prep := mustNightCommand(t, api, token, "prepare-site")
	mustNightCommand(t, api, token, "run-readiness")

	done := nightEventsByCategory(t, st, eventCategoryNightReadinessComplete)
	if len(done) != 1 {
		t.Fatalf("readiness events = %d, want 1", len(done))
	}
	if done[0].Severity != "warning" {
		t.Fatalf("readiness severity = %q, want warning for a not ready result", done[0].Severity)
	}
	d := eventDetails(t, done[0])
	failed, _ := d["failedChecks"].([]any)
	if d["outcome"] != "not_ready" || d["sessionId"] != prep.Session.ID || !containsAny(failed, "fpp:player-01:reachable") {
		t.Fatalf("readiness details = %v, want not_ready, session %s and fpp:player-01:reachable among the failed checks", d, prep.Session.ID)
	}
}

func TestNightEvents_ReadyReadinessIsInformationalAndAutomaticRerunIsRecorded(t *testing.T) {
	api, st, token, advance, obs := setupNightControlFixture(t, time.Minute)
	runToPreshow(t, api, token, obs, testNow)
	if got := len(nightEventsByCategory(t, st, eventCategoryNightReadinessComplete)); got != 1 {
		t.Fatalf("readiness events after the first run = %d, want 1", got)
	}

	advance(2 * time.Minute)
	setHealthyFPPReachable(obs, testNow.Add(2*time.Minute))
	mustNightCommand(t, api, token, "start-night")

	done := nightEventsByCategory(t, st, eventCategoryNightReadinessComplete)
	if len(done) != 2 {
		t.Fatalf("readiness events after start-night's rerun = %d, want 2", len(done))
	}
	for _, ev := range done {
		if ev.Severity != "informational" || eventDetails(t, ev)["outcome"] != "ready" {
			t.Fatalf("readiness event severity=%q details=%v, want informational and ready", ev.Severity, eventDetails(t, ev))
		}
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
