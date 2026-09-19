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

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// This file is the node's half of ADR-053: it holds the weather delay
// state and alert plan, persisted so both survive an agent restart and a
// broker outage (decision 2), and it never goes stale. A node that last
// heard "active" stays delayed until it is explicitly told otherwise,
// either by the retained state topic reporting not active or by
// "weatherdelay.resume" running locally; silence from the broker is never
// read as a reason to clear it (ADR-053 decision 8's own "stays delayed
// until the coordinator returns"). This is the one deliberate difference
// from ShowModeHolder (showmode.go), which this file otherwise mirrors.

// weatherDelayStateSubdir is where the persisted state file lives, rooted
// at the agent's asset directory, matching heldcatalog.FileStore's and
// pipeline.AssignmentStore's identical convention.
const weatherDelayStateSubdir = "weather-delay-state"

// weatherDelayStateFile is the single file the persisted record lives in.
const weatherDelayStateFile = "state.json"

// weatherDelayRecord is the persisted shape: the state fields plus the
// alert plan, so a node that restarts (or that later receives only a
// signed HTTP start, with no MQTT reachable) already knows both whether
// it is delayed and what to play.
type weatherDelayRecord struct {
	Active    bool                       `json:"active"`
	Kind      string                     `json:"kind,omitempty"`
	StartedAt time.Time                  `json:"startedAt,omitzero"`
	StartedBy string                     `json:"startedBy,omitempty"`
	Revision  int64                      `json:"revision"`
	Plan      mqttproto.WeatherDelayPlan `json:"plan"`
}

// weatherDelayStore persists a [weatherDelayRecord] to one JSON file,
// atomically (temp file + rename), matching heldcatalog.FileStore.Save.
type weatherDelayStore struct {
	dir string
}

func newWeatherDelayStore(assetDir string) *weatherDelayStore {
	return &weatherDelayStore{dir: filepath.Join(assetDir, weatherDelayStateSubdir)}
}

func (s *weatherDelayStore) path() string {
	return filepath.Join(s.dir, weatherDelayStateFile)
}

func (s *weatherDelayStore) save(rec weatherDelayRecord) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("weatherdelay: create state directory: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("weatherdelay: encode state: %w", err)
	}
	target := s.path()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("weatherdelay: write state: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("weatherdelay: commit state: %w", err)
	}
	return nil
}

// load returns the persisted record, or the zero record when none has
// ever been saved (a fresh node). A corrupt file is reported as an error,
// never silently treated as "not active": the caller (NewWeatherDelayHolder)
// must not let a disk read failure masquerade as an honest "no delay"
// state, matching heldcatalog.FileStore.Load's identical rule.
func (s *weatherDelayStore) load() (weatherDelayRecord, bool, error) {
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return weatherDelayRecord{}, false, nil
		}
		return weatherDelayRecord{}, false, fmt.Errorf("weatherdelay: read state: %w", err)
	}
	var rec weatherDelayRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return weatherDelayRecord{}, false, fmt.Errorf("weatherdelay: decode state: %w", err)
	}
	return rec, true, nil
}

// WeatherDelayState is one answer about the weather delay, at the moment
// it was asked, mirroring ShowModeState's shape.
type WeatherDelayState struct {
	Active    bool
	Kind      string
	StartedAt time.Time
	StartedBy string
	Plan      mqttproto.WeatherDelayPlan
}

// WeatherDelayHolder holds this node's current view of the
// installation-wide weather delay state and alert plan, persisted so both
// survive a restart and a broker outage. Safe for concurrent use.
type WeatherDelayHolder struct {
	mu    sync.Mutex
	store *weatherDelayStore
	rec   weatherDelayRecord

	// alertStartMu serializes alert starts from every delivery path, and
	// alertMu guards alertKind, the kind of the alert most recently started.
	alertStartMu sync.Mutex
	alertMu      sync.Mutex
	alertKind    string
}

// NewWeatherDelayHolder constructs a holder and loads whatever this node
// last persisted, BEFORE anything that could start audio runs, see
// agent.go's own boot ordering. A load failure is logged by the caller and
// treated as not active, the same posture heldcatalog's own load failure
// takes toward its own state (a corrupt file is exactly as untrustworthy
// as no file, never silently kept as though it read clean).
func NewWeatherDelayHolder(assetDir string, logger *slog.Logger) *WeatherDelayHolder {
	store := newWeatherDelayStore(assetDir)
	h := &WeatherDelayHolder{store: store}
	rec, ok, err := store.load()
	if err != nil {
		logger.Warn("failed to load persisted weather delay state at startup; treating this node as not delayed", "error", err)
		return h
	}
	if ok {
		h.rec = rec
		if rec.Active {
			logger.Warn("resuming with a weather delay already active from before this node's last restart", "kind", rec.Kind, "started_at", rec.StartedAt, "started_by", rec.StartedBy)
		}
	}
	return h
}

// Current reports the weather delay state as of right now. Every consumer
// must call this at its own point of decision, never hold its result.
func (h *WeatherDelayHolder) Current() WeatherDelayState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return WeatherDelayState{
		Active: h.rec.Active, Kind: h.rec.Kind, StartedAt: h.rec.StartedAt,
		StartedBy: h.rec.StartedBy, Plan: h.rec.Plan,
	}
}

// persistLocked writes the current record to disk, best effort: a failed
// write is logged by the caller (every caller here already holds a
// logger) but never blocks the in-memory state from taking effect, the
// in-memory holder is the value every consumer actually reads at its
// point of decision, and disk is only how it survives a restart.
func (h *WeatherDelayHolder) persistLocked() error {
	return h.store.save(h.rec)
}

