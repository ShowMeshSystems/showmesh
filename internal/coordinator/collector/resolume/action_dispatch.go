package resolume

import (
	"context"
	"fmt"
	"time"
)

// This file holds the seven actions' own resolve/baseline/dispatch/confirm
// logic — action.go's Dispatch routes to exactly one of the methods below
// per call. Every method follows the identical shape, in the identical
// order, per TRACK-D-D3-SPEC.md:
//
//  1. The composition-identity gate (§3.6) — [identityGateRefusal], off
//     [Collector.LastSurveySnapshot] alone, zero HTTP requests.
//  2. Resolve the target id against the stored composition
//     ([CompositionStore]) — an id this composition does not contain is
//     refused, zero HTTP requests for that check itself.
//  3. Only for launchClip: the deck refusal (§3.4) — [deckRefusal], again
//     off the cached snapshot alone, zero HTTP requests.
//  4. The pre-dispatch baseline read (§4.2) — ALWAYS at least one by-id GET.
//     A read failure here refuses the command outright: "the action does
//     not silently proceed to a confirmation that cannot mean anything."
//  5. If the baseline ALREADY satisfies the action's own confirming
//     predicate (§3.5, §4.2), the write is still dispatched (harmless — the
//     operator's own click), but the outcome is [ActionUnconfirmable]
//     immediately, with no confirmation poll: no signal here can prove this
//     dispatch caused anything, so nothing is spent trying.
//  6. The write request itself. A definite negative response (non-2xx, or a
//     transport failure) becomes [ActionFailed] straight away — dispatch
//     never silently "succeeds" on an error.
//  7. Otherwise, [ActionDispatcher.pollUntilConfirmedOrDeadline] against a
//     DERIVED deadline (§3.3): a fixed budget for launchClip/launchColumn/
//     selectDeck/setLayerBypass/setLayerMaster, and the affected layer(s)'
//     own transition.duration + margin for clearLayer/blackout.
//
// Every confirmation check function passed to step 7 re-reads Resolume
// through the SAME by-id [Client] methods D-2 uses (readClip/readLayer/
// readColumn/readDeck below), checks [evidenceIsPostDispatch] before
// trusting anything it read, and states a non-empty reason on every branch,
// confirmed included — mirroring fppcommand_evidence.go's own "state the
// confirming evidence even on success" rule (Step 8 review Finding 6).

// --- Timestamped by-id reads -----------------------------------------------
//
// Every confirmation read in this package goes through one of these four
// functions, never a bespoke second implementation — TRACK-D-D3-SPEC.md
// §4.4's own "one resolver, one authority" rule, restated for D-3: the
// timestamp returned alongside each value is [ActionDispatcher.now] read
// immediately after the underlying [Client] call returns successfully, so
// it can only ever be LATER than the true moment Resolume answered — never
// earlier, which is the safe direction for a fence that must never credit
// a read with having happened before it did.

func (d *ActionDispatcher) readClip(ctx context.Context, id ObjectID) (Clip, time.Time, error) {
	clip, err := d.collector.client.Clip(ctx, id)
	if err != nil {
		return Clip{}, time.Time{}, err
	}
	return clip, d.now(), nil
}

func (d *ActionDispatcher) readLayer(ctx context.Context, id ObjectID) (Layer, time.Time, error) {
	layer, err := d.collector.client.Layer(ctx, id)
	if err != nil {
		return Layer{}, time.Time{}, err
	}
	return layer, d.now(), nil
}

func (d *ActionDispatcher) readColumn(ctx context.Context, id ObjectID) (Column, time.Time, error) {
	column, err := d.collector.client.Column(ctx, id)
	if err != nil {
		return Column{}, time.Time{}, err
	}
	return column, d.now(), nil
}

func (d *ActionDispatcher) readDeck(ctx context.Context, id ObjectID) (Deck, time.Time, error) {
	deck, err := d.collector.client.Deck(ctx, id)
	if err != nil {
		return Deck{}, time.Time{}, err
	}
	return deck, d.now(), nil
}

// --- Confirming-predicate primitives ---------------------------------------
//
// Each of these tests exactly one leaf, per TRACK-D-D3-SPEC.md §2's own
// table, and every one refuses to treat an unreadable leaf as satisfying
// (or refuting) anything — an absent or unresolved Presence returns false
// from every "is satisfied" predicate below, never true, matching this
// package's own "absent evidence is stated, never treated as a value"
// discipline.

