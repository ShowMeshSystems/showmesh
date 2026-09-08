import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AuditEntry, Event, Model, SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const listAudit = vi.fn()
vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, listAudit: (...args: unknown[]) => listAudit(...args) }
})

const { MonitorActivity } = await import('./MonitorActivity')

function session(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-28T21:07:00Z',
    authenticated: true,
    principal: { id: 'p', name: 'erbartos', role: 'operator', disabled: false },
    session: null,
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function auditEntry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 1,
    timestamp: '2026-08-28T21:02:14Z',
    principalId: 'p',
    principalName: 'erbartos',
    form: 'session',
    credentialId: 'c',
    clientAddr: '10.0.0.1',
    action: 'night.start-night',
    target: 'night/winter-ridge',
    params: {},
    idempotencyKey: 'k',
    kind: 'dispatch',
    commandId: 'cmd',
    outcome: '',
    outcomeState: '',
    outcomeReason: '',
    ...overrides,
  } as unknown as AuditEntry
}

function event(overrides: Partial<Event> = {}): Event {
  return {
    seq: 2 as never,
    recordedAt: '2026-08-28T21:02:20Z',
    occurredAt: '2026-08-28T21:02:20Z',
    source: 'coordinator',
    resource: { kind: 'node', id: 'n' },
    category: 'control_plane',
    severity: 'critical',
    summary: 'Projector strike refused',
    details: {},
    correlationId: null,
    ...overrides,
  } as unknown as Event
}

function renderScreen(model: Partial<Model>) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model, serverTime: '2026-08-28T21:07:00Z', serverTimeReceivedAt: Date.now() }}>
      <MemoryRouter>
        <MonitorActivity />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor · Activity', () => {
  afterEach(() => {
    cleanup()
    listAudit.mockReset()
  })

  it('names the facet heading', () => {
    listAudit.mockReturnValue(new Promise(() => {}))
    renderScreen({ session: session(['audit:read']) })
    expect(screen.getByRole('heading', { level: 2, name: 'Activity' })).toBeInTheDocument()
  })

  it('renders system events without needing any scope', () => {
    listAudit.mockReturnValue(new Promise(() => {}))
    renderScreen({ events: [event()], snapshotReceivedAt: Date.now(), session: null })
    expect(screen.getByText(/Projector strike refused/)).toBeInTheDocument()
  })

  it('states the refusal, disabled from the operator-action portion, when the scope is missing', () => {
    renderScreen({ events: [event()], snapshotReceivedAt: Date.now(), session: session([]) })
    expect(screen.getByText('Operator actions not shown')).toBeInTheDocument()
    expect(screen.getByText(/does not include "audit:read"/)).toBeInTheDocument()
    expect(listAudit).not.toHaveBeenCalled()
  })

  it('merges operator actions into the stream once the scope is granted', async () => {
    listAudit.mockResolvedValue({ serverTime: '2026-08-28T21:07:00Z', order: 'desc', oldestRetainedId: 1, entries: [auditEntry()] })
    renderScreen({ events: [event()], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText(/night.start-night/)).toBeInTheDocument())
    expect(screen.getByText(/Projector strike refused/)).toBeInTheDocument()
  })

  it('says a retention gap out loud when the stream has one', () => {
    listAudit.mockReturnValue(new Promise(() => {}))
    renderScreen({ events: [event()], eventsGap: true, oldestRetainedSeq: 5, snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    expect(screen.getByText(/permanently lost to retention/)).toBeInTheDocument()
    expect(screen.getByText(/seq 5/)).toBeInTheDocument()
  })

  it('puts the severity on what happened, never on the source', () => {
    renderScreen({ events: [event({ severity: 'critical' })] })
    const cells = [...document.querySelectorAll('tbody tr td')]
    const summaryCell = cells[1]
    const sourceCell = cells[2]
    expect(summaryCell?.querySelector('.sm-status')).not.toBeNull()
    expect(summaryCell?.textContent).toContain('Critical')
    // The source is who reported it, not a health verdict about that reporter.
    expect(sourceCell?.querySelector('.sm-status')).toBeNull()
    expect(sourceCell?.textContent).toBe('coordinator')
  })

  it('shows no state word on an ordinary informational event', () => {
    renderScreen({ events: [event({ severity: 'informational' })] })
    const cells = [...document.querySelectorAll('tbody tr td')]
    expect(cells[1]?.querySelector('.sm-status')).toBeNull()
  })

  it('never renders a retention gap when the stream reports none', () => {
    listAudit.mockReturnValue(new Promise(() => {}))
    renderScreen({ events: [event()], eventsGap: false, snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    expect(screen.queryByText(/permanently lost to retention/)).not.toBeInTheDocument()
  })

  it('reads a refused action as bad, worded "Refused", never a green check', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [auditEntry({ outcome: 'refused', outcomeState: '', outcomeReason: 'Current session does not exist' })],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText('Refused')).toBeInTheDocument())
    const status = screen.getByText('Refused').closest('.sm-status')
    expect(status).not.toBeNull()
    expect(status?.className).toContain('sm-status--bad')
    expect(status?.className).not.toContain('sm-status--good')
  })

  it('reads a failed action as bad, worded "Failed"', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [auditEntry({ outcome: 'failed', outcomeState: '', outcomeReason: 'Node did not answer' })],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText('Failed')).toBeInTheDocument())
    expect(screen.getByText('Failed').closest('.sm-status')?.className).toContain('sm-status--bad')
  })

  it('reads an unconfirmable action as warn, never good or bad', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [auditEntry({ outcome: 'unconfirmable', outcomeState: '', outcomeReason: 'Clip was already playing' })],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText('Unconfirmable')).toBeInTheDocument())
    expect(screen.getByText('Unconfirmable').closest('.sm-status')?.className).toContain('sm-status--warn')
  })

  it('reads a confirmed action as good, worded "Confirmed", only for an actually confirmed outcome', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [auditEntry({ outcome: 'confirmed', outcomeState: 'current', outcomeReason: 'Surface applied' })],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText('Confirmed')).toBeInTheDocument())
    expect(screen.getByText('Confirmed').closest('.sm-status')?.className).toContain('sm-status--good')
  })

  it('never colors a dispatch/replay entry from a stray outcomeState (no outcome word to report)', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [auditEntry({ kind: 'dispatch', outcome: '', outcomeState: 'current', outcomeReason: '' })],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await waitFor(() => expect(screen.getByText(/night.start-night/)).toBeInTheDocument())
    expect(screen.queryByText('Current')).not.toBeInTheDocument()
  })
})
