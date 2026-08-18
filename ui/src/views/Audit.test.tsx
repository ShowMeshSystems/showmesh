import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Audit } from './Audit'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { AuditEntry, Model } from '../app/types'

// Track G seam G-8: the audit view. GET /audit pages from the OLDEST
// entry, so a full window is the oldest window the API exposes, not the
// tail — the load-bearing case here is that a full window states that out
// loud (ADR-020: absent evidence is stated, never omitted) instead of
// presenting week-old history as the audit log.
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

describe('Audit: full-window notice', () => {
  it('states that the window is the oldest the API exposes when the fetch fills the requested limit', async () => {
    // The default page size is 100 — return exactly that many, the
    // indistinguishable-from-truncated case.
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      entries: Array.from({ length: 100 }, (_, i) =>
        makeAuditEntry({ target: `config/kind-${i}` }),
      ),
    })
    renderAudit(makeModel({ session: auditSession }))

    const notice = await screen.findByText(/this window is full/i)
    expect(notice).toHaveTextContent(/oldest/i)
    expect(notice).toHaveTextContent(/newer entries beyond this window may exist/i)
    expect(listAudit).toHaveBeenCalledWith({ since: 0, limit: 100 })
  })

  it('renders no full-window notice when the fetch comes back under the requested limit', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-17T00:00:00Z',
      entries: [makeAuditEntry()],
    })
    renderAudit(makeModel({ session: auditSession }))

    expect(await screen.findByText('config/fpp.endpoints')).toBeInTheDocument()
    expect(screen.queryByText(/this window is full/i)).not.toBeInTheDocument()
  })
})
