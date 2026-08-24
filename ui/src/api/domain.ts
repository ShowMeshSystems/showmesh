/**
 * The client-facing domain model: the shape `useModel()` (useModel.ts)
 * exposes to the rest of the UI, and the types the store (store.ts)
 * maintains internally.
 *
 * Resource shapes (Node, FPPInstance, CollectorStatus, Event) are aliased
 * directly from the generated wire schema (generated/schema.d.ts) rather
 * than hand-copied, per ADR-015's "generated from or verified against the
 * Go types" requirement — a hand-maintained second copy would drift.
 */
import type { components } from './generated/schema'

// ---------------------------------------------------------------------
// Branded sequence numbers.
//
// api/openapi.yaml is explicit that these are two different things that
// happen to both be called "seq": Event.seq (this file's EventSeq) is a
// durable, strictly increasing cursor into GET /events history. The `seq`
// on an SSE stream frame (StreamSeq) is per-connection, starts at 1 on
// every reconnect, and ADR-020 decision 3 forbids ever treating it as a
// cursor. They are structurally both `number` on the wire, so nothing
// stops a client from accidentally comparing or persisting the wrong one
// — except the type system, if the two are distinct types. That's what
// this branding buys: an EventSeq and a StreamSeq are not mutually
// assignable, even though both erase to `number` at runtime.
// ---------------------------------------------------------------------
declare const eventSeqBrand: unique symbol
export type EventSeq = number & { readonly [eventSeqBrand]: true }

declare const streamSeqBrand: unique symbol
export type StreamSeq = number & { readonly [streamSeqBrand]: true }

export function asEventSeq(value: number): EventSeq {
  return value as EventSeq
}

export function asStreamSeq(value: number): StreamSeq {
  return value as StreamSeq
}

// ---------------------------------------------------------------------
// Resource shapes, aliased from the generated schema.
// ---------------------------------------------------------------------
export type Node = components['schemas']['Node']
export type FPPInstance = components['schemas']['FPPInstance']
export type CollectorStatus = components['schemas']['CollectorStatus']
export type Capability = components['schemas']['Capability']
export type ControlPlane = components['schemas']['ControlPlane']
export type NodeEvidence = components['schemas']['NodeEvidence']
export type EvidenceState = components['schemas']['Evidence']['state']
export type Evidence = components['schemas']['Evidence']
export type ResourceRef = components['schemas']['ResourceRef']

// ADR-024: the session/identity shapes, aliased from the generated schema
// for the same reason as every type above (ADR-015: generated from or
// verified against the Go types, never hand-copied a second time).
export type SessionResponse = components['schemas']['SessionResponse']
export type PrincipalSummary = components['schemas']['PrincipalSummary']
export type SessionInfo = components['schemas']['SessionInfo']

// Track G seam G-5: identity administration over the API. PrincipalObject
// is the admin surface's own full object, distinct from PrincipalSummary
// above (SessionResponse's narrower "who am I" shape).
export type PrincipalObject = components['schemas']['PrincipalObject']
export type PrincipalsResponse = components['schemas']['PrincipalsResponse']
export type PrincipalResponse = components['schemas']['PrincipalResponse']
export type CreatePrincipalRequest = components['schemas']['CreatePrincipalRequest']
export type SetPrincipalRoleRequest = components['schemas']['SetPrincipalRoleRequest']
export type SetPrincipalPasswordRequest = components['schemas']['SetPrincipalPasswordRequest']
export type TokenObject = components['schemas']['TokenObject']
export type TokensResponse = components['schemas']['TokensResponse']
export type IssueTokenRequest = components['schemas']['IssueTokenRequest']
export type IssueTokenResponse = components['schemas']['IssueTokenResponse']

