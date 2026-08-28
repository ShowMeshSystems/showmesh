package api

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
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

	// InstanceUUID is this endpoint's most recently observed identity
	//: FPP's own SystemUUID, decoded off the fpp.uuid signal and
	// tracked separately from Observations because it carries its own
	// conflict state (a pending, unacknowledged change per
	// [store.FPPInstanceUUIDRecord.HasUnacknowledgedChange], the "never a
	// silent re-association" rule) that a plain observation.Observation
	// has no room for. Nil, never a zero value, until this endpoint has
	// actually reported a uuid at least once.
	InstanceUUID *store.FPPInstanceUUIDRecord

	// DuplicateInstanceUUIDEndpointIDs lists every OTHER currently
	// configured endpoint reporting the same uuid as InstanceUUID, the
	// "two endpoints reporting the same uuid is a stated finding, never a
	// silently overwritten row" rule. Empty, never nil, when there is no
	// duplicate.
	DuplicateInstanceUUIDEndpointIDs []string
}

// FPPLister lists the coordinator's configured FPP instances and their
// current collector state.
type FPPLister interface {
	ListInstances(ctx context.Context) ([]FPPInstanceView, error)
}

// ResolumeInstanceView is what this package needs from one configured
// Resolume Arena instance's collector state: its identity and whatever
// observations the collector currently holds. Unlike [FPPInstanceView]
// there is no Endpoint/LastPollAt/LastPollError here — Health/Composition
// are derived elsewhere (mapping.go/resolumecomposition.go), never carried
// on this view.
type ResolumeInstanceView struct {
	InstanceID   string
	Observations []observation.Observation
}

// ResolumeLister lists the coordinator's configured Resolume instances and
// their current collector state, mirroring [FPPLister]'s shape. The list
// holds at most one element today (SHOWMESH_RESOLUME_ID) and stays a list:
// a singleton-shaped API that later grows a second member would be a
// breaking change.
type ResolumeLister interface {
	ListInstances(ctx context.Context) ([]ResolumeInstanceView, error)
}

// NodeRenderLister looks up the render-pipeline observations this
// coordinator currently holds for one node's surfaces (Track B seam B2b),
// for embedding into GET /api/v1/nodes' per-node view — the node read
// path's analogue of [nodeEvidenceObservations] in mapping.go, except
// backed by internal/coordinator/collector/noderender's own push cache
// rather than synthesized from store rows: a render report's surfaceId
// (not nodeId) is the observations table's resource key, so this package
// cannot simply filter ListObservations by node the way FPPInstanceView
// does.
type NodeRenderLister interface {
	// NodeRenderObservations returns every surface.* AND node.multisync.*
	// observation this coordinator currently holds for nodeID's most
	// recently reported render assignment, or nil if none has ever been
	// received. Never blocks on I/O — this renders an in-memory cache,
	// matching [FPPPollNudger.NudgePoll]'s identical "must not block"
	// contract one dependency over.
	NodeRenderObservations(nodeID string) []observation.Observation
}

// NodeAudioLister is [NodeRenderLister]'s analogue for Track C's audio
// discovery reports. Unlike NodeRenderObservations, this DOES perform a
// bounded, synchronous local store read on every call — nodeID's active
// audio.node configuration (ADR-039), the only source its
// node.audio.clock.domain/provenance observations are ever built from,
// because a node cannot supply its own clock domain.
type NodeAudioLister interface {
	// NodeAudioObservations returns every node.audio.* observation this
	// coordinator currently holds for nodeID's most recently reported
	// audio discovery, or nil if none has ever been received.
	NodeAudioObservations(nodeID string) []observation.Observation
}

