package clock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// fppAES67Status is the shape of GET :32322/api/aes67/status's "ptp"
// object (RES-019 §1) — this package's own copy of the wire shape, not an
// import from FPP: this agent has no dependency on FPP's source, matching
// every other FPP-facing wire boundary in this codebase.
type fppAES67Status struct {
	PTP struct {
		Synced        bool   `json:"synced"`
		OffsetNs      int64  `json:"offsetNs"`
		GrandmasterID string `json:"grandmasterId"`
		PortState     string `json:"portState"`
		IsGrandmaster bool   `json:"isGrandmaster"`
		Enabled       bool   `json:"enabled"`
		Domain        int    `json:"domain"`
		Role          string `json:"role"`
	} `json:"ptp"`
}

// FPPConfig configures [NewFPPProvider].
type FPPConfig struct {
	Interface string

	// BaseURL is the FPP 10 host's own base URL, e.g.
	// "http://fpp-host.local". Required.
	BaseURL string

	// StatusPaths are tried in order on every poll — RES-019 §1: "GET
	// :32322/api/aes67/status (or the PHP proxy GET
	// /api/pipewire/aes67/status)". Defaults to
	// [DefaultFPPStatusPaths] when empty.
	StatusPaths []string

	// UDSAddress is FPP's own ptp4l management socket, checked for
	// PRESENCE only (RES-019 §1: "plus the ptp4l UDS socket" — this
	// provider exposes PTP STATE, never PTP time, and never touches
	// FPP's ptp4l in any way). Optional: an empty value skips the check.
	UDSAddress string

	HTTPClient *http.Client
}

// DefaultFPPStatusPaths is [FPPConfig.StatusPaths]'s default: the native
// FPP 10 AES67 status endpoint first, the PHP proxy second.
var DefaultFPPStatusPaths = []string{
	":32322/api/aes67/status",
	"/api/pipewire/aes67/status",
}

const fppHTTPTimeout = 3 * time.Second

// FPPProvider observes an FPP 10 host's own AES67/PTP status over HTTP.
// It never restarts or reconfigures FPP's ptp4l (RES-019 §1: "Never
// restart or reconfigure FPP's ptp4l"), and it exposes PTP STATE only —
// [FPPProvider.Now] always reports MediaTime.Valid=false. PTP is not
// available on an FPP 10 host by default (no AES67 instance enabled, or
// every AES67 Apply momentarily tearing it down), so absence is this
// provider's normal, expected reading, never an error it retries into
// (RES-019 §1's own words).
type FPPProvider struct {
	cfg    FPPConfig
	client *http.Client
}

// NewFPPProvider builds an FPPProvider.
func NewFPPProvider(cfg FPPConfig) *FPPProvider {
	if len(cfg.StatusPaths) == 0 {
		cfg.StatusPaths = DefaultFPPStatusPaths
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: fppHTTPTimeout}
	}
	return &FPPProvider{cfg: cfg, client: client}
}

func (p *FPPProvider) Kind() ProviderKind { return ProviderFPP }
func (p *FPPProvider) Interface() string  { return p.cfg.Interface }
func (p *FPPProvider) Close() error       { return nil }

// Now always reports invalid: this provider exposes PTP state, never PTP
// time (RES-019 §1's own words) — a caller that needs the media clock on
// an FPP-observed node reads a locally-attached PHC directly via
// [ReadPHC], keyed off whatever this provider's own Poll reports as the
// interface, not through this provider.
func (p *FPPProvider) Now(context.Context) MediaTime {
	return MediaTime{Valid: false, Reason: "the FPP-observed provider exposes PTP state, never PTP time"}
}

