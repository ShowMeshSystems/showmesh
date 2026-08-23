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
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
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

	// Render is Track B seam B2b's dependency — see [NodeRenderLister]. A
	// nil field is replaced by [noNodeRenderLister], under which every
	// node renders with no render evidence at all: correct for a
	// coordinator with no render node ever attached, and for every test
	// in this package that predates this field.
	Render NodeRenderLister

	// Audio is Track C seam C1a/C1b's dependency — see [NodeAudioLister].
	// A nil field is replaced by [noNodeAudioLister], matching Render's
	// identical no-op default posture.
	Audio NodeAudioLister

	// RenderPublisher is Track B seam B2b-front's own dependency — see
	// [RenderPublisher]'s doc comment (renderdispatch.go). A nil field is
	// replaced by [noRenderPublisher], under which every render.* dispatch
	// fails with an internal error naming the missing wiring, matching
	// [Dependencies.Commands]'s identical no-op default posture.
	RenderPublisher RenderPublisher

	// AudioPublisher is this API's MQTT publish-and-await capability for
	// audio.session.* commands — see [AudioSessionPublisher]'s doc
	// comment (audiodispatch.go). A nil field is replaced by
	// [noAudioSessionPublisher], under which every audio.session.*
	// dispatch fails with an internal error naming the missing wiring,
	// matching [Dependencies.RenderPublisher]'s identical no-op default
	// posture.
	AudioPublisher AudioSessionPublisher

	// AudioSessions is the coordinator's own durable record of each
	// playback session's last-dispatched desired state (schemaV9's
	// audio_sessions table) — see [AudioSessionStore]. A nil field is
	// replaced by [noAudioSessionStore], under which every dispatch still
	// runs (this coordinator's own durable mirror is best-effort
	// bookkeeping, never a precondition for reaching the node — ADR-024
	// decision 7) but is never persisted coordinator-side.
	AudioSessions AudioSessionStore

	// AssetManifests is ALSO this seam's own dependency for resolving a
	// surface's current FSEQ asset by identity (ADR-028) — see
	// [Dependencies.AssetManifests]'s own doc comment below (Track E seam
	// E5). Reusing that field rather than adding a second one means the
	// asset-resolution answer render.surface.apply gets can never
	// disagree with what GET /assets/manifest reports for the same node.

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

	// ResolumeInstancesEnvVarSet is [FPPEndpointsEnvVarSet]'s mirror for
	// Track G seam G-2 (ADR-039 decision 4): whether SHOWMESH_RESOLUME_URL
	// is currently set in the coordinator PROCESS's environment. Consumed
	// by handlePutResolumeInstancesConfig (resolumeinstancesconfig.go): a
	// write is refused with 409 while this is true, for the identical
	// reason FPPEndpointsEnvVarSet's own doc comment gives. The zero value
	// (false) is the same "nothing told this API otherwise" posture as
	// every other unwired dependency in this struct.
	ResolumeInstancesEnvVarSet bool

	// ResolumeInstancesMigrationDeferred is [FPPEndpointsMigrationDeferred]'s
	// mirror: this coordinator started with SHOWMESH_RESOLUME_URL set, tried
	// to migrate it into the store, and could not persist that write — see
	// internal/coordinator's resolumeinstancessync.go. The zero value
	// (false) is the same "nothing told this API otherwise" posture as
	// every other unwired dependency here.
	ResolumeInstancesMigrationDeferred bool

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

	// AssetSettings is Track G seam G-4's live, no-restart view of the
	// assets.settings configuration kind (ADR-039): the upload byte limit,
	// the manifest staleness interval, and (via ContentBaseURL) whether the
	// sync service is enabled at all — read fresh on every request, rather
	// than the startup-snapshot fields (AssetMaxUploadBytes,
	// AssetInventoryInterval, AssetSyncEnabled) this replaced, which could
	// not change without a restart. In practice the real value is
	// *assetsync.Service, wired by coordinator.go — the SAME value wired as
	// [Dependencies.AssetSyncNudger], because that one Service is this
	// coordinator's single live holder of this configuration kind. A nil
	// field is replaced by [noAssetSettingsSource], which reproduces the
	// exact defaults the old startup-snapshot fields used to fall back to.
	AssetSettings AssetSettingsSource

	// AssetSyncNudger is Track E seam E6's out-of-band sync trigger — see
	// [AssetSyncNudger]'s own doc comment. In practice the real value is
	// *assetsync.Service, wired by coordinator.go. A nil field is replaced
	// by [noAssetSyncNudger], under which Nudge is a no-op: an upload or a
	// show activation degrades to waiting out the service's own interval,
	// matching this struct's standing "an unwired dependency is not this
	// API failing" posture.
	AssetSyncNudger AssetSyncNudger

	// AssetSettingsEnvVarsSet is [FPPEndpointsEnvVarSet]'s mirror for Track
	// G seam G-4 (ADR-039 decision 4): whether ANY of the four
	// SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/
	// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL
	// variables is currently set in the coordinator PROCESS's environment.
	// Consumed by handlePutAssetsSettingsConfig: a write is refused with
	// 409 while this is true, for the identical reason
	// FPPEndpointsEnvVarSet's own doc comment gives. The zero value (false)
	// is the same "nothing told this API otherwise" posture as every other
	// unwired dependency in this struct.
	AssetSettingsEnvVarsSet bool

	// AssetSettingsMigrationDeferred is [FPPEndpointsMigrationDeferred]'s
	// mirror: this coordinator started with one or more of the four
	// SHOWMESH_ASSET_* settings variables set, tried to migrate them into
	// the store, and could not persist that write — see
	// internal/coordinator's assetsettingssync.go. The zero value (false)
	// is the same "nothing told this API otherwise" posture as every other
	// unwired dependency here.
	AssetSettingsMigrationDeferred bool

	// Resolume is this API's observability dependency: whatever
	// Resolume instances this coordinator has configured, with their
	// currently-held observations. A nil field is replaced by
	// [noResolumeLister], under which GET /resolume/instances reports an
	// empty array, the single-instance route always 404s, and the change
	// stream announces no resolume.changed — matching this struct's
	// standing "an unwired dependency is not this API failing" posture.
	Resolume ResolumeLister

	// ResolumeReferences is Track D seam C's own write-time reference
	// resolver (ADR-037), reached only through
	// [config.ResolumeReferenceResolver] — see that interface's own doc
	// comment (internal/coordinator/config/showaction.go) for why config
	// declares it rather than importing
	// internal/coordinator/collector/resolume directly. Consumed by
	// handlePutShowAction (showconfig.go) to validate a resolume
	// show.action's ref against the coordinator's currently stored
	// composition, independent of whether a live Resolume instance is even
	// configured (the stored composition and the live collector are
	// separate concerns — see resolumeCompositionWiring's own doc comment
	// in internal/coordinator). A nil field is replaced by
	// [noResolumeReferenceResolver]: every write of a resolume show.action
	// is refused with config.ErrResolumeCompositionNotUploaded's own
	// sentence, the honest answer for a coordinator with nothing wired to
	// resolve against — matching this struct's standing "an unwired
	// dependency is not this API failing" posture.
	ResolumeReferences config.ResolumeReferenceResolver

	// MQTTBrokers is the mqtt action-invocation dispatch dependency: the
	// deployment's live integration broker registry, reached only through
	// [MQTTBrokerRegistry] (mqttactiondispatch.go). The real value is the
	// SAME *broker.Registry internal/coordinator/macro's own executor
	// dispatches an mqtt-integration show.macro step through, wired twice
	// against one shared registry the identical way
	// [NewFPPCommandDispatcher]'s doc comment explains is safe for
	// *handlers. A nil field is replaced by [noMQTTBrokerRegistry]: every
	// method refuses loudly rather than fabricating success — this
	// struct's standing "an unwired dependency is not this API failing"
	// posture.
	MQTTBrokers MQTTBrokerRegistry

	// ResolumeRecovery is Track D seam D-3a's own recovery controller
	// (internal/coordinator/collector/resolume.Recovery), reached only
	// through [ResolumeRecoveryProvider]. A nil field is replaced by
	// [noResolumeRecoveryProvider]: the record reads empty, no restore
	// has ever run, and a manual restore refuses loudly — matching this
	// struct's standing "an unwired dependency is not this API failing"
	// posture.
	ResolumeRecovery ResolumeRecoveryProvider

	// ResolumeRecoverySettleSeconds threads [config.Config.ResolumeRecoverySettle]
	// through for the identical "this package does not read the
	// environment or config package state on its own" reason
	// [Dependencies.ResolumeID] documents. Rendered on GET
	// /resolume/recovery as settleDelaySeconds.
	ResolumeRecoverySettleSeconds float64

	// FPPMQTT is Track G seam G-3's live fpp.mqtt host map — see
	// [FPPMQTTHostLister]. A nil field is replaced by [noFPPMQTTHostLister],
	// under which the cross-check in handlePutFPPEndpointsConfig has
	// nothing to enforce, matching this struct's standing "an unwired
	// dependency is not this API failing" posture.
	FPPMQTT FPPMQTTHostLister

	// FPPMQTTSecret is Track G seam G-3's write-only credential surface for
	// the fpp.mqtt broker password (ADR-039 decision 7) — see
	// [FPPMQTTSecretStore]. A nil field is replaced by
	// [noFPPMQTTSecretStore], under which GET reports no password set and a
	// PUT naming one refuses loudly, matching every other unwired write
	// dependency's posture.
	FPPMQTTSecret FPPMQTTSecretStore

	// FPPMQTTEnvVarSet is [Dependencies.FPPEndpointsEnvVarSet]'s mirror for
	// Track G seam G-3: whether SHOWMESH_FPP_MQTT_BROKER_URL is currently
	// set in the coordinator PROCESS's environment. Consumed by
	// handlePutFPPMQTTConfig: a write is refused with 409 while this is
	// true (ADR-039 decision 4). The zero value (false) is the same
	// "nothing told this API otherwise" posture as every other unwired
	// dependency in this struct.
	FPPMQTTEnvVarSet bool

	// FPPMQTTMigrationDeferred is [Dependencies.FPPEndpointsMigrationDeferred]'s
	// mirror: this coordinator started with SHOWMESH_FPP_MQTT_BROKER_URL
	// set, tried to migrate it into the store, and could not persist that
	// write — see internal/coordinator's fppmqttsync.go. The zero value
	// (false) is the same "nothing told this API otherwise" posture as
	// every other unwired dependency here.
	FPPMQTTMigrationDeferred bool

	// NightSessions is Track F seam F2's own store dependency — see
	// [NightSessionStore]. A nil field is replaced by
	// [noNightSessionStore], under which every night-session read reports
	// "no session" and every command refuses with an internal error,
	// matching this struct's standing "an unwired dependency is not this
	// API failing" posture.
	NightSessions NightSessionStore

	// FPPObservations is the playlist-entry observation store dependency — see
	// [FPPObservationStore]. A nil field is replaced by
	// [noFPPObservationStore], under which GET reports an empty list and
	// POST refuses with an internal error, matching this struct's
	// standing "an unwired dependency is not this API failing" posture.
	FPPObservations FPPObservationStore

	// FPPPlaylistDefinitions is the playlist definition publication store
	// dependency — see [FPPPlaylistDefinitionStore]. A nil field is
	// replaced by [noFPPPlaylistDefinitionStore], under which GET reports
	// an empty list/not-found and POST refuses with an internal error,
	// matching this struct's standing "an unwired dependency is not this
	// API failing" posture.
	FPPPlaylistDefinitions FPPPlaylistDefinitionStore

	// FPPReconciliation is the reconciliation read route's own dependency:
	// see [FPPReconciliationStore]. Unlike AssetManifests, which is a
	// bare *store.Store because no interface can stand in for it, this
	// field IS an interface specifically so it can carry a nil-safe
	// refusing default: a nil field is replaced by
	// [noFPPReconciliationStore], under which the reconciliation GET
	// refuses with an internal error naming the missing wiring, matching
	// this struct's standing "an unwired dependency is not this API
	// failing" posture, and covered by
	// TestEveryRefusingDependencyIsWired (apidependencywiring_test.go).
	FPPReconciliation FPPReconciliationStore
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

