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
  FPPInstance,
  Model,
  Node,
  NodeDeclaration,
  NodeEvidence,
  PrincipalSummary,
  ResolumeCompositionResponse,
  ResolumeCompositionSummary,
  ResolumeCompositionUploadResponse,
  ResourceRef,
  SessionInfo,
  SessionResponse,
  StreamSeq,
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
  getResolumeComposition,
  uploadResolumeComposition,
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
