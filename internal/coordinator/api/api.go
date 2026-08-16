package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Dependencies are the data sources this API renders. Every field is an
// interface declared in interfaces.go, not a concrete package this task
// does not own — see this package's doc comment. A nil field is replaced
// by a no-op implementation that returns an empty, successful result
// (never an error): a dependency nobody has wired in yet is not this API
// failing, it is this API accurately reporting that it currently has
// nothing to say about that resource. Production wiring (a later task)
// is expected to supply all six; tests in this package exercise the
// no-op defaults deliberately, to prove the router itself works before any
// real store exists.
type Dependencies struct {
	Nodes        NodeLister
	FPP          FPPLister
	Observations ObservationLister
	Events       EventReader
	Collectors   CollectorStatusLister

	// Identity is ADR-024's principal, session, token, bootstrap, and
	// audit surface — internal/coordinator/identity.Service, already
	// built (Step 6's own dependency, not this package's to define). A
	// nil field is replaced by [noIdentityService], under which every
	// credential fails to authenticate and every read stays exactly as
	// open (or closed, per [Options.CloseReads]) as it already was:
	// wiring this in is what makes POST/GET/DELETE /api/v1/session and
	// GET /api/v1/audit do anything other than always answer 401/403 —
	// see auth.go's noIdentityService doc comment.
	Identity identity.Service

	// Config is Step 7 seam A's read half of the configuration write
	// surface (RES-008 D1) — see [ConfigStore]'s doc comment for why the
	// write half is composed against Identity.AuditedWrite instead of a
	// method here. A nil field is replaced by [noConfigStore], under which
	// every config read reports [store.ErrConfigObjectNotFound] — the
	// identical "no revision has ever been activated" answer a real,
	// freshly-migrated *store.Store gives, so a coordinator that has not
	// wired this in renders the honest "nothing configured yet" state
	// rather than a 500.
	Config ConfigStore

	// Commands is Step 7 seam C's store dependency — see [CommandStore].
	// A nil field is replaced by [noCommandStore], under which
	// POST /api/v1/fpp/{instanceId}/commands always fails with a 500
	// naming the missing wiring, matching every other write dependency's
	// identical "refuse loudly, never fabricate success" posture under
	// this default (noIdentityService.CreateSession and friends, auth.go).
	Commands CommandStore

	// Discovery is BUILD-PLAN Step 7 seam B's node-declaration surface
	// (RES-008 D2/D6) — see [DeclarationStore]'s doc comment. In practice
	// the real argument to [New] is *store.Store itself, whose
	// StartDiscoveryRun/FinishDiscoveryRun/ListDiscoveryRuns/
	// ListNodeDeclarations/RecordNodeDiscoverySeen methods already satisfy
	// this interface with no adapter, exactly like [Dependencies.Nodes].
	// A nil field is replaced by [noDeclarationStore]: every read returns
	// an empty, successful result and every write refuses with an
	// internal error, matching this package's standing "an unwired
	// dependency is not this API failing" posture.
	Discovery DeclarationStore

	// FPPEndpointsEnvVarSet is whether SHOWMESH_FPP_ENDPOINTS is currently
	// set in the coordinator PROCESS's environment — internal/coordinator's
	// Run computes it once, from the same already-loaded
	// [config.Config.FPPEndpointsEnvSet] that decided the migration and
	// disagreement rule at startup (internal/coordinator/configsync.go),
	// and passes the plain bool through here. This package deliberately
	// has no access to os.Getenv anywhere: reading the environment is
	// config's job, not the API's, and threading the fact through as data
	// (rather than this package doing its own lookup) is what keeps that
	// boundary real rather than a naming convention. Consumed by
	// handlePutFPPEndpointsConfig (config.go) for Step 7 seam A review
	// defect 3a: a write is refused with 409 while this is true, because
	// it cannot survive this coordinator's own next restart (see that
	// handler's doc comment). The zero value (false) is the same posture
	// as every other unwired dependency in this struct: "nothing told
	// this API otherwise", so no refusal fires — correct for tests and
	// for any embedder that has not wired it in.
	FPPEndpointsEnvVarSet bool

	// FPPEndpointsMigrationDeferred is whether this coordinator started
	// with SHOWMESH_FPP_ENDPOINTS set, tried to migrate it into the store
	// (RES-008 D1), and could NOT persist that write — so it is collecting
	// from an endpoint list the store does not hold. Threaded through as
	// data for the same reason [FPPEndpointsEnvVarSet] is: this package
	// reads neither the environment nor the store's startup history.
	//
	// It exists because the two states are otherwise indistinguishable
	// from the store alone, and answering the wrong one is a falsehood in
	// both directions. handleGetFPPEndpointsConfig would report "no
	// fpp.endpoints configuration has been created yet" while three hosts
	// are being polled from the list that failed to persist, and
	// handlePutFPPEndpointsConfig's 409 would tell the operator to remove
	// SHOWMESH_FPP_ENDPOINTS and restart — correct advice once a migration
	// has landed, and a silent loss of every configured endpoint before
	// one has. Both are stated correctly when this is true.
	//
	// The zero value (false) is the same "nothing told this API otherwise"
	// posture as every other unwired dependency here, and is correct: a
	// coordinator that never deferred a migration, and any embedder or
	// test that does not wire it, both want the unqualified messages.
	FPPEndpointsMigrationDeferred bool

	// FPPMQTTHostIDs is SHOWMESH_FPP_MQTT_HOSTS's parsed instance-id ->
	// host-name mapping ([config.Config.FPPMQTTHosts]), threaded through
	// for the identical "this package does not read the environment or
	// config package state on its own" reason [FPPEndpointsEnvVarSet]
	// documents. Consumed by handlePutFPPEndpointsConfig for Step 7 seam A
	// review defect 4: a PUT is refused with 400 when the proposed
	// endpoint list would drop an id this map still references, naming
	// that id, rather than accepting 200 and refusing to boot on the next
	// restart against [config.ValidateFPPMQTTHostIDs]'s identical rule.
	// A nil/empty map (the zero value, and every existing test's default)
	// means the cross-check has nothing to enforce — identical to how an
	// unset SHOWMESH_FPP_MQTT_HOSTS behaves everywhere else in this
	// codebase.
	FPPMQTTHostIDs map[string]string

	// Nudger is the post-dispatch poll nudge's dependency — see
	// [FPPPollNudger]. A nil field is replaced by [noFPPPollNudger], under
	// which NudgePoll always reports false: every command dispatch
	// degrades to exactly the pre-nudge behavior (wait for the collector's
	// own scheduled poll), matching every other unwired dependency's
	// "nothing told this API otherwise" posture in this struct.
	Nudger FPPPollNudger

	// Macros is Step 9's macro executor as this package needs it — see
	// [MacroRunner] (macro_seam.go). In practice the real value is
	// *macro.Executor, built and reconciled by coordinator wiring, never
	// this package (the import direction is forced: macro imports api, so
	// api must never import macro — see macro_seam.go's own top comment).
	// A nil field is replaced by [noMacroRunner], under which every read
	// answers empty-and-successful (or, for GetRun, "not found") and
	// SubmitRun refuses loudly — matching this package's standing "a
	// dependency nobody has wired in yet is not this API failing" posture,
	// with SubmitRun held to the same "refuse loudly, never fabricate
	// success" rule as every other write dependency's default.
	Macros MacroRunner

	// IntegrationBrokers is the deployment's declared external MQTT broker
	// set (SHOWMESH_INTEGRATION_BROKERS — internal/coordinator/config's
	// IntegrationBroker, wave 2 shared contract section 5), threaded
	// through as data for the identical reason [FPPEndpointsEnvVarSet] is:
	// this package does not read the environment or the config package's
	// own parsing on its own. Consumed by showconfig.go's show.action
	// write validation to check an mqtt target's declared broker against
	// what this deployment actually has (STEP-9-SPEC.md section 2.10: "an
	// action naming no broker is rejected at write time... validated at
	// write time against the brokers the deployment declares"). This is
	// the declared SET only, for validation — actually publishing through
	// one of them at run time is [Dependencies.Macros]' own
	// *broker.Registry dependency, built and wired separately by
	// coordinator wiring, never held here: this package never publishes.
	IntegrationBrokers []config.IntegrationBroker
	// ResolumeID is SHOWMESH_RESOLUME_ID ([config.Config.ResolumeID]),
	// threaded through for the identical "this package does not read the
	// environment or config package state on its own" reason
	// [FPPEndpointsEnvVarSet] documents — set ONLY when the Resolume
	// collector is actually enabled (the wiring caller's cfg.ResolumeURL
	// != "" gate), and left as the empty string otherwise.
	//
	// That gating matters: [config.Config.ResolumeID] defaults to
	// "resolume" even with the collector disabled (see that field's own
	// doc comment), so this field must NOT simply mirror it
	// unconditionally — a coordinator with no Resolume instance configured
	// at all could still have an FPP endpoint literally named "resolume",
	// and that is not a collision anything can actually hit, because no
	// Resolume collector is ever constructed to collide with it. The empty
	// string here means exactly what it means at startup
	// (internal/coordinator's own boot-time re-check, gated identically):
	// nothing to cross-check.
	//
	// Consumed by handlePutFPPEndpointsConfig (config.go) — a proposed
	// endpoint list whose id equals this one is refused with 400, mirroring
	// [FPPMQTTHostIDs]'s identical id-collision shape, against
	// [config.ValidateResolumeIDAgainstFPPEndpoints], the SAME rule the
	// coordinator's own startup enforces fatally for this id: a write
	// accepted here that collides would otherwise report 200 now and
	// refuse to boot on the very next restart. The zero value (empty
	// string) is the same "nothing told this API otherwise" posture as
	// every other unwired dependency in this struct, and is correct for
	// tests and for any embedder that has not wired it in.
	ResolumeID string

	// ResolumeActions is Track D seam D-3/A's action engine
	// (internal/coordinator/collector/resolume), reached only through
	// [ResolumeActionDispatcher] — see that interface's own doc comment
	// (resolumeaction_interfaces.go) for why this package declares its own
	// consumer-side view rather than importing that package directly. A
	// nil field is replaced by [noResolumeActionDispatcher]
	// (resolumeaction.go): GET /resolume/actions reports an empty
	// vocabulary and POST /resolume/actions refuses every action as
	// unsupported, matching this struct's standing "an unwired dependency
	// is not this API failing" posture.
	ResolumeActions ResolumeActionDispatcher

	// Assets is Track E seam E3/E4's asset metadata read half — see
	// [AssetStore]'s own doc comment for why its write half is composed
	// against Identity.AuditedWrite instead of a method here. A nil field
	// is replaced by [noAssetStore], under which every read reports "not
	// found"/empty, matching this struct's standing "an unwired dependency
	// is not this API failing" posture.
	Assets AssetStore

	// AssetBackend stores and serves asset bytes, content-addressed by
	// sha256 (ADR-028 decision 4) — see assetstore.Backend. A nil field is
	// replaced by [noAssetBackend], under which POST /assets refuses
	// loudly (matching every other unwired write dependency's posture) and
	// GET /assets/{id}/content reports the blob not found.
	AssetBackend assetstore.Backend

	// AssetMaxUploadBytes bounds a single asset upload (SHOWMESH_ASSET_MAX_UPLOAD_BYTES).
	// A value <= 0 is replaced by [assetstore.DefaultMaxUploadBytes] —
	// the same "nothing told this API otherwise" posture as every other
	// unwired/unset numeric dependency in this struct.
	AssetMaxUploadBytes int64

	// AssetManifests is Track E seam E5's asset manifest surface
	// (GET /assets/manifest, GET /nodes/{nodeId}/assets — assetmanifest.go).
	// It is a concrete *store.Store, not an interface, unlike every other
	// field in this struct: assetsync.BuildManifest/BuildNodeManifest
	// (ComputeNodeManifest is the ONLY function in this codebase permitted
	// to decide a node's asset readiness — see that package's own doc
	// comment) take *store.Store directly, because computing one node's
	// manifest reads config objects, node declarations, and asset
	// inventory across more than ten store methods, and a narrow
	// interface here would just be assetsync's own dependency surface
	// duplicated with no decoupling benefit — the identical reasoning
	// [CommandStore]'s doc comment (interfaces.go) gives for importing
	// store's record types directly one field over. A nil field is NOT
	// defaulted to a no-op (there is no safe no-op *store.Store): both
	// handlers check for nil explicitly and render every node's manifest
	// "unknown" with a reason naming the missing wiring, rather than
	// panicking on a nil dereference.
	AssetManifests *store.Store

	// AssetInventoryInterval is SHOWMESH_ASSET_INVENTORY_INTERVAL
	// ([config.Config.AssetInventoryInterval]), threaded through for the
	// identical "this package does not read the environment or config
	// package state on its own" reason [FPPEndpointsEnvVarSet] documents.
	// assetsync.StalenessWindow(AssetInventoryInterval) is the ONE staleness
	// computation every manifest response rests on. A value <= 0 is
	// replaced by [defaultAssetManifestInventoryInterval].
	AssetInventoryInterval time.Duration
}

