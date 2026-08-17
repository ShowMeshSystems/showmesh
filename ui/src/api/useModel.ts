/**
 * The one file in src/api allowed to import React (spec section 5): a
 * thin `useSyncExternalStore` wrapper around a single module-level
 * `ApiStore`. Everything else in src/api is framework-free and is
 * tested with no React present at all.
 *
 * `submitToken`/`clearToken` are exported alongside the hook because
 * they operate on the same singleton and there is nowhere else for
 * seam C to reach it — `ConnectionState`'s `unauthorized` variant
 * (domain.ts) is a dead end without some way to answer the prompt it
 * implies. Kept here rather than adding a third file so the singleton's
 * wiring stays in one place. Named `submitToken`/`clearToken` (not
 * `submitApiToken`/`clearApiToken`) to match seam C's documented
 * assumption in src/app/App.tsx, discovered once both seams' code
 * existed side by side.
 *
 * `login`/`logout`/`claimBootstrap` (ADR-024) are the same pattern for
 * the session/cookie credential form: thin pass-throughs to the one
 * `ApiStore` instance, so a form component never touches the store
 * class directly.
 */
import { useSyncExternalStore } from 'react'
import { ApiStore } from './store'
import type {
  ConfigFPPEndpointsPayload,
  ConfigResolumeRecoveryPayload,
  ConfigRevisionsResponse,
  ConfigShowAction,
  ConfigShowActive,
  ConfigShowMacro,
  ConfigShowSurface,
  ConfigShowWrite,
  FPPCommandResult,
  FPPEndpointsConfigResponse,
  Model,
  ResolumeCompositionResponse,
  ResolumeCompositionUploadResponse,
  ResolumeRecoveryConfigResponse,
  ResolumeRecoveryResponse,
  ResolumeRecoveryRestoreResponse,
} from './domain'
import type { UploadProgress } from './resolumeCompositionUpload'
import type { components } from './generated/schema'
import type { ResolumeActionResult } from './domain'

type SchemaDiscoveryRunResponse = components['schemas']['DiscoveryRunResponse']
type SchemaNodeDeclarationResponse = components['schemas']['NodeDeclarationResponse']
type SchemaConfigObjectsListResponse = components['schemas']['ConfigObjectsListResponse']
type SchemaShowActionConfigResponse = components['schemas']['ShowActionConfigResponse']
type SchemaShowMacroConfigResponse = components['schemas']['ShowMacroConfigResponse']
type SchemaMacroRunResponse = components['schemas']['MacroRunResponse']
type SchemaMacroRunSubmitResponse = components['schemas']['MacroRunSubmitResponse']
type SchemaMacroRunsListResponse = components['schemas']['MacroRunsListResponse']
type SchemaResolumeInstancesResponse = components['schemas']['ResolumeInstancesResponse']
type SchemaResolumeInstanceResponse = components['schemas']['ResolumeInstanceResponse']
type SchemaResolumeActionsResponse = components['schemas']['ResolumeActionsResponse']
// Track G seam G-8: the Operator UI for Track E.
type SchemaShowConfigResponse = components['schemas']['ShowConfigResponse']
type SchemaShowSurfaceConfigResponse = components['schemas']['ShowSurfaceConfigResponse']
type SchemaShowActiveConfigResponse = components['schemas']['ShowActiveConfigResponse']
type SchemaAssetResponse = components['schemas']['AssetResponse']
type SchemaAssetsListResponse = components['schemas']['AssetsListResponse']
type SchemaAssetManifestResponse = components['schemas']['AssetManifestResponse']
type SchemaNodeAssetManifestResponse = components['schemas']['NodeAssetManifestResponse']
type SchemaAuditResponse = components['schemas']['AuditResponse']

const store = new ApiStore({ baseUrl: '/api/v1' })
store.connect()

export function useModel(): Model {
  return useSyncExternalStore(store.subscribe, store.getSnapshot)
}

export function submitToken(token: string): void {
  store.submitToken(token)
}

export function clearToken(): void {
  store.clearToken()
}

export function login(name: string, password: string, deviceLabel: string): Promise<void> {
  return store.login(name, password, deviceLabel)
}

export function logout(sessionId?: string): Promise<void> {
  return store.logout(sessionId)
}

export function claimBootstrap(
  code: string,
  name: string,
  password: string,
  deviceLabel: string,
): Promise<void> {
  return store.claimBootstrap(code, name, password, deviceLabel)
}

// Step 7 seam A (RES-008 D1): the same thin pass-through pattern as
// login/logout/claimBootstrap above, for the configuration write surface —
// see store.ts's "Step 7 seam A" methods for why none of these three
// touches `Model`.
export function getFPPEndpointsConfig(): Promise<FPPEndpointsConfigResponse> {
  return store.getFPPEndpointsConfig()
}

