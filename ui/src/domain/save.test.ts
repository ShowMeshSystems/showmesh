import { describe, expect, it } from 'vitest'
import { guardedCreate, guardedSave, type Revisioned } from './save'
import { ApiError } from '../api'

type Payload = { name: string; notes: string }

function fake(overrides: Partial<Revisioned<Payload>> = {}): Revisioned<Payload> {
  return {
    revision: 1,
    payload: { name: 'Winter Ridge', notes: '' },
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalName: 'erbartos',
    ...overrides,
  }
}

describe('guardedSave', () => {
  it('writes when the re-read revision matches the loaded one', async () => {
    const loaded = fake()
    const written = fake({ revision: 2, payload: { name: 'Winter Ridge 2027', notes: '' } })
    const outcome = await guardedSave({
      loaded,
      read: () => Promise.resolve(loaded),
      write: () => Promise.resolve(written),
    })
    expect(outcome).toEqual({ kind: 'saved', response: written })
  })

  it('refuses to write and names the changed fields when the revision moved', async () => {
    const loaded = fake()
    const current = fake({
      revision: 2,
      payload: { name: 'Someone Else Renamed It', notes: 'left by someone else' },
      createdByPrincipalName: 'night-crew',
      updatedAt: '2026-08-30T19:00:00Z',
    })
    let wrote = false
    const outcome = await guardedSave({
      loaded,
      read: () => Promise.resolve(current),
      write: () => {
        wrote = true
        return Promise.resolve(current)
      },
    })
    expect(wrote).toBe(false)
    expect(outcome).toEqual({
      kind: 'stale',
      loadedRevision: 1,
      currentRevision: 2,
      changedBy: 'night-crew',
      changedAt: '2026-08-30T19:00:00Z',
      changedFields: ['name', 'notes'],
    })
  })

  it('does not write when the re-read itself fails', async () => {
    const loaded = fake()
    let wrote = false
    const outcome = await guardedSave({
      loaded,
      read: () => Promise.reject(new Error('network down')),
      write: () => {
        wrote = true
        return Promise.resolve(loaded)
      },
    })
    expect(wrote).toBe(false)
    expect(outcome).toEqual({ kind: 'unreadable', reason: 'network down' })
  })

  it('lets a write rejection propagate to the caller', async () => {
    const loaded = fake()
    await expect(
      guardedSave({
        loaded,
        read: () => Promise.resolve(loaded),
        write: () => Promise.reject(new ApiError('refused', 400)),
      }),
    ).rejects.toThrow('refused')
  })
})

describe('guardedCreate', () => {
  it('refuses a taken id without writing', async () => {
    let wrote = false
    const outcome = await guardedCreate({
      read: () => Promise.resolve(fake()),
      write: () => {
        wrote = true
        return Promise.resolve(fake())
      },
    })
    expect(wrote).toBe(false)
    expect(outcome).toEqual({ kind: 'taken' })
  })

  it('creates when the read is a 404, meaning the id is free', async () => {
    const created = fake({ revision: 1 })
    const outcome = await guardedCreate({
      read: () => Promise.reject(new ApiError('not found', 404, 'https://showmesh.dev/problems/resource-not-found')),
      write: () => Promise.resolve(created),
    })
    expect(outcome).toEqual({ kind: 'created', response: created })
  })

  it('does not write when the read fails for a reason other than 404', async () => {
    let wrote = false
    const outcome = await guardedCreate({
      read: () => Promise.reject(new Error('network down')),
      write: () => {
        wrote = true
        return Promise.resolve(fake())
      },
    })
    expect(wrote).toBe(false)
    expect(outcome).toEqual({ kind: 'unreadable', reason: 'network down' })
  })
})
