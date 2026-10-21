package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const (
	eventCategoryNightCommandRefused    = "night.command.refused"
	eventCategoryNightCommandApplied    = "night.command.applied"
	eventCategoryNightReadinessComplete = "night.readiness.completed"
)

func nightEventRecord(now time.Time, category, severity, summary string, details map[string]any) store.EventRecord {
	raw, err := json.Marshal(details)
	if err != nil {
		raw = nil
	}
	return store.EventRecord{
		Source:   "night",
		Resource: observation.ResourceRef{Kind: observation.ResourceCoordinator, ID: "night"},
		Category: category, Severity: severity, Summary: summary,
		Details: raw, OccurredAt: &now,
	}
}

// appendNightEvent records a night event after the fact; a failure is logged and never changes the response.
func (h *handlers) appendNightEvent(ctx context.Context, ev store.EventRecord) {
	if h.deps.WeatherDelayEvents == nil {
		return
	}
	if _, err := h.deps.WeatherDelayEvents.AppendEvent(ctx, ev); err != nil {
		h.logWarn("night events: failed to append event", "category", ev.Category, "error", err)
	}
}

// currentNightSessionID is the session a command acted on, or "" when none exists or the read fails.
func (h *handlers) currentNightSessionID(ctx context.Context) string {
	cur, ok, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil || !ok {
		return ""
	}
	return cur.ID
}

// refuseNightCommand records the refusal as an event, then answers the caller with it.
func (h *handlers) refuseNightCommand(w http.ResponseWriter, ctx context.Context, now time.Time, cmd string, p v1.Problem) {
	h.appendNightEvent(ctx, nightEventRecord(now, eventCategoryNightCommandRefused, "warning", p.Detail, map[string]any{
		"command": cmd, "sessionId": h.currentNightSessionID(ctx), "reason": p.Detail,
	}))
	writeProblem(w, h.logger, now, p)
}

func (h *handlers) appendNightCommandApplied(ctx context.Context, now time.Time, cmd, sessionID, outcome string) {
	summary := inWords(cmd) + " was applied."
	if outcome != nightOutcomeApplied {
		summary = inWords(cmd) + " needed no change."
	}
	h.appendNightEvent(ctx, nightEventRecord(now, eventCategoryNightCommandApplied, "informational", summary, map[string]any{
		"command": cmd, "sessionId": sessionID, "outcome": outcome,
	}))
}

// nightReadinessEventRecord is the event for a readiness run; failed names the checks that did not pass.
func nightReadinessEventRecord(now time.Time, sessionID, outcome string, failed []string) store.EventRecord {
	severity, summary := "informational", "Readiness finished and the site is ready."
	if outcome != "ready" {
		severity, summary = "warning", "Readiness finished and the site is not fully ready."
	}
	if failed == nil {
		failed = []string{}
	}
	return nightEventRecord(now, eventCategoryNightReadinessComplete, severity, summary, map[string]any{
		"sessionId": sessionID, "outcome": outcome, "failedChecks": failed,
	})
}

// nightFailingCheckNames lists the checks that count toward the outcome and are not healthy.
func nightFailingCheckNames(checks []nightReadinessCheck) []string {
	names := []string{}
	for _, c := range checks {
		if c.health == nightCheckStateNotVerifiable || c.health == nightCheckStateNotConfigured || c.health == nightHealthHealthy() {
			continue
		}
		names = append(names, c.name)
	}
	return names
}