export function putFPPEndpointsConfig(
  payload: ConfigFPPEndpointsPayload,
): Promise<FPPEndpointsConfigResponse> {
  return store.putFPPEndpointsConfig(payload)
}

export function getFPPEndpointsConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getFPPEndpointsConfigRevisions()
}

// Track D seam D-3a: Arena crash recovery. Same thin pass-through pattern.
export function getResolumeRecovery(): Promise<ResolumeRecoveryResponse> {
  return store.getResolumeRecovery()
}

export function getResolumeRecoveryConfig(): Promise<ResolumeRecoveryConfigResponse> {
  return store.getResolumeRecoveryConfig()
}

export function putResolumeRecoveryConfig(
  payload: ConfigResolumeRecoveryPayload,
): Promise<ResolumeRecoveryConfigResponse> {
  return store.putResolumeRecoveryConfig(payload)
}

export function restoreResolumeRecovery(): Promise<ResolumeRecoveryRestoreResponse> {
  return store.restoreResolumeRecovery()
}

// Step 7 seam C / Step 8: FPP primitive command dispatch. Same thin
// pass-through pattern as the others above — every method here maps to
// one row of docs/bench/fpp-command-vocabulary.md section 4's registry,
// via ApiStore's single dispatchFPPCommand (store.ts).
export function stopFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
  return store.stopFPPPlaylist(instanceId)
}

export function startFPPPlaylist(
  instanceId: string,
  playlist: string,
  repeat: boolean,
  ifBusy: 'refuse' | 'replace',
): Promise<FPPCommandResult> {
  return store.startFPPPlaylist(instanceId, playlist, repeat, ifBusy)
}

export function stopFPPPlaylistGracefully(instanceId: string, afterLoop: boolean): Promise<FPPCommandResult> {
  return store.stopFPPPlaylistGracefully(instanceId, afterLoop)
}

export function pauseFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
  return store.pauseFPPPlaylist(instanceId)
}

export function resumeFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
  return store.resumeFPPPlaylist(instanceId)
}

export function nextFPPPlaylistItem(instanceId: string): Promise<FPPCommandResult> {
  return store.nextFPPPlaylistItem(instanceId)
}

export function prevFPPPlaylistItem(instanceId: string): Promise<FPPCommandResult> {
  return store.prevFPPPlaylistItem(instanceId)
}

export function setFPPVolume(instanceId: string, volume: number): Promise<FPPCommandResult> {
  return store.setFPPVolume(instanceId, volume)
}

// Step 7 seam B (RES-008 D2/D6): node discovery and declaration. Same
// thin pass-through pattern as the others above.

export function runDiscovery(): Promise<SchemaDiscoveryRunResponse> {
  return store.runDiscovery()
}

export function declareNode(nodeId: string, label: string, notes: string): Promise<SchemaNodeDeclarationResponse> {
  return store.declareNode(nodeId, label, notes)
}

export function deleteNodeDeclaration(nodeId: string): Promise<void> {
  return store.deleteNodeDeclaration(nodeId)
}

// Step 9 (STEP-9-SPEC.md sections 5, 6): show.action / show.macro
// configuration objects and the macro run surface. Same thin
// pass-through pattern as every method above.

export function listConfigObjects(
  kind: 'show.action' | 'show.macro' | 'show' | 'show.surface',
  show?: string,
): Promise<SchemaConfigObjectsListResponse> {
  return store.listConfigObjects(kind, show)
}

export function getShowAction(id: string): Promise<SchemaShowActionConfigResponse> {
  return store.getShowAction(id)
}

export function putShowAction(id: string, payload: ConfigShowAction): Promise<SchemaShowActionConfigResponse> {
  return store.putShowAction(id, payload)
}

export function getShowActionRevisions(id: string): Promise<ConfigRevisionsResponse> {
  return store.getShowActionRevisions(id)
}

export function getShowMacro(id: string): Promise<SchemaShowMacroConfigResponse> {
  return store.getShowMacro(id)
}

export function putShowMacro(id: string, payload: ConfigShowMacro): Promise<SchemaShowMacroConfigResponse> {
  return store.putShowMacro(id, payload)
}

export function getShowMacroRevisions(id: string): Promise<ConfigRevisionsResponse> {
  return store.getShowMacroRevisions(id)
}

export function submitMacroRun(macroId: string): Promise<SchemaMacroRunSubmitResponse> {
  return store.submitMacroRun(macroId)
}