// storeSatisfiesCommandStore is a compile-time assertion that
// *store.Store — the real production implementation wired in by
// internal/coordinator/apiwiring.go — already satisfies [CommandStore]
// with no adapter needed, the same property [NodeLister]'s doc comment
// notes for *inventory.Manager.Snapshot. If store.Store's method set
// drifts from this interface, this line fails to compile rather than the
// drift surfacing only once someone tries to wire the two together.
var _ CommandStore = (*store.Store)(nil)

// storeSatisfiesAssetStore is [AssetStore]'s identical compile-time
// assertion — *store.Store's GetAsset/ListAssets already satisfy it with
// no adapter needed.
var _ AssetStore = (*store.Store)(nil)

// withDefaults returns d with every nil field replaced by a no-op
// implementation.
func (d Dependencies) withDefaults() Dependencies {
	if d.Nodes == nil {
		d.Nodes = noNodeLister{}
	}
	if d.FPP == nil {
		d.FPP = noFPPLister{}
	}
	if d.Observations == nil {
		d.Observations = noObservationLister{}
	}
	if d.Events == nil {
		d.Events = noEventReader{}
	}
	if d.Collectors == nil {
		d.Collectors = noCollectorStatusLister{}
	}
	if d.Identity == nil {
		d.Identity = noIdentityService{}
	}
	if d.Config == nil {
		d.Config = noConfigStore{}
	}
	if d.Commands == nil {
		d.Commands = noCommandStore{}
	}
	if d.Discovery == nil {
		d.Discovery = noDeclarationStore{}
	}
	if d.Nudger == nil {
		d.Nudger = noFPPPollNudger{}
	}
	if d.Macros == nil {
		d.Macros = noMacroRunner{}
	}
	if d.ResolumeActions == nil {
		d.ResolumeActions = noResolumeActionDispatcher{}
	}
	if d.Assets == nil {
		d.Assets = noAssetStore{}
	}
	if d.AssetBackend == nil {
		d.AssetBackend = noAssetBackend{}
	}
	if d.AssetMaxUploadBytes <= 0 {
		d.AssetMaxUploadBytes = assetstore.DefaultMaxUploadBytes
	}
	if d.AssetInventoryInterval <= 0 {
		d.AssetInventoryInterval = defaultAssetManifestInventoryInterval
	}
	return d
}

