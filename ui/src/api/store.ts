/**
 * The live model store: owns the connection state machine, the
 * snapshot+stream protocol from ADR-020, and the `Model` seam C reads
 * through `useModel()`. Framework-free — testable and (per spec section
 * 5) actually tested with no React present, driven directly against a
 * real `node:http` server.
 *
 * Connection algorithm, in outline (ADR-020, spec section 5.1):
 *
 *   1. Open `GET /stream?deltas=1` — this store always opts into
 *      observation delta frames (ADR-023 decision 1). A coordinator that
 *      has never heard of `?deltas=1` serves exactly what it always did;
 *      only the literal query value `1` changes anything server-side, so
 *      this is safe to send unconditionally. Read frames as they arrive.
 *   2. The first frame is always `stream.start`. On it — and on any
 *      later `stream.reset` — fetch `GET /snapshot` (then `GET /events`
 *      for initial history) and apply them, BEFORE processing any frame
 *      that arrived after it. This is enforced structurally, not just by
 *      convention: frame processing is a single sequential `await` loop
 *      (runConnectionAttempt), so the loop simply does not advance to
 *      the next frame until the snapshot fetch it is waiting on
 *      resolves. There is no separate delta buffer to keep in sync,
 *      because nothing reads ahead while a snapshot fetch is in flight.
 *   3. `node.changed` / `fpp.changed` / `event.recorded` frames update
 *      the model in place once a snapshot has been applied.
 *      `fpp.observations.changed` frames also update the model in place,
 *      but with OPPOSITE merge semantics from `fpp.changed` — see
 *      `applyFppChanged` (replace) versus `applyFppObservationsChanged`
 *      (merge `changed`, delete `removed`) below, and ADR-023 decision 3a,
 *      which names confusing the two as the entire failure mode this
 *      feature can introduce that a full-frame-only client could not have.
 *   4. Any interruption — the stream closing, a network error, a
 *      `stream.reset` whose connection then closes — is handled the
 *      same way: reconnect (with backoff) and start over from step 1.
 *      Nothing here tries to distinguish a clean coordinator shutdown
 *      from a network fault, because the wire protocol does not let it
 *      (api/openapi.yaml's /stream description) and OPERATOR-UI section
 *      7 does not ask it to. This is also, structurally, why deltas
 *      introduce no resume logic: ADR-023 decision 4 is that a delta
 *      applies only to the baseline this store already holds, and step 2
 *      already guarantees that baseline is re-fetched from scratch on
 *      every reconnect — there is no cursor anywhere in this file, and
 *      none should ever be added; a gap in the stream is handled by
 *      re-snapshotting, never by reconciling.
 *
 * What this store deliberately does NOT do: apply a whole-RESOURCE
 * delete. The stream carries no node/FPP-instance deletions in v1
 * (api/openapi.yaml's /stream description, spec section 5.1) — a node or
 * FPP instance dropped from the coordinator's inventory produces no
 * frame, so `model.nodes` and the membership of `model.fpp` can only ever
 * be replaced wholesale by the next snapshot, never shrunk by a delta. If
 * a future contributor is tempted to add a `node.removed` handler here:
 * don't, until the wire contract adds the event that would drive it. This
 * is distinct from `fpp.observations.changed`'s `removed` list, which
 * this store DOES apply as a deletion — but only of individual
 * observations within an FPP instance that remains present, never of the
 * instance itself (ADR-023's own scoping of that field).
 */
import {
  ACTION_INVOKE_REQUEST_TIMEOUT_MS,
  ApiClient,
  AUDIO_COMMAND_REQUEST_TIMEOUT_MS,
  FPP_COMMAND_REQUEST_TIMEOUT_MS,
  RENDER_COMMAND_REQUEST_TIMEOUT_MS,
  RESOLUME_ACTION_REQUEST_TIMEOUT_MS,
  RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS,
  type FetchLike,
} from './client'
import { uploadFileWithProgress, type UploadProgress } from './resolumeCompositionUpload'
import { computeBackoffMs, DEFAULT_BACKOFF, type BackoffConfig } from './backoff'
import { SYSTEM_CLOCK, type Clock, type TimerHandle } from './clock'
import {
  asEventSeq,
  initialModel,
  type AudioSessionCommandResult,
  type ConnectionState,
  type CurrentRunsResponse,
  type CueCatalogDeployResult,
  type Evidence,
  type Event as ModelEvent,
  type EventSeq,
  type FPPCommandResult,
  type FPPInstance,
  type FPPPlaylistEntryObservation,
  type Model,
  type NightCommandName,
  type RenderCommandResult,
  type ResolumeInstance,
} from './domain'
import { IncompatibleVersionError, UnauthorizedError, isAbortError } from './errors'
import { SSEParser, type SSEFrame } from './sse'
import { sleep, waitUntilAborted } from './async-utils'
import { clearStoredToken, setStoredToken } from './token'
import { randomUUIDv4 } from './uuid'
import type { components } from './generated/schema'

type SchemaSnapshot = components['schemas']['Snapshot']
type SchemaEventsResponse = components['schemas']['EventsResponse']
type SchemaNode = components['schemas']['Node']
type SchemaFPPInstance = components['schemas']['FPPInstance']
type SchemaEvent = components['schemas']['Event']
type SchemaSessionResponse = components['schemas']['SessionResponse']
type SchemaFPPEndpointsConfigResponse = components['schemas']['FPPEndpointsConfigResponse']
type SchemaConfigFPPEndpointsPayload = components['schemas']['ConfigFPPEndpointsPayload']
type SchemaConfigRevisionsResponse = components['schemas']['ConfigRevisionsResponse']
// Track G seam G-2 (ADR-039).
type SchemaResolumeInstancesConfigResponse = components['schemas']['ResolumeInstancesConfigResponse']
type SchemaConfigResolumeInstancesPayload = components['schemas']['ConfigResolumeInstancesPayload']
// Track G seam G-3 (ADR-039).
type SchemaFPPMQTTConfigResponse = components['schemas']['FPPMQTTConfigResponse']
type SchemaConfigFPPMQTTPutRequest = components['schemas']['ConfigFPPMQTTPutRequest']
type SchemaFPPConnectSettingsConfigResponse = components['schemas']['FPPConnectSettingsConfigResponse']
type SchemaConfigFPPConnectSettingsPayload = components['schemas']['ConfigFPPConnectSettingsPayload']
// Track G seam G-4 (ADR-039).
type SchemaAssetsSettingsConfigResponse = components['schemas']['AssetsSettingsConfigResponse']
type SchemaConfigAssetsSettingsPutPayload = components['schemas']['ConfigAssetsSettingsPutPayload']
// The audio.settings engine-wide singleton and audio.node per-node object
// (ADR-039/ADR-018). Both are full-replacement kinds, unlike
// assets.settings/fpp.mqtt above: one payload type serves both GET and PUT.
type SchemaAudioSettingsConfigResponse = components['schemas']['AudioSettingsConfigResponse']
type SchemaConfigAudioSettingsPayload = components['schemas']['ConfigAudioSettingsPayload']
type SchemaAudioNodeConfigResponse = components['schemas']['AudioNodeConfigResponse']
type SchemaConfigAudioNode = components['schemas']['ConfigAudioNode']
type SchemaResolumeRecoveryResponse = components['schemas']['ResolumeRecoveryResponse']
type SchemaResolumeRecoveryConfigResponse = components['schemas']['ResolumeRecoveryConfigResponse']
type SchemaConfigResolumeRecoveryPayload = components['schemas']['ConfigResolumeRecoveryPayload']
type SchemaResolumeRecoveryRestoreResponse = components['schemas']['ResolumeRecoveryRestoreResponse']
// The pending-instanceUuid-change acknowledgement.
type SchemaAcknowledgeFPPInstanceUUIDChangeResponse = components['schemas']['AcknowledgeFPPInstanceUUIDChangeResponse']
// `GET /` (getServiceDescriptor): the coordinator's own self-description.
type SchemaServiceDescriptor = components['schemas']['ServiceDescriptor']
// Track B seam B2c (ADR-039): the render.settings configuration singleton.
type SchemaRenderSettingsConfigResponse = components['schemas']['RenderSettingsConfigResponse']
type SchemaConfigRenderSettingsPayload = components['schemas']['ConfigRenderSettingsPayload']
// ADR-033: the show.mode installation-wide operating mode singleton.
type SchemaShowModeConfigResponse = components['schemas']['ShowModeConfigResponse']
type SchemaConfigShowModePayload = components['schemas']['ConfigShowModePayload']
type SchemaFPPCommandResponse = components['schemas']['FPPCommandResponse']
type SchemaFPPCommandRequest = components['schemas']['FPPCommandRequest']
// TRACK-H-H2-SPEC.md §5.1: the stored playlist-entry observation surface,
// the recovery path for a wedged sequence anchor.
type SchemaFPPPlaylistEntryObservationsResponse = components['schemas']['FPPPlaylistEntryObservationsResponse']
type SchemaFPPPlaylistEntryObservation = components['schemas']['FPPPlaylistEntryObservation']
// TRACK-H-H2-SPEC.md §5/§6: the two read-only show-night verdicts:
// whether a Playlist is ready, and whether an instance's latest accepted
// observation still matches the show's bindings.
type SchemaFPPPlaylistReadinessResponse = components['schemas']['FPPPlaylistReadinessResponse']
type SchemaFPPPlaylistEntryReconciliationResponse = components['schemas']['FPPPlaylistEntryReconciliationResponse']
// TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition import
// evidence: the list of what has been reported, one full definition,
// and its parsed entries.
type SchemaFPPPlaylistDefinitionsListResponse = components['schemas']['FPPPlaylistDefinitionsListResponse']
type SchemaFPPPlaylistDefinitionResponse = components['schemas']['FPPPlaylistDefinitionResponse']
type SchemaFPPPlaylistDefinitionEntriesResponse = components['schemas']['FPPPlaylistDefinitionEntriesResponse']
// Track B seam B2b-front: the three render.* dispatch endpoints.
type SchemaRenderCommandResponse = components['schemas']['RenderCommandResponse']
type SchemaRenderApplyRequest = components['schemas']['RenderApplyRequest']
type SchemaRenderSurfaceRequest = components['schemas']['RenderSurfaceRequest']
// First audio-dispatch slice: pause/resume/stop/output.mute/output.unmute.
type SchemaAudioSessionCommandResponse = components['schemas']['AudioSessionCommandResponse']
// Each of the thirteen audio session dispatch endpoints has its own
// request schema now (api/openapi.yaml review finding 2: a shared
// AudioSessionCommandRequest with a params union let any operation's
// body validate against any other operation's own shape). The envelope
// (revision/idempotencyKey/params) is identical across all five; this
// alias reconstructs it locally, typed against the union of the real
// per-operation params shapes, rather than importing a combined
// "AudioSessionCommandParams" schema type that no longer exists.
type SchemaAudioSessionParams =
  | components['schemas']['AudioSessionApplyParams']
  | components['schemas']['AudioSessionSeekParams']
  | components['schemas']['AudioSessionGainParams']
  | components['schemas']['AudioSessionGainFadeParams']
  | components['schemas']['AudioSessionNoParamsRequest']['params']
// BUILD-PLAN Step 7 seam B (RES-008 D2/D6).
type SchemaDiscoveryRunResponse = components['schemas']['DiscoveryRunResponse']
type SchemaNodeDeclarationResponse = components['schemas']['NodeDeclarationResponse']
// Step 9 (STEP-9-SPEC.md sections 5, 6): show.action/show.macro
// configuration objects and the macro run surface.
type SchemaConfigObjectsListResponse = components['schemas']['ConfigObjectsListResponse']
type SchemaConfigShowAction = components['schemas']['ConfigShowAction']
type SchemaShowActionConfigResponse = components['schemas']['ShowActionConfigResponse']
type SchemaConfigShowMacro = components['schemas']['ConfigShowMacro']
type SchemaShowMacroConfigResponse = components['schemas']['ShowMacroConfigResponse']
// Finding 16: show.surface's own read response, same aliasing pattern.
type SchemaShowSurfaceConfigResponse = components['schemas']['ShowSurfaceConfigResponse']
// Track H seam H6: show.cue authoring, same aliasing pattern.
type SchemaConfigShowCue = components['schemas']['ConfigShowCue']
type SchemaShowCueConfigResponse = components['schemas']['ShowCueConfigResponse']

// Track H seam H6: show.playlist's own read/write response, same
// aliasing pattern.
type SchemaConfigShowPlaylist = components['schemas']['ConfigShowPlaylist']
type SchemaShowPlaylistConfigResponse = components['schemas']['ShowPlaylistConfigResponse']
type SchemaMacroRunResponse = components['schemas']['MacroRunResponse']
type SchemaMacroRunSubmitResponse = components['schemas']['MacroRunSubmitResponse']
type SchemaMacroRunsListResponse = components['schemas']['MacroRunsListResponse']
type SchemaCreateMacroRunRequest = components['schemas']['CreateMacroRunRequest']
type SchemaMacroRunChangedEvent = components['schemas']['MacroRunChangedEvent']
type SchemaMacroRunSummary = components['schemas']['MacroRunSummary']
// Track D seam D-2a (ADR-032).
type SchemaResolumeCompositionResponse = components['schemas']['ResolumeCompositionResponse']
type SchemaResolumeCompositionUploadResponse = components['schemas']['ResolumeCompositionUploadResponse']
// Track D seam D-4: Resolume as an observability resource (seam E) and
// the seven-action vocabulary (D-3/seam B).
type SchemaResolumeInstance = components['schemas']['ResolumeInstance']
type SchemaResolumeInstancesResponse = components['schemas']['ResolumeInstancesResponse']
type SchemaResolumeInstanceResponse = components['schemas']['ResolumeInstanceResponse']
type SchemaResolumeActionsResponse = components['schemas']['ResolumeActionsResponse']
type SchemaResolumeActionRequest = components['schemas']['ResolumeActionRequest']
type SchemaResolumeActionResponse = components['schemas']['ResolumeActionResponse']
type SchemaResolumeActionResult = components['schemas']['ResolumeActionResult']
type SchemaActionBinding = components['schemas']['ActionBinding']
type SchemaActionBindingResponse = components['schemas']['ActionBindingResponse']
type SchemaActionBindingsResponse = components['schemas']['ActionBindingsResponse']
type SchemaActionInvocationResponse = components['schemas']['ActionInvocationResponse']
type SchemaActionInvocationResult = components['schemas']['ActionInvocationResult']
// Track G seam G-5: identity administration over the API.
type SchemaPrincipalsResponse = components['schemas']['PrincipalsResponse']
type SchemaPrincipalResponse = components['schemas']['PrincipalResponse']
type SchemaCreatePrincipalRequest = components['schemas']['CreatePrincipalRequest']
type SchemaSetPrincipalRoleRequest = components['schemas']['SetPrincipalRoleRequest']
type SchemaSetPrincipalPasswordRequest = components['schemas']['SetPrincipalPasswordRequest']
type SchemaTokensResponse = components['schemas']['TokensResponse']
type SchemaIssueTokenRequest = components['schemas']['IssueTokenRequest']
type SchemaIssueTokenResponse = components['schemas']['IssueTokenResponse']
// Track G seam G-8: the Operator UI for Track E (ADR-027, ADR-026,
// ADR-028) — show, show.surface, show.active, asset, and audit shapes.
type SchemaConfigShowWrite = components['schemas']['ConfigShowWrite']
type SchemaShowConfigResponse = components['schemas']['ShowConfigResponse']
type SchemaConfigShowSurface = components['schemas']['ConfigShowSurface']
type SchemaConfigShowActive = components['schemas']['ConfigShowActive']
type SchemaShowActiveConfigResponse = components['schemas']['ShowActiveConfigResponse']
type SchemaAssetResponse = components['schemas']['AssetResponse']
type SchemaAssetsListResponse = components['schemas']['AssetsListResponse']
type SchemaAssetManifestResponse = components['schemas']['AssetManifestResponse']
type SchemaNodeAssetManifestResponse = components['schemas']['NodeAssetManifestResponse']
type SchemaAuditResponse = components['schemas']['AuditResponse']
type SchemaCueCatalogResponse = components['schemas']['CueCatalogResponse']
type SchemaCueCatalogDeployRequest = components['schemas']['CueCatalogDeployRequest']
type SchemaCueCatalogDeployResponse = components['schemas']['CueCatalogDeployResponse']
// Track F seam F2/F1: the night-session lifecycle controller and the
// night.session/night.session.active configuration kinds.
type SchemaNightSessionResponse = components['schemas']['NightSessionResponse']
type SchemaNightCommandRequest = components['schemas']['NightCommandRequest']
type SchemaNightInterlockOverride = components['schemas']['NightInterlockOverride']
type SchemaNightCommandResponse = components['schemas']['NightCommandResponse']
type SchemaNightSessionChangedEvent = components['schemas']['NightSessionChangedEvent']
type SchemaConfigNightSessionWrite = components['schemas']['ConfigNightSessionWrite']
type SchemaNightSessionConfigResponse = components['schemas']['NightSessionConfigResponse']
type SchemaConfigNightSessionActive = components['schemas']['ConfigNightSessionActive']
type SchemaNightSessionActiveConfigResponse = components['schemas']['NightSessionActiveConfigResponse']
type SchemaCurrentRunsResponse = components['schemas']['CurrentRunsResponse']
type SchemaCurrentRunsChangedEvent = components['schemas']['CurrentRunsChangedEvent']

