package resolume

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-3a: Arena crash recovery
// (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md, TRACK-D-D3A-BUILD-CONTRACT.md).
//
// Two halves live here. The recovery RECORD (§4) is owned by [Collector]:
// a per-layer map, updated at action confirmation (never dispatch — §4
// rule 1, ADR-003) and from every survey's own layer reads (§4 rules 2-3),
// held in memory only and never persisted (§4 rule 4 — a restore is a
// claim about what was on the wall a moment ago, and a record that
// outlived the coordinator is a claim nobody can date). The RECOVERY
// CONTROLLER ([Recovery]) is the gate and the restore itself (§5-§6): it
// holds both a *Collector and a *ActionDispatcher, because restoring is an
// ordinary D-3 dispatch and inherits everything D-3 already enforces (the
// identity gate, the deck refusal, confirmation).
//
// Nothing here starts, restarts, or signals the Arena process — see
// guardnoprocesssupervision_test.go's structural guard.

// --- The recovery record (§4) -----------------------------------------

// RecoveryLayerState is the record's three-value vocabulary. unknown is
// never collapsed into dark: an absence of evidence is not evidence of
// absence (CLAUDE.md, decided four times before this seam).
type RecoveryLayerState string

const (
	RecoveryLayerClip    RecoveryLayerState = "clip"
	RecoveryLayerDark    RecoveryLayerState = "dark"
	RecoveryLayerUnknown RecoveryLayerState = "unknown"
)

// RecoverySource names which of §3's two sources established a record
// entry.
type RecoverySource string

const (
	RecoverySourceAction RecoverySource = "action"
	RecoverySourceSurvey RecoverySource = "survey"
)

// recoveryEntry is the record's own internal, unlabeled storage shape —
// [Collector.RecoveryRecord] is what turns this into an operator-facing,
// ADR-037-labeled [RecoveryLayerRecord].
type recoveryEntry struct {
	state         RecoveryLayerState
	clipID        ObjectID // meaningful only when state == RecoveryLayerClip
	establishedAt time.Time
	source        RecoverySource
	reason        string // meaningful only when state == RecoveryLayerUnknown
}

// RecoveryLayerRecord is one layer's labeled recovery record row — the
// build contract §1.3 read shape, as a Go value; the API package maps
// this onto the wire.
type RecoveryLayerRecord struct {
	LayerID            ObjectID
	Layer              string
	LayerNameGenerated bool

	State RecoveryLayerState

	// ClipID/Clip/ClipNameGenerated/Deck/DeckKnown are meaningful only
	// when State == RecoveryLayerClip.
	ClipID            ObjectID
	Clip              string
	ClipNameGenerated bool
	Deck              string
	DeckKnown         bool

	EstablishedAt time.Time
	Source        RecoverySource

	// Reason is required and non-empty whenever State == RecoveryLayerUnknown.
	Reason string
}

// applyConfirmedActionToRecoveryRecord is [ActionDispatcher.Dispatch]'s
// own hook, called once per Dispatch call, only when outcome.State ==
// ActionConfirmed (§4 rule 1 — at confirmation, never at dispatch). Per
// the build contract §2.2's table: launchClip sets its clip's own layer to
// clip; clearLayer sets its layer to dark; blackout sets EVERY layer in
// the composition to dark; launchColumn, selectDeck, setLayerBypass, and
// setLayerMaster leave every entry alone.
//
// launchColumn is the interesting no-op: a column launch changes many
// layers at once and this package does not know which, because it never
// enumerates the composition (ADR-032). Marking every layer unknown would
// throw away good evidence; marking layers dark would manufacture
// absence. Doing nothing is honest — the entries are simply older, which
// their own EstablishedAt already states.
func (c *Collector) applyConfirmedActionToRecoveryRecord(name ActionName, params ActionParams, at time.Time) {
	switch name {
	case ActionLaunchClip:
		tc, err := c.compositionStore.Current()
		if err != nil {
			// Surprising here specifically: a launchClip was just
			// confirmed against Resolume, which means SOME composition
			// was active, yet the stored composition disagrees.
			c.logger.Warn("resolume recovery: confirmed launchClip but the stored composition could not be read; recovery record not updated", "error", err)
			return
		}
		var layerID ObjectID
		var known bool
		if cl, ok := tc.ClipByID(params.ClipID); ok && cl.LayerID != nil {
			layerID, known = *cl.LayerID, true
		} else if pc, ok := tc.PersistentClipByID(params.ClipID); ok && pc.LayerID != nil {
			layerID, known = *pc.LayerID, true
		}
		if !known {
			// A clip whose LayerID could not be resolved updates nothing
			// rather than an arbitrary layer — see [TrackedClip.LayerID]'s
			// own doc comment for when this happens.
			c.logger.Warn("resolume recovery: confirmed launchClip whose clip has no resolved layer; recovery record not updated", "clipId", params.ClipID)
			return
		}
		c.recoverySetClip(layerID, params.ClipID, RecoverySourceAction, at)
	case ActionClearLayer:
		c.recoverySetDark(params.LayerID, RecoverySourceAction, at)
	case ActionBlackout:
		c.recoverySetAllDark(at)
	}
}

