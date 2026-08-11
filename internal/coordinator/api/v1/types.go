package v1

// Evidence is the wire envelope every observation-bearing field uses, per
// the Step 3 contract section 6.3. It is the JSON projection of
// [github.com/showmeshsystems/showmesh/pkg/observation.Observation] (see
// that package's Observation and State for the domain rules this merely
// renders) plus the Signal identifying which piece of evidence this is,
// since an Evidence often travels outside the context that would
// otherwise name it (e.g. a flat entry in the /api/v1/observations list).
//
// Value is null for every absence state. ObservedAt is null whenever the
// observation time is unknown — a retained MQTT delivery, or any absence —
// and this package's mapping code must never fill it from CollectedAt; see
// pkg/observation's package doc comment for why that specific shortcut is
// the bug ADR-011 exists to prevent, one layer up from where that package
// itself guards against it. Reason is non-null whenever State is not
// "current".
type Evidence struct {
	Signal string `json:"signal"`

	// Value is one of bool, string, int64, or float64 on the wire, or null
	// for any absence state. encoding/json renders a Go `any` holding one
	// of those exactly as expected; nothing here needs to special-case the
	// type.
	Value any     `json:"value"`
	Unit  *string `json:"unit"`

	// State is one of the six values pkg/observation.State names:
	// "current", "stale", "unknown_age", "not_collected",
	// "collection_failed", "unsupported".
	State  string  `json:"state"`
	Reason *string `json:"reason"`

	// ObservedAt is an RFC 3339 timestamp with explicit offset, or null when
	// the observation time is unknown. NEVER defaulted to CollectedAt.
	ObservedAt *string `json:"observedAt"`

	// CollectedAt is the coordinator's own bookkeeping of when this
	// evidence was recorded, never evidence of the subject's state (see
	// pkg/observation.Observation.CollectedAt) — and null whenever State
	// is "not_collected": that state means no collection attempt has ever
	// been made, so there is no collection time to report, and rendering
	// one anyway would be the exact ObservedAt fabrication contract
	// section 3.3 already forbids, one field over (Step 3 review finding
	// 3.6). Non-null for every other state, including "collection_failed"
	// and "unknown_age", where a collection attempt genuinely happened at
	// the time given even though it produced no current value.
	CollectedAt *string `json:"collectedAt"`

	Source  string `json:"source"`
	Quality string `json:"quality"`

	// ValidForSeconds is null when the underlying observation's ValidFor is
	// zero (the value does not expire on its own — see
	// pkg/observation.Observation.ValidFor) and a number of seconds
	// otherwise. It is never a Go duration string; contract section 7
	// requires wire durations to be plain numbers named "...Seconds".
	ValidForSeconds *int64 `json:"validForSeconds"`
}

// ResourceRef identifies the subject of an [Event] or an
// [ObservationEntry]. It is the wire projection of
// pkg/observation.ResourceRef — kept as its own type for the same reason
// [Evidence] is not pkg/observation.Observation directly; see this
// package's doc comment.
type ResourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Capability is one entry of a node's advertised capability set, per
// ADR-002. It mirrors pkg/capability.Capability's JSON shape; kept
// separate for the same reason every wire type in this package is kept
// separate from its domain source.
type Capability struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// ControlPlane is a node's control-plane liveness verdict. Contract
// section 3.2, pinned by name: this field is deliberately never
// "node.state", "node.online", or "node.status" anywhere in this package,
// because "offline" here means the MQTT control-plane connection is gone —
// not that the node, or the show it may still be running, is dead. A
// running show survives coordinator loss and broker loss (ADR-008); a
// client that renders State as "the node is dead" has to ignore this
// field's own name to reach that conclusion.
type ControlPlane struct {
	// State is one of "online", "offline", "unknown".
	State string `json:"state"`

	// Reason is non-null whenever State is not "online".
	Reason *string `json:"reason"`
}

// NodeEvidence carries a node's three evidence sources — capability
// advertisement, control-plane last will, and health heartbeat — each
// rendered through the [Evidence] envelope. See the mapping code in the
// parent api package for how each is derived from
// internal/coordinator/store's HelloRecord/LWTRecord/HealthRecord, none of
// which is an observation.Observation on the domain side; the mapping
// constructs one so all three evidence kinds — these, and an FPP
// instance's collected observations — go through exactly one envelope
// rule.
type NodeEvidence struct {
	Hello     Evidence `json:"hello"`
	LastWill  Evidence `json:"lastWill"`
	Heartbeat Evidence `json:"heartbeat"`
}