/**
 * `Omit<Union, K>` is NOT distributive in TypeScript — `Omit` is defined
 * via `Pick<T, Exclude<keyof T, K>>`, and `keyof` on a union type yields
 * only the KEYS COMMON to every member, so a naive `Omit` over
 * `SchemaFPPCommandRequest` (api/openapi.yaml's discriminated `oneOf` on
 * `action`) would collapse every variant's own distinct `action`
 * literal/`params` shape down to whatever the branches happen to share.
 * This distributes the `Omit` over each union member individually instead
 * (a standard TS idiom: `T extends unknown ? ... : never` forces
 * distribution), which is what makes [dispatchFPPCommand]'s own
 * `request` parameter still a real discriminated union — the whole point
 * of Step 8's `oneOf` fix (see api/openapi.yaml's own FPPCommandRequest
 * description): passing `{action: 'startPlaylist', params: {playist:
 * 'x'}}` (a typo'd `playlist`) must fail to compile, not decay to
 * `Record<string, never>`.
 */
type DistributiveOmit<T, K extends PropertyKey> = T extends unknown ? Omit<T, K> : never

/**
 * Every variant of [SchemaFPPCommandRequest] MINUS `idempotencyKey` — the
 * one field every caller of [ApiStore.dispatchFPPCommand] does NOT supply,
 * because that method mints it itself (a fresh `randomUUIDv4()` per call,
 * never caller-supplied — see that method's own doc comment).
 */
type FPPCommandDispatchArgs = DistributiveOmit<SchemaFPPCommandRequest, 'idempotencyKey'>

/**
 * Every variant of [SchemaResolumeActionRequest] MINUS `idempotencyKey` —
 * the Resolume sibling of [FPPCommandDispatchArgs], for the identical
 * reason: [ApiStore.dispatchResolumeAction] mints the key itself, and the
 * distribution keeps this a real discriminated union on `action` rather
 * than collapsing every variant's own `params` shape to their common
 * subset.
 */
type ResolumeActionDispatchArgs = DistributiveOmit<SchemaResolumeActionRequest, 'idempotencyKey'>

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how many events the in-browser model
 * keeps before trimming the oldest. Nothing has measured an operator's
 * actual working set during a show; this exists so a long-running tab
 * has a bounded memory footprint rather than growing without limit. It
 * bounds the *client-side* model only — it has no effect on the
 * coordinator's own retention (`oldestRetainedSeq`, `gap`), which is a
 * server-side policy this client just reports.
 */