// defaultAssetManifestInventoryInterval mirrors
// internal/coordinator/config's own defaultAssetInventoryInterval (2
// minutes). Duplicated, not imported: that constant is unexported, and
// this package must not import internal/coordinator/config for a value —
// the same posture [Dependencies.FPPEndpointsEnvVarSet]'s doc comment
// states for reading the environment. Only reached when
// [Dependencies.AssetInventoryInterval] is left unset, which production
// wiring never does (config.Load always computes a positive value).
const defaultAssetManifestInventoryInterval = 2 * time.Minute

// noFPPPollNudger is [Dependencies.Nudger]'s nil-safe default: NudgePoll
// always reports false, which every caller already treats identically to
// a suppressed or unknown nudge — see [FPPPollNudger]'s own doc comment.
// An embedder that has not wired a Nudger in gets exactly the pre-nudge
// behavior (wait for the collector's own scheduled poll), never an error.
type noFPPPollNudger struct{}

func (noFPPPollNudger) NudgePoll(string) bool { return false }

type noNodeLister struct{}

func (noNodeLister) Snapshot(context.Context, time.Time) ([]inventory.NodeView, error) {
	return nil, nil
}

type noFPPLister struct{}

func (noFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) { return nil, nil }

type noObservationLister struct{}

func (noObservationLister) ListObservations(context.Context, ObservationFilter) ([]observation.Observation, error) {
	return nil, nil
}

type noEventReader struct{}

func (noEventReader) ListEvents(context.Context, uint64, int) ([]EventRecord, bool, error) {
	return nil, false, nil
}
func (noEventReader) LatestEventSeq(context.Context) (uint64, error) { return 0, nil }
func (noEventReader) OldestEventSeq(context.Context) (uint64, bool, error) {
	return 0, false, nil
}

type noCollectorStatusLister struct{}

func (noCollectorStatusLister) CollectorStatuses(context.Context) ([]CollectorState, error) {
	return nil, nil
}

// noConfigStore is [Dependencies.Config]'s nil-safe default. Every method
// returns [store.ErrConfigObjectNotFound] (GetConfigObject/GetConfigRevision)
// or an empty, successful list (ListConfigRevisions) — never a fabricated
// object — matching this package's standing "a dependency nobody has wired
// in yet is not this API failing" posture (see [Dependencies.withDefaults]'s
// doc comment).
type noConfigStore struct{}

func (noConfigStore) GetConfigObject(context.Context, string, string) (store.ConfigObjectRecord, error) {
	return store.ConfigObjectRecord{}, store.ErrConfigObjectNotFound
}

func (noConfigStore) GetConfigRevision(context.Context, string, string, int64) (store.ConfigRevisionRecord, error) {
	return store.ConfigRevisionRecord{}, store.ErrConfigRevisionNotFound
}

func (noConfigStore) ListConfigRevisions(context.Context, string, string) ([]store.ConfigRevisionRecord, error) {
	return nil, nil
}

func (noConfigStore) ListConfigObjects(context.Context, string) ([]store.ConfigObjectRecord, error) {
	return nil, nil
}

// errCommandStoreNotConfigured is [noCommandStore]'s uniform failure,
// matching [errIdentityNotConfigured]'s identical posture in auth.go: a
// write dependency nobody has wired in refuses loudly rather than
// fabricating a success no state change actually backs.
var errCommandStoreNotConfigured = errors.New("api: no CommandStore was wired into this API's Dependencies")

type noCommandStore struct{}

func (noCommandStore) InsertCommand(context.Context, store.CommandRecord) (store.CommandRecord, error) {
	return store.CommandRecord{}, errCommandStoreNotConfigured
}

func (noCommandStore) SetDesiredState(context.Context, store.DesiredStateRecord) (store.DesiredStateRecord, error) {
	return store.DesiredStateRecord{}, errCommandStoreNotConfigured
}

func (noCommandStore) UpdateCommandOutcome(context.Context, string, store.CommandOutcomeUpdate) error {
	return errCommandStoreNotConfigured
}

// ListUnresolvedCommands answers empty-and-successful, not an error,
// unlike this type's write methods above: [ReconcileStrandedFPPCommands]
// (Step 7 seam C review defect 5) is a best-effort startup sweep, and a
// coordinator that has not wired a CommandStore in at all has no commands
// table to sweep — "nothing to reconcile" is the honest, successful answer
// for that case, matching every other no-op LISTER in this file
// (noObservationLister, noEventReader, ...), not this type's own write
// methods, which correctly refuse loudly.
func (noCommandStore) ListUnresolvedCommands(context.Context) ([]store.CommandRecord, error) {
	return nil, nil
}

// GetCommandByIdempotencyKey answers store.ErrCommandNotFound, not
// errCommandStoreNotConfigured: a coordinator with no CommandStore wired in
// has never recorded any command under any key, so "not found" is the
// honest, successful answer for this READ (matching ListUnresolvedCommands'
// identical reasoning just above), distinct from this type's WRITE methods,
// which correctly refuse loudly. [handlers.handleFPPCommand]'s own
// idempotency-replay lookup (Step 8 review finding 4) treats this exactly
// like a genuinely new key and proceeds to the write path below, which then
// fails loudly on errCommandStoreNotConfigured as it always did.
func (noCommandStore) GetCommandByIdempotencyKey(context.Context, string) (store.CommandRecord, error) {
	return store.CommandRecord{}, store.ErrCommandNotFound
}

// GetCommand answers store.ErrCommandNotFound, matching
// GetCommandByIdempotencyKey's identical reasoning immediately above: a
// coordinator with no CommandStore wired in has recorded no command under
// any id.
func (noCommandStore) GetCommand(context.Context, string) (store.CommandRecord, error) {
	return store.CommandRecord{}, store.ErrCommandNotFound
}

