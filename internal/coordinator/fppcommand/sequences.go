package fppcommand

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SequencesPath is FPP's own sequence file list: a JSON array of names,
// subfolder files as relative paths. FPP's playlist editor reads the same URL.
const SequencesPath = "/api/files/sequences?nameOnly=1"

const maxSequenceListBytes = 1 << 20

// ListSequences reads the sequence files this FPP holds. Read only.
func (c *Client) ListSequences(ctx context.Context) ([]string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+SequencesPath, nil)
	if err != nil {
		return nil, fmt.Errorf("fppcommand: building sequence list request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fppcommand: reading sequence list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSequenceListBytes))
	if err != nil {
		return nil, fmt.Errorf("fppcommand: reading sequence list: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fppcommand: reading sequence list: %w", &httpStatusError{StatusCode: resp.StatusCode})
	}
	var names []string
	if err := json.Unmarshal(body, &names); err != nil {
		return nil, fmt.Errorf("fppcommand: decoding sequence list: %w", err)
	}
	return names, nil
}
