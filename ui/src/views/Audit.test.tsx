import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Audit } from './Audit'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { AuditEntry, Model } from '../app/types'

// Track G seam G-8: the audit view. GET /audit pages newest-first
// (`order=desc`), so this screen opens on the most recent activity in ONE
// request and pages BACKWARD on entry ids. The load-bearing cases here are
// that the single request happens, that the backward cursor is a real id
// rather than a count, that a bounded window says what is on it (ADR-020:
// absent evidence is stated, never omitted), and that a failed fetch stays
// distinguishable from an empty log.
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

// descendingPage builds a newest-first page of n entries ending just above
// `lowestId`, the shape the coordinator returns for `order=desc`.
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

function renderAudit(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <Audit />
    </ModelContext.Provider>,
  )
}

afterEach(() => {
  cleanup()
  listAudit.mockReset()
})

describe('Audit: newest-first opening', () => {
  it('opens on the most recent activity in exactly one request', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(100, 9001),
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByText('config/kind-9100')).toBeInTheDocument()
    expect(listAudit).toHaveBeenCalledTimes(1)
    expect(listAudit).toHaveBeenCalledWith({ order: 'desc', limit: 100 })
  })

  it('states what the bounded window holds and that older entries exist', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: descendingPage(100, 9001),
    })
    renderAudit(makeModel({ session: auditSession }))

    const notice = await screen.findByText(/most recent retained/i)
    expect(notice).toHaveTextContent(/100/)
    expect(notice).toHaveTextContent(/Older entries exist beyond this window/i)
  })

  it('says so when the window already reaches the beginning of retained history', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(3, 9001),
    })
    renderAudit(makeModel({ session: auditSession }))

    const notice = await screen.findByText(/most recent retained/i)
    expect(notice).toHaveTextContent(/beginning of retained history/i)
    expect(screen.queryByRole('button', { name: /show older entries/i })).not.toBeInTheDocument()
  })
})

describe('Audit: paging backward', () => {
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
    renderAudit(makeModel({ session: auditSession }))

    await userEvent.click(await screen.findByRole('button', { name: /show older entries/i }))

    // 9001 is the lowest id on the first page; the cursor is that id, not
    // the 100 entries received. A count-derived cursor is exactly what
    // re-reads the same page forever once retention has pruned.
    await waitFor(() =>
      expect(listAudit).toHaveBeenLastCalledWith({ order: 'desc', before: 9001, limit: 100 }),
    )
    expect(await screen.findByText('config/kind-8901')).toBeInTheDocument()
    expect(screen.getByText('config/kind-9100')).toBeInTheDocument()
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
    renderAudit(makeModel({ session: auditSession }))

    await userEvent.click(await screen.findByRole('button', { name: /show older entries/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/still what was loaded/i)
    expect(screen.getByText('config/kind-9100')).toBeInTheDocument()
  })
})

describe('Audit: order is confirmed, never trusted', () => {
  // AuditResponse.order is required by the generated types, but a
  // coordinator older than PR #129 does not know order/id/oldestRetainedId
  // and will not send any of them: this view must never present that
  // response's oldest entries as recent activity.
  it('opens normally once the coordinator confirms desc order', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 9001,
      entries: descendingPage(3, 9001),
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByText('config/kind-9003')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('refuses to render entries as recent activity when order is not echoed', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      entries: [
        { ...makeAuditEntry(), id: undefined as unknown as number },
        { ...makeAuditEntry(), id: undefined as unknown as number },
      ],
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/did not echo an order/i)
    expect(screen.queryByText('config/kind-9100')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /show older entries/i })).not.toBeInTheDocument()
  })

  it('refuses to render entries as recent activity when order is echoed as asc', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'asc',
      oldestRetainedId: null,
      entries: descendingPage(3, 9001),
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/echoed order "asc"/i)
    expect(screen.queryByText('config/kind-9003')).not.toBeInTheDocument()
  })

  it('does not offer another page when the last entry has no usable id', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [{ ...makeAuditEntry(), id: undefined as unknown as number }],
    })
    renderAudit(makeModel({ session: auditSession }))

    await screen.findByText(/most recent retained/i)
    expect(screen.queryByRole('button', { name: /show older entries/i })).not.toBeInTheDocument()
    expect(listAudit).toHaveBeenCalledTimes(1)
  })
})

describe('Audit: failure is not emptiness', () => {
  it('reports a failed fetch as an error, not as an empty log', async () => {
    listAudit.mockRejectedValue(new Error('network is down'))
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.queryByText(/No audit entries retained/i)).not.toBeInTheDocument()
  })

  it('reports an empty log as empty, not as an error', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      order: 'desc',
      oldestRetainedId: null,
      entries: [],
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByText(/No audit entries retained/i)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