func (c *Collector) recoverySetClip(layerID, clipID ObjectID, source RecoverySource, at time.Time) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recoveryRecord == nil {
		c.recoveryRecord = make(map[ObjectID]recoveryEntry)
	}
	c.recoveryRecord[layerID] = recoveryEntry{state: RecoveryLayerClip, clipID: clipID, establishedAt: at, source: source}
}

func (c *Collector) recoverySetDark(layerID ObjectID, source RecoverySource, at time.Time) {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recoveryRecord == nil {
		c.recoveryRecord = make(map[ObjectID]recoveryEntry)
	}
	c.recoveryRecord[layerID] = recoveryEntry{state: RecoveryLayerDark, establishedAt: at, source: source}
}

// recoverySetAllDark implements blackout's own recovery-record effect:
// every tracked layer in the CURRENT composition becomes dark. If the
// composition is not available, nothing is recorded — a blackout that
// this package cannot even enumerate leaves the record exactly as
// untouched as any other unreadable state would.
func (c *Collector) recoverySetAllDark(at time.Time) {
	tc, err := c.compositionStore.Current()
	if err != nil {
		// Surprising here specifically: a blackout was just confirmed
		// against Resolume, so SOME composition was active.
		c.logger.Warn("resolume recovery: confirmed blackout but the stored composition could not be read; recovery record not updated", "error", err)
		return
	}
	layers := tc.Layers()
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recoveryRecord == nil {
		c.recoveryRecord = make(map[ObjectID]recoveryEntry, len(layers))
	}
	for _, l := range layers {
		c.recoveryRecord[l.ID] = recoveryEntry{state: RecoveryLayerDark, establishedAt: at, source: RecoverySourceAction}
	}
}

// recoveryUpdateFromSurvey implements §4 rules 2-3: a survey updates
// every layer it read, and only those; an unreadable layer is marked
// unknown explicitly, never simply left out of the update (the same
// "under complete=true delivery there is no such thing as leaving it
// untouched" trap the deck-mismatch observation defect was, one
// subsystem over — build contract §2.4). Called from [Collector.survey]
// with the SAME layerResults that survey's own observations are built
// from; this performs no read of its own (criterion 8).
func (c *Collector) recoveryUpdateFromSurvey(layerResults map[ObjectID]layerReadResult, at time.Time) {
	if len(layerResults) == 0 {
		return
	}
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if c.recoveryRecord == nil {
		c.recoveryRecord = make(map[ObjectID]recoveryEntry, len(layerResults))
	}
	for layerID, r := range layerResults {
		if r.err != nil {
			c.recoveryRecord[layerID] = recoveryEntry{
				state: RecoveryLayerUnknown, source: RecoverySourceSurvey, establishedAt: at,
				reason: "the most recent survey could not read this layer: " + ClassifyError(r.err),
			}
			continue
		}
		switch r.layer.ActiveClip.Presence {
		case PresencePresent:
			if r.layer.ActiveClip.Clip == nil {
				// Defensive only — see layerActiveClipIs's identical nil
				// check; not observed in the capture.
				c.recoveryRecord[layerID] = recoveryEntry{
					state: RecoveryLayerUnknown, source: RecoverySourceSurvey, establishedAt: at,
					reason: "the most recent survey reported an active clip with no id for this layer",
				}
				continue
			}
			clipID := r.layer.ActiveClip.Clip.ID
			if existing, known := c.recoveryRecord[layerID]; known && recoverySurveyAgreesWithAction(existing, RecoveryLayerClip, clipID) {
				continue
			}
			c.recoveryRecord[layerID] = recoveryEntry{
				state: RecoveryLayerClip, clipID: clipID, source: RecoverySourceSurvey, establishedAt: at,
			}
		case PresenceNull:
			if existing, known := c.recoveryRecord[layerID]; known && recoverySurveyAgreesWithAction(existing, RecoveryLayerDark, 0) {
				continue
			}
			c.recoveryRecord[layerID] = recoveryEntry{state: RecoveryLayerDark, source: RecoverySourceSurvey, establishedAt: at}
		default: // PresenceAbsent
			c.recoveryRecord[layerID] = recoveryEntry{
				state: RecoveryLayerUnknown, source: RecoverySourceSurvey, establishedAt: at,
				reason: "the most recent survey's active_clip field was absent for this layer",
			}
		}
	}
}

