import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Events } from './Events'
import { ModelContext } from '../app/ModelContext'
import { makeEvent, makeModel } from '../app/test-support/fixtures'
import type { Model } from '../app/types'
import { formatAbsolute } from '../app/time'

// Monitor / Activity (owner ruling: Events.tsx + Audit.tsx merged into
// ONE stream). Every assertion this file used to make about a bare
// events-only table now expects the merged shape: Time / What happened /
// Source columns up front, with severity/subject/category reachable
// behind each row's own "Detail" disclosure (aria-expanded) rather than
// inline — nothing measured here was dropped, only relocated, matching
// the mock's own column collapse.
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

describe('Events (Monitor / Activity)', () => {
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

  it('renders occurredAt: null as an unknown occurrence time behind the row detail, distinct from recordedAt', async () => {
    const user = userEvent.setup()
    const event = makeEvent(1, { occurredAt: null, recordedAt: '2026-08-11T12:05:00.000Z', summary: 'test event' })
    renderEvents(makeModel({ events: [event] }))
    await user.click(screen.getByRole('button', { name: 'Detail' }))
    expect(screen.getByText('occurrence time unknown')).toBeInTheDocument()
    expect(screen.getByText(formatAbsolute('2026-08-11T12:05:00.000Z'))).toBeInTheDocument()
  })

  it('renders events severity-distinguished with the resource reference, reachable behind the row detail', async () => {
    const user = userEvent.setup()
    const events = [
      makeEvent(2, { severity: 'critical', summary: 'critical thing happened', resource: { kind: 'fpp', id: 'fpp-1' } }),
      makeEvent(1, { severity: 'informational', summary: 'informational thing happened', resource: { kind: 'node', id: 'node-1' } }),
    ]
    renderEvents(makeModel({ events }))

    const criticalRow = screen.getByText('critical thing happened').closest('tr')!
    await user.click(within(criticalRow).getByRole('button', { name: 'Detail' }))
    expect(screen.getByText('critical')).toBeInTheDocument()
    expect(screen.getByText('fpp: fpp-1')).toBeInTheDocument()

    const infoRow = screen.getByText('informational thing happened').closest('tr')!
    await user.click(within(infoRow).getByRole('button', { name: 'Detail' }))
    expect(screen.getByText('informational')).toBeInTheDocument()
    expect(screen.getByText('node: node-1')).toBeInTheDocument()
  })

  it('states plainly when no activity has been recorded', () => {
    renderEvents(makeModel({ events: [] }))
    expect(screen.getByText('No activity recorded yet.')).toBeInTheDocument()
  })

  it('renders as a table with one row per event and the merged column headers', () => {
    const events = [
      makeEvent(2, { summary: 'first event' }),
      makeEvent(1, { summary: 'second event' }),
    ]
    renderEvents(makeModel({ events }))
    const table = screen.getByRole('table', { name: 'Activity' })
    expect(table).toBeInTheDocument()
    const headers = screen.getAllByRole('columnheader').map((h) => h.textContent)
    expect(headers).toEqual(['Time', 'What happened', 'Source', ''])
    // Header row plus one row per event (each event has a possible, but
    // collapsed by default, detail row -- collapsed rows render nothing).
    expect(screen.getAllByRole('row')).toHaveLength(events.length + 1)
  })

  it('states that audit rows are withheld, without blanking the system-event rows, when audit:read is not held', () => {
    renderEvents(makeModel({ events: [makeEvent(1, { summary: 'a system event' })], session: null }))
    expect(screen.getByText('a system event')).toBeInTheDocument()
    expect(screen.getByText(/Operator-action \(audit\) rows are not shown/)).toBeInTheDocument()
  })
})
