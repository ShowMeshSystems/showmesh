package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/capability"
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

	// nightBGStepSchedule is ADR-049 decisions 7-9's own addition, for a
	// multi-node (declared-Targets) bed only - see this file's own
	// "multi-node bed" section further down. It is a BED-LEVEL step (its
	// "node" is [nightBedScheduleNodeID], a sentinel never a real
	// audio.node id): the one shared start/resume instant chosen once per
	// (session record, cycle), recorded so a replay tick reads no clock
	// and dispatches nothing new (nightGetOrComputeBedSchedule). The
	// program+ltc node's own resume bookmark travels on audio.session.
	// resume itself (amended ruling); no separate push step exists.
	nightBGStepSchedule = "schedule"
)

// nightBedScheduleNodeID is the sentinel "node" [nightBGStepSchedule] rows
// are recorded under: a bed-level decision, not any one real audio.node's
// own step, but recorded under nightPhaseRestingBackgroundNode's own
// per-node phase shape (so it shares this session's single revision
// counter and history read - see that constant's own doc comment) rather
// than a second phase family. Chosen to be unmistakably not a real node
// id (audio.node ids come from operator-authored config objects, never
// containing this shape) so a coincidental real id can never collide with
// it.
const nightBedScheduleNodeID = "__bed-schedule__"

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
func nightBackgroundAudioCueNameSchedule(seq int) string { return fmt.Sprintf("bg-%04d-schedule", seq) }

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
	case strings.HasSuffix(row.CueName, "-schedule"):
		return nightBackgroundAudioStep{Seq: seq, Kind: nightBGStepSchedule}, nodeID, true
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

// nightBackgroundAudioDispatchedNodeIDs returns every node id this
// controller has ever committed a background-audio step for, read from
// history alone - the coordinator's own record of what it actually
// dispatched. Every stop path in this file (nightStopBackgroundAudioIfRunning,
// nightClearBackgroundAudioAtEndSession, nightRetryEndSessionClear) derives
// its node list from this, never from ba.OutputNodeIDs(): a referenced
// media.playlist can be tombstoned, or edited to fewer targets, while a
// night is running, and the stop path must still reach every node it
// actually put audio on, not only the ones the playlist still names.
// [nightBedScheduleNodeID]'s own schedule rows are never a real node and
// are excluded: nothing is ever dispatchable to that sentinel.
// Deduplicated, in history's own first-appearance order.
func nightBackgroundAudioDispatchedNodeIDs(history []nightBackgroundAudioHistoryRow) []string {
	seen := make(map[string]bool, len(history))
	out := make([]string, 0, len(history))
	for _, row := range nightBackgroundAudioSteps(history) {
		if row.NodeID == nightBedScheduleNodeID || seen[row.NodeID] {
			continue
		}
		seen[row.NodeID] = true
		out = append(out, row.NodeID)
	}
	return out
}

// nightBackgroundAudioLatestFadeDownDispatchedAt returns nodeID's own most
// recent fadedown step's DispatchedAt from already-read history, or the
// zero time.Time when no fadedown has ever been dispatched for this node.
// The one anchor every fade-completion guard in this file shares: a check
// anchored to anything else (a session's own StateEnteredAt, or evidence
// with no floor on its own age) can be satisfied by elapsed time that has
// nothing to do with the fade actually in flight.
func nightBackgroundAudioLatestFadeDownDispatchedAt(history []nightBackgroundAudioHistoryRow, nodeID string) time.Time {
	var latest time.Time
	for _, row := range nightBackgroundAudioStepsForNode(history, nodeID) {
		if row.Step.Kind != nightBGStepFadeDown || row.Row.DispatchedAt == nil {
			continue
		}
		latest = *row.Row.DispatchedAt
	}
	return latest
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
		// nil fade: a background-audio step has no cue definition to read one from.
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cueName, target, idemKey, issuer, revision, nil)
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
		// nil fade: a background-audio step has no cue definition to read one from.
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cueName, target, idemKey, issuer, revision, nil)
	default:
		return store.NightCueOutboxRecord{}, err
	}
}

// nightBackgroundAudioOwnerKindSession is audio.session.apply's own
// ownerKind for the inline form - unchanged wire value from before the
// reference form existed.
const nightBackgroundAudioOwnerKindSession = "night.session.resting.backgroundAudio"

// nightBackgroundAudioOwner is audio.session.apply's playlist owner triple
// (ownerKind/ownerId/ownerRevision): the night.session itself for the
// inline form, or the referenced media.playlist object for the reference
// form, so editing that object alone changes what the next apply pins
// without writing the session again.
type nightBackgroundAudioOwner struct {
	Kind     string
	ID       string
	Revision int64
}

// nightResolveMediaPlaylist reads id's current media.playlist revision and
// decodes it, mirroring h.nightSessionMediaPlaylistCurrent's own tombstone
// rule (nightsession.go): CurrentRevision == 0, or any store/decode
// failure, is answered as ok == false, never a distinguished error - the
// same "no store access reads as unavailable" posture this package uses
// throughout (nightSessionAssetCurrent's own doc comment, showobjects.go's
// showExists).
func nightResolveMediaPlaylist(ctx context.Context, deps Dependencies, id string) (config.MediaPlaylistPayload, int64, bool) {
	obj, err := deps.Config.GetConfigObject(ctx, config.MediaPlaylistConfigKind, id)
	if err != nil || obj.CurrentRevision == 0 {
		return config.MediaPlaylistPayload{}, 0, false
	}
	rev, err := deps.Config.GetConfigRevision(ctx, config.MediaPlaylistConfigKind, id, obj.CurrentRevision)
	if err != nil {
		return config.MediaPlaylistPayload{}, 0, false
	}
	var payload config.MediaPlaylistPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return config.MediaPlaylistPayload{}, 0, false
	}
	return payload, int64(obj.CurrentRevision), true
}

// nightMediaPlaylistBackgroundAudio converts a media.playlist object's own
// current revision into config.NightSessionBackgroundAudio's shape - the
// same items/repeat/resume/itemTransition/gain/fade fields the inline form
// already produces, so every reader downstream of resting.backgroundAudio
// needs no reference-vs-inline branch of its own. Item ids are synthesized
// (mediaPlaylistID-index): media.playlist items carry no itemId of their
// own (mediaplaylist.go), and this file's own itemId is otherwise only a
// wire/debug label, never an identity a lookup keys on.
// targets is the referencing session's own resting.backgroundAudio.Targets
// (ADR-049 decision 7): a media.playlist object carries no targets field of
// its own (mediaplaylist.go), so the reference form's Targets always lives
// on the OUTER wrapper the session itself pins, and must be carried through
// here explicitly or every reference-form bed would silently fall back to
// OutputNodeIDs/ItemsForTarget's per-item routing regardless of what the
// session actually declared.
func nightMediaPlaylistBackgroundAudio(mediaPlaylistID string, payload config.MediaPlaylistPayload, targets, excludeNodes []string) *config.NightSessionBackgroundAudio {
	items := make([]config.NightSessionBackgroundAudioItem, 0, len(payload.Items))
	for i, it := range payload.Items {
		items = append(items, config.NightSessionBackgroundAudioItem{
			ItemID: fmt.Sprintf("%s-%d", mediaPlaylistID, i), Asset: it.Asset,
		})
	}
	return &config.NightSessionBackgroundAudio{
		Items: items, Repeat: payload.Repeat, Resume: payload.Resume, ItemTransition: payload.ItemTransition,
		CrossfadeMs: payload.CrossfadeMs, MaxGainDb: payload.MaxGainDb,
		FadeOutMs: payload.FadeOutMs, FadeInMs: payload.FadeInMs,
		Targets: targets, ExcludeNodes: excludeNodes,
	}
}