// recoverySurveyAgreesWithAction reports whether existing is an
// action-sourced entry whose own value already matches what a survey just
// read (newState, and newClipID when newState is clip) — the case where
// ShowMesh's own confirmed action put a value on this layer and Arena's
// survey confirms it is still there. Provenance records how we know: a
// survey that only confirms what an action already established must keep
// the action's own provenance, not overwrite it. A survey that DISAGREES
// still replaces the entry, becoming survey-sourced with the survey's own
// timestamp — something else changed it.
func recoverySurveyAgreesWithAction(existing recoveryEntry, newState RecoveryLayerState, newClipID ObjectID) bool {
	if existing.source != RecoverySourceAction || existing.state != newState {
		return false
	}
	if newState == RecoveryLayerClip {
		return existing.clipID == newClipID
	}
	return true
}

// RecoveryRecord returns one labeled entry per layer in the CURRENT
// composition (build contract §1.3). A layer with no established entry at
// all reports unknown, "never observed" (criterion 5). A source=survey
// entry is ALWAYS reported as unknown regardless of its stored state
// (criterion 14) — see [recoverySurveyAgreesWithAction] for the one case
// that keeps an entry action-sourced instead.
func (c *Collector) RecoveryRecord() []RecoveryLayerRecord {
	c.recoveryMu.Lock()
	entries := make(map[ObjectID]recoveryEntry, len(c.recoveryRecord))
	for k, v := range c.recoveryRecord {
		entries[k] = v
	}
	c.recoveryMu.Unlock()

	tc, err := c.compositionStore.Current()
	if err != nil {
		// No composition has ever been uploaded — an ordinary, expected
		// state, not logged. Stated as one row rather than a bare empty
		// list: an empty list otherwise renders identically to "a
		// composition IS uploaded and simply has no layers," and a
		// restore against either silently reports nothing_to_do.
		return []RecoveryLayerRecord{{Layer: "(no composition uploaded)", State: RecoveryLayerUnknown, Reason: err.Error()}}
	}

	order := tc.Layers()
	out := make([]RecoveryLayerRecord, 0, len(order))
	for _, l := range order {
		entry, known := entries[l.ID]
		out = append(out, buildRecoveryLayerRecord(tc, l.ID, entry, known))
	}
	return out
}

func buildRecoveryLayerRecord(tc *TrackedComposition, layerID ObjectID, entry recoveryEntry, known bool) RecoveryLayerRecord {
	rec := RecoveryLayerRecord{LayerID: layerID}
	rec.Layer, rec.LayerNameGenerated = layerLabelForID(tc, layerID)

	if !known {
		rec.State = RecoveryLayerUnknown
		rec.Reason = "no record has ever been established for this layer"
		return rec
	}

	rec.EstablishedAt = entry.establishedAt
	rec.Source = entry.source

	if entry.source == RecoverySourceSurvey {
		// No precomputed age: EstablishedAt (set above) is already on the
		// wire as an absolute timestamp (ADR-020), and a client computes
		// its own age from it. A string that changes every second here
		// would make this resource re-broadcast on every stream render
		// tick for as long as any layer carries a survey-sourced entry.
		rec.State = RecoveryLayerUnknown
		rec.Reason = "the only reading for this layer is from a survey; the timeline may have launched a clip on it since"
		return rec
	}

	switch entry.state {
	case RecoveryLayerClip:
		rec.State = RecoveryLayerClip
		rec.ClipID = entry.clipID
		rec.Clip, rec.ClipNameGenerated, rec.Deck, rec.DeckKnown = labelRecoveryClip(tc, entry.clipID)
	case RecoveryLayerDark:
		rec.State = RecoveryLayerDark
	default:
		rec.State = RecoveryLayerUnknown
		rec.Reason = entry.reason
	}
	return rec
}

// layerLabelForID and labelRecoveryClip are this file's own ADR-037 label
// lookups, by id, over the CURRENT composition — the reverse direction of
// references.go's ResolveLayer/ResolveClip (name -> id). Kept local to
// this file rather than added to references.go: they exist only for this
// seam's own operator-facing record and restore report.

func layerLabelForID(tc *TrackedComposition, layerID ObjectID) (label string, generated bool) {
	if tc != nil {
		if l, ok := tc.LayerByID(layerID); ok {
			return resolumecomp.LayerLabel(l.Index, l.Name)
		}
	}
	return fmt.Sprintf("layer id %s", layerID), false
}

func deckLabelForID(tc *TrackedComposition, deckID ObjectID) (label string, known bool) {
	if tc == nil {
		return "", false
	}
	for i, d := range tc.Decks() {
		if d.ID == deckID {
			l, _ := resolumecomp.DeckLabel(i+1, d.Name)
			return l, true
		}
	}
	return "", false
}

