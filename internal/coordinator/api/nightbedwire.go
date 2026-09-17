package api

// ADR-049 decision 7's multi-node bed wire contract. bookmarkKnown/
// ItemID/Index/PositionMs ride audio.session.pause's result evidence;
// resumeItemId/Index/PositionMs ride audio.session.resume's own params,
// all three present together or none. Only the string VALUES are the
// frozen contract; the Go identifiers are this lane's own choice.
const (
	bookmarkKnown      = "bookmarkKnown"
	bookmarkItemId     = "bookmarkItemId"
	bookmarkIndex      = "bookmarkIndex"
	bookmarkPositionMs = "bookmarkPositionMs"

	resumeItemId     = "resumeItemId"
	resumeIndex      = "resumeIndex"
	resumePositionMs = "resumePositionMs"
)
