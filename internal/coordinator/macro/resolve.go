package macro

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file resolves and pins a show.macro and every show.action its steps
// reference, at whatever revision is current the instant a run is
// submitted (ADR-031: "a run pins the macro revision and each action
// revision at submission, so editing a macro at 16:58 cannot change what
// the 17:00 run does halfway through").
//
// It reads directly through internal/coordinator/store's generic
// (kind, id) config object/revision store and decodes payload_json with
// internal/coordinator/config's own exported ShowActionPayload/
// ShowMacroPayload types (config.DecodeShowActionPayload/
// DecodeShowMacroPayload are NOT called here: those two functions perform
// WRITE-TIME validation against live state this package does not hold —
// the configured FPP endpoint list, the declared broker set, the Step 8
// primitive registry — per STEP-9-SPEC.md section 5.6, "write-time
// validation does not close the in-flight case." A revision that reached
// config_revisions already passed that validation once and, per ADR-009,
// is immutable afterward, so this package trusts payload_json's shape and
// only decodes it — a plain json.Unmarshal into the SAME struct
// EncodeShowActionPayload/EncodeShowMacroPayload produced it with, never a
// second, hand-written decoder that could drift from that one.
//
// One thing that decode does NOT give back, and the first version of this
// file assumed it did: config.ShowActionTarget.Params does not survive the
// round trip with its types intact. The write path normalizes params to
// string, bool and int64, exactly the shape [api.FPPCommandInput.Params]
// documents needing, and marshalling that map into a revision and
// unmarshalling it back into map[string]any yields float64 for every
// number. See renormalizeFPPParams, which re-derives them through the same
// registry the write path used, and its doc comment for what that cost
// before it existed.

// resolvedAction is one show.action object, decoded at the revision this
// package is about to pin.
type resolvedAction struct {
	ObjectID string
	Revision int64
	Payload  config.ShowActionPayload
}

// resolvedMacro is one show.macro object, decoded at the revision this
// package is about to pin, together with every step's own resolved action
// (same order, same index, as Payload.Steps).
type resolvedMacro struct {
	ObjectID string
	Revision int64
	Payload  config.ShowMacroPayload
	Actions  []resolvedAction
}

// resolveMacro reads macroObjectID's current object pointer and current
// revision, decodes it, and resolves every step's own action the same way.
// Returns a wrapped [store.ErrConfigObjectNotFound] (via errors.Is) if
// macroObjectID names no show.macro object, or if any step's action
// object has since been removed (STEP-9-SPEC.md section 5.4 requires a
// macro's step.action to resolve to an EXISTING show.action object at
// write time; this package does not assume that guarantee still holds by
// the time a run is submitted, since nothing in this step's schema
// prevents a show.action object from being deleted after a macro was
// authored against it — Track E, which owns show.macro/show.action's
// write surface in full, has not specified whether a delete operation
// even exists, so this package treats "the referenced action is gone" as
// a submission-time refusal rather than an assumption).
func (e *Executor) resolveMacro(ctx context.Context, macroObjectID string) (resolvedMacro, error) {
	obj, err := e.store.GetConfigObject(ctx, config.ShowMacroConfigKind, macroObjectID)
	if err != nil {
		return resolvedMacro{}, fmt.Errorf("macro: resolve macro %q: %w", macroObjectID, err)
	}
	rev, err := e.store.GetConfigRevision(ctx, config.ShowMacroConfigKind, macroObjectID, obj.CurrentRevision)
	if err != nil {
		return resolvedMacro{}, fmt.Errorf("macro: read pinned revision %d of macro %q: %w", obj.CurrentRevision, macroObjectID, err)
	}
	var payload config.ShowMacroPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return resolvedMacro{}, fmt.Errorf("macro: decode pinned payload of macro %q at revision %d: %w", macroObjectID, obj.CurrentRevision, err)
	}
	if err := normalizeStepPolicies(payload.Steps, macroObjectID, obj.CurrentRevision); err != nil {
		return resolvedMacro{}, err
	}

	actions := make([]resolvedAction, len(payload.Steps))
	for i, st := range payload.Steps {
		ra, err := e.resolveAction(ctx, st.Action)
		if err != nil {
			return resolvedMacro{}, fmt.Errorf("macro: resolve macro %q step %q's action: %w", macroObjectID, st.ID, err)
		}
		actions[i] = ra
	}

	return resolvedMacro{ObjectID: macroObjectID, Revision: obj.CurrentRevision, Payload: payload, Actions: actions}, nil
}

