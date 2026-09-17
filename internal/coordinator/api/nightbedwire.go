package api

// ADR-049 decision 7's multi-node bed runtime needs audio.session.pause's
// NEW result-evidence keys (R1: ResultBookmarkKnown/ItemID/Index/
// PositionMs) to read the program+ltc node's own bookmark before pushing
// it to every other listed node (decision 8). The agent side of this wire
// contract is built in parallel, against these same frozen string values,
// and pkg/audio does not yet declare them on this lane's base. Defining
// them here - rather than in pkg/audio, which this lane must not edit -
// keeps this lane and the agent lane from touching the same file for the
// same feature; the orchestrator reconciles these against pkg/audio's own
// constants (once the agent lane adds them) at integration. Only the
// string VALUES are the frozen contract; the Go identifiers are this
// lane's own choice.
const (
	bookmarkKnown      = "bookmarkKnown"
	bookmarkItemId     = "bookmarkItemId"
	bookmarkIndex      = "bookmarkIndex"
	bookmarkPositionMs = "bookmarkPositionMs"
)