// NodeClockLister is [NodeRenderLister]'s analogue for Track I seam I1's
// PTP clock status reports — a plain push-cache read like
// NodeRenderObservations (node.clock.ptp.* comes straight off the node's
// own report; no coordinator-declared value needs merging in, unlike
// NodeAudioLister's clock-domain read).
type NodeClockLister interface {
	// NodeClockObservations returns every node.clock.ptp.* observation
	// this coordinator currently holds for nodeID's most recently
	// reported clock status, or nil if none has ever been received.
	NodeClockObservations(nodeID string) []observation.Observation
}

// FPPMQTTHostLister reports the id->HostName map fpp.mqtt currently
// configures, live — not a startup snapshot. Used by
// handlePutFPPEndpointsConfig (config.go) to cross-check a proposed
// fpp.endpoints list against fpp.mqtt as it stands right now, mirroring
// the identical live re-check that handler already runs against
// [Dependencies.Resolume].
type FPPMQTTHostLister interface {
	CurrentHosts(ctx context.Context) (map[string]string, error)
}

// FPPMQTTSecretStore stores and reports presence of the fpp.mqtt broker
// password (ADR-039 decision 7). The value itself is never read back
// through this interface — only Set/Clear write it, and Has reports
// presence only.
type FPPMQTTSecretStore interface {
	HasFPPMQTTPassword(ctx context.Context) (bool, error)
	SetFPPMQTTPassword(ctx context.Context, password string) error
	ClearFPPMQTTPassword(ctx context.Context) error
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

	// ListConfigObjects returns every config_objects row of kind (Step 9
	// wave 2, STEP-9-SPEC.md section 5.5's list route: "GET /config/{kind}
	// -> list: object ids with label, show, current revision. NOT full
	// payloads."). *store.Store already satisfies this directly, no
	// adapter needed, matching every other method on this interface.
	ListConfigObjects(ctx context.Context, kind string) ([]store.ConfigObjectRecord, error)
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

	// ListUnresolvedCommands returns every command whose lifecycle never
	// reached resolution (resolved_at IS NULL) — Step 7 seam C review
	// defect 5's startup reconciliation sweep
	// ([ReconcileStrandedFPPCommands]) is this method's only caller; see
	// its own doc comment for why calling it at any time other than
	// coordinator startup would be unsound.
	ListUnresolvedCommands(ctx context.Context) ([]store.CommandRecord, error)

	// GetCommandByIdempotencyKey returns the command that owns key, or
	// wraps [store.ErrCommandNotFound] ([errors.Is]-detectable) when none
	// exists. Step 8 review finding 4: [handlers.handleFPPCommand] calls
	// this BEFORE running a primitive's own PreDispatchCheck, specifically
	// so a replayed idempotency key is recognized and answered before any
	// guard gets a chance to refuse it — a legitimate replay must never be
	// answered with a fresh 409 a guard invented for what it wrongly
	// treated as a brand-new request. This is a best-effort READ used only
	// to route an ALREADY-PERSISTED key around the guard early; it is
	// deliberately NOT the mechanism that makes replay detection
	// race-free for two concurrent NEW requests sharing one key — that
	// guarantee still belongs to [InsertCommand]'s own UNIQUE-constraint
	// violation alone (see [store.ErrCommandIdempotencyKeyExists]'s own
	// doc comment on why a SELECT-then-INSERT is racy by construction).
	// When this lookup races a concurrent first insert and misses, the
	// request falls through to the guard and the insert exactly as before
	// this finding's fix, and the existing DuplicateCommandError handling
	// on that insert is what still catches it.
	GetCommandByIdempotencyKey(ctx context.Context, key string) (store.CommandRecord, error)

	// GetCommand returns one command by its own id, or wraps
	// [store.ErrCommandNotFound]. Step 9 wave 2's own addition: a macro
	// run's step carries a command id (store.MacroRunStepRecord.CommandID),
	// and the run detail route resolves it to the command's full detail
	// through this method — see STEP-9-SPEC.md section 6.1's "the commands.id
	// reference is dangling by design and must be read as one": retention
	// prunes commands independently of macro_run_steps, so this lookup MAY
	// legitimately return store.ErrCommandNotFound for a step that really
	// did dispatch one; that is rendered as "not retained", never as a
	// blank or an internal error.
	GetCommand(ctx context.Context, id string) (store.CommandRecord, error)

	// GetLatestCommandByTargetAction returns the most recently created
	// command matching (targetKind, targetID, action), or wraps
	// [store.ErrCommandNotFound] when none exists. Track H seam H4's
	// blackAndSilence audio stop (cueactivationloop.go's
	// dispatchBlackAndSilenceAudioStop) uses this to read the EvidenceAt of
	// the last cue.activate THIS coordinator dispatched to a node, so its
	// stop revision does not depend on the node's own clock running behind
	// the coordinator's — see [cueactivation.AudioSessionRevision]'s own
	// doc comment for why a stop revision derived only from the
	// coordinator's "now" is unsound.
	GetLatestCommandByTargetAction(ctx context.Context, targetKind, targetID, action string) (store.CommandRecord, error)
}