// normalizeStepPolicies decides what a stored step's two policy values mean
// before anything executes against them, rather than letting Go's zero
// value decide it at the branch.
//
// [config.DecodeShowMacroPayload] resolves both axes at write time and
// [config.ShowMacroStep] tags both without omitempty, so a payload that
// went through that decoder and its encoder carries the resolved words.
// This function exists because resolveMacro reads the stored revision with
// a plain json.Unmarshal, so nothing in THIS package enforces that the
// bytes on disk ever went through that pair. If a revision is ever written
// from a raw request body instead of a normalized payload, both fields
// arrive as "". Reading section 2.2's defaults off Go's zero value would
// then be an accident that happens to be right today: "" != "continue" is
// abort, and "" != "abort" is continue, which is the documented pair by
// coincidence and not by decision. A later edit to either branch would
// silently change what an unwritten policy means.
//
// An empty value therefore becomes the documented default explicitly. A
// value that is neither empty nor a member of its enum refuses the
// submission: something wrote past the write-time validator, and coercing
// an unrecognized policy to a default would present a value this
// coordinator invented as the operator's own recorded choice.
func normalizeStepPolicies(steps []config.ShowMacroStep, macroObjectID string, revision int64) error {
	for i := range steps {
		switch steps[i].OnFailure {
		case "":
			steps[i].OnFailure = config.ShowMacroOnFailureDefault
		case config.ShowMacroOnFailureAbort, config.ShowMacroOnFailureContinue:
		default:
			return fmt.Errorf("macro: macro %q at revision %d, step %q: stored onFailure %q is not a recognized value", macroObjectID, revision, steps[i].ID, steps[i].OnFailure)
		}
		switch steps[i].OnUnconfirmed {
		case "":
			steps[i].OnUnconfirmed = config.ShowMacroOnUnconfirmedDefault
		case config.ShowMacroOnUnconfirmedAbort, config.ShowMacroOnUnconfirmedContinue:
		default:
			return fmt.Errorf("macro: macro %q at revision %d, step %q: stored onUnconfirmed %q is not a recognized value", macroObjectID, revision, steps[i].ID, steps[i].OnUnconfirmed)
		}
	}
	return nil
}

func (e *Executor) resolveAction(ctx context.Context, actionObjectID string) (resolvedAction, error) {
	obj, err := e.store.GetConfigObject(ctx, config.ShowActionConfigKind, actionObjectID)
	if err != nil {
		return resolvedAction{}, err
	}
	rev, err := e.store.GetConfigRevision(ctx, config.ShowActionConfigKind, actionObjectID, obj.CurrentRevision)
	if err != nil {
		return resolvedAction{}, fmt.Errorf("read pinned revision %d of action %q: %w", obj.CurrentRevision, actionObjectID, err)
	}
	var payload config.ShowActionPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return resolvedAction{}, fmt.Errorf("decode pinned payload of action %q at revision %d: %w", actionObjectID, obj.CurrentRevision, err)
	}
	if payload.Target.Integration == config.ShowActionIntegrationFPP {
		params, err := renormalizeFPPParams(e.prims, rev.PayloadJSON, payload.Target.Primitive)
		if err != nil {
			return resolvedAction{}, fmt.Errorf("macro: action %q at revision %d: %w", actionObjectID, obj.CurrentRevision, err)
		}
		payload.Target.Params = params
	}
	return resolvedAction{ObjectID: actionObjectID, Revision: obj.CurrentRevision, Payload: payload}, nil
}

