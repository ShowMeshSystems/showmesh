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
  ConfigRevisionsResponse,
  FPPCommandResult,
  FPPEndpointsConfigResponse,
  Model,
} from './domain'
import type { components } from './generated/schema'

type SchemaDiscoveryRunResponse = components['schemas']['DiscoveryRunResponse']
type SchemaNodeDeclarationResponse = components['schemas']['NodeDeclarationResponse']

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

// Step 7 seam C: the first write that leaves this machine. Same thin
// pass-through pattern as the others above.
export function stopFPPPlaylist(instanceId: string): Promise<FPPCommandResult> {
  return store.stopFPPPlaylist(instanceId)
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
