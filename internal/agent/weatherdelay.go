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

// The node's weather delay state and alert plan, persisted so both survive a
// restart and a broker outage. Silence never clears an active delay: only a
// newer not-active state message or weatherdelay.resume does.

const weatherDelayStateSubdir = "weather-delay-state"

const weatherDelayStateFile = "state.json"

// weatherDelayRecord is the persisted shape. Revision is the newest state
// message revision seen; HeldRevision is the revision held when the delay
// last became active, and only a not-active message newer than it clears.
type weatherDelayRecord struct {
	Active       bool                       `json:"active"`
	Kind         string                     `json:"kind,omitempty"`
	StartedAt    time.Time                  `json:"startedAt,omitzero"`
	StartedBy    string                     `json:"startedBy,omitempty"`
	Revision     int64                      `json:"revision"`
	HeldRevision int64                      `json:"heldRevision"`
	Plan         mqttproto.WeatherDelayPlan `json:"plan"`
}

type weatherDelayStore struct {
	dir string
}

func newWeatherDelayStore(assetDir string) *weatherDelayStore {
	return &weatherDelayStore{dir: filepath.Join(assetDir, weatherDelayStateSubdir)}
}

func (s *weatherDelayStore) path() string {
	return filepath.Join(s.dir, weatherDelayStateFile)
}

