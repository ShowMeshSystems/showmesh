package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Track F seam F5's own resting.backgroundAudio lifecycle: unlike a cue,
// this is a CONTINUOUS session the controller starts on entering resting
// and stops or pauses on the way out, not a one-shot dispatch triggered
// by an offset.
//
// The whole configured item list is pinned on the node in ONE apply, as
// a real pkgaudio.PlaylistRef (ownerKind/ownerId/ownerRevision/items/
// repeat/resume/requestedTransition, exactly the fields
// internal/agent/audiosessionops.go's parsePlaylistRef accepts) - AUDIO-
// ENGINE section 3's own rule ("select a pinned ordered playlist and
// advance its current item exactly once") is an ENGINE capability, not
// something this controller reimplements: internal/agent/audio/
// restore.go's own natural-completion watcher calls advanceLocked
// itself once the engine reports Completed, so this controller never
// polls for item completion or issues its own per-item apply.
//
// Leaving playback (a real show, or an interrupting announcement) always
// uses [nightBackgroundSuspendKind]: pause when resume policy is
// "resume" (the engine keeps position; a bare audio.session.resume
// continues it), stop when it is "restart" (a fresh apply starts over at
// item 0). This is the ONE mechanism both the ordinary rest/show cycle
// and an interrupt-policy announcement share, so resume never needs a
// coordinator-tracked bookmark at all - pkgaudio.Bookmark and
// ApplyRequest.Bookmark are not wired on the coordinator-facing apply
// parser anyway (internal/agent/audiosessionops.go's own comment).
//
// Every step is one night_cue_outbox row, committed before dispatch and
// resumed exactly the way nightDispatchAndPersistCue's two crash-window
// hooks already prove for cues (nightcuerun.go). A step's identity is
// (phase, cueName) as separate DB columns, never fields packed into one
// parsed string - the exact defect an earlier version of this file had
// for an interrupt's own stop step, found by review: a hyphenated cue
// name broke a greedy regex meant to split it back into "which phase"
// and "which cue". Phase values below are all fixed, coordinator-chosen
// constants (never an operator string), so encoding a KNOWN-safe
// classifier (an announcement's own enterShow/enterResting/fadeOut
// phase) into the outbox phase column is safe; a cue's own arbitrary
// name only ever occupies the cue_name column, verbatim, never parsed.
//
// One pkgaudio.Revision counter is shared across every step that
// addresses this session, regardless of which of the phase spellings
// below recorded it - its own RevisionState enforces one strictly-
// increasing space for the whole session, and
// [nightNextBackgroundAudioRevision]/[nightBackgroundAudioRevisionState]
// both read via [NightSessionStore.ListNightCueOutboxRowsForPhasePrefix]
// so a duck/restore/interrupt step and an ordinary apply/gain/start/
// pause/resume/stop step never collide or race each other's revision.
//
// Known, deliberate limits (see this builder's own report): a failed
// apply or start is logged and left for an operator rather than auto-
// retried indefinitely (a session with genuinely bad configuration would
// otherwise retry forever); gapless/crossfade item transitions can never
// be confirmed because no audio.node capability signal for them exists
// yet (ValidateItemTransitionSupport's outputConfirms is always false
// here), so configuring one refuses background audio outright; and
// maxGainDb is enforced only at the moment this controller computes and
// sends a gain - there is no wire-level standing ceiling on
// audio.session.apply or audio.gain.set today (a contract gap filed
// separately, not invented here).

// nightPhaseRestingBackground is the exact phase for this controller's
// own apply/gain/start/pause/resume/stop steps. Every announcement-
// triggered step below uses a phase that STARTS WITH this string plus a
// colon, so [NightSessionStore.ListNightCueOutboxRowsForPhasePrefix]
// (store/nightsession.go) returns all of them together.
const nightPhaseRestingBackground = "restingBackground"

// The three announcement-triggered phase families, each followed by
// ":<cuePhase>" where cuePhase is one of nightPhaseEnterShow/
// EnterResting/FadeOut - always one of this package's own constants,
// never a cue's own name.
const (
	nightPhaseAnnouncementDuck           = nightPhaseRestingBackground + ":announcementDuck"
	nightPhaseAnnouncementRestorePrefix  = nightPhaseRestingBackground + ":announcementRestore"
	nightPhaseAnnouncementInterruptPause = nightPhaseRestingBackground + ":announcementInterruptPause"
	nightPhaseAnnouncementInterruptStop  = nightPhaseRestingBackground + ":announcementInterruptStop"
)