export function listMacroRuns(filter?: {
  macroId?: string
  state?: 'running' | 'finished'
  limit?: number
}): Promise<SchemaMacroRunsListResponse> {
  return store.listMacroRuns(filter)
}

export function getMacroRun(runId: string): Promise<SchemaMacroRunResponse> {
  return store.getMacroRun(runId)
}

// Track D seam D-2a (ADR-032): the Resolume composition upload surface.
// Same thin pass-through pattern as the others above.

export function getResolumeComposition(): Promise<ResolumeCompositionResponse> {
  return store.getResolumeComposition()
}

export function uploadResolumeComposition(
  file: File,
  onProgress: (progress: UploadProgress) => void,
): Promise<ResolumeCompositionUploadResponse> {
  return store.uploadResolumeComposition(file, onProgress)
}

// Track D seam D-4: Resolume as an observability resource (seam E) and
// the seven-action vocabulary (D-3/seam B). Same thin pass-through
// pattern as every method above.

export function listResolumeInstances(): Promise<SchemaResolumeInstancesResponse> {
  return store.listResolumeInstances()
}

export function getResolumeInstance(instanceId: string): Promise<SchemaResolumeInstanceResponse> {
  return store.getResolumeInstance(instanceId)
}

export function listResolumeActions(): Promise<SchemaResolumeActionsResponse> {
  return store.listResolumeActions()
}

export function launchResolumeClip(params: {
  clip: string
  deck?: string
  layer?: string
  persistent?: boolean
}): Promise<ResolumeActionResult> {
  return store.launchResolumeClip(params)
}

export function clearResolumeLayer(layer: string): Promise<ResolumeActionResult> {
  return store.clearResolumeLayer(layer)
}

export function launchResolumeColumn(column: string, deck: string): Promise<ResolumeActionResult> {
  return store.launchResolumeColumn(column, deck)
}

export function selectResolumeDeck(deck: string): Promise<ResolumeActionResult> {
  return store.selectResolumeDeck(deck)
}

export function blackoutResolume(): Promise<ResolumeActionResult> {
  return store.blackoutResolume()
}

export function setResolumeLayerBypass(layer: string, bypassed: boolean): Promise<ResolumeActionResult> {
  return store.setResolumeLayerBypass(layer, bypassed)
}

export function setResolumeLayerMaster(layer: string, master: number): Promise<ResolumeActionResult> {
  return store.setResolumeLayerMaster(layer, master)
}

// Track G seam G-8: the Operator UI for Track E (ADR-027, ADR-026,
// ADR-028). Same thin pass-through pattern as every method above.

export function getShow(id: string): Promise<SchemaShowConfigResponse> {
  return store.getShow(id)
}

export function putShow(id: string, payload: ConfigShowWrite): Promise<SchemaShowConfigResponse> {
  return store.putShow(id, payload)
}

export function getShowRevisions(id: string): Promise<ConfigRevisionsResponse> {
  return store.getShowRevisions(id)
}

export function getShowSurface(id: string): Promise<SchemaShowSurfaceConfigResponse> {
  return store.getShowSurface(id)
}

export function putShowSurface(id: string, payload: ConfigShowSurface): Promise<SchemaShowSurfaceConfigResponse> {
  return store.putShowSurface(id, payload)
}

export function getShowSurfaceRevisions(id: string): Promise<ConfigRevisionsResponse> {
  return store.getShowSurfaceRevisions(id)
}

export function getShowActive(): Promise<SchemaShowActiveConfigResponse> {
  return store.getShowActive()
}

export function putShowActive(payload: ConfigShowActive): Promise<SchemaShowActiveConfigResponse> {
  return store.putShowActive(payload)
}

export function getShowActiveRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getShowActiveRevisions()
}

export function listAssets(filter?: {
  show?: string
  sequence?: string
  node?: string
}): Promise<SchemaAssetsListResponse> {
  return store.listAssets(filter)
}

export function uploadAsset(
  file: File,
  fields: { show: string; sequence: string; mediaType: 'fseq' | 'audio' | 'media'; targetKind: 'node' | 'show'; target?: string },
  onProgress: (progress: UploadProgress) => void,
): Promise<SchemaAssetResponse> {
  return store.uploadAsset(file, fields, onProgress)
}

export function assetContentUrl(id: string): string {
  return store.assetContentUrl(id)
}

export function getAssetManifest(): Promise<SchemaAssetManifestResponse> {
  return store.getAssetManifest()
}

export function getNodeAssetManifest(nodeId: string): Promise<SchemaNodeAssetManifestResponse> {
  return store.getNodeAssetManifest(nodeId)
}

export function listAudit(filter?: { since?: number; limit?: number }): Promise<SchemaAuditResponse> {
  return store.listAudit(filter)
}
