package weathertrigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// NWSSource is the fixed source name the built-in poller reports, and the
// only trigger source name this package reserves.
const NWSSource = "nws"

// nwsDefaultBaseURL is the real API; a test overrides BaseURL to point at a
// local fake server so no test ever calls the real service.
const nwsDefaultBaseURL = "https://api.weather.gov"

// nwsHTTPTimeout and nwsMaxResponseBytes bound one poll: a slow or huge
// response never blocks the poller past this request, and a compromised or
// misbehaving endpoint cannot exhaust memory.
const (
	nwsHTTPTimeout      = 10 * time.Second
	nwsMaxResponseBytes = 1 << 20 // 1 MiB: generous for an alerts list, far under anything pathological.
)

// NWSAlertsResponse is the https://api.weather.gov/alerts/active response
// shape, narrowed to exactly the fields ADR-053 decision 12 permits this
// package to read: an alert's id, event type, severity, and expiry. A
// field is deliberately absent from this type, not merely unused, for
// every other property the real API returns (headline, description,
// instruction, area description, and more): decoding into this struct
// cannot retain what it has no field for.
type NWSAlertsResponse struct {
	Features []NWSAlertFeature `json:"features"`
}

// NWSAlertFeature is one alert. ID is the feature's own id, stable for the
// life of that alert, which is what lets the same alert ask once.
type NWSAlertFeature struct {
	ID         string             `json:"id"`
	Properties NWSAlertProperties `json:"properties"`
}

// NWSAlertProperties is deliberately narrow: see [NWSAlertsResponse].
type NWSAlertProperties struct {
	Event    string `json:"event"`
	Severity string `json:"severity"`
	Expires  string `json:"expires"`
}

// NWSPollerConfig is the built-in poller's configuration, decoded from
// show.weatherdelay's triggers.nws (internal/coordinator/config).
type NWSPollerConfig struct {
	Latitude    float64
	Longitude   float64
	Contact     string
	PollSeconds int
	EventTypes  []string
}

// NWSHealth is one poller's reported health for GET /weather-delay.
type NWSHealth struct {
	LastError     string
	LastErrorAt   time.Time
	LastSuccessAt time.Time
}

// NWSPoller polls api.weather.gov/alerts/active on an interval and reports
// each new matching alert exactly once. A poll failure never panics and
// never blocks anything else; it is recorded and read back through
// [NWSPoller.Health].
type NWSPoller struct {
	cfg     NWSPollerConfig
	onAlert func(TriggerEvent) error
	now     func() time.Time
	logger  *slog.Logger
	client  *http.Client
	baseURL string

	mu      sync.Mutex
	seen    map[string]time.Time // alert id -> its own expiry, for pruning
	lastErr string
	lastErrAt,
	lastOKAt time.Time
}

// NewNWSPoller builds a poller. onAlert is called once per new alert id
// that matches cfg.EventTypes; its error only prevents that alert from
// being marked seen; it does not fail the poll.
func NewNWSPoller(cfg NWSPollerConfig, onAlert func(TriggerEvent) error, now func() time.Time, logger *slog.Logger) *NWSPoller {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &NWSPoller{
		cfg: cfg, onAlert: onAlert, now: now, logger: logger,
		client:  &http.Client{Timeout: nwsHTTPTimeout, CheckRedirect: nwsRefuseCrossHostRedirect},
		baseURL: nwsDefaultBaseURL,
		seen:    map[string]time.Time{},
	}
}

// nwsRefuseCrossHostRedirect follows a same-host redirect (the real API can
// use one) and refuses any redirect to a different host, so a compromised
// or misconfigured endpoint cannot redirect this request elsewhere.
func nwsRefuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("weathertrigger: refusing a redirect from %s to a different host %s", via[0].URL.Host, req.URL.Host)
	}
	if len(via) >= 5 {
		return errors.New("weathertrigger: too many redirects")
	}
	return nil
}

// WithBaseURL overrides the API base URL, for a test's local fake server. It
// returns p so a caller can chain it onto [NewNWSPoller].
func (p *NWSPoller) WithBaseURL(baseURL string) *NWSPoller {
	p.baseURL = baseURL
	return p
}

