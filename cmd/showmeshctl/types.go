package main

import "time"

// The types in this file are showmeshctl's own decoding of the wire shapes
// pinned in the Step 3 design contract, section 6.10. They are transcribed
// independently of internal/coordinator/api's server-side types — see
// doc.go and importgraph_test.go for why that independence is the point.
//
// encoding/json ignores fields it does not recognize by default (no
// json.Decoder.DisallowUnknownFields call anywhere in this package), so a
// coordinator that has added a field since this CLI was built decodes
// cleanly per contract §6.2's additive-only rule. See
// TestDecodeIgnoresUnknownFields.
//
// All optional/absent fields are pointers so "absent" (nil) is
// distinguishable from the JSON zero value, matching the contract's own
// distinction (e.g. a node with no hello observed has label == nil, not
// label == "").

// evidence is the §6.3 observation envelope. It is the shape of every
// observation-bearing field on the wire.
type evidence struct {
	Signal      string     `json:"signal"`
	Value       any        `json:"value"`
	Unit        *string    `json:"unit"`
	State       string     `json:"state"`
	Reason      *string    `json:"reason"`
	ObservedAt  *time.Time `json:"observedAt"`
	CollectedAt time.Time  `json:"collectedAt"`
	Source      string     `json:"source"`
	Quality     string     `json:"quality"`
	// ValidForSeconds is *int64, matching internal/coordinator/api/v1.Evidence
	// and the openapi.yaml schema (an integer, never a fraction of a
	// second) — it used to be declared *float64 here with nothing pinning
	// it against either (Step 3 review finding 4.7). A coordinator that
	// only ever emits whole seconds happened to decode fine either way,
	// which is exactly how a real type mismatch hides.
	ValidForSeconds *int64 `json:"validForSeconds"`
}

// Evidence states, per contract §4 / pkg/observation.State. Reproduced here
// (not imported — see doc.go) because this package renders on the vocabulary
// of the six values, not on the package that defines them.
const (
	stateCurrent          = "current"
	stateStale            = "stale"
	stateUnknownAge       = "unknown_age"
	stateNotCollected     = "not_collected"
	stateCollectionFailed = "collection_failed"
	stateUnsupported      = "unsupported"
)

// capability is one entry of node.capabilities.
type capability struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	Attributes map[string]any `json:"attributes"`
}

// controlPlane is node.controlPlane. Deliberately never called "state" or
// "online" at any call site in this package without the "control plane"
// qualifier — see contract §3.2 and task spec §3 ("offline is not dead").
type controlPlane struct {
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}

// nodeEvidence is node.evidence.
type nodeEvidence struct {
	Hello     evidence `json:"hello"`
	LastWill  evidence `json:"lastWill"`
	Heartbeat evidence `json:"heartbeat"`
}