// clipIsConnected implements §2's clip half of launchClip's predicate:
// connected in {Connected, Connected & previewing} — never reduced to a
// bool comparison against a single string, per composition.go's own
// [ParamState] doc comment.
func clipIsConnected(c Clip) bool {
	v, ok := c.Connected.String()
	if !ok {
		return false
	}
	switch v {
	case "Connected", "Connected & previewing":
		return true
	default:
		return false
	}
}

func clipConnectedValue(c Clip) string {
	if v, ok := c.Connected.String(); ok {
		return v
	}
	return "unknown"
}

// layerActiveClipIs implements launchClip's layer half: the owning layer's
// active_clip.id == id.
func layerActiveClipIs(l Layer, id ObjectID) bool {
	return l.ActiveClip.Presence == PresencePresent && l.ActiveClip.Clip != nil && l.ActiveClip.Clip.ID == id
}

// layerActiveClipAbsent implements clearLayer's and blackout's own
// predicate: the layer's active_clip reported ABSENT. Presence ==
// PresenceNull specifically — capture §4.4's own measured shape for
// "nothing is playing on this layer" (an explicit JSON null, matching
// [activeClipNoneValue]'s own reasoning in collector.go), never
// PresenceAbsent, which means the key was missing from the response
// entirely and is "we don't know," not "we know it's empty." Confirming a
// clear on an unreadable field would be the identical mistake toInt64's own
// doc comment (fppcommand_evidence.go) warns against for a different type.
func layerActiveClipAbsent(l Layer) bool {
	return l.ActiveClip.Presence == PresenceNull
}

// columnIsConnected implements launchColumn's predicate: connected ==
// Connected — a column's own three-state value (Empty|Disconnected|
// Connected), distinct from a clip's five-state one; see [Column]'s own doc
// comment.
func columnIsConnected(c Column) bool {
	v, ok := c.Connected.String()
	return ok && v == "Connected"
}

func columnConnectedValue(c Column) string {
	if v, ok := c.Connected.String(); ok {
		return v
	}
	return "unknown"
}

// deckIsSelected implements selectDeck's predicate: selected == true.
func deckIsSelected(dk Deck) bool {
	v, ok := dk.Selected.Bool()
	return ok && v
}

// floatsNearlyEqual is setLayerMaster's own comparison — see
// [layerMasterEpsilon]'s doc comment for why an exact == is not used.
func floatsNearlyEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}

// deriveClearDeadline implements §3.3's clearLayer row: layer's own
// transition.duration + [DefaultActionConfirmMargin], or
// [DefaultActionConfirmDeadlineUnknownTransition] when that leaf could not
// be read — never a silent zero. The transition.duration case is passed
// through [clampActionConfirmDeadline]: that value is live, operator-set
// state this package cannot bound in advance — see
// [MaxActionConfirmDeadline]'s own doc comment for why a clamp, not a
// larger constant, is the correct fix.
func deriveClearDeadline(l Layer) time.Duration {
	if v, ok := l.TransitionDuration().Float(); ok {
		return clampActionConfirmDeadline(time.Duration(v*float64(time.Second)) + DefaultActionConfirmMargin)
	}
	return DefaultActionConfirmDeadlineUnknownTransition
}

func firstNonNilErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// --- launchClip -------------------------------------------------------------

