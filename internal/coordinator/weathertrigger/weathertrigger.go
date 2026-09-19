// Package weathertrigger holds ADR-053 decision 12's automatic weather
// delay triggers: the two trigger kinds, the pure question/reason logic a
// trigger event turns into, and the built-in poller of the United States
// National Weather Service alerts API. It has no store or HTTP-server
// access; internal/coordinator/api owns persistence, the endpoints, and the
// webhook.
package weathertrigger

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// The two trigger kinds a source reports.
const (
	KindWarning   = "warning"
	KindLightning = "lightning"
)

var validKinds = map[string]bool{KindWarning: true, KindLightning: true}

// ValidKind reports whether kind is a member of the closed vocabulary.
func ValidKind(kind string) bool { return validKinds[kind] }

// The two questions a pending decision can ask.
const (
	QuestionDelay         = "delay"
	QuestionDelayOrCancel = "delayOrCancel"
)

// The two actions a pending decision defaults to, and an operator can
// answer with. AnswerDismiss is a third answer that runs neither.
const (
	ActionDelay       = "delay"
	ActionCancelNight = "cancelNight"
)

// The three answers POST /weather-delay/decision accepts.
const (
	AnswerDelay       = ActionDelay
	AnswerCancelNight = ActionCancelNight
	AnswerDismiss     = "dismiss"
)

// ErrInvalidTriggerEvent is wrapped by every error [TriggerEvent.Validate] returns.
var ErrInvalidTriggerEvent = errors.New("weathertrigger: invalid trigger event")

// TriggerEvent is one source's report, independent of how it arrived: the
// built-in NWS poller and the inbound POST /weather-delay/triggers/{source}
// endpoint both build one. ShowMesh reads only Kind, EventType, Severity,
// and ExpiresAt from a warning (ADR-053 decision 12); it never carries or
// stores a warning's headline, description, or instruction text.
type TriggerEvent struct {
	// Source names who reported this: a configured inbound source id, or
	// "nws" for the built-in poller.
	Source string
	// Kind is [KindWarning] or [KindLightning].
	Kind string
	// EventType is the warning's type (e.g. "Severe Thunderstorm
	// Warning"). Empty is accepted for a warning whose source did not
	// report one; [BuildReason] falls back to a generic sentence.
	EventType string
	// Severity is read but never placed in an operator-visible sentence.
	Severity string
	// ExpiresAt is the warning's own expiry, nil when unknown or for a
	// lightning report.
	ExpiresAt *time.Time
	// DistanceKm is a lightning report's distance, nil when unknown or
	// for a warning.
	DistanceKm *float64
	// SuggestCancel is set by an inbound source that itself knows tonight's
	// schedule and judges too little of it would remain: it is the only
	// way a trigger reaches the delayOrCancel question, since the
	// coordinator has no calendar or time zone of its own (ADR-038
	// decision 1: FPP is the sole calendar authority).
	SuggestCancel bool
	// ExternalID is the source's own identifier for this exact warning
	// (an NWS alert id), empty for a manually reported trigger. It is
	// never shown to an operator; it exists only so the same warning asks
	// once (ADR-053 decision 12).
	ExternalID string
}

// Validate checks Source is non-empty and Kind is a member of the closed
// vocabulary. It does not validate Source's syntax; a caller with node-id
// syntax rules (the coordinator API) applies those separately.
func (e TriggerEvent) Validate() error {
	if e.Source == "" {
		return fmt.Errorf("%w: source is empty", ErrInvalidTriggerEvent)
	}
	if !ValidKind(e.Kind) {
		return fmt.Errorf("%w: kind %q must be %q or %q", ErrInvalidTriggerEvent, e.Kind, KindWarning, KindLightning)
	}
	return nil
}

// WarningKey identifies "the same warning" for ADR-053 decision 12's
// dismiss suppression: a source, kind, and (for a warning) event type.
// Two warnings of different types from the same source are different
// warnings even if both are active at once.
func WarningKey(e TriggerEvent) string {
	if e.Kind == KindWarning {
		eventType := e.EventType
		if eventType == "" {
			eventType = "unspecified"
		}
		return e.Source + ":warning:" + eventType
	}
	return e.Source + ":lightning"
}

// BuildReason builds the operator-visible sentence for e: fact only, no
// clock time (an owner ruling superseding the brief this package was built
// from: ADR-038 leaves the coordinator with no calendar or time zone to
// render one correctly), and never the warning's own text.
func BuildReason(e TriggerEvent) string {
	switch e.Kind {
	case KindWarning:
		if e.EventType == "" {
			return "A weather warning is in effect."
		}
		return "A " + strings.ToLower(e.EventType) + " is in effect."
	case KindLightning:
		if e.DistanceKm != nil {
			miles := kmToMiles(*e.DistanceKm)
			unit := "miles"
			if miles == 1 {
				unit = "mile"
			}
			return fmt.Sprintf("Lightning was reported %d %s away.", miles, unit)
		}
		return "Lightning was reported nearby."
	default:
		return "A weather trigger fired."
	}
}

// kmToMiles rounds to the nearest whole mile; a lightning distance is
// approximate to begin with, so a fraction adds nothing an operator can act
// on.
func kmToMiles(km float64) int {
	return int(math.Round(km * 0.621371))
}

// ClassifyQuestion is ADR-053 decision 12's question logic, reduced to what
// the coordinator can actually know (owner ruling): every trigger asks
// "delay" unless it says outright that the night is lost, which only
// suggestCancel (set by a source that itself knows tonight's schedule) can
// do. The coordinator computes no schedule of its own.
func ClassifyQuestion(suggestCancel bool) (question, defaultAction string) {
	if suggestCancel {
		return QuestionDelayOrCancel, ActionCancelNight
	}
	return QuestionDelay, ActionDelay
}

// PendingDecision is ADR-053 decision 12's one outstanding trigger
// question: one at a time, persisted so a coordinator restart does not
// lose its deadline.
type PendingDecision struct {
	ID            string
	Source        string
	Reason        string
	Question      string
	DefaultAction string
	AskedAt       time.Time
	Deadline      time.Time
}

// NewPendingDecision builds the pending decision e raises: its question and
// default action from [ClassifyQuestion], its deadline askedAt plus
// answerWindowSeconds (or cancelAnswerWindowSeconds for delayOrCancel).
func NewPendingDecision(id string, e TriggerEvent, askedAt time.Time, answerWindowSeconds, cancelAnswerWindowSeconds int) PendingDecision {
	question, defaultAction := ClassifyQuestion(e.SuggestCancel)
	windowSeconds := answerWindowSeconds
	if question == QuestionDelayOrCancel {
		windowSeconds = cancelAnswerWindowSeconds
	}
	return PendingDecision{
		ID: id, Source: e.Source, Reason: BuildReason(e), Question: question, DefaultAction: defaultAction,
		AskedAt: askedAt, Deadline: askedAt.Add(time.Duration(windowSeconds) * time.Second),
	}
}