// storeSatisfiesFPPObservationStore is [FPPObservationStore]'s identical
// compile-time assertion — *store.Store's Get/List/InTx already satisfy
// it with no adapter needed.
var _ FPPObservationStore = (*store.Store)(nil)

// storeSatisfiesFPPPlaylistDefinitionStore is [FPPPlaylistDefinitionStore]'s
// identical compile-time assertion.
var _ FPPPlaylistDefinitionStore = (*store.Store)(nil)

// storeFPPReconciliationSatisfiesFPPReconciliationStore is
// [FPPReconciliationStore]'s compile-time assertion. Unlike the ones
// above, this is against [StoreFPPReconciliation], the adapter, not
// *store.Store directly, because *store.Store itself has no
// ReconcileFPPPlaylistEntryObservation method (see that interface's own
// doc comment for why).
var _ FPPReconciliationStore = StoreFPPReconciliation{}

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
	if d.Render == nil {
		d.Render = noNodeRenderLister{}
	}
	if d.Audio == nil {
		d.Audio = noNodeAudioLister{}
	}
	if d.RenderPublisher == nil {
		d.RenderPublisher = noRenderPublisher{}
	}
	if d.AudioPublisher == nil {
		d.AudioPublisher = noAudioSessionPublisher{}
	}
	if d.AudioSessions == nil {
		d.AudioSessions = noAudioSessionStore{}
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
	if d.AssetSettings == nil {
		d.AssetSettings = noAssetSettingsSource{}
	}
	if d.AssetSyncNudger == nil {
		d.AssetSyncNudger = noAssetSyncNudger{}
	}
	if d.Resolume == nil {
		d.Resolume = noResolumeLister{}
	}
	if d.ResolumeReferences == nil {
		d.ResolumeReferences = noResolumeReferenceResolver{}
	}
	if d.MQTTBrokers == nil {
		d.MQTTBrokers = noMQTTBrokerRegistry{}
	}
	if d.ResolumeRecovery == nil {
		d.ResolumeRecovery = noResolumeRecoveryProvider{}
	}
	if d.FPPMQTT == nil {
		d.FPPMQTT = noFPPMQTTHostLister{}
	}
	if d.FPPMQTTSecret == nil {
		d.FPPMQTTSecret = noFPPMQTTSecretStore{}
	}
	if d.NightSessions == nil {
		d.NightSessions = noNightSessionStore{}
	}
	if d.FPPObservations == nil {
		d.FPPObservations = noFPPObservationStore{}
	}
	if d.FPPPlaylistDefinitions == nil {
		d.FPPPlaylistDefinitions = noFPPPlaylistDefinitionStore{}
	}
	if d.FPPReconciliation == nil {
		d.FPPReconciliation = noFPPReconciliationStore{}
	}
	return d
}