// nightAnnouncementRestorePhase builds one restore ATTEMPT's own phase:
// attempt is a plain increasing integer, never adversarial content, so
// this needs no escaping or parsing back - the caller looks up an exact
// attempt number directly rather than scanning and splitting a string.
func nightAnnouncementRestorePhase(cuePhase string, attempt int) string {
	return fmt.Sprintf("%s:%s:attempt%d", nightPhaseAnnouncementRestorePrefix, cuePhase, attempt)
}

// nightInterruptPhase builds one interrupt suspend ATTEMPT's own phase,
// mirroring nightAnnouncementRestorePhase's identical reasoning: attempt
// is a plain increasing integer this package itself mints, so the
// announcement cue's own name can be the row's cueName UNCHANGED across
// every attempt, and the gate check in nightAdvanceBackgroundAudio can
// look up that exact cue by name directly rather than trying to recover
// it from an encoded string.
func nightInterruptPhase(kind, cuePhase string, attempt int) string {
	prefix := nightPhaseAnnouncementInterruptStop
	if kind == nightBGStepInterruptPause {
		prefix = nightPhaseAnnouncementInterruptPause
	}
	return fmt.Sprintf("%s:%s:attempt%d", prefix, cuePhase, attempt)
}

// nightSplitInterruptPhase is nightInterruptPhase's inverse for one
// known prefix.
func nightSplitInterruptPhase(phase, prefix string) (cuePhase string, attempt int) {
	rest := strings.TrimPrefix(phase, prefix+":")
	idx := strings.LastIndex(rest, ":attempt")
	if idx < 0 {
		return rest, 0
	}
	cuePhase = rest[:idx]
	n, err := strconv.Atoi(rest[idx+len(":attempt"):])
	if err != nil {
		return cuePhase, 0
	}
	return cuePhase, n
}

// nightMaxAnnouncementRestoreAttempts bounds retry growth: far more than
// any real announcement should ever need, so hitting it means something
// is durably broken, worth its own loud log line rather than an
// unbounded row count.
const nightMaxAnnouncementRestoreAttempts = 50

// nightBackgroundAudioSessionID is this session's own deterministic
// pkg/audio.SessionID: stable for the whole lifetime of the night.session
// record, never reset per cycle, so the node's own RevisionState for it
// persists exactly as long as this identity does.
func nightBackgroundAudioSessionID(rec store.NightSessionRecord) string {
	return "night-bg:" + rec.ID
}

// The playback-relevant step kinds this controller's own state machine
// recognizes. "duck"/"restore" are announcement-only and never change
// playback state (they only ever fade gain), so they are deliberately
// NOT in this set - nightAdvanceBackgroundAudio's own decision only
// looks at the ones that do.
const (
	nightBGStepApply          = "apply"
	nightBGStepGain           = "gain"
	nightBGStepStart          = "start"
	nightBGStepPause          = "pause"
	nightBGStepResume         = "resume"
	nightBGStepStop           = "stop"
	nightBGStepInterruptPause = "interruptPause"
	nightBGStepInterruptStop  = "interruptStop"
	nightBGStepDuck           = "duck"
	nightBGStepRestore        = "restore"
)

// nightBackgroundAudioStep is one parsed night_cue_outbox row under a
// background-audio phase. CuePhase is set for every announcement-
// triggered kind (duck, restore, interruptPause, interruptStop) - the
// enterShow/enterResting/fadeOut phase the triggering cue belongs to,
// read directly off this row's own Phase column, never parsed out of a
// combined string.
type nightBackgroundAudioStep struct {
	Seq      int
	Kind     string
	CuePhase string
	Attempt  int
}

func nightBackgroundAudioCueNameApply(seq int) string  { return fmt.Sprintf("bg-%04d-apply", seq) }
func nightBackgroundAudioCueNameGain(seq int) string   { return fmt.Sprintf("bg-%04d-gain", seq) }
func nightBackgroundAudioCueNameStart(seq int) string  { return fmt.Sprintf("bg-%04d-start", seq) }
func nightBackgroundAudioCueNamePause(seq int) string  { return fmt.Sprintf("bg-%04d-pause", seq) }
func nightBackgroundAudioCueNameResume(seq int) string { return fmt.Sprintf("bg-%04d-resume", seq) }
func nightBackgroundAudioCueNameStop(seq int) string   { return fmt.Sprintf("bg-%04d-stop", seq) }

