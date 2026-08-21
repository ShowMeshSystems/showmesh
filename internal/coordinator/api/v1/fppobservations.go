package v1

// This file is the playlist-entry observation wire types: FPP-PLUGIN-COORDINATOR-CONTRACTS.md
// §1.2 (request) and §1.6 (response). Optional identity fields
// (playlistName, playlistHash, section, sequenceFilename, mediaFilename,
// entryKey, unavailable) use omitempty on the request, §1.2's own table
// says they are "permitted to be absent when [unavailable] is present"
// but position and coalescedSincePreviousAcknowledged use *int/*uint32
// rather than a bare int/int32: the contract requires distinguishing
// "absent" from "zero" for both (position 0 is a real, valid first-slot
// position; coalescedSincePreviousAcknowledged 0 is a real, valid "no
// gap"), and a bare zero-value int cannot make that distinction on the
// wire.

// FPPPlaylistEntryObservationRequest is the body of
// POST /api/v1/integrations/fpp/playlist-entry-observations, contract
// §1.2, schema version 1.
//
// Position is *int, not int: it is one of the five identity fields
// ("required when unavailable is absent, permitted to be absent when it
// is present", §1.2), and position 0 is a real, valid first-slot
// position, so a bare int cannot distinguish "absent" from "the first
// entry". Sequence and CoalescedSincePreviousAcknowledged are plain int64:
// both are present on EVERY observation regardless of unavailable (§1.2's
// own note), so there is no absent case for either to distinguish from
// zero, decoding both as signed (never unsigned) is what lets step 7's
// "refuse 400 when...negative" check run as this handler's own audited
// validation rather than surfacing as an unaudited JSON decode error.
type FPPPlaylistEntryObservationRequest struct {
	SchemaVersion                      int    `json:"schemaVersion"`
	InstanceUUID                       string `json:"instanceUuid"`
	PlaylistName                       string `json:"playlistName,omitempty"`
	PlaylistHash                       string `json:"playlistHash,omitempty"`
	Section                            string `json:"section,omitempty"`
	Position                           *int   `json:"position,omitempty"`
	EntryKey                           string `json:"entryKey,omitempty"`
	SequenceFilename                   string `json:"sequenceFilename,omitempty"`
	MediaFilename                      string `json:"mediaFilename,omitempty"`
	Action                             string `json:"action"`
	Sequence                           int64  `json:"sequence"`
	ObservedAtMillis                   int64  `json:"observedAtMillis"`
	CoalescedSincePreviousAcknowledged int64  `json:"coalescedSincePreviousAcknowledged"`
	Unavailable                        string `json:"unavailable,omitempty"`
}

// FPPPlaylistEntryObservationResponse is POST's 200 response, contract
// §1.6 step 10: what was decided, never a copy of the request body, a
// caller confirms what was actually stored (or, on replay, what already
// was) by re-fetching via GET, not by trusting its own echoed input.
type FPPPlaylistEntryObservationResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstanceUUID  string `json:"instanceUuid"`
	Sequence      int64  `json:"sequence"`
	EntryKey      string `json:"entryKey"`
	// Accepted is true when this observation changed the stored state;
	// false only for an idempotent replay (§1.6 step 9's "equal sequence,
	// identical canonical body" case), which stores nothing and publishes
	// nothing.
	Accepted bool `json:"accepted"`
	// Replay is Accepted's inverse, carried explicitly rather than left
	// for a client to compute, matching this API's standing "state the
	// fact, never make the client derive it" convention.
	Replay     bool   `json:"replay"`
	ServerTime string `json:"serverTime"`
}

// FPPPlaylistEntryObservation is one instance's latest accepted
// observation, as GET /api/v1/integrations/fpp/playlist-entry-observations
// and the fppPlaylistEntry.changed stream event both render it, the full
// stored record, not merely the acceptance receipt
// [FPPPlaylistEntryObservationResponse] gives POST.
type FPPPlaylistEntryObservation struct {
	InstanceUUID                       string `json:"instanceUuid"`
	SchemaVersion                      int    `json:"schemaVersion"`
	Sequence                           int64  `json:"sequence"`
	PlaylistName                       string `json:"playlistName,omitempty"`
	PlaylistHash                       string `json:"playlistHash,omitempty"`
	Section                            string `json:"section,omitempty"`
	Position                           *int   `json:"position,omitempty"`
	EntryKey                           string `json:"entryKey,omitempty"`
	SequenceFilename                   string `json:"sequenceFilename,omitempty"`
	MediaFilename                      string `json:"mediaFilename,omitempty"`
	Action                             string `json:"action"`
	Unavailable                        string `json:"unavailable,omitempty"`
	ObservedAt                         string `json:"observedAt"`
	CoalescedSincePreviousAcknowledged int64  `json:"coalescedSincePreviousAcknowledged"`
	ReceivedAt                         string `json:"receivedAt"`
}

// FPPPlaylistEntryObservationsResponse is GET's list response: the latest
// accepted observation for every known instance, ordered as the store
// returns them (instance_uuid ascending).
type FPPPlaylistEntryObservationsResponse struct {
	Observations []FPPPlaylistEntryObservation `json:"observations"`
	ServerTime   string                        `json:"serverTime"`
}

// FPPPlaylistEntryChangedEvent is the payload of an
// "fppPlaylistEntry.changed" SSE event: the latest observation for one
// instance, full-frame only, no ADR-023 delta narrowing (build brief
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1).
type FPPPlaylistEntryChangedEvent struct {
	Seq         uint64                      `json:"seq"`
	ServerTime  string                      `json:"serverTime"`
	Observation FPPPlaylistEntryObservation `json:"observation"`
}
