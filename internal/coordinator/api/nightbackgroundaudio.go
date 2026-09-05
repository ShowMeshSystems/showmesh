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
	"github.com/showmeshsystems/showmesh/pkg/observation"
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
// otherwise retry forever); and gapless/crossfade item transitions are
// confirmed against the output node's live capability advertisement
// ([audioNodeConfirmsTransition]) - configuring one against an
// output that has never declared the matching audio.transition.* ID
// refuses background audio outright, honestly, rather than approximating
// it as sequential. maxGainDb now also travels as a standing ceiling on
// audio.session.apply itself ([nightBackgroundApplyParams]'s own ceiling
// field), so the node enforces it on every path gain takes effect, not
// only at the moments this controller happens to compute and send one.

// nightPhaseRestingBackground is the phase FAMILY prefix for every step
// this controller commits for background audio: apply, gain, start,
// pause, resume, stop, and (when resting.backgroundAudio.fadeOutMs/
// fadeInMs are configured) fadedown and fadeup. It is read back through
// [NightSessionStore.ListNightCueOutboxRowsForPhasePrefix]
// (store/nightsession.go).
//
// An individual step's own phase is this prefix plus ":"+nodeID
// ([nightPhaseRestingBackgroundNode]) - one node's own steps never share
// an outbox row identity with another's, so a refused step on one node
// can never block or corrupt another's state machine. The revision
// COUNTER (nightNextBackgroundAudioRevision, nightBackgroundAudioRevisionState)
// stays shared across every node's history under this same prefix: a
// shared, monotonically-advancing counter is still safe per node (it can
// only ever REQUIRE a revision higher than necessary, never lower), and
// sharing it avoids a second, per-node revision space to reason about.
const nightPhaseRestingBackground = "restingBackground"

// nightPhaseRestingBackgroundNode is nodeID's own phase under the
// background-audio family.
func nightPhaseRestingBackgroundNode(nodeID string) string {
	return nightPhaseRestingBackground + ":" + nodeID
}

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
	nightBGStepApply         = "apply"
	nightBGStepGain          = "gain"
	nightBGStepStart         = "start"
	nightBGStepPause         = "pause"
	nightBGStepResume        = "resume"
	nightBGStepStop          = "stop"
	nightBGStepFadeDown      = "fadedown"
	nightBGStepFadeUp        = "fadeup"
	nightBGStepExpiryRefresh = "expiryrefresh"
)

// nightBackgroundAudioExpiryTTL is how long the agent keeps a bed session
// alive, per audio.session.apply's own expiresInMs param, before
// restore.go retires it as unclaimed. nightBackgroundAudioExpiryRefreshInterval
// is how often this controller re-sends it while steadily playing: well
// under the TTL so a missed tick or two never lets the deadline lapse.
const (
	nightBackgroundAudioExpiryTTL             = 10 * time.Minute
	nightBackgroundAudioExpiryRefreshInterval = 4 * time.Minute
)

// nightBackgroundAudioStep is one parsed night_cue_outbox row under this
// controller's background-audio phase.
type nightBackgroundAudioStep struct {
	Seq  int
	Kind string
}

func nightBackgroundAudioCueNameApply(seq int) string    { return fmt.Sprintf("bg-%04d-apply", seq) }
func nightBackgroundAudioCueNameGain(seq int) string     { return fmt.Sprintf("bg-%04d-gain", seq) }
func nightBackgroundAudioCueNameStart(seq int) string    { return fmt.Sprintf("bg-%04d-start", seq) }
func nightBackgroundAudioCueNamePause(seq int) string    { return fmt.Sprintf("bg-%04d-pause", seq) }
func nightBackgroundAudioCueNameResume(seq int) string   { return fmt.Sprintf("bg-%04d-resume", seq) }
func nightBackgroundAudioCueNameStop(seq int) string     { return fmt.Sprintf("bg-%04d-stop", seq) }
func nightBackgroundAudioCueNameFadeDown(seq int) string { return fmt.Sprintf("bg-%04d-fadedown", seq) }
func nightBackgroundAudioCueNameFadeUp(seq int) string   { return fmt.Sprintf("bg-%04d-fadeup", seq) }
func nightBackgroundAudioCueNameExpiryRefresh(seq int) string {
	return fmt.Sprintf("bg-%04d-expiryrefresh", seq)
}

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

