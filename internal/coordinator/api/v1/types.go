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

	// Declaration is RES-008 D2/D6's declared-versus-observed split
	// (BUILD-PLAN Step 7 seam B), added additively per ADR-020 decision 8:
	// an existing client that has never heard of it keeps working
	// unchanged. Always present, even for a node nobody has ever declared
	// — see [NodeDeclaration]'s own doc comment for what "declared: false"
	// means and why this is never an omitted field.
	Declaration NodeDeclaration `json:"declaration"`

	// Render is Track B seam B2b's addition, additive per ADR-020 decision
	// 8: whatever surface.* observations this coordinator currently holds
	// for the render pipeline(s) this node has reported, one
	// [ObservationEntry] per signal. Never null — an empty array means this
	// node has never published a render report, not that the field was
	// omitted. Each entry's resource names the SURFACE it concerns
	// (ADR-026: a surface, not the node running it, is the thing being
	// observed), so entries here may span more than one surface once a
	// node runs N>1, and are not necessarily contiguous with any other
	// node's entries in GET /api/v1/observations.
	Render []ObservationEntry `json:"render"`

	// Audio is Track C seam C1a/C1b's addition, additive per ADR-020
	// decision 8: whatever node.audio.* observations this coordinator
	// currently holds for this node, one [ObservationEntry] per signal.
	// Never null — an empty array means this node has never published an
	// audio discovery report.
	Audio []ObservationEntry `json:"audio"`

	// FPPConnect is an addition, additive per ADR-020 decision 8:
	// whatever node.fppconnect.channel_range.* observations this
	// coordinator currently holds for this node's most recently resolved
	// fppconnect.configure push — whether the pushed channel range was
	// formatted, legitimately empty (no configured surface), or dropped
	// (a surface existed but could not be formatted, e.g. a refused range
	// or a string too long for the ping's 120-byte field), and why. Never
	// null — an empty array means this node has never had a
	// fppconnect.configure push resolved for it. Resource names this node
	// directly, the node.multisync.* precedent (one push carries one
	// channel-range string per node).
	FPPConnect []ObservationEntry `json:"fppConnect"`
}

// NodeDeclaration is a node's declaration state: an operator's durable
// statement that this node belongs to the installation (RES-008 D2),
// independent of whether the node currently reports in, plus the
// discovery-evidence verdict RES-008 D6 requires (BUILD-PLAN Step 7 seam B,
// acceptance criteria 2/5/6).
//
// Declared is false for a node that exists only as an observation — an
// agent hello, in the [Node] this is embedded in — that no operator action
// has ever promoted. Every field below Declared is null in that case:
// there is nothing to report about a declaration that does not exist, and
// rendering zero-valued placeholders (an empty label, a zero-time
// declaredAt) would be exactly the "blank reads as fine" failure ADR-011
// and ADR-020 decision 5 exist to forbid.
//
// DiscoveryState is one of four values, computed at read time from
// evidence plus the caller's clock — never stored, matching every other
// derived verdict in this API (ADR-011: "a verdict is computed on read,
// never stored"):
//
//   - "present": the most recent discovery run was complete and saw this
//     node. DiscoveryReason is null; LastDiscoveryRunID/LastDiscoveredAt
//     name which run and when.
//   - "not_seen": the most recent discovery run was complete and did NOT
//     see this node. DiscoveryReason names why, and NotSeenAsOfRunID/
//     NotSeenAsOfRunFinishedAt name THAT run specifically —
//     LastDiscoveryRunID/LastDiscoveredAt are NEVER overwritten with that
//     run's own identity here: they keep reporting this declaration's own
//     last-seen bookkeeping (or null if it has never once been seen), so a
//     field named "lastDiscoveredAt" never reports a time seconds old for a
//     node that has actually been dark for a week (BUILD-PLAN Step 7 seam B
//     review finding, DEFECT 8).
//   - "unknown": either the most recent discovery run did not complete
//     (still running, or ended with complete=false) — an incomplete run is
//     not evidence of absence, so this is never "not_seen" — or no
//     discovery run history is available at all, which covers BOTH "no
//     run has ever been performed" and "the run(s) this coordinator once
//     had have all been pruned by retention" identically, because this API
//     cannot and must not guess which. DiscoveryReason states why.
//   - "not_applicable": Declared is false. Discovery-seen state has no
//     meaning for something that is not part of the declared inventory —
//     it may still appear as a proposal (POST /api/v1/discovery/runs'
//     response), which is a different question this field does not
//     answer.
//
// None of this is stored: [store.NodeDeclarationRecord] only ever holds
// LastDiscoveryRunID and LastDiscoveredAt (the last run that DID see this
// node, whenever that was); present/not_seen/unknown is derived here, on
// every read, against the single most recent [store.DiscoveryRunRecord] —
// see internal/coordinator/api/mapping.go's declarationState.
type NodeDeclaration struct {
	Declared bool `json:"declared"`

	Label *string `json:"label"`
	Notes *string `json:"notes"`

	DeclaredAt              *string `json:"declaredAt"`
	DeclaredByPrincipalID   *string `json:"declaredByPrincipalId"`
	DeclaredByPrincipalName *string `json:"declaredByPrincipalName"`

	DiscoveryState  string  `json:"discoveryState"`
	DiscoveryReason *string `json:"discoveryReason"`

	// LastDiscoveryRunID/LastDiscoveredAt are THIS DECLARATION's OWN
	// last-seen-by-discovery bookkeeping — the run that most recently DID
	// see it, and when — null if it has never once been seen. They are
	// NEVER repurposed to carry any OTHER run's identity or timestamp,
	// including the run that just failed to see it (that is
	// NotSeenAsOfRunID/NotSeenAsOfRunFinishedAt below): a field named
	// "lastDiscoveredAt" asserting a time seconds old for a node dark for a
	// week is precisely the defect this split fixes.
	//
	// LastDiscoveryRunID may name a run id that no longer resolves to any
	// [DiscoveryRun] — discovery_runs is pruned by retention and
	// node_declarations is not, so a dangling id is expected, not a bug
	// (RES-008 D2/D6, migrations.go's schemaV6 doc comment). A client must
	// never treat either field as evidence of anything on its own;
	// DiscoveryState is what to read.
	LastDiscoveryRunID *string `json:"lastDiscoveryRunId"`
	LastDiscoveredAt   *string `json:"lastDiscoveredAt"`

	// NotSeenAsOfRunID/NotSeenAsOfRunFinishedAt name the run that did NOT
	// see this declared node — populated ONLY when DiscoveryState is
	// "not_seen". Added additively (ADR-020): an existing client that has
	// never heard of these two fields keeps working unchanged, reading
	// DiscoveryReason's prose and LastDiscoveryRunID/LastDiscoveredAt's own
	// (unaffected, possibly older) bookkeeping exactly as before.
	NotSeenAsOfRunID         *string `json:"notSeenAsOfRunId"`
	NotSeenAsOfRunFinishedAt *string `json:"notSeenAsOfRunFinishedAt"`
}