// Health reports this poller's last error and last success, for
// GET /weather-delay's source health.
func (p *NWSPoller) Health() NWSHealth {
	p.mu.Lock()
	defer p.mu.Unlock()
	return NWSHealth{LastError: p.lastErr, LastErrorAt: p.lastErrAt, LastSuccessAt: p.lastOKAt}
}

// Run polls immediately, then on cfg.PollSeconds, until ctx is done.
func (p *NWSPoller) Run(ctx context.Context) {
	p.Poll(ctx)
	interval := time.Duration(p.cfg.PollSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Poll(ctx)
		}
	}
}

// Poll runs one fetch-and-report cycle. It never returns an error: a
// failure at any step is recorded on Health and logged, and every existing
// piece of state (seen alert ids, other sources) is left exactly as it was.
func (p *NWSPoller) Poll(ctx context.Context) {
	now := p.now()
	resp, err := p.fetch(ctx)
	if err != nil {
		p.recordFailure(now, err)
		return
	}
	p.recordSuccess(now)
	p.reportNewAlerts(now, resp)
}

func (p *NWSPoller) recordFailure(now time.Time, err error) {
	p.logger.Warn("weather delay: NWS poll failed; the last known state is unchanged", "error", err)
	p.mu.Lock()
	p.lastErr, p.lastErrAt = err.Error(), now
	p.mu.Unlock()
}

func (p *NWSPoller) recordSuccess(now time.Time) {
	p.mu.Lock()
	p.lastOKAt = now
	p.mu.Unlock()
}

func (p *NWSPoller) fetch(ctx context.Context) (NWSAlertsResponse, error) {
	url := fmt.Sprintf("%s/alerts/active?point=%g,%g", p.baseURL, p.cfg.Latitude, p.cfg.Longitude)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return NWSAlertsResponse{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/geo+json")
	req.Header.Set("User-Agent", "ShowMesh weather delay trigger ("+p.cfg.Contact+")")

	resp, err := p.client.Do(req)
	if err != nil {
		return NWSAlertsResponse{}, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, nwsMaxResponseBytes+1))
	if err != nil {
		return NWSAlertsResponse{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) > nwsMaxResponseBytes {
		return NWSAlertsResponse{}, fmt.Errorf("response exceeded %d bytes", nwsMaxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return NWSAlertsResponse{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	var out NWSAlertsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return NWSAlertsResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// reportNewAlerts prunes expired ids from seen, then calls onAlert once for
// every feature whose event type is configured and whose id has not
// already asked.
func (p *NWSPoller) reportNewAlerts(now time.Time, resp NWSAlertsResponse) {
	wanted := make(map[string]bool, len(p.cfg.EventTypes))
	for _, et := range p.cfg.EventTypes {
		wanted[et] = true
	}

	p.mu.Lock()
	for id, expires := range p.seen {
		if now.After(expires) {
			delete(p.seen, id)
		}
	}
	p.mu.Unlock()

	for _, f := range resp.Features {
		if f.ID == "" || !wanted[f.Properties.Event] {
			continue
		}
		p.mu.Lock()
		_, already := p.seen[f.ID]
		p.mu.Unlock()
		if already {
			continue
		}

		var expiresAt *time.Time
		if f.Properties.Expires != "" {
			if t, err := time.Parse(time.RFC3339, f.Properties.Expires); err == nil {
				expiresAt = &t
			} else {
				p.logger.Warn("weather delay: NWS alert had an unparsable expires field; treating it as unknown", "alert_id", f.ID, "error", err)
			}
		}
		expiryForPrune := now.Add(24 * time.Hour)
		if expiresAt != nil {
			expiryForPrune = *expiresAt
		}

		ev := TriggerEvent{
			Source: NWSSource, Kind: KindWarning, EventType: f.Properties.Event,
			Severity: f.Properties.Severity, ExpiresAt: expiresAt, ExternalID: f.ID,
		}
		if err := p.onAlert(ev); err != nil {
			p.logger.Warn("weather delay: NWS alert could not be turned into a trigger; it will be retried on the next poll", "alert_id", f.ID, "error", err)
			continue
		}
		p.mu.Lock()
		p.seen[f.ID] = expiryForPrune
		p.mu.Unlock()
	}
}