// Poll fetches FPP's own AES67 status and translates its "ptp" object
// into a [RawStatus]. Absence — no AES67 instance enabled, or a request
// arriving mid-Apply while FPP's AES67 subsystem is momentarily torn down
// — is reported as Reachable=true, Locked=false with a stated reason, NOT
// Reachable=false: this is FPP's normal resting state, not an interface
// or link failure this node itself experienced (RES-019 §1: "absence is
// a normal state to report, not an error to retry into" — mapping it to
// StateFailed would misrepresent a quiet FPP host as a broken one).
func (p *FPPProvider) Poll(ctx context.Context) RawStatus {
	status, err := p.fetchStatus(ctx)
	if err != nil {
		return RawStatus{Reachable: true, Locked: false, Reason: err.Error(), Owner: "fpp"}
	}

	if !status.PTP.Enabled {
		return RawStatus{Reachable: true, Locked: false,
			Reason: "FPP reports PTP not enabled (no AES67 instance enabled, or an AES67 Apply is in progress)", Owner: "fpp"}
	}

	raw := RawStatus{
		Reachable: true,
		Locked:    status.PTP.Synced,
		Owner:     "fpp",
		Timescale: TimescaleUnknown,
		Domain:    status.PTP.Domain, DomainKnown: true,
		OffsetNs: status.PTP.OffsetNs, OffsetKnown: status.PTP.Synced,
	}
	if !raw.Locked {
		raw.Reason = fmt.Sprintf("FPP reports PTP enabled but not synced (portState %s)", status.PTP.PortState)
	}
	if status.PTP.GrandmasterID != "" {
		raw.GrandmasterIdentity, raw.GMKnown = status.PTP.GrandmasterID, true
	}
	if role, ok := fppRoleToRole(status.PTP.Role, status.PTP.IsGrandmaster); ok {
		raw.Role, raw.RoleKnown = role, true
	}

	if p.cfg.UDSAddress != "" {
		if _, err := os.Stat(p.cfg.UDSAddress); err == nil {
			raw.Owner = "fpp (ptp4l socket present at " + p.cfg.UDSAddress + ")"
		}
	}

	return raw
}

// fppRoleToRole maps FPP's own "role" string (or, failing that, the
// isGrandmaster flag) onto this package's Role vocabulary.
func fppRoleToRole(role string, isGrandmaster bool) (Role, bool) {
	switch role {
	case "grandmaster", "master":
		return RoleGrandmaster, true
	case "follower", "slave":
		return RoleFollower, true
	case "passive":
		return RolePassive, true
	case "listening":
		return RoleListening, true
	}
	if isGrandmaster {
		return RoleGrandmaster, true
	}
	return "", false
}

// fetchStatus tries each of p.cfg.StatusPaths in order against
// p.cfg.BaseURL, returning the first one that responds 200 with a
// decodable body. A path is either "host:port/path" (no leading "http://"
// — joined onto BaseURL's scheme+host) or "/path" (joined onto BaseURL
// directly), matching [DefaultFPPStatusPaths]'s own two forms.
func (p *FPPProvider) fetchStatus(ctx context.Context) (fppAES67Status, error) {
	var lastErr error
	for _, path := range p.cfg.StatusPaths {
		target, err := fppStatusURL(p.cfg.BaseURL, path)
		if err != nil {
			lastErr = err
			continue
		}
		status, err := fetchOne(ctx, p.client, target)
		if err != nil {
			lastErr = err
			continue
		}
		return status, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no status paths configured")
	}
	return fppAES67Status{}, fmt.Errorf("fetching FPP AES67 status from %s: %w", p.cfg.BaseURL, lastErr)
}

func fetchOne(ctx context.Context, client *http.Client, target string) (fppAES67Status, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fppAES67Status{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fppAES67Status{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fppAES67Status{}, fmt.Errorf("%s: status %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fppAES67Status{}, err
	}
	var status fppAES67Status
	if err := json.Unmarshal(body, &status); err != nil {
		return fppAES67Status{}, fmt.Errorf("%s: decode: %w", target, err)
	}
	return status, nil
}

// fppStatusURL joins path onto baseURL. A "/..." path replaces baseURL's
// own path, keeping its host and port (the PHP proxy form, served
// alongside FPP's own web UI). A ":<port>/..." path replaces baseURL's
// port too, keeping only its scheme and bare hostname (the native FPP 10
// AES67 status port, which is not the web UI's port).
func fppStatusURL(baseURL, path string) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("no FPP base URL configured")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid FPP base URL %q: %w", baseURL, err)
	}
	if strings.HasPrefix(path, "/") {
		u.Path = path
		return u.String(), nil
	}
	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Hostname(), path), nil
}