func labelRecoveryClip(tc *TrackedComposition, clipID ObjectID) (label string, generated bool, deck string, deckKnown bool) {
	if tc != nil {
		if c, ok := tc.ClipByID(clipID); ok {
			label, generated = resolumecomp.ClipLabel(c.LayerIndex, c.ColumnIndex, c.Name)
			deck, deckKnown = deckLabelForID(tc, c.DeckID)
			return label, generated, deck, deckKnown
		}
		for i, p := range tc.PersistentClips() {
			if p.ID == clipID {
				label, generated = resolumecomp.PersistentClipLabel(i+1, p.Name)
				return label, generated, "", false
			}
		}
	}
	return fmt.Sprintf("clip id %s (not found in the current composition)", clipID), false, "", false
}

// --- The restore report (build contract §1.4) --------------------------

// RestoreTrigger names what caused a restore to run.
type RestoreTrigger string

const (
	RestoreTriggerAutomatic RestoreTrigger = "automatic"
	RestoreTriggerManual    RestoreTrigger = "manual"
)

// RestoreResult is one layer's own outcome within a restore.
type RestoreResult string

const (
	RestoreResultRestored RestoreResult = "restored"
	RestoreResultSkipped  RestoreResult = "skipped"
	RestoreResultFailed   RestoreResult = "failed"
)

// RestoreOutcome is the whole restore's own summary outcome.
type RestoreOutcome string

const (
	RestoreOutcomeRestored    RestoreOutcome = "restored"
	RestoreOutcomePartial     RestoreOutcome = "partial"
	RestoreOutcomeNothingToDo RestoreOutcome = "nothing_to_do"
	RestoreOutcomeFailed      RestoreOutcome = "failed"
)

// RestoreLayerResult is one layer's row in a [RestoreReport].
type RestoreLayerResult struct {
	LayerID            ObjectID
	Layer              string
	LayerNameGenerated bool

	Result RestoreResult
	// Reason is always present on Skipped and Failed, always absent on
	// Restored (build contract §1.4).
	Reason string

	// ClipKnown is true when this layer had a clip-state target — Clip/
	// ClipNameGenerated are meaningful only then.
	ClipKnown         bool
	Clip              string
	ClipNameGenerated bool

	// ActionOutcome is D-3's own five-word outcome vocabulary
	// (ActionOutcomeState as a string), present only when
	// [ActionDispatcher.Dispatch] was actually called for this layer.
	ActionOutcome string
}

// RestoreReport is one restore's whole outcome (build contract §1.4).
// Principal attribution is deliberately NOT carried here: this package
// has no identity/audit dependency (see this file's own top comment on
// layering), so the caller — which does — attaches the acting principal
// when it writes this restore's audit entry and renders the wire
// response.
type RestoreReport struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Trigger    RestoreTrigger
	Outcome    RestoreOutcome
	Layers     []RestoreLayerResult

	// OmittedLayerCount is nonzero when the target composition had more
	// layers than [MaxRestoreLayers] permits in one restore: the excess
	// layers, beyond the first MaxRestoreLayers in target's own order,
	// were never attempted at all and do not appear in Layers. Stated here
	// rather than silently truncated, so a report can never be mistaken
	// for a complete accounting of every layer.
	OmittedLayerCount int
}

// MaxRestoreLayers bounds how many layers ONE restore call ever attempts.
// SHOWMESH GUESS, NOT MEASURED: chosen comfortably above the reference
// installation's own measured 18 layers (LESSONS.md) while keeping the
// worst-case HTTP write deadline this restore composes (build contract
// §1.6, internal/coordinator/api's resolumeRecoveryRestoreDeadline)
// bounded rather than scaling without limit against an unusually large
// composition. A composition with more layers than this runs the restore
// again to continue — RestoreReport.OmittedLayerCount states when that is
// needed.
const MaxRestoreLayers = 30

// --- The recovery controller: the gate (§5) and the restore (§6) -------

// RecoveryOptions configures a [Recovery]. Every field left at its zero
// value is replaced by a documented default, except AutoRestoreEnabled,
// whose nil means "always enabled" (production always supplies it; a nil
// default keeps every test that does not care about the toggle simple).
type RecoveryOptions struct {
	// Now and Sleep mirror [ActionDispatcherOptions]' identical fields: a
	// test injecting a fake Now MUST pair it with a Sleep that advances
	// the same fake clock.
	Now   func() time.Time
	Sleep func(time.Duration)

	// Settle is §5 term 2's own wait — [config.Config.ResolumeRecoverySettle],
	// default 8s, 0s permitted as a test affordance.
	Settle time.Duration

	// AutoRestoreEnabled reads the current resolume.recovery configuration
	// toggle. Consulted ONLY for [RestoreTriggerAutomatic] — a manual
	// restore always attempts, per §7.1 ("the manual path exists ... for
	// the case where the toggle is off"). A non-nil error is treated as
	// "enabled": this project degrades toward the show continuing, and a
	// toggle this package cannot even read must not silently disarm
	// recovery.
	AutoRestoreEnabled func(ctx context.Context) (bool, error)

	// OnRestoreComplete is called once, synchronously, at the end of
	// every restore (automatic or manual) — the hook the caller uses to
	// write this restore's ONE audit entry (build contract §1.5) and
	// publish the change-stream update. nil means no hook.
	OnRestoreComplete func(RestoreReport)
}