// nightParseBackgroundAudioRow classifies one outbox row already known to
// belong to this session's background-audio phase family and recovers
// the node it addressed. false means the row does not match any
// recognized shape - never expected in practice, answered rather than
// panicking on a malformed row. A row left behind by an older build
// under a phase this one no longer writes lands here too, and is dropped
// rather than mistaken for a step.
func nightParseBackgroundAudioRow(row store.NightCueOutboxRecord) (step nightBackgroundAudioStep, nodeID string, ok bool) {
	nodeID, ok = strings.CutPrefix(row.Phase, nightPhaseRestingBackground+":")
	if !ok || nodeID == "" {
		return nightBackgroundAudioStep{}, "", false
	}
	seq, ok := nightBackgroundAudioSeqFromCueName(row.CueName)
	if !ok {
		return nightBackgroundAudioStep{}, "", false
	}
	switch {
	case strings.HasSuffix(row.CueName, "-apply"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepApply}, nodeID, true
	case strings.HasSuffix(row.CueName, "-gain"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepGain}, nodeID, true
	case strings.HasSuffix(row.CueName, "-start"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepStart}, nodeID, true
	case strings.HasSuffix(row.CueName, "-pause"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepPause}, nodeID, true
	case strings.HasSuffix(row.CueName, "-resume"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepResume}, nodeID, true
	case strings.HasSuffix(row.CueName, "-stop"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepStop}, nodeID, true
	case strings.HasSuffix(row.CueName, "-fadedown"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepFadeDown}, nodeID, true
	case strings.HasSuffix(row.CueName, "-fadeup"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepFadeUp}, nodeID, true
	case strings.HasSuffix(row.CueName, "-expiryrefresh"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepExpiryRefresh}, nodeID, true
	}
	return nightBackgroundAudioStep{}, "", false
}