// node is the Node shape from contract §6.10: an element of GET
// /api/v1/nodes, the body of GET /api/v1/nodes/{id}, an element of the
// snapshot, and the payload of a node.changed stream event.
type node struct {
	NodeID       string     `json:"nodeId"`
	Label        *string    `json:"label"`
	Platform     *string    `json:"platform"`
	AgentVersion *string    `json:"agentVersion"`
	BootID       *string    `json:"bootId"`
	StartedAt    *time.Time `json:"startedAt"`
	// FirstSeenAt and UpdatedAt are non-pointer time.Time, not *time.Time:
	// contract §6.10 and internal/coordinator/api/v1.Node both make them
	// coordinator-inventory bookkeeping that is always set, unlike
	// Label/Platform/AgentVersion/BootID/StartedAt (null only when no hello
	// has ever been observed). Declaring these two optional too was a
	// divergence from the pinned contract with nothing to catch it (Step 3
	// review finding 4.7) — a JSON `null` here still decodes cleanly to the
	// zero time (time.Time.UnmarshalJSON treats "null" as a no-op), so this
	// stays additive-only-tolerant even against a body that violates the
	// "always present" guarantee.
	FirstSeenAt  time.Time    `json:"firstSeenAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	Capabilities []capability `json:"capabilities"`
	ControlPlane controlPlane `json:"controlPlane"`
	Evidence     nodeEvidence `json:"evidence"`

	// Declaration is BUILD-PLAN Step 7 seam B's addition (RES-008 D2/D6),
	// additive per contract §6.2's within-a-major-version rule — decoded
	// here unconditionally rather than treated as optional, since
	// encoding/json's default (no DisallowUnknownFields) already tolerates
	// an OLDER coordinator that predates this field: nodeDeclaration's own
	// zero value (every pointer field nil) is what this program renders
	// for that case, not a special "declaration unknown" branch.
	Declaration nodeDeclaration `json:"declaration"`
}

// nodeDeclaration is node.declaration (RES-008 D2/D6). See
// internal/coordinator/api/v1.NodeDeclaration's own doc comment for what
// each DiscoveryState value means; this program renders the vocabulary
// rather than re-deciding it (format.go's discoveryStateGlyph).
type nodeDeclaration struct {
	Declared bool `json:"declared"`

	Label *string `json:"label"`
	Notes *string `json:"notes"`

	DeclaredAt              *time.Time `json:"declaredAt"`
	DeclaredByPrincipalID   *string    `json:"declaredByPrincipalId"`
	DeclaredByPrincipalName *string    `json:"declaredByPrincipalName"`

	DiscoveryState  string  `json:"discoveryState"`
	DiscoveryReason *string `json:"discoveryReason"`

	// LastDiscoveryRunID/LastDiscoveredAt are this declaration's OWN
	// last-seen-by-discovery bookkeeping (null if it has never once been
	// seen) — never the identity of a run that did NOT see it. See
	// NotSeenAsOfRunID/NotSeenAsOfRunFinishedAt below for that (DEFECT 8).
	LastDiscoveryRunID *string    `json:"lastDiscoveryRunId"`
	LastDiscoveredAt   *time.Time `json:"lastDiscoveredAt"`

	// NotSeenAsOfRunID/NotSeenAsOfRunFinishedAt name the run that did NOT
	// see this declared node — populated only when DiscoveryState is
	// "not_seen". Decoded unconditionally rather than treated as required,
	// matching this file's existing tolerance of an older coordinator that
	// predates them (both simply stay nil).
	NotSeenAsOfRunID         *string    `json:"notSeenAsOfRunId"`
	NotSeenAsOfRunFinishedAt *time.Time `json:"notSeenAsOfRunFinishedAt"`
}

// discoveryRun is one discovery_runs row (BUILD-PLAN Step 7 seam B).
type discoveryRun struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Complete   bool       `json:"complete"`
	Reason     *string    `json:"reason"`
	FoundCount int64      `json:"foundCount"`

	InitiatedByPrincipalID   string `json:"initiatedByPrincipalId"`
	InitiatedByPrincipalName string `json:"initiatedByPrincipalName"`
}

// discoveryProposal is one element of discoveryRunResponse.Proposals: an
// entity the run observed that is not currently declared.
type discoveryProposal struct {
	NodeID string `json:"nodeId"`
	Source string `json:"source"`
}

// discoveryRunResponse is the body of POST /api/v1/discovery/runs.
type discoveryRunResponse struct {
	ServerTime time.Time           `json:"serverTime"`
	Run        discoveryRun        `json:"run"`
	Proposals  []discoveryProposal `json:"proposals"`
}

// declareNodeRequest is the body of POST /api/v1/nodes/{nodeId}/declaration.
// Label/Notes are *string, matching internal/coordinator/api/v1's own
// DeclareNodeRequest (DEFECT 6): nil/omitted leaves that field's currently
// declared value UNCHANGED; a non-nil pointer, including one pointing at
// "", sets it. cmd_discovery.go's cmdDeclare only ever sets a field when
// the corresponding --label/--notes flag was ACTUALLY passed on this
// invocation — see its own doc comment — so this type can represent, and
// this program can now issue, the "leave this field alone" request a
// second `showmeshctl declare` with no flags needs.
type declareNodeRequest struct {
	Label *string `json:"label,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

// nodeDeclarationResponse is the body of POST /api/v1/nodes/{nodeId}/declaration.
type nodeDeclarationResponse struct {
	ServerTime  time.Time       `json:"serverTime"`
	Declaration nodeDeclaration `json:"declaration"`
}

// deleteNodeDeclarationRequest is the required body of
// DELETE /api/v1/nodes/{nodeId}/declaration.
type deleteNodeDeclarationRequest struct {
	Confirm bool `json:"confirm"`
}

// fppInstance is the FPP instance shape from contract §6.10.
type fppInstance struct {
	InstanceID    string     `json:"instanceId"`
	Endpoint      string     `json:"endpoint"`
	Health        string     `json:"health"`
	Observations  []evidence `json:"observations"`
	LastPollAt    *time.Time `json:"lastPollAt"`
	LastPollError *string    `json:"lastPollError"`
}

// resourceRef is event.resource.
type resourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// event is the Event shape from contract §6.10.
//
// Seq is uint64, matching internal/coordinator/api/v1.Event and
// openapi.yaml's schema (an unsigned integer — seq numbers never go
// negative and the store's own type is uint64). Declared int64 here with
// nothing pinning it against either was a Step 3 review finding 4.7
// divergence; every real seq value in range for both types decodes
// identically, which is exactly how the mismatch stayed invisible.
type event struct {
	Seq           uint64         `json:"seq"`
	RecordedAt    time.Time      `json:"recordedAt"`
	OccurredAt    *time.Time     `json:"occurredAt"`
	Source        string         `json:"source"`
	Resource      resourceRef    `json:"resource"`
	Category      string         `json:"category"`
	Severity      string         `json:"severity"`
	Summary       string         `json:"summary"`
	Details       map[string]any `json:"details"`
	CorrelationID *string        `json:"correlationId"`
}

// collectorState is one element of snapshot.collectors.
type collectorState struct {
	ID     string  `json:"id"`
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}

// coordinatorInfo is serviceDescriptor.coordinator.
type coordinatorInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

// serviceDescriptor is the body of GET /api/v1/.
type serviceDescriptor struct {
	ServerTime        time.Time       `json:"serverTime"`
	APIVersion        int             `json:"apiVersion"`
	SupportedVersions []int           `json:"supportedVersions"`
	Coordinator       coordinatorInfo `json:"coordinator"`
}

// nodesResponse is the body of GET /api/v1/nodes.
type nodesResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Nodes      []node    `json:"nodes"`
}

