package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/weathertrigger"
)

// The optional show.weatherdelay.notify webhook. Best effort: it never
// blocks or fails the operator's request; a failure shows only as
// lastNotifyError on GET /api/v1/weather-delay.

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

// weatherDelayNotifyBody is anything the webhook queue can deliver: the
// state-change payload below and, for a trigger, [weatherDelayDecisionNotifyPayload].
// eventName is used only for a dropped-queue log line.
type weatherDelayNotifyBody interface {
	eventName() string
}

// weatherDelayNotifyPayload is the small JSON document every start, cancel
// night, or resume webhook call carries, exactly the fields ADR-053
// decision 13 names.
type weatherDelayNotifyPayload struct {
	Event     string `json:"event"`
	Kind      string `json:"kind"`
	Active    bool   `json:"active"`
	StartedAt string `json:"startedAt,omitempty"`
	StartedBy string `json:"startedBy,omitempty"`
	ChangedAt string `json:"changedAt"`
}

func (p weatherDelayNotifyPayload) eventName() string { return p.Event }

// weatherDelayNotify queues the configured webhook call, if any, for the
// delivery worker. event is delay_started, changed_to_cancel_night,
// cancel_night_started, resumed or cleared.
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
	h.deps.WeatherDelayNotifier.enqueue(weatherDelayNotifyItem{url: webhookURL, body: body})
}

// weatherDelayDecisionNotifyPayload is the "decisionNeeded"/"decisionResolved"
// webhook payload (ADR-053 decision 12): exactly the fields the task
// names, and nothing from the warning's own text. This is how an outside
// system puts the question on a phone; ShowMesh itself sends no messages
// to anyone.
type weatherDelayDecisionNotifyPayload struct {
	Event         string `json:"event"`
	ID            string `json:"id"`
	Source        string `json:"source"`
	Reason        string `json:"reason,omitempty"`
	Question      string `json:"question,omitempty"`
	DefaultAction string `json:"defaultAction,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
}

func (p weatherDelayDecisionNotifyPayload) eventName() string { return p.Event }

// weatherDelayNotifyDecision queues the configured webhook call, if any,
// for event ("decisionNeeded" or "decisionResolved") about pd.
func (h *handlers) weatherDelayNotifyDecision(ctx context.Context, event string, pd weathertrigger.PendingDecision) {
	payload, _, _, _, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.logWarn("weather delay notify: failed to resolve show.weatherdelay config; no webhook could be checked", "error", err)
		return
	}
	webhookURL := payload.Notify.WebhookURL
	if webhookURL == "" {
		return
	}
	body := weatherDelayDecisionNotifyPayload{
		Event: event, ID: pd.ID, Source: pd.Source, Reason: pd.Reason,
		Question: pd.Question, DefaultAction: pd.DefaultAction, Deadline: formatTime(pd.Deadline),
	}
	h.deps.WeatherDelayNotifier.enqueue(weatherDelayNotifyItem{url: webhookURL, body: body})
}

// weatherDelayNotifyQueueSize bounds the events waiting for the webhook.
const weatherDelayNotifyQueueSize = 64

// weatherDelayNotifyDroppedMessage is lastNotifyError after a full queue
// dropped its oldest event.
const weatherDelayNotifyDroppedMessage = "A weather notification was dropped because too many were waiting to be sent. Check that the webhook receiver is answering."

type weatherDelayNotifyItem struct {
	url  string
	body weatherDelayNotifyBody
}

// WeatherDelayNotifier delivers webhook events one at a time, in the order
// they were queued. Enqueue never blocks; a full queue drops its oldest.
type WeatherDelayNotifier struct {
	cache  *WeatherDelayGateCache
	logger *slog.Logger

	mu      sync.Mutex
	idle    *sync.Cond
	queue   []weatherDelayNotifyItem
	pending int
	wake    chan struct{}
}

// NewWeatherDelayNotifier builds a notifier recording failures in cache.
// Nothing is delivered until Run is started.
func NewWeatherDelayNotifier(cache *WeatherDelayGateCache, logger *slog.Logger) *WeatherDelayNotifier {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	n := &WeatherDelayNotifier{cache: cache, logger: logger, wake: make(chan struct{}, 1)}
	n.idle = sync.NewCond(&n.mu)
	return n
}

func (n *WeatherDelayNotifier) enqueue(item weatherDelayNotifyItem) {
	n.mu.Lock()
	if len(n.queue) >= weatherDelayNotifyQueueSize {
		dropped := n.queue[0]
		n.queue = n.queue[1:]
		n.pending--
		n.cache.setNotifyError(weatherDelayNotifyDroppedMessage)
		n.logger.Warn("weather delay notify: queue full; dropped the oldest event", "event", dropped.body.eventName())
	}
	n.queue = append(n.queue, item)
	n.pending++
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run delivers queued events until ctx ends. Events still queued then are
// abandoned.
func (n *WeatherDelayNotifier) Run(ctx context.Context) {
	for {
		n.mu.Lock()
		if len(n.queue) == 0 {
			n.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-n.wake:
			}
			continue
		}
		item := n.queue[0]
		n.queue = n.queue[1:]
		n.mu.Unlock()

		n.deliver(ctx, item)

		n.mu.Lock()
		n.pending--
		n.idle.Broadcast()
		n.mu.Unlock()
	}
}

func (n *WeatherDelayNotifier) deliver(ctx context.Context, item weatherDelayNotifyItem) {
	defer func() {
		if r := recover(); r != nil {
			n.logger.Warn("weather delay notify: webhook delivery panicked; recovered", "panic", fmt.Sprintf("%v", r))
		}
	}()
	reason := weatherDelayPostNotify(ctx, item.url, item.body)
	n.cache.setNotifyError(reason)
	if reason != "" {
		n.logger.Warn("weather delay notify: webhook delivery failed", "event", item.body.eventName(), "error", reason)
	}
}

// waitIdle blocks until every queued event was delivered or dropped.
func (n *WeatherDelayNotifier) waitIdle() {
	n.mu.Lock()
	for n.pending > 0 {
		n.idle.Wait()
	}
	n.mu.Unlock()
}

// weatherDelayPostNotify POSTs body to webhookURL under ctx, never the
// triggering request's context. Returns "" on a 2xx response, else a reason
// for lastNotifyError.
func weatherDelayPostNotify(ctx context.Context, webhookURL string, body weatherDelayNotifyBody) string {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("failed to encode the notification: %v", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, weatherDelayWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Sprintf("failed to build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := weatherDelayWebhookClient.Do(req)
	if err != nil {
		// url.Error carries the full URL, which can hold a token; report only the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Sprintf("The weather notification could not be sent: %v. Check the webhook address.", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, weatherDelayWebhookMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("The webhook answered with status %d. Check the webhook address.", resp.StatusCode)
	}
	return ""
}
