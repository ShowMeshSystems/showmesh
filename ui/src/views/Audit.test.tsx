import { cleanup, renderHook, waitFor } from '@testing-library/react'
import { act } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAuditLog } from './Audit'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { AuditEntry, Model } from '../app/types'
import type { ReactNode } from 'react'

// Track G seam G-8: the audit log's data layer. Monitor's Activity facet
// (Events.tsx) merged the audit view's own page into the combined
// system-event/audit stream, so this file's job narrowed from "render
// the audit page" to "prove useAuditLog" -- the hook Events.tsx consumes
// for the fetch, the newest-first opening, the backward id cursor, and
// the order-confirmation refusal. Every case this file proved before the
// merge is still asserted here, against the hook's own returned state
// rather than rendered DOM text.
const { listAudit } = vi.hoisted(() => ({ listAudit: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listAudit }
})

function makeAuditEntry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 9100,
    timestamp: '2026-08-17T00:00:00Z',
    principalId: 'p-1',
    principalName: 'admin-1',
    form: 'session',
    credentialId: 's-1',
    clientAddr: '10.0.1.9',
    action: 'config.put',
    target: 'config/fpp.endpoints',
    params: {},
    idempotencyKey: '',
    kind: 'admin',
    commandId: '',
    outcome: '',
    outcomeState: '',
    outcomeReason: '',
    ...overrides,
  }
}

function descendingPage(n: number, lowestId: number): AuditEntry[] {
  return Array.from({ length: n }, (_, i) =>
    makeAuditEntry({ id: lowestId + n - 1 - i, target: `config/kind-${lowestId + n - 1 - i}` }),
  )
}

const auditSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['audit:read'],
  scopesState: 'current',
})

function renderAuditLog(model: Model) {
  function wrapper({ children }: { children: ReactNode }) {
    return <ModelContext.Provider value={model}>{children}</ModelContext.Provider>
  }
  return renderHook(() => useAuditLog(), { wrapper })
}

afterEach(() => {
  cleanup()
  listAudit.mockReset()
})

describe('useAuditLog: newest-first opening', () => {
  it('opens on the most recent activity in exactly one request', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(100, 9001),
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
    expect(listAudit).toHaveBeenCalledTimes(1)
    expect(listAudit).toHaveBeenCalledWith({ order: 'desc', limit: 100 })
    expect(result.current.state.kind === 'loaded' && result.current.state.entries).toHaveLength(100)
  })

  it('states whether the window reaches the beginning of retained history', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: descendingPage(100, 9001),
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
    expect(result.current.state.kind === 'loaded' && result.current.state.atBeginning).toBe(false)
  })

  it('says so when the window already reaches the beginning of retained history', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(3, 9001),
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
    expect(result.current.state.kind === 'loaded' && result.current.state.atBeginning).toBe(true)
  })
})

describe('useAuditLog: paging backward', () => {
  it('pages backward on the last entry id, never on a count of entries', async () => {
    listAudit
      .mockResolvedValueOnce({
        serverTime: '2026-08-17T00:00:00Z',
        order: 'desc',
        oldestRetainedId: 1,
        entries: descendingPage(100, 9001),
      })
      .mockResolvedValueOnce({
        serverTime: '2026-08-17T00:00:00Z',
        order: 'desc',
        oldestRetainedId: 1,
        entries: descendingPage(100, 8901),
      })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))
    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))

    act(() => result.current.loadOlder())

    // 9001 is the lowest id on the first page; the cursor is that id, not
    // the 100 entries received. A count-derived cursor is exactly what
    // re-reads the same page forever once retention has pruned.
    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith({ order: 'desc', before: 9001, limit: 100 }),
    )
    await waitFor(() =>
      expect(result.current.state.kind === 'loaded' && result.current.state.entries).toHaveLength(200),
    )
  })

  it('keeps loaded entries and reports the failure when paging backward fails', async () => {
    listAudit
      .mockResolvedValueOnce({
        serverTime: '2026-08-17T00:00:00Z',
        order: 'desc',
        oldestRetainedId: 1,
        entries: descendingPage(100, 9001),
      })
      .mockRejectedValueOnce(new Error('network is down'))
    const { result } = renderAuditLog(makeModel({ session: auditSession }))
    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))

    act(() => result.current.loadOlder())

    await waitFor(() => expect(result.current.older.kind).toBe('error'))
    expect(result.current.state.kind === 'loaded' && result.current.state.entries).toHaveLength(100)
  })
})

describe('useAuditLog: order is confirmed, never trusted', () => {
  it('opens normally once the coordinator confirms desc order', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(3, 9001),
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
  })

  it('refuses to render entries as recent activity when order is not echoed', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      entries: [
        { ...makeAuditEntry(), id: undefined as unknown as number },
        { ...makeAuditEntry(), id: undefined as unknown as number },
      ],
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('unconfirmed-order'))
    expect(result.current.state.kind === 'unconfirmed-order' && result.current.state.message).toMatch(
      /did not echo an order/i,
    )
  })

  it('refuses to render entries as recent activity when order is echoed as asc', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'asc',
      oldestRetainedId: null,
      entries: descendingPage(3, 9001),
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('unconfirmed-order'))
    expect(result.current.state.kind === 'unconfirmed-order' && result.current.state.message).toMatch(
      /echoed order "asc"/i,
    )
  })

  it('does not offer another page when the last entry has no usable id', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [{ ...makeAuditEntry(), id: undefined as unknown as number }],
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
    expect(listAudit).toHaveBeenCalledTimes(1)
  })
})

describe('useAuditLog: failure is not emptiness', () => {
  it('reports a failed fetch as an error, not as an empty log', async () => {
    listAudit.mockRejectedValue(new Error('network is down'))
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('error'))
  })

  it('reports an empty log as empty, not as an error', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: null,
      entries: [],
    })
    const { result } = renderAuditLog(makeModel({ session: auditSession }))

    await waitFor(() => expect(result.current.state.kind).toBe('loaded'))
    expect(result.current.state.kind === 'loaded' && result.current.state.entries).toHaveLength(0)
  })
})

describe('useAuditLog: scope gating', () => {
  it('never fetches when audit:read is not held, and states the reason', async () => {
    const { result } = renderAuditLog(makeModel({ session: null }))
    expect(result.current.scopeGate.allowed).toBe(false)
    expect(result.current.scopeGate.reason).not.toBe('')
    expect(listAudit).not.toHaveBeenCalled()
  })
})
