package resolume

import (
	"context"
	"fmt"
	"time"
)

// The seven actions' own resolve/baseline/dispatch/confirm logic. Every
// method below runs the same steps in the same order, per TRACK-D-D3-SPEC.md:
// the composition-identity gate (§3.6), resolve the target against the stored
// composition, the deck refusal for a deck clip (§3.4), the pre-dispatch
// baseline (§4.2), the write, and — unless the baseline already satisfied the
// confirming predicate — a confirmation poll against a derived deadline
// (§3.3). Each confirmation check re-reads through the same by-id [Client]
// methods D-2 uses, applies [evidenceIsPostDispatch] before trusting anything
// it read, and states a non-empty reason on every branch, confirmed included.

// --- Timestamped by-id reads -----------------------------------------------
//
// Every confirmation read in this package goes through one of these four
// functions, never a second implementation (§4.4, "one resolver, one
// authority"). The timestamp is [ActionDispatcher.now] read immediately after
// the [Client] call returns, so it can only ever be LATER than the moment
// Resolume answered — the safe direction for a fence that must never credit a
// read with having happened before it did.

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
// Each tests exactly one leaf of §2's table. An absent or unresolved Presence
// returns false from every predicate here, never true.

// clipIsConnected implements §2's clip half of launchClip's predicate:
// connected in {Connected, Connected & previewing}, never reduced to a
// comparison against one string.
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

// layerActiveClipIs implements launchClip's layer half: active_clip.id == id.
func layerActiveClipIs(l Layer, id ObjectID) bool {
	return l.ActiveClip.Presence == PresencePresent && l.ActiveClip.Clip != nil && l.ActiveClip.Clip.ID == id
}

// layerActiveClipAbsent implements clearLayer's and blackout's predicate: the
// layer's active_clip reported ABSENT. PresenceNull specifically — capture
// §4.4's measured shape for "nothing is playing on this layer" is an explicit
// JSON null. Never PresenceAbsent, which means the key was missing from the
// response entirely: that is "we don't know", not "we know it's empty".
func layerActiveClipAbsent(l Layer) bool {
	return l.ActiveClip.Presence == PresenceNull
}

// columnIsConnected implements launchColumn's predicate: connected ==
// Connected — a column's three-state value, distinct from a clip's five-state
// one.
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

// floatsNearlyEqual is setLayerMaster's comparison — see [layerMasterEpsilon].
func floatsNearlyEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}

// deriveClearDeadline implements §3.3's clearLayer row: the layer's own
// transition.duration plus [DefaultActionConfirmMargin], clamped, or
// [DefaultActionConfirmDeadlineUnknownTransition] when that leaf could not be
// read — never a silent zero.
func deriveClearDeadline(l Layer) time.Duration {
	if v, ok := l.TransitionDuration().Float(); ok {
		return clampActionConfirmDeadline(time.Duration(v*float64(time.Second)) + DefaultActionConfirmMargin)
	}
	return DefaultActionConfirmDeadlineUnknownTransition
}

// --- launchClip -------------------------------------------------------------

