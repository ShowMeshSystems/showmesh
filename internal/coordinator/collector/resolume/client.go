package resolume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// apiPrefix is Arena's versioned REST root. baseURL passed to [NewClient]
// never includes this — see NewClient's doc comment — so every request
// path this file builds is c.baseURL + apiPrefix + path.
const apiPrefix = "/api/v1"

// Provenance-labeled defaults, in the pattern
// internal/coordinator/collector/fpp's own Default* constants establish:
// every threshold states whether it is derived from something measured or
// is a ShowMesh guess. This one is a guess; the bench capture that
// measured Resolume's REST latency (4-64ms for a connect, on an arm64
// laptop over loopback) did not measure ordinary GET latency against a
// live show host, and RES-001/RES-013 own the real value once that
// exists.
const (
	// DefaultRequestTimeout bounds an ordinary GET (/product). SHOWMESH
	// HYPOTHESIS, NOT MEASURED.
	DefaultRequestTimeout = 5 * time.Second
)

// maxProductResponseBytes bounds how much of the /product response is
// read. The capture recorded this body at 64 bytes; this is generous
// headroom above that so a misbehaving or compromised Resolume streaming
// an unbounded body on this endpoint cannot exhaust coordinator memory —
// the same reasoning internal/coordinator/collector/fpp's
// MaxResponseBytes states, applied to the one endpoint this file reads.
const maxProductResponseBytes = 4 << 10 // 4 KiB

// maxErrorBodyBytes bounds how much of a non-2xx response body [StatusError]
// retains. Resolume's own errors are plain text and echo the request path
// (capture section 2.5), so a bound here is what stops an error string
// from becoming a log bomb if some future non-2xx response is much larger
// than the short messages this capture observed.
const maxErrorBodyBytes = 200

// ClientOptions configures a [Client]. Every field left at its zero value
// is replaced by the matching Default* constant.
type ClientOptions struct {
	// HTTPClient is shared across every request this Client makes. See
	// NewClient's doc comment for why whatever is supplied here — nil or
	// not — is copied rather than mutated, and why CheckRedirect is
	// unconditionally overridden on that copy.
	HTTPClient *http.Client

	// RequestTimeout bounds an ordinary GET, via a context deadline
	// applied per request. See DefaultRequestTimeout.
	RequestTimeout time.Duration
}

// Client is a read-only REST client for one Resolume Arena instance's
// `/api/v1` surface. It issues GET requests only — there is no method on
// this type that can send Resolume a POST, PUT, or DELETE — per this
// package's doc comment. It has no method that reads GET /composition:
// see this package's doc comment and guardfullcomposition_test.go for why
// that call is forbidden outright, not merely unused.
type Client struct {
	baseURL string // scheme://host[:port], no path, no trailing slash
	http    *http.Client

	requestTimeout time.Duration
}

// NewClient constructs a Client for one Resolume Arena instance. baseURL
// is the API root WITHOUT the version path — e.g. "http://127.0.0.1:9080"
// — and must be an absolute http or https URL with a host and no
// userinfo/credentials, no path, no query, and no fragment. This is
// exactly the rule internal/coordinator/collector/fpp.New enforces on its
// own baseURL, duplicated here deliberately: a *Client must be safe to
// construct directly, including in tests, without relying on some other
// package's validation already having run, and both packages build every
// request path by string-concatenating a fixed prefix onto baseURL (see
// apiPrefix), so a baseURL carrying its own path or query would corrupt
// every request this Client ever makes exactly the way it would for fpp.
func NewClient(baseURL string, opts ClientOptions) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("resolume client: invalid URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("resolume client: URL %q must use http or https", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("resolume client: URL %q must include a host", baseURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("resolume client: URL %q must not include userinfo/credentials", baseURL)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("resolume client: URL %q must not include a path", baseURL)
	}
	if parsed.RawQuery != "" {
		return nil, fmt.Errorf("resolume client: URL %q must not include a query", baseURL)
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("resolume client: URL %q must not include a fragment", baseURL)
	}

	o := opts
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	// Force CheckRedirect on a private, shallow copy of whatever client is
	// now in o.HTTPClient — the freshly-built default above, or a
	// caller-supplied one, including one whose own CheckRedirect is
	// already permissive. Never a mutation of a caller's original
	// *http.Client. The Transport field is a pointer, so the copy still
	// shares the caller's connection pool. See this package's doc comment
	// for why this defence applies even though every request this Client
	// issues is a GET: Resolume's own REST API serves destructive POSTs
	// and DELETEs on the same host, so an unguarded redirect follow could
	// still turn a read into a confused deputy for one of those.
	guarded := *o.HTTPClient
	guarded.CheckRedirect = refuseRedirects
	o.HTTPClient = &guarded

	return &Client{
		baseURL:        strings.TrimSuffix(parsed.String(), "/"),
		http:           o.HTTPClient,
		requestTimeout: o.RequestTimeout,
	}, nil
}

// refuseRedirects is installed as every request client's CheckRedirect by
// NewClient. Returning [http.ErrUseLastResponse] tells the standard
// library's Client.Do to stop after the first hop and hand back the
// redirect response itself — the 3xx, with its Location header — rather
// than silently following it (Go's zero-value behavior: up to 10
// redirects to whatever host and path the response names). The status
// check in [Client.Product] already treats any non-2xx response, 3xx
// included, as a [StatusError], so refusing the follow is the whole fix.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Product is Arena's own version identity: name plus a four-part version
// (major.minor.micro, revision).
type Product struct {
	Name     string `json:"name"`
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	Micro    int    `json:"micro"`
	Revision int    `json:"revision"`
}

