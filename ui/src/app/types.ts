// Domain types this seam (C, views) renders.
//
// UPDATE (mid-build): this file originally hand-mirrored api/openapi.yaml
// because seam B's `ui/src/api` did not exist yet when this seam started
// (see git history / this builder's report for why, and for the two
// reasons that motivated it at the time). Seam B has since landed. Now
// that `../api` is real, hand-mirroring its shapes would itself become
// the exact duplication this project warns against ("Type definitions
// for API payloads must be generated from the same source as the Go
// types or verified against them... Hand-maintaining a second copy of
// the state model will drift" — ADR-015). So this file now re-exports
// seam B's types directly rather than redefining them, and derives the
// small standalone enum aliases this seam's components use (e.g.
// `ControlPlaneState`) from seam B's field types by indexed access,
// rather than hand-copying their literal unions a second time.
//
// This file remains the single import site for domain types across
// src/app, src/views, and src/components, so call sites did not need to
// change when the underlying source did.
export type {
  Capability,
  CollectorStatus,
  ConnectionState,
  CurrentRunsResponse,
  CurrentShowContext,
  CurrentRun,
  CurrentPlayback,
  CurrentRunFreshness,
  CurrentReconciliation,
  CurrentRunActivation,
  CurrentRunTarget,
  CurrentRunNext,
  ControlPlane,
  DiscoveryProposal,
  DiscoveryRun,
  Evidence,
  EvidenceState,
  EventSeq,
  FPPCommandResult,
  FPPInstance,
  Model,
  Node,
  NodeDeclaration,
  NodeEvidence,
  PrincipalSummary,
  ResourceRef,
  SessionInfo,
  SessionResponse,
  // Track B seam B2b-front: the three render.* dispatch endpoints.
  ObservationEntry,
  RenderCommandResult,
  // The first audio-dispatch slice: pause/resume/stop/output.mute/output.unmute.
  AudioSessionCommandResult,
  // Track D seam D-4: Resolume as an observability resource and the
  // seven-action vocabulary.
  ResolumeInstanceComposition,
  ResolumeInstance,
  ResolumeActionParam,
  ResolumeAction,
  ResolumeActionResult,
  // The pre-show binding check and one action invocation, outside of any
  // macro run (ADR-029).
  ActionBinding,
  ActionInvocationResult,
  ResolumeCompositionResponse,
  ResolumeCompositionSummary,
  ResolumeCompositionDeckSummary,
  ResolumeCompositionLayerGroup,
  ResolumeCompositionLayer,
  ResolumeCompositionColumn,
  ResolumeCompositionClip,
  ResolumeRecoveryRecordEntry,
  ResolumeRecoveryRestoreLayer,
  ResolumeRecoveryRestoreReport,
  ResolumeRecoveryResponse,
  // Step 9 (STEP-9-SPEC.md sections 5, 6): show.action / show.macro
  // configuration objects and the macro run surface.
  ConfigObjectSummary,
  // Finding 16: show.surface reads, so the UI can discover a
  // configured-but-not-yet-applied surface the way showmeshctl already can.
  ConfigShowSurface,
  ShowSurfaceConfigResponse,
  // Track H seam H6: show.cue authoring.
  ConfigShowCueRenderOutput,
  ConfigShowCueAudioOutput,
  ConfigShowCueLTCOutput,
  ConfigShowCueAnnouncementOutput,
  ConfigShowCueOutputs,
  ConfigShowCue,
  ShowCueConfigResponse,
  // ADR-039/ADR-018: the audio.settings engine-wide singleton and
  // audio.node per-node object.
  ConfigAudioSettingsPayload,
  AudioSettingsConfigResponse,
  ConfigAudioNode,
  AudioNodeConfigResponse,

  ConfigShowPlaylistFPPBinding,
  ConfigShowPlaylistShowmeshAudio,
  ConfigShowPlaylistEntryFPP,
  ConfigShowPlaylistEntry,
  ConfigShowPlaylist,
  ShowPlaylistConfigResponse,
  ConfigShowActionMQTTPublish,
  ConfigShowActionMQTTExpect,
  ConfigShowActionTarget,
  ConfigShowAction,
  ShowActionConfigResponse,
  ConfigShowMacroLocalFallback,
  ConfigShowMacroStep,
  ConfigShowMacro,
  ShowMacroConfigResponse,
  MacroRunSummary,
  MacroRunStepCommand,
  MacroRunStep,
  MacroRun,
  // Track G seam G-8: the Operator UI for Track E (ADR-027, ADR-026,
  // ADR-028).
  ConfigShow,
  ConfigShowWrite,
  ShowConfigResponse,
  ConfigShowSurfaceChannelRange,
  ConfigShowSurfaceGeometry,
  ConfigShowSurfaceNDIOutput,
  ConfigShowSurfaceHDMI,
  ConfigShowSurfaceOutput,
  ConfigShowActive,
  ShowActiveConfigResponse,
  Asset,
  AssetResponse,
  NodeAssetManifest,
  MissingAsset,
  AssetGap,
  ExtraAsset,
  AuditEntry,
  // Track H seam H6: the resolved Cue catalog a node holds and the
  // deploy dispatch's own result shape.
  CueCatalogResponse,
  CueCatalogEntry,
  CueCatalogDeployResult,
  // Track F seam F2/F1: the night-session lifecycle controller and the
  // night.session/night.session.active configuration kinds.
  ConfigNightSession,
  ConfigNightSessionWrite,
  ConfigNightSessionResting,
  ConfigNightSessionRestingWrite,
  ConfigNightSessionEnterShow,
  ConfigNightSessionEnterShowWrite,
  ConfigNightSessionEnterResting,
  ConfigNightSessionEnterRestingWrite,
  ConfigNightSessionCue,
  ConfigNightSessionCueWrite,
  ConfigNightSessionFPPPlaylist,
  ConfigNightSessionAssetRef,
  ConfigNightSessionBackgroundAudio,
  ConfigNightSessionBackgroundAudioWrite,
  ConfigNightSessionBackgroundAudioItem,
  ConfigNightSessionActive,
  NightSessionConfigResponse,
  NightSessionActiveConfigResponse,
  NightSessionState,
  NightSessionResponse,
  NightReadiness,
  NightReadinessCheck,
  NightPhaseEvidence,
  NightCue,
  NightCues,
  NightBackgroundAudio,
  NightBackgroundAudioStep,
  NightAuthorization,
  NightCommandName,
  NightCommandResult,
  // TRACK-H-H2-SPEC.md §5/§6: the two read-only FPP playlist show-night
  // verdicts.
  FPPPlaylistReadinessResponse,
  FPPPlaylistEntryReconciliationResponse,
  // TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition import
  // evidence: the list of what has been reported, one full definition,
  // and its parsed entries.
  FPPPlaylistDefinitionMetadata,
  FPPPlaylistDefinitionsListResponse,
  FPPPlaylistDefinitionResponse,
  FPPPlaylistDefinitionEntry,
  FPPPlaylistDefinitionEntriesResponse,
} from '../api'
// Renamed on import, not re-declared: seam B's `Event` is identical to
// the wire schema's Event plus a branded `EventSeq` (see api/domain.ts);
// aliasing it here avoids colliding with the DOM's global `Event` type
// throughout this seam's component props.
export type { Event as ShowMeshEvent } from '../api'