// NightSessionStore is Track F seam F2's own store dependency: the
// night_sessions / night_readiness_results rows underneath the lifecycle
// controller (nightsessioncontrol.go). Declared directly in terms of
// internal/coordinator/store's own record types, matching [CommandStore]'s
// identical departure from this file's shadow-type convention and for the
// identical reason: schemaV10 is fixed by this same seam, not by an
// independent parallel effort. *store.Store already satisfies this with
// no adapter needed.
type NightSessionStore interface {
	CreateNightSession(ctx context.Context, rec store.NightSessionRecord, now time.Time) error
	GetNightSession(ctx context.Context, id string) (store.NightSessionRecord, error)
	GetCurrentNightSession(ctx context.Context) (store.NightSessionRecord, bool, error)
	GetNightSessionByIdempotencyKey(ctx context.Context, key string) (store.NightSessionRecord, error)
	UpdateNightSession(ctx context.Context, rec store.NightSessionRecord, now time.Time) error

	CreateNightReadiness(ctx context.Context, rec store.NightReadinessRecord) error
	GetLatestNightReadiness(ctx context.Context, sessionID string) (store.NightReadinessRecord, error)

	// The four methods below are Track F seam F4's own addition, over the
	// night_cue_outbox table schemaV10 already created (store/migrations.go).
	// InsertNightCueOutboxRow's [Tx] form is what makes RESTING-MODE.md
	// §7.1.1's atomic commit possible: the session's own show_committed
	// flag and the first outward-facing cue's outbox row are written
	// together, inside one InTx call, before either is ever acted on.
	InsertNightCueOutboxRow(ctx context.Context, rec store.NightCueOutboxRecord, now time.Time) error
	GetNightCueOutboxRow(ctx context.Context, sessionID string, cycle int64, phase, cueName string) (store.NightCueOutboxRecord, error)
	ListNightCueOutboxRows(ctx context.Context, sessionID string, cycle int64) ([]store.NightCueOutboxRecord, error)
	// ListNightCueOutboxRowsForPhase is seam F5's own addition: the
	// resting-background-audio and announcement-duck-restore controllers
	// reuse night_cue_outbox as their own durable command log (this
	// file's own doc comment on nightbackgroundaudio.go explains why),
	// and their revision/bookmark state must be reconstructed across
	// every cycle a night.session record lives through, not one cycle at
	// a time.
	ListNightCueOutboxRowsForPhase(ctx context.Context, sessionID, phase string) ([]store.NightCueOutboxRecord, error)
	// ListNightCueOutboxRowsForPhasePrefix is ListNightCueOutboxRowsForPhase's
	// prefix-matching sibling: every step that shares one pkg/audio
	// session's revision counter, regardless of exactly which phase
	// spelling recorded it (nightbackgroundaudio.go's own doc comment).
	ListNightCueOutboxRowsForPhasePrefix(ctx context.Context, sessionID, prefix string) ([]store.NightCueOutboxRecord, error)
	UpdateNightCueOutboxRow(ctx context.Context, rec store.NightCueOutboxRecord) error

	// InTx runs fn inside one BEGIN IMMEDIATE transaction, so a lifecycle
	// command's read, decision, and write share one atomic unit. *store.
	// Store already satisfies this with no adapter.
	InTx(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) error
}