// nightBackgroundAudioHistoryRow pairs a parsed step with the outbox row
// it came from.
type nightBackgroundAudioHistoryRow struct {
	Step   nightBackgroundAudioStep
	Row    store.NightCueOutboxRecord
	NodeID string

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

// nightBackgroundAudioStepsForNode is [nightBackgroundAudioSteps] further
// narrowed to nodeID's own steps - the state machine decides each
// node's own next step from that node's own step history alone, so
// a stalled or refused step on one node is never mistaken for another
// node's own latest step.
func nightBackgroundAudioStepsForNode(history []nightBackgroundAudioHistoryRow, nodeID string) []nightBackgroundAudioHistoryRow {
	out := make([]nightBackgroundAudioHistoryRow, 0, len(history))
	for _, row := range nightBackgroundAudioSteps(history) {
		if row.NodeID == nodeID {
			out = append(out, row)
		}
	}
	return out
}

// nightBackgroundAudioHistory returns every step ever recorded for rec's
// background-audio session, across every node and every cycle, sorted
// stably by Row.CreatedAt/rowid (the store's own insertion order). Used
// whole for revision counting (safe to share across nodes - see
// [nightPhaseRestingBackground]'s own doc comment) and narrowed to one
// node via [nightBackgroundAudioStepsForNode] for that node's own state
// machine decisions.
func (h *handlers) nightBackgroundAudioHistory(ctx context.Context, rec store.NightSessionRecord) ([]nightBackgroundAudioHistoryRow, error) {
	rows, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseRestingBackground)
	if err != nil {
		return nil, fmt.Errorf("api: list background-audio history: %w", err)
	}
	out := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	for _, r := range rows {
		step, nodeID, ok := nightParseBackgroundAudioRow(r)
		out = append(out, nightBackgroundAudioHistoryRow{Step: step, Row: r, NodeID: nodeID, Parsed: ok})
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

// nightBackgroundAudioInitialGainDb is the gain [nightBackgroundAudioGain]
// commits immediately before every audio.session.start: maxGainDb itself
// when no fade-in is configured (today's unchanged behavior), or silence
// when ba.FadeInMs is set. A fade-in bed must never be audible before its
// own fadeup step ramps it up, exactly the same "never audible for even
// one tick at the wrong gain" rule [nightBackgroundAudioGain]'s own doc
// comment already states for the no-fade case.
func nightBackgroundAudioInitialGainDb(ba *config.NightSessionBackgroundAudio) float64 {
	if ba.FadeInMs != nil {
		return pkgaudio.SilenceFloorDb
	}
	return ba.MaxGainDb
}

// audioSessionFadeStateSignalID mirrors internal/coordinator/collector/
// nodeaudio's SignalSessionFadeState literal, and
// audioSessionFadeStateInProgress mirrors internal/agent/audio.
// FadeStateInProgress's own wire value. This package must never import
// the collector package that produces the signal, nor internal/agent
// (TestPackageNeverImportsACollector, resolumeinstances_test.go;
// audionode.go's own audioOutputLocalCapabilityID precedent one file
// over) - copied literals here, like audioEngineStateSignalID one file
// over (nightaudioreadiness.go), for the same reason.
const audioSessionFadeStateSignalID observation.SignalID = "audio_session.fade.state"

const audioSessionFadeStateInProgress string = "in_progress"

// nightBackgroundAudioFadeSettled reports whether sessionID's own fade on
// nodeID has genuinely finished ramping, per the node's own reported
// audio_session.fade.state - never inferred from a fade command's own
// dispatch outcome, which [pkgaudio.Fade]'s doc comment is explicit is
// reported the instant the ramp starts, not when it ends. No CURRENT
// observation for this signal (a session whose telemetry has not yet
// reported one) is treated as NOT settled: dispatching the pause or stop
// this fade is meant to precede before this coordinator has ever heard
// from the session risks racing a ramp it cannot yet see, and racing it
// is exactly the defect this function exists to prevent.
func nightBackgroundAudioFadeSettled(audio NodeAudioLister, now time.Time, nodeID, sessionID string) bool {
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Signal != audioSessionFadeStateSignalID || o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		if o.StateAt(now) != observation.StateCurrent {
			return false
		}
		return o.Value != audioSessionFadeStateInProgress
	}
	return false
}

func nightAudioTarget(nodeID, sessionID, action string, params map[string]any) config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration:  config.ShowActionIntegrationAudio,
		AudioNodeIDs: config.AudioNodeIDList{nodeID}, AudioSessionID: sessionID, AudioAction: action, Params: params,
	}
}

// nightBackgroundApplyParams builds audio.session.apply's own wire
// params: a full pkgaudio.PlaylistRef pinning every configured item,
// repeat, resume, and requestedTransition on the node - the fields
// internal/agent/audiosessionops.go's parsePlaylistRef accepts, spelled
// exactly as it requires (ownerKind, ownerId, ownerRevision, items,
// repeat, resume, requestedTransition; each item itemId/index/assetId/
// contentHash/filename/sizeBytes). ceiling carries
// resting.backgroundAudio.maxGainDb, already converted to the linear
// pkgaudio.Ceiling this controller sends every gain against
// ([nightBackgroundCeilingGain]): this is a server-built target, not an
// operator's own HTTP request, so it is sent linear rather than as
// ceilingDb, matching audio.gain.set's own gain field one call below.
func nightBackgroundApplyParams(rec store.NightSessionRecord, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem) map[string]any {
	wireItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		wireItems = append(wireItems, map[string]any{
			"itemId": item.ItemID, "index": item.Index,
			"assetId": item.Media.AssetID, "contentHash": item.Media.ContentHash,
			"filename": item.Media.RuntimeFilename, "sizeBytes": item.Media.SizeBytes,
		})
	}
	_, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
	return map[string]any{
		"sourceRole": string(pkgaudio.SourceRoleBackground),
		"playlist": map[string]any{
			"ownerKind": "night.session.resting.backgroundAudio", "ownerId": rec.ConfigObjectID,
			"ownerRevision": rec.ConfigRevision, "items": wireItems,
			"repeat": ba.Repeat, "resume": ba.Resume, "requestedTransition": ba.ItemTransition,
		},
		"mixPolicy":   string(pkgaudio.MixPolicyMix),
		"expiresInMs": float64(nightBackgroundAudioExpiryTTL.Milliseconds()),
		"ceiling":     float64(ceiling),
	}
}