// renormalizeFPPParams re-derives a pinned FPP action's params from the
// stored revision bytes, through the same primitive registry the config
// write path used, and returns the natively-typed map
// [api.FPPCommandInput.Params] requires.
//
// It exists because a JSON round trip is not the identity function for
// map[string]any, and the gap is silent at every layer that would normally
// catch it. The write path normalizes to string, bool and int64;
// json.Marshal writes 50; json.Unmarshal into map[string]any reads it back
// as float64(50). Every integer-valued primitive then reads its parameter
// through a params["x"].(int64) assertion whose ok is deliberately
// discarded, because at that point in the command endpoint the value
// genuinely cannot be anything else. Through this path it can.
//
// Measured on setVolume before this function existed: an action authored
// at volume 50 dispatched volume 0, wrote desired state 0, and confirmed
// against observed volume 0, so the run reported confirmed while the show
// played muted. That is Step 8's "confirmed while doing nothing" defect
// with the polarity reversed, and no assertion anywhere would have fired.
//
// Re-decoding through the registry rather than coercing float64 to int64
// here is deliberate: coercion would be a second normalization rule that
// can drift from the write path's, and this project has a standing rule
// against exactly that.
// The argument is the TARGET-level object, the one carrying a "params"
// key, not the unwrapped params map. That is what the config write path
// passes ([config.DecodeShowActionPayload] hands it targetFields) and what
// decodeFPPCommandParams reads, since distinguishing an absent "params"
// key from an explicit null is part of the contract and neither is
// expressible once the map has been unwrapped. Passing the inner map
// instead makes every primitive with a required parameter fail resolution
// with "params.playlist is required", which is how this was caught.
func renormalizeFPPParams(prims config.FPPPrimitiveRegistry, payloadJSON, primitive string) (map[string]any, error) {
	var raw struct {
		Target map[string]json.RawMessage `json:"target"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &raw); err != nil {
		return nil, fmt.Errorf("re-read stored params: %w", err)
	}
	params, err := prims.DecodeActionParams(primitive, raw.Target)
	if err != nil {
		return nil, fmt.Errorf("stored params are no longer valid for primitive %q: %w", primitive, err)
	}
	return params, nil
}

// buildStepRecords turns a resolvedMacro into the [store.MacroRunStepRecord]
// slice [store.Store.CreateMacroRun] requires, pinning SafetyClass and
// LocalFallbackClass at this exact moment (their own doc comments: "pinned
// the moment Store.CreateMacroRun runs, never re-read later"). Every step
// starts State "pending", Outcome "" (unresolved — createMacroRun's own
// validation does not require Outcome non-empty, unlike OutcomeState/
// OutcomeReason, which is why those two use
// [store.MacroRunStepOutcomeStatePending]/[store.MacroRunStepOutcomeReasonPending]
// while Outcome does not need an equivalent placeholder), and CommandID
// nil (neither dispatched yet, per [store.MacroRunStepRecord]'s own doc
// comment on that column's dual meaning for "not yet" versus "MQTT, never
// has one").
func buildStepRecords(rm resolvedMacro) []store.MacroRunStepRecord {
	steps := make([]store.MacroRunStepRecord, len(rm.Payload.Steps))
	for i, st := range rm.Payload.Steps {
		action := rm.Actions[i]
		steps[i] = store.MacroRunStepRecord{
			StepIndex:          i,
			StepID:             st.ID,
			ActionObjectID:     action.ObjectID,
			ActionRevision:     action.Revision,
			Integration:        action.Payload.Target.Integration,
			SafetyClass:        action.Payload.SafetyClass,
			LocalFallbackClass: st.LocalFallback.Class,
			State:              stepStatePending,
			Outcome:            "",
			OutcomeState:       store.MacroRunStepOutcomeStatePending,
			OutcomeReason:      store.MacroRunStepOutcomeReasonPending,
		}
	}
	return steps
}

// stepSafetyClasses reports every step's own [api.FPPCommandDecision11Class],
// in step order — ADR-024 decision 11's four-member vocabulary, which
// config.ShowSafetyClass{None,Blackout,Stop,PowerOff}'s string values
// already match exactly (both are the literal strings "none", "blackout",
// "stop", "powerOff"; see config/showaction.go's own const block), so this
// is a plain type conversion, never a second hand-maintained mapping that
// could drift from either vocabulary.
func stepSafetyClasses(steps []store.MacroRunStepRecord) []api.FPPCommandDecision11Class {
	classes := make([]api.FPPCommandDecision11Class, len(steps))
	for i, st := range steps {
		classes[i] = api.FPPCommandDecision11Class(st.SafetyClass)
	}
	return classes
}