func (d *ActionDispatcher) dispatchLaunchClip(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionLaunchClip
	clipID := params.ClipID

	snap := d.collector.LastSurveySnapshot()
	if reason, refuse := d.identityGate(name, snap); refuse {
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

	baseline := w.beginBaseline(ctx)
	defer baseline.close()

	// §3.4's deck term, from one by-id read of the clip's own deck at decision
	// time. A persistent clip carries no deck term (ADR-032 decision 6).
	if !persistent {
		var deck Deck
		var deckAt time.Time
		if !baseline.read(func(bctx context.Context) (err error) { deck, deckAt, err = d.readDeck(bctx, deckID); return }) {
			return refusedOutcome(name, fmt.Sprintf("could not read whether this clip's deck is selected: %s", baseline.failure()))
		}
		if reason, refuse := deckSelectionRefusal(tc, deckID, deck, deckAt, snap); refuse {
			return refusedOutcome(name, reason)
		}
	}

	var baseClip Clip
	var baseLayer Layer
	baseline.read(func(bctx context.Context) (err error) { baseClip, _, err = d.readClip(bctx, clipID); return })
	baseline.read(func(bctx context.Context) (err error) { baseLayer, _, err = d.readLayer(bctx, layerID); return })
	if why := baseline.failure(); why != "" {
		return d.baselineFailureOutcome(ctx, w, name, why, func(c context.Context) error { return d.collector.client.ConnectClip(c, clipID) })
	}
	alreadySatisfied := clipIsConnected(baseClip) && layerActiveClipIs(baseLayer, clipID)

	dispatchedAt, bad := d.writePhase(ctx, w, name, "the clip launch", func(c context.Context) error {
		return d.collector.client.ConnectClip(c, clipID)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this clip was already playing before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, DefaultActionConfirmDeadline, func(s confirmScope) (bool, time.Time, string) {
		clip, clipAt, err := d.readClip(s.ctx, clipID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the clip's confirming evidence failed: %s", ClassifyError(err))
		}
		if s.expired() {
			return false, time.Time{}, "the confirming reads did not finish before the deadline"
		}
		layer, layerAt, err := d.readLayer(s.ctx, layerID)
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

func (d *ActionDispatcher) dispatchClearLayer(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionClearLayer
	layerID := params.LayerID

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseline := w.beginBaseline(ctx)
	var baseLayer Layer
	baseline.read(func(bctx context.Context) (err error) { baseLayer, _, err = d.readLayer(bctx, layerID); return })
	baseline.close()
	if why := baseline.failure(); why != "" {
		return d.baselineFailureOutcome(ctx, w, name, why, func(c context.Context) error { return d.collector.client.ClearLayer(c, layerID) })
	}
	alreadySatisfied := layerActiveClipAbsent(baseLayer)
	deadline := deriveClearDeadline(baseLayer)

	dispatchedAt, bad := d.writePhase(ctx, w, name, "the layer clear", func(c context.Context) error {
		return d.collector.client.ClearLayer(c, layerID)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this layer already reported no active clip before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, deadline, func(s confirmScope) (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(s.ctx, layerID)
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

func (d *ActionDispatcher) dispatchBlackout(ctx context.Context, w dispatchWindow) ActionOutcome {
	name := ActionBlackout

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
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

	baseline := w.beginBaseline(ctx)
	baselines := make([]Layer, 0, len(layers))
	for _, l := range layers {
		var layer Layer
		if !baseline.read(func(bctx context.Context) (err error) { layer, _, err = d.readLayer(bctx, l.ID); return }) {
			break
		}
		baselines = append(baselines, layer)
	}
	baseline.close()
	if why := baseline.failure(); why != "" {
		return d.baselineFailureOutcome(ctx, w, name, why, d.collector.client.DisconnectAll)
	}

	// §3.3: blackout's deadline is the MAX transition.duration over the
	// AFFECTED layers only — a layer already showing no active clip
	// contributes nothing to the wait.
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
		deadline = clampActionConfirmDeadline(maxTransition + DefaultActionConfirmMargin)
	}
	alreadySatisfied := affectedCount == 0

	dispatchedAt, bad := d.writePhase(ctx, w, name, "blackout", d.collector.client.DisconnectAll)
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"every tracked layer already reported no active clip before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, deadline, func(s confirmScope) (bool, time.Time, string) {
		var latest time.Time
		for _, l := range layers {
			if s.expired() {
				return false, time.Time{}, fmt.Sprintf("the confirming read of every tracked layer did not finish before the deadline (stopped at layer %s)", l.ID)
			}
			layer, readAt, err := d.readLayer(s.ctx, l.ID)
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

func (d *ActionDispatcher) dispatchLaunchColumn(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionLaunchColumn
	columnID := params.ColumnID

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.ColumnByID(columnID); !ok {
		return refusedOutcome(name, fmt.Sprintf("column id %s is not part of the uploaded composition", columnID))
	}

	baseline := w.beginBaseline(ctx)
	var baseColumn Column
	baseline.read(func(bctx context.Context) (err error) { baseColumn, _, err = d.readColumn(bctx, columnID); return })
	baseline.close()
	if why := baseline.failure(); why != "" {
		return d.baselineFailureOutcome(ctx, w, name, why, func(c context.Context) error { return d.collector.client.ConnectColumn(c, columnID) })
	}
	alreadySatisfied := columnIsConnected(baseColumn)

	dispatchedAt, bad := d.writePhase(ctx, w, name, "the column launch", func(c context.Context) error {
		return d.collector.client.ConnectColumn(c, columnID)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this column was already connected before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, DefaultActionConfirmDeadline, func(s confirmScope) (bool, time.Time, string) {
		column, readAt, err := d.readColumn(s.ctx, columnID)
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

func (d *ActionDispatcher) dispatchSelectDeck(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionSelectDeck
	deckID := params.DeckID

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.DeckByID(deckID); !ok {
		return refusedOutcome(name, fmt.Sprintf("deck id %s is not part of the uploaded composition", deckID))
	}

	baseline := w.beginBaseline(ctx)
	var baseDeck Deck
	baseline.read(func(bctx context.Context) (err error) { baseDeck, _, err = d.readDeck(bctx, deckID); return })
	baseline.close()
	if why := baseline.failure(); why != "" {
		return d.baselineFailureOutcome(ctx, w, name, why, func(c context.Context) error { return d.collector.client.SelectDeck(c, deckID) })
	}
	alreadySatisfied := deckIsSelected(baseDeck)

	dispatchedAt, bad := d.writePhase(ctx, w, name, "deck selection", func(c context.Context) error {
		return d.collector.client.SelectDeck(c, deckID)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt,
			"this deck was already selected before this command was dispatched, so evidence collected afterward cannot be attributed to it")
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, DefaultActionConfirmDeadline, func(s confirmScope) (bool, time.Time, string) {
		deck, readAt, err := d.readDeck(s.ctx, deckID)
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

func (d *ActionDispatcher) dispatchSetLayerBypass(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionSetLayerBypass
	layerID := params.LayerID
	want := params.Bypassed

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseline := w.beginBaseline(ctx)
	var baseLayer Layer
	baseline.read(func(bctx context.Context) (err error) { baseLayer, _, err = d.readLayer(bctx, layerID); return })
	baseline.close()
	if why := baseline.failure(); why != "" {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this layer: %s", why))
	}

	// The live, session-scoped ParameterID this dispatch PUTs to comes from
	// this same baseline read. The id lives on the envelope whether or not its
	// "value" key is present, so this check is against Presence/Param — but
	// the CURRENT value must go through Bool(), never a bare .Param.Value
	// read: a value-less envelope is contract-legal (capture §17.3) and its Go
	// zero value is false, setLayerBypass's own darkening direction, so an
	// unguarded read would make an unreadable baseline look like "already off".
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

	dispatchedAt, bad := d.writePhase(ctx, w, name, "the bypass change", func(c context.Context) error {
		return d.collector.client.SetParameterBool(c, parameterID, want)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt, fmt.Sprintf(
			"this layer's bypass already equalled the requested value (%t) before this command was dispatched, so evidence collected afterward cannot be attributed to it", want))
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, DefaultActionConfirmDeadline, func(s confirmScope) (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(s.ctx, layerID)
		if err != nil {
			return false, time.Time{}, fmt.Sprintf("reading the layer's confirming evidence failed: %s", ClassifyError(err))
		}
		if !evidenceIsPostDispatch(readAt, dispatchedAt) {
			return false, time.Time{}, "confirming evidence has not yet been re-read since dispatch"
		}
		// A value-less envelope (ok=false) must never read as false==want and
		// report confirmed — see the baseline check above.
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

func (d *ActionDispatcher) dispatchSetLayerMaster(ctx context.Context, w dispatchWindow, params ActionParams) ActionOutcome {
	name := ActionSetLayerMaster
	layerID := params.LayerID
	want := params.Master

	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		return refusedOutcome(name, reason)
	}

	tc, err := d.collector.compositionStore.Current()
	if err != nil {
		return refusedOutcome(name, "no composition has been uploaded to this coordinator yet")
	}
	if _, ok := tc.LayerByID(layerID); !ok {
		return refusedOutcome(name, fmt.Sprintf("layer id %s is not part of the uploaded composition", layerID))
	}

	baseline := w.beginBaseline(ctx)
	var baseLayer Layer
	baseline.read(func(bctx context.Context) (err error) { baseLayer, _, err = d.readLayer(bctx, layerID); return })
	baseline.close()
	if why := baseline.failure(); why != "" {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this layer: %s", why))
	}

	// See dispatchSetLayerBypass: the id is read off the envelope directly,
	// but the CURRENT value must go through Float() — a value-less envelope's
	// Go zero value is 0.0, setLayerMaster's own darkening direction.
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

	// Range-validated against Arena's own declared bounds for this specific
	// parameter, never the [0, 1] the bench capture happened to observe.
	// Refused rather than clamped silently, and skipped entirely when the
	// bound itself could not be read: an unknown declared bound is not
	// evidence a request is out of range.
	if min, max, ok := baseLayer.Master.Range(); ok && (want < min || want > max) {
		return refusedOutcome(name, fmt.Sprintf(
			"the requested master value %.6f is outside this layer's declared range [%.6f, %.6f]", want, min, max))
	}

	alreadySatisfied := floatsNearlyEqual(currentMaster, want, layerMasterEpsilon)

	dispatchedAt, bad := d.writePhase(ctx, w, name, "the master change", func(c context.Context) error {
		return d.collector.client.SetParameterRange(c, parameterID, want)
	})
	if bad != nil {
		return *bad
	}

	if alreadySatisfied {
		return unconfirmableOutcome(name, dispatchedAt, fmt.Sprintf(
			"this layer's master already equalled the requested value (%.6f) before this command was dispatched, so evidence collected afterward cannot be attributed to it", want))
	}

	return d.pollUntilConfirmedOrDeadline(ctx, w, name, dispatchedAt, DefaultActionConfirmDeadline, func(s confirmScope) (bool, time.Time, string) {
		layer, readAt, err := d.readLayer(s.ctx, layerID)
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