// noFPPObservationStore is [Dependencies.FPPObservations]'s nil-safe
// default: GET reports an empty list (not an error — an unwired store and
// a store with no observations yet are indistinguishable to a reader) and
// InTx refuses with an internal error, matching every other unwired
// write-capable dependency's identical posture in this file.
type noFPPObservationStore struct{}

func (noFPPObservationStore) GetFPPPlaylistEntryObservation(context.Context, string) (store.FPPPlaylistEntryObservationRecord, error) {
	return store.FPPPlaylistEntryObservationRecord{}, store.ErrFPPPlaylistEntryObservationNotFound
}

func (noFPPObservationStore) ListFPPPlaylistEntryObservations(context.Context) ([]store.FPPPlaylistEntryObservationRecord, error) {
	return nil, nil
}

func (noFPPObservationStore) InTx(context.Context, func(context.Context, *store.Tx) error) error {
	return fmt.Errorf("api: fpp observation store not wired in")
}

// noFPPPlaylistDefinitionStore is [Dependencies.FPPPlaylistDefinitions]'s
// nil-safe default, mirroring [noFPPObservationStore]'s identical posture.
type noFPPPlaylistDefinitionStore struct{}

func (noFPPPlaylistDefinitionStore) GetFPPPlaylistDefinition(context.Context, string, string) (store.FPPPlaylistDefinitionRecord, error) {
	return store.FPPPlaylistDefinitionRecord{}, store.ErrFPPPlaylistDefinitionNotFound
}

func (noFPPPlaylistDefinitionStore) ListFPPPlaylistDefinitions(context.Context) ([]store.FPPPlaylistDefinitionRecord, error) {
	return nil, nil
}

func (noFPPPlaylistDefinitionStore) InTx(context.Context, func(context.Context, *store.Tx) error) error {
	return fmt.Errorf("api: fpp playlist definition store not wired in")
}

// noFPPReconciliationStore is [Dependencies.FPPReconciliation]'s nil-safe
// default: it never calls [fppreconcile.Reconcile] against a nil store,
// it just returns the refusal; see [FPPReconciliationStore]'s own doc
// comment for why this field can have one when AssetManifests cannot.
type noFPPReconciliationStore struct{}

func (noFPPReconciliationStore) ReconcileFPPPlaylistEntryObservation(context.Context, store.FPPPlaylistEntryObservationRecord) (fppreconcile.Result, error) {
	return fppreconcile.Result{}, fmt.Errorf("api: fpp reconciliation store not wired in")
}

func (noFPPReconciliationStore) PlaylistReadinessForFPPPlaylist(context.Context, string, int64, config.ShowPlaylistPayload) (fppreconcile.Report, error) {
	return fppreconcile.Report{}, fmt.Errorf("api: fpp reconciliation store not wired in")
}

