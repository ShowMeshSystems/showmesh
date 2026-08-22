package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F5's own resting.backgroundAudio lifecycle: unlike a cue,
// this is a CONTINUOUS session the controller starts on entering resting
// and stops on the way out, not a one-shot dispatch triggered by an
// offset.
//
// It reuses night_cue_outbox as its own durable command log rather than a
// new table: each background-audio "step" (apply a media item, start
// playback, or stop) is one outbox row, committed before dispatch and
// resumed exactly the way nightDispatchAndPersistCue's two crash-window
// hooks already prove for cues (nightcuerun.go). phase is
// [nightPhaseRestingBackground] for a step the background controller
// itself decided to take, and [nightPhaseAnnouncement] for a duck,
// restore, or interrupt-stop step an announcement cue's own policy
// requires against the SAME background pkg/audio session - see
// nightannouncement.go. Every step's cueName embeds a sequence number
// from ONE shared counter across both phases (nightBackgroundAudioHistory
// merges them), because they all address the same pkg/audio session id
// and its RevisionState enforces one strictly-increasing revision space
// regardless of which phase recorded the command that used it.
//
// Known, deliberate limits (see this builder's own report for the
// reasoning): a bookmark's Position is always zero - resume restarts at
// the TOP of the last-known item, not its last-known position, because
// nothing in this codebase polls audio_session.position_ms continuously
// enough to trust a resumed position; a failed apply/start is logged and
// left for an operator rather than auto-retried indefinitely; and
// gapless/crossfade item transitions can never be confirmed because no
// audio.node capability signal for them exists yet (ValidateItemTransition
// Support's outputConfirms is always false here), so configuring one
// refuses background audio outright rather than silently playing
// sequential.

// nightPhaseRestingBackground and nightPhaseAnnouncement are night_cue_outbox.phase
// values distinct from nightPhaseEnterShow/EnterResting/FadeOut
// (nightcuerun.go): a background-audio or announcement step is never an
// enterShow/enterResting cue row and must never collide with one.
const (
	nightPhaseRestingBackground = "restingBackground"
	nightPhaseAnnouncement      = "announcement"
)

// nightBackgroundAudioSessionID is this session's own deterministic
// pkg/audio.SessionID: stable for the whole lifetime of the night.session
// record, never reset per cycle, so the node's own RevisionState for it
// persists exactly as long as this identity does.
func nightBackgroundAudioSessionID(rec store.NightSessionRecord) string {
	return "night-bg:" + rec.ID
}

// nightBackgroundAudioStep is one parsed night_cue_outbox.cue_name under
// either background-audio phase. Kind is "apply", "start", or "stop".
// ItemID is set for "apply"/"start". InterruptCuePhase/InterruptCueName
// are set only for an interrupt's own stop step, naming the announcement
// cue that stop must wait to see resolved before background audio may
// restart (nightAdvanceBackgroundAudio's own restart gate).
type nightBackgroundAudioStep struct {
	Seq               int
	Kind              string
	ItemID            string
	InterruptCuePhase string
	InterruptCueName  string
}

var (
	nightBGApplyPattern  = regexp.MustCompile(`^bg-(\d{4,})-apply-(.+)$`)
	nightBGStartPattern  = regexp.MustCompile(`^bg-(\d{4,})-start-(.+)$`)
	nightBGStopPattern   = regexp.MustCompile(`^bg-(\d{4,})-stop$`)
	nightBGInterruptStop = regexp.MustCompile(`^bg-(\d{4,})-stop-interrupt-(.+)-(.+)$`)
)

func nightBackgroundAudioCueNameApply(seq int, itemID string) string {
	return fmt.Sprintf("bg-%04d-apply-%s", seq, itemID)
}
func nightBackgroundAudioCueNameStart(seq int, itemID string) string {
	return fmt.Sprintf("bg-%04d-start-%s", seq, itemID)
}
func nightBackgroundAudioCueNameStop(seq int) string {
	return fmt.Sprintf("bg-%04d-stop", seq)
}
func nightBackgroundAudioCueNameInterruptStop(seq int, cuePhase, cueName string) string {
	return fmt.Sprintf("bg-%04d-stop-interrupt-%s-%s", seq, cuePhase, cueName)
}

