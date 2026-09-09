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
