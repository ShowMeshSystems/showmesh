import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Events } from './Events'
import { ModelContext } from '../app/ModelContext'
import { makeEvent, makeModel } from '../app/test-support/fixtures'
import type { Model } from '../app/types'
import { formatAbsolute } from '../app/time'

afterEach(cleanup)

function renderEvents(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Events />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Events', () => {
  // spec section 6.4: gap:true is surfaced as permanently lost history,
  // never as something a retry could recover.
  it('surfaces gap:true as permanently lost history, with the oldest retained sequence', () => {
    renderEvents(makeModel({ eventsGap: true, oldestRetainedSeq: 42 }))
    expect(
      screen.getByText(/Event history before this point has been permanently lost to retention/),
    ).toBeInTheDocument()
    expect(screen.getByText(/oldest event still retained has sequence 42/)).toBeInTheDocument()
    expect(screen.getByText(/Reconnecting will not recover it/)).toBeInTheDocument()
  })

  it('does not render the gap notice when gap is false', () => {
    renderEvents(makeModel({ eventsGap: false }))
    expect(screen.queryByText(/permanently lost to retention/)).not.toBeInTheDocument()
  })

  // occurredAt: null means the occurrence time is genuinely unknown -- it
  // must render as that, never silently substituted with recordedAt.
  it('renders occurredAt: null as an unknown occurrence time, distinct from recordedAt', () => {
    const event = makeEvent(1, { occurredAt: null, recordedAt: '2026-08-11T12:05:00.000Z', summary: 'test event' })
    renderEvents(makeModel({ events: [event] }))
    expect(screen.getByText('occurrence time unknown')).toBeInTheDocument()
  })

  it('renders events severity-distinguished with the resource reference', () => {
    const events = [
      makeEvent(2, { severity: 'critical', summary: 'critical thing happened', resource: { kind: 'fpp', id: 'fpp-1' } }),
      makeEvent(1, { severity: 'informational', summary: 'informational thing happened', resource: { kind: 'node', id: 'node-1' } }),
    ]
    renderEvents(makeModel({ events }))
    expect(screen.getByText('critical')).toBeInTheDocument()
    expect(screen.getByText('informational')).toBeInTheDocument()
    expect(screen.getByText('fpp: fpp-1')).toBeInTheDocument()
    expect(screen.getByText('node: node-1')).toBeInTheDocument()
  })

  it('states plainly when no events have been recorded', () => {
    renderEvents(makeModel({ events: [] }))
    expect(screen.getByText('No events recorded yet.')).toBeInTheDocument()
  })

  // One row per event (real table), so the operator scanning for what
  // just changed gets aligned columns instead of stacked panels.
  it('renders as a table with one row per event and the expected column headers', () => {
    const events = [
      makeEvent(2, { summary: 'first event' }),
      makeEvent(1, { summary: 'second event' }),
    ]
    renderEvents(makeModel({ events }))
    expect(screen.getByRole('table')).toBeInTheDocument()
    const headers = screen.getAllByRole('columnheader').map((h) => h.textContent)
    expect(headers).toEqual(['Severity', 'Message', 'Subject', 'Occurred', 'Recorded', 'Category', 'Source'])
    // Header row plus one row per event.
    expect(screen.getAllByRole('row')).toHaveLength(events.length + 1)
  })

  // occurredAt: null must still render its own row and its own "occurrence
  // time unknown" cell, distinct from a populated recordedAt in the same row.
  it('renders an event with an unknown occurrence time as its own table row', () => {
    const event = makeEvent(1, { occurredAt: null, recordedAt: '2026-08-11T12:05:00.000Z', summary: 'unknown occurrence' })
    renderEvents(makeModel({ events: [event] }))
    const row = screen.getByText('unknown occurrence').closest('tr')
    expect(row).not.toBeNull()
    expect(row).toHaveTextContent('occurrence time unknown')
    expect(row).toHaveTextContent(formatAbsolute('2026-08-11T12:05:00.000Z'))
  })
})