// Step 7 seam A (RES-008 D1): the configuration write surface's shapes,
// aliased for the identical reason as every type above. Not part of
// `Model` — see store.ts's "Step 7 seam A" section header comment for why
// this data lives outside the SSE snapshot/delta stream.
export type FPPEndpointsConfigResponse = components['schemas']['FPPEndpointsConfigResponse']
export type ConfigFPPEndpoint = components['schemas']['ConfigFPPEndpoint']
export type ConfigFPPEndpointsPayload = components['schemas']['ConfigFPPEndpointsPayload']
export type ConfigRevisionMeta = components['schemas']['ConfigRevisionMeta']
export type ConfigRevisionsResponse = components['schemas']['ConfigRevisionsResponse']
// Track G seam G-2 (ADR-039): the resolume.instances configuration write
// surface, aliased for the identical reason as fpp.endpoints' own shapes
// above — mirrors that kind exactly, including reusing ConfigRevisionMeta/
// ConfigRevisionsResponse for its own revision history.
export type ResolumeInstancesConfigResponse = components['schemas']['ResolumeInstancesConfigResponse']
export type ConfigResolumeInstance = components['schemas']['ConfigResolumeInstance']
export type ConfigResolumeInstancesPayload = components['schemas']['ConfigResolumeInstancesPayload']
// Track G seam G-3 (ADR-039): the fpp.mqtt configuration write surface.
// Unlike fpp.endpoints/resolume.instances, the PUT request shape
// (ConfigFPPMQTTPutRequest) is NOT the same as the response payload shape
// (ConfigFPPMQTTPayload, reached via FPPMQTTConfigResponse['payload']):
// every PUT field is independently optional (ADR-039 decision 5) and the
// response never carries the password itself (decision 7), only
// `passwordSet`.
export type FPPMQTTConfigResponse = components['schemas']['FPPMQTTConfigResponse']
export type ConfigFPPMQTTPayload = components['schemas']['ConfigFPPMQTTPayload']
export type ConfigFPPMQTTPutRequest = components['schemas']['ConfigFPPMQTTPutRequest']
// Track G seam G-4 (ADR-039): the assets.settings configuration write
// surface, aliased for the identical reason as resolume.instances' own
// shapes above. ConfigAssetsSettingsPutPayload is a SEPARATE type from
// ConfigAssetsSettingsPayload (unlike every other config kind's PUT/GET
// pair, which share one payload shape): every field of a PUT request is
// independently optional, while a GET/PUT response always carries all
// four.
export type AssetsSettingsConfigResponse = components['schemas']['AssetsSettingsConfigResponse']
export type ConfigAssetsSettingsPayload = components['schemas']['ConfigAssetsSettingsPayload']
export type ConfigAssetsSettingsPutPayload = components['schemas']['ConfigAssetsSettingsPutPayload']
// Step 7 seam C: the first write's own response shape, aliased for the
// identical reason as every type above.
export type FPPCommandResult = components['schemas']['FPPCommandResult']
// Track B seam B2b-front: the three render.* dispatch endpoints' own
// response shape, aliased for the identical reason.
export type RenderCommandResult = components['schemas']['RenderCommandResult']
// ObservationEntry is Node['render']'s element type, aliased here
// separately so NodeDetail.tsx and the dashboard attention list can
// import it directly rather than indexing through Node.
export type ObservationEntry = components['schemas']['ObservationEntry']
// BUILD-PLAN Step 7 seam B (RES-008 D2/D6): node discovery and
// declaration. NodeDeclaration is also reachable as Node['declaration'],
// aliased here separately for call sites (DomainBadges.tsx,
// NodesList.tsx) that only ever need the declaration block on its own.
export type NodeDeclaration = components['schemas']['NodeDeclaration']
export type DiscoveryRun = components['schemas']['DiscoveryRun']
export type DiscoveryProposal = components['schemas']['DiscoveryProposal']

// Step 9 (STEP-9-SPEC.md sections 5, 6): show.action / show.macro
// configuration objects and the macro run surface. Aliased for the
// identical reason as every type above (ADR-015).
export type ConfigObjectSummary = components['schemas']['ConfigObjectSummary']
export type ConfigObjectsListResponse = components['schemas']['ConfigObjectsListResponse']
export type ConfigShowActionMQTTPublish = components['schemas']['ConfigShowActionMQTTPublish']
export type ConfigShowActionMQTTExpect = components['schemas']['ConfigShowActionMQTTExpect']
export type ConfigShowActionTarget = components['schemas']['ConfigShowActionTarget']
export type ConfigShowAction = components['schemas']['ConfigShowAction']
export type ShowActionConfigResponse = components['schemas']['ShowActionConfigResponse']
export type ConfigShowMacroLocalFallback = components['schemas']['ConfigShowMacroLocalFallback']
export type ConfigShowMacroStep = components['schemas']['ConfigShowMacroStep']
export type ConfigShowMacro = components['schemas']['ConfigShowMacro']
export type ShowMacroConfigResponse = components['schemas']['ShowMacroConfigResponse']
export type ConfigRevisionsResponseKind = ConfigRevisionsResponse['kind']
// Finding 16 (Track B surface fixes): show.surface reads, so the UI can
// discover a configured-but-not-yet-applied surface the same way
// showmeshctl already can. Aliased for the identical reason as every
// type above.
export type ConfigShowSurface = components['schemas']['ConfigShowSurface']
export type ShowSurfaceConfigResponse = components['schemas']['ShowSurfaceConfigResponse']