// parseNightBackgroundAudioCueName parses a cueName produced by one of the
// nightBackgroundAudioCueName* constructors above. false means the name
// does not match this scheme at all (never expected once a row exists
// under one of the two background-audio phases, but answered rather than
// panicking on a malformed row).
func parseNightBackgroundAudioCueName(name string) (nightBackgroundAudioStep, bool) {
	if m := nightBGInterruptStop.FindStringSubmatch(name); m != nil {
		seq, err := strconv.Atoi(m[1])
		if err != nil {
			return nightBackgroundAudioStep{}, false
		}
		return nightBackgroundAudioStep{Seq: seq, Kind: "stop", InterruptCuePhase: m[2], InterruptCueName: m[3]}, true
	}
	if m := nightBGStopPattern.FindStringSubmatch(name); m != nil {
		seq, err := strconv.Atoi(m[1])
		if err != nil {
			return nightBackgroundAudioStep{}, false
		}
		return nightBackgroundAudioStep{Seq: seq, Kind: "stop"}, true
	}
	if m := nightBGApplyPattern.FindStringSubmatch(name); m != nil {
		seq, err := strconv.Atoi(m[1])
		if err != nil {
			return nightBackgroundAudioStep{}, false
		}
		return nightBackgroundAudioStep{Seq: seq, Kind: "apply", ItemID: m[2]}, true
	}
	if m := nightBGStartPattern.FindStringSubmatch(name); m != nil {
		seq, err := strconv.Atoi(m[1])
		if err != nil {
			return nightBackgroundAudioStep{}, false
		}
		return nightBackgroundAudioStep{Seq: seq, Kind: "start", ItemID: m[2]}, true
	}
	return nightBackgroundAudioStep{}, false
}

// nightBackgroundAudioHistoryRow pairs a parsed step with the outbox row
// it came from.
type nightBackgroundAudioHistoryRow struct {
	Step nightBackgroundAudioStep
	Row  store.NightCueOutboxRecord
}

