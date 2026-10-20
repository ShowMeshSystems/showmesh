package config

import (
	"encoding/json"
	"fmt"
)

// night.session.siteActions (ADR-065): prepare-site actions, the night's
// power down choice over the global shutdown sequences, and end session
// actions. Replaces siteControl, which is still read and translated.

const nightSiteActionMaxCount = 32

// NightSitePowerDown picks the global sequences a night runs and adds its own.
type NightSitePowerDown struct {
	SkipSequences       []string           `json:"skipSequences,omitempty"`
	AdditionalSequences []ShutdownSequence `json:"additionalSequences,omitempty"`
}

type NightSiteActions struct {
	PrepareSite []string            `json:"prepareSite,omitempty"`
	PowerDown   *NightSitePowerDown `json:"powerDown,omitempty"`
	EndSession  []string            `json:"endSession,omitempty"`
}

// PowerDownSequences returns the sequences this night runs at power down:
// the global ones it does not skip, then its own.
func (a *NightSiteActions) PowerDownSequences(global []ShutdownSequence) []ShutdownSequence {
	if a == nil || a.PowerDown == nil {
		return append([]ShutdownSequence(nil), global...)
	}
	return append(SequencesExcept(global, a.PowerDown.SkipSequences), a.PowerDown.AdditionalSequences...)
}

// nightActionCheck accepts only an existing action of the session's own show.
func nightActionCheck(sessionShow string, resolver ActionResolver) ActionCheck {
	return func(field, actionID string) *ValidationError {
		if resolver == nil {
			return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field, Detail: "Actions cannot be checked right now. Try again."}
		}
		show, ok := resolver(actionID)
		if !ok {
			return &ValidationError{Code: ValidationCodeFieldUnknownReference, Field: field, Detail: fmt.Sprintf("Action %s does not exist. Create it first.", actionID)}
		}
		if show != sessionShow {
			return &ValidationError{Code: ValidationCodeCrossShowReference, Field: field, Detail: fmt.Sprintf("Action %s belongs to show %s. Pick an action from show %s.", actionID, show, sessionShow)}
		}
		return nil
	}
}

// decodeNightSiteActions reads siteActions, or translates a legacy
// siteControl block. Both at once is refused.
func decodeNightSiteActions(top map[string]json.RawMessage, sessionShow string, resolver ActionResolver) (*NightSiteActions, *ValidationError) {
	raw, has := top["siteActions"]
	_, hasLegacy := top["siteControl"]
	if has && hasLegacy {
		return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: "siteControl", Detail: "Site control was replaced by site actions. Remove site control."}
	}
	if hasLegacy {
		sc, verr := decodeNightSiteControl(top, sessionShow, resolver)
		if verr != nil || sc == nil {
			return nil, verr
		}
		return siteActionsFromLegacy(sc), nil
	}
	if !has || isJSONNull(raw) {
		return nil, nil
	}
	var a NightSiteActions
	if verr := decodeStrict(string(raw), &a, "siteActions"); verr != nil {
		return nil, verr
	}
	check := nightActionCheck(sessionShow, resolver)
	if verr := ValidateActionList(a.PrepareSite, "siteActions.prepareSite", nightSiteActionMaxCount, check); verr != nil {
		return nil, verr
	}
	if verr := ValidateActionList(a.EndSession, "siteActions.endSession", nightSiteActionMaxCount, check); verr != nil {
		return nil, verr
	}
	if a.PowerDown != nil {
		seen := map[string]bool{}
		for i, id := range a.PowerDown.SkipSequences {
			if !shutdownSequenceIDPattern.MatchString(id) || seen[id] {
				return nil, &ValidationError{Code: ValidationCodeFieldInvalid, Field: fmt.Sprintf("siteActions.powerDown.skipSequences[%d]", i), Detail: fmt.Sprintf("Sequence %s cannot be skipped twice or by that name. Fix the list.", id)}
			}
			seen[id] = true
		}
		if verr := ValidateShutdownSequences(a.PowerDown.AdditionalSequences, "siteActions.powerDown.additionalSequences", check); verr != nil {
			return nil, verr
		}
	}
	if len(a.PrepareSite) == 0 && len(a.EndSession) == 0 && a.PowerDown == nil {
		return nil, nil
	}
	return &a, nil
}

// siteActionsFromLegacy maps siteControl onto siteActions: the thermal
// profile becomes the prepare-site action, power-off becomes a night-only
// sequence, and power-on is dropped because nothing ever ran it.
func siteActionsFromLegacy(sc *NightSiteControl) *NightSiteActions {
	a := &NightSiteActions{}
	if sc.RequestThermalProfile != "" {
		a.PrepareSite = []string{sc.RequestThermalProfile}
	}
	if off := sc.PresentationPowerOff; off != nil {
		var steps []ShutdownStep
		for _, p := range off.Prerequisites {
			switch p.Kind {
			case NightPrerequisiteKindDelay:
				steps = append(steps, ShutdownStep{DelayMs: p.DelayMs})
			case NightPrerequisiteKindEvidence:
				steps = append(steps, ShutdownStep{Action: p.Action, RequireConfirmation: true})
			default:
				steps = append(steps, ShutdownStep{Action: p.Action, RequireConfirmation: p.RequireConfirmation})
			}
		}
		steps = append(steps, ShutdownStep{Action: off.Action, PowerDomain: off.PowerDomain, DomainProvenance: off.DomainProvenance})
		a.PowerDown = &NightSitePowerDown{AdditionalSequences: []ShutdownSequence{{ID: "power-off", Label: "Power off", Steps: steps}}}
	}
	if len(a.PrepareSite) == 0 && a.PowerDown == nil {
		return nil
	}
	return a
}

// UnmarshalJSON reads a stored revision, translating a legacy
// siteControl block into siteActions so a night saved before ADR-065
// still loads.
func (p *NightSessionPayload) UnmarshalJSON(b []byte) error {
	type plain NightSessionPayload
	var aux struct {
		plain
		SiteControl *NightSiteControl `json:"siteControl,omitempty"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*p = NightSessionPayload(aux.plain)
	if aux.SiteControl != nil && p.SiteActions == nil {
		p.SiteActions = siteActionsFromLegacy(aux.SiteControl)
	}
	return nil
}