const MAX_RETAINED_EVENTS = 500

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how many of the most recent events the
 * initial (and every re-snapshot's) `GET /events` fetch asks for. Chosen
 * to match the endpoint's own default `limit` (api/openapi.yaml: "100...
 * this coordinator's own pagination choice, not a measured or
 * contractually significant value"), not because 100 is independently
 * validated as the right operator working set.
 */
const INITIAL_EVENTS_WINDOW = 100

/**
 * UNMEASURED SHOWMESH HYPOTHESIS: how long an established `/stream`
 * connection may go without receiving ANY bytes — including a `:
 * keepalive` comment — before this client gives up on it as half-open
 * and reconnects (D2). The coordinator writes `: keepalive` on its own
 * fixed interval, which api/openapi.yaml itself calls "a ShowMesh-chosen
 * hypothesis, not a measured or contractually significant value" (15s as
 * of this writing — internal/coordinator/api/api.go's
 * defaultStreamKeepaliveInterval — but that default is not part of the
 * wire contract and this client must not assume it). This value is
 * deliberately not derived from that number; it is simply picked
 * comfortably above any plausible keepalive interval so an occasional
 * scheduling delay never produces a false disconnect, while still being
 * short enough that a genuinely dead connection (a partition, a Wi-Fi
 * handoff, a dropped NAT mapping) does not read as "Live" for long.
 */
const STREAM_IDLE_TIMEOUT_MS = 45_000

/**
 * ADR-024 decision 11's amendment (owner ruling, 2026-08-26): `Model.auditStore`
 * is not part of the SSE snapshot/delta stream at all (no `Snapshot.auditStore`
 * write ever triggers a change-stream event; the coordinator computes it
 * fresh only when GET /api/v1/snapshot is actually called), so a dashboard
 * left open across a whole show, on the SAME long-lived stream connection
 * `applySnapshot` only ever runs against once, would otherwise show
 * whatever `auditStore` value happened to be current at connect time,
 * indefinitely. This client-side poll is what keeps it live instead:
 * re-fetches GET /api/v1/snapshot on this interval and folds ONLY
 * `auditStore` into the model, leaving every other field (nodes, fpp,
 * macroRuns, ...) exactly as the delta stream already maintains them.
 * UNMEASURED SHOWMESH HYPOTHESIS, picked the same way STREAM_IDLE_TIMEOUT_MS
 * above was: frequent enough that an operator watching the dashboard
 * learns of an audit-store outage within a show-relevant time, infrequent
 * enough that it costs nothing next to the keepalive traffic already on
 * this connection.
 */
const AUDIT_STORE_POLL_INTERVAL_MS = 30_000

export interface ApiStoreOptions {
  /** Default '/api/v1' — same-origin per ADR-022. */
  baseUrl?: string
  fetchImpl?: FetchLike
  now?: () => number
  /** Override for tests only; production code should rely on the DEFAULT_BACKOFF hypothesis. */
  backoff?: BackoffConfig
  /** Override for tests only; production code relies on STREAM_IDLE_TIMEOUT_MS above. */
  streamIdleTimeoutMs?: number
  /** Override for tests only; production code relies on client.ts's DEFAULT_REQUEST_TIMEOUT_MS. */
  requestTimeoutMs?: number
  /** Override for tests only; production code relies on AUDIT_STORE_POLL_INTERVAL_MS above. */
  auditStorePollIntervalMs?: number
  /**
   * Override for tests only (see clock.ts). Drives BOTH the stream
   * idle-deadline below and, via the ApiClient this store constructs,
   * the request timeout — production code always uses SYSTEM_CLOCK.
   */
  clock?: Clock
}

type Listener = () => void

export class ApiStore {
  private readonly client: ApiClient
  // Duplicated from the value handed to `ApiClient`'s own constructor
  // rather than read back off `client`, which exposes no getter for it
  // (it is otherwise a purely internal transport detail). Needed here
  // ONLY for [uploadResolumeComposition] below, which bypasses `ApiClient`
  // entirely (`XMLHttpRequest`, not `fetch` — see
  // resolumeCompositionUpload.ts's own header comment for why) and so has
  // no other way to learn where "same origin, under /api/v1" actually is.
  private readonly baseUrl: string
  private readonly now: () => number
  private readonly backoffConfig: BackoffConfig
  private readonly streamIdleTimeoutMs: number
  private readonly auditStorePollIntervalMs: number
  private readonly clock: Clock

  private model: Model = initialModel()
  private readonly listeners = new Set<Listener>()

  private running = false
  private disposed = false
  private generation = 0
  private attempt = 0
  private lastError: string | null = null
  private loopAbort: AbortController | null = null
  private currentRunsUpdateCounter = 0

  /**
   * ADR-024: independent, short-lived requests this store makes outside
   * the `/stream` read loop above — `GET /session` on connect(), and
   * `login`/`logout`/`claimBootstrap`. Tracked in a set (rather than one
   * field) because more than one can be in flight at once (the
   * connect()-time session fetch and the first reconnect's own refresh
   * race deliberately — see applySessionResponse's ordering guard) and so
   * dispose() can abort every one of them, the same guarantee loopAbort
   * already gives the read loop.
   */
  private readonly sideControllers = new Set<AbortController>()

  constructor(options: ApiStoreOptions = {}) {
    this.clock = options.clock ?? SYSTEM_CLOCK
    this.baseUrl = options.baseUrl ?? '/api/v1'
    this.client = new ApiClient(this.baseUrl, options.fetchImpl, options.requestTimeoutMs, this.clock)
    this.now = options.now ?? (() => Date.now())
    this.backoffConfig = options.backoff ?? DEFAULT_BACKOFF
    this.streamIdleTimeoutMs = options.streamIdleTimeoutMs ?? STREAM_IDLE_TIMEOUT_MS
    this.auditStorePollIntervalMs = options.auditStorePollIntervalMs ?? AUDIT_STORE_POLL_INTERVAL_MS
  }

  // -- useSyncExternalStore surface ------------------------------------

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  getSnapshot = (): Model => this.model

  // -- lifecycle --------------------------------------------------------

  connect(): void {
    if (this.disposed || this.running) return
    this.running = true
    void this.runLoop()
    // ADR-024: "GET /api/v1/session is always open ... so the banner
    // costs one request." Fired independently of the read loop above,
    // not as part of it, because it must reach a state even when the
    // loop never does — a coordinator running with reads closed answers
    // `GET /stream` with a `401` before ever reaching reloadSnapshot's
    // own session refresh (see that method), and an anonymous device on
    // a coordinator with reads open still needs this on first paint to
    // learn it has never authenticated at all (decision 5's third case).
    const controller = this.beginSideCall()
    void this.fetchSession(controller.signal).finally(() => this.endSideCall(controller))
  }

  dispose(): void {
    this.disposed = true
    this.loopAbort?.abort()
    for (const controller of this.sideControllers) controller.abort()
  }

  /**
   * Called after a `401` prompt (spec section 5.6) is answered. Stores
   * the token, then interrupts whatever the loop is currently doing —
   * a backoff wait, an indefinite `unauthorized` pause, or an in-flight
   * request — so the next attempt uses it immediately rather than
   * waiting out a stale backoff schedule.
   */
  submitToken(token: string): void {
    setStoredToken(token)
    this.attempt = 0
    this.loopAbort?.abort()
  }

  /** The only "logout": discard the stored secret and retry unauthenticated. */
  clearToken(): void {
    clearStoredToken()
    this.attempt = 0
    this.loopAbort?.abort()
  }

  // -- ADR-024: sessions ------------------------------------------------
  //
  // These three are deliberately NOT folded into the read loop's ERROR
  // handling above: a wrong password submitted at the sign-in form is a
  // fact about ONE request, to be shown next to that form, and must never
  // flip the whole app's connection banner to "unauthorized". Each method
  // here lets its error propagate to the caller — the form component —
  // to render; only fetchSession (the background refresh) swallows its
  // own failures, because nothing is waiting synchronously on it.
  //
  // A SUCCESSFUL call is different: login/logout/claimBootstrap all end
  // by calling wakeReadLoop(), deliberately reaching into the read loop's
  // state — see that method's own comment for the real-browser-only
  // defect this exists to fix.

  private beginSideCall(): AbortController {
    const controller = new AbortController()
    this.sideControllers.add(controller)
    return controller
  }

  private endSideCall(controller: AbortController): void {
    this.sideControllers.delete(controller)
  }

  /**
   * Found only by loading this in a real browser against a coordinator
   * running with reads closed (`SHOWMESH_API_CLOSE_READS=true`) — the
   * exact class of defect this project's own standing lesson names: a
   * unit suite exercising `login()` in isolation has no read loop sitting
   * in `unauthorized` to fail to wake, so it cannot see this.
   *
   * Without this, a cookie-authenticated `login()`/`claimBootstrap()`
   * success updates `model.session` correctly, but the READ loop
   * (runLoop) — if it was already parked in `{ kind: 'unauthorized' }`
   * because reads are closed and this browser had no credential yet — has
   * no way to learn a credential now exists: it is sitting in
   * `waitUntilAborted(signal)`, and the only two things that have ever
   * aborted that wait are `submitToken`/`clearToken`. The persistent
   * session banner would then show "Signed in as ..." while the rest of
   * the dashboard stayed stuck on "Waiting for the first response from
   * the coordinator" indefinitely — an operator who just proved their
   * identity, looking at a UI that still won't show them anything.
   *
   * Mirrors submitToken/clearToken's own unconditional `attempt = 0` +
   * abort exactly, including firing even when the loop was NOT parked
   * waiting (the ordinary case: reads open, the stream never needed a
   * credential at all) — that costs one harmless extra reconnect
   * (isAbortError's "retry immediately, no backoff" branch), the same
   * accepted cost submitToken already pays on every call, and is
   * simpler and more robust than trying to detect which case applies.
   */
  private wakeReadLoop(): void {
    this.attempt = 0
    this.loopAbort?.abort()
  }

  /**
   * `GET /api/v1/` (getServiceDescriptor). "Always open, with no
   * credential and regardless of whether reads are otherwise closed"
   * (api/openapi.yaml's own doc comment) -- the coordinator's build
   * metadata, not one of the four resources the read-closure posture
   * gates. A plain pass-through like the ADR-024 session methods below:
   * not part of `Model`/the SSE stream, and never retried by this store
   * -- a caller (ConnectionBanner) fetches it once and renders its own
   * failure to do so as a fact, not a blank.
   */
  async getServiceDescriptor(): Promise<SchemaServiceDescriptor> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaServiceDescriptor>('/', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/session` (ADR-024 decisions 1, 5, 8). Throws on invalid credentials, rate limiting, etc — the caller renders the failure. */
  async login(name: string, password: string, deviceLabel: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.postJson<SchemaSessionResponse>(
        '/session',
        { name, password, deviceLabel },
        controller.signal,
      )
      this.applySessionResponse(resp)
      this.clearShadowingToken()
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `DELETE /api/v1/session` (ADR-024 decisions 5, 6). No `sessionId`
   * revokes the session that authenticated THIS request — which must be
   * the cookie itself (server-enforced; see api/openapi.yaml) — the
   * ordinary "sign out this device" action. A `204` carries no body, so
   * the authoritative post-logout state (signed out, or — for a
   * `sessionId` naming some OTHER session — still signed in) is learned
   * by re-fetching rather than guessed at client-side.
   */
  async logout(sessionId?: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson<SchemaSessionResponse>(
        '/session',
        sessionId === undefined ? undefined : { sessionId },
        controller.signal,
      )
      await this.fetchSession(controller.signal)
      // See wakeReadLoop's own comment. Symmetrical with login: if reads
      // are closed and this was the credential the read loop was living
      // on, that loop must re-attempt now and land on 'unauthorized'
      // promptly, rather than an already-live stream (revalidated on its
      // own schedule — ADR-024 decision 5) being the only thing that
      // eventually notices.
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/bootstrap` (ADR-024 decision 9). Throws on an invalid/claimed/expired code — the caller renders the failure. */
  async claimBootstrap(code: string, name: string, password: string, deviceLabel: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.postJson<SchemaSessionResponse>(
        '/bootstrap',
        { code, name, password, deviceLabel },
        controller.signal,
      )
      this.applySessionResponse(resp)
      this.clearShadowingToken()
      this.wakeReadLoop()
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Step 7 seam A: the configuration write surface (RES-008 D1) -----
  //
  // None of the three methods below touches `this.model` or the read
  // loop: config.write is admin-only (there is no config:read scope),
  // this data is not part of the SSE snapshot/delta stream at all (it is
  // not a "resource" ADR-020's change stream models), and — unlike
  // login/logout/claimBootstrap — nothing here changes what credential
  // this browser presents, so there is no `wakeReadLoop()` call to make.
  // Each is a plain pass-through the config view (views/Configuration.tsx)
  // calls directly and renders (or fails to reach, and renders THAT)
  // itself.

  /** `GET /api/v1/config/fpp.endpoints` (Step 7 seam A). Throws (404) when nothing has been configured yet. */
  async getFPPEndpointsConfig(): Promise<SchemaFPPEndpointsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPEndpointsConfigResponse>(
        '/config/fpp.endpoints',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/fpp.endpoints` (Step 7 seam A): this application's
   * first write besides the session/bootstrap pair. Validated
   * before-activation server-side (ADR-009) — a rejected payload throws
   * and appends no revision; the caller (the config view's save handler)
   * renders the thrown error via `describeApiError`, matching every other
   * write path in this app.
   */
  async putFPPEndpointsConfig(
    payload: SchemaConfigFPPEndpointsPayload,
  ): Promise<SchemaFPPEndpointsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaFPPEndpointsConfigResponse>(
        '/config/fpp.endpoints',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlist-entry-observations`
   * (TRACK-H-H2-SPEC.md §5): the latest accepted observation for every
   * known instance. Open under `observation:read`; never throws on
   * 401/403 when reads are open. Not part of the SSE snapshot/delta
   * stream, so a caller re-fetches this explicitly, same posture as
   * `listResolumeInstances` above.
   */
  async listFPPPlaylistEntryObservations(): Promise<SchemaFPPPlaylistEntryObservationsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistEntryObservationsResponse>(
        '/integrations/fpp/playlist-entry-observations',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `DELETE /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}`
   * (TRACK-H-H2-SPEC.md §5.1): clears one instance's stored
   * playlist-entry observation and its sequence anchor, so the next
   * plugin report re-establishes it. Behind `fpp:command`, deliberately
   * not `fpp:observe` (see the route's own doc comment in
   * api/openapi.yaml). Idempotent server-side: always succeeds, whether
   * or not a row existed.
   */
  async deleteFPPPlaylistEntryObservation(instanceUuid: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson(
        `/integrations/fpp/playlist-entry-observations/${encodeURIComponent(instanceUuid)}`,
        undefined,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlists/{playlistId}/readiness`
   * (TRACK-H-H2-SPEC.md §6): whether one FPP-backed Playlist is ready to
   * run, and the first of §6's seven conditions that fails when it is not.
   * Open under `observation:read`, same posture as
   * [listFPPPlaylistEntryObservations] above. Throws (400) for a
   * non-fpp-runner playlist and (404) for a playlist with no active
   * revision: both are real, distinguishable answers a caller inspects
   * `ApiError.status` for, never confused with "not ready" or with a
   * failure to ask.
   */
  async getFPPPlaylistReadiness(playlistId: string): Promise<SchemaFPPPlaylistReadinessResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistReadinessResponse>(
        `/integrations/fpp/playlists/${encodeURIComponent(playlistId)}/readiness`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}/reconciliation`
   * (TRACK-H-H2-SPEC.md §5): what this coordinator currently makes of one
   * instance's latest accepted observation against the show's
   * `show.playlist` bindings. Open under `observation:read`, same posture
   * as [getFPPPlaylistReadiness] above. Throws (404) when this instance
   * has no accepted observation yet: a real, distinguishable answer, not
   * a failure to ask.
   */
  async getFPPPlaylistEntryReconciliation(
    instanceUuid: string,
  ): Promise<SchemaFPPPlaylistEntryReconciliationResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistEntryReconciliationResponse>(
        `/integrations/fpp/playlist-entry-observations/${encodeURIComponent(instanceUuid)}/reconciliation`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlist-definitions`
   * (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3.6): metadata for every FPP
   * playlist definition the coordinator has ever accepted, newest
   * received first. Open under `observation:read`, same posture as
   * [getFPPPlaylistReadiness] above. No definition payload here: a
   * caller picks a (instanceUuid, playlistHash) from this list, then
   * calls [getFPPPlaylistDefinition] or [getFPPPlaylistDefinitionEntries]
   * for that one definition's own content.
   */
  async listFPPPlaylistDefinitions(): Promise<SchemaFPPPlaylistDefinitionsListResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistDefinitionsListResponse>(
        '/integrations/fpp/playlist-definitions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}`
   * (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3.6): the stored definition
   * itself, as the coordinator canonicalized it. Open under
   * `observation:read`, same posture as [listFPPPlaylistDefinitions]
   * above. Throws (404) when no definition is stored for this pair: a
   * real, distinguishable answer, never confused with a failure to ask.
   */
  async getFPPPlaylistDefinition(
    instanceUuid: string,
    playlistHash: string,
  ): Promise<SchemaFPPPlaylistDefinitionResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistDefinitionResponse>(
        `/integrations/fpp/playlist-definitions/${encodeURIComponent(instanceUuid)}/${encodeURIComponent(playlistHash)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/integrations/fpp/playlist-definitions/{instanceUuid}/{playlistHash}/entries`
   * (TRACK-H-H2-SPEC.md §4 step 2): the definition's parsed entries,
   * `leadIn` then `mainPlaylist` then `leadOut`, each section positioned
   * from zero independently. Open under `observation:read`, same posture
   * as [getFPPPlaylistDefinition] above. Throws (404) for the same reason
   * as that method.
   */
  async getFPPPlaylistDefinitionEntries(
    instanceUuid: string,
    playlistHash: string,
  ): Promise<SchemaFPPPlaylistDefinitionEntriesResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPPlaylistDefinitionEntriesResponse>(
        `/integrations/fpp/playlist-definitions/${encodeURIComponent(instanceUuid)}/${encodeURIComponent(playlistHash)}/entries`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/fpp/{instanceId}/instance-uuid/acknowledge`. Behind
   * `config:write` (an operator-authored inventory decision, not a
   * command sent to any device, api/openapi.yaml's own doc comment on
   * this route). Clears the ONE marker that this endpoint's observed
   * instanceUuid changed since it was last seen -- never the recorded
   * instanceUuid itself. Throws 409 when the instance has no pending
   * unacknowledged change. The response carries the post-acknowledge
   * FPPInstance (or null, if the instance was removed between the write
   * and this response), so a caller renders the OBSERVED result from
   * this response body rather than the bare fact the POST returned.
   */
  async acknowledgeFPPInstanceUUIDChange(instanceId: string): Promise<SchemaAcknowledgeFPPInstanceUUIDChangeResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaAcknowledgeFPPInstanceUUIDChangeResponse>(
        `/fpp/${encodeURIComponent(instanceId)}/instance-uuid/acknowledge`,
        undefined,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- BUILD-PLAN Step 7 seam B: node discovery and declaration ---------
  //
  // Unlike login/logout/claimBootstrap above, these three do NOT call
  // wakeReadLoop() or touch `this.model` directly: a discovery run or a
  // declaration change is node/config data, not identity, so its effect
  // reaches the model through the ordinary path every other node change
  // does — the next SSE `node.changed` frame or hub tick (contract
  // section 6.5) — never a client-side guess at what the coordinator now
  // holds. Each still uses beginSideCall/endSideCall so an in-flight
  // request is tracked and aborted on dispose() exactly like the session
  // calls above.

  /**
   * `POST /api/v1/discovery/runs` (RES-008 D2/D6). Reads what the
   * coordinator already observes and proposes what is not currently
   * declared; never creates, modifies, or deletes a declaration by
   * itself (ADR-003). Throws on 401/403/500 — the caller renders the
   * failure.
   */
  async runDiscovery(): Promise<SchemaDiscoveryRunResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaDiscoveryRunResponse>('/discovery/runs', undefined, controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track G seam G-2 (ADR-039): the resolume.instances configuration
  // write surface. Same thin pass-through shape as the fpp.endpoints trio
  // just above, including the same "the config view calls this directly
  // and renders (or fails to reach, and renders THAT) itself" posture.

  /** `GET /api/v1/config/resolume.instances` (Track G seam G-2). Throws (404) when nothing has been configured yet. */
  async getResolumeInstancesConfig(): Promise<SchemaResolumeInstancesConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeInstancesConfigResponse>(
        '/config/resolume.instances',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/resolume.instances` (Track G seam G-2). Validated
   * before-activation server-side (ADR-009) — a rejected payload throws
   * and appends no revision; the caller (the config view's save handler)
   * renders the thrown error via `describeApiError`, matching
   * `putFPPEndpointsConfig`'s identical contract.
   */
  async putResolumeInstancesConfig(
    payload: SchemaConfigResolumeInstancesPayload,
  ): Promise<SchemaResolumeInstancesConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaResolumeInstancesConfigResponse>(
        '/config/resolume.instances',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/resolume.instances/revisions` (Track G seam G-2): revision history, newest first, metadata only. */
  async getResolumeInstancesConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/resolume.instances/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track G seam G-3 (ADR-039): the fpp.mqtt configuration write
  // surface. Unlike resolume.instances/fpp.endpoints, `set` is a PARTIAL
  // UPDATE (every field independently optional — see
  // ConfigFPPMQTTPutRequest's own doc comment); the caller builds a
  // request object naming only the fields it actually intends to change.

  /** `GET /api/v1/config/fpp.mqtt` (Track G seam G-3). Throws (404) when nothing has been configured yet. The broker password is never returned — `payload.passwordSet` reports presence only. */
  async getFPPMQTTConfig(): Promise<SchemaFPPMQTTConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPMQTTConfigResponse>('/config/fpp.mqtt', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/fpp.mqtt` (Track G seam G-3). A key absent from
   * request leaves that field's stored value unchanged (ADR-039 decision
   * 5) — this is what lets the operator edit, say, only topicPrefix
   * without re-typing a password `getFPPMQTTConfig` never gave back.
   * Validated before activation (ADR-009) — a rejected payload throws and
   * appends no revision.
   */
  async putFPPMQTTConfig(request: SchemaConfigFPPMQTTPutRequest): Promise<SchemaFPPMQTTConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaFPPMQTTConfigResponse>('/config/fpp.mqtt', request, controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/fpp.mqtt/revisions` (Track G seam G-3): revision history, newest first, metadata only. */
  async getFPPMQTTConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>('/config/fpp.mqtt/revisions', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  async getFPPConnectSettingsConfig(): Promise<SchemaFPPConnectSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaFPPConnectSettingsConfigResponse>('/config/fppconnect.settings', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  async putFPPConnectSettingsConfig(payload: SchemaConfigFPPConnectSettingsPayload): Promise<SchemaFPPConnectSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaFPPConnectSettingsConfigResponse>('/config/fppconnect.settings', payload, controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  async getFPPConnectSettingsConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>('/config/fppconnect.settings/revisions', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track G seam G-4 (ADR-039): the assets.settings configuration write
  // surface. Same thin pass-through shape as resolume.instances just
  // above — the one difference is putAssetsSettingsConfig's payload type,
  // which is INTENTIONALLY the separate PutPayload schema (every field
  // optional), not the response payload schema.

  /** `GET /api/v1/config/assets.settings` (Track G seam G-4). Throws (404) when nothing has been configured yet. */
  async getAssetsSettingsConfig(): Promise<SchemaAssetsSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaAssetsSettingsConfigResponse>(
        '/config/assets.settings',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/assets.settings` (Track G seam G-4). payload
   * carries ONLY the fields the caller wants to change — an absent field
   * leaves the stored value alone (ADR-039 decision 5). Validated
   * before-activation server-side (ADR-009) — a rejected payload throws
   * and appends no revision.
   */
  async putAssetsSettingsConfig(
    payload: SchemaConfigAssetsSettingsPutPayload,
  ): Promise<SchemaAssetsSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaAssetsSettingsConfigResponse>(
        '/config/assets.settings',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/assets.settings/revisions` (Track G seam G-4): revision history, newest first, metadata only. */
  async getAssetsSettingsConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/assets.settings/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- ADR-039/ADR-018: the audio.settings engine-wide singleton and
  // audio.node per-node object. Both are FULL REPLACEMENT kinds (every
  // field required and non-null on every PUT) — unlike assets.settings/
  // fpp.mqtt just above, one payload type serves both GET and PUT.

  /** `GET /api/v1/config/audio.settings` (ADR-039). Never 404s: a well-defined default answers with revision 0, source "default". */
  async getAudioSettingsConfig(): Promise<SchemaAudioSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaAudioSettingsConfigResponse>('/config/audio.settings', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/audio.settings` (ADR-039). A full replacement:
   * every field is required and non-null. Validated before activation
   * server-side (ADR-009); a rejected payload throws and appends no
   * revision.
   */
  async putAudioSettingsConfig(
    payload: SchemaConfigAudioSettingsPayload,
  ): Promise<SchemaAudioSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaAudioSettingsConfigResponse>(
        '/config/audio.settings',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/audio.settings/revisions` (ADR-039): revision history, newest first, metadata only. */
  async getAudioSettingsConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/audio.settings/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/audio.node/{id}` (ADR-018). Throws (404) when no such object exists. */
  async getAudioNode(id: string): Promise<SchemaAudioNodeConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaAudioNodeConfigResponse>(
        `/config/audio.node/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/audio.node/{id}` (ADR-018). A full replacement:
   * every field required. `programRoute`/`ltcRoute` are cross-checked,
   * live, against this node's own most recent capability advertisement —
   * a route this node has not advertised is refused with `400`, never
   * accepted on the operator's claim alone.
   */
  async putAudioNode(id: string, payload: SchemaConfigAudioNode): Promise<SchemaAudioNodeConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaAudioNodeConfigResponse>(
        `/config/audio.node/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/audio.node/{id}/revisions` (ADR-018): revision history, newest first, metadata only. */
  async getAudioNodeConfigRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/audio.node/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/fpp.endpoints/revisions` (Step 7 seam A): revision history, newest first, metadata only. */
  async getFPPEndpointsConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/fpp.endpoints/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track D seam D-3a: Arena crash recovery ---------------------------
  //
  // Same "plain pass-through, touches neither `this.model` nor the read
  // loop" posture as Step 7 seam A's fpp.endpoints methods above: the
  // recovery record and the toggle are not part of the SSE snapshot/delta
  // stream (build contract §1.7: no new observation signal is minted).

  /** `GET /api/v1/resolume/recovery` (Track D seam D-3a). The open read: never throws on 401/403 — it carries no auth requirement at all. */
  async getResolumeRecovery(): Promise<SchemaResolumeRecoveryResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeRecoveryResponse>('/resolume/recovery', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/resolume.recovery` (Track D seam D-3a). Requires config:write. */
  async getResolumeRecoveryConfig(): Promise<SchemaResolumeRecoveryConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeRecoveryConfigResponse>('/config/resolume.recovery', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `PUT /api/v1/config/resolume.recovery` (Track D seam D-3a). Requires config:write. */
  async putResolumeRecoveryConfig(
    payload: SchemaConfigResolumeRecoveryPayload,
  ): Promise<SchemaResolumeRecoveryConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaResolumeRecoveryConfigResponse>(
        '/config/resolume.recovery',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/resolume.recovery/revisions` (Track D seam D-3a).
   * Requires `config:write`, mirroring [getFPPEndpointsConfigRevisions]'s
   * own posture: metadata only, newest first.
   */
  async getResolumeRecoveryConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/resolume.recovery/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/resolume/recovery/restore` (Track D seam D-3a). Requires
   * resolume:action. A restore can attempt up to 30 sequential per-layer
   * dispatches before answering (client.ts's own doc comment on
   * [RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS]) — this uses that
   * budget rather than the instance-wide default, matching D-4's own rule
   * that a client timeout is sized from the server's bound, never picked
   * independently (build contract §3).
   */
  async restoreResolumeRecovery(): Promise<SchemaResolumeRecoveryRestoreResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaResolumeRecoveryRestoreResponse>(
        '/resolume/recovery/restore',
        undefined,
        controller.signal,
        RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track B seam B2c: render.settings (ADR-039) -----------------------
  //
  // Same "plain pass-through, touches neither `this.model` nor the read
  // loop" posture as the resolume.recovery config methods just above: not
  // part of the SSE snapshot/delta stream.

  /** `GET /api/v1/config/render.settings` (Track B seam B2c). Requires config:write. Never 404s — reports the built-in default. */
  async getRenderSettingsConfig(): Promise<SchemaRenderSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaRenderSettingsConfigResponse>('/config/render.settings', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `PUT /api/v1/config/render.settings` (Track B seam B2c). Requires config:write. A full replacement — every field required. */
  async putRenderSettingsConfig(
    payload: SchemaConfigRenderSettingsPayload,
  ): Promise<SchemaRenderSettingsConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaRenderSettingsConfigResponse>(
        '/config/render.settings',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/render.settings/revisions` (Track B seam B2c). Requires config:write. */
  async getRenderSettingsConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/render.settings/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- ADR-033: the installation-wide operating mode -------------------
  //
  // Same plain pass-through posture as the render.settings methods above.
  // The GET is the one configuration read this UI can make without
  // config:write: ADR-033 decision 3 requires the mode to be persistently
  // visible, and the operator at the console does not hold config:write.

  /** `GET /api/v1/config/show.mode` (ADR-033). Requires only observation:read. Never 404s, reports the built-in default "program". */
  async getShowModeConfig(): Promise<SchemaShowModeConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowModeConfigResponse>('/config/show.mode', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `PUT /api/v1/config/show.mode` (ADR-033). Requires config:write. A full replacement. */
  async putShowModeConfig(payload: SchemaConfigShowModePayload): Promise<SchemaShowModeConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowModeConfigResponse>(
        '/config/show.mode',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.mode/revisions` (ADR-033). Requires config:write, unlike the current-value read above. */
  async getShowModeConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/show.mode/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track B seam B2b-front: the three render.* dispatch endpoints -----
  //
  // Same "long request by design, RENDER_COMMAND_REQUEST_TIMEOUT_MS not
  // the default budget" posture as dispatchFPPCommand above: the
  // coordinator holds the response open for its own confirmation deadline
  // before answering (renderdispatch.go). Never rendered as unqualified
  // success on a bare 200 (ADR-003) — the caller reads `.outcome`.

  private async dispatchRenderCommand(
    nodeId: string,
    surfaceId: string,
    verb: 'apply' | 'clear' | 'restart' | 'transport-probe',
    sequenceId?: string,
  ): Promise<RenderCommandResult> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaRenderApplyRequest | SchemaRenderSurfaceRequest =
        sequenceId !== undefined
          ? { sequenceId, idempotencyKey: randomUUIDv4() }
          : { idempotencyKey: randomUUIDv4() }
      const resp = await this.client.postJson<SchemaRenderCommandResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/render/surfaces/${encodeURIComponent(surfaceId)}/${verb}`,
        body,
        controller.signal,
        RENDER_COMMAND_REQUEST_TIMEOUT_MS,
      )
      return resp.command
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /nodes/{nodeId}/render/surfaces/{surfaceId}/apply`. Requires render:command. */
  async applyRenderSurface(nodeId: string, surfaceId: string, sequenceId: string): Promise<RenderCommandResult> {
    return this.dispatchRenderCommand(nodeId, surfaceId, 'apply', sequenceId)
  }

  /** `POST /nodes/{nodeId}/render/surfaces/{surfaceId}/clear`. Requires render:command. */
  async clearRenderSurface(nodeId: string, surfaceId: string): Promise<RenderCommandResult> {
    return this.dispatchRenderCommand(nodeId, surfaceId, 'clear')
  }

  /** `POST /nodes/{nodeId}/render/surfaces/{surfaceId}/restart`. Requires render:command. */
  async restartRenderPipeline(nodeId: string, surfaceId: string): Promise<RenderCommandResult> {
    return this.dispatchRenderCommand(nodeId, surfaceId, 'restart')
  }

  // -- The first slice of audio dispatch: the five operations an
  // operator reaches for when something is audibly wrong:
  // pause/resume/stop and output.mute/output.unmute. Same
  // "long request by design" posture as dispatchRenderCommand above:
  // AUDIO_COMMAND_REQUEST_TIMEOUT_MS matches audioHandlerWriteDeadline()
  // on the coordinator side (client.ts's own doc comment). Never rendered
  // as unqualified success on a bare 200 (ADR-003). The caller reads
  // `.outcome`, which for this endpoint family is "unconfirmable" (not
  // "unconfirmed") whenever the dispatch was accepted but not
  // corroborated.
  //
  // -- Second slice: prepare/start/advance/clear (no params) plus
  // seek/gain/gain.fade (each carrying operation-specific params the
  // node itself validates -- each operation's own request schema's
  // params is opaque to this coordinator by design). apply is the same
  // shape as seek/gain: this coordinator passes params through verbatim
  // without validating it, exactly as showmeshctl's own "params-json is
  // passed through verbatim" positional argument does
  // (cmd_audio_session.go).

  private async dispatchAudioSessionCommand(
    nodeId: string,
    sessionId: string,
    path: string,
    revision: number,
    params?: Record<string, unknown>,
  ): Promise<AudioSessionCommandResult> {
    const controller = this.beginSideCall()
    try {
      const body: { revision: number; idempotencyKey: string; params?: SchemaAudioSessionParams } = {
        revision,
        idempotencyKey: randomUUIDv4(),
      }
      if (params !== undefined) {
        body.params = params as Record<string, never>
      }
      const resp = await this.client.postJson<SchemaAudioSessionCommandResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/audio/sessions/${encodeURIComponent(sessionId)}/${path}`,
        body,
        controller.signal,
        AUDIO_COMMAND_REQUEST_TIMEOUT_MS,
      )
      return resp.command
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/pause`. Requires audio:command. */
  async pauseAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'pause', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/resume`. Requires audio:command. */
  async resumeAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'resume', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/stop`. Requires audio:command. */
  async stopAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'stop', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/output/mute`. Requires audio:command. */
  async muteAudioSessionOutput(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'output/mute', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/output/unmute`. Requires audio:command. */
  async unmuteAudioSessionOutput(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'output/unmute', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/prepare`. Requires audio:command. */
  async prepareAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'prepare', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/start`. Requires audio:command. */
  async startAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'start', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/advance`. Requires audio:command. */
  async advanceAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'advance', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/clear`. Requires audio:command. Releases the session entirely on the node; destructive, matching stop's own "never refused for want of node evidence" posture. */
  async clearAudioSession(nodeId: string, sessionId: string, revision: number): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'clear', revision)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/seek`. Requires audio:command. params.positionMs is the target position, in milliseconds. */
  async seekAudioSession(
    nodeId: string,
    sessionId: string,
    revision: number,
    positionMs: number,
  ): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'seek', revision, { positionMs })
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/gain`. Requires audio:command. params.gainDb is in decibels: 0 dB is unity, -60 dB and below is silence. The coordinator converts it to the engine's linear multiplier and clamps it to the session's own ceiling. */
  async setAudioSessionGain(
    nodeId: string,
    sessionId: string,
    revision: number,
    gainDb: number,
  ): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'gain', revision, { gainDb })
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/apply`. Requires audio:command. params is the same opaque, node-validated session definition (sourceRole/media/playlist/outputs/mixPolicy) `showmeshctl audio session apply` takes as its params-json argument; omitted entirely (not sent as `{}`) when the caller supplies none, matching that CLI's own optional positional argument. */
  async applyAudioSession(
    nodeId: string,
    sessionId: string,
    revision: number,
    params?: Record<string, unknown>,
  ): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'apply', revision, params)
  }

  /** `POST /nodes/{nodeId}/audio/sessions/{sessionId}/gain/fade`. Requires audio:command. params.targetGainDb is in decibels, like setAudioSessionGain's own gainDb; params.durationMs is the fade duration in milliseconds; params.curve is fixed to "linear", which names the fade SHAPE and not the gain unit. */
  async fadeAudioSessionGain(
    nodeId: string,
    sessionId: string,
    revision: number,
    targetGainDb: number,
    durationMs: number,
  ): Promise<AudioSessionCommandResult> {
    return this.dispatchAudioSessionCommand(nodeId, sessionId, 'gain/fade', revision, {
      targetGainDb,
      durationMs,
      curve: 'linear',
    })
  }

  /** `POST /nodes/{nodeId}/render/surfaces/{surfaceId}/transport-probe`. Requires render:command. */
  async probeRenderTransport(nodeId: string, surfaceId: string): Promise<RenderCommandResult> {
    return this.dispatchRenderCommand(nodeId, surfaceId, 'transport-probe')
  }

  // -- Track H seam H6: the resolved Cue catalog a node holds, and the
  // operator's own deploy control. GET is a plain open read (observation:read
  // under closed reads, ADR-024) — no scope gate, matching
  // listResolumeInstances' identical posture. Deploy shares
  // dispatchRenderCommand's shape one seam earlier: same idempotency key
  // generation, same "outcome decides success, never a bare 200" rule
  // (ADR-003), and the same request budget, since cuecatalogdeploy.go's own
  // confirm deadline mirrors renderdispatch.go's.

  /** `GET /nodes/{nodeId}/cue-catalog`. Open read; the coordinator's current resolution for this node, not a persisted acknowledgement. */
  async getNodeCueCatalog(nodeId: string): Promise<SchemaCueCatalogResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaCueCatalogResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/cue-catalog`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /nodes/{nodeId}/cue-catalog/deploy`. Requires cuecatalog:deploy (admin only). */
  async deployNodeCueCatalog(nodeId: string): Promise<CueCatalogDeployResult> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaCueCatalogDeployRequest = { idempotencyKey: randomUUIDv4() }
      const resp = await this.client.postJson<SchemaCueCatalogDeployResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/cue-catalog/deploy`,
        body,
        controller.signal,
        RENDER_COMMAND_REQUEST_TIMEOUT_MS,
      )
      return resp.command
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track D seam D-4: Resolume observability (seam E) and the
  // seven-action vocabulary (D-3/seam B). Plain side-calls; `model.resolume`
  // is populated from the snapshot/`resolume.changed` stream, not these. --

  /** `GET /api/v1/resolume/instances`. Open read (`observation:read` under closed reads); never throws on 401/403 when reads are open. */
  async listResolumeInstances(): Promise<SchemaResolumeInstancesResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeInstancesResponse>('/resolume/instances', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/resolume/instances/{instanceId}`. Throws (404) when no such instance is configured. */
  async getResolumeInstance(instanceId: string): Promise<SchemaResolumeInstanceResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeInstanceResponse>(
        `/resolume/instances/${encodeURIComponent(instanceId)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/resolume/actions`. Never gated by any scope — static capability metadata, identical across every coordinator running this software version. */
  async listResolumeActions(): Promise<SchemaResolumeActionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeActionsResponse>('/resolume/actions', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/resolume/actions` (D-3/seam B, ADR-024, ADR-037) — the
   * Resolume sibling of [dispatchFPPCommand]. Mints its own idempotency key;
   * uses RESOLUME_ACTION_REQUEST_TIMEOUT_MS, not the instance-wide default.
   * Returns `result.outcome` as-is, never inferred from this call's HTTP
   * success (ADR-029).
   */
  private async dispatchResolumeAction(request: ResolumeActionDispatchArgs): Promise<SchemaResolumeActionResult> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaResolumeActionRequest = {
        ...request,
        idempotencyKey: randomUUIDv4(),
      } as SchemaResolumeActionRequest
      const resp = await this.client.postJson<SchemaResolumeActionResponse>(
        '/resolume/actions',
        body,
        controller.signal,
        RESOLUME_ACTION_REQUEST_TIMEOUT_MS,
      )
      return resp.result
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `{"action":"launchClip","params":{clip,deck?,layer?,persistent?}}` — see [dispatchResolumeAction]. */
  async launchResolumeClip(params: {
    clip: string
    deck?: string
    layer?: string
    persistent?: boolean
  }): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'launchClip', params })
  }

  /** `{"action":"clearLayer","params":{layer}}` — see [dispatchResolumeAction]. */
  async clearResolumeLayer(layer: string): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'clearLayer', params: { layer } })
  }

  /** `{"action":"launchColumn","params":{column,deck}}` — see [dispatchResolumeAction]. */
  async launchResolumeColumn(column: string, deck: string): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'launchColumn', params: { column, deck } })
  }

  /** `{"action":"selectDeck","params":{deck}}` — see [dispatchResolumeAction]. */
  async selectResolumeDeck(deck: string): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'selectDeck', params: { deck } })
  }

  /** `{"action":"blackout"}`, no params — see [dispatchResolumeAction]. */
  async blackoutResolume(): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'blackout' })
  }

  /** `{"action":"setLayerBypass","params":{layer,bypassed}}` — see [dispatchResolumeAction]. */
  async setResolumeLayerBypass(layer: string, bypassed: boolean): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'setLayerBypass', params: { layer, bypassed } })
  }

  /** `{"action":"setLayerMaster","params":{layer,master}}` — see [dispatchResolumeAction]. */
  async setResolumeLayerMaster(layer: string, master: number): Promise<SchemaResolumeActionResult> {
    return this.dispatchResolumeAction({ action: 'setLayerMaster', params: { layer, master } })
  }

  // -- Track G seam G-5: identity administration over the API -----------
  //
  // Same "plain pass-through, touches neither `this.model` nor the read
  // loop" posture as Step 7 seam A's fpp.endpoints methods above:
  // principals and tokens are not part of the SSE snapshot/delta stream
  // (ADR-020 change-stream resources), so there is nothing here for
  // `wakeReadLoop()` to do. Every write requires principal:write and every
  // read requires principal:read (ADR-024, ADR-039 decision 8) — the
  // Access view (views/Access.tsx) is this surface's only caller and
  // gates itself on those scopes via `evaluateScope`, mirroring
  // Configuration.tsx's own posture for config:write.

  /** `GET /api/v1/principals` (Track G seam G-5). Requires principal:read. */
  async listPrincipals(): Promise<SchemaPrincipalsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaPrincipalsResponse>('/principals', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/principals` (Track G seam G-5). Requires principal:write. */
  async createPrincipal(payload: SchemaCreatePrincipalRequest): Promise<SchemaPrincipalResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaPrincipalResponse>('/principals', payload, controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/principals/{id}/role` (Track G seam G-5). Throws (409)
   * when this would leave no enabled principal able to reach
   * principal:write (ADR-039 decision 8) — the caller renders the thrown
   * error via `describeApiError`, matching every other write in this app.
   */
  async setPrincipalRole(id: string, payload: SchemaSetPrincipalRoleRequest): Promise<SchemaPrincipalResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaPrincipalResponse>(
        `/principals/${encodeURIComponent(id)}/role`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/principals/{id}/disable` (Track G seam G-5). Throws
   * (409) when this is the coordinator's last enabled administrator
   * (ADR-039 decision 8).
   */
  async disablePrincipal(id: string): Promise<SchemaPrincipalResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaPrincipalResponse>(
        `/principals/${encodeURIComponent(id)}/disable`,
        undefined,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/principals/{id}/enable` (Track G seam G-5). Never refused for lockout — enabling only ever adds back a way to authenticate. */
  async enablePrincipal(id: string): Promise<SchemaPrincipalResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaPrincipalResponse>(
        `/principals/${encodeURIComponent(id)}/enable`,
        undefined,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/principals/{id}/password` (Track G seam G-5). Bumps the target principal's generation, invalidating its existing sessions and tokens. */
  async resetPrincipalPassword(
    id: string,
    payload: SchemaSetPrincipalPasswordRequest,
  ): Promise<SchemaPrincipalResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaPrincipalResponse>(
        `/principals/${encodeURIComponent(id)}/password`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/principals/{id}/tokens` (Track G seam G-5). Requires principal:read. Never carries a digest or a raw value. */
  async listPrincipalTokens(id: string): Promise<SchemaTokensResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaTokensResponse>(
        `/principals/${encodeURIComponent(id)}/tokens`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `POST /api/v1/principals/{id}/tokens` (Track G seam G-5). The response's `value` is this token's only appearance on the wire, ever again (ADR-024 decision 1). */
  async issuePrincipalToken(id: string, payload: SchemaIssueTokenRequest): Promise<SchemaIssueTokenResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaIssueTokenResponse>(
        `/principals/${encodeURIComponent(id)}/tokens`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `DELETE /api/v1/principals/{id}/tokens/{tokenId}` (Track G seam G-5). Throws (409) when this is the last credential able to reach principal:write (ADR-039 decision 8). */
  async revokePrincipalToken(id: string, tokenId: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson(
        `/principals/${encodeURIComponent(id)}/tokens/${encodeURIComponent(tokenId)}`,
        undefined,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track D seam D-2a (ADR-032): the Resolume composition upload
  // surface ------------------------------------------------------------
  //
  // Same "plain pass-through, touches neither `this.model` nor the read
  // loop" posture as Step 7 seam A's fpp.endpoints methods just above,
  // for the identical reason: this data is not part of the SSE
  // snapshot/delta stream (ADR-032 stores it as a configuration object,
  // not a resource ADR-020's change stream models), so there is nothing
  // here for `wakeReadLoop()` to do.

  /**
   * `GET /api/v1/config/resolume/composition` (ADR-032 decision 1: the
   * stored id map — decks, layer groups, layers, columns, clips, and
   * persistent clips — every ShowMesh reference to a Resolume object
   * resolves through). Throws (404) when nothing has been uploaded yet,
   * exactly like [getFPPEndpointsConfig]'s own "nothing configured"
   * case — the coordinator deliberately uses the same status and shape
   * for both (api/openapi.yaml's own description of this route).
   */
  async getResolumeComposition(): Promise<SchemaResolumeCompositionResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaResolumeCompositionResponse>(
        '/config/resolume/composition',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/config/resolume/composition`, `multipart/form-data`
   * (ADR-032 decisions 7 and 8). `onProgress` is called with REAL byte
   * counts as `XMLHttpRequest.upload`'s own `progress` events arrive —
   * see resolumeCompositionUpload.ts's header comment for why this one
   * call bypasses `ApiClient`/`fetch` entirely. Replaces whatever
   * composition was stored before, in one transaction with its audit
   * entry (ADR-024 decision 11); a rejected file (400/413) persists
   * nothing at all, and the caller (ResolumeCompositionUpload.tsx) must
   * not render the new composition until this promise resolves — ADR-030:
   * "a partial upload registers nothing."
   */
  async uploadResolumeComposition(
    file: File,
    onProgress: (progress: UploadProgress) => void,
  ): Promise<SchemaResolumeCompositionUploadResponse> {
    const controller = this.beginSideCall()
    try {
      return await uploadFileWithProgress<SchemaResolumeCompositionUploadResponse>(
        this.baseUrl,
        '/config/resolume/composition',
        file,
        onProgress,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/nodes/{nodeId}/declaration`. Promotes nodeId to
   * declared, or — idempotently — updates its label/notes. Throws on
   * 401/403/500, including when the audit store fails and ADR-024
   * decision 11's same-transaction rule refuses the whole write.
   */
  async declareNode(nodeId: string, label: string, notes: string): Promise<SchemaNodeDeclarationResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.postJson<SchemaNodeDeclarationResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/declaration`,
        { label, notes },
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Step 9: show.action / show.macro configuration objects ----------
  //
  // Same posture as the Step 7 seam A methods above (plain side-calls,
  // never touching `this.model` or the read loop) with ONE difference:
  // reads here require `show:macro:run` OR `config:write` (STEP-9-SPEC.md
  // section 5.5), not `config:write` alone, so — unlike Configuration.tsx
  // — the calling view must NOT gate its fetch on `config:write` only, or
  // it silently renders empty/403 for the operator role these config
  // kinds exist to serve. See views/Macros.tsx and views/ShowActions.tsx.

  /**
   * `GET /api/v1/config/show.action`, `GET /api/v1/config/show.macro`,
   * `GET /api/v1/config/show`, `GET /api/v1/config/show.surface`, or
   * `GET /api/v1/config/show.playlist`: object ids, labels, and current
   * revision only, never the full payloads. `show` narrows the list to
   * that show's objects (`?show=`); `show` itself is a namespace and
   * does not accept it on itself.
   */
  async listConfigObjects(
    kind:
      | 'show.action'
      | 'show.macro'
      | 'show'
      | 'show.surface'
      | 'show.cue'
      | 'show.playlist'
      | 'night.session'
      // audio.node carries no show reference (its own list summary reports
      // programRoute as label instead) - `show` is simply never passed for it.
      | 'audio.node',
    show?: string,
  ): Promise<SchemaConfigObjectsListResponse> {
    const controller = this.beginSideCall()
    try {
      const query = kind !== 'show' && show !== undefined ? `?show=${encodeURIComponent(show)}` : ''
      return await this.client.getJson<SchemaConfigObjectsListResponse>(
        `/config/${kind}${query}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/show.surface?node=<nodeId>` — server-side
   * filtered, so this is how the Operator UI learns which show.surface
   * objects are assigned to nodeId (payload.node) without a per-row
   * detail fetch. Replaces the earlier approach of calling
   * [listConfigObjects]('show.surface') and then [getShowSurface] once
   * per candidate to read its node field (an external review of PR #14
   * caught that as O(M) HTTP calls per render for a node with M
   * configured surfaces — api.go's showobjects.go now filters server-side
   * instead).
   */
  async listShowSurfacesForNode(nodeId: string): Promise<SchemaConfigObjectsListResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigObjectsListResponse>(
        `/config/show.surface?node=${encodeURIComponent(nodeId)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/show.surface/{id}`. Throws (404) when no such
   * surface exists.
   */
  async getShowSurface(id: string): Promise<SchemaShowSurfaceConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowSurfaceConfigResponse>(
        `/config/show.surface/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/show.playlist/{id}`. Throws (404) when no such
   * playlist exists.
   */
  async getShowPlaylist(id: string): Promise<SchemaShowPlaylistConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowPlaylistConfigResponse>(
        `/config/show.playlist/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.action/{id}`. Throws (404) when no such action exists. */
  async getShowAction(id: string): Promise<SchemaShowActionConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowActionConfigResponse>(
        `/config/show.action/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.action/{id}` (STEP-9-SPEC.md section 5.3).
   * `config:write` only — validated and normalized server-side; a
   * rejected payload throws and appends no revision, rendered by the
   * caller via `describeApiError` exactly like `putFPPEndpointsConfig`.
   */
  async putShowAction(id: string, payload: SchemaConfigShowAction): Promise<SchemaShowActionConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowActionConfigResponse>(
        `/config/show.action/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.action/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowActionRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show.action/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/actions/{id}/binding` (ADR-029). Never gated by any
   * scope and never dispatches anything — a read, re-resolving this
   * action's stored target against current integration state.
   */
  async getActionBinding(id: string): Promise<SchemaActionBinding> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.getJson<SchemaActionBindingResponse>(
        `/actions/${encodeURIComponent(id)}/binding`,
        controller.signal,
      )
      return resp.binding
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/actions/bindings`: the pre-show sweep, every action's
   * own binding check in one request. `show` narrows the result to that
   * show; unmatched returns an empty list, never a refusal.
   */
  async listActionBindings(show?: string): Promise<SchemaActionBinding[]> {
    const controller = this.beginSideCall()
    try {
      const query = show !== undefined ? `?show=${encodeURIComponent(show)}` : ''
      const resp = await this.client.getJson<SchemaActionBindingsResponse>(`/actions/bindings${query}`, controller.signal)
      return resp.bindings
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/actions/{id}/invocations` (ADR-037 decision 8). Mints
   * its own idempotency key; uses
   * ACTION_INVOKE_REQUEST_TIMEOUT_MS, not the instance-wide default.
   * Returns `result.outcome` as-is, never inferred from this call's HTTP
   * success (ADR-029) — the action's own stored target supplies every
   * parameter, so this method takes none.
   */
  async invokeAction(id: string): Promise<SchemaActionInvocationResult> {
    const controller = this.beginSideCall()
    try {
      const resp = await this.client.postJson<SchemaActionInvocationResponse>(
        `/actions/${encodeURIComponent(id)}/invocations`,
        { idempotencyKey: randomUUIDv4() },
        controller.signal,
        ACTION_INVOKE_REQUEST_TIMEOUT_MS,
      )
      return resp.result
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.macro/{id}`. Throws (404) when no such macro exists. */
  async getShowMacro(id: string): Promise<SchemaShowMacroConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowMacroConfigResponse>(
        `/config/show.macro/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.macro/{id}` (STEP-9-SPEC.md section 5.4).
   * `config:write` only. `onFailure`/`onUnconfirmed` are sent as their
   * RESOLVED value on every step (this method's caller — the macro editor
   * — never omits them; see ConfigShowMacroStep's own generated doc
   * comment for why the wire type makes both required rather than
   * optional), which is wire-equivalent to omitting them: the resolved
   * default and an explicit request for that same default produce the
   * identical stored revision.
   */
  async putShowMacro(id: string, payload: SchemaConfigShowMacro): Promise<SchemaShowMacroConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowMacroConfigResponse>(
        `/config/show.macro/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.macro/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowMacroRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show.macro/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track G seam G-8: show, show.surface, show.active, assets, audit --
  //
  // Same "plain side-call, never touches `this.model` or the read loop"
  // posture as every method above — none of this data is part of the SSE
  // snapshot/delta stream. Reads (show/show.surface/show.active/assets/
  // manifest) share the show.action/show.macro read posture just above
  // (`show:macro:run` OR `config:write`); the audit log is the one
  // exception (`audit:read` only, always, per api/openapi.yaml's own
  // description of GET /audit — never one of the open-by-default reads).

  /** `GET /api/v1/config/show/{id}`. Throws (404) when no such show exists. */
  async getShow(id: string): Promise<SchemaShowConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowConfigResponse>(
        `/config/show/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show/{id}` (ADR-027 decision 2). `config:write`
   * only. Full replacement: an absent `notes` resolves to empty, never
   * "keep the previous value" (ConfigShowWrite's own description).
   */
  async putShow(id: string, payload: SchemaConfigShowWrite): Promise<SchemaShowConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowConfigResponse>(
        `/config/show/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.surface/{id}` (ADR-026). `config:write` only.
   * Full replacement — every field is required on every write, including
   * `channelRange` (the manual-channel-range path ADR-027 decision 4
   * makes permanent, not a fallback) and exactly one of `output.ndi` /
   * `output.hdmi` matching `output.transport`. Validated and normalized
   * server-side; a rejected payload throws and appends no revision.
   */
  async putShowSurface(id: string, payload: SchemaConfigShowSurface): Promise<SchemaShowSurfaceConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowSurfaceConfigResponse>(
        `/config/show.surface/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.surface/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowSurfaceRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show.surface/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.cue/{id}` (Track H seam H1/H6). Throws (404) when no such cue exists. */
  async getShowCue(id: string): Promise<SchemaShowCueConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowCueConfigResponse>(
        `/config/show.cue/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.cue/{id}` (Track H seam H1/H6, ADR-043).
   * `config:write` only. Full replacement: `outputs` must declare at
   * least one of render/audio/ltc/announcement. Validated and normalized
   * server-side; a rejected payload throws and appends no revision.
   */
  async putShowCue(id: string, payload: SchemaConfigShowCue): Promise<SchemaShowCueConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowCueConfigResponse>(
        `/config/show.cue/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.cue/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowCueRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show.cue/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.playlist/{id}` (Track H seam H1, ADR-043).
   * `config:write` only. Full replacement — `entries` is sent exactly as
   * built, never merged with what the server already holds. Validated
   * and normalized server-side; a rejected payload throws and appends no
   * revision.
   */
  async putShowPlaylist(id: string, payload: SchemaConfigShowPlaylist): Promise<SchemaShowPlaylistConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowPlaylistConfigResponse>(
        `/config/show.playlist/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.playlist/{id}/revisions`: revision history, newest first, metadata only. */
  async getShowPlaylistRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/show.playlist/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/show.active` (ADR-027 decision 3). Throws (404)
   * when nothing has ever been activated.
   */
  async getShowActive(): Promise<SchemaShowActiveConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaShowActiveConfigResponse>('/config/show.active', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/show.active` (ADR-027 decision 3). `config:write`
   * only. This is the sharp control: activating a different show changes
   * what every node is expected to hold (ADR-028) — the caller
   * (views/ShowActive.tsx) must confirm before calling this, never fire it
   * from a bare click.
   */
  async putShowActive(payload: SchemaConfigShowActive): Promise<SchemaShowActiveConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaShowActiveConfigResponse>(
        '/config/show.active',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/show.active/revisions`: revision history, newest first, metadata only. */
  async getShowActiveRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/show.active/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Track F seam F2/F1: the night-session lifecycle controller and the
  // night.session/night.session.active configuration kinds ---------------
  //
  // Same "plain side-call, never touches `this.model` directly" posture as
  // every method above — `getCurrentNightSession`/`getNightSessionById`
  // seed a view's own state (views/NightSession.tsx), and live updates
  // arrive separately via the `nightSession.changed` stream frame
  // (handleFrame/applyNightSessionChanged below), matching
  // getResolumeRecovery's identical split for the same reason (this
  // resource is not part of Snapshot).

  /**
   * `GET /api/v1/night/session` (Track F seam F2). Never gated by any
   * scope (ADR-024 constraint 23) — reads stay open by default, and this
   * one never even throws on 401/403 the way most reads can, since the
   * route itself carries no auth requirement. Answers `200` with
   * `session.state === "inactive"` when no session has ever been
   * created, never `404`.
   */
  async getCurrentNightSession(): Promise<SchemaNightSessionResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNightSessionResponse>('/night/session', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/current-runs`: authoritative runner playback for the Dashboard. */
  async getCurrentRuns(): Promise<SchemaCurrentRunsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaCurrentRunsResponse>('/current-runs', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/night/sessions/{id}` (Track F seam F2). Throws (404) when no session with this id has ever existed. */
  async getNightSessionById(id: string): Promise<SchemaNightSessionResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNightSessionResponse>(
        `/night/sessions/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/night/commands/{command}` (ADR-038). Requires
   * `night:command`. Answers `202`, never `200` — accepted and applied,
   * or recognized as an idempotent duplicate. `idempotencyKey` is
   * honored only by `prepare-site`; every other command ignores it.
   * `interlockOverrides` (Track F seam F6) is honored by every command
   * except `request-final-show` and `end-session`, which REFUSE a
   * non-empty one; each override is consulted only against a rule that
   * declares `overridePolicy: authorized-operator` and only when the
   * caller separately holds `night:override` — `night:command` alone
   * never authorizes a bypass. `skipEnterShowLead` is honored only by
   * `start-night`; sent only when true. Throws a typed `ApiError` on the
   * three distinguishable `409`s (`night-not-ready`, `night-state-rejected`,
   * `night-ambiguous`) and the `503`
   * (`night-command-refused-audit-unavailable`, `prepare-site`/
   * `run-readiness`/`start-preshow`/`start-night` only) — see
   * ShowNight.tsx for the caller that branches on `ApiError.problemType`
   * to render each distinguishably rather than as one generic failure.
   */
  async dispatchNightCommand(
    command: NightCommandName,
    idempotencyKey?: string,
    interlockOverrides?: readonly SchemaNightInterlockOverride[],
    skipEnterShowLead?: boolean,
  ): Promise<SchemaNightCommandResponse> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaNightCommandRequest = {}
      if (idempotencyKey !== undefined) body.idempotencyKey = idempotencyKey
      if (interlockOverrides !== undefined && interlockOverrides.length > 0) {
        body.interlockOverrides = [...interlockOverrides]
      }
      if (command === 'start-night' && skipEnterShowLead === true) body.skipEnterShowLead = true
      return await this.client.postJson<SchemaNightCommandResponse>(
        `/night/commands/${encodeURIComponent(command)}`,
        body,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/night.session/{id}` (Track F seam F1). Throws (404) when no such object exists. */
  async getNightSessionConfig(id: string): Promise<SchemaNightSessionConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNightSessionConfigResponse>(
        `/config/night.session/${encodeURIComponent(id)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/night.session/{id}` (Track F seam F1). `config:write`
   * only (admin). Full replacement — every field this seam's create/edit
   * form does not expose (calendar/duration keys, siteControl, interlocks)
   * must never be sent, since the coordinator rejects the write outright
   * if it sees any of them.
   */
  async putNightSessionConfig(
    id: string,
    payload: SchemaConfigNightSessionWrite,
  ): Promise<SchemaNightSessionConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaNightSessionConfigResponse>(
        `/config/night.session/${encodeURIComponent(id)}`,
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/night.session/{id}/revisions`: revision history, newest first, metadata only. */
  async getNightSessionConfigRevisions(id: string): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        `/config/night.session/${encodeURIComponent(id)}/revisions`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/night.session/{id}/revisions/{revision}`: one
   * past, immutable revision's full payload — no other config kind in
   * this coordinator exposes this route yet (api/openapi.yaml's own
   * description of the endpoint).
   */
  async getNightSessionConfigRevision(id: string, revision: number): Promise<SchemaNightSessionConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNightSessionConfigResponse>(
        `/config/night.session/${encodeURIComponent(id)}/revisions/${revision}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/config/night.session.active` (ADR-039 rule 4). Throws
   * (404) when nothing has ever been activated.
   */
  async getNightSessionActiveConfig(): Promise<SchemaNightSessionActiveConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNightSessionActiveConfigResponse>(
        '/config/night.session.active',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `PUT /api/v1/config/night.session.active` (ADR-039 rule 4).
   * `config:write` only. `session` may be the empty string, which
   * explicitly clears the pointer — the caller (views/NightSessionActive.tsx)
   * must confirm before calling this, matching putShowActive's identical
   * "sharp control" posture.
   */
  async putNightSessionActiveConfig(
    payload: SchemaConfigNightSessionActive,
  ): Promise<SchemaNightSessionActiveConfigResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.putJson<SchemaNightSessionActiveConfigResponse>(
        '/config/night.session.active',
        payload,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/config/night.session.active/revisions`: revision history, newest first, metadata only. */
  async getNightSessionActiveConfigRevisions(): Promise<SchemaConfigRevisionsResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaConfigRevisionsResponse>(
        '/config/night.session.active/revisions',
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/assets`, optionally narrowed by show, sequence, and/or
   * node (ADR-028). Metadata only — bytes never come down this path.
   */
  async listAssets(filter?: { show?: string; sequence?: string; node?: string }): Promise<SchemaAssetsListResponse> {
    const controller = this.beginSideCall()
    try {
      const params = new URLSearchParams()
      if (filter?.show !== undefined && filter.show !== '') params.set('show', filter.show)
      if (filter?.sequence !== undefined && filter.sequence !== '') params.set('sequence', filter.sequence)
      if (filter?.node !== undefined && filter.node !== '') params.set('node', filter.node)
      const query = params.toString()
      return await this.client.getJson<SchemaAssetsListResponse>(
        `/assets${query === '' ? '' : `?${query}`}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `POST /api/v1/assets`, `multipart/form-data` (ADR-028, ADR-030).
   * Requires `asset:write`. `show`, `sequence`, `mediaType`, and
   * `targetKind` are required on every call; `target` is required
   * (non-empty) when `targetKind` is `"node"` — the caller
   * (components/AssetUpload.tsx) must not omit it, since the target is
   * part of the asset's own identity (ADR-028 decision 1). `onProgress`
   * reports real byte counts, matching [uploadResolumeComposition]'s own
   * contract; a rejected upload (400/413/507) registers nothing and the
   * caller must not render it as stored (ADR-030: "a partial upload
   * registers nothing").
   */
  async uploadAsset(
    file: File,
    fields: { show: string; sequence: string; mediaType: 'fseq' | 'audio' | 'media'; targetKind: 'node' | 'show'; target?: string },
    onProgress: (progress: UploadProgress) => void,
  ): Promise<SchemaAssetResponse> {
    const controller = this.beginSideCall()
    try {
      const formFields: Record<string, string> = {
        show: fields.show,
        sequence: fields.sequence,
        mediaType: fields.mediaType,
        targetKind: fields.targetKind,
      }
      if (fields.target !== undefined) formFields.target = fields.target
      return await uploadFileWithProgress<SchemaAssetResponse>(
        this.baseUrl,
        '/assets',
        file,
        onProgress,
        controller.signal,
        formFields,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * The same-origin URL for `GET /api/v1/assets/{id}/content` (ADR-028).
   * Never fetched by this store itself — a download is a plain browser
   * navigation/anchor, which carries the session cookie same-origin per
   * ADR-022 with no code here needing to touch the bytes.
   */
  assetContentUrl(id: string): string {
    return `${this.baseUrl}/assets/${encodeURIComponent(id)}/content`
  }

  /**
   * `GET /api/v1/assets/manifest` (ADR-028 seam E5): every declared
   * node's asset readiness, "what should it hold" versus "what does it
   * hold". `ready`/`not_ready`/`unknown` are three distinct states the
   * caller (views/AssetManifest.tsx) must render distinctly — an
   * `unknown` verdict is never evidence of absence.
   */
  async getAssetManifest(): Promise<SchemaAssetManifestResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaAssetManifestResponse>('/assets/manifest', controller.signal)
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/nodes/{nodeId}/assets`: the same verdict, for one node. 404 when nodeId is not declared. */
  async getNodeAssetManifest(nodeId: string): Promise<SchemaNodeAssetManifestResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaNodeAssetManifestResponse>(
        `/nodes/${encodeURIComponent(nodeId)}/assets`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/audit` (ADR-024 decision 11). Requires `audit:read`
   * always — this is not one of the four pre-existing open-by-default
   * reads (api/openapi.yaml's own description of this route).
   */
  async listAudit(filter?: {
    order?: 'asc' | 'desc'
    since?: number
    before?: number
    limit?: number
  }): Promise<SchemaAuditResponse> {
    const controller = this.beginSideCall()
    try {
      const params = new URLSearchParams()
      if (filter?.order !== undefined) params.set('order', filter.order)
      if (filter?.since !== undefined) params.set('since', String(filter.since))
      if (filter?.before !== undefined) params.set('before', String(filter.before))
      if (filter?.limit !== undefined) params.set('limit', String(filter.limit))
      const query = params.toString()
      return await this.client.getJson<SchemaAuditResponse>(
        `/audit${query === '' ? '' : `?${query}`}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  // -- Step 9: the macro run surface (STEP-9-SPEC.md section 6.6, ADR-031) --

  /**
   * `POST /api/v1/macros/{id}/runs`. Requires `show:macro:run`
   * specifically (never satisfied by `config:write` alone — decision 4).
   * `202`, never a completed result (ADR-031 decision 1): the returned
   * run is its INITIAL state, and the caller learns the outcome by
   * watching `model.macroRuns` (this store's `macroRun.changed` handling
   * below) or by calling [getMacroRun], never by waiting on this
   * response any longer than it takes the coordinator to accept it.
   *
   * Mints a fresh idempotency key per call, exactly like
   * [dispatchFPPCommand] — the caller never supplies one, so two fast
   * clicks on the SAME run control mint two DIFFERENT keys and correctly
   * produce two submissions, one of which the coordinator's own
   * overlap guard (ADR-031 decision 6, `409`) refuses; a caller wanting
   * "this exact click, replayed" is not a case this UI's own run control
   * needs (unlike a machine client retrying after a lost response), so
   * this method does not expose the key as a parameter.
   *
   * `priorFailures`/`priorFailuresDropped` are the FPP plugin's own
   * buffered-degraded-outcome mechanism (STEP-9-SPEC.md section 8.3 path
   * 2) — this UI is never that caller (`trigger: 'ui'` always, never
   * `'plugin'`), so both are always omitted here, matching
   * CreateMacroRunRequest's own "absent means nothing buffered" contract.
   */
  async submitMacroRun(macroId: string): Promise<SchemaMacroRunSubmitResponse> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaCreateMacroRunRequest = {
        idempotencyKey: randomUUIDv4(),
        trigger: 'ui',
      }
      const resp = await this.client.postJson<SchemaMacroRunSubmitResponse>(
        `/macros/${encodeURIComponent(macroId)}/runs`,
        body,
        controller.signal,
      )
      // Immediate, optimistic feedback for the common interactive case —
      // a run THIS browser just started — without waiting for the next
      // `macroRun.changed` frame or reconnect/re-snapshot. Safe as a
      // plain upsert: `resp.run` is this coordinator's own authoritative
      // initial state for this run id, the same shape a snapshot's
      // `macroRuns` entry would carry (steps omitted, matching
      // MacroRunSummary), so this can never disagree with what a
      // subsequent frame or re-snapshot would say. See
      // applyMacroRunChanged's own comment for why an event for a run
      // NOT already known here is instead dropped rather than
      // synthesized — this call site is the one place a not-yet-known
      // run is safe to add, because the full MacroRun (steps aside) came
      // back on THIS response, not reconstructed from a partial delta.
      this.upsertMacroRunSummary(toMacroRunSummary(resp.run))
      return resp
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `GET /api/v1/macro-runs` (STEP-9-SPEC.md section 6.6): most recent first, optionally filtered by macro id and/or state. Steps are not included. */
  async listMacroRuns(filter: { macroId?: string; state?: 'running' | 'finished'; limit?: number } = {}): Promise<SchemaMacroRunsListResponse> {
    const controller = this.beginSideCall()
    try {
      const params = new URLSearchParams()
      if (filter.macroId !== undefined) params.set('macroId', filter.macroId)
      if (filter.state !== undefined) params.set('state', filter.state)
      if (filter.limit !== undefined) params.set('limit', String(filter.limit))
      const qs = params.toString()
      return await this.client.getJson<SchemaMacroRunsListResponse>(
        `/macro-runs${qs === '' ? '' : `?${qs}`}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * `GET /api/v1/macro-runs/{runId}` (STEP-9-SPEC.md section 6.6): one
   * run WITH its steps. Deliberately not part of the push model — "step
   * detail is fetched, not streamed" (section 6.6) — so a caller
   * (views/MacroRunView.tsx) calls this directly on mount and on its own
   * refresh schedule, rather than reading it off `model`.
   */
  async getMacroRun(runId: string): Promise<SchemaMacroRunResponse> {
    const controller = this.beginSideCall()
    try {
      return await this.client.getJson<SchemaMacroRunResponse>(
        `/macro-runs/${encodeURIComponent(runId)}`,
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * Upserts one [SchemaMacroRunSummary] into `model.macroRuns` by `id`,
   * preserving every other entry's relative position (matching
   * `applyNodeChanged`'s own append-if-new/replace-in-place shape). The
   * ONLY two callers are [submitMacroRun] (a run this browser just
   * created) and [applyMacroRunChanged] (a live transition for a run
   * already present) — never called for a runId this store has not
   * already been told exists from an authoritative source, per
   * applyMacroRunChanged's own comment.
   */
  private upsertMacroRunSummary(run: SchemaMacroRunSummary): void {
    const idx = this.model.macroRuns.findIndex((r) => r.id === run.id)
    const macroRuns = idx === -1 ? [run, ...this.model.macroRuns] : replaceAt(this.model.macroRuns, idx, run)
    this.setModel({ ...this.model, macroRuns })
  }

  /**
   * `POST /api/v1/fpp/{instanceId}/commands` (Step 7 seam C, Step 8,
   * ADR-001/ADR-003) — the one dispatch path every FPP primitive control
   * (docs/bench/fpp-command-vocabulary.md section 4's eight-member
   * registry) shares. Mints a fresh idempotency key per call via
   * `randomUUIDv4()` (./uuid.ts) — NOT a bare `crypto.randomUUID()` call,
   * which is secure-context-only and is simply absent (not thrown, not
   * caught) on the plain `http://` origin this UI actually deploys to
   * (ADR-022; see uuid.ts's own doc comment for the full story) —
   * (RES-015 section 7.3: FPP supplies nothing a coordinator could derive
   * one from, so the CALLER mints it — the same rule showmeshctl's own
   * subcommands follow independently). `params` is passed through
   * UNTYPED (this method's own signature, not `postJson`'s, which already
   * takes `body: unknown`): api/openapi.yaml declares `FPPCommandRequest
   * .params` as a bare `object` with no nested JSON Schema `properties`
   * (the per-action shape lives in prose, in that field's own
   * description, not in the schema), so openapi-typescript generates
   * `Record<string, never>` for it — a type that, taken literally, no
   * non-empty object satisfies. Typing this method's own `params`
   * parameter against that generated type would make every real call
   * (`{ playlist: 'x', repeat: false, ifBusy: 'refuse' }` and so on) a
   * compile error, so this method deliberately does not attempt to; the
   * per-action shape is instead enforced ONCE, server-side, exactly as
   * strictly (`decodeFPPCommandParams`, fppcommand_primitives.go: absent
   * key, explicit null, and empty string are three different rejections)
   * — this client sends what the caller asked for and reports the
   * server's own 400 back through `describeApiError` like any other
   * validation failure, never re-validates a shape it cannot express in
   * TypeScript.
   *
   * `request` is now REAL, typed, per-action data (`FPPCommandDispatchArgs`
   * — api/openapi.yaml's own discriminated `oneOf` on `action`, generated
   * from the schema, minus `idempotencyKey`), not an untyped
   * `Record<string, unknown>`. Before this fix, api/openapi.yaml declared
   * `FPPCommandRequest.params` as a bare `object` with no nested JSON
   * Schema `properties` (the per-action shape lived only in prose), so
   * openapi-typescript generated `Record<string, never>` for it — a type
   * that, taken literally, no non-empty object satisfies — and this
   * method's own `params` parameter had to be typed `Record<string,
   * unknown>` by hand to avoid making every real call a compile error.
   * That meant the ONE place this endpoint's entire request shape was
   * checked was server-side (`decodeFPPCommandParams`,
   * fppcommand_primitives.go) — ADR-015 satisfied in form ("generated from
   * the Go types") but not in substance (nothing generated could actually
   * express a non-empty params object). Now each of the eight callers
   * below passes a `request` literal TypeScript checks against ITS OWN
   * variant's real shape — a misspelled `playlist` (e.g. `paylist`), a
   * wrong type, or an extra key is a compile error, not a 400 discovered
   * at runtime.
   *
   * `params` is OMITTED from the request body entirely (not sent as
   * `{}`) whenever the caller's own object literal does not set it —
   * capture section 1.4/spec section 2's own rule, "absent, null, and
   * empty are three different things," applies to THIS client's own
   * outbound request exactly as much as to the server it is calling: a
   * zero-argument primitive (stopPlaylist, pausePlaylist, resumePlaylist,
   * nextPlaylistItem, prevPlaylistItem) has never sent a `params` key at
   * all (unchanged from Step 7 — `NoParamsFPPCommandRequest.params` is
   * generated OPTIONAL, so the callers below simply never set it), and
   * this method keeps that exact wire shape rather than switching to
   * `params: {}` merely because a generated type could now express it.
   *
   * Returns the decoded `command` object as-is — including its own
   * `outcome` field, which is "confirmed", "unconfirmed", or (the one
   * accepted replay race) empty, NEVER inferred from this call's own
   * success: a resolved `Promise` here means the HTTP round trip
   * succeeded, not that the command's effect was confirmed. Every caller
   * (the FPP*Control components) is responsible for rendering outcome
   * honestly, matching ADR-003 exactly the way the CLI's own
   * `reportFPPCommandResult` does. Unlike login/logout/claimBootstrap,
   * this does NOT call `wakeReadLoop()` — a command dispatch is not a
   * credential change, and has no reason to interrupt the SSE connection.
   *
   * FPP_COMMAND_REQUEST_TIMEOUT_MS, never the default request budget
   * every other call here uses (Step 7 seam C review defect 1): this is a
   * long request by design, since the coordinator waits out its own
   * confirmation deadline before answering — see that constant's own doc
   * comment. Every primitive shares ONE client-side budget here because
   * client.ts cannot import the Go per-primitive `ConfirmDeadline`
   * functions (module boundary, see FPP_COMMAND_REQUEST_TIMEOUT_MS's own
   * comment) and — per fppcommand_primitives.go's own
   * `fppConfirmDeadlineUnchanged` — every registered primitive uses the
   * SAME server-side deadline today; the day a primitive's own deadline
   * diverges, this client's budget still bounds it correctly because
   * FPP_COMMAND_REQUEST_TIMEOUT_MS was derived from the coordinator's
   * configured base plus margin, not from any one primitive's number.
   */
  private async dispatchFPPCommand(
    instanceId: string,
    request: FPPCommandDispatchArgs,
  ): Promise<FPPCommandResult> {
    const controller = this.beginSideCall()
    try {
      const body: SchemaFPPCommandRequest = {
        ...request,
        idempotencyKey: randomUUIDv4(),
      } as SchemaFPPCommandRequest
      const resp = await this.client.postJson<SchemaFPPCommandResponse>(
        `/fpp/${encodeURIComponent(instanceId)}/commands`,
        body,
        controller.signal,
        FPP_COMMAND_REQUEST_TIMEOUT_MS,
      )
      return resp.command
    } finally {
      this.endSideCall(controller)
    }
  }

  /** `{"action":"stopPlaylist"}` — unchanged from Step 7. See [dispatchFPPCommand]. */
  async stopFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'stopPlaylist' })
  }

  /**
   * `{"action":"startPlaylist","params":{playlist,repeat,ifBusy}}`
   * (Step 8, capture sections 4/5). `ifBusy` defaults to `"refuse"`
   * SERVER-side when omitted, but this method always sends it explicitly
   * — the caller (FPPStartPlaylistControl) decides "refuse" vs "replace"
   * per attempt, and sending it explicitly makes that choice visible in
   * this method's own signature rather than relying on a default the
   * caller has to remember exists. A `409` here means a DIFFERENT
   * playlist is confirmed playing (or the evidence to decide that is not
   * current) — the caller renders `err.message` (the coordinator's own
   * `detail`, which already names what is playing, per
   * fppStartPlaylistBusyProblem) and offers the explicit `ifBusy:
   * "replace"` retry; this method never retries on its own.
   */
  async startFPPPlaylist(
    instanceId: string,
    playlist: string,
    repeat: boolean,
    ifBusy: 'refuse' | 'replace',
  ): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'startPlaylist', params: { playlist, repeat, ifBusy } })
  }

  /**
   * `{"action":"stopPlaylistGracefully","params":{afterLoop}}` (capture
   * section 3.3/4). Confirmed does NOT mean stopped — see
   * FPPStopPlaylistGracefullyControl's own comment; this method itself
   * does nothing beyond dispatch, exactly like every other action here.
   */
  async stopFPPPlaylistGracefully(instanceId: string, afterLoop: boolean): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'stopPlaylistGracefully', params: { afterLoop } })
  }

  /** `{"action":"pausePlaylist"}`, no params — see [dispatchFPPCommand]. */
  async pauseFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'pausePlaylist' })
  }

  /** `{"action":"resumePlaylist"}`, no params — see [dispatchFPPCommand]. */
  async resumeFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'resumePlaylist' })
  }

  /**
   * `{"action":"nextPlaylistItem"}`, no params. Capture section 3.5: at
   * the last item, one Next Playlist Item ENDS the playlist — this
   * method sends the same command regardless of playlist position; the
   * caller (FPPNextPlaylistItemControl) is what looks at
   * `fpp.playlist.index`/`fpp.playlist.count` to warn before the click,
   * since this method has no observation evidence to consult from here.
   */
  async nextFPPPlaylistItem(instanceId: string): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'nextPlaylistItem' })
  }

  /** `{"action":"prevPlaylistItem"}`, no params — see [dispatchFPPCommand]. */
  async prevFPPPlaylistItem(instanceId: string): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'prevPlaylistItem' })
  }

  /**
   * `{"action":"setVolume","params":{volume}}` (capture section 3.6/4).
   * `volume` is sent exactly as the caller supplied it — this method does
   * NOT clamp or coerce (capture section 1.5: FPP itself silently clamps
   * an out-of-range value and coerces a garbage one to 0, and this
   * project's own standing rule is not to repeat that). Range validation
   * (0-100) lives in FPPSetVolumeControl, client-side, so the operator
   * sees why a value was rejected before a round trip, and the server
   * (`fppcommand.ValidateVolume`) enforces the same rule independently —
   * two checks that must agree are safer than one the client trusts
   * blindly.
   */
  async setFPPVolume(instanceId: string, volume: number): Promise<FPPCommandResult> {
    return this.dispatchFPPCommand(instanceId, { action: 'setVolume', params: { volume } })
  }

  /**
   * `DELETE /api/v1/nodes/{nodeId}/declaration`. The server itself
   * requires `{"confirm":true}` in the body (BUILD-PLAN Step 7 seam B
   * B2) — this method always sends it, so the UI-level confirmation
   * dialog (NodesList.tsx) is what actually gates the call from ever
   * being made, in addition to, never instead of, the server's own
   * check.
   */
  async deleteNodeDeclaration(nodeId: string): Promise<void> {
    const controller = this.beginSideCall()
    try {
      await this.client.deleteJson(
        `/nodes/${encodeURIComponent(nodeId)}/declaration`,
        { confirm: true },
        controller.signal,
      )
    } finally {
      this.endSideCall(controller)
    }
  }

  /**
   * ADR-024 decision 6 / client.ts's `request()`: "an Authorization
   * header, if present at all, is the only credential path considered
   * for this request" — a stored break-glass token always wins over the
   * cookie the browser would otherwise attach, on EVERY request,
   * including the ones this store makes right after a successful
   * cookie-authenticated login. Left alone, a token that was revoked (or
   * simply wrong) permanently shadows a session this operator just
   * proved is valid: every subsequent request keeps presenting the dead
   * token, the coordinator keeps answering as if uncredentialed, and
   * `GET /session` keeps reporting `authenticated: false` forever — the
   * persistent sign-in banner (SessionPanel.tsx) would show "signed out"
   * even immediately after a login this method just confirmed succeeded.
   * A successful login or bootstrap claim is exactly the signal that the
   * cookie path works again, so this clears whatever token was stored
   * rather than leaving it to keep winning silently. (The operator also
   * has a direct way to do this without signing in first — see
   * SessionPanel.tsx's "Clear stored token" affordance, which calls
   * `clearToken()` below.)
   */
  private clearShadowingToken(): void {
    clearStoredToken()
  }

  /**
   * `GET /api/v1/session`, silently. Never throws: this is the background
   * refresh fired at connect() and on every stream reconnect
   * (reloadSnapshot below), and nothing is synchronously waiting on it to
   * report a failure to. A failure instead sets `sessionFetchFailed`,
   * which app/session.ts's scope-gate treats as "cannot currently vouch
   * for this" — the ADR-024 decision 12 degradation — while leaving the
   * last-known `session` in place rather than discarding it (a momentary
   * blip must not flash "signed out" over a session that is actually
   * fine).
   */
  private async fetchSession(signal: AbortSignal): Promise<void> {
    try {
      const resp = await this.client.getJson<SchemaSessionResponse>('/session', signal)
      this.applySessionResponse(resp)
    } catch (err) {
      if (isAbortError(err)) return
      this.setModel({ ...this.model, sessionFetchFailed: true })
    }
  }

  /**
   * Applies a `SessionResponse` from any of the four call sites above.
   * Guarded by `serverTime` rather than arrival order: connect()'s
   * independent fetch and the first reconnect's own reloadSnapshot
   * refresh are fired close together on purpose (see connect()'s
   * comment), and either may resolve second — this store's existing
   * posture (store.ts's header comment; computeClockSkewMs) is that the
   * coordinator's own clock, not receipt order, is authoritative, so a
   * response that is (by serverTime) OLDER than what's already held is
   * dropped rather than allowed to overwrite fresher data with stale.
   * An unparseable serverTime is treated as "not older" (applied
   * anyway) rather than silently discarded — this mirrors
   * IncompatibleVersionError's own posture of surfacing a malformed
   * server response instead of pretending it didn't happen.
   */
  private applySessionResponse(resp: SchemaSessionResponse): void {
    const incomingMs = Date.parse(resp.serverTime)
    const currentMs = this.model.session === null ? -Infinity : Date.parse(this.model.session.serverTime)
    if (!Number.isNaN(incomingMs) && !Number.isNaN(currentMs) && incomingMs < currentMs) return
    this.setModel({
      ...this.model,
      session: resp,
      sessionReceivedAt: this.now(),
      sessionFetchFailed: false,
    })
  }

  // -- the loop -----------------------------------------------------------

  private setModel(next: Model): void {
    this.model = next
    for (const listener of this.listeners) listener()
  }

  private setConnection(connection: ConnectionState): void {
    this.setModel({ ...this.model, connection })
  }

  private async runLoop(): Promise<void> {
    while (!this.disposed) {
      this.loopAbort = new AbortController()
      const signal = this.loopAbort.signal

      if (this.attempt > 0) {
        const delay = computeBackoffMs(this.attempt, this.backoffConfig)
        this.setConnection({
          kind: 'reconnecting',
          attempt: this.attempt,
          nextAttemptAt: this.now() + delay,
          lastError: this.lastError ?? 'unknown error',
        })
        try {
          await sleep(delay, signal)
        } catch {
          if (this.disposed) break
          continue // interrupted by submitToken/clearToken: retry now
        }
      } else {
        this.setConnection({ kind: 'connecting' })
      }
      if (this.disposed) break

      const gen = ++this.generation
      try {
        await this.runConnectionAttempt(gen, signal)
        // runConnectionAttempt only resolves normally if the read loop
        // exits without throwing, which its own implementation never
        // does (every exit path below throws). Treat an unexpected
        // normal return defensively as a retryable failure rather than
        // silently looping forever on an assumption that turned out to
        // be wrong.
        this.attempt += 1
        this.lastError = 'stream ended unexpectedly'
        continue
      } catch (err) {
        if (isAbortError(err)) {
          if (this.disposed) break
          continue // interrupted deliberately: retry now, per submitToken/clearToken
        }

        if (err instanceof UnauthorizedError) {
          this.attempt = 0
          // ADR-024 item 7: "a closed change stream may mean a revoked
          // session ... the client's existing reconnect and snapshot path
          // then hits a 401, which must surface as an explicit
          // authentication state." That's the `setConnection` call below,
          // unchanged since Step 4 — this refresh is what ALSO updates
          // the persistent sign-in banner (app/session.ts,
          // model.session), which is a separate piece of state from
          // `connection` and would otherwise still read "signed in" from
          // whatever /session last reported, stale, until the operator
          // happened to trigger another read of it. Fire-and-forget: the
          // 'unauthorized' connection state below must render immediately
          // regardless of how long this fetch takes, and fetchSession
          // never throws.
          void this.fetchSession(signal)
          if (err.tokenWasPresent) {
            // Spec section 5.6: "clear the stored token and return to
            // unauthorized with a distinguishable ... detail, so a
            // wrong secret does not present as a missing one." Clearing
            // it is what makes the NEXT attempt (whether a manual
            // submitToken or an operator reload) present as "missing"
            // rather than silently retrying the same rejected secret.
            clearStoredToken()
          }
          this.setConnection({
            kind: 'unauthorized',
            reason: err.tokenWasPresent ? 'rejected' : 'missing',
          })
          // No backoff retry storm (spec section 5.4/5.6): pause here
          // until submitToken/clearToken aborts this signal, then retry
          // immediately with whatever token is now stored.
          try {
            await waitUntilAborted(signal)
          } catch {
            /* woken by submitToken/clearToken, or disposed */
          }
          if (this.disposed) break
          continue
        }

        if (err instanceof IncompatibleVersionError) {
          // Terminal (spec section 5.4): retrying against a coordinator
          // that will never serve this version is a loop, not a
          // recovery. Stop the loop entirely.
          this.setConnection({
            kind: 'incompatible',
            requiredVersion: err.requiredVersion,
            supportedVersions: err.supportedVersions,
            detail: err.message,
          })
          break
        }

        if (err instanceof Error) {
          // Every other classified error (network failure, a plain
          // ApiError from a non-2xx response, the stream closing) is
          // treated as retryable.
          this.attempt += 1
          this.lastError = err.message
          continue
        }

        // Defensive catch-all: something that isn't even an Error was
        // thrown. This should not be reachable by any code path in this
        // module; if it happens, retrying blindly is more likely to
        // mask a real bug than fix a transient condition, so this is
        // the one truly terminal, non-retryable state.
        this.setConnection({ kind: 'failed', detail: `unexpected error: ${String(err)}` })
        break
      }
    }
    this.running = false
  }

  // -- one connection attempt -------------------------------------------

  private async runConnectionAttempt(gen: number, signal: AbortSignal): Promise<void> {
    // ADR-023 decision 1: `?deltas=1` is the ONLY literal value that opts a
    // connection into `fpp.observations.changed` frames; sent
    // unconditionally here rather than behind a flag because a coordinator
    // that predates ADR-023 (or any other value it might see) serves this
    // connection exactly what it always served — additive by construction,
    // nothing for this client to negotiate.
    const response = await this.client.request('/stream?deltas=1', signal)
    if (response.body === null) {
      throw new Error('stream response had no body')
    }
    const reader = response.body.getReader()
    const parser = new SSEParser()
    try {
      for (;;) {
        const { value, done } = await this.readWithIdleTimeout(reader)
        if (gen !== this.generation) throw signal.reason ?? new Error('superseded')
        if (done) {
          // "A stream closes with a clean end-of-response and no
          // terminating frame of any kind" (api/openapi.yaml) —
          // indistinguishable from a network fault by design. Reconnect
          // and re-snapshot either way, exactly like any other
          // interruption.
          throw new Error('stream closed')
        }
        const frames = parser.push(value)
        for (const frame of frames) {
          await this.handleFrame(frame, gen, signal)
          if (gen !== this.generation) throw signal.reason ?? new Error('superseded')
        }
      }
    } finally {
      reader.cancel().catch(() => {
        /* connection is going away regardless; nothing to react to */
      })
    }
  }

  /**
   * `reader.read()`, but raced against an idle deadline (D2:
   * STREAM_IDLE_TIMEOUT_MS) so a half-open socket — one where the
   * connection accepted at the TCP level but nothing is arriving, ever,
   * not even a `: keepalive` comment — does not leave this loop awaiting
   * `read()` forever with the connection state stuck reporting `live`.
   * The deadline is naturally reset on every call: as soon as one read
   * resolves with ANY bytes (a real frame or just a keepalive comment,
   * both arrive as raw bytes to `reader.read()` before this class ever
   * parses them), the loop calls this method again and a fresh timer
   * starts. On expiry this throws a plain Error, which the caller treats
   * exactly like a socket drop or a parse failure: reconnect (with
   * backoff) and re-snapshot, per store.ts's header comment — this
   * function does not attempt to distinguish "idle timeout" from any
   * other interruption once it's outside this method.
   */
  private async readWithIdleTimeout(
    reader: ReadableStreamDefaultReader<Uint8Array>,
  ): Promise<ReadableStreamReadResult<Uint8Array>> {
    let timer: TimerHandle | undefined
    const idleDeadline = new Promise<never>((_resolve, reject) => {
      timer = this.clock.setTimeout(() => {
        reject(
          new Error(
            `stream received no bytes (not even a keepalive comment) for over ${this.streamIdleTimeoutMs}ms; treating the connection as dead`,
          ),
        )
      }, this.streamIdleTimeoutMs)
    })
    try {
      return await Promise.race([reader.read(), idleDeadline])
    } finally {
      if (timer !== undefined) this.clock.clearTimeout(timer)
    }
  }

  private async handleFrame(frame: SSEFrame, gen: number, signal: AbortSignal): Promise<void> {
    switch (frame.event) {
      case 'stream.start':
      case 'stream.reset': {
        // Both require an authoritative resnapshot before anything else
        // is applied (ADR-020 decision 3). This is deliberately
        // unconditional on successfully parsing the frame's own JSON:
        // even if that parse fails, resnapshotting is still correct —
        // for stream.start it always was, and a stream.reset whose body
        // failed to parse must not be read as "no reset happened".
        await this.reloadSnapshot(gen, signal)
        // ADR-024 decision 11's amendment: auditStore carries no
        // change-stream event of its own (see AUDIT_STORE_POLL_INTERVAL_MS's
        // own doc comment for why), so this generation's own poll loop is
        // what keeps it live for the rest of this connection. One loop
        // per generation: a fresh stream.start/stream.reset starts a new
        // one, and the old one's own gen check stops it on its very next
        // tick without needing to be cancelled explicitly.
        void this.pollAuditStoreLoop(gen, signal)
        return
      }
      case 'node.changed': {
        const payload = tryParse<{ serverTime: string; node: SchemaNode }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyNodeChanged(payload.node, payload.serverTime)
        return
      }
      case 'fpp.changed': {
        const payload = tryParse<{ serverTime: string; instance: SchemaFPPInstance }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyFppChanged(payload.instance, payload.serverTime)
        return
      }
      case 'fpp.observations.changed': {
        // ADR-023 decision 3a: this frame's payload shape (a bag of
        // `changed`/`removed`, no whole-`instance` field at all) is
        // structurally incapable of being handed to applyFppChanged's
        // replacement path — there is nothing here to replace WITH. That
        // is deliberate: it is what stops this call site from being able
        // to confuse the two merge semantics, not just a naming
        // convention.
        const payload = tryParse<{
          serverTime: string
          instanceId: string
          changed: Evidence[]
          removed: string[]
        }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyFppObservationsChanged(payload.instanceId, payload.changed, payload.removed, payload.serverTime)
        return
      }
      case 'event.recorded': {
        const payload = tryParse<{ serverTime: string; event: SchemaEvent }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyEventRecorded(payload.event, payload.serverTime)
        return
      }
      case 'resolume.changed': {
        // Track D seam D-4: mirrors `fpp.changed` exactly — this event
        // carries the instance's COMPLETE current representation, never a
        // delta. There is no `resolume.observations.changed` variant.
        const payload = tryParse<{ serverTime: string; instance: SchemaResolumeInstance }>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyResolumeChanged(payload.instance, payload.serverTime)
        return
      }
      case 'macroRun.changed': {
        // Unlike every case above, this frame's own schema (MacroRunChangedEvent)
        // carries serverTime as one of its OWN top-level fields rather than
        // being nested under a wrapper — see the /stream table in
        // api/openapi.yaml. Parsed as the whole event, not destructured.
        const payload = tryParse<SchemaMacroRunChangedEvent>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyMacroRunChanged(payload)
        return
      }
      case 'nightSession.changed': {
        // Track F seam F2: mirrors `resolume.changed`'s own shape — this
        // frame's schema (NightSessionChangedEvent) carries serverTime as
        // one of its OWN top-level fields, alongside the full current
        // `session`, never a delta. Parsed as the whole event, matching
        // `macroRun.changed`'s own parsing above.
        const payload = tryParse<SchemaNightSessionChangedEvent>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyNightSessionChanged(payload)
        return
      }
      case 'fppPlaylistEntry.changed': {
        // Mirrors `fpp.changed`/`node.changed`'s own shape, a
        // `serverTime` wrapper around one full-frame value (`observation`
        // here, `instance`/`node` there), not `macroRun.changed`'s
        // flattened-top-level shape. This frame's own `seq` field is
        // per-connection only (api/openapi.yaml's FPPPlaylistEntryChangedEvent),
        // never a durable cursor, so it is deliberately not read here.
        const payload = tryParse<{ serverTime: string; observation: SchemaFPPPlaylistEntryObservation }>(
          frame.data,
        )
        if (payload === null || gen !== this.generation) return
        this.applyFppPlaylistEntryChanged(payload.observation, payload.serverTime)
        return
      }
      case 'currentRuns.changed': {
        // This is a complete replacement, not a cursor or delta. A client
        // already connected can apply it immediately; reconnects fetch the
        // REST authority in reloadSnapshot below.
        const payload = tryParse<SchemaCurrentRunsChangedEvent>(frame.data)
        if (payload === null || gen !== this.generation) return
        this.applyCurrentRuns({
          serverTime: payload.serverTime,
          activeShow: payload.activeShow,
          runs: payload.runs,
        })
        return
      }
      default:
        // Unknown event: name — ignored, not an error. v1 is
        // additive-only (api/openapi.yaml's /stream description).
        return
    }
  }

  private async reloadSnapshot(gen: number, signal: AbortSignal): Promise<void> {
    const snapshot = await this.client.getJson<SchemaSnapshot>('/snapshot', signal)
    if (gen !== this.generation) return
    this.applySnapshot(snapshot)

    // D1 fix: `GET /events` with no `since` defaults to `since=0`, an
    // EXCLUSIVE lower bound over ascending seq (api/openapi.yaml, and
    // internal/coordinator/store/events.go's `WHERE seq > ? ORDER BY seq
    // ASC LIMIT ?`) — that returns the OLDEST retained page, not the
    // newest, once history exceeds one page. The snapshot's own
    // `latestEventSeq` is exactly the anchor api/openapi.yaml describes
    // for this ("fetch this snapshot once and then request exactly the
    // event history after it via GET /events?since="), so derive a
    // recent window from it instead of taking the endpoint's bare
    // default.
    //
    // `seq` is a durable but non-contiguous cursor — retention pruning
    // (pruneEvents) deletes rows, so `since` values do not correspond
    // 1:1 with row counts. `since = latestEventSeq - INITIAL_EVENTS_WINDOW`
    // can therefore return FEWER than INITIAL_EVENTS_WINDOW events (some
    // seqs in that range were pruned) but never MORE — that is the
    // correct, accepted behavior of an exclusive lower bound and is not
    // worked around here.
    const since = Math.max(0, snapshot.latestEventSeq - INITIAL_EVENTS_WINDOW)
    const events = await this.client.getJson<SchemaEventsResponse>(
      `/events?since=${since}&limit=${INITIAL_EVENTS_WINDOW}`,
      signal,
    )
    if (gen !== this.generation) return
    this.applyInitialEvents(events)

    // GET /current-runs is the reconnect authority for playback. Keep a
    // failed read visible as unknown while allowing the inventory stream to
    // remain live; a transient current-runs failure must not reconnect the
    // entire shell or make stale macro-run data masquerade as playback.
    // Do not hold the inventory stream's live transition on this auxiliary
    // projection. Older coordinators and a temporarily unavailable runner
    // may leave this read pending, while the stream itself still provides a
    // valid inventory baseline. The request remains tied to the connection
    // signal and its result is applied only for this generation.
    void this.fetchCurrentRuns(gen, signal)

    // ADR-024 decision 12: a reconnect is exactly the moment the coordinator
    // may have closed the PREVIOUS connection over a generation bump (decision
    // 5's "closes open streams and forces a re-fetch, so the stale window is
    // bounded") — refreshing session/scopes here, on every reconnect, is what
    // makes that bound real on the client side rather than only asserted
    // coordinator-side. Swallows its own errors (see fetchSession) so a
    // /session hiccup never blocks reaching `live`.
    await this.fetchSession(signal)
    if (gen !== this.generation) return

    this.attempt = 0
    this.lastError = null
    if (this.model.connection.kind !== 'live') {
      this.setConnection({ kind: 'live', connectedAt: this.now() })
    }
  }

  /**
   * Keeps `Model.auditStore` live for the rest of this generation's
   * connection: see AUDIT_STORE_POLL_INTERVAL_MS's own doc comment for
   * why this exists at all (no change-stream event carries this field).
   * Sleeps first: `reloadSnapshot` just fetched a fresh value moments
   * ago, so an immediate re-fetch would be redundant. Every fetch and
   * every wake-up re-checks `gen`/`signal` before touching the model or
   * scheduling the next tick, so a reconnect or dispose() started by
   * another generation stops this loop within one interval, without this
   * loop needing its own cancellation token.
   */
  private async pollAuditStoreLoop(gen: number, signal: AbortSignal): Promise<void> {
    for (;;) {
      await this.sleep(this.auditStorePollIntervalMs)
      if (signal.aborted || gen !== this.generation) return
      try {
        const snapshot = await this.client.getJson<Pick<SchemaSnapshot, 'auditStore'>>('/snapshot', signal)
        if (signal.aborted || gen !== this.generation) return
        this.setModel({ ...this.model, auditStore: snapshot.auditStore })
      } catch {
        // Best-effort: a failed poll fetch leaves the model's own
        // auditStore at its last-known value and simply retries next
        // tick. A connection genuinely gone stale or dead is the read
        // loop's own idle-timeout/reconnect concern, not this loop's;
        // duplicating that handling here would only race it.
      }
    }
  }

  /** this.clock-driven so store.test.ts can advance it deterministically, matching every other timer in this file. */
  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      this.clock.setTimeout(resolve, ms)
    })
  }

  // -- model mutation -----------------------------------------------------

  private computeClockSkewMs(serverTimeIso: string, nowMs: number): number {
    // Positive means the coordinator's clock reads ahead of the
    // browser's. See domain.ts's Model.clockSkewMs and spec section 5.3:
    // this is raw bookkeeping for seam C to surface past a threshold,
    // not itself a judgement about which clock is "right". `nowMs` is
    // taken as a parameter (rather than calling `this.now()` internally)
    // so every caller below captures exactly one browser-clock reading
    // per mutation and reuses it for `serverTimeReceivedAt` too — the
    // skew figure and the effective-now anchor (app/time.ts's
    // effectiveServerTimeIso) must agree on what "the browser clock said
    // when this serverTime arrived" was.
    return Date.parse(serverTimeIso) - nowMs
  }

  private applySnapshot(snapshot: SchemaSnapshot): void {
    const receivedAt = this.now()
    this.currentRunsUpdateCounter += 1
    this.setModel({
      ...this.model,
      serverTime: snapshot.serverTime,
      clockSkewMs: this.computeClockSkewMs(snapshot.serverTime, receivedAt),
      snapshotReceivedAt: receivedAt,
      serverTimeReceivedAt: receivedAt,
      // The API guarantees nodes ascending by nodeId already
      // (api/openapi.yaml Snapshot.nodes); re-sorting is a cheap,
      // defensive no-op against that guarantee, not a correction of it.
      nodes: [...snapshot.nodes].sort(compareByNodeId),
      fpp: snapshot.fpp.instances,
      collectors: snapshot.collectors,
      // Step 9 / ADR-020 decision 3: "in-flight runs must appear in
      // /api/v1/snapshot" — a plain wholesale replace, exactly like
      // `nodes`/`fpp` above, because `Snapshot.macroRuns` is this
      // coordinator's own authoritative current window (in-flight plus a
      // bounded recently-finished tail), not a delta this store needs to
      // merge.
      macroRuns: snapshot.macroRuns,
      currentRuns: null,
      currentRunsReceivedAt: null,
      currentRunsFetchFailed: false,
      // Track D seam D-4 (build contract §1.7): a plain wholesale replace,
      // exactly like `fpp` above — `Snapshot.resolume` is this
      // coordinator's own authoritative current list, never a delta this
      // store needs to merge.
      resolume: snapshot.resolume,
      // ADR-024 decision 11's amendment (owner ruling, 2026-08-26): a plain
      // wholesale replace, exactly like `resolume` above -- `Snapshot.auditStore`
      // is this coordinator's own live, current signal, never a delta this
      // store needs to merge.
      auditStore: snapshot.auditStore,
      // Review finding 9: `Snapshot` carries no `nightSession` field
      // (domain.ts's own comment on Model.nightSession), so unlike every
      // field above this one is not being refreshed here, it is being
      // INVALIDATED. `applySnapshot` runs on every resnapshot — the
      // initial connect, every reconnect, and every `stream.reset` — each
      // of which is a generation boundary this store cannot vouch for
      // continuity across. The coordinator's stream hub keys change
      // detection on its own hub-wide "last rendered" map (stream.go), so
      // a freshly (re)connected client gets no `nightSession.changed`
      // frame at all until the session's state next actually moves —
      // without this, a stale, possibly wrong-session value from BEFORE
      // the reconnect would keep rendering as current indefinitely. Null
      // forces the view back to its own GET (views/NightSession.tsx) to
      // re-establish ground truth rather than trusting a value this
      // connection has no evidence still holds.
      nightSession: null,
      // Same "invalidate, do not carry forward" posture as
      // `nightSession` immediately above, and for the identical reason:
      // this is not part of `Snapshot` either (Model.fppPlaylistEntryObservations's
      // own comment), so a stale entry from before a reconnect must not
      // keep rendering as current across a generation boundary this
      // connection cannot vouch for.
      fppPlaylistEntryObservations: [],
    })
  }

  private applyCurrentRuns(currentRuns: CurrentRunsResponse): void {
    this.currentRunsUpdateCounter += 1
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      currentRuns,
      currentRunsReceivedAt: receivedAt,
      currentRunsFetchFailed: false,
      serverTime: currentRuns.serverTime,
      clockSkewMs: this.computeClockSkewMs(currentRuns.serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
    })
  }

  private async fetchCurrentRuns(gen: number, signal: AbortSignal): Promise<void> {
    const updateCounter = this.currentRunsUpdateCounter
    try {
      const currentRuns = await this.client.getJson<SchemaCurrentRunsResponse>('/current-runs', signal)
      // A full-frame SSE update received while this REST read was pending is
      // newer than the request's starting point. Never let the older REST
      // response roll the model back over that event.
      if (gen !== this.generation || updateCounter !== this.currentRunsUpdateCounter) return
      this.applyCurrentRuns(currentRuns)
    } catch (err) {
      if (isAbortError(err) || gen !== this.generation || updateCounter !== this.currentRunsUpdateCounter) return
      this.setModel({ ...this.model, currentRunsFetchFailed: true })
    }
  }

  /**
   * `macroRun.changed` (STEP-9-SPEC.md section 6.6): a run's own state
   * transition, carrying [MacroRunSummary]'s fields minus everything a
   * summary knows but a transition event does not (macroRevision, show,
   * trigger, issuer, createdAt) — structurally a DELTA, not "this run's
   * complete current representation" the way `fpp.changed` is for an FPP
   * instance. If `runId` is already present in `model.macroRuns` (it
   * arrived via the last snapshot, or via [submitMacroRun]'s own
   * optimistic upsert), update those five fields in place. If it is NOT
   * present — a run this connection has never been told about by an
   * authoritative source — the frame is DROPPED rather than synthesizing
   * a partial MacroRunSummary with invented values for the fields this
   * event does not carry: exactly [applyFppObservationsChanged]'s own
   * posture for the identical reason (that method's own comment), and
   * for the identical reason [applyFppChanged] does NOT drop for a new
   * FPP instance — its `fpp.changed` frame carries the instance's WHOLE
   * representation, so there is something real to add; this event does
   * not. A run started by another client (the CLI, the FPP plugin,
   * another browser tab) while this connection is live therefore only
   * appears here once its FIRST transition after this connection already
   * knows it — practically: after the next reconnect/re-snapshot, which
   * ADR-020 decision 3's snapshot inclusion already bounds. Recorded here
   * rather than silently accepted as a gap, per this task's own report.
   */
  private applyMacroRunChanged(event: SchemaMacroRunChangedEvent): void {
    const idx = this.model.macroRuns.findIndex((r) => r.id === event.runId)
    if (idx === -1) return
    const existing = this.model.macroRuns[idx]
    if (existing === undefined) return // unreachable — see applyFppObservationsChanged's identical guard
    const updated: SchemaMacroRunSummary = {
      ...existing,
      state: event.state,
      completed: event.completed,
      confirmed: event.confirmed,
      reason: event.reason,
      attributionDegraded: event.attributionDegraded,
      // [MacroRunChangedEvent] carries no finishedAt (see the type alias
      // above and its own doc comment) — the transition tells this store
      // THAT the run finished, never WHEN. Substituting the event's
      // `serverTime` here would be a fabricated fact stored as though the
      // coordinator had reported it (this task's own finding 6; CLAUDE.md's
      // standing rule that absent evidence is stated, never invented), and
      // it would be silently WRONG by however long this SSE frame took to
      // arrive after the actual finish. Left exactly as `existing` held it
      // (null, unless a fuller fetch already populated it) — a consumer
      // wanting the real value calls `getMacroRun`, which returns the
      // coordinator's own authoritative `finishedAt` for this run.
      finishedAt: existing.finishedAt,
    }
    const macroRuns = replaceAt(this.model.macroRuns, idx, updated)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime: event.serverTime,
      clockSkewMs: this.computeClockSkewMs(event.serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      macroRuns,
    })
  }

  private applyInitialEvents(resp: SchemaEventsResponse): void {
    // D1 fix: this fires on EVERY re-snapshot (initial connect, and
    // every reconnect/stream.reset), not just the first one. Replacing
    // `model.events` wholesale with just this fetched page would discard
    // any event this store already applied live via `event.recorded`
    // frames since the last snapshot — those events are real and were
    // already shown to the operator; a later re-snapshot's window not
    // happening to cover them again is not evidence they didn't happen.
    // Reconcile instead: union the freshly fetched page with whatever is
    // already held, deduplicating by the durable `Event.seq`, so a
    // reconnect can only ever ADD to what the operator has already seen
    // (bounded by MAX_RETAINED_EVENTS), never silently drop it.
    const fetched = resp.events.map(toModelEvent)
    const events = mergeEventsBySeq(this.model.events, fetched)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime: resp.serverTime,
      clockSkewMs: this.computeClockSkewMs(resp.serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      events,
      // Sticky true: gap means history was permanently lost to
      // retention (api/openapi.yaml's top-level description), so a
      // later fetch reporting gap:false — whether because retention
      // moved on or because this fetch's `since` window no longer
      // reaches the gap — cannot mean the lost events came back; they
      // cannot, by construction. Never clear a gap once seen.
      eventsGap: this.model.eventsGap || resp.gap,
      oldestRetainedSeq: resp.oldestRetainedSeq,
    })
  }

  private applyNodeChanged(node: SchemaNode, serverTime: string): void {
    const idx = this.model.nodes.findIndex((n) => n.nodeId === node.nodeId)
    const nodes =
      idx === -1
        ? [...this.model.nodes, node].sort(compareByNodeId)
        : replaceAt(this.model.nodes, idx, node)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      nodes,
    })
  }

  /**
   * `fpp.changed` carries an FPP instance's COMPLETE current
   * representation (ADR-023 decision 3a) and is therefore always a
   * REPLACEMENT of whatever this store held for that instanceId — never a
   * merge. This is the same posture this method has always had, since
   * before deltas existed; it stays a plain whole-object replace so that a
   * connection that never asked for deltas (and, per decision 3, a
   * delta-subscribed connection's OWN structural-change frames) observes
   * no difference from before ADR-023.
   */
  private applyFppChanged(instance: SchemaFPPInstance, serverTime: string): void {
    const idx = this.model.fpp.findIndex((i) => i.instanceId === instance.instanceId)
    // FPPInstance ordering is "exactly as configured" (api/openapi.yaml
    // FPPResponse.instances), which a delta cannot know for an instance
    // it has never seen before — appending is a best-effort fallback for
    // that case, not a claim about configuration order.
    const fpp: FPPInstance[] =
      idx === -1 ? [...this.model.fpp, instance] : replaceAt(this.model.fpp, idx, instance)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      fpp,
    })
  }

  /**
   * `resolume.changed` carries a Resolume instance's COMPLETE current
   * representation, matching [applyFppChanged]'s exact same
   * whole-object-replace posture and for the identical reason (no
   * `resolume.observations.changed` delta variant exists — build contract
   * §4).
   */
  private applyResolumeChanged(instance: SchemaResolumeInstance, serverTime: string): void {
    const idx = this.model.resolume.findIndex((i) => i.instanceId === instance.instanceId)
    const resolume: ResolumeInstance[] =
      idx === -1 ? [...this.model.resolume, instance] : replaceAt(this.model.resolume, idx, instance)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      resolume,
    })
  }

  /**
   * `nightSession.changed` (Track F seam F2) carries the night session's
   * COMPLETE current representation, matching [applyResolumeChanged]'s
   * exact same whole-object-replace posture and for the identical reason
   * (no delta kind exists for this resource). Unlike `resolume`, there is
   * only ever one current night session, so this is a plain assignment
   * rather than an array upsert.
   */
  private applyNightSessionChanged(event: SchemaNightSessionChangedEvent): void {
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime: event.serverTime,
      clockSkewMs: this.computeClockSkewMs(event.serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      nightSession: event.session,
    })
  }

  /**
   * `fppPlaylistEntry.changed` carries one instance's
   * COMPLETE latest observation (api/openapi.yaml's
   * FPPPlaylistEntryChangedEvent: "full-frame only - no ADR-023 delta
   * narrowing exists for this resource"), matching [applyResolumeChanged]'s
   * exact same whole-object-replace-by-key posture. Unlike `resolume`,
   * this array is not seeded from `Snapshot` at all (see
   * `Model.fppPlaylistEntryObservations`'s own comment): a connection
   * that has never heard a live frame for a given `instanceUuid` simply
   * has no entry here yet, which is correct: there is nothing to render
   * eagerly, and `views/PlaylistReadiness.tsx`'s own fetch of the
   * reconciliation endpoint is what establishes ground truth on mount.
   */
  private applyFppPlaylistEntryChanged(observation: SchemaFPPPlaylistEntryObservation, serverTime: string): void {
    const idx = this.model.fppPlaylistEntryObservations.findIndex(
      (o) => o.instanceUuid === observation.instanceUuid,
    )
    const fppPlaylistEntryObservations: FPPPlaylistEntryObservation[] =
      idx === -1
        ? [...this.model.fppPlaylistEntryObservations, observation]
        : replaceAt(this.model.fppPlaylistEntryObservations, idx, observation)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      fppPlaylistEntryObservations,
    })
  }

  /**
   * `fpp.observations.changed` carries only what moved (ADR-023 decision
   * 3a) and is therefore always a MERGE onto the instance's existing
   * `observations` — `changed` entries are folded in by `Evidence.signal`
   * (updating an existing signal in place or appending a genuinely new
   * one) and every signal named in `removed` is deleted outright. This is
   * the opposite operation from `applyFppChanged` immediately above, on
   * purpose: see `mergeFppObservations`'s own doc comment for how the two
   * are kept from being confused at a call site.
   *
   * If this connection has no existing instance for `instanceId` at all,
   * the frame is dropped rather than synthesizing a partial instance from
   * a delta alone: ADR-023 decision 4 says a delta applies to a baseline
   * this client already has, and the snapshot-before-delta ordering this
   * store enforces (see this file's header comment, step 2) is exactly
   * what is supposed to guarantee that baseline exists before any delta
   * for it can arrive. An unseen instanceId here would mean that
   * guarantee was violated somewhere upstream, and the correct response to
   * an unmergeable delta is to do nothing and wait for the next
   * authoritative snapshot or fpp.changed to supply the instance in full —
   * not to guess at a shape the wire contract never sent.
   */
  private applyFppObservationsChanged(
    instanceId: string,
    changed: readonly Evidence[],
    removed: readonly string[],
    serverTime: string,
  ): void {
    const idx = this.model.fpp.findIndex((i) => i.instanceId === instanceId)
    if (idx === -1) return

    const instance = this.model.fpp[idx]
    // Unreachable: idx came from findIndex over this exact array two
    // lines above, so the -1 branch already returned. noUncheckedIndexedAccess
    // still types this access as possibly undefined; this guard satisfies
    // the type checker rather than papering over a real gap.
    if (instance === undefined) return
    const nextInstance: FPPInstance = {
      ...instance,
      observations: mergeFppObservations(instance.observations, changed, removed),
    }
    const fpp = replaceAt(this.model.fpp, idx, nextInstance)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      fpp,
    })
  }

  private applyEventRecorded(event: SchemaEvent, serverTime: string): void {
    const branded = toModelEvent(event)
    const alreadyPresent = this.model.events.some((e) => e.seq === branded.seq)
    const events = alreadyPresent
      ? this.model.events
      : [branded, ...this.model.events].slice(0, MAX_RETAINED_EVENTS)
    const receivedAt = this.now()
    this.setModel({
      ...this.model,
      serverTime,
      clockSkewMs: this.computeClockSkewMs(serverTime, receivedAt),
      serverTimeReceivedAt: receivedAt,
      events,
    })
  }
}