// DiscoveryRun is one discovery_runs row's wire representation, per
// BUILD-PLAN Step 7 seam B / RES-008 D6. FinishedAt and Reason are null
// while a run is still in progress; Reason is also null for a run that
// completed successfully (Complete true) — it is populated only when
// Complete is false and the run has finished (failed partway), per
// [store.DiscoveryRunRecord]'s own doc comment on that distinction.
type DiscoveryRun struct {
	ID         string  `json:"id"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
	Complete   bool    `json:"complete"`
	Reason     *string `json:"reason"`
	FoundCount int64   `json:"foundCount"`

	InitiatedByPrincipalID   string `json:"initiatedByPrincipalId"`
	InitiatedByPrincipalName string `json:"initiatedByPrincipalName"`
}

// DiscoveryProposal is one entity a discovery run observed that is not
// currently declared (BUILD-PLAN Step 7 seam B B1: "Proposals are computed
// at read time by diffing what is observed against what is declared").
// Source is "node" (an agent hello already in inventory) or "fpp" (a
// configured FPP instance) — the two sources B1 names discovery as
// reading, since it performs no active probing of its own.
//
// A run never creates a declaration from a proposal by itself — that is
// exactly what ADR-003 forbids ("discovery as authoritative desired
// configuration") — POST /api/v1/nodes/{nodeId}/declaration is the
// separate operator action that promotes one.
type DiscoveryProposal struct {
	NodeID string `json:"nodeId"`
	Source string `json:"source"`
}

// DiscoveryRunResponse is the body of POST /api/v1/discovery/runs.
type DiscoveryRunResponse struct {
	ServerTime string              `json:"serverTime"`
	Run        DiscoveryRun        `json:"run"`
	Proposals  []DiscoveryProposal `json:"proposals"`
}

// DeclareNodeRequest is the body of POST /api/v1/nodes/{nodeId}/declaration.
// Both fields are optional AND nullable — a bare {} (or an absent/null
// field) promotes nodeId to declared with no label or notes on a BRAND-NEW
// declaration, or leaves that field's currently declared value UNCHANGED
// on an already-declared node, matching [store.Store.DeclareNode]'s
// idempotent create-or-update semantics.
//
// A pointer, not a plain string, is DELIBERATE and load-bearing (DEFECT 6):
// a plain string cannot distinguish "this field was not provided, leave it
// alone" from "this field was provided as an explicit empty string, clear
// it" — the same absent-versus-empty distinction as CLAUDE.md's "a JSON
// null is not an absent key". Before this, a bare `showmeshctl declare
// roof-01` with no --label flag sent `{"label":"","notes":""}`
// unconditionally, silently erasing a previously set label on every
// re-declare, and node_declarations has no revision history to recover
// from that. An explicit `null` or an omitted key now means "leave
// unchanged"; an explicit `""` still means "set to empty", exactly as
// before.
type DeclareNodeRequest struct {
	Label *string `json:"label,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

// NodeDeclarationResponse is the body of POST /api/v1/nodes/{nodeId}/declaration.
type NodeDeclarationResponse struct {
	ServerTime  string          `json:"serverTime"`
	Declaration NodeDeclaration `json:"declaration"`
}

// DeleteNodeDeclarationRequest is the required body of
// DELETE /api/v1/nodes/{nodeId}/declaration. Confirm must be true, per
// BUILD-PLAN Step 7 seam B B2: "requires an explicit confirmation in the
// request rather than being a bare DELETE, so a mis-issued call cannot
// quietly remove inventory." A missing or false Confirm is rejected with
// 400 before anything is deleted; this is in addition to, never instead
// of, any confirmation dialog a UI client shows.
type DeleteNodeDeclarationRequest struct {
	Confirm bool `json:"confirm"`
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

	// InstanceUUID is FPP's own SystemUUID, as most recently
	// reported by this endpoint. Null until this endpoint has actually
	// reported a uuid at least once, never an empty string, and never
	// populated from a not_collected placeholder.
	InstanceUUID *string `json:"instanceUuid"`

	// InstanceUUIDFirstObservedAt is when this endpoint's CURRENT
	// instanceUuid was first observed (RFC 3339). Null exactly when
	// InstanceUUID is null.
	InstanceUUIDFirstObservedAt *string `json:"instanceUuidFirstObservedAt"`

	// InstanceUUIDChange is non-null exactly when this endpoint has a
	// PENDING, unacknowledged uuid change: it reported a different
	// instanceUuid than InstanceUUID before InstanceUUID's current value.
	// This is the visible-conflict rule made concrete on the wire, an SD
	// card clone, a restored backup, or a swapped controller (ADR-025)
	// never silently re-associates an endpoint's identity. It stays
	// non-null until an operator explicitly acknowledges it via
	// POST /fpp/{instanceId}/instance-uuid/acknowledge.
	InstanceUUIDChange *FPPInstanceUUIDChange `json:"instanceUuidChange"`

	// DuplicateInstanceUUIDEndpointIDs lists every OTHER currently
	// configured FPP instance reporting the SAME instanceUuid as this one
	//, a stated finding, never a silently overwritten row. Empty, never
	// null, when there is no duplicate.
	DuplicateInstanceUUIDEndpointIDs []string `json:"duplicateInstanceUuidEndpointIds"`
}

// FPPInstanceUUIDChange is [FPPInstance.InstanceUUIDChange]'s shape: the
// uuid this instance reported immediately before its current one, and
// when that change was first observed.
type FPPInstanceUUIDChange struct {
	PreviousUUID string `json:"previousUuid"`
	ChangedAt    string `json:"changedAt"`
}

// AcknowledgeFPPInstanceUUIDChangeResponse is the body of
// POST /api/v1/fpp/{instanceId}/instance-uuid/acknowledge.
type AcknowledgeFPPInstanceUUIDChangeResponse struct {
	ServerTime string `json:"serverTime"`

	// Instance is null exactly when the acknowledgment committed but the
	// endpoint is no longer among the currently configured fpp.endpoints
	// (an operator removed it between the write and this response
	// rendering it) - there is no current FPPInstance view left to
	// render, but the acknowledgment itself still succeeded and is not
	// undone. Never null otherwise.
	Instance *FPPInstance `json:"instance"`
}

// ResolumeInstanceComposition is one Resolume instance's "composition"
// member: which show is loaded, from the composition ShowMesh holds as
// configuration (ADR-032), never a live read of Arena. Name only — owner
// ruling, 2026-08-16: this route is an open read with no credential by
// default (ADR-024), the same class as an FPP playlist name already
// served openly, so what show is loaded belongs here. Revision and
// activatedAt are administrative provenance (who changed it, when) and
// stay on the gated GET /config/resolume/composition, which already
// carries both.
type ResolumeInstanceComposition struct {
	Name string `json:"name"`
}

// ResolumeInstance is one configured Resolume Arena instance's current
// representation: an element of GET /resolume/instances, the body of GET
// /resolume/instances/{id}, and the payload of a resolume.changed stream
// event. Mirrors FPPInstance's conventions: Observations is never null
// (an instance with nothing collected yet still reports whatever the
// coordinator currently holds), and Composition is null before any
// composition has ever been uploaded.
type ResolumeInstance struct {
	InstanceID   string                       `json:"instanceId"`
	Health       string                       `json:"health"`
	Observations []Evidence                   `json:"observations"`
	Composition  *ResolumeInstanceComposition `json:"composition"`
}

// ResolumeInstancesResponse is the body of GET /resolume/instances.
// Instances is never null: an unconfigured coordinator reports an empty
// array, not an absent field.
type ResolumeInstancesResponse struct {
	ServerTime string             `json:"serverTime"`
	Instances  []ResolumeInstance `json:"instances"`
}

// ResolumeInstanceResponse is the body of GET
// /resolume/instances/{instanceId}. See [NodeResponse]'s doc comment; the
// same pinned-wrapper rule applies identically here.
type ResolumeInstanceResponse struct {
	ServerTime string           `json:"serverTime"`
	Instance   ResolumeInstance `json:"instance"`
}

// ResolumeChangedEvent is the payload of a "resolume.changed" SSE event.
type ResolumeChangedEvent struct {
	Seq        uint64           `json:"seq"`
	ServerTime string           `json:"serverTime"`
	Instance   ResolumeInstance `json:"instance"`
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

	// MacroRuns is Step 9's own addition (STEP-9-SPEC.md section 6.6): every
	// in-flight macro run, plus a bounded window of recently finished ones.
	// ADR-020 decision 3 makes this fatal to omit — the change stream emits
	// no id, so a client connecting for the first time during an in-flight
	// run has no other way to learn the run exists; without this field it
	// would see nothing, submit a duplicate, and receive ADR-031 decision
	// 6's overlap 409 naming a run it cannot display.
	MacroRuns []MacroRunSummary `json:"macroRuns"`

	// Resolume: every configured Resolume instance (today: at most one),
	// rendered exactly as GET /resolume/instances renders it. Never null,
	// matching MacroRuns' own "fatal to omit" reasoning.
	Resolume []ResolumeInstance `json:"resolume"`

	// AudioConfigPush is the coordinator-level signal for whether this
	// coordinator can decode its stored, engine-wide audio.settings
	// revision right now. It reports ONLY that: it says nothing about
	// whether a given node's own separate audio.node binding is usable,
	// or whether that node is currently reachable, so "usable" here does
	// not by itself mean every node's next push will land — see
	// [AudioConfigPushStatus]'s own doc comment. Fatal to omit for the
	// same reason Collectors is — an operator whose nodes have gone quiet
	// on audio configuration has no other API-visible way to learn why.
	AudioConfigPush AudioConfigPushStatus `json:"audioConfigPush"`
}

// AudioConfigPushStatus is GET /api/v1/snapshot's "audioConfigPush"
// member (coordinator.audio.config.push.state /
// coordinator.audio.config.push.reason in IDENTIFIER-REGISTER.md). Unlike
// [CollectorStatus], this is a single coordinator-wide value, not a list:
// audio.settings is a singleton (ADR-039), so there is exactly one
// current revision to decode. Deliberately narrow: it reports whether
// THIS coordinator-wide singleton revision decodes, never whether any one
// node's own separate audio.node binding does — a node can still be
// stranded by a broken audio.node revision while this reads "usable".
// State is one of internal/coordinator/api.AudioConfigPushRunState's
// three values ("usable", "unusable", or "unknown" when a genuine
// config-store failure, not a decode failure, kept the coordinator from
// reading its own revision); Reason is set whenever State is not "usable"
// and always nil otherwise, mirroring [CollectorStatus.Reason]'s own
// convention.
type AudioConfigPushStatus struct {
	State  string  `json:"state"`
	Reason *string `json:"reason"`
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

	// ConflictingRunID names the macro run a 409 is about: the in-flight
	// run that refused an overlapping submission (ADR-031 decision 6), or
	// the existing run an idempotency key already belongs to
	// (STEP-9-SPEC.md section 6.2). Empty on every other problem.
	//
	// It exists as a field because a client must never recover it by
	// parsing Detail. LESSONS.md records the shape: the FPP command
	// endpoint returned two different 409s that differed only in their
	// English, so the Operator UI told them apart by matching a substring
	// of the server's prose, and rewording that sentence would have
	// silently offered the operator the wrong remedy. Here the stakes are
	// concrete: the 409's whole purpose is to point at a run, and a UI
	// that cannot link to it leaves the operator concluding the system is
	// stuck.
	//
	// Type already distinguishes WHICH conflict occurred. This carries
	// the one piece of data all three share. Additive under ADR-020, and
	// omitted rather than sent empty, so no existing client sees a change.
	ConflictingRunID string `json:"conflictingRunId,omitempty"`
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

// PrincipalSummary is a principal's own non-secret identity (ADR-024),
// rendered by GET and POST /api/v1/session. Never a password hash, a
// token digest, or a session/token secret.
type PrincipalSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}

// SessionInfo is the session that authenticated this request, rendered
// only when that credential was a session cookie. ID is the session's
// non-secret row identifier (never the cookie value — see
// internal/coordinator/identity's package doc comment for why
// [identity.Session.ID] is deliberately not that).
type SessionInfo struct {
	ID          string `json:"id"`
	DeviceLabel string `json:"deviceLabel"`
	CreatedAt   string `json:"createdAt"`
}

// CreateSessionRequest is the body of POST /api/v1/session.
type CreateSessionRequest struct {
	Name        string `json:"name"`
	Password    string `json:"password"`
	DeviceLabel string `json:"deviceLabel"`
}

// BootstrapRequest is the body of POST /api/v1/bootstrap (ADR-024
// decision 9). Unauthenticated by construction — no principal exists yet
// for a credential to name — and useless without Code, which is readable
// only from a file in the coordinator's data volume: possessing it proves
// filesystem access, the host-level property decision 9 requires this
// endpoint preserve rather than weaken. A successful claim creates the
// first administrator (always human, always admin — see
// internal/coordinator/identity.Service.ClaimBootstrap) and, exactly like
// POST /api/v1/session, immediately mints a session for it: see
// [SessionResponse], which this endpoint's success response reuses
// verbatim rather than inventing a second, near-identical shape.
type BootstrapRequest struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Password    string `json:"password"`
	DeviceLabel string `json:"deviceLabel"`
}

// DeleteSessionRequest is DELETE /api/v1/session's optional body.
// SessionID is empty (the common case: the request body is omitted
// entirely) to revoke the session that authenticated this very request,
// which requires that credential to be the session cookie itself — a
// bearer-token-authenticated caller has no session of its own to name
// implicitly. A non-empty SessionID instead revokes that specific,
// already-known session — self-service management of one of the
// authenticated principal's OTHER sessions (ADR-024 decision 5: "device-
// scoped and individually revocable"), which works under either
// credential form as long as the named session belongs to the
// authenticated principal; the server rejects one that does not.
type DeleteSessionRequest struct {
	SessionID string `json:"sessionId"`
}

// SessionResponse is the body of GET and POST /api/v1/session (ADR-024
// decisions 5 and 12).
//
// Authenticated is false whenever no valid credential authenticated this
// request — a new device, cleared cookies, a revoked or idle-expired
// session, a bad or absent bearer token. This is deliberately NOT a 401:
// decision 5 requires "being signed out" to be a persistent, readable
// state a client learns on load, covering a device that has never
// authenticated at all as well as one whose credential has stopped
// working, never an error the caller has to catch to find out which.
// Principal, Session, and CredentialForm are all null when Authenticated
// is false; Scopes is an empty array (never null, per this API's standing
// "absent evidence is stated, never omitted" rule applied to a
// collection) and ScopesState is "not_applicable".
//
// ScopesState follows this API's standing evidence-state discipline
// (ADR-020 decision 5, restated for authorization by ADR-024 decision
// 12): "current" when Scopes was computed from a fresh read of this
// principal's role in this same request — the only case this
// implementation currently produces, since scopes are derived
// synchronously from the authenticated principal rather than cached —
// and "unknown" on an internal error computing them, which a client MUST
// treat exactly like an empty scope list (decision 12: "a stale or
// unavailable [scope list] renders as unknown, never as permissive").
// This field, plus ServerTime, is what lets a client bound how long it
// trusts an already-fetched SessionResponse: ADR-024 decision 5's
// generation-triggered stream closure is what bounds that window, not a
// freshness computation this field performs itself.
//
// BootstrapRequired is ADR-024 decision 9's "loud and persistent"
// unclaimed-bootstrap signal, exposed here — on the one endpoint every
// client already fetches unauthenticated, on load, per decision 5 —
// specifically so a UI can render its banner with no credential and no
// second round trip. true means this coordinator currently holds zero
// principals: the volume-loss/fresh-host case decision 9 names, where
// reads stay open and the dashboard renders normally with nothing else
// visibly wrong. It is computed fresh on every request from
// identity.Service.HasAnyPrincipal, never cached, and an error computing
// it fails toward true (show the banner) rather than toward false (hide
// a real unclaimed state behind a transient store hiccup) — the same
// "stale/unknown must not read as fine" direction ADR-011 and this
// contract's own evidence rules take everywhere else.
type SessionResponse struct {
	ServerTime        string            `json:"serverTime"`
	Authenticated     bool              `json:"authenticated"`
	Principal         *PrincipalSummary `json:"principal"`
	Session           *SessionInfo      `json:"session"`
	CredentialForm    *string           `json:"credentialForm"`
	Scopes            []string          `json:"scopes"`
	ScopesState       string            `json:"scopesState"`
	BootstrapRequired bool              `json:"bootstrapRequired"`
}

// AuditEntry is one element of GET /api/v1/audit (ADR-024 decision 11).
// Params is never null (an entry with none still reports an empty
// object), matching Event.Details' identical convention.
//
// ID is this entry's append-only row id: strictly increasing, never
// reused, and the value BOTH of this endpoint's cursors are expressed in
// (`since` walking forward, `before` walking backward). It is not a
// position in the result: retention prunes from the oldest end, so the
// lowest retained id is normally greater than zero, and a client that
// advanced a cursor by counting entries would re-read the same page
// forever.
type AuditEntry struct {
	ID             int64          `json:"id"`
	Timestamp      string         `json:"timestamp"`
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

// AuditResponse is the body of GET /api/v1/audit.
//
// Order is the ordering the entries in this body are actually in, echoed
// rather than inferred: "asc" (oldest first, paged with `since`) or "desc"
// (newest first, paged with `before`). OldestRetainedID is the lowest id
// still retained, or nil when the log retains nothing at all. It mirrors
// [EventsResponse.OldestRetainedSeq] and is what lets a client paging
// backward tell the beginning of retained history from a page boundary.
//
// There is deliberately still no gap flag: unlike ADR-020's events
// contract, ADR-024 promises no "no gap and no duplicate" across a prune
// for this endpoint (see store.Store.ListAuditEntries' doc comment), and
// OldestRetainedID reports what this service can actually establish
// rather than a guarantee it cannot.
type AuditResponse struct {
	ServerTime       string       `json:"serverTime"`
	Order            string       `json:"order"`
	OldestRetainedID *int64       `json:"oldestRetainedId"`
	Entries          []AuditEntry `json:"entries"`
}

// ConfigFPPEndpoint is one element of [ConfigFPPEndpointsPayload.Endpoints]:
// one FPP instance's (id, url) pair, the same shape
// SHOWMESH_FPP_ENDPOINTS carries (RES-008 D1). Kept as its own wire type
// rather than reusing internal/coordinator/config.FPPEndpoint directly, for
// the identical reason every other wire type in this package is kept
// separate from its domain source — see this package's doc comment.
type ConfigFPPEndpoint struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// ConfigFPPEndpointsPayload is the "fpp.endpoints" configuration kind's
// decoded payload (Step 7 seam A, RES-008 D1): the body PUT
// /config/fpp.endpoints accepts, and the "payload" member of GET
// /config/fpp.endpoints' response. Endpoints is never null on the wire — an
// empty configured-endpoints list is a real, valid state, not an absence.
type ConfigFPPEndpointsPayload struct {
	Endpoints []ConfigFPPEndpoint `json:"endpoints"`
}

// FPPEndpointsConfigResponse is the body of GET and PUT
// /config/fpp.endpoints.
//
// RestartRequired is always false since ADR-036: dispatch resolves the
// endpoint list per request and collectors reconcile within about ten
// seconds, so a change here needs no restart. The field stays on the wire
// because the contract is additive-only within a major version (ADR-020).
//
// CreatedByPrincipalID/CreatedByPrincipalName are null for the one revision
// the startup env->store migration creates (Source
// "env_migration"): a startup migration has no principal, and inventing
// one to fill the field would misattribute it — see
// internal/coordinator's configsync.go.
type FPPEndpointsConfigResponse struct {
	ServerTime             string                    `json:"serverTime"`
	Kind                   string                    `json:"kind"`
	Revision               int64                     `json:"revision"`
	Payload                ConfigFPPEndpointsPayload `json:"payload"`
	UpdatedAt              string                    `json:"updatedAt"`
	CreatedByPrincipalID   *string                   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                   `json:"createdByPrincipalName"`
	Source                 string                    `json:"source"`
	RestartRequired        bool                      `json:"restartRequired"`
	RestartRequiredReason  string                    `json:"restartRequiredReason"`
}

// ConfigResolumeInstance is one element of
// [ConfigResolumeInstancesPayload.Instances]: one Resolume Arena instance's
// (id, url) pair, mirroring [ConfigFPPEndpoint]'s identical shape. The
// payload stays a list — [ResolumeInstancesConfigResponse]'s own doc
// comment says why — even though Track G seam G-2 enforces at most one
// element at write time.
type ConfigResolumeInstance struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// ConfigResolumeInstancesPayload is the "resolume.instances" configuration
// kind's decoded payload (Track G seam G-2, ADR-039): the body PUT
// /config/resolume.instances accepts, and the "payload" member of GET
// /config/resolume.instances' response. Instances is never null on the
// wire — zero configured Resolume instances is a real, valid state, not an
// absence.
type ConfigResolumeInstancesPayload struct {
	Instances []ConfigResolumeInstance `json:"instances"`
}

// ResolumeInstancesConfigResponse is the body of GET and PUT
// /config/resolume.instances, mirroring [FPPEndpointsConfigResponse]'s
// shape exactly, including the list-shaped payload: this list holds at
// most one element today and stays a list (api/interfaces.go's
// ResolumeLister doc comment already commits GET /resolume/instances to
// the identical convention) — the limit lives in validation, never in the
// schema, matching ADR-026's "N surfaces implemented at N=1".
//
// RestartRequired is always false: the coordinator's collector set follows
// this configuration within about ten seconds with no restart (Track G
// seam G-2 applies ADR-036 to this kind exactly as fpp.endpoints already
// has it). The field stays on the wire for the identical additive-only
// reason [FPPEndpointsConfigResponse.RestartRequired] does.
//
// CreatedByPrincipalID/CreatedByPrincipalName are null for the one revision
// the startup env->store migration creates (Source "env_migration"): a
// startup migration has no principal — see
// internal/coordinator's resolumeinstancessync.go.
type ResolumeInstancesConfigResponse struct {
	ServerTime             string                         `json:"serverTime"`
	Kind                   string                         `json:"kind"`
	Revision               int64                          `json:"revision"`
	Payload                ConfigResolumeInstancesPayload `json:"payload"`
	UpdatedAt              string                         `json:"updatedAt"`
	CreatedByPrincipalID   *string                        `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                        `json:"createdByPrincipalName"`
	Source                 string                         `json:"source"`
	RestartRequired        bool                           `json:"restartRequired"`
	RestartRequiredReason  string                         `json:"restartRequiredReason"`
}

// ConfigFPPMQTTPayload is the "fpp.mqtt" configuration kind's decoded
// payload (Track G seam G-3, ADR-039): the "payload" member of GET and PUT
// /config/fpp.mqtt's response. PasswordSet reports presence only — the
// password itself never appears on this or any other wire type (ADR-039
// decision 7). Hosts is never null on the wire.
type ConfigFPPMQTTPayload struct {
	BrokerURL   string            `json:"brokerURL"`
	Username    string            `json:"username"`
	TopicPrefix string            `json:"topicPrefix"`
	Hosts       map[string]string `json:"hosts"`
	PasswordSet bool              `json:"passwordSet"`
}

// FPPMQTTConfigResponse is the body of GET and PUT /config/fpp.mqtt,
// mirroring [FPPEndpointsConfigResponse]'s shape. RestartRequired is
// always false: this configuration applies without a restart from the
// start (ADR-036 via ADR-039 decision 6).
type FPPMQTTConfigResponse struct {
	ServerTime             string               `json:"serverTime"`
	Kind                   string               `json:"kind"`
	Revision               int64                `json:"revision"`
	Payload                ConfigFPPMQTTPayload `json:"payload"`
	UpdatedAt              string               `json:"updatedAt"`
	CreatedByPrincipalID   *string              `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string              `json:"createdByPrincipalName"`
	Source                 string               `json:"source"`
	RestartRequired        bool                 `json:"restartRequired"`
	RestartRequiredReason  string               `json:"restartRequiredReason"`
}

// ConfigAssetsSettingsPayload is the "assets.settings" configuration kind's
// decoded payload (Track G seam G-4, ADR-039): the body PUT
// /config/assets.settings accepts, and the "payload" member of GET
// /config/assets.settings' response. Every field is optional on a PUT
// request body (absent means "leave the stored value alone" — ADR-039
// decision 5); on a response every field is always present. Durations are
// seconds, matching this contract's existing "...Seconds" convention (e.g.
// ObservationEvidence.ValidForSeconds) rather than a Go duration string —
// FLOAT64, not an integer, mirroring ResolumeRecoveryResponse.
// SettleDelaySeconds' identical choice one seam over: an integer-second
// encoding silently truncates a legitimate sub-second interval to zero.
type ConfigAssetsSettingsPayload struct {
	ContentBaseURL           string  `json:"contentBaseUrl"`
	MaxUploadBytes           int64   `json:"maxUploadBytes"`
	SyncIntervalSeconds      float64 `json:"syncIntervalSeconds"`
	InventoryIntervalSeconds float64 `json:"inventoryIntervalSeconds"`
}

// AssetsSettingsConfigResponse is the body of GET and PUT
// /config/assets.settings, mirroring [ResolumeInstancesConfigResponse]'s
// shape exactly (env->store migration, singleton object, no-restart apply).
//
// RestartRequired is always false: every field of this kind applies to the
// already-running asset sync service with no restart (ADR-039 decision 6,
// Track G seam G-4). The field stays on the wire for the identical
// additive-only reason [FPPEndpointsConfigResponse.RestartRequired] does.
//
// CreatedByPrincipalID/CreatedByPrincipalName are null for the one revision
// the startup env->store migration creates (Source "env_migration"): a
// startup migration has no principal — see
// internal/coordinator's assetsettingssync.go.
type AssetsSettingsConfigResponse struct {
	ServerTime             string                      `json:"serverTime"`
	Kind                   string                      `json:"kind"`
	Revision               int64                       `json:"revision"`
	Payload                ConfigAssetsSettingsPayload `json:"payload"`
	UpdatedAt              string                      `json:"updatedAt"`
	CreatedByPrincipalID   *string                     `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                     `json:"createdByPrincipalName"`
	Source                 string                      `json:"source"`
	RestartRequired        bool                        `json:"restartRequired"`
	RestartRequiredReason  string                      `json:"restartRequiredReason"`
}

// ConfigRevisionMeta is one element of [ConfigRevisionsResponse.Revisions]:
// a config_revisions row's metadata, WITHOUT its payload — the revisions
// list is for browsing history (which revision, when, by whom, from what
// source), not for fetching one's full content; GET /config/fpp.endpoints
// serves the active revision's payload, and revision immutability (ADR-009)
// means any past revision's payload is recoverable later without this
// endpoint needing to carry it now. See BUILD-PLAN Step 7 seam A: "rollback
// tooling is deliberately out of scope... nothing you build may make
// rollback harder to add later" — a metadata-only list is exactly that: it
// documents history without committing this API to a shape a future
// rollback endpoint would have to match.
type ConfigRevisionMeta struct {
	Revision               int64   `json:"revision"`
	CreatedAt              string  `json:"createdAt"`
	CreatedByPrincipalID   *string `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string `json:"createdByPrincipalName"`
	Source                 string  `json:"source"`
	Note                   string  `json:"note"`
	Active                 bool    `json:"active"`
}

// ConfigRevisionsResponse is the body of GET
// /config/fpp.endpoints/revisions: every revision, newest first (Kind's
// history is never pruned — ADR-009 requires revisions stay immutable and
// available).
type ConfigRevisionsResponse struct {
	ServerTime string               `json:"serverTime"`
	Kind       string               `json:"kind"`
	Revisions  []ConfigRevisionMeta `json:"revisions"`
}

// ResolumeCompositionWrittenBy identifies the Resolume Arena build that
// wrote a stored composition file (Track D seam D-2a, ADR-032). The .avc
// format is undocumented, so this is recorded specifically so a future
// parse that looks wrong has a version to suspect first — see
// pkg/resolumecomp.WrittenBy, whose fields this mirrors.
type ResolumeCompositionWrittenBy struct {
	Product  string `json:"product"`
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	Micro    int    `json:"micro"`
	Revision int    `json:"revision"`
}

// ResolumeCompositionCanvas is a stored composition's output size in
// pixels — not available anywhere over Resolume's own REST API, only in
// the composition file itself.
type ResolumeCompositionCanvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ResolumeCompositionDeckSummary is one deck, as it appears both in
// [ResolumeCompositionSummary.Decks] and in [ResolumeCompositionResponse]'s
// own top-level "decks" (the full id map's deck list — the same shape,
// deliberately, since a deck's summary IS its complete representation;
// unlike a clip, a deck has no further detail the id map carries that the
// summary omits).
//
// Name and NameGenerated are ADR-037 decision 4, extended to decks: Name is
// the deck's own authored name (CompositionInfo/DeckInfo) when non-empty,
// otherwise the generated "Deck <n>" form from its 1-based position among
// this response's own deck list — see [ResolumeCompositionLayer.Name]'s
// identical rule for layers.
type ResolumeCompositionDeckSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	NameGenerated bool   `json:"nameGenerated"`
	Closed        bool   `json:"closed"`
	ClipCount     int    `json:"clipCount"`
}

// ResolumeCompositionSummary is what the coordinator parsed from an
// uploaded composition file, in terms an operator recognizes — never a
// bare success flag (ADR-032 decisions 7 and 8). Shared verbatim between
// POST /config/resolume/composition's own response and the "composition"
// member of GET /config/resolume/composition.
type ResolumeCompositionSummary struct {
	Name                string                           `json:"name"`
	SourceFilename      string                           `json:"sourceFilename"`
	ContentHash         string                           `json:"contentHash"`
	SizeBytes           int64                            `json:"sizeBytes"`
	WrittenBy           ResolumeCompositionWrittenBy     `json:"writtenBy"`
	Canvas              ResolumeCompositionCanvas        `json:"canvas"`
	Decks               []ResolumeCompositionDeckSummary `json:"decks"`
	LayerCount          int                              `json:"layerCount"`
	LayerGroupCount     int                              `json:"layerGroupCount"`
	ColumnCount         int                              `json:"columnCount"`
	ClipCount           int                              `json:"clipCount"`
	PersistentClipCount int                              `json:"persistentClipCount"`
}

// ResolumeCompositionUploadResponse is the body of a successful
// POST /config/resolume/composition. ServerTime is always present (the
// standing contract convention — see [FPPEndpointsConfigResponse] and
// every other response in this package), never a pointer or omitted:
// showmeshctl's own request/response contract for this endpoint decodes
// serverTime tolerantly because it was written before this was settled,
// but nothing about this type or its encoding leaves it optional now.
type ResolumeCompositionUploadResponse struct {
	ServerTime  string                     `json:"serverTime"`
	Revision    int64                      `json:"revision"`
	ActivatedAt string                     `json:"activatedAt"`
	Composition ResolumeCompositionSummary `json:"composition"`
}

// ResolumeCompositionLayerGroup is one element of
// [ResolumeCompositionResponse.LayerGroups]. Index is the group's
// position among the file's own <Group> elements in document order, which
// is what [ResolumeCompositionLayer.LayerGroupIndex] refers to.
type ResolumeCompositionLayerGroup struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// ResolumeCompositionLayer is one element of
// [ResolumeCompositionResponse.Layers]. Layers are deck-independent (ADR-032
// decision 6 — only a clip's resolution depends on its deck being
// selected). LayerGroupIndex is omitted from the wire entirely, not sent
// as null, when the composition has no layer groups at all: the source
// file omits the attribute rather than zeroing it, and a 0 would look
// like membership in a first group that does not exist.
//
// When present, LayerGroupIndex is [resolumecomp.Layer.LayerGroupIndex]
// exactly as parsed — the file's own raw layerGroup value, NOT validated
// to fall within [0, len(LayerGroups)). See that field's own doc comment
// for why this package does not bounds-check it: a caller that needs an
// actual [ResolumeCompositionLayerGroup] must check the index against
// [ResolumeCompositionResponse.LayerGroups] itself before indexing.
//
// Name and NameGenerated are ADR-037 decision 7 and decision 4: Name is
// never blank, so a client never has to special-case an empty string, but
// a blank cell is not the same claim as an operator's own name, which is
// why NameGenerated exists as a separate, explicit field rather than a
// convention like an empty string or a magic prefix. When the composition
// file carried a Name param for this layer, Name is that value verbatim
// and NameGenerated is false. When it did not (measured 5 of 18 layers in
// the operator's own composition), Name is a positional label
// ("Layer <index+1>") this coordinator invented and NameGenerated is
// true — an absent value stated with a reason, never rendered as an
// empty cell (CLAUDE.md's standing rule).
type ResolumeCompositionLayer struct {
	ID              string `json:"id"`
	Index           int    `json:"index"`
	LayerGroupIndex *int   `json:"layerGroupIndex,omitempty"`
	Name            string `json:"name"`
	NameGenerated   bool   `json:"nameGenerated"`
}

// ResolumeCompositionColumn is one element of
// [ResolumeCompositionResponse.Columns]: one column position within one
// deck.
//
// Name and NameGenerated are ADR-037 decision 4, extended to columns.
// Columns never carry an authored name at all (resolumecomp.Column has no
// Name field — the .avc format does not give one), so NameGenerated is
// always true and Name is always the generated "Column <n>" form from
// Index. The fields still exist here, rather than being omitted for
// "always generated", so every kind's response carries the identical
// (name, nameGenerated) shape a client can render uniformly.
type ResolumeCompositionColumn struct {
	ID            string `json:"id"`
	DeckID        string `json:"deckId"`
	Index         int    `json:"index"`
	Name          string `json:"name"`
	NameGenerated bool   `json:"nameGenerated"`
}

// ResolumeCompositionClip is one element of
// [ResolumeCompositionResponse.Clips] or
// [ResolumeCompositionResponse.PersistentClips].
//
// DeckID is omitted from the wire entirely (never sent as an empty
// string) for a persistent clip: [Composition.PersistentClips] "live
// outside any deck and resolve regardless of selection" (ADR-032 decision
// 6), so there is no deck to name — the omission itself IS the fact,
// mirroring [LayerGroupIndex]'s identical absent-vs-empty rule. Every
// element of Clips, in contrast, always carries a non-empty DeckID: a
// Resolume clip id resolves over Resolume's own API only while its own
// deck is selected (measured 30/30 against 0/10 for other decks), so a
// clip reference without its deck cannot tell a stale id from an
// unselected one.
//
// TransportTypeIndex is the clip's raw TransportType ParamChoice index,
// omitted when the clip carries no such param, and never translated to a
// label: the option list for this parameter is served inline over
// Resolume's REST API, varies per clip, and is not present in the
// composition file at all, so inventing a name for an index here would be
// exactly the mistake ADR-032's own bench capture warns against.
// Name is never blank: the clip's own authored name when non-empty,
// otherwise a generated form — "Clip L<layerIndex+1>C<columnIndex+1>" for
// a deck clip, "Persistent clip <n>" (1-based position among
// [ResolumeCompositionResponse.PersistentClips]) for a persistent one —
// with NameGenerated saying which.
//
// Ambiguous is true when this clip's own (deck-or-persistent, layer,
// label) triple is shared by another clip, meaning no reference —
// including one naming its own layer — can ever resolve it, since the
// colliding clips already agree on their own layer too. Measured against
// the operator's real composition: 16 of 36 clips on one deck alone.
// Always present, never omitted when false.
type ResolumeCompositionClip struct {
	ID                 string `json:"id"`
	DeckID             string `json:"deckId,omitempty"`
	LayerIndex         int    `json:"layerIndex"`
	ColumnIndex        int    `json:"columnIndex"`
	Name               string `json:"name"`
	NameGenerated      bool   `json:"nameGenerated"`
	Ambiguous          bool   `json:"ambiguous"`
	TransportTypeIndex *int   `json:"transportTypeIndex,omitempty"`
	SourcePath         string `json:"sourcePath,omitempty"`
	Width              *int   `json:"width,omitempty"`
	Height             *int   `json:"height,omitempty"`
}

// ResolumeCompositionResponse is the body of GET
// /config/resolume/composition: the stored composition's own summary plus
// the full id map every ShowMesh reference to a Resolume object resolves
// through (ADR-032 decision 1). "No composition stored yet" is not this
// type — see handleGetResolumeComposition's own doc comment for the 404
// that case produces instead, deliberately matching GET
// /config/fpp.endpoints' identical answer for its own unset case.
type ResolumeCompositionResponse struct {
	ServerTime      string                           `json:"serverTime"`
	Revision        int64                            `json:"revision"`
	ActivatedAt     string                           `json:"activatedAt"`
	Composition     ResolumeCompositionSummary       `json:"composition"`
	Decks           []ResolumeCompositionDeckSummary `json:"decks"`
	LayerGroups     []ResolumeCompositionLayerGroup  `json:"layerGroups"`
	Layers          []ResolumeCompositionLayer       `json:"layers"`
	Columns         []ResolumeCompositionColumn      `json:"columns"`
	Clips           []ResolumeCompositionClip        `json:"clips"`
	PersistentClips []ResolumeCompositionClip        `json:"persistentClips"`
}

// FPPObservationsChangedEvent is the payload of an
// "fpp.observations.changed" SSE event (ADR-023), delivered only to a
// connection that opted into delta frames via
// GET /api/v1/stream?deltas=1 — see [FPPChangedEvent], which continues to
// carry an instance's full current representation for every connection,
// delta-subscribed or not, whenever one of its STRUCTURAL fields (health,
// endpoint, lastPollAt, lastPollError) changes; this event exists
// specifically so an OBSERVATION-level change never has to repeat every
// other observation on the same instance just to report the few that
// actually moved.
//
// Changed carries the full [Evidence] envelope — exactly the shape an
// element of [FPPInstance.Observations] would have — for every signal on
// this instance whose resolved evidence differs from what this connection
// has already been told; it never repeats a signal whose resolved evidence
// is byte-identical to last time, using the same current-state
// ObservedAt/Source/CollectedAt masking [FPPChangedEvent]'s own diff
// detection already applies (see internal/coordinator/api's
// fppInstanceDiffProjection and maskEvidenceForDiff), so a value merely
// reconfirmed by a fresh poll is not "changed" here either.
//
// Removed carries the signal IDs — bare strings, not [Evidence] envelopes,
// since there is no evidence left to report — of every observation this
// hub has previously told this connection about for this instance that no
// longer exists at all: a cape swapped for a smaller one, a renamed port, a
// sensor that stopped being reported. This is required, not an
// optimization: without it, a client merging Changed onto its own baseline
// accumulates rows the coordinator no longer has, the same ghost-row
// problem ADR-023's Context section describes moving from the store into
// the browser.
//
// Both are never null — a slice with nothing to report renders as `[]`,
// per this API's standing "absent evidence is stated, never omitted" rule —
// and at least one of the two is always non-empty: an instance with
// nothing to report this render pass produces no fpp.observations.changed
// event at all (see internal/coordinator/api's Hub.render), so a client
// that receives one always has something to apply.
type FPPObservationsChangedEvent struct {
	Seq        uint64     `json:"seq"`
	ServerTime string     `json:"serverTime"`
	InstanceID string     `json:"instanceId"`
	Changed    []Evidence `json:"changed"`
	Removed    []string   `json:"removed"`
}
