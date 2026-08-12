package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// NodeLister lists the coordinator's current node inventory. It exists so
// this package does not import internal/coordinator/inventory's Manager
// concrete type directly into its handler wiring; in practice the real
// argument to [New] is *inventory.Manager, whose existing Snapshot method
// already satisfies this interface with no adapter needed —
// internal/coordinator/inventory is Step 2 work, already built and stable,
// not one of the packages Step 3 builds in parallel underneath this task.
type NodeLister interface {
	Snapshot(ctx context.Context, now time.Time) ([]inventory.NodeView, error)
}

// FPPInstanceView is what this package needs from one configured FPP
// instance's collector state: its identity, whatever observations the
// collector (Task C, built in parallel) has produced, and its last poll
// bookkeeping. This is declared here rather than imported from
// internal/coordinator/collector, which does not exist yet as this task is
// written; a later wiring task adapts the collector's real type into this
// shape, or into something structurally identical.
//
// Endpoint may carry userinfo (a config value such as
// "http://user:pass@10.0.1.20"); this package strips it before it ever
// reaches the wire (see mapping.go), so FPPLister is not required to have
// done that itself.
type FPPInstanceView struct {
	InstanceID string
	Endpoint   string

	// Observations is whatever the collector currently holds for this
	// instance, including absence observations (not_collected,
	// collection_failed, unsupported) for a signal it tracks but has no
	// current value for — an empty slice here means "the collector tracks
	// no signals for this instance", which should not normally happen, not
	// "nothing has been collected yet" (that case is one or more
	// [observation.Observation] values whose Absence is
	// [observation.StateNotCollected]).
	Observations []observation.Observation

	LastPollAt    *time.Time
	LastPollError *string
}

// FPPLister lists the coordinator's configured FPP instances and their
// current collector state.
type FPPLister interface {
	ListInstances(ctx context.Context) ([]FPPInstanceView, error)
}

// ObservationFilter narrows GET /api/v1/observations per contract section
// 6.1 ("filters resourceKind, resourceId, signal"). A nil field means no
// filter on that dimension.
type ObservationFilter struct {
	ResourceKind *observation.ResourceKind
	ResourceID   *string
	Signal       *observation.SignalID
}

// ObservationLister lists observations for GET /api/v1/observations,
// already narrowed by filter. Callers of [New] may implement filtering
// however their store indexes data; this package does not assume a
// particular query shape, only that the returned slice already satisfies
// filter.
type ObservationLister interface {
	ListObservations(ctx context.Context, filter ObservationFilter) ([]observation.Observation, error)
}

// EventRecord is what this package needs from one stored event. It is
// declared here, not imported from internal/coordinator/store (Task B,
// built in parallel), for the same reason [FPPInstanceView] is: a later
// wiring task adapts the store's real event type into this shape.
//
// OccurredAt is nil when the change this event records was first learned
// from evidence of unknown age — the event log's version of the same rule
// [observation.Observation.ObservedAt] follows, and just as load-bearing:
// never fill it from RecordedAt.
type EventRecord struct {
	Seq        uint64
	RecordedAt time.Time
	OccurredAt *time.Time
	Source     string
	Resource   observation.ResourceRef
	Category   string

	// Severity is "informational", "warning", or "critical" per
	// OBSERVABILITY section 11.2. This package does not itself enforce
	// that vocabulary on a value it did not produce; the mapping layer
	// passes it through.
	Severity      string
	Summary       string
	Details       map[string]any
	CorrelationID *string
}

// EventReader reads the coordinator's event history for GET /api/v1/events
// and for the event.recorded stream. Seq is a store-assigned, strictly
// increasing sequence number — the "since" cursor contract section 6.1
// names — and is unrelated to the SSE stream's own per-connection seq
// (contract section 6.4): this Seq is a durable, comparable cursor into
// history; the stream's seq exists specifically so it cannot become one.
//
// The store prunes event history by age and row count, so a caller's since
// cursor can legitimately name a point before everything the store still
// retains. That is not an error condition — see [Hub.renderNewEvents] and
// the events handler in handlers.go for how gap and OldestEventSeq are
// used to report it honestly instead of silently returning a short page
// that looks complete (an orchestrator contract addition closing exactly
// this hole; the events response's "gap"/"oldestRetainedSeq" fields exist
// because of it).
type EventReader interface {
	// ListEvents returns events with Seq strictly greater than since, in
	// ascending Seq order, capped at limit, and reports whether pruning has
	// removed one or more events between since and the oldest row the
	// store currently retains — mirroring
	// internal/coordinator/store.Store.ListEvents's real signature (Task
	// B), declared here so this package does not have to import that
	// package before it exists.
	ListEvents(ctx context.Context, since uint64, limit int) (events []EventRecord, gap bool, err error)

	// LatestEventSeq returns the highest Seq ever recorded, or 0 if no
	// event has ever been recorded.
	LatestEventSeq(ctx context.Context) (uint64, error)

	// OldestEventSeq returns the lowest Seq currently retained and true, or
	// ok=false if no event is currently retained (either none has ever
	// been recorded, or history has been pruned back to nothing). The
	// events handler reports this as "oldestRetainedSeq" on every response,
	// not only when gap is true.
	OldestEventSeq(ctx context.Context) (seq uint64, ok bool, err error)
}