// nightBackgroundAudioSeqFromCueName extracts the leading "bg-%04d-"
// sequence number. The suffix after the second hyphen is always one of
// the fixed, non-adversarial words above (apply/gain/start/pause/
// resume/stop), so a plain prefix strip is unambiguous - never an
// operator-controlled value.
func nightBackgroundAudioSeqFromCueName(name string) (int, bool) {
	if !strings.HasPrefix(name, "bg-") {
		return 0, false
	}
	rest := strings.TrimPrefix(name, "bg-")
	dash := strings.IndexByte(rest, '-')
	if dash < 0 {
		return 0, false
	}
	seq, err := strconv.Atoi(rest[:dash])
	if err != nil {
		return 0, false
	}
	return seq, true
}

// nightParseBackgroundAudioRow classifies one outbox row already known
// to belong to this session's background-audio phase family (its Phase
// is nightPhaseRestingBackground or starts with it plus ":"). false
// means the row does not match any recognized shape - never expected in
// practice, answered rather than panicking on a malformed row.
func nightParseBackgroundAudioRow(row store.NightCueOutboxRecord) (nightBackgroundAudioStep, bool) {
	switch {
	case row.Phase == nightPhaseRestingBackground:
		seq, ok := nightBackgroundAudioSeqFromCueName(row.CueName)
		if !ok {
			return nightBackgroundAudioStep{}, false
		}
		switch {
		case strings.HasSuffix(row.CueName, "-apply"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepApply}, true
		case strings.HasSuffix(row.CueName, "-gain"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepGain}, true
		case strings.HasSuffix(row.CueName, "-start"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepStart}, true
		case strings.HasSuffix(row.CueName, "-pause"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepPause}, true
		case strings.HasSuffix(row.CueName, "-resume"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepResume}, true
		case strings.HasSuffix(row.CueName, "-stop"):
			return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepStop}, true
		}
		return nightBackgroundAudioStep{}, false
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementDuck+":"):
		return nightBackgroundAudioStep{Kind: nightBGStepDuck, CuePhase: strings.TrimPrefix(row.Phase, nightPhaseAnnouncementDuck+":")}, true
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementRestorePrefix+":"):
		cuePhase, _ := nightSplitAnnouncementRestorePhase(row.Phase)
		return nightBackgroundAudioStep{Kind: nightBGStepRestore, CuePhase: cuePhase}, true
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementInterruptPause+":"):
		cuePhase, attempt := nightSplitInterruptPhase(row.Phase, nightPhaseAnnouncementInterruptPause)
		return nightBackgroundAudioStep{Kind: nightBGStepInterruptPause, CuePhase: cuePhase, Attempt: attempt}, true
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementInterruptStop+":"):
		cuePhase, attempt := nightSplitInterruptPhase(row.Phase, nightPhaseAnnouncementInterruptStop)
		return nightBackgroundAudioStep{Kind: nightBGStepInterruptStop, CuePhase: cuePhase, Attempt: attempt}, true
	}
	return nightBackgroundAudioStep{}, false
}

// nightSplitAnnouncementRestorePhase splits
// "restingBackground:announcementRestore:<cuePhase>:attempt<N>" back
// into cuePhase and N. cuePhase is always one of a small fixed constant
// set (enterShow/enterResting/fadeOut) and attempt is always a plain
// integer this package itself minted, so splitting on the LAST
// ":attempt" occurrence is unambiguous regardless of either value.
func nightSplitAnnouncementRestorePhase(phase string) (cuePhase string, attempt int) {
	rest := strings.TrimPrefix(phase, nightPhaseAnnouncementRestorePrefix+":")
	idx := strings.LastIndex(rest, ":attempt")
	if idx < 0 {
		return rest, 0
	}
	cuePhase = rest[:idx]
	n, err := strconv.Atoi(rest[idx+len(":attempt"):])
	if err != nil {
		return cuePhase, 0
	}
	return cuePhase, n
}

// nightBackgroundAudioHistoryRow pairs a parsed step with the outbox row
// it came from.
type nightBackgroundAudioHistoryRow struct {
	Step nightBackgroundAudioStep
	Row  store.NightCueOutboxRecord
}