// nightResolveBackgroundAudio resolves ba into its dispatch shape and
// owner: the session's own inline block unchanged (ok always true), or -
// when ba names a media.playlist - that object's current revision
// resolved via [nightMediaPlaylistBackgroundAudio]. ok is false only for a
// reference naming a missing or tombstoned playlist; every caller treats
// that the same way a dangling reference is treated everywhere else in
// this controller - warned and left for an operator, never dispatched.
func (h *handlers) nightResolveBackgroundAudio(ctx context.Context, rec store.NightSessionRecord, ba *config.NightSessionBackgroundAudio) (*config.NightSessionBackgroundAudio, nightBackgroundAudioOwner, bool) {
	if ba.MediaPlaylist == "" {
		return ba, nightBackgroundAudioOwner{Kind: nightBackgroundAudioOwnerKindSession, ID: rec.ConfigObjectID, Revision: rec.ConfigRevision}, true
	}
	payload, revision, ok := nightResolveMediaPlaylist(ctx, h.deps, ba.MediaPlaylist)
	if !ok {
		return nil, nightBackgroundAudioOwner{}, false
	}
	return nightMediaPlaylistBackgroundAudio(ba.MediaPlaylist, payload, ba.Targets, ba.ExcludeNodes), nightBackgroundAudioOwner{Kind: config.MediaPlaylistConfigKind, ID: ba.MediaPlaylist, Revision: revision}, true
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

// audioSessionStateSignalID mirrors nodeaudio.SignalSessionState's own
// wire value, for the same reason [audioSessionFadeStateSignalID] mirrors
// SignalSessionFadeState one constant up: this package must never import
// the collector package that produces it.
const audioSessionStateSignalID observation.SignalID = "audio_session.state"

// nightBackgroundAudioReportedSessionState reads nodeID's own most recent
// CURRENT report of sessionID's [pkgaudio.State] (the node's own claim,
// never this coordinator's own idea of what it should be). ok is false
// for no observation at all, a stale one, or one that predates notBefore -
// the same three "cannot yet trust this evidence" cases
// [nightBackgroundAudioFadeSettled] already guards for its own signal.
// Pass the zero time.Time for notBefore when there is no specific dispatch
// instant to fence the read against.
func nightBackgroundAudioReportedSessionState(audio NodeAudioLister, now, notBefore time.Time, nodeID, sessionID string) (state string, ok bool) {
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Signal != audioSessionStateSignalID || o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		if o.StateAt(now) != observation.StateCurrent {
			return "", false
		}
		if o.CollectedAt.Before(notBefore) {
			return "", false
		}
		state, _ = o.Value.(string)
		return state, state != ""
	}
	return "", false
}

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
//
// The node's own reported session state is checked first and is
// authoritative over the fade-state signal: once the node reports
// anything but Playing, the session has already left the state a fade
// ramps within, whatever the fade-state signal itself still claims -
// a session a node-side cut (or an operator stop/pause) paused mid-ramp
// can leave that signal latched at "in_progress" forever, since nothing
// ever drives the ramp to its own natural completion from there. A
// paused, stopped, or otherwise no-longer-playing session is settled by
// definition: there is no live ramp left to wait for.
//
// notBefore fences the evidence itself, matching resolveConfirmationEvidence's
// own ADR-003 fence (fppcommand_evidence.go): the node audio collector
// (internal/coordinator/collector/nodeaudio) holds only a node's single
// most recent report, so an observation collected before this fade's own
// dispatch instant predates the fade and can never report it settled, no
// matter what value it carries. Pass the zero time.Time to disable the
// fence for a caller with no dispatch instant to fence against.
func nightBackgroundAudioFadeSettled(audio NodeAudioLister, now, notBefore time.Time, nodeID, sessionID string) bool {
	if state, ok := nightBackgroundAudioReportedSessionState(audio, now, notBefore, nodeID, sessionID); ok && state != string(pkgaudio.StatePlaying) {
		return true
	}
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Signal != audioSessionFadeStateSignalID || o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		if o.StateAt(now) != observation.StateCurrent {
			return false
		}
		if o.CollectedAt.Before(notBefore) {
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

// audioPlaybackCeilingCapabilityID mirrors the literal capability ID
// internal/agent/audiocapabilities.go advertises for the standing-ceiling
// ability, matching audionode.go's own audioOutputLocalCapabilityID/
// audioOutputLTCCapabilityID convention: pkg/capability's vocabulary is
// untyped strings by design, so there is no shared constant to import.
const audioPlaybackCeilingCapabilityID capability.ID = "audio.playback.ceiling"

// audioNodeConfirmsCeiling reads nodeID's live capability advertisement
// and reports whether it declares audioPlaybackCeilingCapabilityID,
// mirroring audioNodeConfirmsTransition's (nightaudioreadiness.go) own
// evidence-first pattern: a node this coordinator cannot currently
// confirm anything about (never published, not currently online, or a
// lookup failure) never gets a wire ceiling manufactured for it. Sending
// ceiling unconditionally to a node built before this capability existed
// would fail the WHOLE apply at that agent's own rejectUnknownKeys, not
// merely leave the ceiling unenforced, so omission here is not a
// courtesy, it is what keeps a deployed older agent's background bed
// starting at all.
func audioNodeConfirmsCeiling(ctx context.Context, nodes NodeLister, now time.Time, nodeID string) bool {
	evidence, err := audioNodeCapabilitySet(ctx, nodes, now, nodeID)
	if err != nil || !evidence.Live {
		return false
	}
	_, confirmed := evidence.Capabilities.Lookup(audioPlaybackCeilingCapabilityID)
	return confirmed
}

// nightBackgroundApplyParams builds audio.session.apply's own wire
// params: a full pkgaudio.PlaylistRef pinning every configured item,
// repeat, resume, and requestedTransition on the node - the fields
// internal/agent/audiosessionops.go's parsePlaylistRef accepts, spelled
// exactly as it requires (ownerKind, ownerId, ownerRevision, items,
// repeat, resume, requestedTransition; each item itemId/index/assetId/
// contentHash/filename/sizeBytes). ceiling, when sent at all, carries
// resting.backgroundAudio.maxGainDb, already converted to the linear
// pkgaudio.Ceiling this controller sends every gain against
// ([nightBackgroundCeilingGain]): this is a server-built target, not an
// operator's own HTTP request, so it is sent linear rather than as
// ceilingDb, matching audio.gain.set's own gain field one call below.
// The key is present only when nodeID's live advertisement confirms
// audioPlaybackCeilingCapabilityID ([audioNodeConfirmsCeiling]); it is
// omitted entirely, not sent as zero or null, for every other node,
// including one this coordinator cannot currently confirm anything
// about, so a deployed agent from before this field existed still
// accepts this apply exactly as it always has. owner is passed in rather
// than derived here, resolved by [handlers.nightResolveBackgroundAudio]:
// the session itself for the inline form, or the referenced media.playlist
// object for the reference form.
func nightBackgroundApplyParams(ctx context.Context, nodes NodeLister, now time.Time, nodeID string, owner nightBackgroundAudioOwner, ba *config.NightSessionBackgroundAudio, items []pkgaudio.PlaylistItem) map[string]any {
	wireItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		wireItems = append(wireItems, map[string]any{
			"itemId": item.ItemID, "index": item.Index,
			"assetId": item.Media.AssetID, "contentHash": item.Media.ContentHash,
			"filename": item.Media.RuntimeFilename, "sizeBytes": item.Media.SizeBytes,
		})
	}
	params := map[string]any{
		"sourceRole": string(pkgaudio.SourceRoleBackground),
		"playlist": map[string]any{
			"ownerKind": owner.Kind, "ownerId": owner.ID,
			"ownerRevision": owner.Revision, "items": wireItems,
			"repeat": ba.Repeat, "resume": ba.Resume, "requestedTransition": ba.ItemTransition,
		},
		"mixPolicy":   string(pkgaudio.MixPolicyMix),
		"expiresInMs": float64(nightBackgroundAudioExpiryTTL.Milliseconds()),
	}
	if audioNodeConfirmsCeiling(ctx, nodes, now, nodeID) {
		_, ceiling := nightBackgroundCeilingGain(ba.MaxGainDb)
		params["ceiling"] = float64(ceiling)
	}
	return params
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
	resolved, owner, ok := h.nightResolveBackgroundAudio(ctx, rec, ba)
	if !ok {
		h.logWarn("night loop: background audio: referenced media.playlist is missing or tombstoned; not advancing", "sessionId", rec.ID, "mediaPlaylist", ba.MediaPlaylist)
		return
	}
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history", "sessionId", rec.ID, "error", err)
		return
	}
	showAudioNodes := h.showAudioNodes(ctx)(payload.Show)
	if nightBackgroundAudioIsMultiNode(resolved, showAudioNodes) {
		h.nightAdvanceMultiNodeBackgroundAudio(ctx, now, rec, payload.Show, resolved, owner, history)
		return
	}
	resolvedNodeIDs, _ := resolved.ResolvedPlaybackNodeIDs(showAudioNodes)
	h.nightDispatchBedNodesConcurrently(resolvedNodeIDs, func(nodeID string) {
		h.nightAdvanceBackgroundAudioForNode(ctx, now, rec, payload.Show, nodeID, resolved, owner, history)
	})
}

// nightBackgroundAudioIsMultiNode reports whether ba resolves (ADR-049
// decision 10: Targets, else showAudioNodes minus ExcludeNodes, else the
// per-item default) to more than one node from an explicit or show-wide
// list: decisions 8 and 9's own shared-instant start/resume and bookmark
// push apply only to this case. A bed falling through to the per-item
// default, or resolving to a single node, behaves exactly as it always
// has (ADR-049's own regression rule) - see
// [nightAdvanceBackgroundAudioForNode]'s own gates for the two transitions
// this changes.
func nightBackgroundAudioIsMultiNode(ba *config.NightSessionBackgroundAudio, showAudioNodes []string) bool {
	resolved, resolvedFrom := ba.ResolvedPlaybackNodeIDs(showAudioNodes)
	return resolvedFrom != config.AudioNodeResolutionDefault && len(resolved) > 1
}