func (d *ActionDispatcher) dispatchLaunchClip(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionLaunchClip
	clipID := params.ClipID

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}

	var layerID ObjectID
	var layerKnown, persistent bool
	var deckID ObjectID
	if c, ok := tc.ClipByID(clipID); ok {
		deckID = c.DeckID
		if c.LayerID != nil {
			layerID, layerKnown = *c.LayerID, true
		}
	} else if pc, ok := tc.PersistentClipByID(clipID); ok {
		persistent = true
		if pc.LayerID != nil {
			layerID, layerKnown = *pc.LayerID, true
		}
	} else {
		return refusedOutcome(name, fmt.Sprintf("clip id %s is not part of the uploaded composition", clipID))
	}
	if !layerKnown {
		return refusedOutcome(name, fmt.Sprintf("the uploaded composition does not identify which layer owns clip %s, so its launch cannot be confirmed", clipID))
	}

	// §3.4: the deck refusal applies to a deck clip only — a persistent
	// clip carries no deck term (ADR-032 decision 6).
	if !persistent {
		if reason, refuse := deckRefusal(tc, deckID, snap); refuse {
			return refusedOutcome(name, reason)
		}
	}

	baseClip, _, errClip := d.readClip(ctx, clipID)
	baseLayer, _, errLayer := d.readLayer(ctx, layerID)
	if errClip != nil || errLayer != nil {
		return refusedOutcome(name, fmt.Sprintf(
			"could not read a pre-dispatch baseline for this clip: %s", ClassifyError(firstNonNilErr(errClip, errLayer))))
	}
	alreadySatisfied := clipIsConnected(baseClip) && layerActiveClipIs(baseLayer, clipID)

	dispatchedAt := d.now()
	if err := d.collector.client.ConnectClip(ctx, clipID); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching the clip launch failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this clip was already playing before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, DefaultActionConfirmDeadline, func() (bool, time.Time, string) {
		clip, clipAt, err := d.readClip(ctx, clipID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the clip's confirming evidence failed: %s", ClassifyError(err))
		}
		layer, layerAt, err := d.readLayer(ctx, layerID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the layer's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(clipAt, dispatchedAt) || !evidenceIsPostDispatch(layerAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		if !clipIsConnected(clip) {
			return false, time.Time{}, fmt.Sprintf("the clip is not yet connected (state %s)", clipConnectedValue(clip))
		}
		if !layerActiveClipIs(layer, clipID) {
			return false, time.Time{}, "the clip is connected, but its owning layer does not yet report it as the active clip"
		}
		confirmedAt := clipAt
		if layerAt.After(confirmedAt) {
			confirmedAt = layerAt
		}
		return true, confirmedAt, fmt.Sprintf("the clip is connected and its owning layer reports it as the active clip (confirmed %s)", confirmedAt.Format(time.RFC3339))
	})
}

// --- clearLayer ---------------------------------------------------------

func (d *ActionDispatcher) dispatchClearLayer(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionClearLayer
	layerID := params.LayerID

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseLayer, _, err := d.readLayer(ctx, layerID)
	if err != nil {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this layer: %s", ClassifyError(err)))
	}
	alreadySatisfied := layerActiveClipAbsent(baseLayer)
	deadline := deriveClearDeadline(baseLayer)

	dispatchedAt := d.now()
	if err := d.collector.client.ClearLayer(ctx, layerID); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching the layer clear failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this layer already reported no active clip before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, deadline, func() (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(ctx, layerID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the layer's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		if !layerActiveClipAbsent(layer) {
			return false, time.Time{}, "the layer still reports an active clip (or its absence is not yet confirmed)"
		}
		return true, readAt, fmt.Sprintf("the layer reports no active clip (confirmed %s)", readAt.Format(time.RFC3339))
	})
}

// --- blackout -------------------------------------------------------------

func (d *ActionDispatcher) dispatchBlackout(ctx context.Context, _ ActionParams) ActionOutcome {
	name := ActionBlackout

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	layers := tc.Layers()
	if len(layers) == 0 {
		return refusedOutcome(name, "the uploaded composition has no tracked layers to blackout")
	}

	baselines := make([]Layer, len(layers))
	for i, l := range layers {
		layer, _, err := d.readLayer(ctx, l.ID)
		if err != nil {
			return refusedOutcome(name, fmt.Sprintf(
				"could not read a pre-dispatch baseline for every tracked layer (layer %s: %s), so blackout was not dispatched",
				l.ID, ClassifyError(err)))
		}
		baselines[i] = layer
	}

	// §3.3: blackout's own deadline is the MAX transition.duration over
	// the AFFECTED layers only — a layer already showing no active clip
	// contributes nothing to the wait, matching the capture's own
	// reasoning ("a blackout across 18 layers is bounded by the slowest of
	// them" — the slowest of the ones actually fading out).
	var maxTransition time.Duration
	haveTransition := false
	affectedCount := 0
	for _, layer := range baselines {
		if layerActiveClipAbsent(layer) {
			continue
		}
		affectedCount++
		if v, ok := layer.TransitionDuration().Float(); ok {
			d := time.Duration(v * float64(time.Second))
			if !haveTransition || d > maxTransition {
				maxTransition, haveTransition = d, true
			}
		}
	}
	deadline := DefaultActionConfirmDeadlineUnknownTransition
	if haveTransition {
		// clampActionConfirmDeadline: see deriveClearDeadline's identical
		// call and [MaxActionConfirmDeadline]'s own doc comment — the same
		// live, operator-set transition.duration input, here maximized
		// across every affected layer instead of read from one.
		deadline = clampActionConfirmDeadline(maxTransition + DefaultActionConfirmMargin)
	}
	alreadySatisfied := affectedCount == 0

	dispatchedAt := d.now()
	if err := d.collector.client.DisconnectAll(ctx); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching blackout failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"every tracked layer already reported no active clip before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, deadline, func() (bool, time.Time, string) {
		var latest time.Time
		for _, l := range layers {
			layer, readAt, err := d.readLayer(ctx, l.ID)
			if err != nil {
				return false, time.Time{}, fmt.Sprintf("reading layer %s's confirming evidence failed: %s", l.ID, ClassifyError(err))
			}
			if !evidenceIsPostDispatch(readAt, dispatchedAt) {
				return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
			}
			if !layerActiveClipAbsent(layer) {
				return false, time.Time{}, fmt.Sprintf("layer %s still reports an active clip (or its absence is not yet confirmed)", l.ID)
			}
			if readAt.After(latest) {
				latest = readAt
			}
		}
		return true, latest, fmt.Sprintf("every tracked layer reports no active clip (confirmed %s)", latest.Format(time.RFC3339))
	})
}

