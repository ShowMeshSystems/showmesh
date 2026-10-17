import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    const summaryCell = cells[2]
    const stateCell = cells[1]
    const sourceCell = cells[3]
    expect(stateCell?.querySelector('.sm-status')).not.toBeNull()
    expect(stateCell?.textContent).toContain('Critical')
    expect(summaryCell?.querySelector('.sm-status')).toBeNull()
    // The source is who reported it, not a health verdict about that reporter.
    expect(sourceCell?.querySelector('.sm-status')).toBeNull()
    expect(sourceCell?.textContent).toBe('coordinator')
  })

  it('shows no state word on an ordinary informational event', () => {
    renderScreen({ events: [event({ severity: 'informational' })] })
    const cells = [...document.querySelectorAll('tbody tr td')]
    expect(cells[1]?.querySelector('.sm-status')).toBeNull()
  })

  it('keeps What happened in the same column whether or not a row has a state', () => {
    renderScreen({ events: [event({ severity: 'critical', seq: 3 as never }), event({ severity: 'informational', summary: 'Plain note' })] })
    const rows = [...document.querySelectorAll('tbody tr')]
    expect(rows).toHaveLength(2)
    for (const row of rows) expect(row.querySelectorAll('td')).toHaveLength(4)
    const summaries = rows.map((row) => row.querySelectorAll('td')[2]?.textContent)
    expect(summaries).toContain('Plain note')
  })

  it('loads older operator actions with the audit cursor and appends them', async () => {
    listAudit
      .mockResolvedValueOnce({ serverTime: '', order: 'desc', oldestRetainedId: 1, entries: [auditEntry({ id: 5, action: 'first.page' })] })
      .mockResolvedValueOnce({ serverTime: '', order: 'desc', oldestRetainedId: 1, entries: [auditEntry({ id: 1, action: 'older.page' })] })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await screen.findByText(/first.page/)
    fireEvent.click(screen.getByRole('button', { name: 'Load older operator actions' }))
    await screen.findByText(/older.page/)
    expect(listAudit).toHaveBeenLastCalledWith({ order: 'desc', before: 5, limit: 100 })
    expect(screen.getByText(/first.page/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load older operator actions' })).not.toBeInTheDocument()
  })

  it('offers no load more when the oldest retained entry is already shown', async () => {
    listAudit.mockResolvedValue({ serverTime: '', order: 'desc', oldestRetainedId: 5, entries: [auditEntry({ id: 5 })] })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await screen.findByText(/night.start-night/)
    expect(screen.queryByRole('button', { name: 'Load older operator actions' })).not.toBeInTheDocument()
  })

  it('filters by type and keeps system events visible without audit permission', async () => {
    listAudit.mockResolvedValue({ serverTime: '', order: 'desc', oldestRetainedId: 1, entries: [auditEntry()] })
    renderScreen({ events: [event()], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    await screen.findByText(/night.start-night/)
    fireEvent.change(screen.getByLabelText('Show'), { target: { value: 'event' } })
    expect(screen.queryByText(/night.start-night/)).not.toBeInTheDocument()
    expect(screen.getByText(/Projector strike refused/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Show'), { target: { value: 'audit' } })
    expect(screen.getByText(/night.start-night/)).toBeInTheDocument()
    expect(screen.queryByText(/Projector strike refused/)).not.toBeInTheDocument()
  })

  it('filters by text over what happened and says when nothing matches', () => {
    renderScreen({ events: [event(), event({ seq: 9 as never, summary: 'Fan speed normal' })], snapshotReceivedAt: Date.now(), session: null })
    fireEvent.change(screen.getByLabelText('Search what happened'), { target: { value: 'fan' } })
    expect(screen.getByText('Fan speed normal')).toBeInTheDocument()
    expect(screen.queryByText(/Projector strike/)).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Search what happened'), { target: { value: 'zzz' } })
    expect(screen.getByText(/No loaded activity matches/)).toBeInTheDocument()
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

  it('names a cue activation’s show action outcomes on the activation’s own row', async () => {
    listAudit.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      order: 'desc',
      oldestRetainedId: 1,
      entries: [
        auditEntry({
          action: 'cue.activate',
          target: 'node:render-01',
          kind: 'outcome',
          outcome: 'confirmed',
          params: {
            cueId: 'song-one',
            actions: [
              { actionId: 'column-1', label: 'Song one column', outcome: 'confirmed', outcomeState: 'current' },
              { actionId: 'blackout-now', outcome: 'failed', outcomeReason: 'Resolume did not answer' },
            ],
          },
        } as Partial<AuditEntry>),
      ],
    })
    renderScreen({ events: [], snapshotReceivedAt: Date.now(), session: session(['audit:read']) })
    expect(await screen.findByText(/Show actions: Song one column confirmed, current; blackout-now failed: Resolume did not answer\./)).toBeInTheDocument()
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
