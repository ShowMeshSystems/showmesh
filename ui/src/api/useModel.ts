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
  AssetsSettingsConfigResponse,
  ConfigAssetsSettingsPutPayload,
  ConfigFPPEndpointsPayload,
  ConfigFPPMQTTPutRequest,
  ConfigNightSessionActive,
  ConfigNightSessionWrite,
  ConfigRenderSettingsPayload,
  ConfigResolumeInstancesPayload,
  ConfigResolumeRecoveryPayload,
  ConfigRevisionsResponse,
  ConfigShowAction,
  ConfigShowActive,
  ConfigShowMacro,
  ConfigShowSurface,
  ConfigShowWrite,
  CreatePrincipalRequest,
  FPPCommandResult,
  FPPEndpointsConfigResponse,
  FPPMQTTConfigResponse,
  IssueTokenRequest,
  IssueTokenResponse,
  Model,
  NightCommandName,
  NightCommandResponse,
  NightSessionActiveConfigResponse,
  NightSessionConfigResponse,
  NightSessionResponse,
  PrincipalResponse,
  PrincipalsResponse,
  RenderCommandResult,
  RenderSettingsConfigResponse,
  ConfigShowModePayload,
  ShowModeConfigResponse,
  ResolumeCompositionResponse,
  ResolumeCompositionUploadResponse,
  ResolumeInstancesConfigResponse,
  ResolumeRecoveryConfigResponse,
  ResolumeRecoveryResponse,
  ResolumeRecoveryRestoreResponse,
  SetPrincipalPasswordRequest,
  SetPrincipalRoleRequest,
  TokensResponse,
} from './domain'
import type { UploadProgress } from './resolumeCompositionUpload'
import type { components } from './generated/schema'
import type { ActionBinding, ActionInvocationResult, CueCatalogDeployResult, ResolumeActionResult } from './domain'

type SchemaDiscoveryRunResponse = components['schemas']['DiscoveryRunResponse']
type SchemaNodeDeclarationResponse = components['schemas']['NodeDeclarationResponse']
type SchemaConfigObjectsListResponse = components['schemas']['ConfigObjectsListResponse']
type SchemaShowActionConfigResponse = components['schemas']['ShowActionConfigResponse']
type SchemaShowMacroConfigResponse = components['schemas']['ShowMacroConfigResponse']
type SchemaShowSurfaceConfigResponse = components['schemas']['ShowSurfaceConfigResponse']
type SchemaMacroRunResponse = components['schemas']['MacroRunResponse']
type SchemaMacroRunSubmitResponse = components['schemas']['MacroRunSubmitResponse']
type SchemaMacroRunsListResponse = components['schemas']['MacroRunsListResponse']
type SchemaResolumeInstancesResponse = components['schemas']['ResolumeInstancesResponse']
type SchemaResolumeInstanceResponse = components['schemas']['ResolumeInstanceResponse']
type SchemaResolumeActionsResponse = components['schemas']['ResolumeActionsResponse']
// Track G seam G-8: the Operator UI for Track E.
type SchemaShowConfigResponse = components['schemas']['ShowConfigResponse']
type SchemaShowActiveConfigResponse = components['schemas']['ShowActiveConfigResponse']
type SchemaAssetResponse = components['schemas']['AssetResponse']
type SchemaAssetsListResponse = components['schemas']['AssetsListResponse']
type SchemaAssetManifestResponse = components['schemas']['AssetManifestResponse']
type SchemaNodeAssetManifestResponse = components['schemas']['NodeAssetManifestResponse']
type SchemaAuditResponse = components['schemas']['AuditResponse']
type SchemaCueCatalogResponse = components['schemas']['CueCatalogResponse']

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

// Track G seam G-2 (ADR-039): the same thin pass-through pattern, for the
// resolume.instances configuration write surface.
export function getResolumeInstancesConfig(): Promise<ResolumeInstancesConfigResponse> {
  return store.getResolumeInstancesConfig()
}

