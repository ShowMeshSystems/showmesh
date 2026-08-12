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
	ServerTime     time.Time        `json:"serverTime"`
	LatestEventSeq uint64           `json:"latestEventSeq"`
	Nodes          []node           `json:"nodes"`
	FPP            snapshotFPP      `json:"fpp"`
	Collectors     []collectorState `json:"collectors"`
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
