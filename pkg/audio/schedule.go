package audio

// This file is Track I seam I2's shared start-scheduling vocabulary: the
// name of audio.session.start's optional media-clock start instant, and
// the refusal reason a node reports when that instant is already past.
// Both are wire values a coordinator writes and a node reads, so they
// live here rather than in either side's own package.

// ParamScheduledAtNs is audio.session.start's optional start instant
// (RES-019 section 6's T0): a reading of the RECEIVING node's media
// clock, in nanoseconds on that clock's own timescale, carried as a JSON
// int64. It is never wall time and never a duration.
//
// Nanoseconds since an epoch are around 1.79e18, well past float64's
// exact integer range, so every decoder on the path has to preserve the
// literal rather than round it: see [github.com/showmeshsystems/showmesh/pkg/mqttproto.DecodeCmdPayload]
// on the node side and ui/src/api/bigint.ts on the operator side.
// Milliseconds are not an alternative unit here, because 1 ms is 48
// samples at 48 kHz, several times coarser than the alignment this seam
// exists to reach.
const ParamScheduledAtNs = "scheduledAtNs"

// ReasonScheduledStartInPast is the refusal a node reports when
// [ParamScheduledAtNs] names an instant its own media clock has already
// passed. The node refuses; it does not start late, clamp the instant to
// now, or warn and continue. This is the show-continues rule pointing
// the unusual way: a node that starts late is worse than a node that
// visibly refused, because a late start is audible and a refusal is
// legible.
const ReasonScheduledStartInPast = "scheduled_start_in_past"

// ReasonScheduledStartIgnored prefixes the note a node reports when it
// accepted a [ParamScheduledAtNs] start but its own media clock was not
// usable to honor that instant, so it started on arrival instead. Unlike
// [ReasonScheduledStartInPast], this is not a refusal: the node still
// confirms the start, only unaligned to the shared instant, and a caller
// matches this prefix rather than the free text after it to tell the two
// apart.
const ReasonScheduledStartIgnored = "scheduled_start_ignored"

// The audio.session.prepare result's media-clock readiness fields. A
// coordinator cannot pick a T0 without a reading of the target node's own
// media clock (RES-019 section 6: it obtains the clock from the node that
// holds it, because it does not hold one itself), and these are how that
// node reports one.
//
// They ride the PREPARE RESULT, which is request-scoped and freshly built
// per command, and deliberately not the node.clock observation, which is
// RETAINED: a media-clock instant published retained would be served to
// the coordinator from whenever that node last published while looking
// exactly like a current reading, and a stale instant that is plausible
// is worse than none.
//
// The reading is sampled from the clock provider's own Now, never from
// the tracker's status and never from wall time. Its epoch is the PTP
// domain's, which may be arbitrary (RES-019 section 5.1); it is only ever
// meaningful against the SAME node it came from, which is exactly how a
// T0 is used, since ParamScheduledAtNs is read on that node's clock too.
const (
	// ResultMediaClockValid is false whenever this node could not take a
	// usable reading. Everything below is meaningless when it is false,
	// and ResultMediaClockReason then says why. A consumer that treats an
	// absent validity flag as valid has invented a clock.
	ResultMediaClockValid  = "mediaClockValid"
	ResultMediaClockReason = "mediaClockReason"

	// ResultMediaClockNowNs is the reading itself, nanoseconds on this
	// node's media clock, as a JSON int64 with the same exactness
	// requirement ParamScheduledAtNs carries in the other direction: see
	// mqttproto.DecodeResultPayload, which preserves it rather than
	// letting encoding/json round it into a float64.
	ResultMediaClockNowNs = "mediaClockNowNs"

	// ResultMediaClockErrorBoundNs is the reading's own stated error
	// bound, present only when ResultMediaClockErrorBoundKnown is true.
	// A source that cannot state one reports known false rather than a
	// zero, because a zero bound is itself a claim of exactness that no
	// source here can make.
	ResultMediaClockErrorBoundNs    = "mediaClockErrorBoundNs"
	ResultMediaClockErrorBoundKnown = "mediaClockErrorBoundKnown"

	// ResultPrerollMs is how long this node's prepare actually spent
	// opening, decoding and prerolling the item. Absent when this session
	// has never prepared successfully; never a zero standing in for one.
	ResultPrerollMs = "prerollMs"
)

// The audio.session.pause result's bookmark evidence (ADR-049 decision
// 4): a coordinator resuming a multi-node bed reads the program+ltc
// node's own bookmark from THIS node's pause result, rather than a
// separate observation call, then pushes it to every other listed node
// via audio.session.resume's own [ParamResumeItemID]/[ParamResumeIndex]/
// [ParamResumePositionMs] before resuming all of them at one instant.
const (
	// ResultBookmarkKnown is false whenever this session had nothing to
	// bookmark (never paused with a resolvable item and position).
	// ItemID/Index/PositionMs are meaningful only when this is true.
	ResultBookmarkKnown = "bookmarkKnown"

	ResultBookmarkItemID     = "bookmarkItemId"
	ResultBookmarkIndex      = "bookmarkIndex"
	ResultBookmarkPositionMs = "bookmarkPositionMs"
)

// StartTriggerMultiSync and StartTriggerCoordinator are the two closed
// values a session's own start trigger evidence (ADR-051 decision 6)
// reports: "multisync" when a node's MultiSync listener started the
// session itself, on FPP's own sequence START packet, with no coordinator
// round trip in the path; "coordinator" for every other start, dispatched
// by a "cue.activate" or "audio.session.start" command. A session that has
// never started reports neither (an empty string).
const (
	StartTriggerMultiSync   = "multisync"
	StartTriggerCoordinator = "coordinator"
)

// The "cue.activate" result's own start-trigger evidence (ADR-051 decision
// 6), written only when that activation finds its Cue's audio session
// already started by MultiSync rather than starting it itself: a
// coordinator that dispatched the activation needs to know it did not
// restart or reseek anything, and why. ResultStartTrigger is always
// [StartTriggerMultiSync] when these are present at all; the other three
// mirror the audio session report's identical fields for the same event,
// so a caller never has to reconcile two independently-shaped answers to
// "how did this Cue's audio actually start."
const (
	ResultStartTrigger            = "startTrigger"
	ResultTriggerSequenceFilename = "triggerSequenceFilename"
	ResultTriggerArrivalNs        = "triggerArrivalNs"
	ResultStartLeadMs             = "startLeadMs"
	ResultPreparedLate            = "preparedLate"
)

// ParamResumeItemID, ParamResumeIndex, and ParamResumePositionMs are
// audio.session.resume's optional named resume target (ADR-049 decision
// 4, amended): the exact playlist item and position a coordinator wants
// every listed node to resume at, read from [ResultBookmarkItemID]/
// [ResultBookmarkIndex]/[ResultBookmarkPositionMs] on the program+ltc
// node's own pause result and pushed onto resume itself, never onto
// audio.session.apply. All three present together or none; a partial
// set is refused. A node resumes the named item at the named position
// using ITS OWN current playlist revision, never one carried on the
// wire.
const (
	ParamResumeItemID     = "resumeItemId"
	ParamResumeIndex      = "resumeIndex"
	ParamResumePositionMs = "resumePositionMs"
)