// Recovery is Track D seam D-3a's gate (§5) and restore (§6) controller.
// It holds both a *Collector (the record, the survey) and an
// *ActionDispatcher (the restore's own launchClip dispatches, which
// inherit every D-3 guard — the identity gate, the deck refusal,
// confirmation — unweakened: §6 is explicit that "the restore is an
// ordinary D-3 dispatch").
type Recovery struct {
	collector  *Collector
	dispatcher *ActionDispatcher
	now        func() time.Time
	sleep      func(time.Duration)
	settle     time.Duration

	autoRestoreEnabled func(ctx context.Context) (bool, error)
	onRestoreComplete  func(RestoreReport)

	// mu guards generation and lastReport. generation is bumped once per
	// [Recovery.HandleReachableTransition] call; a gate in flight checks
	// it at each checkpoint and abandons itself if a later call has
	// superseded it (§5: "a second return while one gate is in flight
	// supersedes it rather than starting a second").
	mu         sync.Mutex
	generation uint64
	lastReport *RestoreReport

	// crashMu guards crashTarget/haveCrashTarget — a separate lock from mu
	// since [Recovery.captureCrashTarget] is called synchronously from the
	// collector's own liveness-poll goroutine (never blocking behind
	// whatever a gate in flight is doing under mu).
	crashMu         sync.Mutex
	crashTarget     []RecoveryLayerRecord
	haveCrashTarget bool
}

// NewRecovery constructs a [Recovery] over collector and dispatcher, which
// must both be non-nil and must share the same underlying Collector
// (dispatcher's own collector field) — this is a wiring invariant this
// constructor trusts its caller to hold, not something it re-derives.
func NewRecovery(collector *Collector, dispatcher *ActionDispatcher, opts RecoveryOptions) *Recovery {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Recovery{
		collector: collector, dispatcher: dispatcher, now: now, sleep: sleep, settle: opts.Settle,
		autoRestoreEnabled: opts.AutoRestoreEnabled, onRestoreComplete: opts.OnRestoreComplete,
	}
}

// Record returns the current recovery record — [Collector.RecoveryRecord]
// passed straight through, so the read endpoint (build contract §1.3) has
// one call to make regardless of whether it is reading through a
// *Collector or a *Recovery.
func (r *Recovery) Record() []RecoveryLayerRecord { return r.collector.RecoveryRecord() }

// LastReport returns the most recent restore's own report, or nil if no
// restore (automatic or manual) has ever run on this Recovery.
func (r *Recovery) LastReport() *RestoreReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReport
}

// bumpGeneration returns a fresh generation number and installs it as
// current.
func (r *Recovery) bumpGeneration() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	return r.generation
}

// superseded reports whether myGen is no longer the current generation —
// i.e. a later [Recovery.HandleReachableTransition] call has started
// since myGen was issued.
func (r *Recovery) superseded(myGen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation != myGen
}

func (r *Recovery) setLastReport(report RestoreReport) {
	r.mu.Lock()
	r.lastReport = &report
	r.mu.Unlock()
}

// captureCrashTarget snapshots the current recovery record as §4's restore
// target — "what was on the wall a moment ago" — and is [Collector.Options.OnUnreachableTransition]'s
// own hook, called SYNCHRONOUSLY from the collector's liveness-poll
// goroutine the instant a reachable->unreachable transition is detected,
// never from a spawned goroutine: internal/coordinator/collector.Runner
// never calls Poll concurrently with itself for one collector, so a
// synchronous capture here is guaranteed to complete before any LATER poll
// call (including the eventual return's own up-transition) can begin —
// which is what removes the race [Recovery.HandleReachableTransition] used
// to run against an intervening survey. Nothing can corrupt the record
// between a crash and the return that
// follows it (no confirmed action can dispatch, and no survey runs, while
// Resolume is unreachable), so this snapshot is exactly the record's state
// as of the crash for as long as it takes Resolume to come back.
func (r *Recovery) captureCrashTarget(at time.Time) {
	target := r.collector.RecoveryRecord()
	r.crashMu.Lock()
	r.crashTarget = target
	r.haveCrashTarget = true
	r.crashMu.Unlock()
}

// takeCrashTarget returns and clears the most recently captured crash
// target. ok is false when this Recovery has never observed a crash (a
// coordinator started while Resolume was already reachable — see
// [Collector.noteLivenessAndCheckTransition]'s own gateTransition/
// crashTransition split — or a caller drives this gate directly without
// ever routing a real transition through the collector, as several of this
// package's own tests do); [Recovery.HandleReachableTransition] degrades
// that to an empty target rather than falling back to a live re-read, which
// would reopen the exact race this method exists to close.
func (r *Recovery) takeCrashTarget() ([]RecoveryLayerRecord, bool) {
	r.crashMu.Lock()
	defer r.crashMu.Unlock()
	target, ok := r.crashTarget, r.haveCrashTarget
	r.crashTarget, r.haveCrashTarget = nil, false
	return target, ok
}