// Node is one node's current representation: an element of
// GET /api/v1/nodes, of the /api/v1/snapshot nodes list, and the payload of
// a node.changed stream event. All three render identically, per contract
// section 6.4 ("each *.changed event carries the resource's full current
// representation, identical in shape to its element in the snapshot").
//
// Label, Platform, AgentVersion, BootID, and StartedAt are null when no
// hello has ever been observed for this node: a node can exist in
// inventory from a heartbeat or last-will message alone (see
// internal/coordinator/store's NodeRecord doc comment), and this package
// must not invent placeholder values for the fields that come only from a
// hello.
type Node struct {
	NodeID       string  `json:"nodeId"`
	Label        *string `json:"label"`
	Platform     *string `json:"platform"`
	AgentVersion *string `json:"agentVersion"`
	BootID       *string `json:"bootId"`
	StartedAt    *string `json:"startedAt"`

	// FirstSeenAt and UpdatedAt are always set: coordinator bookkeeping of
	// when the store row was created and last touched, not observation
	// evidence. See internal/coordinator/store.NodeRecord's doc comment.
	FirstSeenAt string `json:"firstSeenAt"`
	UpdatedAt   string `json:"updatedAt"`

	// Capabilities is never null: an empty node still reports an empty
	// array, per contract section 6.2's "absent evidence is stated, never
	// omitted" — the same reasoning applied to a collection rather than a
	// scalar field.
	Capabilities []Capability `json:"capabilities"`

	ControlPlane ControlPlane `json:"controlPlane"`
	Evidence     NodeEvidence `json:"evidence"`
}

// FPPInstance is one configured FPP instance's current representation: an
// element of GET /api/v1/fpp, and the payload of an fpp.changed stream
// event.
//
// Endpoint never includes userinfo (a URL of the form
// "http://user:pass@host" with credentials embedded); the mapping code
// strips them before this struct is ever populated, per contract section
// 6.10.
type FPPInstance struct {
	InstanceID string `json:"instanceId"`
	Endpoint   string `json:"endpoint"`

	// Health is one of pkg/observation.Health's five values. See the
	// mapping code's doc comment for how little basis Step 3 has to report
	// anything other than "unknown" or "healthy" here, per the shared
	// contract section 4's closing note.
	Health string `json:"health"`

	// Observations is never null: an instance with nothing collected yet
	// still reports an empty array (or, more precisely, one Evidence per
	// configured signal with state "not_collected" — see the mapping
	// code), never an absent field.
	Observations []Evidence `json:"observations"`

	LastPollAt    *string `json:"lastPollAt"`
	LastPollError *string `json:"lastPollError"`
}

// Event is one recorded event: an element of GET /api/v1/events, and the
// payload of an event.recorded stream event.
//
// OccurredAt is null when the change was first learned from evidence of
// unknown age (contract section 6.10) — the event log's own version of the
// same "never fabricate a timestamp" rule [Evidence.ObservedAt] follows.
type Event struct {
	Seq        uint64      `json:"seq"`
	RecordedAt string      `json:"recordedAt"`
	OccurredAt *string     `json:"occurredAt"`
	Source     string      `json:"source"`
	Resource   ResourceRef `json:"resource"`
	Category   string      `json:"category"`

	// Severity is one of "informational", "warning", "critical", per
	// OBSERVABILITY section 11.2.
	Severity string `json:"severity"`
	Summary  string `json:"summary"`

	// Details is never null: an event with no structured detail still
	// reports an empty object, matching contract section 6.10's pinned
	// example ("details": {}).
	Details map[string]any `json:"details"`

	CorrelationID *string `json:"correlationId"`
}

// ObservationEntry is one element of GET /api/v1/observations: a flat list
// spanning every resource, so — unlike [FPPInstance.Observations], which is
// scoped to one already-identified instance — each entry must carry its
// own [ResourceRef]. Evidence is embedded so the entry's JSON is the
// resource plus a flattened evidence envelope at the same level, rather
// than a nested "evidence" object; this endpoint's shape is not pinned by
// contract section 6.10 (only the endpoint's existence and its filters
// are), so this is Task D's own reasonable choice — see the api package's
// report for why.
type ObservationEntry struct {
	Resource ResourceRef `json:"resource"`
	Evidence
}