// noDeclarationStore is [Dependencies.Discovery]'s nil-safe default. Reads
// answer empty and successful (matching every other no-op lister in this
// file); a write refuses with errDeclarationStoreNotConfigured rather than
// fabricating a row that was never persisted — the same posture
// [noIdentityService]'s write methods take for an unwired identity
// dependency.
type noDeclarationStore struct{}

var errDeclarationStoreNotConfigured = errors.New("api: no DeclarationStore was wired into this API's Dependencies")

func (noDeclarationStore) StartDiscoveryRun(context.Context, store.DiscoveryRunRecord) (store.DiscoveryRunRecord, error) {
	return store.DiscoveryRunRecord{}, errDeclarationStoreNotConfigured
}

func (noDeclarationStore) FinishDiscoveryRun(context.Context, string, bool, string, int64) error {
	return errDeclarationStoreNotConfigured
}

func (noDeclarationStore) ListDiscoveryRuns(context.Context, int) ([]store.DiscoveryRunRecord, error) {
	return nil, nil
}

func (noDeclarationStore) ListNodeDeclarations(context.Context) ([]store.NodeDeclarationRecord, error) {
	return nil, nil
}

func (noDeclarationStore) RecordNodeDiscoverySeen(context.Context, string, string, time.Time) error {
	return errDeclarationStoreNotConfigured
}

// errMacroRunnerNotConfigured is [noMacroRunner.SubmitRun]'s uniform
// failure, matching [errCommandStoreNotConfigured]'s identical posture: a
// write dependency nobody has wired in refuses loudly rather than
// fabricating a run that was never persisted or executed.
var errMacroRunnerNotConfigured = errors.New("api: no MacroRunner was wired into this API's Dependencies")

// noMacroRunner is [Dependencies.Macros]'s nil-safe default. Every read
// answers empty-and-successful (ListRuns, SnapshotRuns) or
// [ErrMacroRunNotFound] (GetRun) — a coordinator with no macro executor
// wired in has no runs, which is the honest answer for a read, matching
// [noCommandStore]'s ListUnresolvedCommands/GetCommandByIdempotencyKey
// posture. SubmitRun is a write and refuses loudly instead, matching
// [noCommandStore.InsertCommand]'s identical posture for the identical
// reason.
type noMacroRunner struct{}

func (noMacroRunner) SubmitRun(context.Context, MacroSubmitRequest) (MacroRunResult, *v1.Problem, error) {
	return MacroRunResult{}, nil, errMacroRunnerNotConfigured
}

func (noMacroRunner) GetRun(_ context.Context, runID string) (MacroRunResult, error) {
	return MacroRunResult{}, fmt.Errorf("%w: %s", ErrMacroRunNotFound, runID)
}

func (noMacroRunner) ListRuns(context.Context, MacroRunFilter) ([]store.MacroRunRecord, error) {
	return nil, nil
}

func (noMacroRunner) SnapshotRuns(context.Context) ([]store.MacroRunRecord, error) {
	return nil, nil
}

// noAssetStore is [Dependencies.Assets]'s nil-safe default. GetAsset
// answers [store.ErrAssetNotFound] and ListAssets answers empty-and-
// successful — a coordinator with no AssetStore wired in has registered no
// assets, which is the honest answer for both reads, matching
// [noCommandStore]'s identical posture for its own read methods.
type noAssetStore struct{}

func (noAssetStore) GetAsset(context.Context, string) (store.AssetRecord, error) {
	return store.AssetRecord{}, store.ErrAssetNotFound
}

func (noAssetStore) ListAssets(context.Context, store.AssetFilter) ([]store.AssetRecord, error) {
	return nil, nil
}

// errAssetBackendNotConfigured is [noAssetBackend.Put]'s uniform failure,
// matching [errCommandStoreNotConfigured]'s identical posture: a write
// dependency nobody has wired in refuses loudly rather than fabricating a
// blob that was never staged.
var errAssetBackendNotConfigured = errors.New("api: no assetstore.Backend was wired into this API's Dependencies")

// noAssetBackend is [Dependencies.AssetBackend]'s nil-safe default. Put
// refuses loudly (a write); Open/Stat answer [assetstore.ErrNotFound] — a
// coordinator with no backend wired in holds no blob under any key, which
// is the honest answer for those two reads.
type noAssetBackend struct{}

func (noAssetBackend) Put(context.Context, io.Reader, int64) (assetstore.Blob, error) {
	return assetstore.Blob{}, errAssetBackendNotConfigured
}

func (noAssetBackend) Open(context.Context, string) (io.ReadSeekCloser, int64, error) {
	return nil, 0, assetstore.ErrNotFound
}

func (noAssetBackend) Stat(context.Context, string) (int64, error) {
	return 0, assetstore.ErrNotFound
}

// scopeConfigWrite is [identity.ScopeConfigWrite] as an addressable