// noNightSessionStore is [Dependencies.NightSessions]'s nil-safe default:
// every read reports "no session has ever been created" and every write
// refuses with an internal error, matching this package's standing
// "an unwired dependency is not this API failing" posture.
type noNightSessionStore struct{}

func (noNightSessionStore) CreateNightSession(context.Context, store.NightSessionRecord, time.Time) error {
	return fmt.Errorf("api: night session store not wired in")
}

func (noNightSessionStore) GetNightSession(context.Context, string) (store.NightSessionRecord, error) {
	return store.NightSessionRecord{}, store.ErrNightSessionNotFound
}

func (noNightSessionStore) GetCurrentNightSession(context.Context) (store.NightSessionRecord, bool, error) {
	return store.NightSessionRecord{}, false, nil
}

func (noNightSessionStore) UpdateNightSession(context.Context, store.NightSessionRecord, time.Time) error {
	return fmt.Errorf("api: night session store not wired in")
}

func (noNightSessionStore) CreateNightReadiness(context.Context, store.NightReadinessRecord) error {
	return fmt.Errorf("api: night session store not wired in")
}

func (noNightSessionStore) GetLatestNightReadiness(context.Context, string) (store.NightReadinessRecord, error) {
	return store.NightReadinessRecord{}, store.ErrNightReadinessNotFound
}

func (noNightSessionStore) GetNightSessionByIdempotencyKey(context.Context, string) (store.NightSessionRecord, error) {
	return store.NightSessionRecord{}, store.ErrNightSessionNotFound
}

func (noNightSessionStore) InTx(context.Context, func(context.Context, *store.Tx) error) error {
	return fmt.Errorf("api: night session store not wired in")
}

func (noNightSessionStore) InsertNightCueOutboxRow(context.Context, store.NightCueOutboxRecord, time.Time) error {
	return fmt.Errorf("api: night session store not wired in")
}

func (noNightSessionStore) GetNightCueOutboxRow(context.Context, string, int64, string, string) (store.NightCueOutboxRecord, error) {
	return store.NightCueOutboxRecord{}, store.ErrNightCueOutboxNotFound
}

func (noNightSessionStore) ListNightCueOutboxRows(context.Context, string, int64) ([]store.NightCueOutboxRecord, error) {
	return nil, nil
}

func (noNightSessionStore) ListNightCueOutboxRowsForPhase(context.Context, string, string) ([]store.NightCueOutboxRecord, error) {
	return nil, nil
}

func (noNightSessionStore) ListNightCueOutboxRowsForPhasePrefix(context.Context, string, string) ([]store.NightCueOutboxRecord, error) {
	return nil, nil
}

func (noNightSessionStore) UpdateNightCueOutboxRow(context.Context, store.NightCueOutboxRecord) error {
	return fmt.Errorf("api: night session store not wired in")
}

// noFPPMQTTHostLister is [Dependencies.FPPMQTT]'s nil-safe default:
// CurrentHosts always answers empty-and-successful, matching every other
// no-op lister in this package.
type noFPPMQTTHostLister struct{}

func (noFPPMQTTHostLister) CurrentHosts(context.Context) (map[string]string, error) {
	return nil, nil
}

// noFPPMQTTSecretStore is [Dependencies.FPPMQTTSecret]'s nil-safe default:
// Has reports no password set, Set/Clear refuse loudly — matching every
// other unwired write dependency's "refuse loudly, never fabricate
// success" posture under this default.
type noFPPMQTTSecretStore struct{}

func (noFPPMQTTSecretStore) HasFPPMQTTPassword(context.Context) (bool, error) { return false, nil }
func (noFPPMQTTSecretStore) SetFPPMQTTPassword(context.Context, string) error {
	return errors.New("api: no fpp.mqtt secret store wired in")
}
func (noFPPMQTTSecretStore) ClearFPPMQTTPassword(context.Context) error {
	return errors.New("api: no fpp.mqtt secret store wired in")
}

// noAssetSyncNudger is [Dependencies.AssetSyncNudger]'s nil-safe default:
// Nudge does nothing, which is exactly the pre-nudge behavior (wait out
// [assetsync.Service]'s own interval) — matching [noFPPPollNudger]'s
// identical shape one field over.
type noAssetSyncNudger struct{}

func (noAssetSyncNudger) Nudge() {}

// defaultAssetManifestInventoryInterval mirrors
// internal/coordinator/config's own defaultAssetInventoryInterval (2
// minutes). Duplicated, not imported: that constant is unexported, and
// this package must not import internal/coordinator/config for a value —
// the same posture [Dependencies.FPPEndpointsEnvVarSet]'s doc comment
// states for reading the environment. Only reached through
// [noAssetSettingsSource], which production wiring never uses (coordinator.go
// always wires a real *assetsync.Service).
const defaultAssetManifestInventoryInterval = 2 * time.Minute

// noAssetSettingsSource is [Dependencies.AssetSettings]'s nil-safe default:
// it reproduces exactly the defaults the old startup-snapshot fields
// (AssetMaxUploadBytes, AssetInventoryInterval, AssetSyncEnabled) used to
// fall back to — an unset upload limit became [assetstore.DefaultMaxUploadBytes],
// an unset inventory interval became [defaultAssetManifestInventoryInterval],
// and an empty ContentBaseURL (this type's own zero value) is exactly what
// AssetSyncEnabled's own false default meant.
type noAssetSettingsSource struct{}

func (noAssetSettingsSource) ContentBaseURL() string { return "" }
func (noAssetSettingsSource) MaxUploadBytes() int64  { return assetstore.DefaultMaxUploadBytes }
func (noAssetSettingsSource) InventoryInterval() time.Duration {
	return defaultAssetManifestInventoryInterval
}

// noResolumeReferenceResolver is [Dependencies.ResolumeReferences]'s
// nil-safe default: every method reports
// [config.ErrResolumeCompositionNotUploaded], the same answer a real
// resolver gives when nothing has ever been uploaded — an unwired
// dependency and a genuinely empty one are indistinguishable from a
// caller's point of view, which is correct: neither has anything to
// resolve against.
type noResolumeReferenceResolver struct{}