// FPPObservationStore is the playlist-entry observation contract's store dependency: the latest accepted
// playlist-entry observation per FPP instance
// (store/fppobservations.go). *store.Store already satisfies this with no
// adapter needed, matching [NightSessionStore]'s identical pattern.
//
// InTx is required, not merely Get/List/Put individually: contract §1.6
// step 9 requires reading the instance's currently stored sequence AND
// body hash to distinguish an idempotent replay from a genuine conflict
// BEFORE deciding whether to write, and that read must share one
// transaction with the write it gates.
type FPPObservationStore interface {
	GetFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (store.FPPPlaylistEntryObservationRecord, error)
	ListFPPPlaylistEntryObservations(ctx context.Context) ([]store.FPPPlaylistEntryObservationRecord, error)
	InTx(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) error
}

// FPPPlaylistDefinitionStore is the playlist definition publication
// contract's store dependency (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3,
// TRACK-H-H2-SPEC.md §3): store/fppplaylistdefinitions.go.
// *store.Store already satisfies this with no adapter needed, matching
// [FPPObservationStore]'s identical pattern.
//
// InTx is required for the same reason [FPPObservationStore]'s is: the
// POST handler's idempotency check ("is this key already held") and its
// write (or, per contract §3.4 step 8's "the first report of a given
// content is the one with provenance", its no-op) must share one
// transaction, and a successful insert's retention prune (H2 spec §3)
// belongs in that same transaction too.
type FPPPlaylistDefinitionStore interface {
	GetFPPPlaylistDefinition(ctx context.Context, instanceUUID, playlistHash string) (store.FPPPlaylistDefinitionRecord, error)
	ListFPPPlaylistDefinitions(ctx context.Context) ([]store.FPPPlaylistDefinitionRecord, error)
	InTx(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) error) error
}

// FPPReconciliationStore is what handleGetFPPPlaylistEntryReconciliation
// and handleGetFPPPlaylistReadiness (fppreconciliation.go) need against
// the fppreconcile package, TRACK-H-H2-SPEC.md §5 and §6. Both methods
// are declared verb-shaped, not as a narrower read surface, because
// [fppreconcile.Reconcile] and [fppreconcile.PlaylistReadiness], and
// assetsync.ResolveActiveShow (which Reconcile calls twice), take a
// concrete *store.Store, the identical reason [Dependencies.AssetManifests]'s
// own doc comment gives for why NO interface can stand in for that
// dependency. Wrapping the calls behind these two methods is what lets
// THIS field, unlike AssetManifests, carry a genuine nil-safe refusing
// default: [noFPPReconciliationStore] returns a "not wired in" error
// instead of ever calling either fppreconcile function against a nil
// store, and TestEveryRefusingDependencyIsWired
// (apidependencywiring_test.go) can see that refusal because it is a
// plain error return, not a nil-pointer dereference.
type FPPReconciliationStore interface {
	ReconcileFPPPlaylistEntryObservation(ctx context.Context, obs store.FPPPlaylistEntryObservationRecord) (fppreconcile.Result, error)
	// PlaylistReadinessForFPPPlaylist reports TRACK-H-H2-SPEC.md §6
	// readiness for one already-resolved fpp-runner show.playlist
	// binding (object id, current revision, decoded payload): the
	// caller (handleGetFPPPlaylistReadiness) is the one that fetches
	// those through [ConfigStore], matching [fppreconcile.PlaylistReadiness]'s
	// own "already resolved by the caller" contract.
	PlaylistReadinessForFPPPlaylist(ctx context.Context, playlistID string, revision int64, p config.ShowPlaylistPayload) (fppreconcile.Report, error)
}