// nightAdvanceBackgroundAudioForNode is [nightAdvanceBackgroundAudio]'s
// own per-node body, unchanged in shape from the single-node state
// machine this coordinator has always run - a single-target installation
// (one node in ba.OutputNodeIDs()) behaves exactly as it always has. ba is
// already resolved (never carries a MediaPlaylist reference); owner is
// its paired dispatch owner triple.
func (h *handlers) nightAdvanceBackgroundAudioForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, history []nightBackgroundAudioHistoryRow) {
	sessionID := nightBackgroundAudioSessionID(rec)
	showAudioNodes := h.showAudioNodes(ctx)(show)

	confirms, _, err := audioNodeConfirmsTransition(ctx, h.deps.Nodes, now, nodeID, pkgaudio.ItemTransition(ba.ItemTransition))
	if err != nil {
		h.logWarn("night loop: background audio: failed to read output node's capability advertisement", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
		return
	}
	if err := pkgaudio.ValidateItemTransitionSupport(pkgaudio.ItemTransition(ba.ItemTransition), confirms); err != nil {
		h.logWarn("night loop: background audio: requested item transition is not confirmed by the output; refusing to start", "sessionId", rec.ID, "nodeId", nodeID, "itemTransition", ba.ItemTransition, "error", err)
		return
	}

	items, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.ResolvedPlaybackItemsFor(nodeID, showAudioNodes))
	if err != nil {
		h.logWarn("night loop: background audio: failed to resolve playlist items", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
		return
	}
	if len(items) == 0 {
		return
	}

	multiNode := nightBackgroundAudioIsMultiNode(ba, showAudioNodes)

	steps := nightBackgroundAudioStepsForNode(history, nodeID)
	if len(steps) == 0 {
		h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, owner, items, history)
		return
	}
	latest := steps[len(steps)-1]

	if latest.Row.State == nightCueStatePending || latest.Row.State == nightCueStateDispatched {
		if multiNode && (latest.Step.Kind == nightBGStepStart || latest.Step.Kind == nightBGStepResume) {
			// The bed-level orchestration ([nightAdvanceMultiNodeBackgroundAudio])
			// owns retrying these two kinds for a multi-node bed: it alone
			// holds the durable schedule/bookmark this node's own retry must
			// reuse rather than re-deciding, so this per-node pass leaves it
			// alone rather than resuming it through the generic, schedule-
			// unaware path below.
			return
		}
		h.nightResumeBackgroundStep(ctx, now, rec, nodeID, sessionID, ba, owner, items, latest, history)
		return
	}

	confirmed := latest.Row.Outcome == nightCueOutcomeConfirmed

	// The node's own reported session state is authoritative over this
	// node's own step history: a bed a node-side cut paused outside the
	// coordinator's own ledger (audio.Manager.CutBackgroundBed) can leave
	// this node's latest step stalled on something other than a confirmed
	// pause - a fadedown whose ramp the cut interrupted before it could
	// ever settle, in particular - with no later step this switch
	// recognizes ever getting committed. Reported Paused is resumed
	// unconditionally here, whatever step the ledger stalled on, so a
	// node the coordinator never finished suspending is never left
	// silently paused for the rest of the night. Left to the bed-level
	// gate for a multi-node bed ([nightAdvanceMultiNodeBackgroundAudio]):
	// it alone holds the shared resume instant more than one resuming
	// node must share.
	if !multiNode && latest.Step.Kind != nightBGStepPause && latest.Step.Kind != nightBGStepResume {
		if state, ok := nightBackgroundAudioReportedSessionState(h.deps.Audio, now, time.Time{}, nodeID, sessionID); ok && state == string(pkgaudio.StatePaused) {
			h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, ba, history)
			return
		}
	}

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
		if multiNode {
			// [nightAdvanceMultiNodeBackgroundAudio] dispatches the actual
			// start, carrying the bed's shared schedule, once every listed
			// node has reached this same point.
			return
		}
		h.nightBackgroundAudioStart(ctx, now, rec, nodeID, sessionID, history)

	case nightBGStepStart:
		if !confirmed {
			h.logBackgroundAudioDidNotConfirmOnce(rec, nodeID, nightBGStepStart, latest.Row)
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
		if multiNode {
			// [nightAdvanceMultiNodeBackgroundAudio] pushes the program+ltc
			// node's bookmark and dispatches the actual resume, carrying the
			// bed's shared schedule, once every listed node has paused.
			return
		}
		h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, ba, history)

	case nightBGStepResume:
		if !confirmed {
			if multiNode {
				// Like start: a resume that did not confirm keeps its row and
				// reason and is not re-sent every tick.
				h.logBackgroundAudioDidNotConfirmOnce(rec, nodeID, nightBGStepResume, latest.Row)
				return
			}
			h.nightBackgroundAudioResume(ctx, now, rec, nodeID, sessionID, ba, history) // retry under a fresh revision: never wedge here.
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
		h.nightBackgroundAudioApply(ctx, now, rec, nodeID, sessionID, ba, owner, items, history)
	}
}

// logBackgroundAudioDidNotConfirmOnce logs a background-audio start or
// resume step's "did not confirm; not auto-retrying" warning, once per
// (session, node, step kind, outbox row): row.ID names the specific
// attempt that failed to confirm, so a genuinely NEW attempt (a fresh
// outbox row) logs again, while a later tick that still finds the SAME
// unconfirmed row - which is every tick, since this controller
// deliberately does not retry a start or resume - does not. The retry
// policy is unchanged; only the log line stops repeating. The row itself,
// with its own reason, stays durably recorded in the night_cue_outbox
// history the Night screen already reads (mapNightBackgroundAudio).
func (h *handlers) logBackgroundAudioDidNotConfirmOnce(rec store.NightSessionRecord, nodeID, kind string, row store.NightCueOutboxRecord) {
	key := rec.ID + "|" + nodeID + "|" + kind
	if !h.nightBGConfirmFailures.shouldLog(key, []string{row.ID}) {
		return
	}
	h.logWarn("night loop: background audio: "+kind+" did not confirm; not auto-retrying",
		"sessionId", rec.ID, "nodeId", nodeID, "outcome", row.Outcome, "reason", row.OutcomeReason)
}

