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
 */
import { useSyncExternalStore } from 'react'
import { ApiStore } from './store'
import type { Model } from './domain'

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