export type MacroRunSummary = components['schemas']['MacroRunSummary']
export type MacroRunStepCommand = components['schemas']['MacroRunStepCommand']
export type MacroRunStep = components['schemas']['MacroRunStep']
export type MacroRun = components['schemas']['MacroRun']
export type MacroRunResponse = components['schemas']['MacroRunResponse']
export type MacroRunSubmitResponse = components['schemas']['MacroRunSubmitResponse']
export type MacroRunsListResponse = components['schemas']['MacroRunsListResponse']
export type MacroPriorFailureRequest = components['schemas']['MacroPriorFailureRequest']
export type CreateMacroRunRequest = components['schemas']['CreateMacroRunRequest']
export type MacroRunChangedEvent = components['schemas']['MacroRunChangedEvent']
// Track D seam D-2a (ADR-032): the Resolume composition upload surface's
// shapes, aliased for the identical reason as every type above. Not part
// of `Model` — this data is not a resource ADR-020's change stream
// models, matching FPPEndpointsConfigResponse's own reasoning just above.
export type ResolumeCompositionSummary = components['schemas']['ResolumeCompositionSummary']
export type ResolumeCompositionResponse = components['schemas']['ResolumeCompositionResponse']
export type ResolumeCompositionUploadResponse = components['schemas']['ResolumeCompositionUploadResponse']

// Track D seam D-3a: Arena crash recovery. Aliased for the identical
// reason as every type above. Not part of `Model` — this data is not a
// resource ADR-020's change stream models (build contract §1.7).
export type ResolumeRecoveryRecordEntry = components['schemas']['ResolumeRecoveryRecordEntry']
export type ResolumeRecoveryRestoreLayer = components['schemas']['ResolumeRecoveryRestoreLayer']
export type ResolumeRecoveryRestoreReport = components['schemas']['ResolumeRecoveryRestoreReport']
export type ResolumeRecoveryResponse = components['schemas']['ResolumeRecoveryResponse']
export type ResolumeRecoveryRestoreResponse = components['schemas']['ResolumeRecoveryRestoreResponse']
export type ConfigResolumeRecoveryPayload = components['schemas']['ConfigResolumeRecoveryPayload']
export type ResolumeRecoveryConfigResponse = components['schemas']['ResolumeRecoveryConfigResponse']

// Track B seam B2c (ADR-039): the render.settings configuration singleton.
// Aliased for the identical reason as every type above. Not part of
// `Model` — this data is not a resource ADR-020's change stream models,
// matching ResolumeRecoveryConfigResponse's own reasoning just above.
export type ConfigRenderRestartPolicy = components['schemas']['ConfigRenderRestartPolicy']
export type ConfigRenderSettingsPayload = components['schemas']['ConfigRenderSettingsPayload']
export type RenderSettingsConfigResponse = components['schemas']['RenderSettingsConfigResponse']

// ADR-033: the installation-wide operating mode. Aliased for the same
// ADR-015 reason as every type above, and NOT part of `Model`: it is
// configuration, not a resource the change stream models.
export type ConfigShowModePayload = components['schemas']['ConfigShowModePayload']
export type ShowModeConfigResponse = components['schemas']['ShowModeConfigResponse']