func (noResolumeReferenceResolver) ResolveClip(config.ResolumeClipReference) error {
	return config.ErrResolumeCompositionNotUploaded
}

func (noResolumeReferenceResolver) ResolveLayer(string) error {
	return config.ErrResolumeCompositionNotUploaded
}

func (noResolumeReferenceResolver) ResolveColumn(string, string) error {
	return config.ErrResolumeCompositionNotUploaded
}

func (noResolumeReferenceResolver) ResolveDeck(string) error {
	return config.ErrResolumeCompositionNotUploaded
}

// noResolumeLister is [Dependencies.Resolume]'s nil-safe default:
// ListInstances always answers empty-and-successful, matching every other
// no-op lister in this package (noNodeLister, noFPPLister) — an
// unconfigured or unwired Resolume dependency is a real, honest "nothing
// configured", never an error.
type noResolumeLister struct{}

func (noResolumeLister) ListInstances(context.Context) ([]ResolumeInstanceView, error) {
	return nil, nil
}

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

// noNodeRenderLister is [Dependencies.Render]'s nil-safe default: every node
// renders with no render evidence, matching every other no-op lister in
// this package (noNodeLister, noFPPLister) — an unwired render dependency
// is a real, honest "nothing reported", never an error.
type noNodeRenderLister struct{}

func (noNodeRenderLister) NodeRenderObservations(string) []observation.Observation { return nil }

// noNodeAudioLister is [Dependencies.Audio]'s nil-safe default, matching
// [noNodeRenderLister]'s identical posture one dependency over.
type noNodeAudioLister struct{}

func (noNodeAudioLister) NodeAudioObservations(string) []observation.Observation { return nil }

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

// noRenderPublisher is [Dependencies.RenderPublisher]'s no-op default:
// every render.* dispatch fails loudly rather than silently pretending a
// command reached a node.
type noRenderPublisher struct{}

var errRenderPublisherNotConfigured = errors.New("api: no render command publisher is configured on this coordinator")

