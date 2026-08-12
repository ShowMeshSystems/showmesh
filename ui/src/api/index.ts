/**
 * Public surface of src/api for seam C. See spec section 5: seam C
 * "imports the API only through ui/src/api, whose surface is fixed in
 * §5.4 and §5.5" — this barrel is that surface, plus the token actions
 * `useModel.ts` adds out of necessity (see that file's header comment).
 */
export type {
  Capability,
  CollectorStatus,
  ConnectionState,
  ControlPlane,
  Evidence,
  EvidenceState,
  Event,
  EventSeq,
  FPPInstance,
  Model,
  Node,
  NodeEvidence,
  PrincipalSummary,
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
} from './useModel'

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