// waitStep bounds [Recovery.wait]'s own polling granularity: a long settle
// delay is slept in steps this small, each followed by a ctx/stop check,
// rather than one opaque call to r.sleep in a spawned goroutine that
// cannot be interrupted. Chosen small enough that neither of this method's
// two callers-in-spirit — a cancellation during shutdown, or a later
// HandleReachableTransition superseding this one — waits longer than this
// to be noticed.
const waitStep = 50 * time.Millisecond

// wait blocks for d (via r.sleep, in [waitStep] increments, so a test's
// fake advances deterministically) or until ctx is done or stop reports
// true, whichever comes first. Reports false when ctx or stop won. No
// goroutine is spawned: a spawned sleeper cannot be interrupted, so
// sleeping this in steps and re-checking ctx/stop between them bounds both
// a cancellation and a supersession to noticing within one step, rather
// than waiting out the whole remaining settle window. stop may be nil (no
// supersession to watch).
func (r *Recovery) wait(ctx context.Context, d time.Duration, stop func() bool) bool {
	for d > 0 {
		if ctx.Err() != nil || (stop != nil && stop()) {
			return false
		}
		step := d
		if step > waitStep {
			step = waitStep
		}
		r.sleep(step)
		d -= step
	}
	if stop != nil && stop() {
		return false
	}
	return ctx.Err() == nil
}

// HandleReachableTransition is the crash-recovery gate (§5), intended to
// be called from a fresh goroutine every time
// [Options.OnReachableTransition] fires (resolumewiring.go's own
// responsibility — this method does not spawn its own goroutine, so it
// itself blocks for the whole gate, including the settle wait). ctx must
// be cancellable by the coordinator's own shutdown context; a cancellation
// during the wait or the survey abandons the gate cleanly, issuing no
// write and producing no report.
//
// The four-term gate, none skippable: (1) this method is only ever called
// on a REST transition to reachable — that is the caller's own job,
// [Collector.Options.OnReachableTransition]. (2) the settle wait, below.
// (3) an explicit survey, via [Collector.SurveyNow], bypassing the
// transition-survey throttle. (4) identity confirmed from THAT survey —
// never a cached reading — checked against returnedAt so a survey that
// somehow predates the return can never open the gate (criterion 3).
func (r *Recovery) HandleReachableTransition(ctx context.Context, returnedAt time.Time) {
	myGen := r.bumpGeneration()

	// The restore target is [Recovery.captureCrashTarget]'s own snapshot,
	// taken at the CRASH rather than read live here at the return — see
	// that method's own doc comment. ok is false only when this Recovery
	// never observed a crash before this return; target then defaults to
	// empty, which [Recovery.restore] reports as RestoreOutcomeNothingToDo,
	// never a live re-read that could race the confirming survey below.
	target, _ := r.takeCrashTarget()

	stop := func() bool { return r.superseded(myGen) }
	if !r.wait(ctx, r.settle, stop) {
		return
	}

	snap := r.collector.SurveyNow(ctx, true)
	if r.superseded(myGen) {
		return
	}

	startedAt := r.now()
	if !snap.SurveyAt.After(returnedAt) {
		// Unreachable in practice — SurveyNow's own surveyedAt is read
		// after the settle wait, which is after returnedAt — kept as an
		// explicit, asserted gate term rather than an implicit ordering
		// (criterion 3: a test must fail if this gate drops to
		// reachability or a name comparison, and an implicit assumption
		// is not a gate a test can meaningfully break).
		r.finish(RestoreReport{
			StartedAt: startedAt, FinishedAt: r.now(), Trigger: RestoreTriggerAutomatic, Outcome: RestoreOutcomeNothingToDo,
			Layers: gateRefusalLayers(target, "the confirming survey predates the observed return; refusing to restore into a possibly-wrong composition"),
		})
		return
	}
	if snap.Identity != IdentityTrue {
		r.finish(RestoreReport{
			StartedAt: startedAt, FinishedAt: r.now(), Trigger: RestoreTriggerAutomatic, Outcome: RestoreOutcomeNothingToDo,
			Layers: gateRefusalLayers(target, describeIdentityGateRefusal(snap)),
		})
		return
	}

	report := r.restore(ctx, target, RestoreTriggerAutomatic, startedAt, stop)
	r.finish(report)
}

func (r *Recovery) finish(report RestoreReport) {
	r.setLastReport(report)
	if r.onRestoreComplete != nil {
		r.onRestoreComplete(report)
	}
}