// Track D seam D-4: Resolume as an observability resource (seam E) and the
// seven-action vocabulary (D-3/seam B). Aliased for the identical reason
// as every type above (ADR-015). `ResolumeInstance` IS part of `Model`
// (below) — unlike the composition/recovery types above, it is a resource
// ADR-020's change stream models (`resolume.changed`).
export type ResolumeInstanceComposition = components['schemas']['ResolumeInstanceComposition']
export type ResolumeInstance = components['schemas']['ResolumeInstance']
export type ResolumeInstancesResponse = components['schemas']['ResolumeInstancesResponse']
export type ResolumeInstanceResponse = components['schemas']['ResolumeInstanceResponse']
export type ResolumeActionParam = components['schemas']['ResolumeActionParam']
export type ResolumeAction = components['schemas']['ResolumeAction']
export type ResolumeActionsResponse = components['schemas']['ResolumeActionsResponse']
export type ResolumeActionResult = components['schemas']['ResolumeActionResult']
export type ResolumeActionResponse = components['schemas']['ResolumeActionResponse']
// The pre-show binding check and one action invocation, outside of any
// macro run (ADR-029).
export type ActionBinding = components['schemas']['ActionBinding']
export type ActionInvocationResult = components['schemas']['ActionInvocationResult']
// The full stored composition id map (decks, layer groups, layers,
// columns, clips, persistent clips) — distinct from
// ResolumeCompositionSummary above, which is the display-only subset.
export type ResolumeCompositionDeckSummary = components['schemas']['ResolumeCompositionDeckSummary']
export type ResolumeCompositionLayerGroup = components['schemas']['ResolumeCompositionLayerGroup']
export type ResolumeCompositionLayer = components['schemas']['ResolumeCompositionLayer']
export type ResolumeCompositionColumn = components['schemas']['ResolumeCompositionColumn']
export type ResolumeCompositionClip = components['schemas']['ResolumeCompositionClip']

// Track G seam G-8: the Operator UI for Track E (ADR-027, ADR-026,
// ADR-028). Aliased for the identical reason as every type above
// (ADR-015). None of these is part of `Model` — this data is not a
// resource ADR-020's change stream models, matching FPPEndpointsConfigResponse's
// own reasoning above.
export type ConfigShow = components['schemas']['ConfigShow']
export type ConfigShowWrite = components['schemas']['ConfigShowWrite']
export type ShowConfigResponse = components['schemas']['ShowConfigResponse']
export type ConfigShowSurfaceChannelRange = components['schemas']['ConfigShowSurfaceChannelRange']
export type ConfigShowSurfaceGeometry = components['schemas']['ConfigShowSurfaceGeometry']
export type ConfigShowSurfaceNDIOutput = components['schemas']['ConfigShowSurfaceNDIOutput']
export type ConfigShowSurfaceHDMI = components['schemas']['ConfigShowSurfaceHDMI']
export type ConfigShowSurfaceOutput = components['schemas']['ConfigShowSurfaceOutput']
export type ConfigShowActive = components['schemas']['ConfigShowActive']
export type ShowActiveConfigResponse = components['schemas']['ShowActiveConfigResponse']
export type Asset = components['schemas']['Asset']
export type AssetResponse = components['schemas']['AssetResponse']
export type AssetsListResponse = components['schemas']['AssetsListResponse']
export type NodeAssetManifest = components['schemas']['NodeAssetManifest']
export type MissingAsset = components['schemas']['MissingAsset']
export type AssetGap = components['schemas']['AssetGap']
export type ExtraAsset = components['schemas']['ExtraAsset']
export type NodeAssetManifestResponse = components['schemas']['NodeAssetManifestResponse']
export type AssetManifestResponse = components['schemas']['AssetManifestResponse']
export type AuditEntry = components['schemas']['AuditEntry']
export type AuditResponse = components['schemas']['AuditResponse']