// --- launchColumn -----------------------------------------------------------

func (d *ActionDispatcher) dispatchLaunchColumn(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionLaunchColumn
	columnID := params.ColumnID

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.ColumnByID(columnID); !ok {
		return refusedOutcome(name, fmt.Sprintf("column id %s is not part of the uploaded composition", columnID))
	}

	baseColumn, _, err := d.readColumn(ctx, columnID)
	if err != nil {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this column: %s", ClassifyError(err)))
	}
	alreadySatisfied := columnIsConnected(baseColumn)

	dispatchedAt := d.now()
	if err := d.collector.client.ConnectColumn(ctx, columnID); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching the column launch failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this column was already connected before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, DefaultActionConfirmDeadline, func() (bool, time.Time, string) {
		column, readAt, err := d.readColumn(ctx, columnID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the column's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		if !columnIsConnected(column) {
			return false, time.Time{}, fmt.Sprintf("the column is not yet connected (state %s)", columnConnectedValue(column))
		}
		return true, readAt, fmt.Sprintf("the column is connected (confirmed %s)", readAt.Format(time.RFC3339))
	})
}

// --- selectDeck -------------------------------------------------------------

func (d *ActionDispatcher) dispatchSelectDeck(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionSelectDeck
	deckID := params.DeckID

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.DeckByID(deckID); !ok {
		return refusedOutcome(name, fmt.Sprintf("deck id %s is not part of the uploaded composition", deckID))
	}

	baseDeck, _, err := d.readDeck(ctx, deckID)
	if err != nil {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this deck: %s", ClassifyError(err)))
	}
	alreadySatisfied := deckIsSelected(baseDeck)

	dispatchedAt := d.now()
	if err := d.collector.client.SelectDeck(ctx, deckID); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching deck selection failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this deck was already selected before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, DefaultActionConfirmDeadline, func() (bool, time.Time, string) {
		deck, readAt, err := d.readDeck(ctx, deckID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the deck's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		if !deckIsSelected(deck) {
			return false, time.Time{}, "the deck is not yet reported as selected"
		}
		return true, readAt, fmt.Sprintf("the deck is reported as selected (confirmed %s)", readAt.Format(time.RFC3339))
	})
}

// --- setLayerBypass -----------------------------------------------------

