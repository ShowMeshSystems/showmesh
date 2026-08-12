package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// clientAPIVersion is the only API major version this build of showmeshctl
// understands, per contract §6.6 / §6.10. It is sent on every request as
// the optional ShowMesh-API-Version request header (contract §6.6), so a
// coordinator serving a different major version can reject the request
// explicitly instead of the client silently misreading a response shaped
// for a version it does not support.
const clientAPIVersion = "1"

// maxResponseBytes bounds how much of one REST response body this client
// reads. SHOWMESH HYPOTHESIS, NOT MEASURED — no bench has sized a real
// snapshot or node/observation list — chosen only so that a misbehaving or
// compromised coordinator (or a proxy serving something unrelated at the
// same URL) cannot make this CLI buffer an unbounded body in memory.
// Mirrors the FPP REST collector's own bounded reads
// (internal/coordinator/collector/fpp's DefaultMaxResponseBytes and
// fetch/io.LimitReader): that package deliberately never reads past its
// limit, and this client had no equivalent bound at all, reading the whole
// body via a bare io.ReadAll (Step 3 review finding 4.7).
const maxResponseBytes = 32 << 20 // 32 MiB

// errResponseTooLarge is returned (wrapped in a *cliError) when a response
// body exceeds maxResponseBytes.
var errResponseTooLarge = fmt.Errorf("response body exceeded %d byte limit", maxResponseBytes)

// client is showmeshctl's HTTP client for the coordinator's /api/v1
// surface. It knows nothing about the coordinator's internal types —
// see doc.go — only the wire shapes declared in types.go.
type client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// newClient validates server and builds a client. A malformed --server
// value is a usage error (caught here, before any request is attempted)
// rather than surfacing later as a confusing network failure.
func newClient(server, token string, httpClient *http.Client) (*client, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, newCLIError(exitUsage, "invalid --server value %q: %v", server, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, newCLIError(exitUsage, "invalid --server value %q: scheme must be http or https", server)
	}
	if u.Host == "" {
		return nil, newCLIError(exitUsage, "invalid --server value %q: missing host", server)
	}
	return &client{baseURL: u, token: token, httpClient: httpClient}, nil
}

// endpoint joins the client's base URL with an /api/v1 path.
func (c *client) endpoint(apiPath string, query url.Values) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + apiPath
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

// getJSON issues an authenticated GET against apiPath and decodes a
// successful JSON response into out. It never sets
// json.Decoder.DisallowUnknownFields: contract §6.2 makes v1 additive-only,
// and a strict decoder would make this CLI break the moment the
// coordinator it talks to adds a field, which is the exact failure mode
// the additive-only rule exists to prevent. See
// TestDecodeIgnoresUnknownFields.
//
// A non-2xx response is decoded as an RFC 9457 problem (contract §6.6) and
// returned as a *cliError carrying the exit code that problem maps to
// (problem.go). A response whose body is not valid JSON at all (a proxy's
// HTML error page, a truncated body) still produces a *cliError, using
// only the HTTP status to classify it.
func (c *client) getJSON(ctx context.Context, apiPath string, query url.Values, out any) error {
	body, err := c.getRaw(ctx, apiPath, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return newCLIError(exitAPIError, "decoding response from %s: %v", c.endpoint(apiPath, query), err)
	}
	return nil
}

