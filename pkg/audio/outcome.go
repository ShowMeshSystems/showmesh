package audio

import (
	"errors"
	"fmt"
)

// Outcome is what a command confirmation actually observed. Unconfirmable
// is a first-class success-adjacent outcome: an effect that cannot be
// observed reports Unconfirmable with its reason, never Started.
type Outcome string

const (
	OutcomeStarted       Outcome = "started"
	OutcomePosition      Outcome = "position"
	OutcomeGain          Outcome = "gain"
	OutcomeFadeComplete  Outcome = "fade_complete"
	OutcomeStopped       Outcome = "stopped"
	OutcomeCompleted     Outcome = "completed"
	OutcomeRefused       Outcome = "refused"
	OutcomeFailed        Outcome = "failed"
	OutcomeUnconfirmable Outcome = "unconfirmable"
)

var outcomes = map[string]struct{}{
	string(OutcomeStarted): {}, string(OutcomePosition): {}, string(OutcomeGain): {},
	string(OutcomeFadeComplete): {}, string(OutcomeStopped): {}, string(OutcomeCompleted): {},
	string(OutcomeRefused): {}, string(OutcomeFailed): {}, string(OutcomeUnconfirmable): {},
}

// Validate reports whether o is one of the nine reserved outcomes.
func (o Outcome) Validate() error {
	return closedSet("audio.Outcome", string(o), outcomes)
}

// outcomesRequiringReason must carry a non-empty Reason: Refused, Failed,
// and Unconfirmable. Every other member is an observation, not a
// failure, and Reason on those is permitted but not required.
var outcomesRequiringReason = map[Outcome]struct{}{
	OutcomeRefused:       {},
	OutcomeFailed:        {},
	OutcomeUnconfirmable: {},
}

// OutcomeResult pairs a confirmed [Outcome] with its evidence.
type OutcomeResult struct {
	Outcome Outcome
	Reason  string
}

// ErrOutcomeReasonRequired is returned by [OutcomeResult.Validate] when
// Refused, Failed, or Unconfirmable carries an empty Reason.
var ErrOutcomeReasonRequired = errors.New("audio: outcome requires a non-empty reason")

// Validate reports whether r.Outcome is a known member and, for Refused,
// Failed, or Unconfirmable, that r.Reason is non-empty.
func (r OutcomeResult) Validate() error {
	if err := r.Outcome.Validate(); err != nil {
		return err
	}
	if _, needsReason := outcomesRequiringReason[r.Outcome]; needsReason && r.Reason == "" {
		return fmt.Errorf("%w: outcome %q", ErrOutcomeReasonRequired, r.Outcome)
	}
	return nil
}
