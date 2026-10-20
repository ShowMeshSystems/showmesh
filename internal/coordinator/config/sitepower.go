package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// show.sitepower (ADR-065): the installation's shutdown sequences and its
// power-on action list. Singleton with an empty default, PUT is a full
// replacement.

const (
	ShowSitePowerConfigKind     = "show.sitepower"
	ShowSitePowerConfigObjectID = "default"
	ShowSitePowerSourceAPI      = "api"
)

const (
	shutdownSequenceMaxCount  = 32
	shutdownSequenceMaxSteps  = 20
	sitePowerOnActionMaxCount = 32
	shutdownStepMaxDelayMs    = 24 * 60 * 60 * 1000
)

var shutdownSequenceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ShutdownStep is either an action (Action set) or a wait (DelayMs set).
// PowerDomain marks an action that removes power; only "presentation" is
// accepted, so a sequence can never take out environmental protection.
type ShutdownStep struct {
	Action              string `json:"action,omitempty"`
	RequireConfirmation bool   `json:"requireConfirmation,omitempty"`
	PowerDomain         string `json:"powerDomain,omitempty"`
	DomainProvenance    string `json:"domainProvenance,omitempty"`
	DelayMs             int    `json:"delayMs,omitempty"`
}

// IsDelay reports whether s is a wait rather than an action.
func (s ShutdownStep) IsDelay() bool { return s.Action == "" }

type ShutdownSequence struct {
	ID    string         `json:"id"`
	Label string         `json:"label"`
	Steps []ShutdownStep `json:"steps"`
}

type SitePowerPayload struct {
	ShutdownSequences []ShutdownSequence `json:"shutdownSequences"`
	PowerOnActions    []string           `json:"powerOnActions"`
}

// SitePowerDefaultPayload is reported when nothing has been written.
var SitePowerDefaultPayload = SitePowerPayload{ShutdownSequences: []ShutdownSequence{}, PowerOnActions: []string{}}

// SequenceIDs returns every sequence id in p, in order.
func (p SitePowerPayload) SequenceIDs() []string {
	out := make([]string, 0, len(p.ShutdownSequences))
	for _, s := range p.ShutdownSequences {
		out = append(out, s.ID)
	}
	return out
}

// ActionCheck validates one referenced action id at field and returns a
// refusal, or nil when the action may be used there.
type ActionCheck func(field, actionID string) *ValidationError

// GlobalActionCheck accepts any existing show.action, of any show.
func GlobalActionCheck(exists EmergencyStopActionResolver) ActionCheck {
	return func(field, actionID string) *ValidationError {
		if exists == nil || !exists(actionID) {
			return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field, Detail: fmt.Sprintf("Action %s does not exist. Create it first.", actionID)}
		}
		return nil
	}
}

func EncodeSitePowerPayload(p SitePowerPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.sitepower payload: %w", err)
	}
	return string(b), nil
}

// DecodeSitePowerPayload parses and validates raw. Both keys may be
// omitted and mean an empty list.
func DecodeSitePowerPayload(raw string, exists EmergencyStopActionResolver) (SitePowerPayload, *ValidationError) {
	var p SitePowerPayload
	if verr := decodeStrict(raw, &p, "show.sitepower"); verr != nil {
		return SitePowerPayload{}, verr
	}
	if p.ShutdownSequences == nil {
		p.ShutdownSequences = []ShutdownSequence{}
	}
	if p.PowerOnActions == nil {
		p.PowerOnActions = []string{}
	}
	check := GlobalActionCheck(exists)
	if verr := ValidateShutdownSequences(p.ShutdownSequences, "shutdownSequences", check); verr != nil {
		return SitePowerPayload{}, verr
	}
	if verr := ValidateActionList(p.PowerOnActions, "powerOnActions", sitePowerOnActionMaxCount, check); verr != nil {
		return SitePowerPayload{}, verr
	}
	return p, nil
}

// decodeStrict unmarshals raw into out, refusing unknown keys and trailing data.
func decodeStrict(raw string, out any, what string) *ValidationError {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "json: unknown field ") {
			name := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
			return &ValidationError{Code: ValidationCodeFieldUnknownKey, Field: what, Detail: fmt.Sprintf("The settings have a field ShowMesh does not know, %s. Remove it.", name)}
		}
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: what, Detail: "The settings could not be read. Check the values and save again."}
	}
	if dec.More() {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: what, Detail: "The settings could not be read. Send one set of settings and save again."}
	}
	return nil
}