// SetFromMessage applies a [mqttproto.WeatherDelayMessage] received on the
// retained state topic: the coordinator's own state, one of ADR-053
// decision 8's parallel delivery paths. An active message sets active
// regardless of whatever this node already held (a later message, whether
// it changes kind or not, still applies); an inactive message clears it.
// The plan is stored from EVERY message, active or not, per
// [mqttproto.WeatherDelayPlan]'s own doc comment: "sent on every state
// message so a node that later gets only a signed start already knows
// what to play."
func (h *WeatherDelayHolder) SetFromMessage(msg mqttproto.WeatherDelayMessage, logger *slog.Logger) {
	h.mu.Lock()
	previouslyActive := h.rec.Active
	h.rec = weatherDelayRecord{
		Active: msg.Active, Kind: msg.Kind, StartedAt: msg.StartedAt, StartedBy: msg.StartedBy,
		Revision: msg.Revision, Plan: msg.Plan,
	}
	err := h.persistLocked()
	h.mu.Unlock()

	if err != nil {
		logger.Warn("failed to persist weather delay state received from the coordinator", "error", err)
	}
	if msg.Active != previouslyActive {
		logger.Warn("weather delay state changed", "active", msg.Active, "kind", msg.Kind, "source", "coordinator state topic")
	}
}

// SetActiveLocal marks the delay active from a node-local trigger, the
// "weatherdelay.start" operation or the signed HTTP start (ADR-053
// decision 8's other two parallel paths, neither of which carries a
// coordinator revision). Already active: only Kind is updated (decision 1:
// "a delay can be changed to a cancel while it is active"), StartedAt and
// StartedBy are left as the original activation's. Not yet active: both
// are set fresh. Either way the change is persisted before this call
// returns, so a crash immediately after still resumes delayed.
func (h *WeatherDelayHolder) SetActiveLocal(kind string, startedAt time.Time, startedBy string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rec.Active {
		h.rec.Kind = kind
	} else {
		h.rec.Active = true
		h.rec.Kind = kind
		h.rec.StartedAt = startedAt
		h.rec.StartedBy = startedBy
	}
	return h.persistLocked()
}

// ClearLocal marks the delay not active from a node-local
// "weatherdelay.resume". The alert plan is kept: a resumed-then-restarted
// delay may need it again, and nothing about a resume implies the plan
// itself is no longer valid.
func (h *WeatherDelayHolder) ClearLocal() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.Active = false
	h.rec.Kind = ""
	h.rec.StartedAt = time.Time{}
	h.rec.StartedBy = ""
	return h.persistLocked()
}

// registerWeatherDelay binds holder to receive the retained weather delay
// state topic and subscribes to it, both fresh on every call (every
// OnConnectionUp), matching registerShowMode's identical reconnect
// reasoning: autopaho hands back a brand new underlying client with no
// memory of a prior SUBSCRIBE or callback registration.
func registerWeatherDelay(ctx context.Context, cm *autopaho.ConnectionManager, holder *WeatherDelayHolder, logger *slog.Logger) {
	topic := mqttproto.WeatherDelayTopic()

	cm.AddOnPublishReceived(func(pr autopaho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != topic {
			return false, nil
		}
		msg, err := mqttproto.DecodeWeatherDelayMessage(pr.Packet.Payload)
		if err != nil {
			// A malformed message is not a reason to change the delay
			// state: this node keeps whatever it already held, the same
			// posture ShowModeHolder's own malformed-message handling
			// takes, and the one this state's own "silence never clears
			// it" rule requires even more strongly here.
			logger.Warn("ignoring a malformed weather delay message; keeping the state already held",
				"topic", topic, "error", err)
			return true, nil
		}
		holder.SetFromMessage(msg, logger)
		return true, nil
	})

	go func() {
		subCtx, cancel := context.WithTimeout(ctx, cmdSubscribeTimeout)
		defer cancel()

		suback, err := cm.Subscribe(subCtx, &paho.Subscribe{
			Subscriptions: []paho.SubscribeOptions{
				{Topic: topic, QoS: mqttproto.WeatherDelayDeliveryPolicy.QoS},
			},
		})
		if err != nil {
			logger.Warn("failed to subscribe to the weather delay topic; this node keeps the state it already holds",
				"topic", topic, "error", err)
			return
		}
		if len(suback.Reasons) == 0 || suback.Reasons[0] >= 0x80 {
			logger.Warn("broker rejected the weather delay topic subscription; this node keeps the state it already holds",
				"topic", topic, "reasons", suback.Reasons)
			return
		}
		logger.Info("subscribed to the weather delay topic", "topic", topic)
	}()
}

// weatherDelayActiveReason is the operator-readable reason cue activation
// (cueactivationops.go) reports while a weather delay is active.
const weatherDelayActiveReason = "A weather delay is active. Resume the show to activate Cues."

// weatherDelayAlertAssetForKind selects plan's asset reference for kind,
// or nil when none is configured. kind is assumed already validated
// against [weatherdelay.ValidKind].
func weatherDelayAlertAssetForKind(plan mqttproto.WeatherDelayPlan, kind string) *mqttproto.WeatherDelayAlertAssetRef {
	switch kind {
	case weatherdelay.KindDelay:
		return plan.Delay
	case weatherdelay.KindCancelNight:
		return plan.CancelNight
	default:
		return nil
	}
}