export function putResolumeInstancesConfig(
  payload: ConfigResolumeInstancesPayload,
): Promise<ResolumeInstancesConfigResponse> {
  return store.putResolumeInstancesConfig(payload)
}

export function getResolumeInstancesConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getResolumeInstancesConfigRevisions()
}

// Track G seam G-3 (ADR-039): the same thin pass-through pattern, for the
// fpp.mqtt configuration write surface. putFPPMQTTConfig's request shape
// is ConfigFPPMQTTPutRequest, not ConfigFPPMQTTPayload — every field is
// independently optional (decision 5), unlike every other config kind's
// PUT here.
export function getFPPMQTTConfig(): Promise<FPPMQTTConfigResponse> {
  return store.getFPPMQTTConfig()
}

export function putFPPMQTTConfig(
  request: ConfigFPPMQTTPutRequest,
): Promise<FPPMQTTConfigResponse> {
  return store.putFPPMQTTConfig(request)
}

export function getFPPMQTTConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getFPPMQTTConfigRevisions()
}

// Track G seam G-4 (ADR-039): the same thin pass-through pattern, for the
// assets.settings configuration write surface. putAssetsSettingsConfig's
// payload is the SEPARATE PutPayload type — every field optional, unlike
// AssetsSettingsConfigResponse's own always-fully-populated payload.
export function getAssetsSettingsConfig(): Promise<AssetsSettingsConfigResponse> {
  return store.getAssetsSettingsConfig()
}

export function putAssetsSettingsConfig(
  payload: ConfigAssetsSettingsPutPayload,
): Promise<AssetsSettingsConfigResponse> {
  return store.putAssetsSettingsConfig(payload)
}

export function getAssetsSettingsConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getAssetsSettingsConfigRevisions()
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

// Track B seam B2c (ADR-039): render.settings. Same thin pass-through
// pattern.
export function getRenderSettingsConfig(): Promise<RenderSettingsConfigResponse> {
  return store.getRenderSettingsConfig()
}

export function putRenderSettingsConfig(
  payload: ConfigRenderSettingsPayload,
): Promise<RenderSettingsConfigResponse> {
  return store.putRenderSettingsConfig(payload)
}

export function getRenderSettingsConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getRenderSettingsConfigRevisions()
}

// ADR-033: show.mode. Same thin pass-through pattern.
export function getShowModeConfig(): Promise<ShowModeConfigResponse> {
  return store.getShowModeConfig()
}

export function putShowModeConfig(payload: ConfigShowModePayload): Promise<ShowModeConfigResponse> {
  return store.putShowModeConfig(payload)
}

export function getShowModeConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getShowModeConfigRevisions()
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

// Track B seam B2b-front: the three render.* dispatch endpoints. Same
// thin pass-through pattern.
export function applyRenderSurface(
  nodeId: string,
  surfaceId: string,
  sequenceId: string,
): Promise<RenderCommandResult> {
  return store.applyRenderSurface(nodeId, surfaceId, sequenceId)
}

export function clearRenderSurface(nodeId: string, surfaceId: string): Promise<RenderCommandResult> {
  return store.clearRenderSurface(nodeId, surfaceId)
}

export function restartRenderPipeline(nodeId: string, surfaceId: string): Promise<RenderCommandResult> {
  return store.restartRenderPipeline(nodeId, surfaceId)
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
  kind: 'show.action' | 'show.macro' | 'show' | 'show.surface' | 'night.session',
  show?: string,
): Promise<SchemaConfigObjectsListResponse> {
  return store.listConfigObjects(kind, show)
}

// Server-side node filter (GET /config/show.surface?node=) — see
// store.ts's listShowSurfacesForNode. Replaces the earlier
// listConfigObjects + per-row getShowSurface fan-out RenderSurfacePanel.tsx
// used to resolve which show.surface objects are assigned to a node.
export function listShowSurfacesForNode(nodeId: string): Promise<SchemaConfigObjectsListResponse> {
  return store.listShowSurfacesForNode(nodeId)
}