// nightAdvanceBackgroundAudio is nightTick's own per-tick entry point
// while rec is in preshow or a resting state. It never blocks: every call either
// resumes an in-flight step or decides and commits the next one,
// returning immediately either way. It runs
// [nightAdvanceBackgroundAudioForNode] independently for every node the
// bed plays on: each node's own state machine advances (or stalls, or is
// refused) entirely on that node's own step history - a refused or
// stalled node can never block another's.
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
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history", "sessionId", rec.ID, "error", err)
		return
	}
	for _, nodeID := range ba.OutputNodeIDs() {
		h.nightAdvanceBackgroundAudioForNode(ctx, now, rec, payload.Show, nodeID, ba, history)
	}
}

// nightAdvanceBackgroundAudioForNode is [nightAdvanceBackgroundAudio]'s
// own per-node body, unchanged in shape from the single-node state
// machine this coordinator has always run - a single-target installation
// (one node in ba.OutputNodeIDs()) behaves exactly as it always has.
func (h *handlers) nightAdvanceBackgroundAudioForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow) {
	sessionID := nightBackgroundAudioSessionID(rec)

	confirms, _, err := audioNodeConfirmsTransition(ctx, h.deps.Nodes, now, nodeID, pkgaudio.ItemTransition(ba.ItemTransition))
	if err != nil {
		h.logWarn("night loop: background audio: failed to read output node's capability advertisement", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
		return
	}
	if err := pkgaudio.ValidateItemTransitionSupport(pkgaudio.ItemTransition(ba.ItemTransition), confirms); err != nil {
		h.logWarn("night loop: background audio: requested item transition is not confirmed by the output; refusing to start", "sessionId", rec.ID, "nodeId", nodeID, "itemTransition", ba.ItemTransition, "error", err)
		return
	}

	items, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.ItemsForTarget(nodeID))
	if err != nil {
		h.logWarn("night loop: background audio: failed to resolve playlist items", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
		return
	}
	if len(items) == 0 {
		return
	}

	steps := nightBackgroundAudioStepsForNode(history, nodeID)
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
		h.nightBackgroundAudioGain(ctx, now, rec, nodeID, sessionID, nightBackgroundAudioInitialGainDb(ba), history)

	case nightBGStepGain:
		if !confirmed {
			h.nightBackgroundAudioGain(ctx, now, rec, nodeID, sessionID, nightBackgroundAudioInitialGainDb(ba), history) // retry under a fresh revision: never wedge here.
			return
		}
		h.nightBackgroundAudioStart(ctx, now, rec, nodeID, sessionID, history)

	case nightBGStepStart:
		if !confirmed {
			h.logWarn("night loop: background audio: start did not confirm; not auto-retrying", "sessionId", rec.ID, "outcome", latest.Row.Outcome)
			return
		}
		if ba.FadeInMs != nil {
			h.nightBackgroundAudioFadeUp(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, *ba.FadeInMs, history)
			return
		}
		// Confirmed, no fade-in configured: playing at full gain
		// already. The engine owns advancement and repeat from here;
		// only a due expiry refresh is left for this controller.
		h.nightMaybeRefreshBackgroundAudioExpiry(ctx, now, rec, nodeID, sessionID, latest, history)

	case nightBGStepFadeUp:
		if !confirmed {
			h.nightBackgroundAudioFadeUp(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, *ba.FadeInMs, history) // retry under a fresh revision: never wedge here.
			return
		}
		// Confirmed: ramping (or already arrived) at full gain again.
		// Nothing downstream depends on this fade's own true completion,
		// unlike fadedown before a suspend, so no further gate is needed.
		h.nightMaybeRefreshBackgroundAudioExpiry(ctx, now, rec, nodeID, sessionID, latest, history)

	case nightBGStepExpiryRefresh:
		if !confirmed {
			h.nightBackgroundAudioRefreshExpiry(ctx, now, rec, nodeID, sessionID, history) // retry under a fresh revision: never let expiry lapse from a stuck refresh.
			return
		}
		h.nightMaybeRefreshBackgroundAudioExpiry(ctx, now, rec, nodeID, sessionID, latest, history)

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
		if ba.FadeInMs != nil {
			h.nightBackgroundAudioFadeUp(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, *ba.FadeInMs, history)
			return
		}
		// Confirmed, no fade-in configured: playing again at full gain.
		h.nightMaybeRefreshBackgroundAudioExpiry(ctx, now, rec, nodeID, sessionID, latest, history)

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
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
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
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: gain failed", "sessionId", rec.ID, "error", err, "requested", float64(result.Requested), "effective", float64(result.Effective), "clamped", result.Clamped)
	}
}