// describeIdentityGateRefusal names WHICH of IdentityFalse/IdentityUnknown/
// IdentityDeckMismatch stopped the gate, and why (build contract §2.4:
// "the wrong composition is loaded" and "we could not tell" are different
// facts).
func describeIdentityGateRefusal(snap SurveySnapshot) string {
	switch snap.Identity {
	case IdentityFalse:
		return "the confirming survey found the wrong composition loaded; not restoring"
	case IdentityDeckMismatch:
		return "the confirming survey found the selected deck changed mid-check; composition identity could not be confirmed"
	default: // IdentityUnknown
		return "the confirming survey could not confirm composition identity"
	}
}

// gateRefusalLayers builds a report's per-layer rows for a restore that
// never got past the gate: every layer with a usable target entry is
// reported skipped with the gate's own reason; every other layer's
// existing state is preserved (unknown stays unknown with its own
// reason).
func gateRefusalLayers(target []RecoveryLayerRecord, gateReason string) []RestoreLayerResult {
	out := make([]RestoreLayerResult, 0, len(target))
	for _, t := range target {
		row := RestoreLayerResult{LayerID: t.LayerID, Layer: t.Layer, LayerNameGenerated: t.LayerNameGenerated, Result: RestoreResultSkipped}
		switch t.State {
		case RecoveryLayerClip:
			row.ClipKnown, row.Clip, row.ClipNameGenerated = true, t.Clip, t.ClipNameGenerated
			row.Reason = gateReason
		case RecoveryLayerUnknown:
			row.Reason = t.Reason
		default:
			row.Reason = "this layer was recorded dark; nothing to restore"
		}
		out = append(out, row)
	}
	return out
}

// RunManualRestore is the manual path (§7.1's showmeshctl command): the
// same restore as the automatic gate, without the crash-return gate's own
// settle wait or freshness check — an operator invoking this on demand is
// not resolving a specific crash-return race, and every clip-target layer
// still gets a fresh per-layer live read before being restored (see
// [Recovery.restoreLayer]), so a layer someone is already fixing by hand
// is still left alone. Always attempts, regardless of the auto-restore
// toggle (§7.1: "the manual path exists either way ... for the case where
// the toggle is off").
func (r *Recovery) RunManualRestore(ctx context.Context) RestoreReport {
	startedAt := r.now()
	target := r.collector.RecoveryRecord()
	report := r.restore(ctx, target, RestoreTriggerManual, startedAt, nil)
	r.finish(report)
	return report
}

// supersededLayerResult builds one layer's row for a restore abandoned
// mid-flight: every remaining layer once [Recovery.restore]'s own
// superseded check trips is reported skipped rather than silently dropped
// from the report, preserving its clip reference when the target had one.
func supersededLayerResult(t RecoveryLayerRecord) RestoreLayerResult {
	row := RestoreLayerResult{LayerID: t.LayerID, Layer: t.Layer, LayerNameGenerated: t.LayerNameGenerated, Result: RestoreResultSkipped,
		Reason: "a later crash-return superseded this restore before this layer was reached"}
	if t.State == RecoveryLayerClip {
		row.ClipKnown, row.Clip, row.ClipNameGenerated = true, t.Clip, t.ClipNameGenerated
	}
	return row
}

// restore implements §6: per layer, in target's own order, decide restore/
// skip/fail and issue at most one D-3 launchClip dispatch per eligible
// layer. superseded is checked BETWEEN layers, never only at the gate's own
// checkpoints before this call — a restore can run for minutes (the
// measured crash interval is 36s), so without a per-layer check a second
// gate could start a second restore while this one is still mid-flight,
// racing two launchClip dispatches on one layer. nil means never
// superseded — [Recovery.RunManualRestore]'s own path, which holds no
// generation to check.
func (r *Recovery) restore(ctx context.Context, target []RecoveryLayerRecord, trigger RestoreTrigger, startedAt time.Time, superseded func() bool) RestoreReport {
	// [MaxRestoreLayers]'s own doc comment: a composition with more layers
	// than this restores its first MaxRestoreLayers only, stating the
	// excess rather than silently attempting all of them — the caller's
	// own HTTP write deadline is sized from this same bound and cannot
	// honour more.
	var omitted int
	if len(target) > MaxRestoreLayers {
		omitted = len(target) - MaxRestoreLayers
		target = target[:MaxRestoreLayers]
	}

	autoEnabled := true
	if trigger == RestoreTriggerAutomatic && r.autoRestoreEnabled != nil {
		enabled, err := r.autoRestoreEnabled(ctx)
		if err == nil {
			autoEnabled = enabled
		}
		// err != nil: autoEnabled stays true — see [RecoveryOptions.AutoRestoreEnabled]'s
		// own doc comment for why an unreadable toggle must not silently
		// disarm recovery.
	}

	layers := make([]RestoreLayerResult, 0, len(target))
	var restoredCount, usableCount int
	for _, t := range target {
		if superseded != nil && superseded() {
			layers = append(layers, supersededLayerResult(t))
			continue
		}
		row := r.restoreLayer(ctx, t, trigger, autoEnabled)
		layers = append(layers, row)
		if t.State != RecoveryLayerClip {
			continue // dark/unknown targets never had a usable entry (build contract §1.4)
		}
		usableCount++
		if row.Result == RestoreResultRestored {
			restoredCount++
		}
	}

	// nothing_to_do means no layer had a usable entry at all (build
	// contract §1.4) — never a usable layer that was refused, already
	// satisfied, or unreadable: those layers had one and could not be
	// acted on, the opposite fact.
	var outcome RestoreOutcome
	switch {
	case usableCount == 0:
		outcome = RestoreOutcomeNothingToDo
	case restoredCount == usableCount:
		outcome = RestoreOutcomeRestored
	case restoredCount > 0:
		outcome = RestoreOutcomePartial
	default:
		// Every usable layer ended up neither restored nor genuinely
		// absent — refused, already satisfied, unreadable, or disabled by
		// the toggle. Zero of the layers this restore had something to do
		// for were actually restored, so this is never a silent success.
		outcome = RestoreOutcomeFailed
	}

	return RestoreReport{StartedAt: startedAt, FinishedAt: r.now(), Trigger: trigger, Outcome: outcome, Layers: layers, OmittedLayerCount: omitted}
}

