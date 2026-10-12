package fppcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ExcludedRangesPath is the plugin route that replaces the channel ranges
// the brightness ceiling leaves alone, relative to an FPP instance's base
// URL. It carries the same prefix [TransitionGainPath] does, for its reason.
const ExcludedRangesPath = "/api/plugin-apis/showmesh/brightness/excluded-ranges"

// ChannelRange is one run of channels: a 1-based first channel and how
// many channels follow from it.
type ChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

type excludedRangesRequest struct {
	ExcludedRanges []ChannelRange `json:"excludedRanges"`
}

// ExcludedRangesOutcome is what the plugin answered. Refusal is the
// plugin's own error text when it sent one, for a caller to show verbatim.
type ExcludedRangesOutcome struct {
	StatusCode int
	Refusal    string
	Body       string
}

// SetBrightnessExcludedRanges replaces the whole excluded list on this
// Client's FPP instance; an empty list clears it. A 2xx means the plugin
// accepted the list, never that it holds it: the caller reads it back.
func (c *Client) SetBrightnessExcludedRanges(ctx context.Context, ranges []ChannelRange) (ExcludedRangesOutcome, error) {
	if ranges == nil {
		ranges = []ChannelRange{}
	}
	payload, err := json.Marshal(excludedRangesRequest{ExcludedRanges: ranges})
	if err != nil {
		return ExcludedRangesOutcome{}, fmt.Errorf("fppcommand: encoding excluded ranges request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, c.baseURL+ExcludedRangesPath, bytes.NewReader(payload))
	if err != nil {
		return ExcludedRangesOutcome{}, fmt.Errorf("fppcommand: building excluded ranges request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return ExcludedRangesOutcome{}, fmt.Errorf("fppcommand: writing excluded ranges: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ExcludedRangesOutcome{}, fmt.Errorf("fppcommand: reading excluded ranges response: %w", err)
	}

	outcome := ExcludedRangesOutcome{StatusCode: resp.StatusCode, Body: string(body)}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var refused struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &refused) == nil {
			outcome.Refusal = strings.TrimSpace(refused.Error)
		}
		return outcome, fmt.Errorf("fppcommand: writing excluded ranges: %w", &httpStatusError{StatusCode: resp.StatusCode})
	}
	return outcome, nil
}