// FPPPollNudger requests an out-of-band poll of one FPP instance's
// collector, ASAP rather than waiting out its own cadence — the owner's
// 2026-08-13 fix for the FPP REST collector's DefaultPollInterval (15s)
// making every command confirmation wait an average ~7.5s for evidence
// that could have been fetched in one LAN round trip. Declared here, at
// the consumer, for the same reason [FPPInstanceView] and [EventRecord]
// are: this package does not import internal/coordinator/collector, and
// the real implementation ([internal/coordinator/collector.Runner.Nudge],
// wrapped by internal/coordinator/apiwiring.go's fppRunnerNudger) is wired
// in by coordinator.go, not built here.
//
// NudgePoll changes ONLY when the collector's next real poll happens. It
// never stands in for evidence: fppcommand_handler.go's dispatch path
// still confirms exclusively through [fppPrimitive.Confirm] against the
// SAME notBefore fence every other confirmation already uses (Step 7 seam
// C review defect 2), reading whatever the nudged (or, if suppressed or
// absent, the ordinarily-scheduled) poll actually wrote to the store. A
// caller that let a nudge itself count as confirmation would be rebuilding
// the 179-microsecond defect this project already found and fixed once,
// deliberately this time.
type FPPPollNudger interface {
	// NudgePoll requests a poll of the collector for the FPP instance
	// named by instanceID, and reports whether the request was accepted —
	// never an error. false covers every reason it might not have been
	// (no collector registered for instanceID, a rate limit suppressing a
	// burst against one instance, or no [FPPPollNudger] wired in at all,
	// per [noFPPPollNudger]) and every one of them is handled identically
	// by every caller: fall back to the collector's own scheduled cadence,
	// never fail or delay the command this nudge was requested for.
	// Implementations MUST return promptly and MUST NOT block on network
	// I/O or on the collector's own Poll call completing — matching
	// [Sink.RecordObservations]'s identical "must not block indefinitely"
	// contract in internal/coordinator/collector, for the identical
	// reason: a slow or wedged NudgePoll must never stall the command
	// dispatch path that calls it.
	NudgePoll(instanceID string) bool
}

// AssetSyncNudger requests an out-of-band asset sync pass as soon as the
// service's current (or next) tick returns, instead of waiting out its own
// interval — see [internal/coordinator/assetsync.Service.Nudge]'s own doc
// comment, which already documents itself as "the upload handler's hook".
// Declared here, at the consumer, for the identical reason [FPPPollNudger]
// is: the real implementation is *assetsync.Service itself, which already
// satisfies this one-method interface with no adapter needed (assetsync.
// Service.Nudge takes no arguments and returns nothing).
//
// This closes a defect this project has now shipped three times: a
// capability with no production caller (Step 6's ClaimBootstrap/IssueToken/
// CreatePrincipal). Service.Nudge existed and was tested but nothing in
// production wiring ever called it, so an upload or a show activation did
// nothing until the service's own next scheduled tick, up to
// SHOWMESH_ASSET_SYNC_INTERVAL (5 minutes) later.
type AssetSyncNudger interface {
	Nudge()
}

// CueActivationNudger requests that [CueActivationLoop]'s current (or
// next) tick run promptly instead of waiting out its own periodic
// interval — [AssetSyncNudger]'s identical pattern, one seam over, for
// Track H seam H4's own activation loop (cueactivationloop.go). Declared
// here, at the consumer (fppobservations.go), for the identical reason
// [AssetSyncNudger] is: the real implementation is *CueActivationLoop
// itself, which already satisfies this one-method interface with no
// adapter needed.
//
// Calling Nudge from the FPP playlist-entry observation POST handler does
// NOT give ingestion execution authority (fppobservations.go's own
// standing rule): Nudge only asks the loop to re-evaluate promptly: the
// loop itself still independently reconciles, decides, and authorizes
// every activation exactly as it would on its own next tick. Without
// this, a fresh observation was invisible to a wall for up to
// [Options.CueActivationLoopInterval] (1 second) — long enough to be
// operator-visible on a real show.
type CueActivationNudger interface {
	Nudge()
}

