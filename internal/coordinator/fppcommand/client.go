package fppcommand

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds a single command request, via a context deadline
// applied per request (never http.Client.Timeout — see Options.HTTPClient
// for why, mirroring the identical reasoning
// internal/coordinator/collector/fpp's own Options.RequestTimeout gives,
// independently arrived at here rather than shared with that package).
// SHOWMESH HYPOTHESIS, NOT MEASURED: RES-009 owns real latency evidence.
// Chosen to be comfortably longer than a healthy FPP's LAN response time
// for a command endpoint, which this project has not benchmarked any
// differently from a status endpoint.
const DefaultTimeout = 5 * time.Second

// Options configures a [Client]. Every field left at its zero value is
// replaced by its documented default in [New].
type Options struct {
	// HTTPClient is the client this Client issues requests with. Left nil,
	// New builds a private one. Whatever is supplied — nil or not — New
	// takes a shallow copy and forces CheckRedirect to refuse every
	// redirect; see [New]'s doc comment for why this package does not
	// trust a caller-supplied client's own CheckRedirect any more than
	// internal/coordinator/collector/fpp trusts one, and does not share
	// that package's implementation to prove it independently (this
	// package's own doc comment).
	HTTPClient *http.Client

	// Timeout bounds a single request. See [DefaultTimeout].
	Timeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	// Force CheckRedirect on a private, shallow copy — never a mutation of
	// a caller's original *http.Client, and never conditional on whether
	// CheckRedirect was already set. A 302 from this instance's own
	// /api/command/... response would, if followed, dispatch a SECOND,
	// FPP-invoked command on whatever host and path the Location header
	// names — a sharper version of the collector's own confused-deputy
	// risk, since what would be walked onto an unnamed host here is not a
	// status read but a command.
	guarded := *o.HTTPClient
	guarded.CheckRedirect = refuseRedirects
	o.HTTPClient = &guarded
	return o
}

// refuseRedirects is this package's own copy of the identical policy
// internal/coordinator/collector/fpp's fpp.go carries under the same
// name: return [http.ErrUseLastResponse] so Client.Do stops after the
// first hop and hands back the 3xx itself, with its Location header
// intact but never dereferenced. Written independently rather than
// imported from that package — see this package's doc comment for why a
// shared implementation would undermine the exact property
// TestCollectorPackageNeverImportsFPPCommand exists to prove: two
// packages that agree today because they share code are not proven to
// still agree once one of them changes, only asserted to.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Client dispatches FPP's own native lifecycle commands against ONE
// configured FPP instance's base URL, over GET at /api/command/... — the
// exact shape internal/coordinator/collector/fpp's own doc comment names
// as the reason that package forces CheckRedirect. A Client is built
// fresh per dispatch by internal/coordinator/api (commands are rare;
// there is no poll loop here to amortize a shared connection pool
// against), so it carries no mutable state beyond its own configuration.
type Client struct {
	baseURL string
	client  *http.Client
	timeout time.Duration
}

// New constructs a Client targeting baseURL, which must be an absolute
// http or https URL with a host and no userinfo, path beyond a bare
// trailing slash, query, or fragment — the identical rule
// internal/coordinator/collector/fpp.New enforces on the same configured
// endpoint value, reproduced here rather than imported (this package's
// own doc comment) so a Client is safe to construct directly, including
// in tests, without depending on that package's validation having already
// run. In production both packages are handed the exact same configured
// endpoint string for the same FPP instance, and both must agree it is
// well-formed independently, not because one delegates to the other.
func New(baseURL string, opts Options) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("fppcommand: invalid URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fppcommand: URL %q must use http or https", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("fppcommand: URL %q must include a host", baseURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("fppcommand: URL must not include userinfo/credentials")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a path", baseURL)
	}
	if parsed.RawQuery != "" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a query", baseURL)
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a fragment", baseURL)
	}

	o := opts.withDefaults()
	return &Client{
		baseURL: strings.TrimSuffix(parsed.String(), "/"),
		client:  o.HTTPClient,
		timeout: o.Timeout,
	}, nil
}

// Outcome is FPP's own HTTP-level response to one dispatched command —
// NOT confirmation that the command took effect. ADR-003: a 200 here is
// never success on its own; internal/coordinator/api confirms by evidence
// against the collector separately, and this type carries nothing that
// could be mistaken for that confirmation.
type Outcome struct {
	// StatusCode is FPP's own response status. A 2xx means FPP accepted
	// and executed the command synchronously (FPP's command endpoint does
	// not queue or defer); it is Invoke's caller's decision what a non-2xx
	// means for the command's own lifecycle, not this package's.
	StatusCode int

	// Body is FPP's raw response body, bounded by maxResponseBytes. FPP's
	// command endpoint answers with a short human-readable string (e.g.
	// "Stopped"), never structured JSON, so this is carried as text for
	// the caller to log or surface verbatim rather than parsed here.
	Body string
}

// maxResponseBytes bounds how much of FPP's command response body is
// read. SHOWMESH HYPOTHESIS: FPP's command responses are a handful of
// bytes (e.g. "Stopped"); this is generous headroom mirroring
// internal/coordinator/collector/fpp's own DefaultMaxResponseBytes
// reasoning, independently sized smaller here because a command response
// is not a status document.
const maxResponseBytes = 64 << 10 // 64 KiB

// Invoke issues GET {baseURL}/api/command/{name} with no arguments, and
// returns FPP's raw [Outcome]. name is path-escaped, matching the exact
// encoding an FPP command name with a space requires (e.g. "Stop Now" ->
// "Stop%20Now") — see [Client.StopPlaylist]'s doc comment for FPP's own
// command name for the primitive this package's one caller dispatches.
//
// Every non-2xx response, including a 3xx this Client's own client was
// built to never follow (see [refuseRedirects]), is returned as a non-nil
// error via [httpStatusError] wrapped by classifyError — never a silent
// retry, never a fabricated success. A transport-level failure (dial
// error, timeout) is likewise a non-nil error with no Outcome.
func (c *Client) Invoke(ctx context.Context, name string) (Outcome, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	target := c.baseURL + "/api/command/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: building request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: dispatching %q: %w", name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: reading response to %q: %w", name, err)
	}
	truncated := int64(len(body)) > maxResponseBytes
	if truncated {
		body = body[:maxResponseBytes]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Outcome{StatusCode: resp.StatusCode, Body: string(body)},
			fmt.Errorf("fppcommand: dispatching %q: %w", name, &httpStatusError{StatusCode: resp.StatusCode})
	}
	return Outcome{StatusCode: resp.StatusCode, Body: string(body)}, nil
}

// StopPlaylist dispatches FPP's own "Stop Now" command: the native,
// zero-argument command that stops playback immediately. See this
// package's report for how that name was confirmed against a real, bench
// fppd — FPP's command list carries no command literally named "Stop
// Playlist" (BUILD-PLAN's own descriptive name for this step's primitive
// command); "Stop Now" is the zero-argument member of FPP's Stop family
// ("Stop Gracefully" requires a "loop" argument, so it is not the
// no-target-parameter command BUILD-PLAN's decision names) confirmed live
// to transition FPP's status_name from "playing" directly to "idle".
func (c *Client) StopPlaylist(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Stop Now")
}

// httpStatusError records a non-2xx HTTP response, distinguishable via
// [errors.As] from a transport-level failure to reach FPP at all (a bare
// *url.Error wrapping a dial or timeout failure, returned as-is from
// Invoke with no wrapping of this type).
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}