func (h *handlers) nightBackgroundAudioApply(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameApply(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.apply", nightBackgroundApplyParams(ctx, h.deps.Nodes, now, nodeID, owner, ba, items))
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

// nightBackgroundAudioResume dispatches audio.session.resume and, when it
// resolves confirmed within this SAME call, immediately chains into the
// fade-in half of the pair - never left for a separate tick to notice.
// audio.session.resume carries no gain of its own (pkg/agent's own
// Resume takes no such param): the node resumes at whatever gain the
// fade-down before the pause already pinned there (its own dispatch
// target, recorded the instant that fade was accepted, per
// [nightBackgroundAudioFadeDown]'s own doc comment - never the ramp's
// merely-in-progress value), so a resume with nothing chained after it is
// audibly silent, not just administratively incomplete, until something
// raises the gain again. A resume that does not resolve confirmed here
// falls back to the ordinary per-tick advance ([nightAdvanceBackgroundAudioForNode]'s
// own case nightBGStepResume), unchanged.
func (h *handlers) nightBackgroundAudioResume(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow) {
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameResume(int(revision))
	target := nightAudioTarget(nodeID, sessionID, "audio.session.resume", map[string]any{})
	row, err := h.nightRunAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, target, revision, history)
	if err != nil {
		h.logWarn("night loop: background audio: resume failed", "sessionId", rec.ID, "error", err)
		return
	}
	if ba.FadeInMs == nil || row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeConfirmed {
		return
	}
	extended := append(append([]nightBackgroundAudioHistoryRow{}, history...), nightBackgroundAudioHistoryRow{
		Step: nightBackgroundAudioStep{Seq: int(revision), Kind: nightBGStepResume}, Row: row, NodeID: nodeID, Parsed: true,
	})
	h.nightBackgroundAudioFadeUp(ctx, now, rec, nodeID, sessionID, ba.MaxGainDb, *ba.FadeInMs, extended)
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
func (h *handlers) nightResumeBackgroundStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, latest nightBackgroundAudioHistoryRow, history []nightBackgroundAudioHistoryRow) {
	revision := latest.Row.ActionRevision
	var target config.ShowActionTarget
	switch latest.Step.Kind {
	case nightBGStepApply:
		target = nightAudioTarget(nodeID, sessionID, "audio.session.apply", nightBackgroundApplyParams(ctx, h.deps.Nodes, now, nodeID, owner, ba, items))
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
	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to read history for stop", "sessionId", rec.ID, "error", err)
		return
	}
	nodeIDs := nightBackgroundAudioDispatchedNodeIDs(history)
	if len(nodeIDs) == 0 {
		return
	}
	resolved, owner, ok := h.nightResolveBackgroundAudio(ctx, rec, payload.Resting.BackgroundAudio)
	if !ok {
		// No playlist left to read Resume/FadeOutMs/items from: fall back to
		// the zero-value shape, which nightBackgroundSuspendKind resolves to
		// an immediate stop (never a pause promising a resume this session
		// can no longer describe) and skips any fade this controller no
		// longer knows the duration of. owner stays zero-value too; it is
		// only read again if a stuck in-flight apply needs resending, which
		// a missing playlist cannot correctly do regardless.
		h.logWarn("night loop: background audio: referenced media.playlist is missing or tombstoned; stopping from dispatch history instead", "sessionId", rec.ID, "mediaPlaylist", payload.Resting.BackgroundAudio.MediaPlaylist)
		resolved, owner = &config.NightSessionBackgroundAudio{}, nightBackgroundAudioOwner{}
	}
	h.nightDispatchBedNodesConcurrently(nodeIDs, func(nodeID string) {
		h.nightStopBackgroundAudioIfRunningForNode(ctx, now, rec, payload.Show, nodeID, resolved, owner, history)
	})
}

// nightStopBackgroundAudioIfRunningForNode's ba is already resolved (never
// carries a MediaPlaylist reference); owner is its paired dispatch owner
// triple, needed only for the in-flight-apply resume case.
func (h *handlers) nightStopBackgroundAudioIfRunningForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, history []nightBackgroundAudioHistoryRow) {
	sessionID := nightBackgroundAudioSessionID(rec)
	showAudioNodes := h.showAudioNodes(ctx)(show)
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
		if nightBackgroundAudioIsMultiNode(ba, showAudioNodes) && latest.Step.Kind == nightBGStepPause {
			// A multi-node bed's own in-flight pause is retried by
			// re-attempting [nightBackgroundAudioSuspend] below under the
			// SAME already-committed identity (nightRunBedAudioCommand's own
			// resume-in-flight branch), not through the generic,
			// evidence-blind nightResumeBackgroundStep path: see
			// nightBackgroundAudioSuspend's own doc comment for why only
			// this dispatcher may capture the bookmark decision 8's resume
			// push needs.
			h.nightBackgroundAudioSuspend(ctx, now, rec, show, nodeID, sessionID, ba, history)
			return
		}
		items, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.ResolvedPlaybackItemsFor(nodeID, showAudioNodes))
		if err != nil {
			return
		}
		h.nightResumeBackgroundStep(ctx, now, rec, nodeID, sessionID, ba, owner, items, latest, history)
		return
	}

	if ba.FadeOutMs == nil {
		h.nightBackgroundAudioSuspend(ctx, now, rec, show, nodeID, sessionID, ba, history)
		return
	}

	confirmed := latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed
	if latest.Step.Kind == nightBGStepFadeDown && confirmed {
		// latest.Row.DispatchedAt is always set once a row reaches
		// resolved (nightDispatchAndPersistCue marks it before ever
		// dispatching), so this fences out any evidence the collector
		// held from before THIS fade was sent.
		fadeDispatchedAt := time.Time{}
		if latest.Row.DispatchedAt != nil {
			fadeDispatchedAt = *latest.Row.DispatchedAt
		}
		if !nightBackgroundAudioFadeSettled(h.deps.Audio, now, fadeDispatchedAt, nodeID, sessionID) {
			return // the ramp is still running; never let pause/stop race it (see nightBackgroundAudioFadeSettled's own doc comment).
		}
		h.nightBackgroundAudioSuspend(ctx, now, rec, show, nodeID, sessionID, ba, history)
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
	history, herr := h.nightBackgroundAudioHistory(ctx, rec)
	if herr != nil {
		h.logWarn("night loop: end-session: failed to read background-audio history; background audio session was not cleared", "sessionId", rec.ID, "error", herr)
		return
	}
	nodeIDs := nightBackgroundAudioDispatchedNodeIDs(history)
	if len(nodeIDs) == 0 {
		return
	}
	// fadeOutMs is best-effort: a tombstoned or edited-away media.playlist
	// leaves no fade config to honor, so the clear below proceeds without
	// waiting on a ramp this controller can no longer describe, rather than
	// leaving the bed running because its own owning playlist is gone.
	var fadeOutMs *int
	if resolved, _, ok := h.nightResolveBackgroundAudio(ctx, rec, ba); ok {
		fadeOutMs = resolved.FadeOutMs
	} else {
		h.logWarn("night loop: end-session: referenced media.playlist is missing or tombstoned; clearing from dispatch history instead", "sessionId", rec.ID, "mediaPlaylist", ba.MediaPlaylist)
	}
	for _, nodeID := range nodeIDs {
		fadeDispatchedAt := nightBackgroundAudioLatestFadeDownDispatchedAt(history, nodeID)
		h.nightClearBackgroundAudioAtEndSessionForNode(ctx, now, fadeDispatchedAt, nodeID, sessionID, fadeOutMs)
	}
}

// nightEndSessionClearFadeGuardMargin is how much longer than the
// configured fade-out [nightEndSessionClearMayProceed] waits past the
// fade's own dispatch instant before forcing the clear regardless of fade
// state: one night-loop tick's worth of slack (matching
// defaultNightLoopInterval) for the fade-settled observation to catch up.
const nightEndSessionClearFadeGuardMargin = 1 * time.Second

// nightEndSessionClearMayProceed reports whether an end-session clear may
// dispatch now. No fadeOutMs configured means yes immediately, unchanged
// from today. Otherwise it waits for [nightBackgroundAudioFadeSettled],
// fenced to fadeDispatchedAt, but never past
// fadeDispatchedAt+fadeOutMs+margin: a stuck or silent node (fadeSettled
// returns false forever with no CURRENT observation at all) must never
// hold end-session open indefinitely. fadeDispatchedAt is the SAME anchor
// both halves of this guard share - see
// [nightBackgroundAudioLatestFadeDownDispatchedAt]'s own doc comment for
// why anchoring the timeout to anything else (this session's own
// StateEnteredAt, in particular) lets elapsed time that has nothing to do
// with the fade satisfy it. No fadedown has ever been dispatched
// (fadeDispatchedAt zero) means there is nothing in flight to wait for at
// all, so this proceeds immediately, same as fadeOutMs == nil.
func (h *handlers) nightEndSessionClearMayProceed(now, fadeDispatchedAt time.Time, nodeID, sessionID string, fadeOutMs *int) bool {
	if fadeOutMs == nil {
		return true
	}
	if nightBackgroundAudioFadeSettled(h.deps.Audio, now, fadeDispatchedAt, nodeID, sessionID) {
		return true
	}
	if fadeDispatchedAt.IsZero() {
		return true
	}
	bound := time.Duration(*fadeOutMs)*time.Millisecond + nightEndSessionClearFadeGuardMargin
	return now.Sub(fadeDispatchedAt) >= bound
}