// nodeResponse is the body of GET /api/v1/nodes/{nodeId}: a single node
// wrapped the same way the list wraps its slice, per contract §6.10 (pinned
// during this session's wiring pass specifically to close this gap — every
// single-resource endpoint is wrapped so §6.2's "every response body
// carries serverTime" has no exception). decodeSingleNode in cmd_nodes.go
// decodes strictly against this shape and fails loudly if serverTime or
// the node object is missing, rather than tolerating a bare, unwrapped
// object the way an earlier version of this file did.
type nodeResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Node       node      `json:"node"`
}

// fppResponse is the body of GET /api/v1/fpp.
type fppResponse struct {
	ServerTime time.Time     `json:"serverTime"`
	Instances  []fppInstance `json:"instances"`
}

// fppInstanceResponse is the body of GET /api/v1/fpp/{instanceId}. See
// [nodeResponse]'s doc comment; the same pinned-wrapper rule applies
// identically here.
type fppInstanceResponse struct {
	ServerTime time.Time   `json:"serverTime"`
	Instance   fppInstance `json:"instance"`
}

// eventsResponse is the body of GET /api/v1/events.
//
// Gap and OldestRetainedSeq are not part of the pinned contract §6.10
// shape — this program observed them in a real response body while being
// built alongside the API's implementation (event history is pruned by
// age/row count, so a `since` cursor can legitimately name a point the
// store no longer retains) and decodes them anyway per contract §6.2's
// additive-only rule: an unpinned field the server actually sends is real
// information, and dropping it silently on the client would be exactly
// the kind of omission §6.2 says must not happen ("absent evidence is
// stated, never omitted"). See the report: this is worth pinning in the
// contract properly, not left as something only discovered by reading a
// response body. Both fields default to their zero value (false / nil)
// against a coordinator that predates them, which is a safe default: "no
// gap" is the correct assumption when the field is simply absent.
type eventsResponse struct {
	ServerTime        time.Time `json:"serverTime"`
	Events            []event   `json:"events"`
	LatestSeq         uint64    `json:"latestSeq"`
	Gap               bool      `json:"gap"`
	OldestRetainedSeq *uint64   `json:"oldestRetainedSeq"`
}

// snapshotFPP is snapshot.fpp.
type snapshotFPP struct {
	Instances []fppInstance `json:"instances"`
}