export function createApiStore(options?: ApiStoreOptions): ApiStore {
  return new ApiStore(options)
}

// -- free functions ---------------------------------------------------------

function compareByNodeId(a: { nodeId: string }, b: { nodeId: string }): number {
  return a.nodeId.localeCompare(b.nodeId)
}

function replaceAt<T>(list: readonly T[], index: number, value: T): T[] {
  const next = list.slice()
  next[index] = value
  return next
}

function toModelEvent(event: SchemaEvent): ModelEvent {
  return { ...event, seq: asEventSeq(event.seq) }
}

/**
 * A full [SchemaMacroRun] minus its `steps` — exactly [MacroRunSummary]'s
 * own shape (`MacroRunSummary`'s doc comment: "a run's own state without
 * its steps"). Used only by [ApiStore.submitMacroRun] to fold its `202`
 * response's full run object into `model.macroRuns`, which holds
 * summaries, never full runs with steps — steps stay fetch-only per
 * section 6.6.
 */
function toMacroRunSummary(run: {
  id: string
  macroObjectId: string
  macroRevision: number
  show: string
  trigger: 'api' | 'plugin' | 'cli' | 'ui'
  issuerPrincipalId: string
  issuerPrincipalName: string
  createdAt: string
  finishedAt: string | null
  state: 'running' | 'finished'
  completed: boolean | null
  confirmed: boolean | null
  reason: string
  attributionDegraded: boolean
}): {
  id: string
  macroObjectId: string
  macroRevision: number
  show: string
  trigger: 'api' | 'plugin' | 'cli' | 'ui'
  issuerPrincipalId: string
  issuerPrincipalName: string
  createdAt: string
  finishedAt: string | null
  state: 'running' | 'finished'
  completed: boolean | null
  confirmed: boolean | null
  reason: string
  attributionDegraded: boolean
} {
  const {
    id,
    macroObjectId,
    macroRevision,
    show,
    trigger,
    issuerPrincipalId,
    issuerPrincipalName,
    createdAt,
    finishedAt,
    state,
    completed,
    confirmed,
    reason,
    attributionDegraded,
  } = run
  return {
    id,
    macroObjectId,
    macroRevision,
    show,
    trigger,
    issuerPrincipalId,
    issuerPrincipalName,
    createdAt,
    finishedAt,
    state,
    completed,
    confirmed,
    reason,
    attributionDegraded,
  }
}

