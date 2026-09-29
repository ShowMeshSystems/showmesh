package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// comparison, or a clock difference: the node once confirmed an apply for
// this bed, it is publishing audio reports this coordinator currently
// holds, those reports were recorded AFTER its own latest resolved step,
// and they carry no state at all for the bed session. That is the node
// saying the session is gone. [observation.Observation.CollectedAt] is
// this coordinator's own record time, so the fence compares one clock
// with itself.
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

// nightBedNodeLostSession reports whether nodeID's bed session is gone
// from a node that once held it, and returns the step the loss is
// attributed to. That step's DispatchedAt is what fences the report: an
// audio report this coordinator recorded before that dispatch cannot
// describe its result.
//
// A step still in flight is left alone; so is a node whose own latest
// step is already a rejoin ([nightBedRejoinNotePrefix]), which bounds
// this to ONE recovery attempt before the ordinary per-step rules take
// the node back. A confirmed pause is included on purpose: an agent that
// restarts while the bed is paused for a show comes back holding nothing,
// and the resume after that show would be refused with nothing to
// self-heal it.
func nightBedNodeLostSession(audio NodeAudioLister, now time.Time, nodeID, sessionID string, steps []nightBackgroundAudioHistoryRow) (nightBackgroundAudioHistoryRow, bool) {
	if len(steps) == 0 || !nightBedNodeEverHeldTheBed(steps) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	latest := steps[len(steps)-1]
	if latest.Row.State != nightCueStateResolved || latest.Row.DispatchedAt == nil {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if _, isRejoin := decodeNightBedRejoinNote(latest.Row.OutcomeReason); isRejoin {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if !nightBedNodeReportsAudioSince(audio, now, *latest.Row.DispatchedAt, nodeID) {
		return nightBackgroundAudioHistoryRow{}, false
	}
	if _, reported := nightBackgroundAudioReportedSessionState(audio, now, time.Time{}, nodeID, sessionID); reported {
		return nightBackgroundAudioHistoryRow{}, false
	}
	return latest, true
}

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
		return nightBedPlaybackPoint{
			FromNodeID: nodeID, ItemID: itemID, Index: index,
			PositionMs: positionMs + elapsed.Milliseconds(),
		}, true
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

// nightBedRejoinNotePrefix tags the JSON fragment a rejoin apply row's
// own OutcomeReason carries, exactly as [nightBedBookmarkNotePrefix]
// tags a pause row's bookmark, so a later tick can tell which lost step
// a rejoin already answered without a second column.
const nightBedRejoinNotePrefix = "bedRejoin="

// nightBedRejoinNote records the revision of the step whose loss this
// rejoin repairs.
type nightBedRejoinNote struct {
	LostRevision int64 `json:"lostRevision"`
}

func encodeNightBedRejoinNote(n nightBedRejoinNote) string {
	b, _ := json.Marshal(n)
	return nightBedRejoinNotePrefix + string(b)
}

func decodeNightBedRejoinNote(reason string) (nightBedRejoinNote, bool) {
	idx := strings.Index(reason, nightBedRejoinNotePrefix)
	if idx < 0 {
		return nightBedRejoinNote{}, false
	}
	var n nightBedRejoinNote
	if err := json.Unmarshal([]byte(reason[idx+len(nightBedRejoinNotePrefix):]), &n); err != nil {
		return nightBedRejoinNote{}, false
	}
	return n, true
}

// nightBedAlreadyRejoinedFor reports whether nodeID has already been
// given the bed back once for the loss of the step at lostRevision. One
// rejoin per lost step is the bound that keeps a node this coordinator
// cannot reach from being re-applied on every tick: the rejoin's own
// start becomes the next step whose loss can be answered, so a node that
// keeps losing the bed keeps being recovered, while a node that confirms
// nothing is left alone with its reason recorded.
func nightBedAlreadyRejoinedFor(history []nightBackgroundAudioHistoryRow, nodeID string, lostRevision int64) bool {
	for _, row := range nightBackgroundAudioStepsForNode(history, nodeID) {
		if row.Step.Kind != nightBGStepApply {
			continue
		}
		if note, ok := decodeNightBedRejoinNote(row.Row.OutcomeReason); ok && note.LostRevision == lostRevision {
			return true
		}
	}
	return false
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
// lostRevision is the revision of the step whose loss this repairs, used
// to keep the repair to one attempt per loss
// ([nightBedAlreadyRejoinedFor]); pass 0 for a caller with no lost step
// to name.
func (h *handlers) nightGiveNodeTheBedNow(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow, note string, lostRevision int64) {
	revision := h.nightBedNodeDispatchRevision(ctx, nodeID, sessionID, history)
	cueName := nightBackgroundAudioCueNameApply(int(revision))
	params := nightBackgroundApplyParams(ctx, h.deps.Nodes, now, nodeID, owner, ba, items)
	rejoinNote := note
	if lostRevision > 0 {
		rejoinNote = nightCueReasonWith(rejoinNote, encodeNightBedRejoinNote(nightBedRejoinNote{LostRevision: lostRevision}))
	}
	composeReason := func(reason string, _ map[string]any) string { return nightCueReasonWith(reason, rejoinNote) }
	if _, _, err := h.nightRunBedAudioCommand(ctx, now, rec, nightPhaseRestingBackgroundNode(nodeID), cueName, "audio.session.apply", nodeID, sessionID, params, revision, history, composeReason); err != nil {
		h.logWarn("night loop: background audio: giving this node the bed again failed", "sessionId", rec.ID, "nodeId", nodeID, "error", err)
	}
}

// nightBedRecoverLostSessionForNode is [nightAdvanceBackgroundAudioForNode]'s
// own rejoin check: true means this tick handled the node and the caller
// must not also run its ordinary step machine.
func (h *handlers) nightBedRecoverLostSessionForNode(ctx context.Context, now time.Time, rec store.NightSessionRecord, nodeID, sessionID string, ba *config.NightSessionBackgroundAudio, owner nightBackgroundAudioOwner, items []pkgaudio.PlaylistItem, history []nightBackgroundAudioHistoryRow) bool {
	lost, ok := nightBedNodeLostSession(h.deps.Audio, now, nodeID, sessionID, nightBackgroundAudioStepsForNode(history, nodeID))
	if !ok {
		return false
	}
	if nightBedAlreadyRejoinedFor(history, nodeID, lost.Row.ActionRevision) {
		return false
	}
	h.logWarn("night loop: background audio: this node is no longer holding the bed session; giving it the bed again",
		"sessionId", rec.ID, "nodeId", nodeID, "lostStep", lost.Step.Kind, "lostRevision", lost.Row.ActionRevision)
	h.nightGiveNodeTheBedNow(ctx, now, rec, nodeID, sessionID, ba, owner, items, history,
		nightBedRejoinOperatorReasonNote, lost.Row.ActionRevision)
	return true
}

// nightBedStepNodeRefused reports whether row records a step the NODE
// itself answered with a refusal, as opposed to one that failed before
// reaching it, one that has not been answered yet, or one this
// coordinator's own acceptance ledger refused before dispatch. A node's
// refusal is an answer: the same command under a fresh revision gets the
// same answer, so it is not re-sent. Everything else still retries, and
// a ledger refusal in particular is CURED by a fresh revision
// ([nightPersistLedgerRefusal] records one with no dispatch instant and
// its own fixed reason).
func nightBedStepNodeRefused(row store.NightCueOutboxRecord) bool {
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeRefused {
		return false
	}
	return row.DispatchedAt != nil && row.OutcomeReason != nightLedgerRefusalOperatorReason
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
func nightBedStartPointNote(point nightBedPlaybackPoint) string {
	return fmt.Sprintf("this speaker was given the background music from where %s is playing it, track %s at about %d seconds in, so it may be slightly behind the others",
		point.FromNodeID, point.ItemID, point.PositionMs/1000)
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
	dispatched := make(map[string]bool, len(rows))
	for _, row := range rows {
		if _, nodeID, ok := nightParseBackgroundAudioRow(row); ok {
			dispatched[nodeID] = true
		}
	}
	sessionID := nightBackgroundAudioSessionID(rec)
	for _, nodeID := range configured {
		if !dispatched[nodeID] {
			continue
		}
		state, reported := nightBackgroundAudioReportedSessionState(deps.Audio, now, time.Time{}, nodeID, sessionID)
		if reported && state == string(pkgaudio.StatePlaying) {
			continue
		}
		out = append(out, v1.NightBedNodeNotPlaying{NodeID: nodeID, Reason: nightBedNotPlayingReason(deps.Audio, now, nodeID, state, reported)})
	}
	return out
}

// nightBedNotPlayingReason states what this coordinator actually knows
// about one absent speaker, and what happens next. Three distinct cases,
// never collapsed into one: the node is not reporting at all, it is
// reporting and has no background music session, or it has one that is
// not playing.
func nightBedNotPlayingReason(audio NodeAudioLister, now time.Time, nodeID, state string, reported bool) string {
	switch {
	case !nightBedNodeReportsAudioSince(audio, now, time.Time{}, nodeID):
		return "This speaker is not reporting its audio, so whether the background music is playing on it is unknown. Check that the node is powered on and connected."
	case !reported:
		return "This speaker is no longer holding the background music this night started. The coordinator gives it the music again on its next check."
	case state == string(pkgaudio.StatePaused):
		return "The background music is paused on this speaker. It starts again when the coordinator brings the speakers back together."
	default:
		return "This speaker is not playing the background music it was given. The coordinator brings it back with the other speakers on its next check."
	}
}