// String renders the canonical form this package uses everywhere it
// displays a Product, e.g. "Arena 7.23.2 (r51094)" — the exact form
// captured from a real instance.
func (p Product) String() string {
	return fmt.Sprintf("%s %d.%d.%d (r%d)", p.Name, p.Major, p.Minor, p.Micro, p.Revision)
}

// Product performs GET /api/v1/product: the cheapest liveness probe this
// API offers, a 64-byte body in the capture.
func (c *Client) Product(ctx context.Context) (Product, error) {
	resp, cancel, err := c.doGET(ctx, "/product", c.requestTimeout)
	if err != nil {
		return Product{}, err
	}
	defer cancel()
	defer drainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Product{}, newStatusError(resp, "/product")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProductResponseBytes+1))
	if err != nil {
		return Product{}, fmt.Errorf("resolume: reading /product response: %w", err)
	}
	if int64(len(body)) > maxProductResponseBytes {
		return Product{}, fmt.Errorf("resolume: /product response exceeded %d byte limit", maxProductResponseBytes)
	}

	var p Product
	if err := json.Unmarshal(body, &p); err != nil {
		return Product{}, &DecodeError{Path: "/product", Err: err}
	}
	return p, nil
}

// doGET issues one bounded GET against c.baseURL+apiPrefix+path and
// returns the response together with the context.CancelFunc the caller
// must defer. The cancel func is returned rather than applied internally
// (unlike internal/coordinator/collector/fpp.fetch, which reads the whole
// body before returning) so the request context stays live for however
// long the caller takes to read and decode resp.Body, not just until
// doGET itself returns.
func (c *Client) doGET(ctx context.Context, path string, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+apiPrefix+path, nil)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("resolume: building request for %s: %w", path, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// drainAndClose discards the remainder of body and closes it so the
// underlying connection can be reused by the shared client's pool. Bounded
// so a misbehaving response cannot make this block on an unbounded read.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	_ = body.Close()
}

// StatusError records a non-2xx HTTP response from Resolume. Resolume's
// own errors are plain text, not JSON, and echo the request path (capture
// section 2.5) — Body is truncated to maxErrorBodyBytes so this can never
// become a log bomb.
type StatusError struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("resolume: http %d for %s: %s", e.StatusCode, e.Path, e.Body)
}

// IsNotFound reports whether err is a [StatusError] with StatusCode 404.
// Resolume genuinely 404s a request against an object id that no longer
// exists (capture section 2.5) — this package does not act on that
// distinction (that is a later seam's job, per this package's doc
// comment), but a caller must be able to tell a stale reference from an
// ordinary transport failure, so this function exists rather than making
// every caller reach into StatusError by hand.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

// newStatusError builds a [StatusError] from a non-2xx response, reading
// (and thereby partially draining) up to maxErrorBodyBytes of the body.
// The caller's own deferred drainAndClose finishes draining whatever this
// leaves and closes the body.
func newStatusError(resp *http.Response, path string) *StatusError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes+1))
	text := string(body)
	if len(text) > maxErrorBodyBytes {
		text = text[:maxErrorBodyBytes]
	}
	return &StatusError{StatusCode: resp.StatusCode, Path: path, Body: text}
}

// DecodeError wraps a JSON decode failure against a named endpoint, so
// [ClassifyError] can name the failure class without dumping the
// underlying decode error, which can be long.
type DecodeError struct {
	Path string
	Err  error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("resolume: decoding %s response: %v", e.Path, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }

// maxClassifiedErrorLen bounds ClassifyError's fallthrough — the only
// branch that returns anything derived from err itself rather than a
// fixed short string. Review finding G (2026-08-14): this function's own
// doc comment promised "never a full error dump," and the fallthrough
// broke that promise by returning err.Error() whole. A *url.Error (Go's
// own http.Client wraps every transport error in one) formats as
// `Get "http://host:port/api/v1/product": dial tcp 127.0.0.1:9080:
// connect: connection refused` — carrying the full request URL and dial
// detail — and this string reaches [observation.Reason], which is
// operator-facing. Bounded the same way StatusError.Body is bounded
// (maxErrorBodyBytes), for the identical reason: a verbose wrapped error
// must never become a rendering hazard even though it was never
// classified into one of the short reasons above.
const maxClassifiedErrorLen = 160

// ClassifyError renders err as a short, operator-useful failure class
// suitable for [observation.Reason]. Every branch above the fallthrough
// returns a FIXED short string carrying no data from err at all. The
// fallthrough, for an error shape none of those branches recognizes,
// still calls err.Error() — there is no fixed string to substitute,
// because this project's own "absent evidence is stated, never omitted"
// rule (CLAUDE.md) means an operator seeing an unclassified failure needs
// SOME detail, not silence — but it is bounded to maxClassifiedErrorLen
// rather than returned whole, which is what keeps this function's own
// "never a full error dump" claim true instead of aspirational. Also
// never leaks more of the URL than the operator already configured:
// NewClient rejects any base URL carrying userinfo, so there is
// structurally nothing of that kind to leak here regardless of length.
//
// Mirrors internal/coordinator/collector/fpp's own classifyFetchError,
// which as of this writing carries the IDENTICAL unbounded fallthrough —
// a divergence recorded here rather than fixed in that package, which
// this seam does not own.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}

	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return fmt.Sprintf("http status %d", statusErr.StatusCode)
	}

	var decodeErr *DecodeError
	if errors.As(err, &decodeErr) {
		return "decode error"
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns error"
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}

	s := err.Error()
	if len(s) > maxClassifiedErrorLen {
		s = s[:maxClassifiedErrorLen] + "…(truncated)"
	}
	return s
}