// Options configures [New]. The zero value is usable: auth and CORS are
// disabled (contract section 6.8's documented default posture), the clock
// is time.Now, and every stream tuning value below falls back to this
// package's own labeled hypothesis.
type Options struct {
	// AllowedOrigins is SHOWMESH_API_ALLOWED_ORIGINS, comma-split by the
	// caller. Empty means no CORS headers at all.
	AllowedOrigins []string

	// CloseReads implements ADR-024 decision 2, carrying forward ADR-021's
	// posture: reads are open by default (this field's zero value, false)
	// and closable by configuration. False keeps every v1 read route this
	// package served before Step 6 open with no credential, exactly as
	// before — GET /api/v1/session and GET /api/v1/ are always open
	// regardless of this setting; see auth.go's readGuard/readGuardAll
	// doc comments for the full route-by-route accounting. New does not
	// itself log the startup warning ADR-021 rule 3/ADR-024 require when
	// reads are open — that belongs to whatever loads config and calls
	// New (a later wiring task), which has the logger and deployment
	// context this package does not.
	//
	// Writes have no equivalent switch: ADR-024 decision 2 states plainly
	// "there is no opt-out" for the write surface (POST/DELETE
	// /api/v1/session, and GET /api/v1/audit's audit:read gate), so this
	// field controls read closure only.
	CloseReads bool

	// SecureCookie sets the ADR-024 decision 5 session cookie's Secure
	// attribute. Off by default: ShowMesh terminates no TLS (ADR-022
	// decision 5), so setting this unconditionally would break the
	// default Compose bundle outright. A deployment that puts TLS in
	// front of the UI container must set this to true — decision 5 also
	// then wants the `__Host-` cookie name prefix, which this package
	// does not implement (it requires Secure, and this package cannot
	// know a deployment has TLS in front any more than it can trust
	// X-Forwarded-Proto to tell it — see decision 5's own reasoning);
	// that is a later, deliberately out-of-scope hardening step named in
	// this package's report.
	SecureCookie bool

	// LoginConcurrency, LoginQueueWait, LoginPerSourceDelay, and
	// LoginMaxDelay implement ADR-024 decision 8's login cost bound — see
	// loginlimiter.go. All four are SHOWMESH HYPOTHESES, not measured
	// values. Defaults: concurrency 4, queue wait 2s, per-failure delay
	// 250ms, max delay 5s.
	LoginConcurrency    int
	LoginQueueWait      time.Duration
	LoginPerSourceDelay time.Duration
	LoginMaxDelay       time.Duration

	// TrustClientAddr, when true, records the request's RemoteAddr on an
	// audit entry (ADR-024 decision 11's "a deployment may declare a
	// trusted proxy address to recover [client address]"). Off by
	// default: behind the UI container's proxy, RemoteAddr is the
	// proxy's own address, not the browser's, and ADR-022 rule 2 forbids
	// trusting X-Forwarded-For for exactly this class of decision. This
	// package implements no X-Forwarded-For-based recovery of the real
	// client address at all, even when this is true — see this
	// package's report for why that is left to a later task rather than
	// guessed at here; setting this true only stops discarding
	// RemoteAddr itself.
	TrustClientAddr bool

	// Clock is substituted in tests; defaults to time.Now.
	Clock func() time.Time

	Logger *slog.Logger

	// StreamTickInterval is how often the SSE hub re-renders every
	// resource and diffs it against what it last published, independent of
	// [Hub.Notify] — the mechanism that catches a Observation transitioning
	// current -> stale with no new evidence (contract section 6.5). THIS IS
	// A SHOWMESH HYPOTHESIS, not a measured value: chosen as a tradeoff
	// between staleness-transition latency and render/diff cost with no
	// load testing behind it. Defaults to 5 seconds.
	StreamTickInterval time.Duration

	// StreamKeepaliveInterval is the SSE ": keepalive" comment cadence
	// (contract section 6.4). A SHOWMESH HYPOTHESIS: chosen to be well
	// inside typical intermediary idle-connection timeouts, not measured
	// against this project's actual deployment path. Defaults to 15
	// seconds.
	StreamKeepaliveInterval time.Duration

	// StreamSubscriberBuffer is how many pending frames a slow SSE
	// subscriber may accumulate before contract section 6.4's overflow rule
	// fires (stream.reset, then disconnect). A SHOWMESH HYPOTHESIS: large
	// enough to absorb a burst of node.changed events from one collector
	// tick without false-positive disconnects, not derived from a measured
	// worst case. Defaults to 64.
	StreamSubscriberBuffer int

	// FPPCommandConfirmDeadline bounds how long
	// POST /api/v1/fpp/{instanceId}/commands waits, after a successful
	// dispatch to FPP, for the collector to report the observed state this
	// command asked for before giving up and reporting the command
	// unconfirmed (ADR-003). THIS IS A SHOWMESH HYPOTHESIS, NOT MEASURED —
	// RES-009 owns real evidence; see this task's report. It is
	// deliberately set well above the FPP REST collector's own
	// DefaultPollInterval (internal/coordinator/collector/fpp, 15s, not
	// imported here — see this package's doc comment on why this package
	// does not import that one): confirmation is read entirely from
	// whatever the collector's own background poll loop has most recently
	// recorded. As of the owner's 2026-08-13 post-dispatch poll nudge
	// (fppcommand_handler.go, [Dependencies.Nudger]), a successful dispatch
	// now also asks that same collector to poll THIS instance immediately
	// rather than waiting out its own cadence — but the nudge is
	// best-effort and rate-limited per instance (see
	// [FPPPollNudger]/[internal/coordinator/collector.Runner.Nudge]), so a
	// suppressed or unknown nudge still falls all the way back to this
	// deadline covering roughly one ordinary poll interval. This value
	// must stay sized for THAT fallback case, not for the nudge's own
	// common-case latency: shrinking it would report "unconfirmed" most of
	// the time for a command that plainly worked, exactly whenever the
	// nudge happens to be the one that gets suppressed. Defaults to 20
	// seconds.
	FPPCommandConfirmDeadline time.Duration

	// FPPCommandPollInterval is how often
	// POST /api/v1/fpp/{instanceId}/commands re-checks the collector's
	// current observations while waiting out FPPCommandConfirmDeadline. A
	// SHOWMESH HYPOTHESIS: frequent enough that this handler's own polling
	// does not meaningfully add to the wait once the collector's evidence
	// actually changes, without hammering [ObservationLister] pointlessly
	// often. Defaults to 500 milliseconds.
	FPPCommandPollInterval time.Duration
}

const (
	defaultStreamTickInterval      = 5 * time.Second
	defaultStreamKeepaliveInterval = 15 * time.Second

	// defaultLoginConcurrency, defaultLoginQueueWait,
	// defaultLoginPerSourceDelay, and defaultLoginMaxDelay back
	// [Options.LoginConcurrency]/[Options.LoginQueueWait]/
	// [Options.LoginPerSourceDelay]/[Options.LoginMaxDelay]. See those
	// fields' doc comments — all four are SHOWMESH HYPOTHESES.
	defaultLoginConcurrency    = 4
	defaultLoginQueueWait      = 2 * time.Second
	defaultLoginPerSourceDelay = 250 * time.Millisecond
	defaultLoginMaxDelay       = 5 * time.Second

	// defaultFPPCommandConfirmDeadline and defaultFPPCommandPollInterval
	// back [Options.FPPCommandConfirmDeadline]/[Options.FPPCommandPollInterval].
	// See those fields' doc comments — both are SHOWMESH HYPOTHESES.
	defaultFPPCommandConfirmDeadline = 20 * time.Second
	defaultFPPCommandPollInterval    = 500 * time.Millisecond
)

// envStreamSubscriberBufferOverride is a TEST-SUPPORT-ONLY environment
// variable (Step 3 wiring task) that lets the integration test harness in
// /test/integration shrink defaultStreamSubscriberBuffer, so a test proving
// contract section 6.4's overflow-then-disconnect behavior can force it
// deterministically with a small burst of real changes, rather than
// needing an implausibly large flood or relying on OS-level TCP
// backpressure against a non-draining client to (eventually, maybe) fill
// the production default of 64 — the exact "genuinely flaky" flood-test
// shape this package's own builder already tried and rejected in favor of
// the white-box unit tests in stream_test.go. It is read exactly once, at
// package initialization, so it can only take effect via the coordinator
// process's environment at startup — e.g. the integration harness exec'ing
// the real showmesh-coordinator binary with it set — never by calling code
// afterward. It must never become a documented production tuning surface:
// unset in every real deployment, it has no effect and
// defaultStreamSubscriberBuffer is exactly 64, matching
// [Options.StreamSubscriberBuffer]'s documented default.
const envStreamSubscriberBufferOverride = "SHOWMESH_TEST_STREAM_SUBSCRIBER_BUFFER"

// defaultStreamSubscriberBuffer is a package-level var, not a const, ONLY
// so envStreamSubscriberBufferOverride can override it for integration
// tests; see that constant's doc comment for why this must not be read as
// an invitation to change it any other way.
var defaultStreamSubscriberBuffer = resolveDefaultStreamSubscriberBuffer()