// save replaces the state file atomically and durably: a crash leaves either
// the old record or the new one on disk, never a partial one.
func (s *weatherDelayStore) save(rec weatherDelayRecord) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("weatherdelay: create state directory: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("weatherdelay: encode state: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, weatherDelayStateFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("weatherdelay: create temp state: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("weatherdelay: write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("weatherdelay: sync state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("weatherdelay: close state: %w", err)
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		return fmt.Errorf("weatherdelay: commit state: %w", err)
	}
	committed = true
	dir, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("weatherdelay: open state directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("weatherdelay: sync state directory: %w", err)
	}
	return nil
}

// load returns the persisted record, false when none was ever saved, or an
// error when the file cannot be read or decoded.
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
// it was asked.
type WeatherDelayState struct {
	Active    bool
	Kind      string
	StartedAt time.Time
	StartedBy string
	Plan      mqttproto.WeatherDelayPlan
}

// WeatherDelayHolder holds this node's view of the weather delay state and
// alert plan. Safe for concurrent use.
type WeatherDelayHolder struct {
	mu     sync.Mutex
	store  *weatherDelayStore
	rec    weatherDelayRecord
	logger *slog.Logger

	// alertStartMu serializes alert starts and stops from every delivery
	// path; alertMu guards alertKind, the kind of the alert last started.
	alertStartMu sync.Mutex
	alertMu      sync.Mutex
	alertKind    string
}

// NewWeatherDelayHolder loads what this node last persisted and must run
// before anything can start audio. A file that cannot be read or decoded is
// logged and treated as not active.
func NewWeatherDelayHolder(assetDir string, logger *slog.Logger) *WeatherDelayHolder {
	store := newWeatherDelayStore(assetDir)
	h := &WeatherDelayHolder{store: store, logger: logger}
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

// persistLocked writes the record and logs a failure at error level. The
// in-memory state has already taken effect either way.
func (h *WeatherDelayHolder) persistLocked() error {
	err := h.store.save(h.rec)
	if err != nil {
		h.log().Error("failed to persist weather delay state; a restart before the next successful write would lose it", "active", h.rec.Active, "error", err)
	}
	return err
}

func (h *WeatherDelayHolder) log() *slog.Logger {
	if h.logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return h.logger
}

// weatherDelayTransition is what a state message did to the holder.
type weatherDelayTransition int

const (
	weatherDelayUnchanged weatherDelayTransition = iota
	weatherDelayStarted
	weatherDelayCleared
	// weatherDelayKindChanged is an active delay changed to a cancel in
	// place: the alert changes, StartedAt does not.
	weatherDelayKindChanged
)

// SetFromMessage applies a retained state message. Active starts a delay only
// when none is active; not active clears only when its revision is newer than
// the one held when the delay became active. The plan is kept from every message.
func (h *WeatherDelayHolder) SetFromMessage(msg mqttproto.WeatherDelayMessage) weatherDelayTransition {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.Plan = msg.Plan
	if msg.Revision > h.rec.Revision {
		h.rec.Revision = msg.Revision
	}
	transition := weatherDelayUnchanged
	switch {
	case msg.Active && !h.rec.Active:
		h.rec.Active = true
		h.rec.Kind = msg.Kind
		h.rec.StartedAt = msg.StartedAt
		h.rec.StartedBy = msg.StartedBy
		h.rec.HeldRevision = msg.Revision
		transition = weatherDelayStarted
	case msg.Active && h.rec.Active && msg.Kind == weatherdelay.KindDelay && h.rec.Kind == weatherdelay.KindCancelNight && msg.Revision <= h.rec.HeldRevision:
		h.log().Warn("ignoring a delay state message that is not newer than the cancelled night", "revision", msg.Revision, "held_revision", h.rec.HeldRevision)
	case msg.Active && h.rec.Active && msg.Kind != h.rec.Kind:
		// Changed in place: StartedAt stays the delay's original start: the
		// active window never ended. HeldRevision advances so an older
		// not-active message still cannot clear it.
		h.rec.Kind = msg.Kind
		h.rec.StartedBy = msg.StartedBy
		h.rec.HeldRevision = msg.Revision
		transition = weatherDelayKindChanged
	case !msg.Active && h.rec.Active && msg.Revision > h.rec.HeldRevision:
		h.clearLocked()
		transition = weatherDelayCleared
	case !msg.Active && h.rec.Active:
		h.log().Warn("ignoring a weather delay state message older than the active delay", "revision", msg.Revision, "held_revision", h.rec.HeldRevision)
	}
	_ = h.persistLocked()
	if transition != weatherDelayUnchanged {
		h.log().Warn("weather delay state changed", "active", h.rec.Active, "kind", msg.Kind, "source", "coordinator state topic")
	}
	return transition
}

// SetActiveLocal marks the delay active from weatherdelay.start or the signed
// HTTP start, recording the newest revision seen as the held one. Already
// active: only Kind changes. Persisted before it returns.
func (h *WeatherDelayHolder) SetActiveLocal(kind string, startedAt time.Time, startedBy string) error {
	_, err := h.setActiveLocal(kind, startedAt, startedBy)
	return err
}

// setActiveLocal is SetActiveLocal returning the kind now held: a delay
// start never downgrades a cancelled night.
func (h *WeatherDelayHolder) setActiveLocal(kind string, startedAt time.Time, startedBy string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.rec.Active {
		h.rec.Active = true
		h.rec.StartedAt = startedAt
		h.rec.StartedBy = startedBy
		h.rec.HeldRevision = h.rec.Revision
	}
	if h.rec.Kind != weatherdelay.KindCancelNight || kind != weatherdelay.KindDelay {
		h.rec.Kind = kind
	}
	return h.rec.Kind, h.persistLocked()
}

// ClearLocal marks the delay not active from weatherdelay.resume, whatever
// revision is held. The alert plan is kept.
func (h *WeatherDelayHolder) ClearLocal() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clearLocked()
	return h.persistLocked()
}

func (h *WeatherDelayHolder) clearLocked() {
	h.rec.Active = false
	h.rec.Kind = ""
	h.rec.StartedAt = time.Time{}
	h.rec.StartedBy = ""
}

// registerWeatherDelay subscribes ops to the retained state topic, fresh on
// every connect, since autopaho's new client remembers no prior subscription.
func registerWeatherDelay(ctx context.Context, cm *autopaho.ConnectionManager, ops *weatherDelayOperations, logger *slog.Logger) {
	topic := mqttproto.WeatherDelayTopic()

	cm.AddOnPublishReceived(func(pr autopaho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != topic {
			return false, nil
		}
		msg, err := mqttproto.DecodeWeatherDelayMessage(pr.Packet.Payload)
		if err != nil {
			logger.Warn("ignoring a malformed weather delay message; keeping the state already held",
				"topic", topic, "error", err)
			return true, nil
		}
		transition := ops.holder.SetFromMessage(msg)
		go ops.react(context.WithoutCancel(ctx), transition, msg.Kind)
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

// weatherDelayActiveReason is what cue activation reports during a delay.
const weatherDelayActiveReason = "A weather delay is active. Resume the show to activate Cues."

// weatherDelayAlertAssetForKind selects plan's alert asset for kind, or nil
// when none is configured.
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