import type {
  ControlPlane as ControlPlaneType,
  Evidence as EvidenceType,
  Event as EventType,
  FPPInstance as FPPInstanceType,
  NodeDeclaration as NodeDeclarationType,
  ResourceRef as ResourceRefType,
  ConfigShowAction as ConfigShowActionType,
  ConfigShowActionTarget as ConfigShowActionTargetType,
  ConfigShowMacroLocalFallback as ConfigShowMacroLocalFallbackType,
  ConfigShowMacroStep as ConfigShowMacroStepType,
  MacroRunSummary as MacroRunSummaryType,
  MacroRunStepCommand as MacroRunStepCommandType,
  ResolumeInstance as ResolumeInstanceType,
  ResolumeActionResult as ResolumeActionResultType,
  ActionBinding as ActionBindingType,
  ResolumeRecoveryRecordEntry as ResolumeRecoveryRecordEntryType,
  ResolumeRecoveryRestoreLayer as ResolumeRecoveryRestoreLayerType,
  ConfigShowSurfaceGeometry as ConfigShowSurfaceGeometryType,
  ConfigShowSurfaceOutput as ConfigShowSurfaceOutputType,
  ConfigShowCueAnnouncementOutput as ConfigShowCueAnnouncementOutputType,

  ConfigShowPlaylist as ConfigShowPlaylistType,
  ConfigShowPlaylistShowmeshAudio as ConfigShowPlaylistShowmeshAudioType,
  Asset as AssetType,
  NodeAssetManifest as NodeAssetManifestType,
  NightSessionState as NightSessionStateType,
  NightReadiness as NightReadinessType,
  NightReadinessCheck as NightReadinessCheckType,
  NightPhaseEvidence as NightPhaseEvidenceType,
  NightCue as NightCueType,
  NightCues as NightCuesType,
  NightBackgroundAudio as NightBackgroundAudioType,
  NightBackgroundAudioStep as NightBackgroundAudioStepType,
  NightAuthorization as NightAuthorizationType,
  FPPPlaylistReadinessResponse as FPPPlaylistReadinessResponseType,
  FPPPlaylistEntryReconciliationResponse as FPPPlaylistEntryReconciliationResponseType,
} from '../api'