// Track F seam F2/F1 (RESTING-MODE.md, ADR-038, ADR-039): the night-session
// lifecycle controller and the night.session/night.session.active
// configuration kinds. Aliased for the identical reason as every type
// above (ADR-015).
export type ConfigNightSessionFPPPlaylist = components['schemas']['ConfigNightSessionFPPPlaylist']
export type ConfigNightSessionAssetRef = components['schemas']['ConfigNightSessionAssetRef']
export type ConfigNightSessionBackgroundAudioItem = components['schemas']['ConfigNightSessionBackgroundAudioItem']
export type ConfigNightSessionBackgroundAudio = components['schemas']['ConfigNightSessionBackgroundAudio']
export type ConfigNightSessionResting = components['schemas']['ConfigNightSessionResting']
export type ConfigNightSessionCue = components['schemas']['ConfigNightSessionCue']
export type ConfigNightSessionEnterShow = components['schemas']['ConfigNightSessionEnterShow']
export type ConfigNightSessionEnterResting = components['schemas']['ConfigNightSessionEnterResting']
export type ConfigNightSessionCueWrite = components['schemas']['ConfigNightSessionCueWrite']
export type ConfigNightSessionEnterShowWrite = components['schemas']['ConfigNightSessionEnterShowWrite']
export type ConfigNightSessionEnterRestingWrite = components['schemas']['ConfigNightSessionEnterRestingWrite']
export type ConfigNightSessionBackgroundAudioWrite = components['schemas']['ConfigNightSessionBackgroundAudioWrite']
export type ConfigNightSessionRestingWrite = components['schemas']['ConfigNightSessionRestingWrite']
export type ConfigNightSessionWrite = components['schemas']['ConfigNightSessionWrite']
export type ConfigNightSession = components['schemas']['ConfigNightSession']
export type NightSessionConfigResponse = components['schemas']['NightSessionConfigResponse']
export type ConfigNightSessionActive = components['schemas']['ConfigNightSessionActive']
export type NightSessionActiveConfigResponse = components['schemas']['NightSessionActiveConfigResponse']
export type NightReadinessCheck = components['schemas']['NightReadinessCheck']
export type NightReadiness = components['schemas']['NightReadiness']
export type NightPhaseEvidence = components['schemas']['NightPhaseEvidence']
export type NightCue = components['schemas']['NightCue']
export type NightCues = components['schemas']['NightCues']
export type NightAuthorization = components['schemas']['NightAuthorization']
export type NightSessionState = components['schemas']['NightSessionState']
export type NightSessionResponse = components['schemas']['NightSessionResponse']
export type NightCommandRequest = components['schemas']['NightCommandRequest']
export type NightCommandResult = components['schemas']['NightCommandResult']
export type NightCommandResponse = components['schemas']['NightCommandResponse']
export type NightSessionChangedEvent = components['schemas']['NightSessionChangedEvent']
export type NightCommandName =
  | 'prepare-site'
  | 'run-readiness'
  | 'start-preshow'
  | 'start-night'
  | 'request-final-show'
  | 'fade-out-night'
  | 'power-down-presentation'
  | 'end-session'

/**
 * One recorded event, as held in the model. Identical to the wire
 * `Event` schema except `seq` is branded EventSeq rather than a bare
 * `number` — see the brand comment above.
 */
export interface Event extends Omit<components['schemas']['Event'], 'seq'> {
  seq: EventSeq
}

// ---------------------------------------------------------------------
// Connection state machine (spec section 5.4).
// ---------------------------------------------------------------------
export type ConnectionState =
  | { kind: 'connecting' }
  | { kind: 'live'; connectedAt: number }
  | {
      kind: 'reconnecting'
      attempt: number
      nextAttemptAt: number
      lastError: string
    }
  // Deviation from the spec section 5.4 code block, recorded here rather
  // than silently: that block shows `{ kind: 'unauthorized' }` with no
  // further fields, but section 5.6 requires distinguishing "no token
  // supplied yet" from "the supplied token was rejected" ("a wrong
  // secret does not present as a missing one"). A bare `unauthorized`
  // cannot express that distinction, so this adds `reason`. Reported to
  // the orchestrator per spec section 9.
  | { kind: 'unauthorized'; reason: 'missing' | 'rejected' }
  | {
      kind: 'incompatible'
      requiredVersion: number
      supportedVersions: number[]
      detail: string
    }
  | { kind: 'failed'; detail: string }