// nightClearBackgroundAudioAtEndSessionForNode is
// [handlers.nightClearBackgroundAudioAtEndSession]'s own per-node body: one
// node's own clear attempt, warned and abandoned independently of every
// other node's own outcome. Guarded by
// [handlers.nightEndSessionClearMayProceed] so a fade-down still ramping
// from the ordinary resting-exit path is never cut off by this clear;
// when the guard holds, [handlers.nightRetryEndSessionClear]'s own
// per-tick retry (below) is what eventually dispatches it.
func (h *handlers) nightClearBackgroundAudioAtEndSessionForNode(ctx context.Context, now, fadeDispatchedAt time.Time, nodeID, sessionID string, fadeOutMs *int) {
	if !h.nightEndSessionClearMayProceed(now, fadeDispatchedAt, nodeID, sessionID, fadeOutMs) {
		return
	}
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
	sessionID := nightBackgroundAudioSessionID(rec)
	history, herr := h.nightBackgroundAudioHistory(ctx, rec)
	if herr != nil {
		h.logWarn("night loop: end-session clear retry: failed to read background-audio history", "sessionId", rec.ID, "error", herr)
		return
	}
	nodeIDs := nightBackgroundAudioDispatchedNodeIDs(history)
	if len(nodeIDs) == 0 {
		return
	}
	var fadeOutMs *int
	if resolved, _, ok := h.nightResolveBackgroundAudio(ctx, rec, ba); ok {
		fadeOutMs = resolved.FadeOutMs
	} else {
		h.logWarn("night loop: end-session clear retry: referenced media.playlist is missing or tombstoned; retrying from dispatch history instead", "sessionId", rec.ID, "mediaPlaylist", ba.MediaPlaylist)
	}
	// KNOWN GAP, flagged for an owner decision: this retry safety net
	// tracks its confirmation via ONE anchor slot on the record
	// (ContentAnchorJSON), which can only represent one node's own
	// retry state. A bed dispatched onto more than one node only gets
	// this crash-recovery retry for its first dispatched node
	// (nodeIDs[0]); nightClearBackgroundAudioAtEndSession's own
	// synchronous warn-and-proceed attempt above still reaches every
	// node once, so only a node whose SYNCHRONOUS attempt also failed
	// is left unrecovered by this tick-based safety net.
	fadeDispatchedAt := nightBackgroundAudioLatestFadeDownDispatchedAt(history, nodeIDs[0])
	if !h.nightEndSessionClearMayProceed(now, fadeDispatchedAt, nodeIDs[0], sessionID, fadeOutMs) {
		return // fade still ramping and within bound; retried again next tick.
	}
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

// ADR-049 decisions 7-9: a multi-node bed's shared start/resume instant
// (decision 8, mirroring decision 3's identical rule for a Cue,
// cueactivationschedule.go) and the program+ltc node's own bookmark push
// before a scheduled resume. Only reached for a bed whose declared Targets
// (decision 7) name more than one node ([nightBackgroundAudioIsMultiNode]);
// every other bed keeps the per-node state machine above completely
// unchanged, including a bed whose distinct per-item Asset.Target values
// happen to span several nodes without ever declaring Targets (ADR-049's
// own regression rule - decisions 8 and 9 do not apply to it).
//
// Unlike the Cue path (cueactivationschedule.go), this never dispatches a
// throwaway probe session: it reads the clock from the bed's own real
// audio.session.prepare, on the program+ltc node alone, so one schedule
// decision costs one real network round trip, never one per node.

// nightBedReadyStepBound bounds how long the shared gate waits, once one
// node is ready, before proceeding without the stragglers. Distinct from
// scheduleProbeStepTimeout (cueactivationschedule.go).
const nightBedReadyStepBound = 5 * time.Second

// nightAdvanceMultiNodeBackgroundAudio is [nightAdvanceBackgroundAudio]'s
// own multi-node body: every node's per-node machine advances first, then
// the bed-level gate drives the shared schedule and dispatch.
func (h *handlers) nightAdvanceMultiNodeBackgroundAudio(ctx context.Context, now time.Time, rec store.NightSessionRecord, show string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, history []nightBackgroundAudioHistoryRow) {
	nodeIDs, _ := ba.ResolvedPlaybackNodeIDs(h.showAudioNodes(ctx)(show))
	h.nightDispatchBedNodesConcurrently(nodeIDs, func(nodeID string) {
		h.nightAdvanceBackgroundAudioForNode(ctx, now, rec, show, nodeID, ba, owner, history)
	})

	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: background audio: failed to re-read history for bed-level scheduling", "sessionId", rec.ID, "error", err)
		return
	}

	sessionID := nightBackgroundAudioSessionID(rec)
	switch {
	case nightBedStartPending(history, nodeIDs):
		h.nightStartMultiNodeBackgroundAudio(ctx, now, rec, show, ba, history, nodeIDs)
	case h.nightBedResumePending(now, history, nodeIDs, sessionID):
		h.nightResumeMultiNodeBackgroundAudio(ctx, now, rec, show, ba, history, nodeIDs)
	}
}

// nightBackgroundAudioLatestStepForNode is [nightBackgroundAudioStepsForNode]
// narrowed to nodeID's own single most recent step, or false when nodeID
// has never recorded one.
func nightBackgroundAudioLatestStepForNode(history []nightBackgroundAudioHistoryRow, nodeID string) (nightBackgroundAudioHistoryRow, bool) {
	steps := nightBackgroundAudioStepsForNode(history, nodeID)
	if len(steps) == 0 {
		return nightBackgroundAudioHistoryRow{}, false
	}
	return steps[len(steps)-1], true
}

// nightBedGateState is one listed node's own position against a shared
// start or resume gate.
type nightBedGateState int

const (
	nightBedGateNotReady nightBedGateState = iota // not yet at gain/pause confirmed.
	nightBedGateReady                             // confirmed; not yet asked to start/resume.
	nightBedGateInFlight                          // asked; not yet resolved.
	nightBedGateDone                              // resolved start/resume, or a later step.
)

// nightBedClassifyStartGate is nodeID's own position against the shared
// start gate. Anything but the exact boundary kinds (gain, start)
// defaults to NotReady, which this gate never needs to act on.
func nightBedClassifyStartGate(history []nightBackgroundAudioHistoryRow, nodeID string) (nightBedGateState, nightBackgroundAudioHistoryRow) {
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok {
		return nightBedGateNotReady, nightBackgroundAudioHistoryRow{}
	}
	switch {
	case latest.Step.Kind == nightBGStepGain && latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed:
		return nightBedGateReady, latest
	case latest.Step.Kind == nightBGStepStart && latest.Row.State != nightCueStateResolved:
		return nightBedGateInFlight, latest
	case latest.Step.Kind == nightBGStepStart && latest.Row.State == nightCueStateResolved:
		return nightBedGateDone, latest
	default:
		return nightBedGateNotReady, latest
	}
}

// nightBedClassifyResumeGate is [nightBedClassifyStartGate]'s own mirror
// for the shared resume gate (pause confirmed / resume in flight).
func nightBedClassifyResumeGate(history []nightBackgroundAudioHistoryRow, nodeID string) (nightBedGateState, nightBackgroundAudioHistoryRow) {
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok {
		return nightBedGateNotReady, nightBackgroundAudioHistoryRow{}
	}
	switch {
	case latest.Step.Kind == nightBGStepPause && latest.Row.State == nightCueStateResolved && latest.Row.Outcome == nightCueOutcomeConfirmed:
		return nightBedGateReady, latest
	case latest.Step.Kind == nightBGStepResume && latest.Row.State != nightCueStateResolved:
		return nightBedGateInFlight, latest
	case latest.Step.Kind == nightBGStepResume && latest.Row.State == nightCueStateResolved:
		return nightBedGateDone, latest
	default:
		return nightBedGateNotReady, latest
	}
}

// nightBedGateClassifier is the shape [nightBedClassifyStartGate] and
// [nightBedClassifyResumeGate] share, so the functions below run once
// against either.
type nightBedGateClassifier func([]nightBackgroundAudioHistoryRow, string) (nightBedGateState, nightBackgroundAudioHistoryRow)

// nightBedGatePending reports whether at least one of nodeIDs is Ready or
// InFlight. A bed whose every node is still catching up or already done
// is left alone.
func nightBedGatePending(history []nightBackgroundAudioHistoryRow, nodeIDs []string, classify nightBedGateClassifier) bool {
	for _, nodeID := range nodeIDs {
		switch state, _ := classify(history, nodeID); state {
		case nightBedGateReady, nightBedGateInFlight:
			return true
		}
	}
	return false
}

// nightBedStartPending is [nightBedGatePending] for the shared start gate.
func nightBedStartPending(history []nightBackgroundAudioHistoryRow, nodeIDs []string) bool {
	return nightBedGatePending(history, nodeIDs, nightBedClassifyStartGate)
}

// nightBedResumeGateClassifier is [nightBedClassifyResumeGate] plus the
// node's own reported session state as a fallback: a node the ledger
// itself never recorded a confirmed pause for (a node-side cut raced the
// coordinator's own suspend before it could commit one) is still Ready
// once it reports [pkgaudio.StatePaused] - the same authority
// [nightBackgroundAudioFadeSettled] gives that report over its own
// fade-state signal. now and sessionID are bound once per call so every
// node checked in one bed-level pass reads the same evidence snapshot.
func (h *handlers) nightBedResumeGateClassifier(now time.Time, sessionID string) nightBedGateClassifier {
	return func(history []nightBackgroundAudioHistoryRow, nodeID string) (nightBedGateState, nightBackgroundAudioHistoryRow) {
		if state, latest := nightBedClassifyResumeGate(history, nodeID); state != nightBedGateNotReady {
			return state, latest
		}
		latest, _ := nightBackgroundAudioLatestStepForNode(history, nodeID)
		if reported, ok := nightBackgroundAudioReportedSessionState(h.deps.Audio, now, time.Time{}, nodeID, sessionID); ok && reported == string(pkgaudio.StatePaused) {
			return nightBedGateReady, latest
		}
		return nightBedGateNotReady, latest
	}
}

// nightBedResumePending is [nightBedGatePending] for the shared resume
// gate.
func (h *handlers) nightBedResumePending(now time.Time, history []nightBackgroundAudioHistoryRow, nodeIDs []string, sessionID string) bool {
	return nightBedGatePending(history, nodeIDs, h.nightBedResumeGateClassifier(now, sessionID))
}

// nightBedEvaluateGate classifies every listed node: which are ready to
// dispatch now, which are still catching up, and the ready ones' earliest
// ResolvedAt (durable on its own row, so the bound survives a restart).
func nightBedEvaluateGate(history []nightBackgroundAudioHistoryRow, nodeIDs []string, classify nightBedGateClassifier) (ready, notReady []string, firstReadyAt time.Time) {
	for _, nodeID := range nodeIDs {
		state, row := classify(history, nodeID)
		switch state {
		case nightBedGateReady:
			ready = append(ready, nodeID)
			if row.Row.ResolvedAt != nil && (firstReadyAt.IsZero() || row.Row.ResolvedAt.Before(firstReadyAt)) {
				firstReadyAt = *row.Row.ResolvedAt
			}
		case nightBedGateNotReady:
			notReady = append(notReady, nodeID)
		}
	}
	return ready, notReady, firstReadyAt
}