// resolveDefaultStreamSubscriberBuffer returns the
// envStreamSubscriberBufferOverride value when it is set to a valid
// positive integer, and the production default (64) otherwise. An invalid
// or non-positive override is silently ignored in favor of the default
// rather than failing package initialization, since a malformed test-only
// environment variable must never be able to crash production startup.
func resolveDefaultStreamSubscriberBuffer() int {
	const def = 64
	if raw := os.Getenv(envStreamSubscriberBufferOverride); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.StreamTickInterval <= 0 {
		o.StreamTickInterval = defaultStreamTickInterval
	}
	if o.StreamKeepaliveInterval <= 0 {
		o.StreamKeepaliveInterval = defaultStreamKeepaliveInterval
	}
	if o.StreamSubscriberBuffer <= 0 {
		o.StreamSubscriberBuffer = defaultStreamSubscriberBuffer
	}
	if o.LoginConcurrency <= 0 {
		o.LoginConcurrency = defaultLoginConcurrency
	}
	if o.LoginQueueWait <= 0 {
		o.LoginQueueWait = defaultLoginQueueWait
	}
	if o.LoginPerSourceDelay <= 0 {
		o.LoginPerSourceDelay = defaultLoginPerSourceDelay
	}
	if o.LoginMaxDelay <= 0 {
		o.LoginMaxDelay = defaultLoginMaxDelay
	}
	if o.FPPCommandConfirmDeadline <= 0 {
		o.FPPCommandConfirmDeadline = defaultFPPCommandConfirmDeadline
	}
	if o.FPPCommandPollInterval <= 0 {
		o.FPPCommandPollInterval = defaultFPPCommandPollInterval
	}
	return o
}

// API is what [New] builds: an http.Handler covering everything under
// /api/v1 (contract section 6.1), and the [Hub] backing /api/v1/stream.
//
// Neither field wires itself into anything. The caller (a later wiring
// task, per this task's spec: "do NOT wire anything into
// internal/coordinator/coordinator.go") is responsible for mounting
// Handler on the coordinator's HTTP server, starting Hub.Run in its own
// goroutine with a context tied to coordinator shutdown, and calling
// Hub.Notify whenever a wired dependency has fresh data — a store write, an
// inventory update, a completed collector poll — so a change reaches
// subscribers well before the next tick.
type API struct {
	Handler http.Handler
	Hub     *Hub
}