// nightBackgroundAudioFadeDown dispatches audio.gain.fade toward silence
// over fadeOutMs - the DOWN half of the show-boundary fade pair
// ([config.NightSessionBackgroundAudio.FadeOutMs]'s own doc comment).
// Dispatched before pause/stop, never after; a dispatch that reports
// confirmed only means the ramp was accepted and started, never that it
// finished - see [nightBackgroundAudioFadeSettled], which the caller
// checks before ever dispatching the suspend this fade precedes.
func (h *handlers) nightBackgroundAudioFadeDown(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, fadeOutMs int, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameFadeDown(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{"targetGain": 0.0, "durationMs": float64(fadeOutMs)})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: fade-down failed", "sessionId", rec.ID, "error", err)
	}
}

// nightBackgroundAudioFadeUp dispatches audio.gain.fade from silence up
// to maxGainDb (through the same ceiling clamp [nightBackgroundAudioGain]
// applies) over fadeInMs - the UP half of the pair. Dispatched only after
// start or resume has already landed: the bed must already be playing
// (at near-silence) before this ramps it up, never the other way around.
func (h *handlers) nightBackgroundAudioFadeUp(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, maxGainDb float64, fadeInMs int, history []nightBackgroundAudioHistoryRow) {
	requested, ceiling := nightBackgroundCeilingGain(maxGainDb)
	result, err := pkgaudio.ApplyCeiling(requested, ceiling)
	if err != nil {
		h.logWarn("night loop: background audio: fade-up gain computation failed", "sessionId", rec.ID, "error", err)
		return
	}
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameFadeUp(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{"targetGain": float64(result.Effective), "durationMs": float64(fadeInMs)})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: fade-up failed", "sessionId", rec.ID, "error", err)
	}
}

func (h *handlers) nightBackgroundAudioStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameStart(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.start", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: start failed", "sessionId", rec.ID, "error", err)
	}
}

// nightBackgroundAudioExpiryRefreshDue reports whether enough real time
// has passed since latest's own confirmation to warrant re-sending the
// bed's expiresInMs before nightBackgroundAudioExpiryTTL lapses. An
// unresolved latest is never due here: its own retry path in
// [nightAdvanceBackgroundAudioForNode]'s case nightBGStepExpiryRefresh
// handles that.
func nightBackgroundAudioExpiryRefreshDue(now time.Time, latest nightBackgroundAudioHistoryRow) bool {
	if latest.Row.ResolvedAt == nil {
		return false
	}
	return now.Sub(*latest.Row.ResolvedAt) >= nightBackgroundAudioExpiryRefreshInterval
}

// nightBackgroundAudioRefreshExpiry re-sends audio.session.apply carrying
// only expiresInMs: every other ApplyRequest field stays
// [pkgaudio.FieldUnset] and so merges onto the session's current desired
// state unchanged (Manager.Apply merges rather than replaces), so this
// never reloads the engine or touches playback.
func (h *handlers) nightBackgroundAudioRefreshExpiry(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameExpiryRefresh(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.apply", map[string]any{
		"expiresInMs": float64(nightBackgroundAudioExpiryTTL.Milliseconds()),
	})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: expiry refresh failed", "sessionId", rec.ID, "error", err)
	}
}

// nightMaybeRefreshBackgroundAudioExpiry is the shared tail call for
// every "steadily playing, nothing else due" branch in
// [nightAdvanceBackgroundAudioForNode]: it re-sends expiresInMs only when
// [nightBackgroundAudioExpiryRefreshDue], so a bed left playing for a
// long show never has its retirement deadline lapse for want of a
// refresh.
func (h *handlers) nightMaybeRefreshBackgroundAudioExpiry(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, latest nightBackgroundAudioHistoryRow, history []nightBackgroundAudioHistoryRow) {
	if nightBackgroundAudioExpiryRefreshDue(now, latest) {
		h.nightBackgroundAudioRefreshExpiry(ctx, now, rec, nodeID, sessionID, history)
	}
}