// nightBedContainsNode reports whether nodeID appears in nodeIDs.
func nightBedContainsNode(nodeIDs []string, nodeID string) bool {
	for _, id := range nodeIDs {
		if id == nodeID {
			return true
		}
	}
	return false
}

// nightBedScheduleResult is [nightGetOrComputeBedSchedule]'s own outcome,
// JSON-encoded into the [nightBGStepSchedule] row's OutcomeReason so a
// later tick, even across a restart, reuses it rather than reading again.
type nightBedScheduleResult struct {
	Aligned         bool   `json:"aligned"`
	ScheduledAtNs   int64  `json:"scheduledAtNs,omitempty"`
	ClockNodeID     string `json:"clockNodeId,omitempty"`
	UnalignedReason string `json:"unalignedReason,omitempty"`
}

func encodeNightBedScheduleResult(r nightBedScheduleResult) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func decodeNightBedScheduleResult(s string) (nightBedScheduleResult, bool) {
	if s == "" {
		return nightBedScheduleResult{}, false
	}
	var r nightBedScheduleResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nightBedScheduleResult{}, false
	}
	return r, true
}

// nightBedScheduleForCycle finds this cycle's own already-resolved
// [nightBGStepSchedule] row, if one exists, so a replay tick reads no
// clock. A cycle sees at most one of a first start or a resume.
func nightBedScheduleForCycle(history []nightBackgroundAudioHistoryRow, cycle int64) (nightBedScheduleResult, bool) {
	for _, row := range nightBackgroundAudioStepsForNode(history, nightBedScheduleNodeID) {
		if row.Step.Kind != nightBGStepSchedule || row.Row.Cycle != cycle || row.Row.State != nightCueStateResolved {
			continue
		}
		if r, ok := decodeNightBedScheduleResult(row.Row.OutcomeReason); ok {
			return r, true
		}
	}
	return nightBedScheduleResult{}, false
}

// nightBedNodeDispatchRevision is the next revision for a bed-level
// dispatch to nodeID: strictly greater than its persisted revision
// (re-read fresh) and this session's outbox counter.
func (h *handlers) nightBedNodeDispatchRevision(ctx context.Context, nodeID, sessionID string, history []nightBackgroundAudioHistoryRow) int64 {
	next := nightNextBackgroundAudioRevision(history)
	if persisted := h.nightAudioSessionPersistedRevision(ctx, nodeID, sessionID); persisted+1 > next {
		next = persisted + 1
	}
	return next
}

// nightGetOrComputeBedSchedule returns this cycle's schedule decision,
// recorded or freshly computed. The row commits PENDING before the clock
// read, never after, so a crash between the two is abandoned, not replayed.
// show and ba resolve the probe media [nightComputeBedSchedule] reads the
// clock against; bookmark, when known, is the item being resumed (pass
// the zero value on a start read).
func (h *handlers) nightGetOrComputeBedSchedule(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeIDs []string, history []nightBackgroundAudioHistoryRow, issuer FPPCommandIssuer, unreadyReason, show string, ba *config.NightSessionBackgroundAudio, bookmark nightBedBookmark) (nightBedScheduleResult, error) {
	if existing, ok := nightBedScheduleForCycle(history, rec.Cycle); ok {
		return existing, nil
	}

	phase := nightPhaseRestingBackgroundNode(nightBedScheduleNodeID)
	revision := nightNextBackgroundAudioRevision(history)
	cueName := nightBackgroundAudioCueNameSchedule(int(revision))
	if err := h.nightCommitCueRow(ctx, now, rec, phase, cueName, revision); err != nil && !errors.Is(err, store.ErrNightCueOutboxDuplicate) {
		return nightBedScheduleResult{}, err
	}
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if err != nil {
		return nightBedScheduleResult{}, err
	}
	if row.State == nightCueStateResolved {
		// A concurrent caller already resolved this exact row (the
		// duplicate-insert race above): its own recorded decision wins,
		// never this call's own freshly computed one, so every caller in
		// this tick agrees on one value.
		if existing, ok := decodeNightBedScheduleResult(row.OutcomeReason); ok {
			return existing, nil
		}
	}

	result := h.nightComputeBedSchedule(ctx, now, rec, nodeIDs, history, issuer, unreadyReason, show, ba, bookmark)

	row.State = nightCueStateResolved
	row.Outcome = nightCueOutcomeConfirmed
	row.OutcomeReason = encodeNightBedScheduleResult(result)
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
		return nightBedScheduleResult{}, err
	}
	return result, nil
}

// nightBedProbeMedia resolves the media [nightComputeBedSchedule] applies
// onto the clock holder's own probe session: the item being resumed, when
// bookmark names one found among nodeID's own configured items, otherwise
// nodeID's own first configured item. Never the bed's own real session -
// this is what lets the clock read leave a paused bed session paused.
func (h *handlers) nightBedProbeMedia(ctx context.Context, show string, ba *config.NightSessionBackgroundAudio, nodeID string, bookmark nightBedBookmark) (pkgaudio.MediaRef, error) {
	items, err := h.nightBuildBackgroundPlaylistItems(ctx, show, ba.ResolvedPlaybackItemsFor(nodeID, h.showAudioNodes(ctx)(show)))
	if err != nil {
		return pkgaudio.MediaRef{}, err
	}
	if len(items) == 0 {
		return pkgaudio.MediaRef{}, fmt.Errorf("no playlist items configured for node %q", nodeID)
	}
	if bookmark.Known {
		for _, item := range items {
			if item.ItemID == bookmark.ItemID {
				return item.Media, nil
			}
		}
	}
	return items[0].Media, nil
}

// nightComputeBedSchedule is the single schedule read: [readScheduleProbe]
// (cueactivationschedule.go) against the program+ltc node's own dedicated
// [cueactivation.ScheduleProbeSessionID], never the bed's own real
// session - preparing the bed session itself would release the agent's
// engine handle and end a just-confirmed pause (internal/agent/audio.
// Manager.Prepare's own doc comment), refusing every later resume. No
// usable clock, or a non-empty unreadyReason, falls back to unaligned.
func (h *handlers) nightComputeBedSchedule(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeIDs []string, history []nightBackgroundAudioHistoryRow, issuer FPPCommandIssuer, unreadyReason, show string, ba *config.NightSessionBackgroundAudio, bookmark nightBedBookmark) nightBedScheduleResult {
	if unreadyReason != "" {
		return nightBedScheduleResult{UnalignedReason: unreadyReason}
	}
	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		return nightBedScheduleResult{UnalignedReason: "could not read audio.settings to select a shared start instant: " + err.Error()}
	}
	probeIssuer := cueActivationIssuer{PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName, Form: issuer.Form, CredentialID: issuer.CredentialID}

	read := func(ctx context.Context, nodeID string) (AudioStartInstantReading, error) {
		holdsClock, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			return AudioStartInstantReading{}, err
		}
		if !holdsClock {
			// Only the clock holder's own reading feeds Select; every
			// other listed node is reported present but without evidence,
			// costing this schedule decision no extra network round trip.
			return AudioStartInstantReading{HoldsMediaClock: false}, nil
		}
		media, err := h.nightBedProbeMedia(ctx, show, ba, nodeID, bookmark)
		if err != nil {
			return AudioStartInstantReading{HoldsMediaClock: true}, err
		}
		evidence, elapsed, probeErr := h.readScheduleProbe(ctx, now, nodeID, media, probeIssuer)
		if probeErr != nil {
			return AudioStartInstantReading{HoldsMediaClock: true}, probeErr
		}
		return AudioStartInstantReading{HoldsMediaClock: true, Evidence: evidence, ProbeElapsed: elapsed}, nil
	}

	sel, nodeErrs, selErr := SelectAudioStartInstant(ctx, nodeIDs, settings, read)
	for _, ne := range nodeErrs {
		h.logWarn("night loop: background audio: bed schedule node read failed", "sessionId", rec.ID, "nodeId", ne.NodeID, "error", ne.Err)
	}
	if selErr != nil {
		return nightBedScheduleResult{UnalignedReason: audiosched.DescribeUnscheduled(selErr)}
	}
	return nightBedScheduleResult{Aligned: true, ScheduledAtNs: sel.ScheduledAtNs, ClockNodeID: sel.ClockNodeID}
}

// nightBedLateUnalignedReason is a late arrival's own reason: the shared
// round's instant, if any, is already past, so it starts or resumes on
// arrival rather than reusing a stale scheduledAtNs.
func nightBedLateUnalignedReason(verb string) string {
	return fmt.Sprintf("joined after the bed's shared %s window", verb)
}

