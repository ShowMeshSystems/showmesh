package fppcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The brightness transition-gain write, FPP-PLUGIN-COORDINATOR-CONTRACTS.md
// section 2.2. It lives beside [Client.Invoke] because it is the same
// shape, one bounded POST to one configured FPP instance, and it reuses
// that Client's redirect refusal and per-request deadline rather than
// building a second transport with its own opinion about either.
//
// It is NOT an FPP native command. FPP's own command vocabulary cannot
// carry this: the transition gain is the coordinator's alone (section
// 2.1), and a registered FPP Action would be discoverable and schedulable
// in FPP's own UI, handing every operator and every schedule entry a way
// to fight the coordinator over one value.

// TransitionGainPath is the path this write goes to, relative to an FPP
// instance's base URL. It carries the /api/plugin-apis prefix because
// that is the only prefix FPP's Apache proxies to the plugin's own
// server, on both majors, and both majors bind that server to loopback.
// The plugin registers the shorter /showmesh/brightness/transition-gain;
// this is the address, and the two are different strings on purpose.
const TransitionGainPath = "/api/plugin-apis/showmesh/brightness/transition-gain"

// TransitionGainSchemaVersion is section 2.2's schemaVersion, currently 1.
const TransitionGainSchemaVersion = 1

// Percent bounds from section 2.2. Out of range is refused here rather
// than clamped, before a request is spent, for the same reason the plugin
// refuses rather than clamping: a mistyped value must stay visible.
const (
	minGainPercent = 0
	maxGainPercent = 100
	maxFadeSeconds = 86400
)

type transitionGainRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	TargetPercent int    `json:"targetPercent"`
	FadeSeconds   int    `json:"fadeSeconds"`
	RequestID     string `json:"requestId"`
}

// TransitionGainOutcome is the applied state section 2.2 requires the
// plugin to return, so a caller has evidence rather than an HTTP 200.
//
// Applied is false for an idempotent repeat of a requestId already
// applied, which is a success and not a failure: the plugin reports the
// state as it stands rather than restarting a fade. A caller that treats
// Applied false as an error will retry a write that already took.
type TransitionGainOutcome struct {
	StatusCode int
	Applied    bool
	// GainStart and GainTarget bound the fade the plugin is now running.
	GainStart   int
	GainTarget  int
	FadeSeconds int
	// Ceiling is FPP's own scheduled ceiling as the plugin sees it, and
	// EffectiveOutput is round(ceiling * gain / 100), the value actually
	// reaching the channels. Both are carried because the gain alone does
	// not say what the audience sees, which is the whole point of the
	// composition in section 2.1.
	Ceiling         int
	EffectiveOutput int
	// Body is the raw response, bounded, for a refusal a caller needs to
	// report verbatim rather than paraphrase.
	Body string
}

type transitionGainResponse struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Applied         bool   `json:"applied"`
	GainStart       int    `json:"gainStart"`
	GainTarget      int    `json:"gainTarget"`
	FadeSeconds     int    `json:"fadeSeconds"`
	Ceiling         int    `json:"ceiling"`
	EffectiveOutput int    `json:"effectiveOutput"`
	Error           string `json:"error"`
}

// SetTransitionGain writes the brightness transition gain to this
// Client's FPP instance, section 2.2.
//
// requestId is the caller-minted idempotency key and must not be empty: a
// repeat of the same id applies nothing and reports the state as it
// stands, which is what makes a retry of an unseen response safe. Minting
// it here would defeat that, because a retry would carry a new id and
// restart the fade, so the caller owns it.
//
// A 200 here means the plugin applied the write and returned the state it
// applied, which is more than [Client.Invoke]'s 200 means. That is a
// property of this route rather than of FPP: the plugin is ShowMesh code
// answering a ShowMesh contract, not FPP's own unconditional success
// body. It is still not evidence the audience saw a change.
func (c *Client) SetTransitionGain(ctx context.Context, targetPercent, fadeSeconds int, requestID string) (TransitionGainOutcome, error) {
	if requestID == "" {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: transition gain requires a requestId")
	}
	if targetPercent < minGainPercent || targetPercent > maxGainPercent {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: transition gain target percent %d is outside [%d, %d]", targetPercent, minGainPercent, maxGainPercent)
	}
	if fadeSeconds < 0 || fadeSeconds > maxFadeSeconds {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: transition gain fade seconds %d is outside [0, %d]", fadeSeconds, maxFadeSeconds)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(transitionGainRequest{
		SchemaVersion: TransitionGainSchemaVersion,
		TargetPercent: targetPercent,
		FadeSeconds:   fadeSeconds,
		RequestID:     requestID,
	})
	if err != nil {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: encoding transition gain request: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+TransitionGainPath, bytes.NewReader(payload))
	if err != nil {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: building transition gain request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: dispatching transition gain: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return TransitionGainOutcome{}, fmt.Errorf("fppcommand: reading transition gain response: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		body = body[:maxResponseBytes]
	}

	outcome := TransitionGainOutcome{StatusCode: resp.StatusCode, Body: string(body)}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return outcome, fmt.Errorf("fppcommand: dispatching transition gain: %w", &httpStatusError{StatusCode: resp.StatusCode})
	}

	var decoded transitionGainResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		// A 200 whose body does not decode is not a success. Section 2.2
		// requires the applied state, so a caller must not be handed a
		// zero-valued outcome that reads as "gain 0, output 0".
		return outcome, fmt.Errorf("fppcommand: transition gain returned 200 with an undecodable body: %w", err)
	}
	if decoded.SchemaVersion != TransitionGainSchemaVersion {
		return outcome, fmt.Errorf("fppcommand: transition gain response declares schemaVersion %d, want %d", decoded.SchemaVersion, TransitionGainSchemaVersion)
	}

	outcome.Applied = decoded.Applied
	outcome.GainStart = decoded.GainStart
	outcome.GainTarget = decoded.GainTarget
	outcome.FadeSeconds = decoded.FadeSeconds
	outcome.Ceiling = decoded.Ceiling
	outcome.EffectiveOutput = decoded.EffectiveOutput
	return outcome, nil
}