// restoreLayer decides and, if eligible, dispatches one layer's own
// restore (§6's five rules).
func (r *Recovery) restoreLayer(ctx context.Context, target RecoveryLayerRecord, trigger RestoreTrigger, autoEnabled bool) RestoreLayerResult {
	row := RestoreLayerResult{LayerID: target.LayerID, Layer: target.Layer, LayerNameGenerated: target.LayerNameGenerated}

	switch target.State {
	case RecoveryLayerDark:
		row.Result = RestoreResultSkipped
		row.Reason = "this layer was recorded dark; a clearLayer to reach a state it is already in would be a write with no purpose"
		return row
	case RecoveryLayerUnknown:
		row.Result = RestoreResultSkipped
		row.Reason = target.Reason
		return row
	}

	row.ClipKnown, row.Clip, row.ClipNameGenerated = true, target.Clip, target.ClipNameGenerated

	live, _, err := r.dispatcher.readLayer(ctx, target.LayerID)
	if err != nil {
		row.Result = RestoreResultSkipped
		row.Reason = fmt.Sprintf("this layer's current state could not be confirmed before restoring: %s", ClassifyError(err))
		return row
	}
	// PresenceAbsent (the field was missing) and PresencePresent-with-a-
	// different-clip are two different facts an operator would act on
	// oppositely: "someone else launched something" versus "we could not
	// read this layer." §6 authorises no blind launch either way, so the
	// skip itself is identical — only the stated reason differs.
	switch live.ActiveClip.Presence {
	case PresencePresent:
		if layerActiveClipIs(live, target.ClipID) {
			row.Result = RestoreResultSkipped
			row.Reason = "this layer is already playing the recorded clip"
			return row
		}
		row.Result = RestoreResultSkipped
		row.Reason = "this layer is currently playing a different clip than recorded; not overwritten"
		return row
	case PresenceAbsent:
		row.Result = RestoreResultSkipped
		row.Reason = "this layer's active clip field was absent from Resolume's response, so whether it already holds the recorded clip is not known; not launched blindly"
		return row
	}
	// PresenceNull: genuinely no active clip — proceed.

	if trigger == RestoreTriggerAutomatic && !autoEnabled {
		row.Result = RestoreResultSkipped
		row.Reason = "automatic restore is currently disabled (see the resolume.recovery configuration toggle)"
		return row
	}

	outcome, err := r.dispatcher.Dispatch(ctx, ActionLaunchClip, ActionParams{ClipID: target.ClipID})
	if err != nil {
		row.Result = RestoreResultFailed
		row.Reason = fmt.Sprintf("dispatching the restore failed: %v", err)
		return row
	}
	row.ActionOutcome = string(outcome.State)
	switch outcome.State {
	case ActionConfirmed:
		row.Result = RestoreResultRestored
	case ActionRefused:
		// §6's own deck-term rule ("the restore does not select a deck")
		// is enforced by D-3's Dispatch, not by this function, so this is
		// the one §6 skip rule with no sentence of its own at this layer —
		// every refusal Dispatch can produce (deck or otherwise) is
		// prefixed here so a restore report never reads as launchClip's
		// own generic wording alone.
		row.Result = RestoreResultSkipped
		row.Reason = "this layer's own recorded clip could not be launched during the restore (the restore never selects a deck — §6): " + outcome.Reason
	default:
		row.Result = RestoreResultFailed
		row.Reason = outcome.Reason
	}
	return row
}
