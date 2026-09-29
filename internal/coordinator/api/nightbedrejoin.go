package api

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// An audio node whose agent restarts, whose host reboots, or whose bed
// session is retired for any other reason comes back holding no bed at
// all. Its own step ledger still reads "started and confirmed", so the
// per-node state machine in nightbackgroundaudio.go has no next step to
// take, and every command this controller sends that session afterwards
// is refused because there is nothing on the node to address. Without
// the path in this file the node stays silent for the rest of the night
// and only an operator ending and restarting the night recovers it.
//
// The evidence is the node's OWN report, never a timer, a boot id
// comparison, or a clock difference. The node once confirmed an apply for
// this bed, it is publishing audio reports this coordinator currently
// holds, and those reports were recorded AFTER its own latest resolved
// step. On that footing there are two ways the bed can be gone, and the
// agent produces BOTH after a restart depending on whether its own
// persisted session survived:
//
//   - the report carries no state at all for the bed session, which is
//     the session having been retired or never restored; or
//   - it carries one that is stopped, failed, ready or unknown while this
//     node's own ledger says the bed should be playing, which is the
//     agent having restored the record without resuming playback.
//
// [observation.Observation.CollectedAt] is this coordinator's own record
// time, so the fence compares one clock with itself.
//
// All of this runs only from [handlers.nightAdvanceBackgroundAudioForNode],
// which nightTick calls only in the states the bed is supposed to be
// audible in (nightloop.go): preshow, the resting gap between shows, and
// the resting state at the end of the night. A bed held down for a show
// is never "recovered" back over it.
//
// Repair is [handlers.nightGiveNodeTheBedNow]: one fresh apply under a
// new revision, after which the ordinary per-node machine advances
// through gain to start exactly as it does for a node joining the bed
// for the first time. The start carries the item and position the nodes
// still playing the bed are at ([nightBedPeerPlaybackPoint]), so the
// rejoining node lands in the same track at the same offset rather than
// restarting the bed from its first item.

// The audio_session.* signals this file reads, mirroring
// nodeaudio.SignalSessionItemID and its siblings for the same reason
// [audioSessionStateSignalID] mirrors SignalSessionState: this package
// must never import the collector package that produces them.
const (
	audioSessionItemIDSignalID     observation.SignalID = "audio_session.playlist.item_id"
	audioSessionItemIndexSignalID  observation.SignalID = "audio_session.playlist.item_index"
	audioSessionPositionMsSignalID observation.SignalID = "audio_session.position_ms"
	audioSessionStaleSignalID      observation.SignalID = "audio_session.stale"
)

// audioNodeEngineStateSignalID is the one node-level audio signal every
// report carries a value for, whatever that node's hardware enumeration
// found: this file reads it only as proof that a node is publishing
// audio reports at all, never for its value.
const audioNodeEngineStateSignalID observation.SignalID = "node.audio.engine.state"

// nightBedPlaybackPoint is one node's own reported position in the bed,
// advanced to now. FromNodeID names the node it was read from, so an
// operator reading a rejoin's recorded reason can see whose position it
// matched.
type nightBedPlaybackPoint struct {
	FromNodeID string
	ItemID     string
	Index      int64
	PositionMs int64
}

// nightBedNodeReportsAudioSince reports whether nodeID is publishing
// audio reports this coordinator currently holds and recorded strictly
// after notBefore. False means there is no report to draw any conclusion
// from, which is never the same as a report that says the bed is gone.
// Strictly after, not at or after: a report recorded in the same instant
// as a command's own dispatch cannot describe that command's result.
func nightBedNodeReportsAudioSince(audio NodeAudioLister, now, notBefore time.Time, nodeID string) bool {
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Signal != audioNodeEngineStateSignalID || o.Resource.Kind != observation.ResourceNode {
			continue
		}
		return o.StateAt(now) == observation.StateCurrent && o.CollectedAt.After(notBefore)
	}
	return false
}

// nightBedSessionReportLimit mirrors mqttproto's own unexported
// maxAudioSessions, the cap a node truncates its session list to, exactly
// as internal/agent/audioreport.go's audioSessionReportLimit mirrors it
// from the other side: this package must not import either. A node
// currently reporting fewer sessions than this cannot have truncated the
// bed session out of its report, which is the one thing that could make a
// still-playing bed look absent.
const nightBedSessionReportLimit = 16

