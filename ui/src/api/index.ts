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
  ResourceRef,
  StreamSeq,
} from './domain'
export { useModel, submitToken, clearToken } from './useModel'

// Exported for seam C's error-boundary / advanced testing needs and for
// this seam's own tests; the real application only ever needs the
// singleton wired up in useModel.ts.
export { ApiStore, createApiStore, type ApiStoreOptions } from './store'