// CollectorRunState is the closed, small vocabulary [CollectorState.State]
// and [v1.CollectorStatus.State] use, deliberately distinct from
// pkg/observation.State — see [v1.CollectorStatus]'s doc comment for why
// the two must not be conflated. Not every value a collector could
// plausibly report exists here yet: only the two this codebase's one
// collector (the FPP REST collector) actually produces. Add a value only
// when a real producer needs it, matching this codebase's standing rule
// against emitting a state nothing can currently justify.
type CollectorRunState string

const (
	// CollectorNotConfigured: this collector has nothing to poll (e.g. no
	// SHOWMESH_FPP_ENDPOINTS entries) and will not run until configuration
	// changes and the coordinator restarts.
	CollectorNotConfigured CollectorRunState = "not_configured"

	// CollectorRunning: the collector's poll loop is registered and
	// executing on its own cadence. This says nothing about whether its
	// most recent poll succeeded against any individual resource — see
	// [v1.CollectorStatus]'s doc comment for why "running" and "every
	// signal collection_failed" are not a contradiction.
	CollectorRunning CollectorRunState = "running"
)

// CollectorState is one collector's own run state, reported in the
// "collectors" member of GET /api/v1/snapshot. State is one of
// [CollectorRunState]'s values; kept as a plain string here (rather than
// CollectorRunState itself) for the same reason [EventRecord.Severity] is
// a plain string despite OBSERVABILITY section 11.2 naming a closed set —
// this package renders whatever its CollectorStatusLister reports, and
// CollectorRunState documents the vocabulary a well-behaved implementation
// commits to rather than a type this package enforces against a producer
// it does not control the construction of.
type CollectorState struct {
	ID     string
	State  string
	Reason *string
}

// CollectorStatusLister lists the coordinator's collectors' own run state
// (not the resources they observe) for GET /api/v1/snapshot.
type CollectorStatusLister interface {
	CollectorStatuses(ctx context.Context) ([]CollectorState, error)
}

// ConfigStore is the read half of the configuration write surface (Step 7
// seam A, RES-008 D1, ADR-009): a config object's current active-revision
// pointer, one immutable revision's full row, and the complete revision
// history for one (kind, id). Declared here against
// internal/coordinator/store's own record types directly, matching this
// package's existing precedent for a dependency this package does not own
// production wiring of but that is cheap to type-check against exactly
// (audit.go's defaultAuditLimit/maxAuditLimit and mapping.go's
// store.HelloRecord/store.LWTRecord/store.HealthRecord already do this) —
// *store.Store's own [store.Store.GetConfigObject]/[store.Store.GetConfigRevision]/
// [store.Store.ListConfigRevisions] methods already satisfy this interface
// with no adapter needed, unlike NodeLister/FPPLister/EventReader above,
// which exist specifically so this package need not import a producer
// package that did not exist yet when THEY were declared.
//
// The WRITE half (creating and activating a revision) is deliberately NOT
// a method here: it is composed directly against a live [store.Tx] inside
// [identity.Service.AuditedWrite]'s closure (see config.go in this
// package), because ADR-024 decision 11's same-transaction rule requires
// exactly that transaction boundary — a ConfigStore.PutFPPEndpoints-shaped
// method would have to either open its own transaction (defeating the
// rule this seam exists to prove) or leak store.Tx through this interface
// anyway, so there is nothing a narrower write method would add.
type ConfigStore interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
	ListConfigRevisions(ctx context.Context, kind, id string) ([]store.ConfigRevisionRecord, error)
}