// nightBackgroundAudioHistory returns every step ever recorded for rec's
// background-audio session, across every phase spelling and every
// cycle, sorted stably by Row.CreatedAt/rowid (the store's own insertion
// order - [nightBackgroundAudioStep.Seq] is meaningful only within the
// nightPhaseRestingBackground family, not across announcement phases,
// which carry no seq at all).
func (h *handlers) nightBackgroundAudioHistory(ctx context.Context, rec store.NightSessionRecord) ([]nightBackgroundAudioHistoryRow, error) {
	rows, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseRestingBackground)
	if err != nil {
		return nil, fmt.Errorf("api: list background-audio history: %w", err)
	}
	out := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	for _, r := range rows {
		step, ok := nightParseBackgroundAudioRow(r)
		if !ok {
			continue
		}
		out = append(out, nightBackgroundAudioHistoryRow{Step: step, Row: r})
	}
	return out, nil
}

// nightBackgroundAudioPlaybackHistory is history narrowed to the steps
// that change or reflect PLAYBACK state (never duck/restore, which only
// ever fade gain around an announcement without stopping or pausing
// anything) - what nightAdvanceBackgroundAudio's own state machine reads
// to decide its next action.
func nightBackgroundAudioPlaybackHistory(history []nightBackgroundAudioHistoryRow) []nightBackgroundAudioHistoryRow {
	out := make([]nightBackgroundAudioHistoryRow, 0, len(history))
	for _, h := range history {
		switch h.Step.Kind {
		case nightBGStepDuck, nightBGStepRestore:
			continue
		}
		out = append(out, h)
	}
	return out
}

// nightBackgroundAudioRevisionState rebuilds a [pkgaudio.RevisionState]
// from history via [pkgaudio.RestoreRevisionState]: current is the
// highest revision any resolved-confirmed step used, and prior seeds one
// recorded decision per step's own idempotency key so a replayed
// invocation after a coordinator restart resolves identically to its
// first attempt rather than re-deciding from a reset current of zero.
func nightBackgroundAudioRevisionState(sessionID string, history []nightBackgroundAudioHistoryRow) *pkgaudio.RevisionState {
	prior := make(map[pkgaudio.InvocationID]pkgaudio.RevisionDecision, len(history))
	var current pkgaudio.Revision
	for _, h := range history {
		rev := pkgaudio.Revision(h.Row.ActionRevision)
		idemKey := pkgaudio.InvocationID(nightCueIdempotencyKey(h.Row.SessionID, h.Row.Cycle, h.Row.Phase, h.Row.CueName))
		accepted := h.Row.State == nightCueStateResolved && h.Row.Outcome == nightCueOutcomeConfirmed
		decision := pkgaudio.RevisionDecision{Requested: rev, Accepted: accepted, Revision: rev}
		if !accepted {
			decision.Result = &pkgaudio.OutcomeResult{Outcome: pkgaudio.OutcomeRefused, Reason: "not confirmed"}
			decision.Revision = current
		} else if rev > current {
			current = rev
		}
		prior[idemKey] = decision
	}
	return pkgaudio.RestoreRevisionState(pkgaudio.SessionID(sessionID), current, prior)
}

// nightNextBackgroundAudioRevision is the next revision to mint for a
// new step against this session: history's own highest ActionRevision,
// plus one, across EVERY phase spelling this session's steps use - never
// reset across a restart (history is read fresh from the store) or
// across cycles (history spans every cycle) or across a phase boundary
// (duck/restore/interrupt steps share this exact counter with apply/
// gain/start/pause/resume/stop).
func nightNextBackgroundAudioRevision(history []nightBackgroundAudioHistoryRow) int64 {
	var max int64
	for _, h := range history {
		if h.Row.ActionRevision > max {
			max = h.Row.ActionRevision
		}
	}
	return max + 1
}

func nightBackgroundAudioIssuer(rec store.NightSessionRecord) FPPCommandIssuer {
	return nightControllerIssuer(rec)
}