// nightBackgroundAudioHistory returns every background-audio step ever
// recorded for rec, across BOTH phases and every cycle, sorted by seq -
// the reconstructed log nightAdvanceBackgroundAudio and the announcement
// duck/restore/interrupt path both read to decide their next action, and
// the same log a restarted coordinator rebuilds its
// [pkgaudio.RevisionState] from (see [nightBackgroundAudioRevisionState]).
func (h *handlers) nightBackgroundAudioHistory(ctx context.Context, rec store.NightSessionRecord) ([]nightBackgroundAudioHistoryRow, error) {
	var rows []store.NightCueOutboxRecord
	for _, phase := range []string{nightPhaseRestingBackground, nightPhaseAnnouncement} {
		r, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhase(ctx, rec.ID, phase)
		if err != nil {
			return nil, fmt.Errorf("api: list background-audio history (%s): %w", phase, err)
		}
		rows = append(rows, r...)
	}
	out := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	for _, r := range rows {
		step, ok := parseNightBackgroundAudioCueName(r.CueName)
		if !ok {
			continue
		}
		out = append(out, nightBackgroundAudioHistoryRow{Step: step, Row: r})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Step.Seq > out[j].Step.Seq; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

// nightBackgroundAudioRevisionState rebuilds a [pkgaudio.RevisionState]
// from history via [pkgaudio.RestoreRevisionState] - the durability
// discipline the issue asks for: current is the highest revision any
// resolved-confirmed step used, and prior seeds one recorded decision per
// step's own idempotency key so a replayed invocation after a coordinator
// restart resolves identically to its first attempt rather than
// re-deciding from a reset current=0.
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

// nightNextBackgroundAudioRevision is the next revision to mint for a new
// step: history's own highest ActionRevision, plus one. Never resets
// across a restart because history is read fresh from the store, and
// never resets across cycles because history spans every cycle.
func nightNextBackgroundAudioRevision(history []nightBackgroundAudioHistoryRow) int64 {
	var max int64
	for _, h := range history {
		if h.Row.ActionRevision > max {
			max = h.Row.ActionRevision
		}
	}
	return max + 1
}

// nightBackgroundAudioAsIssuer is the system actor every background-audio
// and announcement duck/restore dispatch uses, mirroring
// nightControllerIssuer's own reasoning one file over: an autonomous
// controller action, never a still-live user token.
func nightBackgroundAudioIssuer(rec store.NightSessionRecord) FPPCommandIssuer {
	return nightControllerIssuer(rec)
}

// nightRunAudioCommand commits (or resumes) one durable background-audio/
// announcement step and dispatches it, reusing nightDispatchAndPersistCue
// unchanged (nightcuerun.go) - the SAME commit-then-dispatch discipline
// and crash-window hooks the cue outbox already proves, generalized past
// [nightPhaseEnterShow]/[nightPhaseEnterResting] to these two phases with
// a caller-supplied target and revision instead of one resolved from a
// show.action object.
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
		// than an in-memory value that a restart would reset to zero:
		// [pkgaudio.RestoreRevisionState] rebuilds exactly the state this
		// session would be in had the coordinator never restarted, and
		// Apply refuses a revision that does not strictly advance past
		// it - the same rule the node's own RevisionState enforces, kept
		// here too so a coordinator-side bug can never even attempt to
		// send one.
		rs := nightBackgroundAudioRevisionState(target.AudioSessionID, history)
		decision := rs.Apply(pkgaudio.InvocationID(idemKey), pkgaudio.Revision(revision))
		if !decision.Accepted {
			reason := "revision not accepted"
			if decision.Result != nil {
				reason = decision.Result.Reason
			}
			return store.NightCueOutboxRecord{}, fmt.Errorf("api: background audio: refusing to commit %s at revision %d: %s (current %d)", cueName, revision, reason, decision.Revision)
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
// ENGINE §3's "fails visibly instead of guessing" rule for a missing item.
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

func nightBackgroundItemIndex(items []pkgaudio.PlaylistItem, itemID string) int {
	for i, it := range items {
		if it.ItemID == itemID {
			return i
		}
	}
	return -1
}

// nightNextBackgroundItem is AUDIO-ENGINE §3's own item-advance rule:
// repeat "item" always repeats the current item; otherwise the next list
// position plays; past the last item, repeat "playlist" wraps to the
// first and repeat "none" ends the session. false means "no next item" -
// natural completion with nothing more to play, never a guess.
func nightNextBackgroundItem(items []pkgaudio.PlaylistItem, currentItemID string, repeat string) (pkgaudio.PlaylistItem, bool) {
	idx := nightBackgroundItemIndex(items, currentItemID)
	if idx == -1 {
		return pkgaudio.PlaylistItem{}, false
	}
	if repeat == config.NightSessionBackgroundRepeatItem {
		return items[idx], true
	}
	if idx+1 < len(items) {
		return items[idx+1], true
	}
	if repeat == config.NightSessionBackgroundRepeatPlaylist {
		return items[0], true
	}
	return pkgaudio.PlaylistItem{}, false
}

// nightBackgroundStartItem picks the item a fresh (re)start plays first.
// resume policy "resume" replays the last item this history ever
// successfully started, IF that item still exists in the current
// playlist - a stale bookmark (an item id the current config no longer
// carries) is never guessed past; it silently falls back to "restart",
// exactly as AUDIO-ENGINE §3 requires. Position is always the item's own
// start (see this file's own top doc comment for why).
func nightBackgroundStartItem(items []pkgaudio.PlaylistItem, resume string, lastStartedItemID string, haveLastStarted bool) pkgaudio.PlaylistItem {
	if resume == config.NightSessionBackgroundResumeResume && haveLastStarted {
		if idx := nightBackgroundItemIndex(items, lastStartedItemID); idx != -1 {
			return items[idx]
		}
	}
	return items[0]
}

// nightLastStartedBackgroundItem scans history in order and returns the
// item id of the most recent "start" step, whatever its own outcome -
// used both for the resume bookmark and to know what an interrupt must
// restart after it resolves.
func nightLastStartedBackgroundItem(history []nightBackgroundAudioHistoryRow) (string, bool) {
	itemID, found := "", false
	for _, h := range history {
		if h.Step.Kind == "start" {
			itemID, found = h.Step.ItemID, true
		}
	}
	return itemID, found
}

// nightBackgroundCeilingGain converts night.session's own maxGainDb
// (dBFS, always <= 0 per DecodeNightSessionPayload) into a linear
// [pkgaudio.Gain]/[pkgaudio.Ceiling] pair - this coordinator's own
// dB-to-linear convention (no other seam has needed one yet).
func nightBackgroundCeilingGain(maxGainDb float64) (pkgaudio.Gain, pkgaudio.Ceiling) {
	linear := dbToLinearGain(maxGainDb)
	return pkgaudio.Gain(linear), pkgaudio.Ceiling(linear)
}

func dbToLinearGain(db float64) float64 {
	return math.Pow(10, db/20)
}

func nightBackgroundApplyParams(item pkgaudio.PlaylistItem, nodeID string, gain, ceiling float64) map[string]any {
	return map[string]any{
		"sourceRole": string(pkgaudio.SourceRoleBackground),
		"media": map[string]any{
			"itemId":      item.ItemID,
			"assetId":     item.Media.AssetID,
			"contentHash": item.Media.ContentHash,
			"filename":    item.Media.RuntimeFilename,
			"sizeBytes":   item.Media.SizeBytes,
		},
		"outputs":   []string{nodeID},
		"mixPolicy": string(pkgaudio.MixPolicyMix),
	}
}

func nightAudioTarget(nodeID, sessionID, action string, params map[string]any) config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationAudio,
		AudioNodeID: nodeID, AudioSessionID: sessionID, AudioAction: action, Params: params,
	}
}

// nightReadAudioSessionSignal reads the freshest observation for
// (audio_session, sessionID, signal) - the same [ObservationLister]
// contract nightloop.go already reads FPP evidence through, narrowed to
// this resource kind.
func (h *handlers) nightReadAudioSessionSignal(ctx context.Context, sessionID string, signal observation.SignalID) (string, bool) {
	kind := observation.ResourceAudioSession
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &sessionID, Signal: &signal})
	if err != nil {
		return "", false
	}
	var latest observation.Observation
	found := false
	// A caller-supplied ObservationLister is documented to already
	// narrow by filter (interfaces.go's own doc comment), but this
	// defends against one that does not (as this seam's own test fake
	// deliberately does not): a result naming the wrong resource or
	// signal is never mistaken for the one asked for.
	for _, o := range obs {
		if o.Resource.Kind != kind || o.Resource.ID != sessionID || o.Signal != signal {
			continue
		}
		if !found || o.CollectedAt.After(latest.CollectedAt) {
			latest, found = o, true
		}
	}
	if !found || latest.Value == nil {
		return "", false
	}
	s, ok := latest.Value.(string)
	return s, ok
}

