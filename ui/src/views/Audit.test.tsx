import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Audit } from './Audit'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { AuditEntry, Model } from '../app/types'

// Track G seam G-8: the audit view. GET /audit pages from the OLDEST
// entry, so this view walks the cursor forward to the end of retained
// history (in MAX_LIMIT-sized pages) before rendering, so the log opens on
// the most recent activity rather than the oldest retained window. The
// render window is a separate, smaller bound: only the newest 200 fetched
// entries are drawn as rows at a time (widened by "Show more"), so a large
// walked history never turns into an unbounded DOM.
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
    // The cap notice is about the walk, not the render window: only the
    // bounded window's worth of rows should exist, not all 10,000 collected.
    expect(screen.getAllByRole('row').length - 1).toBeLessThanOrEqual(200)
  })

  it('renders only a bounded window of rows and widens it with Show more', async () => {
    listAudit.mockResolvedValueOnce({
      serverTime: '2026-08-17T00:00:00Z',
      entries: Array.from({ length: 300 }, (_, i) => makeAuditEntry({ target: `config/entry-${i}` })),
    })
    renderAudit(makeModel({ session: auditSession }))

    await screen.findByText('config/entry-299') // newest of 300, shown first
    expect(screen.getAllByRole('row').length - 1).toBe(200)
    expect(screen.getByText(/showing the most recent 200 entries; 100 older fetched entries/i)).toBeInTheDocument()
    expect(screen.queryByText('config/entry-99')).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /show more/i }))

    await screen.findByText('config/entry-99')
    expect(screen.getAllByRole('row').length - 1).toBe(300)
    expect(screen.getByText(/showing all 300 entries, most recent first/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /show more/i })).not.toBeInTheDocument()
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