// nightRunAudioCommand commits (or resumes) one durable background-audio
// step and dispatches it, reusing nightDispatchAndPersistCue unchanged
// (nightcuerun.go) - the SAME commit-then-dispatch discipline and crash-
// window hooks the cue outbox already proves.
func (h *handlers) nightRunAudioCommand(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string, target config.ShowActionTarget, revision int64, history []nightBackgroundAudioHistoryRow) (store.NightCueOutboxRecord, error) {
	issuer := nightBackgroundAudioIssuer(rec)
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	switch {
	case err == nil:
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return row, nil
		}
		idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cueName, target, idemKey, issuer, revision)
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
		// The no-rewind guarantee, restored from durable history rather
		// than an in-memory value a restart would reset to zero:
		// RestoreRevisionState rebuilds exactly the state this session
		// would be in had the coordinator never restarted, and Apply
		// refuses a revision that does not strictly advance past it.
		rs := nightBackgroundAudioRevisionState(target.AudioSessionID, history)
		decision := rs.Apply(pkgaudio.InvocationID(idemKey), pkgaudio.Revision(revision))
		if !decision.Accepted {
			reason := "revision not accepted"
			if decision.Result != nil {
				reason = decision.Result.Reason
			}
			return store.NightCueOutboxRecord{}, fmt.Errorf("api: background audio: refusing to commit %s/%s at revision %d: %s (current %d)", phase, cueName, revision, reason, decision.Revision)
		}
		if cerr := h.nightCommitCueRow(ctx, now, rec, phase, cueName, revision); cerr != nil {
			if !errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
				return store.NightCueOutboxRecord{}, cerr
			}
		}
		row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
		if rerr != nil {
			return store.NightCueOutboxRecord{}, rerr
		}
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return row, nil
		}
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cueName, target, idemKey, issuer, revision)
	default:
		return store.NightCueOutboxRecord{}, err
	}
}

// nightBuildBackgroundPlaylistItems resolves ba's configured items into
// pkg/audio.PlaylistItems against this coordinator's own asset store -
// the exact (show, sequence, target) lookup nightasset.go's
// nightResolveCurrentAsset already performs for the resting timeline,
// narrowed to MediaType "audio". A missing or wrong-typed asset fails the
// WHOLE build rather than silently dropping one item, matching AUDIO-
// ENGINE section 3's "fails visibly instead of guessing" rule for a
// missing item.
func (h *handlers) nightBuildBackgroundPlaylistItems(ctx context.Context, show string, items []config.NightSessionBackgroundAudioItem) ([]pkgaudio.PlaylistItem, error) {
	out := make([]pkgaudio.PlaylistItem, 0, len(items))
	for i, item := range items {
		rec, ok, err := nightResolveCurrentAsset(ctx, h.deps.Assets, show, item.Asset.Sequence, item.Asset.Target)
		if err != nil {
			return nil, fmt.Errorf("resolve backgroundAudio item %q: %w", item.ItemID, err)
		}
		if !ok {
			return nil, fmt.Errorf("backgroundAudio item %q: no current asset for show %q sequence %q target %q", item.ItemID, show, item.Asset.Sequence, item.Asset.Target)
		}
		if rec.MediaType != "audio" {
			return nil, fmt.Errorf("backgroundAudio item %q: pinned asset's media type is %q, not \"audio\"", item.ItemID, rec.MediaType)
		}
		out = append(out, pkgaudio.PlaylistItem{
			ItemID: item.ItemID, Index: i,
			Media: pkgaudio.MediaRef{AssetID: rec.ID, ContentHash: rec.ContentHash, SizeBytes: rec.SizeBytes, RuntimeFilename: rec.RuntimeFilename},
		})
	}
	return out, nil
}

// nightBackgroundSuspendKind is the ONE decision both an ordinary exit
// from resting and an interrupt-policy announcement share: resume policy
// "resume" pauses (the engine keeps its position; a bare
// audio.session.resume continues it exactly there), "restart" stops (a
// later re-entry applies the whole playlist fresh, at item 0).
func nightBackgroundSuspendKind(resume string) string {
	if resume == config.NightSessionBackgroundResumeResume {
		return nightBGStepPause
	}
	return nightBGStepStop
}

func nightBackgroundCeilingGain(maxGainDb float64) (pkgaudio.Gain, pkgaudio.Ceiling) {
	linear := dbToLinearGain(maxGainDb)
	return pkgaudio.Gain(linear), pkgaudio.Ceiling(linear)
}

func dbToLinearGain(db float64) float64 {
	return math.Pow(10, db/20)
}

func nightAudioTarget(nodeID, sessionID, action string, params map[string]any) config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationAudio,
		AudioNodeID: nodeID, AudioSessionID: sessionID, AudioAction: action, Params: params,
	}
}