// getRaw is getJSON's building block: it performs the request, applies the
// success/problem split, and returns the raw success body. It is exposed
// separately, not because the wrapper shape of the single-resource
// endpoints is in doubt — contract §6.10 now pins it exactly (GET
// /api/v1/nodes/{id} -> {"serverTime":…, "node":…}, GET
// /api/v1/fpp/{instanceId} -> {"serverTime":…, "instance":…} — and
// decodeSingleNode/decodeSingleFPPInstance decode against that one shape
// only, no fallback candidates) — but because cmdNode and cmdFPP each need
// the raw bytes to hand to their own decode helper rather than a value
// getJSON has already unmarshalled into a caller-supplied struct.
func (c *client) getRaw(ctx context.Context, apiPath string, query url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(apiPath, query), nil)
	if err != nil {
		return nil, newCLIError(exitUsage, "building request: %v", err)
	}
	c.applyHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, classifyRequestError(c.baseURL.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded read, matching the FPP collector's own posture: read one byte
	// past the limit so a body that is exactly at the limit is not
	// mistaken for one that was truncated, then reject anything over it
	// rather than silently returning a partial (and therefore possibly
	// invalid-JSON, misleadingly-truncated) body.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, newCLIError(exitUnreachable, "reading response body: %v", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, newCLIError(exitAPIError, "%v (from %s)", errResponseTooLarge, c.endpoint(apiPath, query))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeProblemError(resp, body)
	}
	if err := c.checkAPIVersionHeader(resp); err != nil {
		return nil, err
	}
	return body, nil
}

// applyHeaders sets the headers common to every /api/v1 request: the
// bearer token (contract §6.8) when configured, and the API version this
// build speaks (contract §6.6).
func (c *client) applyHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ShowMesh-API-Version", clientAPIVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// classifyRequestError turns a transport-level failure (connection
// refused, DNS failure, TLS failure, context deadline) into the
// "coordinator unreachable" exit code. This is deliberately distinct from
// any HTTP-level error, which means the server was reached at all.
func classifyRequestError(server string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newCLIError(exitUnreachable, "timed out contacting %s: %v", server, err)
	}
	return newCLIError(exitUnreachable, "could not reach coordinator at %s: %v", server, err)
}

// checkAPIVersionHeader defends against the case contract §6.6 does not
// cover explicitly: a 2xx response whose ShowMesh-API-Version header does
// not match what this client asked for. That should not happen — the
// contract says a version mismatch is a 400 — but if a coordinator ever
// violates that, rendering the body anyway would be exactly the "partial
// render of plausible-looking state" OPERATOR-UI §5.1 forbids. Absence of
// the header is tolerated (older infra probes, a proxy that strips
// unrecognized headers) rather than treated as a mismatch.
func (c *client) checkAPIVersionHeader(resp *http.Response) error {
	got := resp.Header.Get("ShowMesh-API-Version")
	if got == "" || got == clientAPIVersion {
		return nil
	}
	return newCLIError(exitVersionIncompatible,
		"coordinator responded with ShowMesh-API-Version %s, this CLI speaks %s; refusing to render a partial result",
		got, clientAPIVersion)
}

// decodeProblemError decodes an RFC 9457 problem+json body (contract §6.6)
// and returns the *cliError it maps to. If the body cannot be decoded as a
// problem at all, the resulting error still carries a status-derived exit
// code and the raw body (truncated) for diagnosis, rather than failing
// silently.
//
// A 429 (ADR-024 decision 8's login concurrency bound) carries a
// Retry-After response header the problem body itself does not repeat —
// session.go's tooManyRequestsProblem sets it separately from the detail
// text — so this reads it directly from resp and appends it to the
// message; an operator (or a script parsing stderr) should not have to
// re-derive "how long do I wait" from a header this function already had
// in hand.
func decodeProblemError(resp *http.Response, body []byte) error {
	var p problem
	if err := json.Unmarshal(body, &p); err != nil || p.Title == "" {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return newCLIError(exitCodeForProblem(resp.StatusCode, nil),
			"coordinator returned HTTP %d and a non-problem+json body: %s", resp.StatusCode, snippet)
	}

	msg := p.Title
	if p.Detail != "" {
		msg = fmt.Sprintf("%s: %s", p.Title, p.Detail)
	}
	if p.Type == problemUnsupportedAPIVersion && len(p.SupportedVersions) > 0 {
		msg = fmt.Sprintf("%s (this CLI requested version %s; coordinator supports %v)", msg, clientAPIVersion, p.SupportedVersions)
	}
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		msg = fmt.Sprintf("%s (Retry-After: %s seconds)", msg, retryAfter)
	}
	return newCLIError(exitCodeForProblem(resp.StatusCode, &p), "%s", msg)
}
