import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { Layout } from './Layout'
import { ModelContext } from './ModelContext'
import type { ConnectionState, Model } from './types'

afterEach(cleanup)

function model(connection: ConnectionState, snapshotReceivedAt: number | null): Model {
  return {
    connection,
    serverTime: '2026-08-11T12:00:00.000Z',
    clockSkewMs: 0,
    snapshotReceivedAt,
    serverTimeReceivedAt: snapshotReceivedAt,
    nodes: [],
    fpp: [],
    collectors: [],
    events: [],
    eventsGap: false,
    oldestRetainedSeq: null,
    session: null,
    sessionReceivedAt: null,
    sessionFetchFailed: false,
  }
}

function renderLayout(m: Model) {
  return render(
    <ModelContext.Provider value={m}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route element={<Layout onSubmitToken={() => {}} />}>
            <Route index element={<p>underlying view marker</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Layout', () => {
  // Acceptance criterion 5: an incompatible coordinator produces the
  // explicit error, never a partial render of the normal views.
  it('does not render the underlying view while the API version is incompatible', () => {
    renderLayout(
      model({ kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' }, 12345),
    )
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('does not render the underlying view before any snapshot has ever arrived', () => {
    renderLayout(model({ kind: 'connecting' }, null))
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
  })

  it('renders the underlying view once data has been received, even while reconnecting', () => {
    renderLayout(
      model({ kind: 'reconnecting', attempt: 2, nextAttemptAt: 0, lastError: 'boom' }, 12345),
    )
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })

  it('renders the underlying view normally while live', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345))
    expect(screen.getByText('underlying view marker')).toBeInTheDocument()
  })
})