// nightBackgroundApplyParams builds audio.session.apply's own wire
// params: a full pkgaudio.PlaylistRef pinning every configured item,
// repeat, resume, and requestedTransition on the node - the fields
// internal/agent/audiosessionops.go's parsePlaylistRef accepts, spelled
// exactly as it requires (ownerKind, ownerId, ownerRevision, items,
// repeat, resume, requestedTransition; each item itemId/index/assetId/
// contentHash/filename/sizeBytes).
func nightBackgroundApplyParams(rec store.NightSessionRecord, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem) map[string]any {
	wireItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		wireItems = append(wireItems, map[string]any{
			"itemId": item.ItemID, "index": item.Index,
			"assetId": item.Media.AssetID, "contentHash": item.Media.ContentHash,
			"filename": item.Media.RuntimeFilename, "sizeBytes": item.Media.SizeBytes,
		})
	}
	return map[string]any{
		"sourceRole": string(pkgaudio.SourceRoleBackground),
		"playlist": map[string]any{
			"ownerKind": "night.session.resting.backgroundAudio", "ownerId": rec.ConfigObjectID,
			"ownerRevision": rec.ConfigRevision, "items": wireItems,
			"repeat": ba.Repeat, "resume": ba.Resume, "requestedTransition": ba.ItemTransition,
		},
		"mixPolicy": string(pkgaudio.MixPolicyMix),
	}
}

// nightAdvanceBackgroundAudio is nightTick's own per-tick entry point
// while rec is in a resting state. It never blocks: every call either
// resumes an in-flight step or decides and commits the next one,
// returning immediately either way.
func (h *handlers) nightAdvanceBackgroundAudio(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read pinned payload", "sessionId", rec.ID, "error", err)
		return
	}
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	nodeID := ba.OutputNodeID()
	sessionID := nightBackgroundAudioSessionID(rec)

	if err := pkgaudio.ValidateItemTransitionSupport(pkgaudio.ItemTransition(ba.ItemTransition), false); err != nil {
		h.logWarn("night loop: background audio: requested item transition is not confirmed by the output; refusing to start", "sessionId", rec.ID, "itemTransition", ba.ItemTransition, "error", err)
		return
	}

	items, err := h.nightBuildBackgroundPlaylistItems(ctx, payload.Show, ba.Items)
	if err != nil {
		h.logWarn("night loop: background audio: failed to resolve playlist items", "sessionId", rec.ID, "error", err)
		return
	}
	if len(items) == 0 {
		return
	}

	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history", "sessionId", rec.ID, "error", err)
		return
	}
	pbHistory := nightBackgroundAudioPlaybackHistory(history)

	if len(pbHistory) == 0 {
		h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, items, history)
		return
	}
	latest := pbHistory[len(pbHistory)-1]

	if latest.Row.State == nightCueStatePending || latest.Row.State == nightCueStateDispatched {
		h.nightResumeBackgroundStep(ctx, now, rec, nodeID, sessionID, ba, items, latest, history)
		return
	}

	confirmed := latest.Row.Outcome == nightCueOutcomeConfirmed

	switch latest.Step.Kind {
	case nightBGStepApply:
		if !confirmed {
			h.logWarn("night loop: background audio: apply did not confirm; not auto-retrying", "sessionId", rec.ID, "outcome", latest.Row.Outcome)
			return
		}
		h.nightBackgroundAudioGain(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, history)

	case nightBGStepGain:
		if !confirmed {
			h.nightBackgroundAudioGain(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, history) // retry under a fresh revision: never wedge here.
			return
		}
		h.nightBackgroundAudioStart(ctx, now, rec, nodeID, sessionID, history)

	case nightBGStepStart:
		if !confirmed {
			h.logWarn("night loop: background audio: start did not confirm; not auto-retrying", "sessionId", rec.ID, "outcome", latest.Row.Outcome)
		}
		// Confirmed: playing. The engine owns advancement and repeat
		// from here; nothing more for this controller to do.

	case nightBGStepPause:
		if !confirmed {
			h.logWarn("night loop: background audio: a prior pause did not confirm; leaving it for an operator", "sessionId", rec.ID, "outcome", latest.Row.Outcome)
			return
		}
		h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, history)

	case nightBGStepResume:
		if !confirmed {
			h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, history) // retry under a fresh revision: never wedge here.
			return
		}
		// Confirmed: playing again.

	case nightBGStepStop:
		if !confirmed {
			h.nightBackgroundAudioStop(ctx, now, rec, nodeID, sessionID, ba.Resume, history) // retry: never leave the bed running with a stop that never landed.
			return
		}
		h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, items, history)

	case nightBGStepInterruptPause, nightBGStepInterruptStop:
		gateOK, err := h.nightAnnouncementCueResolved(ctx, rec, latest.Step.CuePhase, latest.Row.CueName)
		if err != nil {
			h.logWarn("night loop: background audio: failed to check the interrupting announcement's own row", "sessionId", rec.ID, "error", err)
			return
		}
		if !confirmed {
			// Retry the suspend itself under a fresh attempt regardless
			// of the gate: an unconfirmed pause/stop must not silently
			// leave the bed audible over the announcement.
			h.nightBackgroundAudioResuspend(ctx, now, rec, nodeID, sessionID, latest.Step.Kind, latest.Step.CuePhase, latest.Row.CueName, latest.Step.Attempt, history)
			return
		}
		if !gateOK {
			return // the interrupting announcement has not resolved yet.
		}
		if latest.Step.Kind == nightBGStepInterruptPause {
			h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, history)
		} else {
			h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, items, history)
		}
	}
}