// snapshot is the body of GET /api/v1/snapshot.
type snapshot struct {
	ServerTime     time.Time          `json:"serverTime"`
	LatestEventSeq uint64             `json:"latestEventSeq"`
	Nodes          []node             `json:"nodes"`
	FPP            snapshotFPP        `json:"fpp"`
	Collectors     []collectorState   `json:"collectors"`
	Resolume       []resolumeInstance `json:"resolume"`
}

// resolumeInstanceComposition is ResolumeInstance.composition: the
// composition ShowMesh holds as configuration (ADR-032), never a live read
// of Arena. Null before any composition has ever been uploaded.
type resolumeInstanceComposition struct {
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
	ActivatedAt time.Time `json:"activatedAt"`
}

// resolumeInstance is the ResolumeInstance shape (Track D seam E): an
// element of GET /resolume/instances, the body of GET
// /resolume/instances/{id}, and the payload of a resolume.changed stream
// event.
type resolumeInstance struct {
	InstanceID   string                       `json:"instanceId"`
	Health       string                       `json:"health"`
	Observations []evidence                   `json:"observations"`
	Composition  *resolumeInstanceComposition `json:"composition"`
}

// resolumeInstancesResponse is the body of GET /resolume/instances.
type resolumeInstancesResponse struct {
	ServerTime time.Time          `json:"serverTime"`
	Instances  []resolumeInstance `json:"instances"`
}

// resolumeInstanceResponse is the body of GET
// /resolume/instances/{instanceId}. See [nodeResponse]'s doc comment; the
// same pinned-wrapper rule applies identically here.
type resolumeInstanceResponse struct {
	ServerTime time.Time        `json:"serverTime"`
	Instance   resolumeInstance `json:"instance"`
}

// principalSummary is SessionResponse.principal (ADR-024 decision 1): a
// principal's own non-secret identity. Non-nil only when Authenticated is
// true (openapi.yaml's SessionResponse doc comment).
type principalSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}

// sessionInfo is SessionResponse.session: the session that authenticated
// this request, present only when that credential was the session cookie
// (openapi.yaml SessionInfo). showmeshctl never presents a cookie (see
// cmd_session.go's doc comment on why this CLI is bearer-only), so this is
// decoded for completeness against the wire shape but this program never
// expects it non-nil in practice.
type sessionInfo struct {
	ID          string    `json:"id"`
	DeviceLabel string    `json:"deviceLabel"`
	CreatedAt   time.Time `json:"createdAt"`
}

// sessionResponse is the body of GET /api/v1/session (ADR-024 decisions 5,
// 9, and 12). Authenticated is false whenever no valid credential
// authenticated this request — deliberately not a 401 (see
// openapi.yaml's SessionResponse description: "being signed out" is a
// persistent, readable state). scopesState is "current", "unknown", or
// "not_applicable": decision 12 requires "unknown" be treated exactly
// like an empty scope list, never as permissive — see
// scopesStateGlyph in format.go.
type sessionResponse struct {
	ServerTime        time.Time         `json:"serverTime"`
	Authenticated     bool              `json:"authenticated"`
	Principal         *principalSummary `json:"principal"`
	Session           *sessionInfo      `json:"session"`
	CredentialForm    *string           `json:"credentialForm"`
	Scopes            []string          `json:"scopes"`
	ScopesState       string            `json:"scopesState"`
	BootstrapRequired bool              `json:"bootstrapRequired"`
}