// nightBackgroundItemCompleted reports whether sessionID's own observed
// state is [pkgaudio.StateCompleted] AND the completed item matches
// itemID - Completed is permanently distinct from Stopped (never treat a
// commanded stop as natural completion), and a completion reported for a
// DIFFERENT item id than the one this controller believes is playing is
// never advanced past, matching "never guesses" for the observation side
// too.
func (h *handlers) nightBackgroundItemCompleted(ctx context.Context, sessionID, itemID string) bool {
	state, ok := h.nightReadAudioSessionSignal(ctx, sessionID, "audio_session.state")
	if !ok || pkgaudio.State(state) != pkgaudio.StateCompleted {
		return false
	}
	observedItem, ok := h.nightReadAudioSessionSignal(ctx, sessionID, "audio_session.playlist.item_id")
	return ok && observedItem == itemID
}

// nightAdvanceBackgroundAudio is nightTick's own per-tick entry point
// while rec is in a resting state. It never blocks on the network: every
// call either resumes an in-flight step or decides and commits the next
// one, returning immediately either way, exactly like the cue advance
// loop it mirrors.
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

	if len(history) == 0 {
		h.nightBackgroundAudioApplyStep(ctx, now, rec, nodeID, sessionID, nightBackgroundStartItem(items, ba.Resume, "", false), history)
		return
	}
	latest := history[len(history)-1]

	if latest.Row.State == nightCueStatePending || latest.Row.State == nightCueStateDispatched {
		h.nightResumeBackgroundStep(ctx, now, rec, nodeID, sessionID, latest, ba, items, history)
		return
	}

	switch latest.Step.Kind {
	case "stop":
		if latest.Step.InterruptCuePhase != "" {
			row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, latest.Step.InterruptCuePhase, latest.Step.InterruptCueName)
			if err != nil || row.State != nightCueStateResolved {
				return // the announcement this interrupt made room for has not finished yet.
			}
		}
		lastItemID, have := nightLastStartedBackgroundItem(history)
		start := nightBackgroundStartItem(items, ba.Resume, lastItemID, have)
		h.nightBackgroundAudioApplyStep(ctx, now, rec, nodeID, sessionID, start, history)

	case "apply":
		if latest.Row.Outcome != nightCueOutcomeConfirmed {
			h.logWarn("night loop: background audio: apply did not confirm; not auto-retrying", "sessionId", rec.ID, "item", latest.Step.ItemID, "outcome", latest.Row.Outcome)
			return
		}
		h.nightBackgroundAudioStartStep(ctx, now, rec, nodeID, sessionID, latest.Step.ItemID, history)

	case "start":
		if latest.Row.Outcome != nightCueOutcomeConfirmed {
			h.logWarn("night loop: background audio: start did not confirm; not auto-retrying", "sessionId", rec.ID, "item", latest.Step.ItemID, "outcome", latest.Row.Outcome)
			return
		}
		if !h.nightBackgroundItemCompleted(ctx, sessionID, latest.Step.ItemID) {
			return
		}
		next, ok := nightNextBackgroundItem(items, latest.Step.ItemID, ba.Repeat)
		if !ok {
			return // natural end: repeat "none" past the last item.
		}
		h.nightBackgroundAudioApplyStep(ctx, now, rec, nodeID, sessionID, next, history)
	}
}

func (h *handlers) nightBackgroundAudioApplyStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, item pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || payload.Resting.BackgroundAudio == nil {
		return
	}
	ba := payload.Resting.BackgroundAudio
	gain, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
	params := nightBackgroundApplyParams(item, nodeID, float64(gain), float64(ceiling))
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameApply(int(revision), item.ItemID)
	target := nightAudioTarget(nodeID, sessionID, "audio.session.apply", params)
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: apply failed", "sessionId", rec.ID, "item", item.ItemID, "error", err)
	}
}

func (h *handlers) nightBackgroundAudioStartStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID, itemID string, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameStart(int(revision), itemID)
	target := nightAudioTarget(nodeID, sessionID, "audio.session.start", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: start failed", "sessionId", rec.ID, "item", itemID, "error", err)
	}
}

// nightResumeBackgroundStep re-attempts an in-flight (pending or
// dispatched) step under its own already-committed identity - audio is
// retryable by identity ([nightCueRetryableByIdentity]), so this can
// never double-play.
func (h *handlers) nightResumeBackgroundStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, latest nightBackgroundAudioHistoryRow, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) {
	revision := latest.Row.ActionRevision
	var target config.ShowActionTarget
	switch latest.Step.Kind {
	case "apply":
		idx := nightBackgroundItemIndex(items, latest.Step.ItemID)
		if idx == -1 {
			return
		}
		gain, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
		target = nightAudioTarget(nodeID, sessionID, "audio.session.apply", nightBackgroundApplyParams(items[idx], nodeID, float64(gain), float64(ceiling)))
	case "start":
		target = nightAudioTarget(nodeID, sessionID, "audio.session.start", map[string]any{})
	case "stop":
		target = nightAudioTarget(nodeID, sessionID, "audio.session.stop", map[string]any{})
	default:
		return
	}
	if _, err := h.nightRunAudioCommand(ctx, now, rec, latest.Row.Phase, latest.Row.CueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: resume failed", "sessionId", rec.ID, "cueName", latest.Row.CueName, "error", err)
	}
}

// nightStopBackgroundAudioIfRunning is nightTick's own entry point for
// every non-resting state: stops a background-audio session that is
// still logically playing (its latest step is an apply/start, or an
// unresolved stop), idempotently - a session already stopped, or never
// started, does nothing.
func (h *handlers) nightStopBackgroundAudioIfRunning(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	sessionID := nightBackgroundAudioSessionID(rec)
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history for stop", "sessionId", rec.ID, "error", err)
		return
	}
	if len(history) == 0 {
		return
	}
	latest := history[len(history)-1]
	if latest.Step.Kind == "stop" && latest.Row.State == nightCueStateResolved {
		return
	}

	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || payload.Resting.BackgroundAudio == nil {
		return
	}
	nodeID := payload.Resting.BackgroundAudio.OutputNodeID()

	if latest.Step.Kind == "stop" {
		// A stop is already committed but unresolved (pending/dispatched):
		// resume it under its own identity rather than minting a new one.
		target := nightAudioTarget(nodeID, sessionID, "audio.session.stop", map[string]any{})
		if _, err := h.nightRunAudioCommand(ctx, now, rec, latest.Row.Phase, latest.Row.CueName, target, latest.Row.ActionRevision, history); err != nil {
			h.logWarn("night loop: background audio: stop resume failed", "sessionId", rec.ID, "error", err)
		}
		return
	}

	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameStop(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.stop", map[string]any{})
	if _, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackground, cueName, target, revision, history); err != nil {
		h.logWarn("night loop: background audio: stop failed", "sessionId", rec.ID, "error", err)
	}
}
