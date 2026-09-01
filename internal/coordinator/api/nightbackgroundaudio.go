package api

import (
	"context"
	"errors"
	"fmt"
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
// Leaving playback for a real show always uses
// [nightBackgroundSuspendKind]: pause when resume policy is
// "resume" (the engine keeps position; a bare audio.session.resume
// continues it), stop when it is "restart" (a fresh apply starts over at
// item 0), so resume never needs a
// coordinator-tracked bookmark at all - pkgaudio.Bookmark and
// ApplyRequest.Bookmark are not wired on the coordinator-facing apply
// parser anyway (internal/agent/audiosessionops.go's own comment).
//
// Every step is one night_cue_outbox row, committed before dispatch and
// resumed exactly the way nightDispatchAndPersistCue's two crash-window
// hooks already prove for cues (nightcuerun.go). A step's identity is
// (phase, cueName) as separate DB columns, never fields packed into one
// parsed string: the phase column holds one fixed, coordinator-chosen
// constant, and a cue's own arbitrary name only ever occupies the
// cue_name column, verbatim, never parsed.
//
// One pkgaudio.Revision counter is shared across every step that
// addresses this session - its own RevisionState enforces one strictly-
// increasing space for the whole session, and
// [nightNextBackgroundAudioRevision]/[nightBackgroundAudioRevisionState]
// both read via [NightSessionStore.ListNightCueOutboxRowsForPhasePrefix].
//
// An announcement never appears here. Its duck/mix/interrupt policy is
// declared on the announcement's own playback session and enforced by
// the audio node (nightannouncement.go); this controller commits no step
// for it, because only the node can observe when an announcement ends.
//
// Known, deliberate limits (see this builder's own report): a failed
// apply or start is logged and left for an operator rather than auto-
// retried indefinitely (a session with genuinely bad configuration would
// otherwise retry forever); gapless/crossfade item transitions are
// confirmed against the output node's live capability advertisement
// ([audioNodeConfirmsTransition]) - configuring one against an
// output that has never declared the matching audio.transition.* ID
// refuses background audio outright, honestly, rather than approximating
// it as sequential; and maxGainDb is enforced only at the moment this
// controller computes and
// sends a gain - there is no wire-level standing ceiling on
// audio.session.apply or audio.gain.set today (a contract gap filed
// separately, not invented here).

// nightPhaseRestingBackground is the exact phase for every step this
// controller commits for background audio: apply, gain, start, pause,
// resume, and stop. It is read back through
// [NightSessionStore.ListNightCueOutboxRowsForPhasePrefix]
// (store/nightsession.go).
const nightPhaseRestingBackground = "restingBackground"

// nightBackgroundAudioSessionID is this session's own deterministic
// pkg/audio.SessionID: stable for the whole lifetime of the night.session
// record, never reset per cycle, so the node's own RevisionState for it
// persists exactly as long as this identity does.
func nightBackgroundAudioSessionID(rec store.NightSessionRecord) string {
	return "night-bg-" + rec.ID
}

// The step kinds this controller's own state machine recognizes. Every
// one of them changes or reflects background audio's playback state:
// making room for an announcement is declared on the announcement's own
// session and enforced by the node (nightannouncement.go), so no step
// kind here exists for it.
const (
	nightBGStepApply  = "apply"
	nightBGStepGain   = "gain"
	nightBGStepStart  = "start"
	nightBGStepPause  = "pause"
	nightBGStepResume = "resume"
	nightBGStepStop   = "stop"
)

// nightBackgroundAudioStep is one parsed night_cue_outbox row under this
// controller's background-audio phase.
type nightBackgroundAudioStep struct {
	Seq  int
	Kind string
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
// to belong to this session's background-audio phase. false means the row
// does not match any recognized shape - never expected in practice,
// answered rather than panicking on a malformed row. A row left behind by
// an older build under a phase this one no longer writes lands here too,
// and is dropped rather than mistaken for a step.
func nightParseBackgroundAudioRow(row store.NightCueOutboxRecord) (nightBackgroundAudioStep, bool) {
	if row.Phase != nightPhaseRestingBackground {
		return nightBackgroundAudioStep{}, false
	}
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
}

// nightBackgroundAudioHistoryRow pairs a parsed step with the outbox row
// it came from.
type nightBackgroundAudioHistoryRow struct {
	Step nightBackgroundAudioStep
	Row  store.NightCueOutboxRecord

	// Parsed is false for a row this build recognizes no step shape for,
	// which in practice means a row written by an older build under a
	// phase this one no longer uses. Such a row is not a step and must
	// never reach the state machine, but its ActionRevision is still a
	// revision the node has already seen, so it stays in history for
	// [nightNextBackgroundAudioRevision] and
	// [nightBackgroundAudioRevisionState] to count. Dropping it at the
	// store read instead would silently rewind this controller's counter
	// below what the node's own RevisionState already holds, and every
	// later command would be refused as stale for the rest of the night
	// with nothing to self-heal it.
	Parsed bool
}

// nightBackgroundAudioSteps is history narrowed to rows that are
// genuinely steps - what the state machine reads. Everything else in
// history exists only to keep revisions monotonic.
func nightBackgroundAudioSteps(history []nightBackgroundAudioHistoryRow) []nightBackgroundAudioHistoryRow {
	out := make([]nightBackgroundAudioHistoryRow, 0, len(history))
	for _, row := range history {
		if row.Parsed {
			out = append(out, row)
		}
	}
	return out
}

// nightBackgroundAudioHistory returns every step ever recorded for rec's
// background-audio session across every cycle, sorted stably by
// Row.CreatedAt/rowid (the store's own insertion order).
func (h *handlers) nightBackgroundAudioHistory(ctx context.Context, rec store.NightSessionRecord) ([]nightBackgroundAudioHistoryRow, error) {
	rows, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseRestingBackground)
	if err != nil {
		return nil, fmt.Errorf("api: list background-audio history: %w", err)
	}
	out := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	for _, r := range rows {
		step, ok := nightParseBackgroundAudioRow(r)
		out = append(out, nightBackgroundAudioHistoryRow{Step: step, Row: r, Parsed: ok})
	}
	return out, nil
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
// plus one. Never reset across a restart (history is read fresh from the
// store), across cycles (history spans every cycle), or across a row
// this build no longer recognizes as a step - see
// [nightBackgroundAudioHistoryRow.Parsed], which is exactly why this
// counts every row in history rather than only the steps.
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

// nightBackgroundCeilingGain converts resting.backgroundAudio.maxGainDb
// once, through the project's single decibel conversion (pkg/audio), into
// the gain to request and the ceiling to request it against. The two
// differ only at the extreme: a maxGainDb at or below the silence floor
// resolves to a gain of exactly 0, while the ceiling stays a small
// positive number because pkgaudio.Ceiling refuses zero on purpose.
func nightBackgroundCeilingGain(maxGainDb float64) (pkgaudio.Gain, pkgaudio.Ceiling) {
	return pkgaudio.GainFromDb(maxGainDb), pkgaudio.CeilingFromDb(maxGainDb)
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
// while rec is in preshow or a resting state. It never blocks: every call either
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

	confirms, _, err := audioNodeConfirmsTransition(ctx, h.deps.Nodes, now, nodeID, pkgaudio.ItemTransition(ba.ItemTransition))
	if err != nil {
		h.logWarn("night loop: background audio: failed to read output node's capability advertisement", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
		return
	}
	if err := pkgaudio.ValidateItemTransitionSupport(pkgaudio.ItemTransition(ba.ItemTransition), confirms); err != nil {
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
	steps := nightBackgroundAudioSteps(history)
	if len(steps) == 0 {
		h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, items, history)
		return
	}
	latest := steps[len(steps)-1]

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
	}
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
	case nightBGStepPause:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.pause", map[string]any{})
	case nightBGStepStop:
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
	steps := nightBackgroundAudioSteps(history)
	if len(steps) == 0 {
		return
	}
	latest := steps[len(steps)-1]
	switch latest.Step.Kind {
	case nightBGStepStop, nightBGStepPause:
		if latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed {
			return // genuinely confirmed suspended; nothing to do.
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

// nightClearBackgroundAudioAtEndSession is end-session's own synchronous
// bed cleanup, called directly from the end-session command handler
// (nightsessioncontrol.go) right after the session record durably reaches
// stopped - never left for a later tick, because nightTick's own Degraded
// guard would otherwise never run it at all: end-session is documented as
// the operator-recovery action for exactly a stuck/degraded session
// (nightEndSessionDecide's own doc comment), and it deliberately leaves
// Degraded unchanged, so a session recovered this way would sit at
// State=stopped, Degraded=true forever - a state nightTick's top-level
// guard only exempts for fading-out, never stopped.
//
// This ALWAYS clears, never pauses or stops: Manager.Clear
// (internal/agent/audio, not touched by this change) releases the node's
// own persisted session record along with its engine resources, while a
// stop or pause leaves that record in place for the agent's own
// RestoreAll to resurrect the bed at its next start. end-session promises
// no resume of this session (ADR-038), so nothing here may leave anything
// for a later agent restart to bring the bed back from.
//
// Dispatches directly via executeAudioSessionDispatch, mirroring
// nightResetAnnouncementCueSessionOnce's identical direct-dispatch shape
// (nightsessioncontrol.go) rather than the cue outbox's own retry
// machinery: end-session is a one-shot, owner-invoked action with no
// later tick that promises to retry it, unlike the ordinary per-cycle
// advance nightBackgroundAudioStop feeds. The revision floor is this
// session's own persisted audio_sessions.revision (every prior outbox
// step's dispatch already keeps that row current via
// persistAudioSessionDesiredState), so this can never be refused as
// stale because of anything this coordinator itself previously sent.
//
// WARN AND PROCEED: nothing here is a reason to fail end-session itself -
// the session record already reached stopped durably before this runs -
// so every failure only logs a warning.
//
// OUT OF SCOPE, DELIBERATELY: this clears rec's OWN bed session only, at
// its current [nightBackgroundAudioSessionID] ("night-bg-" + rec.ID). A
// session minted under the previous colon-bearing scheme ("night-bg:" +
// an older rec.ID) is a DIFFERENT id and is never reached by this path -
// this coordinator cannot even ask an operator to address one, since the
// scheme this fixes is exactly what made those ids unsafe. Any bed
// session stranded on a node before this change ships is handled
// separately, not by this function.
func (h *handlers) nightClearBackgroundAudioAtEndSession(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if rec.ID == "" {
		return
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: end-session: failed to read pinned night.session payload; background audio session was not cleared", "sessionId", rec.ID, "error", err)
		return
	}
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	nodeID := ba.OutputNodeID()
	sessionID := nightBackgroundAudioSessionID(rec)

	clearRevision := h.nightAudioSessionPersistedRevision(ctx, sessionID) + 1
	idemKey := fmt.Sprintf("night-end-session-clear:%s", sessionID)
	result, problem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.clear", NodeID: nodeID, SessionID: sessionID,
		Params: map[string]any{
			"sessionId": sessionID, "invocationId": idemKey, "revision": uint64(clearRevision),
		},
		Revision: uint64(clearRevision), IdempotencyKey: idemKey,
		IssuerID: "night-controller", IssuerName: "night controller",
	})
	if err != nil {
		h.logWarn("night loop: end-session: background audio session clear was not acknowledged", "sessionId", sessionID, "nodeId", nodeID, "error", err)
		return
	}
	if problem != nil {
		h.logWarn("night loop: end-session: background audio session clear was refused", "sessionId", sessionID, "nodeId", nodeID, "reason", problem.Detail)
		return
	}
	if nightAudioCueOutcome(result.Outcome) != nightCueOutcomeConfirmed {
		h.logWarn("night loop: end-session: background audio session clear did not confirm", "sessionId", sessionID, "nodeId", nodeID, "outcome", result.Outcome, "reason", result.Reason)
	}
}