// CommandStore is what Step 7 seam C's FPP command endpoint needs from
// the coordinator's store: insert a new command row (with idempotency-key
// replay detection — [store.DuplicateCommandError]), read one back, record
// the desired state a command is asking for, and update a command's own
// dispatch/outcome lifecycle bookkeeping.
//
// Unlike every other producer-side interface in this file, this one is
// declared directly in terms of internal/coordinator/store's own record
// types ([store.CommandRecord], [store.DesiredStateRecord],
// [store.CommandOutcomeUpdate]) rather than a shadow type local to this
// package. That is a deliberate departure from [FPPInstanceView]'s and
// [EventRecord]'s own doc comments, which decouple this package from a
// producer "being built in parallel by other Step 3 tasks" — store's
// schemaV6 commands/desired_state tables are fixed by ARCHITECTURE
// section 8.1's envelope and by this same task, not by an independent
// parallel effort this package must avoid coupling to prematurely, and
// this package already imports internal/coordinator/store directly for
// [store.Tx] (identity.Service.AuditedWrite's signature, auth.go) and for
// store.DefaultEventsPageSize/MaxEventsPageSize (api.go) — so redeclaring
// three more record shapes here would be duplication with no
// decoupling benefit behind it.
type CommandStore interface {
	// InsertCommand records a new command, or — on a replayed idempotency
	// key — returns *store.DuplicateCommandError wrapping
	// store.ErrCommandIdempotencyKeyExists, carrying the pre-existing row.
	InsertCommand(ctx context.Context, rec store.CommandRecord) (store.CommandRecord, error)

	// SetDesiredState records ADR-003's desired half of the split for one
	// (resourceKind, resourceID, signal) triple — this endpoint's proof
	// that a dispatched command has a recorded target to confirm against,
	// never itself reconciled (store's own standing rule: nothing loops
	// over this table re-issuing commands).
	SetDesiredState(ctx context.Context, rec store.DesiredStateRecord) (store.DesiredStateRecord, error)

	// UpdateCommandOutcome applies a partial update to an existing
	// command row's own lifecycle bookkeeping (dispatched_at, resolved_at,
	// state, result, outcome) — never the audit trail, which is a
	// separate, append-only concern (identity.Service.WriteAudit).
	UpdateCommandOutcome(ctx context.Context, id string, upd store.CommandOutcomeUpdate) error
}

// DeclarationStore is what this package needs from seam 0's
// node_declarations and discovery_runs tables (RES-008 D2/D6, BUILD-PLAN
// Step 7 seam B) — satisfied directly by *store.Store, matching
// [NodeLister]'s "the real dependency already has this method set"
// pattern: no adapter type is needed in
// internal/coordinator/apiwiring.go, exactly the way *inventory.Manager
// already satisfies NodeLister with no wrapper (see that interface's own
// doc comment).
//
// DeclareNode and DeleteNodeDeclaration are deliberately NOT part of this
// interface. Both are coordinator-local state changes gated by
// config:write, and ADR-024 decision 11's same-transaction rule applies to
// both in full — see discovery.go's handlePromoteNode/
// handleDeleteNodeDeclaration, which call identity.Service.AuditedWrite and
// reach store.Tx.DeclareNode/store.Tx.DeleteNodeDeclaration directly inside
// its closure instead of through a plain *Store method a caller could
// invoke outside a transaction by mistake.
type DeclarationStore interface {
	// StartDiscoveryRun records a new, in-progress discovery run.
	StartDiscoveryRun(ctx context.Context, rec store.DiscoveryRunRecord) (store.DiscoveryRunRecord, error)
	// FinishDiscoveryRun records id's terminal state — complete (with
	// foundCount) or, if the run could not complete, complete=false with a
	// stated reason. See [store.Store.FinishDiscoveryRun]'s doc comment:
	// "never a missing row and never a silent partial success."
	FinishDiscoveryRun(ctx context.Context, id string, complete bool, reason string, foundCount int64) error
	// ListDiscoveryRuns returns the most recent discovery runs, newest
	// first. This package only ever asks for the single newest one (see
	// mapping.go's declarationState) to compute B3's flag states.
	ListDiscoveryRuns(ctx context.Context, limit int) ([]store.DiscoveryRunRecord, error)

	// ListNodeDeclarations returns every declared node, for computing
	// discovery proposals (what is observed but not declared) and for
	// rendering every [v1.Node]'s declaration block.
	ListNodeDeclarations(ctx context.Context) ([]store.NodeDeclarationRecord, error)
	// RecordNodeDiscoverySeen stamps a declared node's last-seen-by-
	// discovery evidence. Never creates or deletes a declaration — see
	// [store.Store.RecordNodeDiscoverySeen]'s own doc comment — which is
	// exactly why this narrow method, rather than DeclareNode, is what a
	// discovery run itself is allowed to call.
	RecordNodeDiscoverySeen(ctx context.Context, nodeID, runID string, seenAt time.Time) error
}