func (noRenderPublisher) Publish(context.Context, string, byte, bool, []byte) error {
	return errRenderPublisherNotConfigured
}

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

	// NightReadinessMaxAge is invariant 2's own "configured maximum age":
	// how old a completed run-readiness result for the CURRENT preparation
	// epoch may be and still satisfy start-night. Not a night.session
	// config field (ADR-039: this is an operational tuning knob, not
	// something an operator must set for the subsystem to function — the
	// night.session kind stays silent on it, matching
	// FPPCommandConfirmDeadline's identical posture one field up). A
	// SHOWMESH HYPOTHESIS, not a measured value: long enough that an
	// operator running readiness at 4:15 PM for a 5:00 PM start-night is
	// not forced to rerun it, short enough that a readiness result from
	// hours earlier is not silently treated as still current. Defaults to
	// 30 minutes.
	NightReadinessMaxAge time.Duration

	// NightLoopInterval is [NightLoop]'s own tick period (nightloop.go,
	// Track F seam F3) — how often it checks the current night session for
	// a state it must autonomously advance. It never dispatches an FPP
	// command on every tick (nightEnsureAnchor's own idempotency-key reuse
	// makes a repeat tick a cheap observation poll, not a reissue), so this
	// is a responsiveness/staleness knob, not a rate limit. A SHOWMESH
	// HYPOTHESIS, not a measured value: short enough that a boundary is
	// noticed promptly against F0's own whole-second position precision,
	// without polling FPP or the store needlessly often. Defaults to 1
	// second.
	NightLoopInterval time.Duration
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

	// defaultNightReadinessMaxAge backs [Options.NightReadinessMaxAge].
	// See that field's doc comment — a SHOWMESH HYPOTHESIS.
	defaultNightReadinessMaxAge = 30 * time.Minute

	// defaultNightLoopInterval backs [Options.NightLoopInterval]. See that
	// field's doc comment — a SHOWMESH HYPOTHESIS.
	defaultNightLoopInterval = 1 * time.Second
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
	if o.NightReadinessMaxAge <= 0 {
		o.NightReadinessMaxAge = defaultNightReadinessMaxAge
	}
	if o.NightLoopInterval <= 0 {
		o.NightLoopInterval = defaultNightLoopInterval
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
		nightReadinessMaxAge:      opts.NightReadinessMaxAge,
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

	// Track B seam B2b-front: dispatch the three agent render.* operations
	// (renderdispatch.go). Guarded by render:command, matching
	// fpp:command/resolume:action's identical "reads open, this write
	// isn't" posture.
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/apply", h.writeGuard(&scopeRenderCommand, h.handleRenderSurfaceApply))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/clear", h.writeGuard(&scopeRenderCommand, h.handleRenderSurfaceClear))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/restart", h.writeGuard(&scopeRenderCommand, h.handleRenderPipelineRestart))
	// Track B seam B4: dispatch render.transport.probe — a COMMAND (it
	// starts a real gst-launch-1.0 subprocess on the node), never reachable
	// by GET (ADR-024: no state change is reachable by GET), same
	// render:command scope and same evidence-confirmation discipline as
	// apply/clear/restart above. showmeshctl render transport (a read of
	// last-known evidence) is a different, pre-existing surface — this is
	// the "go find out now" counterpart.
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/render/surfaces/{surfaceId}/transport-probe", h.writeGuard(&scopeRenderCommand, h.handleRenderTransportProbe))

	// Dispatch the agent's audio.session.* operations (audiodispatch.go).
	// Guarded by audio:command, matching render:command's identical
	// "reads open, this write isn't" posture.
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/apply", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionApply))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/prepare", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionPrepare))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/start", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionStart))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/pause", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionPause))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/resume", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionResume))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/seek", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionSeek))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/advance", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionAdvance))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/stop", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionStop))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/clear", h.writeGuard(&scopeAudioCommand, h.handleAudioSessionClear))

	// The four remaining reserved audio.gain.*/audio.output.* operations,
	// same dispatch core and same scope.
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/gain", h.writeGuard(&scopeAudioCommand, h.handleAudioGainSet))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/gain/fade", h.writeGuard(&scopeAudioCommand, h.handleAudioGainFade))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/output/mute", h.writeGuard(&scopeAudioCommand, h.handleAudioOutputMute))
	mux.HandleFunc("POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/output/unmute", h.writeGuard(&scopeAudioCommand, h.handleAudioOutputUnmute))

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

	// Track G seam G-2 (ADR-039): resolume.instances, mirroring
	// fpp.endpoints in every respect including the always-config:write,
	// never-CloseReads read posture — see config.go's own top comment and
	// resolumeinstancesconfig.go's own file comment.
	mux.HandleFunc("GET /api/v1/config/resolume.instances", h.requireScope(identity.ScopeConfigWrite, h.handleGetResolumeInstancesConfig))
	mux.HandleFunc("PUT /api/v1/config/resolume.instances", h.writeGuard(&scopeConfigWrite, h.handlePutResolumeInstancesConfig))
	mux.HandleFunc("GET /api/v1/config/resolume.instances/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetResolumeInstancesConfigRevisions))

	// Track G seam G-3 (ADR-039): fpp.mqtt, mirroring fpp.endpoints in every
	// respect except that PUT is a partial update over several independent
	// fields (including the write-only broker password) rather than one
	// required array — see fppmqttconfig.go's own file comment.
	mux.HandleFunc("GET /api/v1/config/fpp.mqtt", h.requireScope(identity.ScopeConfigWrite, h.handleGetFPPMQTTConfig))
	mux.HandleFunc("PUT /api/v1/config/fpp.mqtt", h.writeGuard(&scopeConfigWrite, h.handlePutFPPMQTTConfig))
	mux.HandleFunc("GET /api/v1/config/fpp.mqtt/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetFPPMQTTConfigRevisions))

	// Track G seam G-4 (ADR-039): assets.settings, mirroring
	// resolume.instances in every respect including the always-config:write,
	// never-CloseReads read posture and the still-set-env-var 409 — see
	// config.go's own top comment and assetssettingsconfig.go's own file
	// comment. Unlike resolume.instances, PUT supports a partial update
	// (each field independently optional).
	mux.HandleFunc("GET /api/v1/config/assets.settings", h.requireScope(identity.ScopeConfigWrite, h.handleGetAssetsSettingsConfig))
	mux.HandleFunc("PUT /api/v1/config/assets.settings", h.writeGuard(&scopeConfigWrite, h.handlePutAssetsSettingsConfig))
	mux.HandleFunc("GET /api/v1/config/assets.settings/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetAssetsSettingsConfigRevisions))

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

	// A pre-show binding check (ADR-029's own Consequences section),
	// re-resolving a stored show.action's target against current
	// integration state. Never gated by any scope — this is a read,
	// dispatches nothing, and must be reachable with no credential
	// (ADR-024 constraint 23) — see actionbinding.go.
	mux.HandleFunc("GET /api/v1/actions/{id}/binding", h.handleGetActionBinding)
	mux.HandleFunc("GET /api/v1/actions/bindings", h.handleListActionBindings)

	// Invoke one stored show.action by id, outside of a macro run
	// (ADR-037 decision 8's controller surface). show:action:invoke is
	// the only scope check on this route — see that scope's own doc
	// comment (identity/types.go) for why. No state change here is
	// reachable by GET — see actioninvoke.go.
	mux.HandleFunc("POST /api/v1/actions/{id}/invocations", h.writeGuard(&scopeActionInvoke, h.handleInvokeAction))

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

	// --- Track H seam H1: show.cue and show.playlist ---
	//
	// Same route shape as show/show.surface immediately above: reads use
	// readAnyGuard(showConfigReadScopes, ...), writes use
	// writeGuard(&scopeConfigWrite, ...). Both are operator-chosen
	// collections with the usual four routes each (TRACK-H-H1-SPEC.md
	// section 6). No new scope: config:write already guards every
	// configuration write (section 7).
	mux.HandleFunc("GET /api/v1/config/show.cue", h.readAnyGuard(showConfigReadScopes, h.handleListShowCues))
	mux.HandleFunc("GET /api/v1/config/show.cue/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShowCue))
	mux.HandleFunc("PUT /api/v1/config/show.cue/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShowCue))
	mux.HandleFunc("GET /api/v1/config/show.cue/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowCueRevisions))

	mux.HandleFunc("GET /api/v1/config/show.playlist", h.readAnyGuard(showConfigReadScopes, h.handleListShowPlaylists))
	mux.HandleFunc("GET /api/v1/config/show.playlist/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetShowPlaylist))
	mux.HandleFunc("PUT /api/v1/config/show.playlist/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutShowPlaylist))
	mux.HandleFunc("GET /api/v1/config/show.playlist/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetShowPlaylistRevisions))

	// --- Track F seam F1: night.session and its active-session pointer ---
	//
	// Same route shape as show/show.surface/show.active immediately above:
	// reads use readAnyGuard(showConfigReadScopes, ...), writes use
	// writeGuard(&scopeConfigWrite, ...). "night.session" is a collection
	// (the usual four routes); "night.session.active" is a singleton (fixed
	// id, no {id} path segment) with three. "night.session.active" is a
	// distinct literal path segment from "night.session" for the identical
	// net/http.ServeMux segment-matching reason TestShowActiveRouteIsNotSwallowedByShowIDRoute
	// exists one kind over.
	mux.HandleFunc("GET /api/v1/config/night.session", h.readAnyGuard(showConfigReadScopes, h.handleListNightSessions))
	mux.HandleFunc("GET /api/v1/config/night.session/{id}", h.readAnyGuard(showConfigReadScopes, h.handleGetNightSession))
	mux.HandleFunc("PUT /api/v1/config/night.session/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutNightSession))
	mux.HandleFunc("GET /api/v1/config/night.session/{id}/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetNightSessionRevisions))
	mux.HandleFunc("GET /api/v1/config/night.session/{id}/revisions/{revision}", h.readAnyGuard(showConfigReadScopes, h.handleGetNightSessionRevision))

	mux.HandleFunc("GET /api/v1/config/night.session.active", h.readAnyGuard(showConfigReadScopes, h.handleGetNightSessionActive))
	mux.HandleFunc("PUT /api/v1/config/night.session.active", h.writeGuard(&scopeConfigWrite, h.handlePutNightSessionActive))
	mux.HandleFunc("GET /api/v1/config/night.session.active/revisions", h.readAnyGuard(showConfigReadScopes, h.handleGetNightSessionActiveRevisions))

	// --- Track F seam F2: the night-session lifecycle controller ---
	// (RESTING-MODE.md, ADR-038). Reads stay open by default (ADR-024
	// constraint 23: a credential problem must never cost the operator
	// sight of the lifecycle state) — night:command guards only the write.
	//
	// The two GETs below deliberately reuse [identity.ScopeObservationRead]
	// rather than minting a new night-session read scope. This is a
	// considered choice, not a shortcut: ScopeObservationRead is already
	// how [readScopes] and every [identity.RoleViewer] principal sees
	// coordinator-observed state with no write authority attached, and
	// night-session lifecycle state is exactly that shape for a read
	// credential — a viewer who can see FPP/node observations must also be
	// able to see whether the show is live, resting, or degraded, for the
	// identical "never cost the operator sight of the show" reason. A
	// dedicated read scope would only let a future role bundle grant
	// observation:read without night visibility or vice versa, a split
	// this architecture has no use case for: nothing here is ever gated
	// behind h.closeReads without also wanting this. resolume/instances
	// (below) reuses the same scope for the identical reason one section
	// down.
	scopeNightCommand := identity.ScopeNightCommand
	mux.HandleFunc("GET /api/v1/night/session", h.readGuard(identity.ScopeObservationRead, h.handleGetNightLifecycle))
	mux.HandleFunc("GET /api/v1/night/sessions/{id}", h.readGuard(identity.ScopeObservationRead, h.handleGetNightLifecycleByID))
	mux.HandleFunc("POST /api/v1/night/commands/{command}", h.writeGuard(&scopeNightCommand, h.handleNightCommand))

	// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1.1: the installed FPP plugin's own evidence-ingestion
	// route, behind fpp:observe rather than fpp:command — accepting
	// plugin evidence grants no execution authority (§1.6's own closing
	// rule), so this is deliberately not the same scope that dispatches
	// FPP's native commands. GET stays open under observation:read,
	// matching every other FPP read surface.
	mux.HandleFunc("POST /api/v1/integrations/fpp/playlist-entry-observations", h.writeGuard(&scopeFPPObserve, h.handlePostFPPPlaylistEntryObservation))
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlist-entry-observations", h.readGuard(identity.ScopeObservationRead, h.handleListFPPPlaylistEntryObservations))

	// TRACK-H-H2-SPEC.md §5.1: the sequence-reset recovery route. Guarded
	// by fpp:command, deliberately NOT fpp:observe — see
	// handleDeleteFPPPlaylistEntryObservation's own doc comment
	// (fppobservations.go) for why clearing evidence and manufacturing it
	// are different powers.
	mux.HandleFunc("DELETE /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}",
		h.writeGuard(&scopeFPPCommand, h.handleDeleteFPPPlaylistEntryObservation))

	// TRACK-H-H2-SPEC.md §5, §7: the reconciliation read route, open under
	// observation:read like every other FPP read surface — see
	// fppreconciliation.go's own doc comment.
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}/reconciliation",
		h.readGuard(identity.ScopeObservationRead, h.handleGetFPPPlaylistEntryReconciliation))

	// TRACK-H-H2-SPEC.md §6, §7: the readiness read route, open under
	// observation:read like the reconciliation route above: "readiness
	// nobody can see is not readiness."
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlists/{playlistId}/readiness",
		h.readGuard(identity.ScopeObservationRead, h.handleGetFPPPlaylistReadiness))

	// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3, TRACK-H-H2-SPEC.md §3-4: playlist definition
	// publication. POST shares fpp:observe with the observation route
	// above (§3: "The same principal, the same credential, and the same
	// fpp:observe scope as section 1."), never fpp:command — publishing a
	// definition grants no execution authority either. The two GETs stay
	// open under observation:read, matching every other FPP read surface
	// (§3.6), including the entries preview route H2 spec §4 step 2 adds
	// on top of the contract's own two.
	mux.HandleFunc("POST /api/v1/integrations/fpp/playlist-definitions", h.writeGuard(&scopeFPPObserve, h.handlePostFPPPlaylistDefinition))
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlist-definitions", h.readGuard(identity.ScopeObservationRead, h.handleListFPPPlaylistDefinitions))
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}",
		h.readGuard(identity.ScopeObservationRead, h.handleGetFPPPlaylistDefinition))
	mux.HandleFunc("GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}/entries",
		h.readGuard(identity.ScopeObservationRead, h.handleGetFPPPlaylistDefinitionEntries))

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
	// GET /api/v1/resolume/recovery (Track D seam D-3a): the open read —
	// never gated, per ADR-024's reads-stay-open posture and the build
	// contract §1.3's own "the dashboard renders with no session"
	// requirement. POST .../restore requires resolume:action, the same
	// scope every other Resolume write requires. GET/PUT
	// /config/resolume.recovery mirror /config/fpp.endpoints's own
	// config:write-only posture (resolumerecovery.go).
	mux.HandleFunc("GET /api/v1/resolume/recovery", h.handleGetResolumeRecovery)
	mux.HandleFunc("POST /api/v1/resolume/recovery/restore", h.writeGuard(&scopeResolumeAction, h.handlePostResolumeRecoveryRestore))
	mux.HandleFunc("GET /api/v1/config/resolume.recovery", h.requireScope(identity.ScopeConfigWrite, h.handleGetResolumeRecoveryConfig))
	mux.HandleFunc("PUT /api/v1/config/resolume.recovery", h.writeGuard(&scopeConfigWrite, h.handlePutResolumeRecoveryConfig))
	mux.HandleFunc("GET /api/v1/config/resolume.recovery/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetResolumeRecoveryConfigRevisions))

	// GET/PUT /api/v1/config/render.settings (Track B seam B2c, ADR-039):
	// the idle-output/restart-policy singleton. Mirrors
	// /config/resolume.recovery's config:write-only posture exactly
	// (rendersettings.go) — no open read exists for this kind.
	mux.HandleFunc("GET /api/v1/config/render.settings", h.requireScope(identity.ScopeConfigWrite, h.handleGetRenderSettingsConfig))
	mux.HandleFunc("PUT /api/v1/config/render.settings", h.writeGuard(&scopeConfigWrite, h.handlePutRenderSettingsConfig))
	mux.HandleFunc("GET /api/v1/config/render.settings/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetRenderSettingsConfigRevisions))

	// GET/PUT /api/v1/config/audio.settings (Track C seam C1b, ADR-039):
	// the engine-wide operator-settings singleton. Mirrors
	// /config/render.settings' config:write-only posture exactly
	// (audiosettings.go).
	mux.HandleFunc("GET /api/v1/config/audio.settings", h.requireScope(identity.ScopeConfigWrite, h.handleGetAudioSettingsConfig))
	mux.HandleFunc("PUT /api/v1/config/audio.settings", h.writeGuard(&scopeConfigWrite, h.handlePutAudioSettingsConfig))
	mux.HandleFunc("GET /api/v1/config/audio.settings/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetAudioSettingsConfigRevisions))

	// GET/PUT /api/v1/config/audio.node/{id} (Track C seam C1b, ADR-039): a
	// collection keyed by node id, mirroring show.surface's four-route
	// shape (audionode.go). Gated by config:write only, matching
	// render.settings/audio.settings rather than show.surface's open
	// showConfigReadScopes posture: this is nearer principal/physical-
	// interface management than show-programming state.
	mux.HandleFunc("GET /api/v1/config/audio.node", h.requireScope(identity.ScopeConfigWrite, h.handleListAudioNodes))
	mux.HandleFunc("GET /api/v1/config/audio.node/{id}", h.requireScope(identity.ScopeConfigWrite, h.handleGetAudioNode))
	mux.HandleFunc("PUT /api/v1/config/audio.node/{id}", h.writeGuard(&scopeConfigWrite, h.handlePutAudioNode))
	mux.HandleFunc("GET /api/v1/config/audio.node/{id}/revisions", h.requireScope(identity.ScopeConfigWrite, h.handleGetAudioNodeRevisions))

	// GET /api/v1/resolume/instances and /instances/{instanceId} (Track D
	// seam E): Resolume as a first-class observability resource. "instances"
	// is an explicit path segment, not a bare {id} under /resolume/, because
	// /resolume/actions already exists and /resolume/recovery is being added
	// in parallel — see resolumeinstances.go's own doc comment. Guarded by
	// observation:read, the same guard GET /observations uses: this is
	// telemetry, not configuration, so it follows ADR-024 decision 4's
	// pre-existing open-by-default read posture rather than requireScope's
	// always-sensitive one (compare GET /config/resolume/composition, which
	// deliberately uses the latter).
	mux.HandleFunc("GET /api/v1/resolume/instances", h.readGuard(identity.ScopeObservationRead, h.handleResolumeInstances))
	mux.HandleFunc("GET /api/v1/resolume/instances/{instanceId}", h.readGuard(identity.ScopeObservationRead, h.handleResolumeInstance))

	// --- Track G seam G-5: identity administration over the API ---
	//
	// Reads require principal:read; writes require principal:write and go
	// through writeGuard (decision 6's CSRF check comes free from that one
	// guard, exactly like every other write in this package). Neither is
	// ever gated by [Options.CloseReads] — identity administration is
	// exactly as sensitive as GET /api/v1/audit, never one of ADR-024
	// decision 4's four pre-existing open-by-default read resources.
	// Bootstrap (creating the FIRST principal) has no route here — ADR-024
	// decision 9 keeps it coordinator-local, since there is no principal
	// yet to authenticate this surface's own guards against. See
	// principals.go's own doc comment for the fail-closed audit pattern
	// every write below follows, and for [handlers.wouldLockOutAdministration],
	// which guards role change, disable, and token revoke against removing
	// the coordinator's last reachable administrator (requirement 3 /
	// ADR-039 decision 8).
	mux.HandleFunc("GET /api/v1/principals", h.requireScope(identity.ScopePrincipalRead, h.handleListPrincipals))
	mux.HandleFunc("POST /api/v1/principals", h.writeGuard(&scopePrincipalWrite, h.handleCreatePrincipal))
	mux.HandleFunc("GET /api/v1/principals/{id}", h.requireScope(identity.ScopePrincipalRead, h.handleGetPrincipal))
	mux.HandleFunc("PUT /api/v1/principals/{id}/role", h.writeGuard(&scopePrincipalWrite, h.handleSetPrincipalRole))
	mux.HandleFunc("POST /api/v1/principals/{id}/enable", h.writeGuard(&scopePrincipalWrite, h.handleEnablePrincipal))
	mux.HandleFunc("POST /api/v1/principals/{id}/disable", h.writeGuard(&scopePrincipalWrite, h.handleDisablePrincipal))
	mux.HandleFunc("POST /api/v1/principals/{id}/password", h.writeGuard(&scopePrincipalWrite, h.handleResetPrincipalPassword))
	mux.HandleFunc("GET /api/v1/principals/{id}/tokens", h.requireScope(identity.ScopePrincipalRead, h.handleListPrincipalTokens))
	mux.HandleFunc("POST /api/v1/principals/{id}/tokens", h.writeGuard(&scopePrincipalWrite, h.handleIssuePrincipalToken))
	mux.HandleFunc("DELETE /api/v1/principals/{id}/tokens/{tokenId}", h.writeGuard(&scopePrincipalWrite, h.handleRevokeToken))

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