// New builds an [API] from deps and opts. It does not start anything: no
// goroutine runs until the caller starts [API.Hub].Run, and no HTTP
// request is served until the caller mounts [API.Handler] on a listening
// server.
func New(deps Dependencies, opts Options) *API {
	deps = deps.withDefaults()
	opts = opts.withDefaults()

	h := &handlers{
		deps: deps, clock: opts.Clock, logger: opts.Logger,
		closeReads: opts.CloseReads, secureCookie: opts.SecureCookie, trustClientAddr: opts.TrustClientAddr,
		loginLimiter:              newLoginLimiter(opts.LoginConcurrency, opts.LoginQueueWait, opts.LoginPerSourceDelay, opts.LoginMaxDelay, opts.Clock),
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline,
		fppCommandPollInterval:    opts.FPPCommandPollInterval,
	}
	hub := newHub(deps, opts, opts.Logger)

	mux := http.NewServeMux()
	// "{$}" matches only the exact path "/api/v1/", not every path under
	// it: net/http.ServeMux treats a bare trailing-slash pattern as a
	// subtree match (matching any path with that prefix). Without "{$}"
	// this route would silently swallow every unmatched /api/v1/... path
	// into the service descriptor handler instead of falling through to
	// the catch-all below, so a typo'd endpoint would 200 with a service
	// descriptor instead of 404 — caught by this package's own
	// TestUnknownV1RouteIsResourceNotFound, not by inspection.
	//
	// GET /api/v1/ is never gated by [Options.CloseReads]: it carries only
	// coordinator build metadata (contract section 6.1's "service
	// descriptor"), nothing about any node, FPP instance, observation, or
	// event, so it is not one of the four resources ADR-024 decision 4
	// names as what the read scopes gate. See this package's report.
	mux.HandleFunc("GET /api/v1/{$}", h.handleServiceDescriptor)
	mux.HandleFunc("GET /api/v1/snapshot", h.readGuardAll(readAllScopes, h.handleSnapshot))
	mux.HandleFunc("GET /api/v1/nodes", h.readGuard(identity.ScopeNodeRead, h.handleNodes))
	mux.HandleFunc("GET /api/v1/nodes/{nodeId}", h.readGuard(identity.ScopeNodeRead, h.handleNode))
	mux.HandleFunc("GET /api/v1/fpp", h.readGuard(identity.ScopeFPPRead, h.handleFPPList))
	mux.HandleFunc("GET /api/v1/fpp/{instanceId}", h.readGuard(identity.ScopeFPPRead, h.handleFPPInstance))

	// POST /api/v1/fpp/{instanceId}/commands (Step 7 seam C): the first
	// write endpoint this project has ever shipped, and the only one
	// touching FPP. Behind fpp:command (ADR-024 decision 4, defined by
	// Step 6 with no consumer until now) via writeGuard, which is what
	// supplies decision 6's CSRF check on top of the scope check —
	// fppcommand_handler.go owns everything past authorization.
	mux.HandleFunc("POST /api/v1/fpp/{instanceId}/commands", h.writeGuard(&scopeFPPCommand, h.handleFPPCommand))

	mux.HandleFunc("GET /api/v1/observations", h.readGuard(identity.ScopeObservationRead, h.handleObservations))
	mux.HandleFunc("GET /api/v1/events", h.readGuard(identity.ScopeEventRead, h.handleEvents))
	mux.HandleFunc("GET /api/v1/stream", h.readGuardAll(readAllScopes, hub.ServeHTTP))

	// ADR-024's own three routes. GET is always open (see
	// v1.SessionResponse's doc comment — "signed out" must be readable
	// with no credential at all); POST is unauthenticated by construction
	// (decision 8); DELETE goes through [handlers.writeGuard] with no
	// scope (decision 4 names no scope for it) because it is gated by "an
	// authenticated principal", not a specific grant, plus decision 6's
	// CSRF check.
	//
	// These, plus POST /api/v1/bootstrap below, are the only non-GET
	// routes this step adds (BUILD-PLAN Step 6: "Step 6 adds NO show write
	// endpoint" — bootstrap creates a principal, not a show write).
	// Registering them as method-specific patterns on paths
	// net/http.ServeMux otherwise only served GET for is what makes
	// ServeMux's own method-mismatch detection handle every other method
	// automatically (a PUT or PATCH gets 405 with an Allow header naming
	// the real set) — no change to the GET-only "GET /api/" catch-all
	// below was needed for this; see this package's report for why the two
	// implementation notes BUILD-PLAN recorded turned out not to require a
	// code change here.
	mux.HandleFunc("GET /api/v1/session", h.handleGetSession)
	mux.HandleFunc("POST /api/v1/session", h.loginCSRFGuard(h.handleCreateSession))
	mux.HandleFunc("DELETE /api/v1/session", h.writeGuard(nil, h.handleDeleteSession))

	// POST /api/v1/bootstrap (ADR-024 decision 9): unauthenticated by
	// construction, exactly like POST /api/v1/session — registered
	// directly, no [handlers.writeGuard], since there is no pre-existing
	// credential to check a scope or CSRF header against. See bootstrap.go.
	mux.HandleFunc("POST /api/v1/bootstrap", h.loginCSRFGuard(h.handleClaimBootstrap))

	// GET /api/v1/audit is always gated by audit:read (requireScope, not
	// readGuard): it is not one of the four pre-existing v1 read
	// resources [Options.CloseReads] toggles, and ADR-024 decision 4
	// grants audit:read to admin only.
	mux.HandleFunc("GET /api/v1/audit", h.requireScope(identity.ScopeAuditRead, h.handleAudit))

	// GET/PUT /api/v1/config/fpp.endpoints and its revisions list (Step 7
	// seam A, RES-008 D1). Every one of these three routes — including
	// both GETs — requires config:write and is NEVER open under
	// [Options.CloseReads]: ADR-024 decision 4 defines no config:read
	// scope, so a read here uses requireScope exactly the way GET
	// /api/v1/audit already does for the identical reason (a new,
	// always-sensitive surface, not one of the four pre-existing read
	// resources the open-reads posture covers) — see config.go's own doc
	// comment for the fuller reasoning this step's spec asks be recorded
	// in code, not only here. The PUT is additionally gated by
	// [handlers.writeGuard]'s CSRF check (decision 6) and fails closed on
	// its own audit write (decision 11) via [identity.Service.AuditedWrite] —
	// see handlePutFPPEndpointsConfig.
	mux.HandleFunc("GET /api/v1/config/fpp.endpoints", h.requireScope(identity.ScopeConfigWrite, h.handleGetFPPEndpointsConfig))
	mux.HandleFunc("PUT /api/v1/config/fpp.endpoints", h.writeGuard(&scopeConfigWrite, h.handlePutFPPEndpointsConfig))
	mux.HandleFunc("GET /api/v1/config/fpp.endpoints/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetFPPEndpointsConfigRevisions))

	// Step 7 seam B: node discovery and declaration (RES-008
	// D2/D6). All three are behind config:write — declaring what hardware
	// exists is configuration, and ADR-024 decision 4 defines no narrower
	// scope, so this is a deliberate choice recorded here rather than a
	// default; see discovery.go's own doc comments on each handler. All
	// three go through writeGuard, so decision 6's CSRF check (with the
	// bearer exemption) applies exactly as it does to every other write in
	// this package.
	mux.HandleFunc("POST /api/v1/discovery/runs", h.writeGuard(&scopeConfigWrite, h.handleStartDiscoveryRun))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/declaration", h.writeGuard(&scopeConfigWrite, h.handlePromoteNode))
	mux.HandleFunc("DELETE /api/v1/nodes/{nodeId}/declaration", h.writeGuard(&scopeConfigWrite, h.handleDeleteNodeDeclaration))

	// Step 9 wave 2: show.action and show.macro, four routes each
	// (STEP-9-SPEC.md section 5.5). Unlike fpp.endpoints, READS here go
	// through readAnyGuard(showConfigReadScopes, ...): "reading show.macro
	// and show.action requires show:macro:run OR config:write" (section
	// 5.5's own correction of the review finding that copying
	// fpp.endpoints' config:write-only read posture "breaks the UI" for
	// the operator role, which holds show:macro:run and not config:write).
	// Like fpp.endpoints, these reads are never toggled by
	// [Options.CloseReads] — this is a new, always-sensitive surface, not
	// one of ADR-024 decision 4's four pre-existing read scopes. Writes are
	// config:write only, via writeGuard, exactly like fpp.endpoints.
	mux.HandleFunc("GET /api/v1/config/show.action", h.readAnyGuard(showConfigReadScopes, h.handleListShowActions))
	mux.HandleFunc("GET /api/v1/config/show.action/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShowAction))
	mux.HandleFunc("PUT /api/v1/config/show.action/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShowAction))
	mux.HandleFunc("GET /api/v1/config/show.action/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowActionRevisions))

	mux.HandleFunc("GET /api/v1/config/show.macro", h.readAnyGuard(showConfigReadScopes, h.handleListShowMacros))
	mux.HandleFunc("GET /api/v1/config/show.macro/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShowMacro))
	mux.HandleFunc("PUT /api/v1/config/show.macro/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShowMacro))
	mux.HandleFunc("GET /api/v1/config/show.macro/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowMacroRevisions))

	// Step 9 wave 2: the run surface (STEP-9-SPEC.md section 6.6). POST is
	// gated on show:macro:run specifically, never "OR config:write" — an
	// admin who has never been granted show:macro:run must not be able to
	// fire a show through the back door of holding a different scope; GETs
	// use the same OR posture as the config kinds above, matching section
	// 6.6's "reads on runs require show:macro:run or config:write, matching
	// section 5.5." No state change is reachable by GET (ADR-024 decision
	// 7's related clause).
	mux.HandleFunc("POST /api/v1/macros/{id}/runs", h.writeGuard(&scopeShowMacroRun, h.handleSubmitMacroRun))
	mux.HandleFunc("GET /api/v1/macro-runs", h.readAnyGuard(showConfigReadScopes, h.handleListMacroRuns))
	mux.HandleFunc("GET /api/v1/macro-runs/{runId}", h.readAnyGuard(showConfigReadScopes, h.handleGetMacroRun))
	// GET/POST /api/v1/config/resolume/composition (Track D seam D-2a,
	// ADR-032): the stored Resolume composition id map, sourced only from
	// an operator-uploaded file — never from Resolume's own crashing
	// GET /composition (ADR-032 decision 2). See resolumecomposition.go.
	//
	// GET is gated behind config:write via [handlers.requireScope],
	// matching GET /config/fpp.endpoints exactly (config.go's own doc
	// comment on that handler) rather than [handlers.readGuard]'s ordinary
	// open-by-default posture. This is deliberate, not an oversight: a
	// composition upload is this coordinator's record of a configured
	// external show-control integration's own objects — exactly the class
	// config.go argues for at length (the same class as GET /audit, not
	// the telemetry ADR-024 decision 2's open-reads rule exists to
	// protect) — and it would have been the kind of two-configuration-
	// surfaces-disagree inconsistency found at 17:00 to gate it any other
	// way. It also reuses fpp:read no longer applies here: ADR-024
	// decision 4 fixes exactly four read scopes and defines no
	// config:read, which is precisely why config.go reaches for
	// config:write instead of inventing one — this route does the same,
	// rather than borrowing fpp:read, a different vendor integration's
	// scope, the way an earlier version of this route did.
	//
	// POST requires config:write via [handlers.writeGuard] — identical
	// gating to PUT /config/fpp.endpoints — because uploading a
	// composition file is exactly ADR-032 decision 1's "the operator
	// uploads a composition file... stored as a configuration object with
	// the existing revision and audit semantics", never its own invented
	// scope.
	mux.HandleFunc("GET /api/v1/config/resolume/composition", h.requireScope(identity.ScopeConfigWrite, h.handleGetResolumeComposition))
	mux.HandleFunc("POST /api/v1/config/resolume/composition", h.writeGuard(&scopeConfigWrite, h.handlePostResolumeCompositionUpload))

	// GET/POST /api/v1/resolume/actions (Track D seam D-3/B): the seven-
	// action Resolume vocabulary. GET is never gated by any scope — this
	// is static capability metadata, identical for every coordinator
	// running this software version, not a resource a credential controls
	// visibility of (see handleListResolumeActions' own doc comment) — and
	// is deliberately NOT the same posture as GET /config/resolume/
	// composition above, which renders an operator's own uploaded show
	// data. POST requires resolume:action via [handlers.writeGuard]: no
	// state change here is reachable by GET, and a principal without the
	// scope gets 403 with no HTTP request ever reaching Resolume — see
	// resolumeaction.go.
	mux.HandleFunc("GET /api/v1/resolume/actions", h.handleListResolumeActions)
	mux.HandleFunc("POST /api/v1/resolume/actions", h.writeGuard(&scopeResolumeAction, h.handleDispatchResolumeAction))

	// --- Track E: show, surface, and the active-show pointer ---
	//
	// Three new configuration kinds (show, show.surface, show.active —
	// TRACK-E-SESSION-SPEC.md section 2), following show.action/show.macro's
	// own route shape immediately above: reads use
	// readAnyGuard(showConfigReadScopes, ...) (show:macro:run OR
	// config:write), writes use writeGuard(&scopeConfigWrite, ...). "show" and
	// "show.surface" are collections with the usual four routes each;
	// "show.active" is a singleton (fixed id, no {id} path segment) with
	// three: GET, PUT, and its own revisions route. "show.active" is a
	// distinct literal path segment from "show" — net/http.ServeMux's
	// pattern matching is by segment, so this route can never be swallowed
	// by "GET /api/v1/config/show/{id}" (see this package's own
	// TestShowActiveRouteIsNotSwallowedByShowIDRoute).
	mux.HandleFunc("GET /api/v1/config/show", h.readAnyGuard(showConfigReadScopes, h.handleListShows))
	mux.HandleFunc("GET /api/v1/config/show/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShow))
	mux.HandleFunc("PUT /api/v1/config/show/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShow))
	mux.HandleFunc("GET /api/v1/config/show/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowRevisions))

	mux.HandleFunc("GET /api/v1/config/show.surface", h.readAnyGuard(showConfigReadScopes, h.handleListShowSurfaces))
	mux.HandleFunc("GET /api/v1/config/show.surface/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShowSurface))
	mux.HandleFunc("PUT /api/v1/config/show.surface/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShowSurface))
	mux.HandleFunc("GET /api/v1/config/show.surface/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowSurfaceRevisions))

	mux.HandleFunc("GET /api/v1/config/show.active", h.readAnyGuard(showConfigReadScopes, h.handleGetShowActive))
	mux.HandleFunc("PUT /api/v1/config/show.active", h.writeGuard(&scopeConfigWrite, h.handlePutShowActive))
	mux.HandleFunc("GET /api/v1/config/show.active/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowActiveRevisions))

	// --- Track E: the asset store ---
	//
	// Seam E3/E4 (ADR-028). POST is a multipart upload behind asset:write
	// (admin only, identity/types.go) via writeGuard, exactly like every
	// other write in this package — CSRF and scope enforcement come free
	// from that one guard. GET (list, one, and its bytes) all stay open by
	// default: reads never require asset:write. Listing and single-asset
	// reads use readAnyGuard(showConfigReadScopes, ...), the identical
	// posture every other Track E config kind uses, because an asset row
	// is exactly that: configuration metadata, not telemetry. The content
	// route uses the plain node:read scope instead — it is what an AGENT
	// authenticates with to fetch its own bytes (TRACK-E-SESSION-SPEC.md
	// section 5.2's asset.fetch operation), and node:read is what an agent
	// credential already holds; requiring show:macro:run/config:write here
	// would make every node fetch need a principal shaped for a human
	// operator instead of a machine one.
	mux.HandleFunc("POST /api/v1/assets", h.writeGuard(&scopeAssetWrite, h.handlePostAssetUpload))
	mux.HandleFunc("GET /api/v1/assets", h.readAnyGuard(showConfigReadScopes, h.handleListAssets))
	mux.HandleFunc("GET /api/v1/assets/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetAsset))
	mux.HandleFunc("GET /api/v1/assets/{id}/content", h.readGuard(identity.ScopeNodeRead, h.handleGetAssetContent))

	// Seam E5 (assetmanifest.go): "what should a node hold" versus "what
	// does it hold" — read-only, same showConfigReadScopes posture as
	// every other Track E config-metadata read above, never asset:write.
	// "GET /api/v1/assets/manifest" and "GET /api/v1/assets/{id}" both
	// match the literal path "/api/v1/assets/manifest"; net/http.ServeMux
	// (Go 1.22+) resolves that in favor of the more specific, all-literal
	// pattern regardless of registration order — the identical property
	// "/posts/latest" vs "/posts/{id}" demonstrates in that package's own
	// doc comment — so this is not a route ordering hazard.
	mux.HandleFunc("GET /api/v1/assets/manifest", h.readAnyGuard(showConfigReadScopes, h.handleAssetManifest))
	mux.HandleFunc("GET /api/v1/nodes/{nodeId}/assets", h.readAnyGuard(showConfigReadScopes, h.handleNodeAssetManifest))

	// Catch-all for anything else under /api/ (an unknown path version, or
	// a typo'd v1 route): see handleUnknownAPIPath's doc comment.
	//
	// Registered as "GET /api/...", not the unrestricted "/api/..." this
	// used to be (a Step 3 review correction, finding 2.8): every real
	// route above is also GET-only, so restricting this one to GET too is
	// what lets net/http.ServeMux's own method-mismatch detection actually
	// fire for a non-GET request to a real route, instead of this
	// catch-all winning the match first and answering a lying 404
	// resource-not-found for what is actually a 405.
	// withMethodNotAllowedAsProblem below (middleware.go) reformats
	// ServeMux's resulting plain-text 405 into this package's usual
	// problem+json shape; this pattern's own job is unchanged from before
	// — a GET to an unknown path version or a typo'd v1 route still
	// reaches handleUnknownAPIPath exactly as before, since GET requests
	// are unaffected by this restriction.
	mux.HandleFunc("GET /api/", handleUnknownAPIPath(opts.Logger, opts.Clock))

	handler := chain(
		withMethodNotAllowedAsProblem(mux, opts.Logger, opts.Clock),
		withRequestLogging(opts.Logger),
		withAPIVersionHeader,
		withCORS(opts.AllowedOrigins),
		withVersionNegotiation(opts.Logger, opts.Clock),
		withIdentity(deps.Identity, h.loginLimiter, opts.Logger, opts.Clock, opts.TrustClientAddr),
	)

	return &API{Handler: handler, Hub: hub}
}