func (h *handlers) nightAnnouncementCueResolved(ctx context.Context, rec store.NightSessionRecord, cuePhase, cueName string) (bool, error) {
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, cuePhase, cueName)
	if errors.Is(err, store.ErrNightCueOutboxNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.State == nightCueStateResolved, nil
}

func (h *handlers) nightBackgroundAudioApply(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameApply(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.apply", nightBackgroundApplyParams(rec, ba, items))
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: apply failed", "sessionId", rec.ID, "error", err)
	}
}

// nightBackgroundAudioGain sets the session's gain to
// resting.backgroundAudio.maxGainDb, converted from dB to a linear
// pkgaudio.Gain and passed through pkgaudio.ApplyCeiling against that
// SAME value as its own ceiling - this controller's only gain intent for
// background audio IS the configured ceiling, so ApplyCeiling never
// clamps in current usage, but the call is real and its CeilingResult is
// logged on failure. Sent BEFORE start (never after) so the bed is never
// audible for even one tick at the node's prior gain.
func (h *handlers) nightBackgroundAudioGain(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, maxGainDb float64, history []nightBackgroundAudioHistoryRow) {
	requested, ceiling := nightBackgroundCeilingGain(maxGainDb)
	result, err := pkgaudio.ApplyCeiling(requested, ceiling)
	if err != nil {
		h.logWarn("night loop: background audio: gain computation failed", "sessionId", rec.ID, "error", err)
		return
	}
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameGain(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.gain.set", map[string]any{"gain": float64(result.Effective)})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: gain failed", "sessionId", rec.ID, "error", err, "requested", float64(result.Requested), "effective", float64(result.Effective), "clamped", result.Clamped)
	}
}

func (h *handlers) nightBackgroundAudioStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameStart(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.start", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: start failed", "sessionId", rec.ID, "error", err)
	}
}

func (h *handlers) nightBackgroundAudioResume(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameResume(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.resume", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: resume failed", "sessionId", rec.ID, "error", err)
	}
}

// nightBackgroundAudioStop issues the ordinary (non-interrupt) suspend
// step, per resume policy.
func (h *handlers) nightBackgroundAudioStop(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID, resume string, history []nightBackgroundAudioHistoryRow) {
	kind := nightBackgroundSuspendKind(resume)
	revision := nightNextBackgroundAudioRevision(history)
	var cueName, action string
	if kind == nightBGStepPause {
		cueName, action = nightBackgroundAudioCueNamePause(int(revision)), "audio.session.pause"
	} else {
		cueName, action = nightBackgroundAudioCueNameStop(int(revision)), "audio.session.stop"
	}
	target := nightAudioTarget(nodeID, sessionID, action, map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: suspend failed", "sessionId", rec.ID, "kind", kind, "error", err)
	}
}

