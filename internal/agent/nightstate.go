package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The coordinator's last word on whether a night session is open (ADR-067),
// persisted so it survives a restart and coordinator or broker loss.

const nightStateSubdir = "night-state"

const nightStateFile = "state.json"

var nightStateSetKnownKeys = map[string]bool{"open": true}

type nightStateRecord struct {
	Open bool `json:"open"`
}

// NightStateHolder holds the node's night flag. A node that never received
// one treats the night as open, so test triggers stay off. Safe for
// concurrent use.
type NightStateHolder struct {
	dir    string
	logger *slog.Logger

	mu   sync.Mutex
	open bool
}

// NewNightStateHolder loads the persisted flag. A missing or unreadable file
// means open.
func NewNightStateHolder(assetDir string, logger *slog.Logger) *NightStateHolder {
	h := &NightStateHolder{dir: filepath.Join(assetDir, nightStateSubdir), logger: logger, open: true}
	data, err := os.ReadFile(h.path())
	if err != nil {
		if !os.IsNotExist(err) && logger != nil {
			logger.Warn("failed to read the persisted night state; treating the night as open", "error", err)
		}
		return h
	}
	var rec nightStateRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		if logger != nil {
			logger.Warn("failed to decode the persisted night state; treating the night as open", "error", err)
		}
		return h
	}
	h.open = rec.Open
	return h
}

func (h *NightStateHolder) path() string { return filepath.Join(h.dir, nightStateFile) }

// Open reports whether a night session is open as far as this node knows.
// A nil holder answers open.
func (h *NightStateHolder) Open() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open
}

// set is the "night.state.set" operation: params {"open": bool}.
func (h *NightStateHolder) set(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "night.state.set"
	if err := rejectUnknownKeys(action, params, nightStateSetKnownKeys); err != nil {
		return OperationResult{}, err
	}
	open, ok := params["open"].(bool)
	if !ok {
		return OperationResult{}, fmt.Errorf("%s: params.open must be a boolean", action)
	}
	h.mu.Lock()
	changed := h.open != open
	h.open = open
	err := h.saveLocked()
	h.mu.Unlock()
	executedAt := now()
	if err != nil {
		return OperationResult{}, fmt.Errorf("%s: persisting night state: %w", action, err)
	}
	if changed && h.logger != nil {
		h.logger.Info("night state changed", "open", open)
	}
	return OperationResult{Confirmed: h.Open() == open, Signal: "node.night.open", Value: open, ExecutedAt: executedAt, ObservedAt: now()}, nil
}

// saveLocked replaces the state file through a rename so a crash leaves the
// old or the new value. The caller holds h.mu.
func (h *NightStateHolder) saveLocked() error {
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(nightStateRecord{Open: h.open})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.dir, nightStateFile+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), h.path()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
