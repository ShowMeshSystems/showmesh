package v1

import "encoding/json"

// This file is the playlist definition publication wire types:
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3.3 (request), §3.4 (response),
// §3.6 (read-back), and TRACK-H-H2-SPEC.md §4 step 2/§4.1 (entries
// preview, an H2-owned addition the contract does not itself fix).

// FPPPlaylistDefinitionPublishRequest is the body of
// POST /api/v1/integrations/fpp/playlist-definitions, contract §3.3,
// schema version 1. Definition is carried as raw JSON, never decoded into
// a Go structure this package would have to keep in sync with FPP's own
// shape: contract §3.3 requires "the JSON value itself ... No member
// removed," and only [pkg/fppidentity.Canonicalize] is authoritative over
// what that value canonicalizes to.
type FPPPlaylistDefinitionPublishRequest struct {
	SchemaVersion    int             `json:"schemaVersion"`
	InstanceUUID     string          `json:"instanceUuid"`
	PlaylistName     string          `json:"playlistName"`
	PlaylistHash     string          `json:"playlistHash"`
	Definition       json.RawMessage `json:"definition"`
	CapturedAtMillis int64           `json:"capturedAtMillis"`
}

// FPPPlaylistDefinitionPublishResponse is POST's 200 response. Stored is
// true only when this call actually inserted a new row; Idempotent is its
// inverse, carried explicitly rather than left for a client to compute,
// matching [FPPPlaylistEntryObservationResponse]'s identical
// Accepted/Replay convention. PlaylistHash always echoes the hash the
// COORDINATOR computed (contract §3.1: "the bytes the plugin hashed are
// the bytes the coordinator imports," never the caller's bare claim) —
// on success this is definitionally equal to the request's own
// playlistHash, since a disagreement is refused before storage.
type FPPPlaylistDefinitionPublishResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstanceUUID  string `json:"instanceUuid"`
	PlaylistHash  string `json:"playlistHash"`
	Stored        bool   `json:"stored"`
	Idempotent    bool   `json:"idempotent"`
	ServerTime    string `json:"serverTime"`
}

// FPPPlaylistDefinitionMetadata is one row of GET
// /integrations/fpp/playlist-definitions, contract §3.6: "metadata only
// ... instance, playlist name, hash, captured and received times, entry
// count, and whether a stored show.playlist references it." EntryCount
// is the number of entries TRACK-H-H2-SPEC.md §4.1's parser finds across
// leadIn, mainPlaylist, and leadOut combined.
type FPPPlaylistDefinitionMetadata struct {
	InstanceUUID string `json:"instanceUuid"`
	PlaylistName string `json:"playlistName"`
	PlaylistHash string `json:"playlistHash"`
	CapturedAt   string `json:"capturedAt"`
	ReceivedAt   string `json:"receivedAt"`
	EntryCount   int    `json:"entryCount"`
	Referenced   bool   `json:"referenced"`
}

// FPPPlaylistDefinitionsListResponse is GET
// /integrations/fpp/playlist-definitions' body: every stored definition's
// metadata, newest received first.
type FPPPlaylistDefinitionsListResponse struct {
	Definitions []FPPPlaylistDefinitionMetadata `json:"definitions"`
	ServerTime  string                          `json:"serverTime"`
}

// FPPPlaylistDefinitionResponse is GET
// /integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}'s
// body: the stored definition itself. Definition is the CANONICAL bytes
// the coordinator stored (H2 spec §3), not a re-serialization of some
// other in-memory form.
type FPPPlaylistDefinitionResponse struct {
	InstanceUUID string          `json:"instanceUuid"`
	PlaylistName string          `json:"playlistName"`
	PlaylistHash string          `json:"playlistHash"`
	Definition   json.RawMessage `json:"definition"`
	CapturedAt   string          `json:"capturedAt"`
	ReceivedAt   string          `json:"receivedAt"`
	ServerTime   string          `json:"serverTime"`
}

// FPPPlaylistDefinitionEntry is one parsed entry, H2 spec §4.1: a
// section/position pair (each section positioned from zero
// independently, matching the entry-key derivation's own five-field
// identity) plus whatever type/sequenceName/mediaName the definition
// happened to carry at that slot. All three of Type/SequenceName/
// MediaName may be empty — "an entry with no filenames is still an
// entry" (H2 spec §4.1) — so this struct never omits them from the
// response the way `omitempty` would; a client should not have to guess
// "field absent" from "field present, empty".
type FPPPlaylistDefinitionEntry struct {
	Section      string `json:"section"`
	Position     int    `json:"position"`
	Type         string `json:"type"`
	SequenceName string `json:"sequenceName"`
	MediaName    string `json:"mediaName"`
}

// FPPPlaylistDefinitionEntriesResponse is GET
// /integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}/entries' body:
// leadIn, then mainPlaylist, then leadOut, in that fixed order (H2 spec
// §4 step 2), each section positioned independently from zero.
type FPPPlaylistDefinitionEntriesResponse struct {
	InstanceUUID string                       `json:"instanceUuid"`
	PlaylistHash string                       `json:"playlistHash"`
	Entries      []FPPPlaylistDefinitionEntry `json:"entries"`
	ServerTime   string                       `json:"serverTime"`
}