// auditEntry is one element of GET /api/v1/audit (ADR-024 decision 11).
// Params is never null on the wire (an entry with none still reports an
// empty object) but is declared as a plain map here rather than a pointer
// for that reason. Every field here is a plain (non-pointer) string:
// unlike node/fpp fields, the API's own AuditEntry schema marks every
// field required with no null variant, so there is no absent-vs-empty
// distinction this type needs to preserve.
//
// Deliberately carries no row id / cursor field, because the wire shape
// does not have one — see cmd_audit.go's doc comment for why that is a
// known contract limitation this CLI works around rather than papers
// over.
type auditEntry struct {
	Timestamp      time.Time      `json:"timestamp"`
	PrincipalID    string         `json:"principalId"`
	PrincipalName  string         `json:"principalName"`
	Form           string         `json:"form"`
	CredentialID   string         `json:"credentialId"`
	ClientAddr     string         `json:"clientAddr"`
	Action         string         `json:"action"`
	Target         string         `json:"target"`
	Params         map[string]any `json:"params"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Kind           string         `json:"kind"`
	CommandID      string         `json:"commandId"`
	Outcome        string         `json:"outcome"`
	OutcomeState   string         `json:"outcomeState"`
	OutcomeReason  string         `json:"outcomeReason"`
}

// auditResponse is the body of GET /api/v1/audit. Unlike eventsResponse,
// this carries no gap/oldestRetainedSeq-shaped fields on the wire — see
// openapi.yaml's AuditResponse description: "the coordinator's audit
// service currently exposes no oldest-retained cursor for this endpoint
// to report one honestly."
type auditResponse struct {
	ServerTime time.Time    `json:"serverTime"`
	Entries    []auditEntry `json:"entries"`
}

// configFPPEndpoint is one element of configFPPEndpointsPayload.endpoints
// (Step 7 seam A, RES-008 D1): the same (id, url) pair
// SHOWMESH_FPP_ENDPOINTS carries.
type configFPPEndpoint struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// configFPPEndpointsPayload is the "fpp.endpoints" configuration kind's
// payload: the body PUT /api/v1/config/fpp.endpoints accepts, and the
// "payload" member of GET /api/v1/config/fpp.endpoints' response.
type configFPPEndpointsPayload struct {
	Endpoints []configFPPEndpoint `json:"endpoints"`
}

// fppEndpointsConfigResponse is the body of GET and PUT
// /api/v1/config/fpp.endpoints. createdByPrincipalId/createdByPrincipalName
// are null for the one revision the coordinator's startup env->store
// migration creates (source "env_migration") — a startup migration has no
// principal. restartRequired is always false since ADR-036: dispatch
// resolves the endpoint list per request and collectors reconcile within
// about ten seconds, so a change here needs no restart. The field stays on
// the wire because the contract is additive-only within a major version
// (ADR-020).
type fppEndpointsConfigResponse struct {
	ServerTime             time.Time                 `json:"serverTime"`
	Kind                   string                    `json:"kind"`
	Revision               int64                     `json:"revision"`
	Payload                configFPPEndpointsPayload `json:"payload"`
	UpdatedAt              time.Time                 `json:"updatedAt"`
	CreatedByPrincipalID   *string                   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                   `json:"createdByPrincipalName"`
	Source                 string                    `json:"source"`
	RestartRequired        bool                      `json:"restartRequired"`
	RestartRequiredReason  string                    `json:"restartRequiredReason"`
}

// configRevisionMeta is one element of configRevisionsResponse.revisions:
// a config revision's metadata, without its payload (Step 7 seam A:
// rollback tooling is deliberately out of scope, so this CLI has no way to
// fetch a PAST revision's payload either, only the active one via
// `showmeshctl config get`).
type configRevisionMeta struct {
	Revision               int64     `json:"revision"`
	CreatedAt              time.Time `json:"createdAt"`
	CreatedByPrincipalID   *string   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string   `json:"createdByPrincipalName"`
	Source                 string    `json:"source"`
	Note                   string    `json:"note"`
	Active                 bool      `json:"active"`
}

// configRevisionsResponse is the body of GET
// /api/v1/config/fpp.endpoints/revisions: every revision, newest first.
type configRevisionsResponse struct {
	ServerTime time.Time            `json:"serverTime"`
	Kind       string               `json:"kind"`
	Revisions  []configRevisionMeta `json:"revisions"`
}

// Stream frame payloads (contract §6.4/§6.10). streamEnvelope is decoded
// first, from the frame's "event:" line and raw "data:" bytes; the
// concrete payload is then decoded from the same bytes by event type.

// streamStart is the payload of a stream.start frame.
type streamStart struct {
	StreamID         string    `json:"streamId"`
	APIVersion       int       `json:"apiVersion"`
	ServerTime       time.Time `json:"serverTime"`
	SnapshotRequired bool      `json:"snapshotRequired"`
}

// streamNodeChanged is the payload of a node.changed frame.
type streamNodeChanged struct {
	Seq        uint64    `json:"seq"`
	ServerTime time.Time `json:"serverTime"`
	Node       node      `json:"node"`
}

// streamFPPChanged is the payload of an fpp.changed frame.
type streamFPPChanged struct {
	Seq        uint64      `json:"seq"`
	ServerTime time.Time   `json:"serverTime"`
	Instance   fppInstance `json:"instance"`
}

// streamEventRecorded is the payload of an event.recorded frame.
type streamEventRecorded struct {
	Seq        uint64    `json:"seq"`
	ServerTime time.Time `json:"serverTime"`
	Event      event     `json:"event"`
}

// streamReset is the payload of a stream.reset frame.
type streamReset struct {
	Seq              uint64    `json:"seq"`
	ServerTime       time.Time `json:"serverTime"`
	Reason           string    `json:"reason"`
	SnapshotRequired bool      `json:"snapshotRequired"`
}

// streamFPPObservationsChanged is the payload of an
// "fpp.observations.changed" frame (ADR-023), delivered only to a stream
// connection opened with --deltas (GET /api/v1/stream?deltas=1 — see
// cmdWatch). Changed and Removed are decoded exactly as documented rather
// than treated as optional: contract convention for this API is that a
// collection field is never omitted, only empty (an absent-vs-empty
// distinction this program does not need to make for a bare string slice
// or an evidence slice the way it does for a genuinely optional pointer
// field elsewhere in this file).
type streamFPPObservationsChanged struct {
	Seq        uint64     `json:"seq"`
	ServerTime time.Time  `json:"serverTime"`
	InstanceID string     `json:"instanceId"`
	Changed    []evidence `json:"changed"`
	Removed    []string   `json:"removed"`
}

// fppCommandRequest is the body of POST /api/v1/fpp/{instanceId}/commands
// (Step 7 seam C, Step 8). IdempotencyKey is minted by this program, once
// per invocation, never by the coordinator — RES-015 section 7.3: FPP
// supplies nothing to derive one from, so the caller mints it. This
// program does not import pkg/command.NewIdempotencyKey for the identical
// reason it decodes every wire type independently rather than sharing
// pkg/observation's: see importgraph_test.go and doc.go — this CLI's
// whole point is to keep the coordinator's own JSON tag renames from
// silently renaming both sides of a shared struct. Minting its own random
// value here (see cmd_fpp_command.go's newIdempotencyKey) costs nothing
// and keeps that independence real for a value this program SENDS, not
// merely one it decodes.
//
// Params is Step 8's own addition (docs/bench/fpp-command-vocabulary.md
// section 4): five of the eight primitives take none, and for those this
// program leaves Params nil, which "params,omitempty" turns into an
// OMITTED "params" key on the wire — never an explicit "null" and never an
// explicit "{}" this program did not intend to send. The three
// parameter-taking primitives (startPlaylist, stopPlaylistGracefully,
// setVolume) always send every one of their own parameters, defaulted
// values included, since this program's flags always resolve to a
// concrete value whether or not the operator passed the flag explicitly —
// see api/openapi.yaml's FPPCommandRequest.params description for why an
// explicitly-sent default and an omitted key are treated identically by
// the coordinator's own decode (fppcommand_primitives.go's
// decodeFPPCommandParams), so there is no behavioral difference, only a
// simpler client.
type fppCommandRequest struct {
	Action         string         `json:"action"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Params         map[string]any `json:"params,omitempty"`
}

// fppCommandResponse is the body of a successful response from
// POST /api/v1/fpp/{instanceId}/commands.
type fppCommandResponse struct {
	ServerTime time.Time        `json:"serverTime"`
	Command    fppCommandResult `json:"command"`
}

// fppCommandResult mirrors v1.FPPCommandResult field for field — see that
// type's doc comment in internal/coordinator/api/v1/commands.go for what
// each field means; this is this program's own independent transcription
// of it, per this file's own doc comment.
type fppCommandResult struct {
	ID                  string     `json:"id"`
	IdempotencyKey      string     `json:"idempotencyKey"`
	Action              string     `json:"action"`
	InstanceID          string     `json:"instanceId"`
	Replay              bool       `json:"replay"`
	Outcome             string     `json:"outcome"`
	OutcomeState        string     `json:"outcomeState"`
	OutcomeReason       string     `json:"outcomeReason"`
	AttributionDegraded bool       `json:"attributionDegraded"`
	DispatchedAt        *time.Time `json:"dispatchedAt"`
	ResolvedAt          *time.Time `json:"resolvedAt"`
}
