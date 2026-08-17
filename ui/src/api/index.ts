/**
 * Public surface of src/api for seam C. See spec section 5: seam C
 * "imports the API only through ui/src/api, whose surface is fixed in
 * §5.4 and §5.5" — this barrel is that surface, plus the token actions
 * `useModel.ts` adds out of necessity (see that file's header comment).
 */
export type {
  Capability,
  CollectorStatus,
  ConfigFPPEndpoint,
  ConfigFPPEndpointsPayload,
  ConfigRevisionMeta,
  ConfigRevisionsResponse,
  ConnectionState,
  ControlPlane,
  DiscoveryProposal,
  DiscoveryRun,
  Evidence,
  EvidenceState,
  Event,
  EventSeq,
  FPPEndpointsConfigResponse,
  FPPCommandResult,
  // Track G seam G-2 (ADR-039).
  ConfigResolumeInstance,
  ConfigResolumeInstancesPayload,
  ResolumeInstancesConfigResponse,
  // Track G seam G-4 (ADR-039).
  AssetsSettingsConfigResponse,
  ConfigAssetsSettingsPayload,
  ConfigAssetsSettingsPutPayload,
  FPPInstance,
  Model,
  Node,
  NodeDeclaration,
  NodeEvidence,
  PrincipalSummary,
  ResolumeCompositionResponse,
  ResolumeCompositionSummary,
  ResolumeCompositionUploadResponse,
  ResolumeRecoveryRecordEntry,
  ResolumeRecoveryRestoreLayer,
  ResolumeRecoveryRestoreReport,
  ResolumeRecoveryResponse,
  ResolumeRecoveryRestoreResponse,
  ConfigResolumeRecoveryPayload,
  ResolumeRecoveryConfigResponse,
  // Track D seam D-4: Resolume as an observability resource and the
  // seven-action vocabulary.
  ResolumeInstanceComposition,
  ResolumeInstance,
  ResolumeInstancesResponse,
  ResolumeInstanceResponse,
  ResolumeActionParam,
  ResolumeAction,
  ResolumeActionsResponse,
  ResolumeActionResult,
  ResolumeActionResponse,
  ResolumeCompositionDeckSummary,
  ResolumeCompositionLayerGroup,
  ResolumeCompositionLayer,
  ResolumeCompositionColumn,
  ResolumeCompositionClip,
  ResourceRef,
  SessionInfo,
  SessionResponse,
  StreamSeq,
  // Step 9 (STEP-9-SPEC.md sections 5, 6): show.action / show.macro
  // configuration objects and the macro run surface.
  ConfigObjectSummary,
  ConfigObjectsListResponse,
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
  MacroRunResponse,
  MacroRunSubmitResponse,
  MacroRunsListResponse,
  MacroPriorFailureRequest,
  CreateMacroRunRequest,
  MacroRunChangedEvent,
} from './domain'
export {
  useModel,
  submitToken,
  clearToken,
  login,
  logout,
  claimBootstrap,
  getFPPEndpointsConfig,
  putFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getResolumeInstancesConfig,
  putResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  getAssetsSettingsConfig,
  putAssetsSettingsConfig,
  getAssetsSettingsConfigRevisions,
  stopFPPPlaylist,
  startFPPPlaylist,
  stopFPPPlaylistGracefully,
  pauseFPPPlaylist,
  resumeFPPPlaylist,
  nextFPPPlaylistItem,
  prevFPPPlaylistItem,
  setFPPVolume,
  runDiscovery,
  declareNode,
  deleteNodeDeclaration,
  listConfigObjects,
  getShowAction,
  putShowAction,
  getShowActionRevisions,
  getShowMacro,
  putShowMacro,
  getShowMacroRevisions,
  submitMacroRun,
  listMacroRuns,
  getMacroRun,
  getResolumeComposition,
  uploadResolumeComposition,
  getResolumeRecovery,
  getResolumeRecoveryConfig,
  putResolumeRecoveryConfig,
  restoreResolumeRecovery,
  listResolumeInstances,
  getResolumeInstance,
  listResolumeActions,
  launchResolumeClip,
  clearResolumeLayer,
  launchResolumeColumn,
  selectResolumeDeck,
  blackoutResolume,
  setResolumeLayerBypass,
  setResolumeLayerMaster,
} from './useModel'

// Track D seam D-2a: the progress shape [uploadResolumeComposition]'s
// callback reports — a type-only export, unlike everything above, since
// no runtime value of this shape is ever constructed inside src/api
// itself (the component that calls uploadResolumeComposition builds and
// reads it).
export type { UploadProgress } from './resolumeCompositionUpload'

// A pure, side-effect-free read of whether a break-glass token is
// currently stored (token.ts) — exported so SessionPanel.tsx can decide
// whether to offer clearing one directly, without reaching past this
// barrel into an internal module. Not a store action (no ApiStore
// singleton involved) and not itself part of ADR-022 decision 4's
// storage contract, which stays owned by token.ts.
export { getStoredToken } from './token'

// Exported for seam C's error-boundary / advanced testing needs and for
// this seam's own tests; the real application only ever needs the
// singleton wired up in useModel.ts.
export { ApiStore, createApiStore, type ApiStoreOptions } from './store'

// ADR-024's typed request errors: seam C's sign-in/sign-out/bootstrap
// forms (login/logout/claimBootstrap above all reject with one of these)
// need to dispatch on error TYPE to render the right message — "wrong
// password" (UnauthorizedError), "your browser can't do this, use the
// token field" (CSRFRejectedError), "you don't hold that scope"
// (ForbiddenError), "slow down" (TooManyRequestsError) — never on message
// text, matching this API's own "dispatch on type, never title/detail"
// posture (problem.ts).
export {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  IncompatibleVersionError,
  TooManyRequestsError,
  UnauthorizedError,
} from './errors'