func (h *handlers) nightBackgroundAudioResume(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameResume(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.resume", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
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
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history); err != nil {
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
		gain, ceiling := nightBackgroundCeilingGain(nightBackgroundAudioInitialGainDb(ba))
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
	case nightBGStepFadeDown:
		if ba.FadeOutMs == nil {
			return
		}
		target = nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{"targetGain": 0.0, "durationMs": float64(*ba.FadeOutMs)})
	case nightBGStepFadeUp:
		if ba.FadeInMs == nil {
			return
		}
		gain, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
		result, err := pkgaudio.ApplyCeiling(gain, ceiling)
		if err != nil {
			return
		}
		target = nightAudioTarget(nodeID, sessionID, "audio.gain.fade", map[string]any{"targetGain": float64(result.Effective), "durationMs": float64(*ba.FadeInMs)})
	case nightBGStepExpiryRefresh:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.apply", map[string]any{
			"expiresInMs": float64(nightBackgroundAudioExpiryTTL.Milliseconds()),
		})
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
// "stopped" while the bed keeps playing over the show. Runs
// independently per node, exactly like [nightAdvanceBackgroundAudio] -
// a stall on one node never withholds the stop/pause another node's own
// history already shows is due.
func (h *handlers) nightStopBackgroundAudioIfRunning(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || payload.Resting.BackgroundAudio == nil {
		return
	}
	ba := payload.Resting.BackgroundAudio
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history for stop", "sessionId", rec.ID, "error", err)
		return
	}
	for _, nodeID := range ba.OutputNodeIDs() {
		h.nightStopBackgroundAudioIfRunningForNode(ctx, now, rec, payload.Show, nodeID, ba, history)
	}
}

func (h *handlers) nightStopBackgroundAudioIfRunningForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow) {
	sessionID := nightBackgroundAudioSessionID(rec)
	steps := nightBackgroundAudioStepsForNode(history, nodeID)
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
		items, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.ItemsForTarget(nodeID))
		if err != nil {
			return
		}
		h.nightResumeBackgroundStep(ctx, now, rec, nodeID, sessionID, ba, items, latest, history)
		return
	}

	if ba.FadeOutMs == nil {
		h.nightBackgroundAudioStop(ctx, now, rec, nodeID, sessionID, ba.Resume, history)
		return
	}

	confirmed := latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed
	if latest.Step.Kind == nightBGStepFadeDown && confirmed {
		if !nightBackgroundAudioFadeSettled(h.deps.Audio, now, nodeID, sessionID) {
			return // the ramp is still running; never let pause/stop race it (see nightBackgroundAudioFadeSettled's own doc comment).
		}
		h.nightBackgroundAudioStop(ctx, now, rec, nodeID, sessionID, ba.Resume, history)
		return
	}
	// Either the fadedown step has never been dispatched yet, or its own
	// prior attempt did not confirm - either way, (re)dispatch it under a
	// fresh revision rather than skipping straight to pause/stop.
	h.nightBackgroundAudioFadeDown(ctx, now, rec, nodeID, sessionID, *ba.FadeOutMs, history)
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
	sessionID := nightBackgroundAudioSessionID(rec)
	for _, nodeID := range ba.OutputNodeIDs() {
		h.nightClearBackgroundAudioAtEndSessionForNode(ctx, now, nodeID, sessionID)
	}
}