// ValidateActionList checks an ordered action list: no blanks, no repeats,
// at most max entries, every action accepted by check.
func ValidateActionList(actions []string, field string, max int, check ActionCheck) *ValidationError {
	if len(actions) > max {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s has more than %d actions. Remove some.", field, max)}
	}
	seen := map[string]bool{}
	for i, id := range actions {
		f := fmt.Sprintf("%s[%d]", field, i)
		if strings.TrimSpace(id) == "" {
			return &ValidationError{Code: ValidationCodeFieldEmpty, Field: f, Detail: f + " has no action. Pick one or remove the row."}
		}
		if seen[id] {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: f, Detail: fmt.Sprintf("Action %s is listed twice in %s. Remove one.", id, field)}
		}
		seen[id] = true
		if verr := check(f, id); verr != nil {
			return verr
		}
	}
	return nil
}

// ValidateShutdownSequences checks sequence ids, labels and every step.
func ValidateShutdownSequences(seqs []ShutdownSequence, field string, check ActionCheck) *ValidationError {
	if len(seqs) > shutdownSequenceMaxCount {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s has more than %d sequences. Remove some.", field, shutdownSequenceMaxCount)}
	}
	ids := map[string]bool{}
	for i, seq := range seqs {
		f := fmt.Sprintf("%s[%d]", field, i)
		if !shutdownSequenceIDPattern.MatchString(seq.ID) {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: f + ".id", Detail: fmt.Sprintf("Sequence id %q must be lowercase letters, digits and dashes. Change it.", seq.ID)}
		}
		if ids[seq.ID] {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: f + ".id", Detail: fmt.Sprintf("Sequence id %s is used twice. Rename one.", seq.ID)}
		}
		ids[seq.ID] = true
		if strings.TrimSpace(seq.Label) == "" {
			return &ValidationError{Code: ValidationCodeFieldEmpty, Field: f + ".label", Detail: fmt.Sprintf("Sequence %s has no name. Add one.", seq.ID)}
		}
		if len(seq.Steps) == 0 {
			return &ValidationError{Code: ValidationCodeFieldEmpty, Field: f + ".steps", Detail: fmt.Sprintf("Sequence %s has no steps. Add one or remove the sequence.", seq.Label)}
		}
		if len(seq.Steps) > shutdownSequenceMaxSteps {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: f + ".steps", Detail: fmt.Sprintf("Sequence %s has more than %d steps. Remove some.", seq.Label, shutdownSequenceMaxSteps)}
		}
		for j, step := range seq.Steps {
			if verr := validateShutdownStep(step, fmt.Sprintf("%s.steps[%d]", f, j), seq.Label, j+1, check); verr != nil {
				return verr
			}
		}
	}
	return nil
}

func validateShutdownStep(step ShutdownStep, field, seqLabel string, n int, check ActionCheck) *ValidationError {
	if step.IsDelay() {
		if step.RequireConfirmation || step.PowerDomain != "" || step.DomainProvenance != "" {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("Step %d of %s is a wait, so it cannot also confirm or remove power. Pick an action or clear those settings.", n, seqLabel)}
		}
		if step.DelayMs <= 0 || step.DelayMs > shutdownStepMaxDelayMs {
			return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".delayMs", Detail: fmt.Sprintf("Step %d of %s needs an action or a wait of 1 ms to 24 hours. Set one.", n, seqLabel)}
		}
		return nil
	}
	if step.DelayMs != 0 {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("Step %d of %s has both an action and a wait. Split it into two steps.", n, seqLabel)}
	}
	if verr := check(field+".action", step.Action); verr != nil {
		return verr
	}
	if step.PowerDomain == "" && step.DomainProvenance == "" {
		return nil
	}
	if step.PowerDomain != NightPowerDomainPresentation {
		return &ValidationError{Code: ValidationCodePowerDomainRefused, Field: field + ".powerDomain", Detail: fmt.Sprintf("Step %d of %s removes %s power. Only show equipment power can be removed by a sequence.", n, seqLabel, orUnset(step.PowerDomain))}
	}
	if step.DomainProvenance != NightDomainProvenanceOperatorDeclared {
		return &ValidationError{Code: ValidationCodeFieldInvalid, Field: field + ".domainProvenance", Detail: fmt.Sprintf("Step %d of %s removes power, so it must be marked operator-declared.", n, seqLabel)}
	}
	return nil
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

// SequencesExcept returns seqs without any whose id is in skip.
func SequencesExcept(seqs []ShutdownSequence, skip []string) []ShutdownSequence {
	drop := make(map[string]bool, len(skip))
	for _, id := range skip {
		drop[id] = true
	}
	out := make([]ShutdownSequence, 0, len(seqs))
	for _, s := range seqs {
		if !drop[s.ID] {
			out = append(out, s)
		}
	}
	return out
}
