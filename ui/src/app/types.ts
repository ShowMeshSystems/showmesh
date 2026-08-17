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
  // Track D seam D-4: Resolume as an observability resource and the
  // seven-action vocabulary.
  ResolumeInstanceComposition,
  ResolumeInstance,
  ResolumeActionParam,
  ResolumeAction,
  ResolumeActionResult,
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
  ConfigShowSurface,
  ShowSurfaceConfigResponse,
  ConfigShowActive,
  ShowActiveConfigResponse,
  Asset,
  AssetResponse,
  NodeAssetManifest,
  MissingAsset,
  AssetGap,
  ExtraAsset,
  AuditEntry,
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
  ResolumeRecoveryRecordEntry as ResolumeRecoveryRecordEntryType,
  ResolumeRecoveryRestoreLayer as ResolumeRecoveryRestoreLayerType,
  ConfigShowSurfaceGeometry as ConfigShowSurfaceGeometryType,
  ConfigShowSurfaceOutput as ConfigShowSurfaceOutputType,
  Asset as AssetType,
  NodeAssetManifest as NodeAssetManifestType,
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
export type ResolumeRecoveryLayerState = ResolumeRecoveryRecordEntryType['state']
export type ResolumeRecoveryRestoreResult = ResolumeRecoveryRestoreLayerType['result']
// Track G seam G-8: same derived-not-duplicated pattern as every alias
// above.
export type SurfacePixelFormat = ConfigShowSurfaceGeometryType['pixelFormat']
export type SurfaceTransport = ConfigShowSurfaceOutputType['transport']
export type AssetMediaType = AssetType['mediaType']
export type AssetTargetKind = AssetType['targetKind']
export type NodeAssetManifestState = NodeAssetManifestType['state']
