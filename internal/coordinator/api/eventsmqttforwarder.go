package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const (
	eventsMQTTPollInterval  = time.Second
	eventsMQTTPublishWait   = 5 * time.Second
	eventsMQTTWarnInterval  = time.Minute
	eventsMQTTBatchLimit    = 500
	eventsMQTTEventQoS      = 1
	eventsMQTTNightStateQoS = 1
)

// EventsMQTTForwarder publishes the event history and the night state to the integration broker named by events.mqtt.
type EventsMQTTForwarder struct {
	h        *handlers
	interval time.Duration

	started   bool
	cursor    uint64
	published string // broker, prefix and state last published; empty until something is
	lastWarn  time.Time
}

// NewEventsMQTTForwarder builds the forwarder from the same dependencies as the API.
func NewEventsMQTTForwarder(deps Dependencies, opts Options) *EventsMQTTForwarder {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &EventsMQTTForwarder{h: newBackgroundHandlers(deps, opts), interval: eventsMQTTPollInterval}
}

// Run polls until ctx is done. A broker problem is logged and the message dropped; nothing here is waited on by a command.
func (f *EventsMQTTForwarder) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		f.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (f *EventsMQTTForwarder) tick(ctx context.Context) {
	settings, _, _, _, err := storedEventsMQTT(ctx, f.h.deps.Config)
	if err != nil {
		f.warn("events mqtt: read setting failed", "error", err)
		return
	}
	latest, err := f.h.deps.Events.LatestEventSeq(ctx)
	if err != nil {
		f.warn("events mqtt: read latest event seq failed", "error", err)
		return
	}
	if !f.started {
		f.cursor, f.started = latest, true
	}
	if !settings.Enabled || settings.Broker == "" {
		f.cursor, f.published = latest, ""
		return
	}
	f.forwardEvents(ctx, settings, latest)
	f.forwardNightState(ctx, settings)
}

func (f *EventsMQTTForwarder) forwardEvents(ctx context.Context, s config.EventsMQTT, latest uint64) {
	if latest <= f.cursor {
		return
	}
	records, gap, err := f.h.deps.Events.ListEvents(ctx, f.cursor, eventsMQTTBatchLimit)
	if err != nil {
		f.warn("events mqtt: list events failed", "error", err)
		return
	}
	for _, rec := range records {
		ev := mapEvent(rec)
		if payload, merr := json.Marshal(ev); merr != nil {
			f.warn("events mqtt: encode event failed", "seq", rec.Seq, "error", merr)
		} else if perr := f.publish(ctx, s.Broker, s.TopicPrefix+"/events/"+rec.Category, eventsMQTTEventQoS, false, payload); perr != nil {
			f.h.logWarn("events mqtt: dropped an event", "seq", rec.Seq, "category", rec.Category, "error", perr)
		}
		f.cursor = max(f.cursor, rec.Seq)
	}
	if gap && len(records) == 0 {
		f.cursor = latest
	}
}

type eventsMQTTNightState struct {
	State     string               `json:"state"`
	SessionID string               `json:"sessionId"`
	Cycle     int64                `json:"cycle"`
	Degraded  bool                 `json:"degraded"`
	Readiness *eventsMQTTReadiness `json:"readiness"`
	At        string               `json:"at"`
}

type eventsMQTTReadiness struct {
	Outcome     string `json:"outcome"`
	CompletedAt string `json:"completedAt"`
}

func (f *EventsMQTTForwarder) nightState(ctx context.Context, now time.Time) (eventsMQTTNightState, error) {
	st := eventsMQTTNightState{State: "inactive", At: formatTime(now)}
	cur, ok, err := f.h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil || !ok {
		return st, err
	}
	st.State, st.SessionID, st.Cycle, st.Degraded = cur.State, cur.ID, cur.Cycle, cur.Degraded
	rd, err := f.h.deps.NightSessions.GetLatestNightReadiness(ctx, cur.ID)
	switch {
	case err == nil:
		st.Readiness = &eventsMQTTReadiness{Outcome: rd.Outcome, CompletedAt: formatTime(rd.CompletedAt)}
	case !errors.Is(err, store.ErrNightReadinessNotFound):
		return st, err
	}
	return st, nil
}

// forwardNightState publishes the retained state when it, the broker or the prefix changed since the last successful publish.
func (f *EventsMQTTForwarder) forwardNightState(ctx context.Context, s config.EventsMQTT) {
	now := f.h.now()
	st, err := f.nightState(ctx, now)
	if err != nil {
		f.warn("events mqtt: read night state failed", "error", err)
		return
	}
	st.At = ""
	keyJSON, _ := json.Marshal(st)
	key := s.Broker + "\x00" + s.TopicPrefix + "\x00" + string(keyJSON)
	if key == f.published {
		return
	}
	st.At = formatTime(now)
	payload, _ := json.Marshal(st)
	if err := f.publish(ctx, s.Broker, s.TopicPrefix+"/night/state", eventsMQTTNightStateQoS, true, payload); err != nil {
		f.warn("events mqtt: night state not published", "error", err)
		return
	}
	f.published = key
}

func (f *EventsMQTTForwarder) publish(ctx context.Context, brokerID, topic string, qos byte, retain bool, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, eventsMQTTPublishWait)
	defer cancel()
	return f.h.deps.MQTTBrokers.Publish(ctx, brokerID, topic, qos, retain, payload)
}

// warn logs at most once a minute, so a broker that stays down does not fill the log.
func (f *EventsMQTTForwarder) warn(msg string, args ...any) {
	if now := f.h.now(); now.Sub(f.lastWarn) >= eventsMQTTWarnInterval {
		f.lastWarn = now
		f.h.logWarn(msg, args...)
	}
}
