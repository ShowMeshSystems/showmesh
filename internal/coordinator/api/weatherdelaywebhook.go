package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// The optional show.weatherdelay.notify webhook (ADR-053 decision 13): a
// small JSON document on delay started, changed to cancel night, cancel
// night started, resumed or cleared. Best effort by construction: it never
// blocks or fails the operator's own request, and a failure only shows up
// as GET /weather-delay's own lastNotifyError.

// weatherDelayWebhookTimeout bounds the whole webhook POST.
const weatherDelayWebhookTimeout = 3 * time.Second

// weatherDelayWebhookMaxBodyBytes bounds how much of a webhook's own
// response this coordinator reads before discarding it; the body's
// content is never used for anything.
const weatherDelayWebhookMaxBodyBytes = 4096

// weatherDelayWebhookClient never follows a redirect: the URL is operator
// configuration, and a redirect could send the notification somewhere the
// operator never approved.
var weatherDelayWebhookClient = &http.Client{
	Timeout:       weatherDelayWebhookTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// weatherDelayNotifyBackground counts webhook deliveries still in flight,
// so a test can wait for one to finish rather than sleeping.
var weatherDelayNotifyBackground sync.WaitGroup

// weatherDelayNotifyPayload is the small JSON document every webhook call
// carries, exactly the fields ADR-053 decision 13 names.
type weatherDelayNotifyPayload struct {
	Event     string `json:"event"`
	Kind      string `json:"kind"`
	Active    bool   `json:"active"`
	StartedAt string `json:"startedAt,omitempty"`
	StartedBy string `json:"startedBy,omitempty"`
	ChangedAt string `json:"changedAt"`
}

// weatherDelayNotify fires the configured webhook, if any, in the
// background: it never blocks or fails the caller's own request. event is
// one of "delay_started", "changed_to_cancel_night", "cancel_night_started",
// "resumed" or "cleared".
func (h *handlers) weatherDelayNotify(ctx context.Context, event, kind string, active bool, startedAt time.Time, startedBy string, changedAt time.Time) {
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay notify: failed to resolve show.weatherdelay config; no webhook could be checked", "error", err)
		return
	}
	webhookURL := payload.Notify.WebhookURL
	if webhookURL == "" {
		return
	}

	body := weatherDelayNotifyPayload{Event: event, Kind: kind, Active: active, ChangedAt: formatTime(changedAt)}
	if !startedAt.IsZero() {
		body.StartedAt = formatTime(startedAt)
	}
	body.StartedBy = startedBy

	weatherDelayNotifyBackground.Add(1)
	go func() {
		defer weatherDelayNotifyBackground.Done()
		reason := h.weatherDelayPostNotify(webhookURL, body)
		h.deps.WeatherDelayGateCache.setNotifyError(reason)
		if reason != "" {
			h.logWarn("weather delay notify: webhook delivery failed", "event", event, "error", reason)
		}
	}()
}

// weatherDelayPostNotify POSTs body to webhookURL, detached from the
// triggering request's own context so a caller hanging up cannot cancel
// it. Returns "" on a 2xx response, else a reason for lastNotifyError.
func (h *handlers) weatherDelayPostNotify(webhookURL string, body weatherDelayNotifyPayload) string {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("failed to encode the notification: %v", err)
	}
	reqCtx, cancel := context.WithTimeout(context.Background(), weatherDelayWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Sprintf("failed to build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := weatherDelayWebhookClient.Do(req)
	if err != nil {
		return fmt.Sprintf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, weatherDelayWebhookMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("webhook answered with status %d", resp.StatusCode)
	}
	return ""
}