// ---------------------------------------------------------------------
// The model (spec section 5.5).
// ---------------------------------------------------------------------
export interface Model {
  connection: ConnectionState
  serverTime: string | null
  clockSkewMs: number | null
  /** Browser clock, for "last updated" — see spec section 5.5. */
  snapshotReceivedAt: number | null
  /**
   * Browser clock (`Date.now()`) at the moment the current `serverTime`
   * value was captured — paired with `serverTime` the same way
   * `snapshotReceivedAt` pairs with "last updated" above. Every code path
   * that sets `serverTime` (store.ts's applySnapshot, applyInitialEvents,
   * applyNodeChanged, applyFppChanged, applyEventRecorded) sets this
   * alongside it. This is what lets a view derive an effective "now" that
   * keeps advancing between responses — most importantly while
   * disconnected, when nothing updates `serverTime` itself — without ever
   * computing an age against the raw browser clock (see app/time.ts's
   * effectiveServerTimeIso and this file's header comment on why
   * `serverTime`, not the browser clock, is the reference). Null exactly
   * when `serverTime` is: before the first response.
   */
  serverTimeReceivedAt: number | null
  /** Ordered by nodeId, as the API guarantees. */
  nodes: Node[]
  fpp: FPPInstance[]
  collectors: CollectorStatus[]
  /**
   * Step 9 (STEP-9-SPEC.md section 6.6): every in-flight macro run, plus a
   * bounded window of recently finished ones, exactly as `Snapshot.macroRuns`
   * carries them — this is why a client reconnecting mid-run still sees it
   * (ADR-020 decision 3's "in-flight runs must appear in the snapshot").
   * Replaced wholesale on every snapshot (like `nodes`/`fpp`), and
   * upserted in place by `macroRun.changed` frames for a run ALREADY
   * present here — see store.ts's applyMacroRunChanged for why an
   * unrecognized runId is dropped rather than synthesized from a partial
   * event, mirroring applyFppObservationsChanged's identical posture.
   */
  macroRuns: MacroRunSummary[]
  /**
   * Track D seam D-4: every configured Resolume instance, exactly as
   * `Snapshot.resolume` carries them — an empty array on a coordinator
   * with none configured, never null. Replaced wholesale on every
   * snapshot and upserted in place by `resolume.changed` frames (see
   * store.ts's applyResolumeChanged). No delta variant exists, so unlike
   * `fpp`, every observation rides every frame.
   */
  resolume: ResolumeInstance[]
  /**
   * Track F seam F2: the night-session lifecycle controller's current
   * state, kept live by `nightSession.changed` frames (store.ts's
   * applyNightSessionChanged) — a whole-object replace, matching
   * `resolume.changed`'s posture (no delta kind exists for this resource
   * either). Unlike `resolume` above, this is NOT part of `Snapshot`
   * (api/openapi.yaml's own Snapshot schema has no `nightSession` field):
   * the stream only announces a CHANGE, so this stays `null` until either
   * the first live frame arrives or a view's own `GET /night/session`
   * call seeds it — see views/NightSession.tsx for that reconciliation.
   * Cleared back to `null` by every `applySnapshot` (store.ts) — the
   * initial connect, every reconnect, and every `stream.reset` — because
   * none of those is a guarantee this connection will hear about the
   * session again soon: the coordinator's stream hub only emits a frame
   * on an actual state change, so a stale value from a PRIOR connection
   * generation must not keep rendering as current across one it was
   * never confirmed against.
   */
  nightSession: NightSessionState | null
  /** Newest first, bounded — see MAX_RETAINED_EVENTS in store.ts. */
  events: Event[]
  /**
   * `true` once any fetched page of event history reported `gap: true`
   * (history permanently lost to retention). Never cleared by a retry:
   * the events that made it true no longer exist anywhere in this
   * system (api/openapi.yaml's top-level description), so retrying
   * cannot un-set it — see store.ts's applyInitialEvents.
   */
  eventsGap: boolean
  oldestRetainedSeq: number | null

  /**
   * The last `GET/POST /api/v1/session` response this store received, or
   * `null` before the first one arrives (spec section 5.5's "before the
   * first response" pattern, reused here). `null` must never be read as
   * "signed out": that is `session !== null && session.authenticated ===
   * false`, a distinct, positively-known state — see
   * app/session.ts's `describeSignInState`, the one place this
   * distinction is turned into what the persistent banner (ADR-024
   * decision 5's "signed out is a persistent state") actually shows.
   */
  session: SessionResponse | null
  /** Browser clock, paired with `session` the same way `snapshotReceivedAt` pairs with the model's data. */
  sessionReceivedAt: number | null
  /**
   * `true` when the MOST RECENT attempt to fetch `/session` failed (network
   * error, non-2xx, or a body that failed to parse) — `session` above is
   * then a stale, possibly-wrong last-known value rather than freshly
   * confirmed. ADR-024 decision 12: "a stale or unavailable [scope list]
   * renders as unknown, never as permissive" — this is the flag
   * `app/session.ts`'s scope-gate reads to force that degradation,
   * exactly as `eventsGap` above is a flag a view reads rather than the
   * store silently discarding the events it already has. Cleared back to
   * `false` by the next fetch that succeeds, unlike `eventsGap` (which is
   * sticky for a different, permanent reason — see that field's comment);
   * this one is a transient "can we currently vouch for this" bit, not a
   * record of data permanently lost.
   */
  sessionFetchFailed: boolean
}

export function initialModel(): Model {
  return {
    connection: { kind: 'connecting' },
    serverTime: null,
    clockSkewMs: null,
    snapshotReceivedAt: null,
    serverTimeReceivedAt: null,
    nodes: [],
    fpp: [],
    collectors: [],
    macroRuns: [],
    resolume: [],
    nightSession: null,
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session: null,
    sessionReceivedAt: null,
    sessionFetchFailed: false,
  }
}