// Derived, not duplicated: these are indexed-access views onto seam B's
// field types, so a value added to any of these unions in
// api/openapi.yaml flows through automatically instead of needing a
// second edit here.
export type ControlPlaneState = ControlPlaneType['state']
export type FPPHealth = FPPInstanceType['health']
export type EventSeverity = EventType['severity']
export type ResourceKind = ResourceRefType['kind']
export type EvidenceQuality = EvidenceType['quality']
// BUILD-PLAN Step 7 seam B (RES-008 D2/D6).
export type DiscoveryState = NodeDeclarationType['discoveryState']
// Step 9 (STEP-9-SPEC.md sections 5, 6): same derived-not-duplicated
// pattern as every alias above.
export type SafetyClass = ConfigShowActionType['safetyClass']
export type ActionIntegration = ConfigShowActionTargetType['integration']
export type LocalFallbackClass = ConfigShowMacroLocalFallbackType['class']
export type MacroStepOnFailure = ConfigShowMacroStepType['onFailure']
export type MacroStepOnUnconfirmed = ConfigShowMacroStepType['onUnconfirmed']
export type MacroRunState = MacroRunSummaryType['state']
export type MacroRunTrigger = MacroRunSummaryType['trigger']
export type MacroRunStepCommandState = MacroRunStepCommandType['state']
// Track D seam D-4: same derived-not-duplicated pattern as every alias
// above.
export type ResolumeHealth = ResolumeInstanceType['health']
export type ResolumeActionOutcome = ResolumeActionResultType['outcome']
export type ActionBindingState = ActionBindingType['state']
export type ResolumeRecoveryLayerState = ResolumeRecoveryRecordEntryType['state']
export type ResolumeRecoveryRestoreResult = ResolumeRecoveryRestoreLayerType['result']
// Track G seam G-8: same derived-not-duplicated pattern as every alias
// above.
export type SurfacePixelFormat = ConfigShowSurfaceGeometryType['pixelFormat']
export type SurfaceTransport = ConfigShowSurfaceOutputType['transport']
// Track H seam H6: same derived-not-duplicated pattern as every alias above.
export type CueAnnouncementPolicy = ConfigShowCueAnnouncementOutputType['policy']

// Track H seam H6: same derived-not-duplicated pattern as every alias
// above.
export type PlaylistRunner = ConfigShowPlaylistType['runner']
export type PlaylistMismatchPolicy = NonNullable<ConfigShowPlaylistType['mismatchPolicy']>
export type PlaylistShowmeshAudioRepeat = ConfigShowPlaylistShowmeshAudioType['repeat']
export type AssetMediaType = AssetType['mediaType']
export type AssetTargetKind = AssetType['targetKind']
export type NodeAssetManifestState = NodeAssetManifestType['state']
// Track F seam F2: same derived-not-duplicated pattern as every alias
// above.
export type NightLifecycleState = NightSessionStateType['state']
export type NightShutdownIntent = NightSessionStateType['shutdownIntent']
export type NightReadinessState = NightReadinessType['state']
// `outcome` is present only when `state` is "recorded" (NightReadiness's
// own schema description), so the wire field type is optional —
// NonNullable here since every call site already narrows on
// `state === 'recorded'` (or `outcome !== undefined`) before reading it.
export type NightReadinessOutcome = NonNullable<NightReadinessType['outcome']>
export type NightReadinessCheckState = NightReadinessCheckType['state']
export type NightPhaseEvidenceState = NightPhaseEvidenceType['state']
export type NightCueState = NightCueType['state']
// `outcome` (and `reason`) are optional wire fields on NightCue — see
// NightReadinessOutcome's identical comment just above.
export type NightCueOutcome = NonNullable<NightCueType['outcome']>
export type NightCuePhase = NightCueType['phase']
export type NightCuesState = NightCuesType['state']
export type NightBackgroundAudioState = NightBackgroundAudioType['state']
export type NightBackgroundAudioSequence = NightBackgroundAudioStepType['sequence']
export type NightBackgroundAudioStepKind = NightBackgroundAudioStepType['kind']
export type NightBackgroundAudioStepState = NightBackgroundAudioStepType['state']
// `outcome` (and `reason`) are optional wire fields, same shape as
// NightCue's own outcome — see NightReadinessOutcome's identical comment
// above.
export type NightBackgroundAudioStepOutcome = NonNullable<NightBackgroundAudioStepType['outcome']>
export type NightAuthorizationState = NightAuthorizationType['state']
// TRACK-H-H2-SPEC.md §5/§6: same derived-not-duplicated pattern as every
// alias above. `failingCondition` is present only when `ready` is false
// (FPPPlaylistReadinessResponse's own schema description): same
// NonNullable-over-optional-field shape as NightReadinessOutcome above.
export type FPPPlaylistReadinessFailingCondition = NonNullable<
  FPPPlaylistReadinessResponseType['failingCondition']
>
export type FPPPlaylistEntryReconciliationOutcome = FPPPlaylistEntryReconciliationResponseType['outcome']