/**
 * Unions two event lists by the durable `Event.seq` (a fetched page
 * winning over an in-memory entry with the same seq, since the server is
 * always the authoritative copy), sorts newest-first by seq — `seq` is a
 * strictly increasing durable cursor (domain.ts), so numeric descending
 * order on it IS recency order, with no timestamp collation involved —
 * and bounds the result to MAX_RETAINED_EVENTS. Used by
 * applyInitialEvents (D1) to reconcile a re-snapshot's fetched window
 * with whatever this store already holds, instead of replacing it.
 */
function mergeEventsBySeq(existing: readonly ModelEvent[], incoming: readonly ModelEvent[]): ModelEvent[] {
  const bySeq = new Map<EventSeq, ModelEvent>()
  for (const event of existing) bySeq.set(event.seq, event)
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((a, b) => b.seq - a.seq).slice(0, MAX_RETAINED_EVENTS)
}

/**
 * Applies ADR-023's delta merge semantics: `changed` is folded onto
 * `existing` by `Evidence.signal` (an existing signal is updated in
 * place, preserving `existing`'s relative order; a signal `existing`
 * never had is appended, in `changed`'s own order — already sorted
 * server-side, see internal/coordinator/api's sortObservations), and every
 * signal named in `removed` is deleted from the result outright.
 *
 * This function's signature is the actual mechanism that keeps this merge
 * from ever being confused with `applyFppChanged`'s replacement at a call
 * site: it takes an observations ARRAY plus a small delta, never a whole
 * `FPPInstance` to replace one with, so there is no value of this
 * function's parameters that could accidentally perform a replace, and no
 * caller can pass this function's result where a full instance is
 * expected — the return type is `Evidence[]`, not `FPPInstance`.
 */