// nightBackgroundAudioResuspend retries an unconfirmed interrupt-driven
// pause/stop under a NEW attempt number, keeping the announcement cue's
// own name as the row's cueName (nightInterruptPhase's own reasoning),
// so the gate in nightAdvanceBackgroundAudio can still find the
// interrupting announcement's own row directly by name once this
// confirms.
func (h *handlers) nightBackgroundAudioResuspend(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID, kind, cuePhase, cueName string, priorAttempt int, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	action := "audio.session.stop"
	if kind == nightBGStepInterruptPause {
		action = "audio.session.pause"
	}
	phase := nightInterruptPhase(kind, cuePhase, priorAttempt+1)
	target := nightAudioTarget(nodeID, sessionID, action, map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: interrupt resuspend failed", "sessionId", rec.ID, "kind", kind, "error", err)
	}
}

// nightResumeBackgroundStep re-attempts an in-flight (pending or
// dispatched) step under its own already-committed identity - audio is
// retryable by identity ([nightCueRetryableByIdentity]), so this can
// never double-send.
func (h *handlers) nightResumeBackgroundStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem, latest nightBackgroundAudioHistoryRow, history []nightBackgroundAudioHistoryRow) {
	revision := latest.Row.ActionRevision
	var target config.ShowActionTarget
	switch latest.Step.Kind {
	case nightBGStepApply:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.apply", nightBackgroundApplyParams(rec, ba, items))
	case nightBGStepGain:
		gain, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
		result, err := pkgaudio.ApplyCeiling(gain, ceiling)
		if err != nil {
			return
		}
		target = nightAudioTarget(nodeID, sessionID, "audio.gain.set", map[string]any{"gain": float64(result.Effective)})
	case nightBGStepStart:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.start", map[string]any{})
	case nightBGStepResume:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.resume", map[string]any{})
	case nightBGStepPause, nightBGStepInterruptPause:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.pause", map[string]any{})
	case nightBGStepStop, nightBGStepInterruptStop:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.stop", map[string]any{})
	default:
		return
	}
	if _, err := h.nightRunAudioCommand(ctx, now, rec, latest.Row.Phase, latest.Row.CueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: resume of an in-flight step failed", "sessionId", rec.ID, "cueName", latest.Row.CueName, "error", err)
	}
}

// nightStopBackgroundAudioIfRunning is nightTick's own entry point for
// every non-resting state: suspends a background-audio session that is
// still logically playing, per resume policy, retrying until the
// outcome is genuinely confirmed rather than accepting any resolved
// state - a refused, failed, or unconfirmable stop must never read as
// "stopped" while the bed keeps playing over the show.
func (h *handlers) nightStopBackgroundAudioIfRunning(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	sessionID := nightBackgroundAudioSessionID(rec)
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history for stop", "sessionId", rec.ID, "error", err)
		return
	}
	pbHistory := nightBackgroundAudioPlaybackHistory(history)
	if len(pbHistory) == 0 {
		return
	}
	latest := pbHistory[len(pbHistory)-1]
	switch latest.Step.Kind {
	case nightBGStepStop, nightBGStepPause:
		if latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed {
			return // genuinely confirmed suspended; nothing to do.
		}
	case nightBGStepInterruptStop, nightBGStepInterruptPause:
		// An unresolved interrupt suspend is background's own to finish
		// via nightAdvanceBackgroundAudio once resting resumes; leaving
		// resting entirely while interrupted is not a state this
		// controller currently reaches (an interrupt never changes
		// rec.State), but if it ever did, falling through to an
		// ordinary suspend below is still correct and never leaves the
		// bed audible.
		if latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed {
			return
		}
	}

	if latest.Row.State == nightCueStatePending || latest.Row.State == nightCueStateDispatched {
		payload, err := h.getPinnedNightSessionPayload(ctx, rec)
		if err != nil || payload.Resting.BackgroundAudio == nil {
			return
		}
		items, err := h.nightBuildBackgroundPlaylistItems(ctx, payload.Show, payload.Resting.BackgroundAudio.Items)
		if err != nil {
			return
		}
		h.nightResumeBackgroundStep(ctx, now, rec, payload.Resting.BackgroundAudio.OutputNodeID(), sessionID, payload.Resting.BackgroundAudio, items, latest, history)
		return
	}

	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || payload.Resting.BackgroundAudio == nil {
		return
	}
	nodeID := payload.Resting.BackgroundAudio.OutputNodeID()
	h.nightBackgroundAudioStop(ctx, now, rec, nodeID, sessionID, payload.Resting.BackgroundAudio.Resume, history)
}