// nightStartMultiNodeBackgroundAudio is the shared start gate: bounded
// waits, one node refusing never stops others. Once this cycle's schedule
// row exists, a newly-ready node is a late arrival and starts alone.
func (h *handlers) nightStartMultiNodeBackgroundAudio(ctx context.Context, now time.Time, rec store.NightSessionRecord, show string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow, nodeIDs []string) {
	sessionID := nightBackgroundAudioSessionID(rec)

	if _, already := nightBedScheduleForCycle(history, rec.Cycle); already {
		for _, nodeID := range nodeIDs {
			if state, _ := nightBedClassifyStartGate(history, nodeID); state == nightBedGateReady {
				h.nightBackgroundAudioStartScheduled(ctx, now, rec, nodeID, sessionID, nightBedScheduleResult{UnalignedReason: nightBedLateUnalignedReason("start")}, history)
			}
		}
		return
	}

	ready, notReady, firstReadyAt := nightBedEvaluateGate(history, nodeIDs, nightBedClassifyStartGate)
	if len(ready) == 0 {
		return
	}
	if len(notReady) > 0 && now.Sub(firstReadyAt) < nightBedReadyStepBound {
		return
	}

	unreadyReason, err := h.nightBedProgramLTCUnreadyReason(ctx, nodeIDs, ready)
	if err != nil {
		h.logWarn("night loop: background audio: bed start: failed to resolve the program+ltc node", "sessionId", rec.ID, "error", err)
		return
	}

	issuer := nightBackgroundAudioIssuer(rec)
	sched, err := h.nightGetOrComputeBedSchedule(ctx, now, rec, nodeIDs, history, issuer, unreadyReason, show, ba, nightBedBookmark{})
	if err != nil {
		h.logWarn("night loop: background audio: bed schedule failed", "sessionId", rec.ID, "error", err)
		return
	}
	h.nightDispatchBedNodesConcurrently(ready, func(nodeID string) {
		h.nightBackgroundAudioStartScheduled(ctx, now, rec, nodeID, sessionID, sched, history)
	})
}

// nightDispatchBedNodesConcurrently runs fn once per nodeID on its own
// goroutine and waits for all of them: a bed's shared start or resume
// instant must reach every ready node before that instant elapses, so
// one node's own dispatch (which blocks on its own agent's result) must
// never delay another node's own publish until after the shared instant
// has already passed - mirrors dispatchCueActivationsConcurrently's
// identical concurrency contract for cue.activate (ADR-049 decision 3).
// A panic on one node's own goroutine is logged and never reaches, or
// blocks, any other node's own dispatch.
func (h *handlers) nightDispatchBedNodesConcurrently(nodeIDs []string, fn func(nodeID string)) {
	var wg sync.WaitGroup
	for _, nodeID := range nodeIDs {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					h.logWarn("night loop: background audio: bed dispatch panicked", "nodeId", nodeID, "panic", r)
				}
			}()
			fn(nodeID)
		}(nodeID)
	}
	wg.Wait()
}

// nightBedProgramLTCUnreadyReason is non-empty when nodeIDs names a
// program+ltc node not itself in ready: no clock is read from a node that
// has not confirmed its own gain or pause by the bound.
func (h *handlers) nightBedProgramLTCUnreadyReason(ctx context.Context, nodeIDs, ready []string) (string, error) {
	programLTC, has, err := h.nightBedProgramLTCNode(ctx, nodeIDs)
	if err != nil {
		return "", err
	}
	if !has || nightBedContainsNode(ready, programLTC) {
		return "", nil
	}
	return "the program+ltc node is not ready by the bound", nil
}

// nightBackgroundAudioStartScheduled dispatches nodeID's own
// audio.session.start, carrying sched's instant when aligned. The
// ordinary per-node state machine takes over from here.
func (h *handlers) nightBackgroundAudioStartScheduled(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, sched nightBedScheduleResult, history []nightBackgroundAudioHistoryRow) {
	revision := h.nightBedNodeDispatchRevision(ctx, nodeID, sessionID, history)
	cueName := nightBackgroundAudioCueNameStart(int(revision))
	params := map[string]any{}
	note := nightBedScheduleNote("start", sched)
	if sched.Aligned {
		params[pkgaudio.ParamScheduledAtNs] = json.Number(fmt.Sprintf("%d", sched.ScheduledAtNs))
	}
	composeReason := func(reason string, _ map[string]any) string { return nightCueReasonWith(reason, note) }
	if _, _, err := h.nightRunBedAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, "audio.session.start", nodeID, sessionID, params, revision, history, composeReason); err != nil {
		h.logWarn("night loop: background audio: bed start failed", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
	}
}

// nightBedScheduleNote is the human-readable text appended to a start or
// resume row's own OutcomeReason, riding the existing
// [NightBackgroundAudioStep.Reason] field with no new API surface.
func nightBedScheduleNote(verb string, sched nightBedScheduleResult) string {
	if sched.Aligned {
		return fmt.Sprintf("bed-aligned %s at scheduledAtNs=%d (clock read from %q)", verb, sched.ScheduledAtNs, sched.ClockNodeID)
	}
	return fmt.Sprintf("bed %s unaligned: %s", verb, sched.UnalignedReason)
}

// nightBedBookmark is the program+ltc node's own decoded pause bookmark
// ([nightBedBookmarkFromEvidence]), carried on the following resume's own
// resumeItemId/Index/PositionMs params.
type nightBedBookmark struct {
	Known      bool   `json:"known"`
	ItemID     string `json:"itemId,omitempty"`
	Index      int    `json:"index,omitempty"`
	PositionMs int64  `json:"positionMs,omitempty"`
}

// nightBedBookmarkFromEvidence decodes audio.session.pause's own result-
// evidence keys ([pkgaudio.ResultBookmarkKnown] and siblings). Known is
// false for nothing bookmark-shaped, ResultBookmarkKnown false, or an
// empty item id - never inferred.
func nightBedBookmarkFromEvidence(evidence map[string]any) nightBedBookmark {
	if evidence == nil {
		return nightBedBookmark{}
	}
	known, _ := evidence[pkgaudio.ResultBookmarkKnown].(bool)
	if !known {
		return nightBedBookmark{}
	}
	itemID, _ := evidence[pkgaudio.ResultBookmarkItemID].(string)
	if itemID == "" {
		return nightBedBookmark{}
	}
	index, _ := evidenceInt64(evidence[pkgaudio.ResultBookmarkIndex])
	positionMs, _ := evidenceInt64(evidence[pkgaudio.ResultBookmarkPositionMs])
	return nightBedBookmark{Known: true, ItemID: itemID, Index: int(index), PositionMs: positionMs}
}

// nightBedBookmarkNotePrefix tags the JSON fragment a pause row's own
// OutcomeReason carries, so [nightBedNodeLatestPauseBookmark] can decode
// it back without a second, dedicated column.
const nightBedBookmarkNotePrefix = "bedBookmark="

func encodeNightBedBookmarkNote(bm nightBedBookmark) string {
	if !bm.Known {
		return ""
	}
	b, _ := json.Marshal(bm)
	return nightBedBookmarkNotePrefix + string(b)
}

func decodeNightBedBookmarkFromReason(reason string) (nightBedBookmark, bool) {
	idx := strings.Index(reason, nightBedBookmarkNotePrefix)
	if idx < 0 {
		return nightBedBookmark{}, false
	}
	var bm nightBedBookmark
	if err := json.Unmarshal([]byte(reason[idx+len(nightBedBookmarkNotePrefix):]), &bm); err != nil {
		return nightBedBookmark{}, false
	}
	return bm, bm.Known
}

// nightBedNodeLatestPauseBookmark reads nodeID's own most recent
// confirmed pause step's bookmark note, or the zero (unknown) value when
// it never paused or its pause never confirmed.
func nightBedNodeLatestPauseBookmark(history []nightBackgroundAudioHistoryRow, nodeID string) nightBedBookmark {
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok || latest.Step.Kind != nightBGStepPause || latest.Row.State != nightCueStateResolved || latest.Row.Outcome != nightCueOutcomeConfirmed {
		return nightBedBookmark{}
	}
	bm, _ := decodeNightBedBookmarkFromReason(latest.Row.OutcomeReason)
	return bm
}

// nightBedProgramLTCNode returns the first of nodeIDs holding the
// program+ltc role, or ok=false when none does.
func (h *handlers) nightBedProgramLTCNode(ctx context.Context, nodeIDs []string) (string, bool, error) {
	for _, nodeID := range nodeIDs {
		holds, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			return "", false, err
		}
		if holds {
			return nodeID, true, nil
		}
	}
	return "", false, nil
}

