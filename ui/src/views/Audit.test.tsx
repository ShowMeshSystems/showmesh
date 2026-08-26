import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Audit } from './Audit'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { AuditEntry, Model } from '../app/types'

// Track G seam G-8: the audit view. GET /audit pages from the OLDEST
// entry, so this view walks the cursor forward to the end of retained
// history (in MAX_LIMIT-sized pages) before rendering, so the log opens on
// the most recent activity rather than the oldest retained window.
const { listAudit } = vi.hoisted(() => ({ listAudit: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listAudit }
})

function makeAuditEntry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
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

describe('Audit: newest-first load', () => {
  it('renders entries newest first', async () => {
    listAudit.mockResolvedValueOnce({
      serverTime: '2026-08-17T00:00:00Z',
      entries: [
        makeAuditEntry({ target: 'config/first', timestamp: '2026-08-17T00:00:00Z' }),
        makeAuditEntry({ target: 'config/second', timestamp: '2026-08-17T00:01:00Z' }),
      ],
    })
    renderAudit(makeModel({ session: auditSession }))

    await screen.findByText('config/second')
    const rows = screen.getAllByRole('row').slice(1) // drop the header row
    expect(rows[0]).toHaveTextContent('config/second')
    expect(rows[1]).toHaveTextContent('config/first')
  })

  it('follows the cursor across multiple pages and stops on a short response', async () => {
    listAudit
      .mockResolvedValueOnce({
        serverTime: '2026-08-17T00:00:00Z',
        entries: Array.from({ length: 500 }, (_, i) => makeAuditEntry({ target: `config/page1-${i}` })),
      })
      .mockResolvedValueOnce({
        serverTime: '2026-08-17T00:00:00Z',
        entries: [makeAuditEntry({ target: 'config/page2-only' })],
      })
    renderAudit(makeModel({ session: auditSession }))

    await screen.findByText('config/page2-only')
    expect(listAudit).toHaveBeenCalledTimes(2)
    expect(listAudit).toHaveBeenNthCalledWith(1, { since: 0, limit: 500 })
    expect(listAudit).toHaveBeenNthCalledWith(2, { since: 500, limit: 500 })
    expect(screen.queryByText(/stopped after/i)).not.toBeInTheDocument()
  })

  it('honours the request cap and says so when it is reached before the end', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      entries: Array.from({ length: 500 }, (_, i) => makeAuditEntry({ target: `config/full-${i}` })),
    })
    renderAudit(makeModel({ session: auditSession }))

    await waitFor(() => expect(listAudit).toHaveBeenCalledTimes(20))
    const notice = await screen.findByText(/stopped after 20 requests/i)
    expect(notice).toHaveTextContent(/10000/)
    expect(notice).toHaveTextContent(/not the most recent activity/i)
  })

  it('renders a fetch failure as a failure, not an empty log', async () => {
    listAudit.mockRejectedValue(new Error('coordinator unreachable'))
    renderAudit(makeModel({ session: auditSession }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/coordinator unreachable/i)
    expect(screen.queryByText(/no audit entries retained/i)).not.toBeInTheDocument()
  })

  it('renders an empty log as empty', async () => {
    listAudit.mockResolvedValue({ serverTime: '2026-08-17T00:00:00Z', entries: [] })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByText(/no audit entries retained/i)).toBeInTheDocument()
  })
})