// nightBedSessionReading is what nodeID's current report says about one
// audio session: whether it appears at all, the state it claims, and
// whether the node marked that claim stale (it could not read the session
// fresh this tick because it was inside an in-flight engine call, so the
// values are its last known evidence rather than current).
type nightBedSessionReading struct {
	Present bool
	State   string
	Stale   bool
}

// nightBedReadSession walks nodeID's observations once for sessionID.
// Present is false only when the node's current report carries no state
// for this session at all: a STALE reading is Present with Stale true,
// never mistaken for an absent one, because a node that is merely busy is
// not a node that lost the bed.
func nightBedReadSession(audio NodeAudioLister, now time.Time, nodeID, sessionID string) nightBedSessionReading {
	var out nightBedSessionReading
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		switch o.Signal {
		case audioSessionStateSignalID:
			if o.StateAt(now) != observation.StateCurrent {
				continue
			}
			if v, ok := o.Value.(string); ok && v != "" {
				out.Present, out.State = true, v
			}
		case audioSessionStaleSignalID:
			if v, ok := o.Value.(bool); ok {
				out.Stale = v
			}
		}
	}
	return out
}

// nightBedNodeSessionCount is how many distinct audio sessions nodeID's
// current report carries, so an absent bed session can be separated from
// a report that hit [nightBedSessionReportLimit] and dropped it.
func nightBedNodeSessionCount(audio NodeAudioLister, nodeID string) int {
	seen := map[string]struct{}{}
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Resource.Kind == observation.ResourceAudioSession {
			seen[o.Resource.ID] = struct{}{}
		}
	}
	return len(seen)
}

// nightBedStepShouldLeaveBedPlaying reports whether a confirmed step of
// this kind leaves the bed playing on its node. It is what makes a
// reported "stopped" mean the bed was lost rather than deliberately
// suspended: a confirmed pause or stop is this controller's own doing.
func nightBedStepShouldLeaveBedPlaying(kind string) bool {
	switch kind {
	case nightBGStepStart, nightBGStepResume, nightBGStepFadeUp, nightBGStepExpiryRefresh:
		return true
	}
	return false
}

// nightBedReportedStateIsLost reports whether a state the node claims for
// the bed session, while this node's ledger says the bed should be
// playing, means playback is no longer running.
//
// Deliberately NOT lost: completed, which a bed configured not to repeat
// reaches legitimately and which re-applying would loop forever;
// restore_pending, which is the node's own restore driver already working
// on it; and preparing or stopping, which are both in transit.
func nightBedReportedStateIsLost(state string) bool {
	switch pkgaudio.State(state) {
	case pkgaudio.StateStopped, pkgaudio.StateFailed, pkgaudio.StateUnknown, pkgaudio.StateReady:
		return true
	}
	return false
}

// nightBedRejoinStartFailed reports whether a rejoin this controller
// already performed for nodeID went on to a start the node did not
// confirm. One such attempt is the end of it: the bed was given back, the
// node would not play it, and repeating that costs a dispatch every time
// the node reports without ever changing the answer. The row and its
// reason stay recorded, and [nightBedNotPlayingReason] names the state.
func nightBedRejoinStartFailed(steps []nightBackgroundAudioHistoryRow) bool {
	rejoined := false
	for _, row := range steps {
		switch {
		case row.Step.Kind == nightBGStepApply && row.Step.Rejoin:
			rejoined = true
		case rejoined && row.Step.Kind == nightBGStepStart &&
			row.Row.State == nightCueStateResolved && row.Row.Outcome != nightCueOutcomeConfirmed:
			return true
		}
	}
	return false
}

// nightBedNodeEverHeldTheBed reports whether nodeID ever confirmed an
// apply for this bed: the evidence that it once held the session, which
// is what separates "this node lost the bed" from "this node never
// accepted it". A node that has only ever refused the bed is left to the
// ordinary "apply did not confirm; not auto-retrying" rule, not
// re-applied on every tick.
func nightBedNodeEverHeldTheBed(steps []nightBackgroundAudioHistoryRow) bool {
	for _, row := range steps {
		if row.Step.Kind == nightBGStepApply && row.Row.State == nightCueStateResolved && row.Row.Outcome == nightCueOutcomeConfirmed {
			return true
		}
	}
	return false
}