// AssetSettingsSource is Track G seam G-4's live, no-restart view of the
// assets.settings configuration kind (ADR-039 decision 6): the three
// settings this package itself needs to read on every request rather than
// once at startup. Declared here, at the consumer, for the identical
// reason [AssetSyncNudger] is: the real implementation is
// *assetsync.Service itself (assetsync.Settings' three matching fields),
// which already satisfies this interface with no adapter needed.
//
// SyncInterval and MaxUploadBytes... only MaxUploadBytes appears below:
// SyncInterval governs assetsync.Service's OWN loop cadence, which this
// package never reads (it has no reason to know how often the service
// ticks, only what it would do once it does).
type AssetSettingsSource interface {
	// ContentBaseURL is the live assets.settings contentBaseUrl. Empty
	// means the asset sync service is disabled — see assetmanifest.go's
	// syncEnabled derivation.
	ContentBaseURL() string
	// MaxUploadBytes bounds a single asset upload.
	MaxUploadBytes() int64
	// InventoryInterval is the ONE staleness computation every manifest
	// response rests on (assetsync.StalenessWindow).
	InventoryInterval() time.Duration
}

// AssetFetchFailureSource is this package's live view of the last known
// asset.fetch failure for a node/content-hash pair, declared here at the
// consumer for the identical reason [AssetSyncNudger] and
// [AssetSettingsSource] are: the real implementation is
// *assetsync.Service's own LastFetchFailure method, which already
// satisfies this one-method interface with no adapter needed.
//
// This closes the bug this seam exists for: an asset.fetch that failed on
// a node used to leave no trace anywhere on the coordinator: no event, no
// reason, and the manifest's own "missing" reason read identically to a
// sync pass that simply had not run yet. assetmanifest.go's notReadyReason
// consults this to say WHY, not only THAT, a node cannot be confirmed
// ready, whenever a real failure is on record for the exact asset it is
// reporting missing.
type AssetFetchFailureSource interface {
	// LastFetchFailure reports the most recent asset.fetch failure this
	// coordinator has observed for nodeID attempting contentHash, if any.
	// ok is false when there is none on record, never fabricated as an
	// empty-string reason, per ADR-011: absent evidence is unknown, not a
	// manufactured negative. This can under-report (a real failure this
	// coordinator process never learned about, or one it has since
	// forgotten across a restart; see assetsync.Service.LastFetchFailure's
	// own doc comment) but never over-reports a failure that did not
	// happen.
	LastFetchFailure(nodeID, contentHash string) (reason string, failedAt time.Time, ok bool)
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

// AssetStore is Track E seam E3/E4's asset metadata store, as this package
// needs it (ADR-028): looking up one asset by id and listing by filter.
// Satisfied directly by *store.Store (its GetAsset/ListAssets methods),
// matching [DeclarationStore]'s "the real dependency already has this
// method set" pattern.
//
// The WRITE half (CreateAsset) is deliberately NOT a method here, for the
// identical reason [ConfigStore]'s doc comment gives for its own write
// half: it is composed directly against a live [store.Tx] inside
// [identity.Service.AuditedWrite]'s closure (assets.go), because ADR-024
// decision 11's same-transaction rule needs that exact boundary — bytes
// are staged and hashed BEFORE any row exists, and the metadata row and its
// audit entry land in one transaction or none of them do.
type AssetStore interface {
	GetAsset(ctx context.Context, id string) (store.AssetRecord, error)
	ListAssets(ctx context.Context, filter store.AssetFilter) ([]store.AssetRecord, error)
}