func (d *ActionDispatcher) dispatchSetLayerBypass(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionSetLayerBypass
	layerID := params.LayerID
	want := params.Bypassed

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseLayer, _, err := d.readLayer(ctx, layerID)
	if err != nil {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this layer: %s", ClassifyError(err)))
	}
	// The live, session-scoped ParameterID this dispatch PUTs to comes
	// from THIS SAME baseline read — never persisted, never re-derived a
	// second way (this package's own doc comment on the parameter-id
	// lifecycle rule). The id lives on the envelope regardless of whether
	// its own "value" key is present, so this check is deliberately against
	// Presence/Param, not [ParamBooleanField.Bool] — but the CURRENT value
	// (alreadySatisfied, just below) MUST go through Bool(), never a bare
	// .Param.Value read: a value-less envelope is contract-legal (capture
	// §17.3) and its Go zero value is false, which is setLayerBypass's own
	// darkening-direction value — reading it unguarded here would make an
	// unreadable baseline masquerade as "already off."
	if baseLayer.Bypassed.Presence != PresencePresent || baseLayer.Bypassed.Param == nil {
		return refusedOutcome(name,
			"this layer's bypass parameter could not be read, so its current session-scoped parameter id is not known and this command cannot be dispatched")
	}
	parameterID := baseLayer.Bypassed.Param.ID
	currentBypassed, ok := baseLayer.Bypassed.Bool()
	if !ok {
		return refusedOutcome(name,
			"this layer's bypass parameter answered with no value, so its current state is not known and this command cannot be dispatched")
	}
	alreadySatisfied := currentBypassed == want

	dispatchedAt := d.now()
	if err := d.collector.client.SetParameterBool(ctx, parameterID, want); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching the bypass change failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt, fmt.Sprintf(
			"this layer's bypass already equalled the requested value (%t) before this command was dispatched, so evidence collected afterward cannot be attributed to it", want))
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, DefaultActionConfirmDeadline, func() (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(ctx, layerID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the layer's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		// [ParamBooleanField.Bool] is the whole fix here: a value-less
		// envelope (ok=false) must NEVER be read as HeldTrue/false=want and
		// reported confirmed — see this function's own top comment and
		// CLAUDE.md's defect-1 write-up ("the blackout-adjacent values").
		bypassed, ok := layer.Bypassed.Bool()
		if !ok {
			return false, time.Time{}, "the layer's bypass value could not be read"
		}
		if bypassed != want {
			return false, time.Time{}, fmt.Sprintf("the layer's bypass is %t, want %t", bypassed, want)
		}
		return true, readAt, fmt.Sprintf("the layer's bypass reached the requested value (%t, confirmed %s)", want, readAt.Format(time.RFC3339))
	})
}

// --- setLayerMaster -----------------------------------------------------

func (d *ActionDispatcher) dispatchSetLayerMaster(ctx context.Context, params ActionParams) ActionOutcome {
	name := ActionSetLayerMaster
	layerID := params.LayerID
	want := params.Master

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := identityGateRefusal(snap); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseLayer, _, err := d.readLayer(ctx, layerID)
	if err != nil {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this layer: %s", ClassifyError(err)))
	}
	// See dispatchSetLayerBypass's identical comment: the id is read off
	// the envelope directly, but the CURRENT value must go through Float(),
	// never a bare .Param.Value — a value-less envelope's Go zero value is
	// 0.0, setLayerMaster's own darkening-direction value.
	if baseLayer.Master.Presence != PresencePresent || baseLayer.Master.Param == nil {
		return refusedOutcome(name,
			"this layer's master parameter could not be read, so its current session-scoped parameter id is not known and this command cannot be dispatched")
	}
	parameterID := baseLayer.Master.Param.ID
	currentMaster, ok := baseLayer.Master.Float()
	if !ok {
		return refusedOutcome(name,
			"this layer's master parameter answered with no value, so its current state is not known and this command cannot be dispatched")
	}

	// Range validation against Arena's OWN declared bounds for this
	// specific parameter (RangeParameter.min/max are readable fields —
	// capture §17), never the [0, 1] this package's own bench capture
	// happened to observe: a different layer, or a different Arena build,
	// is not guaranteed to share that bound. Refused with a stated reason
	// rather than clamped silently — a silent clamp would let an operator
	// or macro author believe a value was set that never was. Skipped
	// (never refused for this reason alone) when the bound itself could
	// not be read: an unknown bound is not evidence the request is out of
	// range.
	if min, max, ok := baseLayer.Master.Range(); ok && (want < min || want > max) {
		return refusedOutcome(name, fmt.Sprintf(
			"the requested master value %.6f is outside this layer's declared range [%.6f, %.6f]", want, min, max))
	}

	alreadySatisfied := floatsNearlyEqual(currentMaster, want, layerMasterEpsilon)

	dispatchedAt := d.now()
	if err := d.collector.client.SetParameterRange(ctx, parameterID, want); err != nil {
		return failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching the master change failed: %s", ClassifyError(err)))
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt, fmt.Sprintf(
			"this layer's master already equalled the requested value (%.6f) before this command was dispatched, so evidence collected afterward cannot be attributed to it", want))
	}

	return d.pollUntilConfirmedOrDeadline(ctx, name, dispatchedAt, DefaultActionConfirmDeadline, func() (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(ctx, layerID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the layer's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		master, ok := layer.Master.Float()
		if !ok {
			return false, time.Time{}, "the layer's master value could not be read"
		}
		if !floatsNearlyEqual(master, want, layerMasterEpsilon) {
			return false, time.Time{}, fmt.Sprintf("the layer's master is %.6f, want %.6f", master, want)
		}
		return true, readAt, fmt.Sprintf("the layer's master reached the requested value (%.6f, confirmed %s)", want, readAt.Format(time.RFC3339))
	})
}
