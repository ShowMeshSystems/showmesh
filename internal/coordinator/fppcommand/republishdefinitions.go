package fppcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The coordinator-triggered playlist definition republish,
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 3.9. It lives beside
// [Client.SetTransitionGain] because it is the same shape, one bounded
// POST to one configured FPP instance, and reuses that Client's redirect
// refusal and per-request deadline.
//
// It writes nothing to FPP. Its whole effect on the plugin is to drop the
// plugin's record of which definitions it has already published and to
// make a sweep due, so definitions the coordinator lost are re-sent even
// though their content, and therefore their hash, has not changed.

// RepublishDefinitionsPath is the path this write goes to, relative to an
// FPP instance's base URL. It carries the /api/plugin-apis prefix because
// that is the only prefix FPP's Apache proxies to the plugin's own server,
// on both majors. The plugin registers the shorter
// /showmesh/playlists/republish; this is the address, and the two are
// different strings on purpose.
const RepublishDefinitionsPath = "/api/plugin-apis/showmesh/playlists/republish"

// RepublishDefinitionsSchemaVersion is section 3.9's schemaVersion,
// currently 1.
const RepublishDefinitionsSchemaVersion = 1

type republishDefinitionsRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	RequestID     string `json:"requestId"`
}

// RepublishDefinitionsOutcome is what the plugin reported at the moment it
// answered, section 3.9.
//
// Read the fields for what they are. They say what the plugin cleared,
// what it still holds, what it deliberately kept, and that a sweep is
// OWED. None of them says a definition reached the coordinator: when this
// route answers, not one post of that sweep has been attempted. What
// actually arrived is read back through the coordinator's own stored
// definitions.
type RepublishDefinitionsOutcome struct {
	StatusCode int

	// Applied is false for an idempotent repeat of a requestId the plugin
	// already applied, which is a success and not a failure: the plugin
	// cleared nothing a second time and reports the state as it stands.
	Applied bool

	// DefinitionsCleared is how many (instanceUuid, playlistHash) pairs
	// this request dropped from the plugin's published-set. 0 on a repeat.
	DefinitionsCleared int

	// DefinitionsHeld is how many pairs that set holds as the answer is
	// written: 0 immediately after an applied clear, and on a repeat how
	// many the sweep has already re-sent and had accepted, which is how
	// polling one requestId shows progress.
	DefinitionsHeld int

	// DefinitionsRefusedTerminally is how many pairs the plugin holds as
	// terminally refused and deliberately did NOT clear. These are the
	// playlists the republish will not re-send, which is what an operator
	// needs when the playlist they were chasing is still missing after.
	DefinitionsRefusedTerminally int

	// SweepPending is whether a sweep is owed and has not completed since
	// this republish. It is the strongest honest claim available at
	// response time: the resend is owed, not done.
	SweepPending bool

	// Body is the raw response, bounded, for a refusal a caller needs to
	// report verbatim rather than paraphrase.
	Body string
}

type republishDefinitionsResponse struct {
	SchemaVersion                int    `json:"schemaVersion"`
	Applied                      bool   `json:"applied"`
	DefinitionsCleared           int    `json:"definitionsCleared"`
	DefinitionsHeld              int    `json:"definitionsHeld"`
	DefinitionsRefusedTerminally int    `json:"definitionsRefusedTerminally"`
	SweepPending                 bool   `json:"sweepPending"`
	Error                        string `json:"error"`
}

// RepublishDefinitions asks this Client's FPP instance to republish its
// playlist definitions, section 3.9.
//
// requestId is the caller-minted idempotency key and must not be empty: a
// repeat of the same id clears nothing and reports the state as it stands,
// which is what makes a retry of an unanswered request safe and what makes
// polling the same id a way to learn the sweep finished. Minting it here
// would defeat both.
//
// A 200 means the plugin agreed to resend and says what it did to its own
// state. It is not evidence that any definition reached the coordinator.
func (c *Client) RepublishDefinitions(ctx context.Context, requestID string) (RepublishDefinitionsOutcome, error) {
	if requestID == "" {
		return RepublishDefinitionsOutcome{}, fmt.Errorf("fppcommand: definition republish requires a requestId")
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(republishDefinitionsRequest{
		SchemaVersion: RepublishDefinitionsSchemaVersion,
		RequestID:     requestID,
	})
	if err != nil {
		return RepublishDefinitionsOutcome{}, fmt.Errorf("fppcommand: encoding definition republish request: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+RepublishDefinitionsPath, bytes.NewReader(payload))
	if err != nil {
		return RepublishDefinitionsOutcome{}, fmt.Errorf("fppcommand: building definition republish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return RepublishDefinitionsOutcome{}, fmt.Errorf("fppcommand: dispatching definition republish: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return RepublishDefinitionsOutcome{}, fmt.Errorf("fppcommand: reading definition republish response: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		body = body[:maxResponseBytes]
	}

	outcome := RepublishDefinitionsOutcome{StatusCode: resp.StatusCode, Body: string(body)}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return outcome, fmt.Errorf("fppcommand: dispatching definition republish: %w", &httpStatusError{StatusCode: resp.StatusCode})
	}

	var decoded republishDefinitionsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		// A 200 whose body does not decode is not a success. The zero
		// value here reads as "cleared nothing, holds nothing, refused
		// nothing, no sweep owed", which is the most reassuring answer
		// this route can produce and would be produced by the plugin
		// having done nothing at all.
		return outcome, fmt.Errorf("fppcommand: definition republish returned 200 with an undecodable body: %w", err)
	}
	if decoded.SchemaVersion != RepublishDefinitionsSchemaVersion {
		return outcome, fmt.Errorf("fppcommand: definition republish response declares schemaVersion %d, want %d",
			decoded.SchemaVersion, RepublishDefinitionsSchemaVersion)
	}
	if decoded.Applied && !decoded.SweepPending {
		// Section 3.9 fixes sweepPending as always true on an applied
		// answer, because the sweep runs on the worker thread and cannot
		// have completed while the handler that made it due is still
		// writing its response. Relaying this pair would tell an operator
		// the resend is finished at the one moment it provably has not
		// started.
		return outcome, fmt.Errorf("fppcommand: definition republish reported applied with sweepPending false, which the contract forbids")
	}

	outcome.Applied = decoded.Applied
	outcome.DefinitionsCleared = decoded.DefinitionsCleared
	outcome.DefinitionsHeld = decoded.DefinitionsHeld
	outcome.DefinitionsRefusedTerminally = decoded.DefinitionsRefusedTerminally
	outcome.SweepPending = decoded.SweepPending
	return outcome, nil
}
