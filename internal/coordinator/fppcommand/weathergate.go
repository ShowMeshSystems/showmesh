package fppcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// The weather gate write and read, ADR-053 decision 5 and
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 2.5. It lives beside
// [Client.SetTransitionGain] because it is the same shape and reuses the
// same Client transport rather than building a second one.

// WeatherGatePath is the coordinator-facing address for both the write and
// the read, mirroring [TransitionGainPath]'s own doc comment: it carries
// the /api/plugin-apis prefix Apache proxies to the plugin's own server,
// which is not the shorter path the plugin registers.
const WeatherGatePath = "/api/plugin-apis/showmesh/brightness/weather-gate"

// ErrWeatherGateUnsupported is returned when the FPP host's plugin has no
// weather gate route (404): an installation running a plugin build that
// predates ADR-053. A caller must report this instance as unable to be
// held dark by ShowMesh, never treat it as a closed or open gate.
var ErrWeatherGateUnsupported = errors.New("fppcommand: this FPP host's plugin has no weather gate route; it cannot be held dark by ShowMesh")

type weatherGateRequest struct {
	Closed   bool  `json:"closed"`
	Revision int64 `json:"revision"`
}

// weatherGateResponse decodes only the three fields the contract adds to
// the plugin's brightness state document; every other member of that
// document is ignored here.
type weatherGateResponse struct {
	WeatherGateClosed      bool  `json:"weatherGateClosed"`
	WeatherGateRevision    int64 `json:"weatherGateRevision"`
	EffectiveOutputPercent int   `json:"effectiveOutputPercent"`
}

// WeatherGateOutcome is the gate state the plugin reports, from either a
// write or a read.
type WeatherGateOutcome struct {
	StatusCode int
	Closed     bool
	Revision   int64
	// EffectiveOutputPercent is 0 whenever Closed, per the contract's
	// composition (section 2.5): the gate ignores the transition gain and
	// the apply/exclude ranges alike when closed.
	EffectiveOutputPercent int
	// Body is the raw response, bounded, for a refusal a caller needs to
	// report verbatim.
	Body string
}

// SetWeatherGate writes closed/revision to this Client's FPP instance. A
// coordinator write always applies (section 2.5): the plugin's stored
// revision becomes max(stored+1, revision), never refused for being
// behind.
//
// A 404 is reported as [ErrWeatherGateUnsupported], distinguishable via
// [errors.Is], never as a closed or open gate.
func (c *Client) SetWeatherGate(ctx context.Context, closed bool, revision int64) (WeatherGateOutcome, error) {
	if revision < 0 {
		return WeatherGateOutcome{}, fmt.Errorf("fppcommand: weather gate revision %d must not be negative", revision)
	}
	payload, err := json.Marshal(weatherGateRequest{Closed: closed, Revision: revision})
	if err != nil {
		return WeatherGateOutcome{}, fmt.Errorf("fppcommand: encoding weather gate request: %w", err)
	}
	return c.doWeatherGate(ctx, http.MethodPost, bytes.NewReader(payload))
}

// ReadWeatherGate reads this Client's FPP instance's current gate state
// from the same address, with no body.
//
// A 404 is reported as [ErrWeatherGateUnsupported], identically to
// [Client.SetWeatherGate].
func (c *Client) ReadWeatherGate(ctx context.Context) (WeatherGateOutcome, error) {
	return c.doWeatherGate(ctx, http.MethodGet, nil)
}

func (c *Client) doWeatherGate(ctx context.Context, method string, body io.Reader) (WeatherGateOutcome, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+WeatherGatePath, body)
	if err != nil {
		return WeatherGateOutcome{}, fmt.Errorf("fppcommand: building weather gate request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return WeatherGateOutcome{}, fmt.Errorf("fppcommand: dispatching weather gate %s: %w", method, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return WeatherGateOutcome{}, fmt.Errorf("fppcommand: reading weather gate %s response: %w", method, err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		respBody = respBody[:maxResponseBytes]
	}

	outcome := WeatherGateOutcome{StatusCode: resp.StatusCode, Body: string(respBody)}
	if resp.StatusCode == http.StatusNotFound {
		return outcome, ErrWeatherGateUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return outcome, fmt.Errorf("fppcommand: dispatching weather gate %s: %w", method, &httpStatusError{StatusCode: resp.StatusCode})
	}

	var decoded weatherGateResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return outcome, fmt.Errorf("fppcommand: weather gate %s returned 200 with an undecodable body: %w", method, err)
	}
	outcome.Closed = decoded.WeatherGateClosed
	outcome.Revision = decoded.WeatherGateRevision
	outcome.EffectiveOutputPercent = decoded.EffectiveOutputPercent
	return outcome, nil
}