// CollectorStatus is one collector's own run state, an element of the
// /api/v1/snapshot "collectors" list.
//
// State is one of a small, closed run-state vocabulary this API defines
// for itself — see internal/coordinator/api.CollectorRunState —
// deliberately distinct from pkg/observation.State (the vocabulary
// [Evidence] uses): a collector's run state answers "is this collector's
// poll loop registered and executing", never "is the most recent thing it
// collected current" — that question belongs to the resource's own
// Health/Evidence (e.g. FPPInstance.Health), so "running" here is a
// normal, expected value even while every one of an instance's
// observations reads collection_failed, not a claim that anything is
// actually healthy. Before this was a closed vocabulary, this field mixed
// the two: a collector with nothing configured reported "not_collected",
// an evidence-absence state borrowed from the wrong vocabulary, while a
// configured one reported the bare string "running" with no enum backing
// it at all (Step 3 review finding 3.7).
//
// ID is stable for a given collector regardless of how many resources it
// polls or how its configuration changes shape: the FPP REST collector
// always reports id "fpp-rest", exactly one row, whether it currently has
// zero or many configured instances — see
// internal/coordinator/apiwiring.go's fppCollectorStatusLister, which used
// to emit one row per configured endpoint (an ID that changed shape with
// configuration, i.e. no stable identity at all) before this fix.
type CollectorStatus struct {
	ID     string  `json:"id"`
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}

// CoordinatorInfo is the coordinator build metadata reported by
// GET /api/v1/. It mirrors internal/version's fields plus the Go runtime
// version, matching what /version already reports (see
// internal/coordinator/httpapi) — duplicated here rather than shared
// because /version is an infrastructure probe outside the versioned
// contract (contract section 6.1) and must not change shape to serve this
// package's needs.
type CoordinatorInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

// ServiceDescriptor is the body of GET /api/v1/.
type ServiceDescriptor struct {
	ServerTime        string          `json:"serverTime"`
	APIVersion        int             `json:"apiVersion"`
	SupportedVersions []int           `json:"supportedVersions"`
	Coordinator       CoordinatorInfo `json:"coordinator"`
}

// NodesResponse is the body of GET /api/v1/nodes.
type NodesResponse struct {
	ServerTime string `json:"serverTime"`
	Nodes      []Node `json:"nodes"`
}

// FPPSection is the "fpp" member of [Snapshot], grouping FPP instances the
// way the top-level /api/v1/fpp response does, so the snapshot's shape
// mirrors the per-resource list endpoints it stands in for.
type FPPSection struct {
	Instances []FPPInstance `json:"instances"`
}

// FPPResponse is the body of GET /api/v1/fpp.
type FPPResponse struct {
	ServerTime string        `json:"serverTime"`
	Instances  []FPPInstance `json:"instances"`
}

// NodeResponse is the body of GET /api/v1/nodes/{nodeId}. A single-resource
// response is wrapped exactly like a collection response, never a bare
// resource object, because contract section 6.2 requires serverTime on
// every response with no exception: a client computes evidence ages
// against serverTime, so a response missing it forces every client to
// either fabricate a time or silently misreport how fresh a show's
// evidence is.
type NodeResponse struct {
	ServerTime string `json:"serverTime"`
	Node       Node   `json:"node"`
}

// FPPInstanceResponse is the body of GET /api/v1/fpp/{instanceId}. See
// [NodeResponse]'s doc comment; the same rule applies identically here.
type FPPInstanceResponse struct {
	ServerTime string      `json:"serverTime"`
	Instance   FPPInstance `json:"instance"`
}

// ObservationsResponse is the body of GET /api/v1/observations.
type ObservationsResponse struct {
	ServerTime   string             `json:"serverTime"`
	Observations []ObservationEntry `json:"observations"`
}