export function getShowSurface(id: string): Promise<SchemaShowSurfaceConfigResponse> {
  return store.getShowSurface(id)
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

// The pre-show binding check and one action invocation, outside of any
// macro run (ADR-029). Same thin pass-through pattern as every method
// above.

export function getActionBinding(id: string): Promise<ActionBinding> {
  return store.getActionBinding(id)
}

export function listActionBindings(show?: string): Promise<ActionBinding[]> {
  return store.listActionBindings(show)
}

export function invokeAction(id: string): Promise<ActionInvocationResult> {
  return store.invokeAction(id)
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

// -- Track G seam G-5: identity administration over the API -------------

export function listPrincipals(): Promise<PrincipalsResponse> {
  return store.listPrincipals()
}

export function createPrincipal(payload: CreatePrincipalRequest): Promise<PrincipalResponse> {
  return store.createPrincipal(payload)
}

export function setPrincipalRole(id: string, payload: SetPrincipalRoleRequest): Promise<PrincipalResponse> {
  return store.setPrincipalRole(id, payload)
}

export function disablePrincipal(id: string): Promise<PrincipalResponse> {
  return store.disablePrincipal(id)
}

export function enablePrincipal(id: string): Promise<PrincipalResponse> {
  return store.enablePrincipal(id)
}

export function resetPrincipalPassword(id: string, payload: SetPrincipalPasswordRequest): Promise<PrincipalResponse> {
  return store.resetPrincipalPassword(id, payload)
}

export function listPrincipalTokens(id: string): Promise<TokensResponse> {
  return store.listPrincipalTokens(id)
}

export function issuePrincipalToken(id: string, payload: IssueTokenRequest): Promise<IssueTokenResponse> {
  return store.issuePrincipalToken(id, payload)
}

export function revokePrincipalToken(id: string, tokenId: string): Promise<void> {
  return store.revokePrincipalToken(id, tokenId)
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

// Track H seam H6: the resolved Cue catalog a node holds, and the
// operator's own deploy control. Same thin pass-through pattern.

export function getNodeCueCatalog(nodeId: string): Promise<SchemaCueCatalogResponse> {
  return store.getNodeCueCatalog(nodeId)
}

export function deployNodeCueCatalog(nodeId: string): Promise<CueCatalogDeployResult> {
  return store.deployNodeCueCatalog(nodeId)
}

// Track F seam F2/F1: the night-session lifecycle controller and the
// night.session/night.session.active configuration kinds. Same thin
// pass-through pattern as every method above.

export function getCurrentNightSession(): Promise<NightSessionResponse> {
  return store.getCurrentNightSession()
}

export function getNightSessionById(id: string): Promise<NightSessionResponse> {
  return store.getNightSessionById(id)
}

export function dispatchNightCommand(
  command: NightCommandName,
  idempotencyKey?: string,
): Promise<NightCommandResponse> {
  return store.dispatchNightCommand(command, idempotencyKey)
}

export function getNightSessionConfig(id: string): Promise<NightSessionConfigResponse> {
  return store.getNightSessionConfig(id)
}

export function putNightSessionConfig(
  id: string,
  payload: ConfigNightSessionWrite,
): Promise<NightSessionConfigResponse> {
  return store.putNightSessionConfig(id, payload)
}

export function getNightSessionConfigRevisions(id: string): Promise<ConfigRevisionsResponse> {
  return store.getNightSessionConfigRevisions(id)
}

export function getNightSessionConfigRevision(id: string, revision: number): Promise<NightSessionConfigResponse> {
  return store.getNightSessionConfigRevision(id, revision)
}

export function getNightSessionActiveConfig(): Promise<NightSessionActiveConfigResponse> {
  return store.getNightSessionActiveConfig()
}

export function putNightSessionActiveConfig(
  payload: ConfigNightSessionActive,
): Promise<NightSessionActiveConfigResponse> {
  return store.putNightSessionActiveConfig(payload)
}

export function getNightSessionActiveConfigRevisions(): Promise<ConfigRevisionsResponse> {
  return store.getNightSessionActiveConfigRevisions()
}