// nightResumeMultiNodeBackgroundAudio is the shared resume gate. The
// resume point travels on resume itself, never a separate apply push;
// with no ready program+ltc node or unknown bookmark, it falls back per-node.
func (h *handlers) nightResumeMultiNodeBackgroundAudio(ctx context.Context, now time.Time, rec store.NightSessionRecord, show string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow, nodeIDs []string) {
	sessionID := nightBackgroundAudioSessionID(rec)
	classify := h.nightBedResumeGateClassifier(now, sessionID)

	if _, already := nightBedScheduleForCycle(history, rec.Cycle); already {
		for _, nodeID := range nodeIDs {
			if state, _ := classify(history, nodeID); state == nightBedGateReady {
				h.nightBackgroundAudioResumeScheduled(ctx, now, rec, nodeID, sessionID, nightBedScheduleResult{UnalignedReason: nightBedLateUnalignedReason("resume")}, nightBedBookmark{}, history)
			}
		}
		return
	}

	ready, notReady, firstReadyAt := nightBedEvaluateGate(history, nodeIDs, classify)
	if len(ready) == 0 {
		return
	}
	if len(notReady) > 0 && now.Sub(firstReadyAt) < nightBedReadyStepBound {
		return
	}

	programLTC, hasProgramLTC, err := h.nightBedProgramLTCNode(ctx, nodeIDs)
	if err != nil {
		h.logWarn("night loop: background audio: bed resume: failed to resolve the program+ltc node", "sessionId", rec.ID, "error", err)
		return
	}
	programLTCReady := hasProgramLTC && nightBedContainsNode(ready, programLTC)

	var bookmark nightBedBookmark
	if programLTCReady {
		bookmark = nightBedNodeLatestPauseBookmark(history, programLTC)
	}

	if !programLTCReady || !bookmark.Known {
		reason := "the program+ltc node's own pause bookmark is unknown"
		switch {
		case !hasProgramLTC:
			reason = "no listed node holds the program+ltc role"
		case !programLTCReady:
			reason = "the program+ltc node is not ready by the bound"
		}
		sched := nightBedScheduleResult{UnalignedReason: reason}
		for _, nodeID := range ready {
			h.nightBackgroundAudioResumeScheduled(ctx, now, rec, nodeID, sessionID, sched, nightBedBookmark{}, history)
		}
		return
	}

	issuer := nightBackgroundAudioIssuer(rec)
	sched, err := h.nightGetOrComputeBedSchedule(ctx, now, rec, nodeIDs, history, issuer, "", show, ba, bookmark)
	if err != nil {
		h.logWarn("night loop: background audio: bed schedule failed", "sessionId", rec.ID, "error", err)
		return
	}
	h.nightDispatchBedNodesConcurrently(ready, func(nodeID string) {
		h.nightBackgroundAudioResumeScheduled(ctx, now, rec, nodeID, sessionID, sched, bookmark, history)
	})
}

// nightBackgroundAudioResumeScheduled mirrors [nightBackgroundAudioStartScheduled]:
// bookmark, when known, carries [pkgaudio.ParamResumeItemID]/Index/
// PositionMs together; sched's instant rides alongside it.
func (h *handlers) nightBackgroundAudioResumeScheduled(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, sched nightBedScheduleResult, bookmark nightBedBookmark, history []nightBackgroundAudioHistoryRow) {
	revision := h.nightBedNodeDispatchRevision(ctx, nodeID, sessionID, history)
	cueName := nightBackgroundAudioCueNameResume(int(revision))
	params := map[string]any{}
	if bookmark.Known {
		params[pkgaudio.ParamResumeItemID] = bookmark.ItemID
		params[pkgaudio.ParamResumeIndex] = bookmark.Index
		params[pkgaudio.ParamResumePositionMs] = bookmark.PositionMs
	}
	note := nightBedScheduleNote("resume", sched)
	if sched.Aligned {
		params[pkgaudio.ParamScheduledAtNs] = json.Number(fmt.Sprintf("%d", sched.ScheduledAtNs))
	}
	composeReason := func(reason string, _ map[string]any) string { return nightCueReasonWith(reason, note) }
	if _, _, err := h.nightRunBedAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, "audio.session.resume", nodeID, sessionID, params, revision, history, composeReason); err != nil {
		h.logWarn("night loop: background audio: bed resume failed", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
	}
}

// nightBackgroundAudioSuspend branches only for a multi-node bed's own
// pause, to capture the program+ltc node's bookmark. KNOWN GAP, accepted:
// a crash-recovery retry resolves without it, falling back to resume on arrival.
func (h *handlers) nightBackgroundAudioSuspend(ctx context.Context, now time.Time, rec store.NightSessionRecord, show, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, history []nightBackgroundAudioHistoryRow) {
	if !nightBackgroundAudioIsMultiNode(ba, h.showAudioNodes(ctx)(show)) || nightBackgroundSuspendKind(ba.Resume) != nightBGStepPause {
		h.nightBackgroundAudioStop(ctx, now, rec, nodeID, sessionID, ba.Resume, history)
		return
	}
	revision := h.nightBedNodeDispatchRevision(ctx, nodeID, sessionID, history)
	cueName := nightBackgroundAudioCueNamePause(int(revision))
	composeReason := func(reason string, evidence map[string]any) string {
		return nightCueReasonWith(reason, encodeNightBedBookmarkNote(nightBedBookmarkFromEvidence(evidence)))
	}
	if _, _, err := h.nightRunBedAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, "audio.session.pause", nodeID, sessionID, map[string]any{}, revision, history, composeReason); err != nil {
		h.logWarn("night loop: background audio: bed pause failed", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
	}
}

// nightRunBedAudioCommand is [nightRunAudioCommand]'s own counterpart for
// a bed's scheduling-sensitive steps: it dispatches directly so
// composeReason (may be nil) can attach a note onto the persisted reason.
func (h *handlers) nightRunBedAudioCommand(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName, action, nodeID, sessionID string, params map[string]any, revision int64, history []nightBackgroundAudioHistoryRow, composeReason func(reason string, evidence map[string]any) string) (store.NightCueOutboxRecord, map[string]any, error) {
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	switch {
	case err == nil:
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return row, nil, nil
		}
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		rs := nightBackgroundAudioRevisionState(sessionID, history)
		idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
		decision := rs.Apply(pkgaudio.InvocationID(idemKey), pkgaudio.Revision(revision))
		if !decision.Accepted {
			reason := "revision not accepted"
			if decision.Result != nil {
				reason = decision.Result.Reason
			}
			return store.NightCueOutboxRecord{}, nil, fmt.Errorf("api: background audio: refusing to commit %s/%s at revision %d: %s (current %d)", phase, cueName, revision, reason, decision.Revision)
		}
		if cerr := h.nightCommitCueRow(ctx, now, rec, phase, cueName, revision); cerr != nil {
			if !errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
				return store.NightCueOutboxRecord{}, nil, cerr
			}
		}
		row, err = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
		if err != nil {
			return store.NightCueOutboxRecord{}, nil, err
		}
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return row, nil, nil
		}
	default:
		return store.NightCueOutboxRecord{}, nil, err
	}

	idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
	if row.State == nightCueStatePending {
		t := now
		row.State = nightCueStateDispatched
		row.DispatchedAt = &t
		if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
			return store.NightCueOutboxRecord{}, nil, err
		}
	}

	// audio.session.* actions require params.sessionId/invocationId/revision
	// on the wire itself (internal/agent/audiosessionops.go's own
	// audioSessionCommonKeys): unlike nightDispatchCueAudio's generic path
	// (nightcue.go), this direct dispatch builds params itself, so it must
	// add all three here too.
	params["sessionId"] = sessionID
	params["invocationId"] = idemKey
	params["revision"] = uint64(revision)

	issuer := nightBackgroundAudioIssuer(rec)
	var evidence map[string]any
	result, problem, derr := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: action, NodeID: nodeID, SessionID: sessionID, Params: params,
		Revision: uint64(revision), IdempotencyKey: idemKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		OnEvidence: func(v map[string]any) { evidence = v },
	})

	row, gerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if gerr != nil {
		return store.NightCueOutboxRecord{}, evidence, gerr
	}

	reason := func(base string) string {
		if composeReason != nil {
			return composeReason(base, evidence)
		}
		return base
	}

	switch {
	case derr != nil:
		if !errors.Is(derr, broker.ErrResponseFailedBeforePublish) {
			// The command reached the wire and its outcome is genuinely
			// unknown (mirrors nightDispatchCueAudio's identical branch,
			// nightcue.go): leave unresolved for a later retry under the
			// SAME idempotency key.
			return row, evidence, nil
		}
		row.State = nightCueStateResolved
		row.Outcome = nightCueOutcomeFailed
		row.OutcomeReason = reason("this step could not be dispatched: " + derr.Error())
		resolvedAt := now
		row.ResolvedAt = &resolvedAt
		if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
			return store.NightCueOutboxRecord{}, evidence, err
		}
		return row, evidence, nil
	case problem != nil:
		row.State = nightCueStateResolved
		row.Outcome = nightCueOutcomeRefused
		row.OutcomeReason = reason(problem.Detail)
		resolvedAt := now
		row.ResolvedAt = &resolvedAt
		if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
			return store.NightCueOutboxRecord{}, evidence, err
		}
		return row, evidence, nil
	}

	if result.DispatchedAt != "" {
		if t, perr := parseTime(result.DispatchedAt); perr == nil {
			row.DispatchedAt = &t
		}
	}
	row.State = nightCueStateResolved
	row.Outcome = nightAudioCueOutcome(result.Outcome)
	row.OutcomeReason = reason(result.Reason)
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
		return store.NightCueOutboxRecord{}, evidence, err
	}
	return row, evidence, nil
}