// nightBedNodeLostSession reports whether nodeID has lost a bed it once
// held, and returns the step the loss is attributed to. That step's
// DispatchedAt is what fences the report: an audio report this
// coordinator recorded before that dispatch cannot describe its result.
//
// A step still in flight is left alone; so is a node whose own latest
// step is already a rejoin ([nightBackgroundAudioCueNameRejoinApply]),
// and so is one
// whose latest step the node itself refused. Together those bound this to
// ONE recovery attempt per genuine loss, after which the ordinary
// per-step rules take the node back.
//
// An ABSENT session counts whatever the latest step was, including a
// confirmed pause: an agent that restarts while the bed is paused for a
// show comes back holding nothing, and the resume after that show would
// be refused with nothing to self-heal it. It counts only when the node's
// report cannot have truncated the session away.
//
// A PRESENT session counts only when the node claims a state playback is
// not running in AND this node's own latest step is a CONFIRMED one that
// should have left the bed playing. Confirmed is what separates "this was
// playing and stopped being" from "this never started": a start that
// FAILED on the node, or one that timed out, resolves not-confirmed and
// leaves the session reporting failed or ready, and treating that as a
// loss would re-apply into the same failure under a new revision for the
// rest of the night. A stale claim never counts at all.
func nightBedNodeLostSession(audio NodeAudioLister, now time.Time, nodeID, sessionID string, steps []nightBackgroundAudioHistoryRow) (nightBackgroundAudioHistoryRow, bool) {
	if len(steps) == 0 || !nightBedNodeEverHeldTheBed(steps) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	latest := steps[len(steps)-1]
	if latest.Row.State != nightCueStateResolved || latest.Row.DispatchedAt == nil {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if latest.Step.Kind == nightBGStepApply && latest.Step.Rejoin {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if nightBedRejoinStartFailed(steps) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	// A node whose latest step it REFUSED is answering, and answering no.
	// Re-applying the bed at it cannot improve on that, and doing so every
	// time its report comes back would be the per-tick resend this work
	// exists to remove, wearing a different hat. The refusal and its
	// reason stay recorded for an operator.
	if nightBedStepNodeRefused(latest.Row) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if !nightBedNodeReportsAudioSince(audio, now, *latest.Row.DispatchedAt, nodeID) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if nightBedSessionLost(audio, now, nodeID, sessionID, latest) {
		return latest, true
	}
	return nightBackgroundAudioHistoryRow{}, false
}

// nightBedSessionLost is [nightBedNodeLostSession]'s own reading of the
// node's report, split out so the not-playing surface and the refusal
// rule can ask the same question without re-deriving the ledger fence.
func nightBedSessionLost(audio NodeAudioLister, now time.Time, nodeID, sessionID string, latest nightBackgroundAudioHistoryRow) bool {
	reading := nightBedReadSession(audio, now, nodeID, sessionID)
	if !reading.Present {
		return nightBedNodeSessionCount(audio, nodeID) < nightBedSessionReportLimit
	}
	if reading.Stale {
		return false
	}
	if latest.Row.Outcome != nightCueOutcomeConfirmed {
		return false
	}
	return nightBedStepShouldLeaveBedPlaying(latest.Step.Kind) && nightBedReportedStateIsLost(reading.State)
}

// nightBedPositionEvidenceMaxAge bounds how old a peer's own position
// reading may be before this coordinator stops extrapolating from it.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED. The node audio collector holds a
// reading as current for 45 s (nodeaudio.DefaultValidFor), which is
// longer than some bed items run: extrapolating that far forward can
// name a position past the end of the item, and the agent's own engine
// answers a start at or past EOS by presenting nothing while the session
// still reports playing, which is silence this controller could not then
// see. The coordinator cannot clamp instead, because it has no item
// duration to clamp against: nothing in the asset store records one. So
// a reading older than this is dropped and the node starts the bed from
// its first item, which is audible rather than silent.
const nightBedPositionEvidenceMaxAge = 10 * time.Second

// nightBedPeerPlaybackPoint is the item and position a node OTHER than
// exclude currently reports for sessionID, advanced by the time since
// this coordinator recorded that report.
//
// The advance is an ESTIMATE, and the recorded reason says so: a node
// publishes its position on its own report interval, so the raw number
// is already that interval out of date by the time it is read, and
// advancing it on this coordinator's own clock from its own record time
// is closer to the truth than using it unchanged. It is never a
// sample-accurate instant, and nothing here claims to align the
// rejoining node with the others the way a shared start instant does
// (ADR-049 decisions 3 and 8); a rejoin is decision 4's own degradation,
// playing rather than silent, and is recorded unaligned with its reason.
func nightBedPeerPlaybackPoint(audio NodeAudioLister, now time.Time, sessionID string, nodeIDs []string, exclude string) (nightBedPlaybackPoint, bool) {
	for _, nodeID := range nodeIDs {
		if nodeID == exclude || nodeID == nightBedScheduleNodeID {
			continue
		}
		state, ok := nightBackgroundAudioReportedSessionState(audio, now, time.Time{}, nodeID, sessionID)
		if !ok || state != string(pkgaudio.StatePlaying) {
			continue
		}
		itemID, index, positionMs, collectedAt, ok := nightBedReportedItemAndPosition(audio, now, nodeID, sessionID)
		if !ok {
			continue
		}
		elapsed := now.Sub(collectedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed > nightBedPositionEvidenceMaxAge {
			continue
		}
		return nightBedPlaybackPoint{
			FromNodeID: nodeID, ItemID: itemID, Index: index,
			PositionMs: positionMs + elapsed.Milliseconds(),
		}, true
	}
	return nightBedPlaybackPoint{}, false
}

// nightBedStartPointRefusedReason is the recorded reason for the one
// retry a refused playback point gets.
const nightBedStartPointRefusedReason = "the position read from another speaker was refused, so the background music starts from its first track here"

// nightBedJoinPoint is where a node joining a running bed should begin:
// the position a node still PLAYING the bed reports, or, when the bed is
// paused on every other node, the resume point those nodes will come back
// on. The second case is what a node rejoining while the bed is held down
// for a show needs; starting it at item zero there would put it on a
// different track the moment the others resume.
//
// The paused case prefers the program+ltc node's own bookmark, the same
// one [handlers.nightResumeMultiNodeBackgroundAudio] pushes to every
// listed node, so a node that rejoins and a node that merely resumes end
// up at the same place.
func (h *handlers) nightBedJoinPoint(ctx context.Context, now time.Time, sessionID string, nodeIDs []string, exclude string, history []nightBackgroundAudioHistoryRow) (nightBedPlaybackPoint, bool) {
	if point, ok := nightBedPeerPlaybackPoint(h.deps.Audio, now, sessionID, nodeIDs, exclude); ok {
		return point, true
	}
	ordered := make([]string, 0, len(nodeIDs))
	if programLTC, has, err := h.nightBedProgramLTCNode(ctx, nodeIDs); err == nil && has && programLTC != exclude {
		ordered = append(ordered, programLTC)
	}
	for _, nodeID := range nodeIDs {
		if nodeID != exclude && nodeID != nightBedScheduleNodeID && !nightBedContainsNode(ordered, nodeID) {
			ordered = append(ordered, nodeID)
		}
	}
	for _, nodeID := range ordered {
		if bm := nightBedNodeLatestPauseBookmark(history, nodeID); bm.Known {
			return nightBedPlaybackPoint{
				FromNodeID: nodeID, ItemID: bm.ItemID,
				Index: int64(bm.Index), PositionMs: bm.PositionMs,
			}, true
		}
	}
	return nightBedPlaybackPoint{}, false
}

// nightBedReportedItemAndPosition reads nodeID's own current item and
// position for sessionID, with the record time of the position itself.
// ok is false unless the item id, its index, and the position are all
// current: a point missing any one of the three would name a position
// without the track it belongs to.
func nightBedReportedItemAndPosition(audio NodeAudioLister, now time.Time, nodeID, sessionID string) (itemID string, index, positionMs int64, collectedAt time.Time, ok bool) {
	var haveItem, haveIndex, havePosition bool
	for _, o := range audio.NodeAudioObservations(nodeID) {
		if o.Resource.Kind != observation.ResourceAudioSession || o.Resource.ID != sessionID {
			continue
		}
		if o.StateAt(now) != observation.StateCurrent {
			continue
		}
		switch o.Signal {
		case audioSessionItemIDSignalID:
			itemID, haveItem = o.Value.(string)
			haveItem = haveItem && itemID != ""
		case audioSessionItemIndexSignalID:
			index, haveIndex = o.Value.(int64)
		case audioSessionPositionMsSignalID:
			positionMs, havePosition = o.Value.(int64)
			collectedAt = o.CollectedAt
		}
	}
	if !haveItem || !haveIndex || !havePosition || index < 0 || positionMs < 0 {
		return "", 0, 0, time.Time{}, false
	}
	return itemID, index, positionMs, collectedAt, true
}

// nightBedSessionTouchedOutsideLedger reports whether a command this
// controller did not commit as a step has reached (nodeID, sessionID)
// since the step at latestStepRevision.
//
// MANUAL CONTROL WINS. An operator stopping or clearing a bed session
// through POST /nodes/{nodeId}/audio/sessions/{sessionId}/stop goes
// through the ordinary audio dispatch path, which writes no night step,
// so this controller's own ledger still reads "started and confirmed"
// while the node correctly reports stopped. Without this check the rejoin
// would read that as a lost bed and start the music again under the
// operator, every time, for the rest of the night.
//
// The evidence is the audio session's own persisted revision, which every
// successfully dispatched audio.session.* command advances
// (persistAudioSessionDesiredState) whoever sent it. A revision higher
// than this controller's own latest step is therefore a command from
// outside this ledger, whatever it was. A refused or unanswered command
// never advances it, so a refusal an operator made no progress with does
// not silence the recovery either.
func (h *handlers) nightBedSessionTouchedOutsideLedger(ctx context.Context, nodeID, sessionID string, latestStepRevision int64) bool {
	return h.nightAudioSessionPersistedRevision(ctx, nodeID, sessionID) > latestStepRevision
}

// nightBedSessionTouchedOutsideLedgerFor is
// [handlers.nightBedSessionTouchedOutsideLedger] for the read path, which
// holds only [Dependencies]. A read failure answers false: the not-playing
// surface says what it can see, and inventing an operator stop it has no
// evidence for would be worse than naming a speaker that is genuinely
// absent.
func nightBedSessionTouchedOutsideLedgerFor(ctx context.Context, deps Dependencies, nodeID, sessionID string, latestStepRevision int64) bool {
	rec, err := deps.AudioSessions.GetAudioSession(ctx, nodeID, sessionID)
	if err != nil {
		return false
	}
	return int64(rec.Revision) > latestStepRevision
}

// nightBedStartPointWasRefused reports whether row is a start the node
// refused that had named a playback point, and which has not already been
// retried without one. A point can be refused for a reason nothing here
// can fix, most plainly a media.playlist edited while the night runs so
// the item the others are on is no longer at that index on this node; one
// retry without the point then gets the node playing from the bed's first
// item instead of leaving it silent.
//
// Which start carried a point is read from the step's own cue name
// ([nightBackgroundAudioCueNameJoinStart]), never from its reason: the
// reason is rendered to operators verbatim and must not carry this
// controller's own bookkeeping.
func nightBedStartPointWasRefused(step nightBackgroundAudioStep, row store.NightCueOutboxRecord) bool {
	return step.Kind == nightBGStepStart && step.WithPoint && nightBedStepNodeRefused(row)
}

// nightBedRejoinOperatorReasonNote is the rejoin apply row's own
// operator-facing note: fact first, then what happens next, with none of
// this controller's own vocabulary in it.
const nightBedRejoinOperatorReasonNote = "This speaker is no longer holding the background music this night started, so it is being given the music again."

// nightGiveNodeTheBedNow is the one "give this node the background music
// now" path: a fresh apply under a new revision, dispatched through the
// bed's own command path so the row can record why it happened. The
// ordinary per-node state machine advances from that apply through gain
// to start on the following ticks, so nothing here duplicates the rest
// of the sequence.
//
// The apply is committed under [nightBackgroundAudioCueNameRejoinApply],
// which is what marks it a rejoin durably without putting any of this
// controller's own bookkeeping into the operator-facing reason.
func (h *handlers) nightGiveNodeTheBedNow(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow, note string) {
	revision := h.nightBedNodeDispatchRevision(ctx, nodeID, sessionID, history)
	cueName := nightBackgroundAudioCueNameRejoinApply(int(revision))
	params := nightBackgroundApplyParams(ctx, h.deps.Nodes, now, nodeID, owner, ba, items)
	composeReason := func(reason string, _ map[string]any) string { return nightCueReasonWith(reason, note) }
	if _, _, err := h.nightRunBedAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, "audio.session.apply", nodeID, sessionID, params, revision, history, composeReason); err != nil {
		h.logWarn("night loop: background audio: giving this node the bed again failed", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
	}
}

// nightBedRecoverLostSessionForNode is [nightAdvanceBackgroundAudioForNode]'s
// own rejoin check: true means this tick handled the node and the caller
// must not also run its ordinary step machine.
func (h *handlers) nightBedRecoverLostSessionForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) bool {
	steps := nightBackgroundAudioStepsForNode(history, nodeID)
	lost, ok := nightBedNodeLostSession(h.deps.Audio, now, nodeID, sessionID, steps)
	if !ok {
		return false
	}
	if h.nightBedSessionTouchedOutsideLedger(ctx, nodeID, sessionID, lost.Row.ActionRevision) {
		h.logWarn("night loop: background audio: this bed session was addressed from outside this night's own controls; leaving it alone",
			"sessionId", rec.ID, "nodeId", nodeID)
		return false
	}
	h.logWarn("night loop: background audio: this node is no longer holding the bed session; giving it the bed again",
		"sessionId", rec.ID, "nodeId", nodeID, "lostStep", lost.Step.Kind, "lostRevision", lost.Row.ActionRevision)
	h.nightGiveNodeTheBedNow(ctx, now, rec, nodeID, sessionID, ba, owner, items, history, nightBedRejoinOperatorReasonNote)
	return true
}

// nightBedStepNodeRefused reports whether row records a step the NODE
// itself answered with a refusal, as opposed to one that failed before
// reaching it, one that has not been answered yet, or one this
// coordinator's own acceptance ledger refused before dispatch (which is
// CURED by a fresh revision: [nightPersistLedgerRefusal] records one with
// no dispatch instant and its own fixed reason).
func nightBedStepNodeRefused(row store.NightCueOutboxRecord) bool {
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeRefused {
		return false
	}
	return row.DispatchedAt != nil && row.OutcomeReason != nightLedgerRefusalOperatorReason
}

// nightBedRefusalIsFinal reports whether re-sending row's step could
// possibly do any good. Only one refusal is final: one the node answered
// while its bed session is gone, because there is nothing left on that
// node for the command to address and the rejoin path owns putting it
// back.
//
// Every other refusal stays retryable, which matters most for the exact
// case an agent restart produces: [audio.Manager.Resume] refuses with
// "No audio engine is connected yet. Retry once one connects." until the
// node's own engine binding arrives, and a rule that treated that as
// final would leave the node silent for the night.
func (h *handlers) nightBedRefusalIsFinal(now time.Time, nodeID, sessionID string, row store.NightCueOutboxRecord) bool {
	if !nightBedStepNodeRefused(row) {
		return false
	}
	reading := nightBedReadSession(h.deps.Audio, now, nodeID, sessionID)
	return !reading.Present && nightBedNodeSessionCount(h.deps.Audio, nodeID) < nightBedSessionReportLimit
}

// nightBedPointOrNil is the pointer form [handlers.nightBackgroundAudioStartScheduled]
// takes, so a caller reads one line rather than branching around it.
func nightBedPointOrNil(point nightBedPlaybackPoint, has bool) *nightBedPlaybackPoint {
	if !has {
		return nil
	}
	return &point
}

// nightBedStartPointNote is the operator-facing note a start carrying a
// playback point records: fact first, then what it means for the sound.
// The item's own id is this controller's internal handle for a playlist
// entry, not a track name an operator would recognise, so it is not shown.
func nightBedStartPointNote(point nightBedPlaybackPoint) string {
	return fmt.Sprintf("This speaker was given the background music from about %d seconds into the track %s is playing, so it may be slightly behind the others.",
		point.PositionMs/1000, point.FromNodeID)
}

// nightBedNodesNotPlaying answers "which speakers is the background music
// not coming out of right now", for the Night screen and for
// showmeshctl. rows is this session's own background-audio outbox rows,
// passed in because its one caller has already read them. It reads each node's OWN current report, never this
// coordinator's idea of what it asked for, and it considers only nodes
// that are both configured for this bed now and have had at least one
// step dispatched to them: a bed that has not been applied to anything
// yet is not a list of absent speakers.
//
// A node reporting the session as playing is not listed, so the list
// clears itself the moment a node is back.
func nightBedNodesNotPlaying(ctx context.Context, deps Dependencies, rec store.NightSessionRecord, now time.Time, rows []store.NightCueOutboxRecord) []v1.NightBedNodeNotPlaying {
	out := []v1.NightBedNodeNotPlaying{}
	payload, err := nightPinnedNightSessionPayload(ctx, deps, rec)
	if err != nil || payload.Resting.BackgroundAudio == nil {
		return out
	}
	resolved, _, ok := nightResolveBackgroundAudioFor(ctx, deps, rec, payload.Resting.BackgroundAudio)
	if !ok {
		return out
	}
	configured, _ := resolved.ResolvedPlaybackNodeIDs(showAudioNodesFor(ctx, deps, payload.Show))
	if len(configured) == 0 {
		return out
	}
	history := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	dispatched := make(map[string]bool, len(rows))
	for _, row := range rows {
		step, nodeID, ok := nightParseBackgroundAudioRow(row)
		history = append(history, nightBackgroundAudioHistoryRow{Step: step, Row: row, NodeID: nodeID, Parsed: ok})
		if ok {
			dispatched[nodeID] = true
		}
	}
	sessionID := nightBackgroundAudioSessionID(rec)
	for _, nodeID := range configured {
		if !dispatched[nodeID] || nightBedNodeDeliberatelySuspended(history, nodeID) {
			continue
		}
		reading := nightBedReadSession(deps.Audio, now, nodeID, sessionID)
		if reading.Present && reading.State == string(pkgaudio.StatePlaying) {
			continue
		}
		steps := nightBackgroundAudioStepsForNode(history, nodeID)
		facts := nightBedNotPlayingFacts{Reading: reading}
		if latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID); ok {
			facts.TouchedOutsideLedger = nightBedSessionTouchedOutsideLedgerFor(ctx, deps, nodeID, sessionID, latest.Row.ActionRevision)
		}
		facts.RejoinAlreadyFailed = nightBedRejoinStartFailed(steps)
		out = append(out, v1.NightBedNodeNotPlaying{NodeID: nodeID, Reason: nightBedNotPlayingReason(deps.Audio, now, nodeID, facts)})
	}
	return out
}

// nightBedNotPlayingFacts is everything [nightBedNotPlayingReason] is
// allowed to speak from, gathered once per node.
type nightBedNotPlayingFacts struct {
	Reading              nightBedSessionReading
	TouchedOutsideLedger bool
	RejoinAlreadyFailed  bool
}

// nightBedNodeDeliberatelySuspended reports whether this controller's own
// latest step for nodeID is a confirmed pause or stop, which is the bed
// being held down for a show rather than a speaker that dropped out. Such
// a node is never named as not playing: it is not playing because it was
// told not to.
func nightBedNodeDeliberatelySuspended(history []nightBackgroundAudioHistoryRow, nodeID string) bool {
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok || latest.Row.State != nightCueStateResolved || latest.Row.Outcome != nightCueOutcomeConfirmed {
		return false
	}
	switch latest.Step.Kind {
	case nightBGStepPause, nightBGStepStop, nightBGStepFadeDown:
		return true
	}
	return false
}

// nightBedNotPlayingReason states what this coordinator actually knows
// about one absent speaker, and what happens next. Three distinct cases,
// never collapsed into one: the node is not reporting at all, it is
// reporting and has no background music session, or it has one that is
// not playing.
func nightBedNotPlayingReason(audio NodeAudioLister, now time.Time, nodeID string, facts nightBedNotPlayingFacts) string {
	reading := facts.Reading
	switch {
	case !nightBedNodeReportsAudioSince(audio, now, time.Time{}, nodeID):
		return "This speaker is not reporting its audio, so whether the background music is playing on it is unknown. Check that the node is powered on and connected."
	case reading.Stale:
		return "This speaker was too busy to report the background music this time, so what it is doing now is unknown. It reports again on its next cycle."
	case facts.TouchedOutsideLedger:
		return "The background music on this speaker was stopped from outside this night's own controls. The coordinator is leaving it that way; start it again from the speaker's own controls if it should be playing."
	case facts.RejoinAlreadyFailed:
		return "The coordinator already gave this speaker the background music again and it did not start. It is not trying again on its own; check the speaker."
	case !reading.Present:
		return "This speaker is no longer holding the background music this night started. The coordinator gives it the music again on its next check."
	case nightBedReportedStateIsLost(reading.State):
		return "The background music this night started is no longer playing on this speaker. The coordinator starts it again on its next check."
	case reading.State == string(pkgaudio.StatePaused):
		return "The background music is paused on this speaker. It starts again when the coordinator brings the speakers back together."
	default:
		return "This speaker is not playing the background music it was given, and the coordinator is not changing that on its own. Check the speaker if the music does not come back."
	}
}
