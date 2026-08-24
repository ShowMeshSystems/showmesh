package v1

// This file is the playlist-entry observation wire types: FPP-PLUGIN-COORDINATOR-CONTRACTS.md
// §1.2 (request) and §1.6 (response). Position uses *int rather than a
// bare int because position 0 is a real, valid first-slot position, and
// the contract requires distinguishing "absent" from "zero" (§1.2, §1.6
// step 7).

// FPPPlaylistEntryObservationRequest is the body of
// POST /api/v1/integrations/fpp/playlist-entry-observations, contract
// §1.2, schema version 1. Sequence and CoalescedSincePreviousAcknowledged
// are plain int64, decoded as signed rather than unsigned, so a negative
// value reaches step 7's own audited "refuse 400" check instead of
// surfacing as an unaudited JSON decode error.
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

	// EndpointID is the configured fpp.endpoints id whose most
	// recently observed instance uuid matches InstanceUUID, resolved
	// best-effort at read time, the correlation this API previously had
	// no way to state (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.5: "no
	// binding between the authenticated principal and the instanceUuid it
	// reports for"). Null when no currently configured endpoint has
	// reported this uuid, or when more than one has (a duplicate, see
	// GET /fpp's duplicateInstanceUuidEndpointIds, makes the
	// correlation ambiguous, so this is null rather than an arbitrary
	// pick).
	EndpointID *string `json:"endpointId"`
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