// nightClearBackgroundAudioAtEndSessionForNode is
// [handlers.nightClearBackgroundAudioAtEndSession]'s own per-node body: one
// node's own clear attempt, warned and abandoned independently of every
// other node's own outcome.
func (h *handlers) nightClearBackgroundAudioAtEndSessionForNode(ctx context.Context, now time.Time, nodeID, sessionID string) {
	clearRevision := h.nightAudioSessionPersistedRevision(ctx, nodeID, sessionID) + 1
	idemKey := fmt.Sprintf("night-end-session-clear:%s:%s", nodeID, sessionID)
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

// nightAnchorPurposeEndSessionClear marks the anchor nightTick's own
// stopped-state retry (nightRetryEndSessionClear) uses to track end-
// session's own background-audio clear when its synchronous, warn-and-
// proceed first attempt (nightClearBackgroundAudioAtEndSession, above)
// does not land - the node unreachable, refused, or unacknowledged.
// Mirrors nightAnchorPurposeShutdownStop (nightshutdown.go) one-for-one:
// an anchor with DispatchedAt/ObservedAt both zero has never confirmed and
// is retried; ObservedAt non-zero means it genuinely confirmed, and this
// anchor is then permanently done for the life of this stopped session -
// nightEndSessionDecide leaves ContentAnchorJSON untouched from whatever
// state preceded end-session, so a stale, unrelated anchor is discarded by
// its Purpose not matching, exactly as nightAdvanceFadingOut already does
// for shutdown-stop.
const nightAnchorPurposeEndSessionClear = "end-session-clear"

// nightEndSessionClearIdempotencyKey is stable for one attempt, so a crash
// mid-dispatch replays rather than double-sending, but a NEW attempt
// number takes a NEW key deliberately - mirrors
// nightShutdownStopIdempotencyKey (nightshutdown.go) exactly, for the
// exact same reason: a reused key would be silently answered from a
// cache, and this coordinator has never sent anything new.
//
// TWO caches, in TWO PROCESSES, both keyed by this same invocation
// identity, and a fresh key per attempt is what defeats BOTH - neither
// substitutes for the other:
//   - Coordinator-side, executeAudioSessionDispatch's own idempotency-
//     first replay (audiodispatch.go's InsertCommand duplicate-key path,
//     resolveAudioSessionReplay) answers a reused key with the FIRST
//     attempt's own recorded outcome and dispatches nothing at all - the
//     agent never even hears a retry that reused the original end-session
//     key, which is the defect this review finding exists to fix.
//   - Agent-side, Session.dispatchLocked (internal/agent/audio/session.go)
//     checks its own executedResults[invocation] cache AFTER the revision
//     check, also keyed by this same invocation id - a fresh key that
//     only fixed the coordinator side would still be answered from this
//     cache without executing Clear again.
//
// audio.session.clear's own stale-revision exemption
// (dispatchExemptFromStaleRevision, same file) is a separate, THIRD
// mechanism: once past both caches above, it is what stops the agent
// refusing a genuinely fresh invocation id whose revision happens not to
// exceed its own current one (ReasonStaleRevision). It does not defeat
// either cache and does not by itself make a retry converge - a reused
// key still replays from cache regardless of what the exemption allows.
//
// SHARP EDGE: the exemption covers ReasonStaleRevision only. Reusing an
// invocation id while its revision changes yields
// ReasonInvocationRevisionMismatch instead, which is NOT exempt and
// refuses outright - so this key and its paired revision
// (nightDispatchEndSessionClearRetry's own clearRevision) must always
// change together, per attempt, never a reused key against a moving
// revision.
func nightEndSessionClearIdempotencyKey(nodeID, sessionID string, attempt int64) string {
	if attempt == 0 {
		return "night-end-session-clear-retry:" + nodeID + ":" + sessionID
	}
	return fmt.Sprintf("night-end-session-clear-retry:%s:%s:%d", nodeID, sessionID, attempt)
}

// nightRetryEndSessionClear is nightTick's own stopped-state entry point -
// see nightloop.go's own case nightStateStopped comment for why doing
// nothing here would reintroduce, one layer down, the exact problem
// end-session's own clear exists to fix. A confirmed anchor costs only
// this function's own JSON decode to recheck: no payload read, no store
// call, no dispatch.
func (h *handlers) nightRetryEndSessionClear(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if has && anchor.Purpose == nightAnchorPurposeEndSessionClear && !anchor.ObservedAt.IsZero() {
		return // genuinely confirmed cleared already; nothing more to do.
	}
	if !has || anchor.Purpose != nightAnchorPurposeEndSessionClear {
		anchor = nightContentAnchor{Purpose: nightAnchorPurposeEndSessionClear}
	}

	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: end-session clear retry: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	ba := payload.Resting.BackgroundAudio
	if ba == nil {
		return
	}
	nodeIDs := ba.OutputNodeIDs()
	if len(nodeIDs) == 0 {
		return
	}
	// KNOWN GAP, flagged for an owner decision: this retry safety net
	// tracks its confirmation via ONE anchor slot on the record
	// (ContentAnchorJSON), which can only represent one node's own
	// retry state. A bed configured onto more than one node only gets
	// this crash-recovery retry for its first target node
	// (nodeIDs[0]); nightClearBackgroundAudioAtEndSession's own
	// synchronous warn-and-proceed attempt above still reaches every
	// node once, so only a node whose SYNCHRONOUS attempt also failed
	// is left unrecovered by this tick-based safety net.
	sessionID := nightBackgroundAudioSessionID(rec)
	h.nightDispatchEndSessionClearRetry(ctx, now, rec, nodeIDs[0], sessionID, anchor)
}

// nightDispatchEndSessionClearRetry issues one clear attempt and persists
// the anchor's next state, mirroring nightDispatchShutdownStop's own
// dispatch/persist shape (nightshutdown.go). Every non-confirming outcome
// - a plain error, a structural refusal, or a resolved-but-not-confirmed
// result - takes the SAME path: advance Attempts so the next tick's
// idempotency key is genuinely new, per [nightEndSessionClearIdempotencyKey]'s
// own doc comment. Nothing here fails end-session itself; it already
// reached stopped durably before nightTick ever calls this.
func (h *handlers) nightDispatchEndSessionClearRetry(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, anchor nightContentAnchor) {
	retry := func(reason string) {
		next := anchor
		next.Purpose, next.FPPInstanceID = nightAnchorPurposeEndSessionClear, nodeID
		next.DispatchedAt = time.Time{}
		next.AttemptedAt = now
		next.Attempts = anchor.Attempts + 1
		next.Source = reason
		h.nightCommitEndSessionClearAnchor(ctx, now, rec, next)
	}

	idemKey := nightEndSessionClearIdempotencyKey(nodeID, sessionID, anchor.Attempts)
	clearRevision := h.nightAudioSessionPersistedRevision(ctx, nodeID, sessionID) + 1
	result, problem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.clear", NodeID: nodeID, SessionID: sessionID,
		Params: map[string]any{
			"sessionId": sessionID, "invocationId": idemKey, "revision": uint64(clearRevision),
		},
		Revision: uint64(clearRevision), IdempotencyKey: idemKey,
		IssuerID: "night-controller", IssuerName: "night controller",
	})
	if err != nil {
		h.logWarn("night loop: end-session clear retry: dispatch failed", "sessionId", sessionID, "nodeId", nodeID, "error", err)
		retry("the clear could not be dispatched: " + err.Error())
		return
	}
	if problem != nil {
		h.logWarn("night loop: end-session clear retry: refused", "sessionId", sessionID, "nodeId", nodeID, "reason", problem.Detail)
		retry("refused: " + problem.Detail)
		return
	}
	if nightAudioCueOutcome(result.Outcome) != nightCueOutcomeConfirmed {
		h.logWarn("night loop: end-session clear retry: did not confirm", "sessionId", sessionID, "nodeId", nodeID, "outcome", result.Outcome, "reason", result.Reason)
		retry("not confirmed: " + result.Reason)
		return
	}

	next := anchor
	next.Purpose, next.FPPInstanceID = nightAnchorPurposeEndSessionClear, nodeID
	next.DispatchedAt = now
	next.ObservedAt = now // confirmed: permanently done for this stopped session.
	next.AttemptedAt = now
	next.Source = result.Reason
	h.nightCommitEndSessionClearAnchor(ctx, now, rec, next)
}

// nightCommitEndSessionClearAnchor persists anchor only while rec is still
// the current session in state stopped, matching nightCommit's own
// standard "moved out from under this tick" guard.
func (h *handlers) nightCommitEndSessionClearAnchor(ctx context.Context, now time.Time, rec store.NightSessionRecord, anchor nightContentAnchor) {
	h.nightCommit(ctx, now, rec.ID, nightStateStopped, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		return cur
	})
}