function mergeFppObservations(
  existing: readonly Evidence[],
  changed: readonly Evidence[],
  removed: readonly string[],
): Evidence[] {
  const removedSignals = new Set(removed)
  const bySignal = new Map<string, Evidence>()
  for (const o of existing) {
    if (!removedSignals.has(o.signal)) bySignal.set(o.signal, o)
  }
  for (const o of changed) {
    bySignal.set(o.signal, o)
  }

  // Preserve `existing`'s own relative order for every signal that
  // survives (untouched or updated in place), then append any genuinely
  // new signal `changed` introduced, in the order `changed` listed it.
  const order: string[] = []
  const seen = new Set<string>()
  for (const o of existing) {
    if (bySignal.has(o.signal) && !seen.has(o.signal)) {
      order.push(o.signal)
      seen.add(o.signal)
    }
  }
  for (const o of changed) {
    if (!seen.has(o.signal)) {
      order.push(o.signal)
      seen.add(o.signal)
    }
  }

  return order.map((signal) => {
    const evidence = bySignal.get(signal)
    if (evidence === undefined) {
      // Unreachable: `order` is built exclusively from signals already
      // confirmed present in `bySignal` above (either by the `bySignal.has`
      // guard in the first loop, or by construction in the second, since
      // every `changed` entry is unconditionally written into `bySignal`
      // before this point). A non-null assertion would silence the same
      // guarantee less legibly; this throws instead, matching this
      // package's existing posture (see stream.go's mustObservation and
      // its "internal invariant violation" panics) of preferring a loud
      // failure over trusting an assertion nothing here re-checks.
      throw new Error(`mergeFppObservations: internal invariant violated for signal "${signal}"`)
    }
    return evidence
  })
}

function tryParse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T
  } catch {
    return null
  }
}