// EventsResponse is the body of GET /api/v1/events. LatestSeq is the
// highest seq the coordinator has ever recorded, independent of Events
// (which may be an empty or short page); a client compares LatestSeq
// against the last seq it has already applied to know whether it is
// caught up, exactly the way [Snapshot.LatestEventSeq] does for the first
// fetch.
//
// Gap and OldestRetainedSeq exist because the store prunes event history
// by age and row count, so a caller's `since` cursor can legitimately name
// a point before anything the store still retains. Gap is always present:
// true means events between the request's `since` and OldestRetainedSeq
// existed and are gone for good, and Events is therefore an incomplete
// answer for that interval — never treat a page with Gap true as a
// complete history, and never retry expecting the gap to close, because it
// cannot. OldestRetainedSeq is the lowest seq still in the store, or nil
// when the store currently retains no events at all. This is a
// successful, 200 response either way: a gap describes an incomplete
// answer, it is not a failure to answer, so it must never become a 4xx —
// see the events handler in handlers.go.
type EventsResponse struct {
	ServerTime        string  `json:"serverTime"`
	Events            []Event `json:"events"`
	LatestSeq         uint64  `json:"latestSeq"`
	Gap               bool    `json:"gap"`
	OldestRetainedSeq *uint64 `json:"oldestRetainedSeq"`
}

// Snapshot is the body of GET /api/v1/snapshot: the authoritative state
// the SSE stream's deltas are relative to, per contract section 6.1.
// LatestEventSeq lets a client fetch this snapshot and then request
// exactly the events after it via GET /api/v1/events?since=, with no gap
// and no duplicate.
type Snapshot struct {
	ServerTime     string            `json:"serverTime"`
	LatestEventSeq uint64            `json:"latestEventSeq"`
	Nodes          []Node            `json:"nodes"`
	FPP            FPPSection        `json:"fpp"`
	Collectors     []CollectorStatus `json:"collectors"`
}

// Problem is the RFC 9457 application/problem+json body every error in
// this API uses, per contract section 6.6. SupportedVersions is the one
// per-class extension member Step 3 needs (for
// "unsupported-api-version"); it is omitted from the JSON entirely
// (omitempty) for every other problem class rather than rendered as null,
// since — unlike an [Evidence] field — this is a member that simply does
// not apply to those classes, not an absent piece of evidence about a
// resource this API models.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`

	// ServerTime is an RFC 9457 extension member, present on every problem
	// document this API emits with no exception — contract section 6.2
	// says "every response body carries serverTime" and names no carve-out
	// for an error response; [writeProblem] is what actually enforces
	// this, not this field's mere existence.
	ServerTime string `json:"serverTime"`

	SupportedVersions []int `json:"supportedVersions,omitempty"`
}

// StreamStart is the payload of the first event on every SSE connection,
// "stream.start". SnapshotRequired is always true; it is a field rather
// than an implicit rule so the wire contract states outright, for a reader
// holding only api/openapi.yaml, that a client must fetch
// GET /api/v1/snapshot before applying anything from this stream.
type StreamStart struct {
	StreamID         string `json:"streamId"`
	APIVersion       int    `json:"apiVersion"`
	ServerTime       string `json:"serverTime"`
	SnapshotRequired bool   `json:"snapshotRequired"`
}

// StreamReset is the payload of a "stream.reset" event: either an
// overflowed subscriber buffer (Reason "subscriber_too_slow", connection
// closed immediately after) or any other condition that makes a client's
// local model unsafe to keep applying deltas to. SnapshotRequired is
// always true, for the same reason as [StreamStart.SnapshotRequired].
type StreamReset struct {
	Seq              uint64 `json:"seq"`
	ServerTime       string `json:"serverTime"`
	Reason           string `json:"reason"`
	SnapshotRequired bool   `json:"snapshotRequired"`
}

// NodeChangedEvent is the payload of a "node.changed" SSE event: the
// node's full current representation, identical in shape to its element
// in [NodesResponse] and [Snapshot].
type NodeChangedEvent struct {
	Seq        uint64 `json:"seq"`
	ServerTime string `json:"serverTime"`
	Node       Node   `json:"node"`
}

// FPPChangedEvent is the payload of an "fpp.changed" SSE event.
type FPPChangedEvent struct {
	Seq        uint64      `json:"seq"`
	ServerTime string      `json:"serverTime"`
	Instance   FPPInstance `json:"instance"`
}

// EventRecordedEvent is the payload of an "event.recorded" SSE event.
type EventRecordedEvent struct {
	Seq        uint64 `json:"seq"`
	ServerTime string `json:"serverTime"`
	Event      Event  `json:"event"`
}
