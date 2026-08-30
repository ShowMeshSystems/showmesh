/**
 * D-014 B: the coordinator's `PUT /config/...` is a full replacement with no
 * concurrency control, so a client detects a stale write itself, and D-011 B
 * / D-017 B's creation surfaces need a create that refuses a taken id. Pure
 * functions, no React: this is the shared path every config editor and every
 * creation draft calls.
 */
import { ApiError } from '../api'
import { describeApiError } from './session'

export type Revisioned<P> = {
  revision: number
  payload: P
  updatedAt: string
  createdByPrincipalName: string | null
}

export type SaveOutcome<R> =
  | { kind: 'saved'; response: R }
  | {
      kind: 'stale'
      loadedRevision: number
      currentRevision: number
      changedBy: string | null
      changedAt: string
      changedFields: string[]
    }
  | { kind: 'unreadable'; reason: string }

export type CreateOutcome<R> = { kind: 'created'; response: R } | { kind: 'taken' } | { kind: 'unreadable'; reason: string }

/** Top-level payload keys whose JSON value differs, sorted: never guessed, always derived. */
function changedPayloadFields<P>(loaded: P, current: P): string[] {
  const before = loaded as Record<string, unknown>
  const after = current as Record<string, unknown>
  const keys = new Set([...Object.keys(before), ...Object.keys(after)])
  const changed: string[] = []
  for (const key of keys) {
    if (JSON.stringify(before[key]) !== JSON.stringify(after[key])) changed.push(key)
  }
  return changed.sort()
}

/**
 * Re-reads immediately before writing. Only a re-read revision matching the
 * one this edit was loaded from is allowed to write; a moved revision or an
 * unreadable re-read both refuse the write outright. A `write()` rejection
 * (a refused `PUT`) propagates to the caller unchanged - that is the
 * screen's own refusal path, not this function's business.
 */
export async function guardedSave<P, R extends Revisioned<P>>(args: {
  loaded: R
  read: () => Promise<R>
  write: () => Promise<R>
}): Promise<SaveOutcome<R>> {
  let current: R
  try {
    current = await args.read()
  } catch (err: unknown) {
    return { kind: 'unreadable', reason: describeApiError(err) }
  }
  if (current.revision === args.loaded.revision) {
    const response = await args.write()
    return { kind: 'saved', response }
  }
  return {
    kind: 'stale',
    loadedRevision: args.loaded.revision,
    currentRevision: current.revision,
    changedBy: current.createdByPrincipalName,
    changedAt: current.updatedAt,
    changedFields: changedPayloadFields(args.loaded.payload, current.payload),
  }
}

/**
 * `PUT /config/.../{id}` on an id that already exists writes a new revision
 * over that object rather than failing, so a create reads first: the read
 * succeeding means the id is taken, a 404 means it is free, and any other
 * read failure means "not knowing" - never the same fact as "free".
 */
export async function guardedCreate<R>(args: { read: () => Promise<R>; write: () => Promise<R> }): Promise<CreateOutcome<R>> {
  try {
    await args.read()
    return { kind: 'taken' }
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) {
      const response = await args.write()
      return { kind: 'created', response }
    }
    return { kind: 'unreadable', reason: describeApiError(err) }
  }
}
