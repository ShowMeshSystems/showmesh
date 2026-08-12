import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { Layout } from './Layout'
import { ModelContext } from './ModelContext'
import type { ConnectionState, Model, SessionResponse } from './types'

afterEach(cleanup)

function model(
  connection: ConnectionState,
  snapshotReceivedAt: number | null,
  session: SessionResponse | null = null,
): Model {
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
    session,
    sessionReceivedAt: session === null ? null : 0,
    sessionFetchFailed: false,
  }
}

// A real, signed-out SessionResponse (not `null`, which SessionPanel
// deliberately renders as nothing at all — see its own header comment).
// Matches the shape SessionPanel.test.tsx's own local fixture uses.
const SIGNED_OUT_SESSION: SessionResponse = {
  serverTime: '2026-08-11T12:00:00.000Z',
  authenticated: false,
  principal: null,
  session: null,
  credentialForm: null,
  scopes: [],
  scopesState: 'not_applicable',
  bootstrapRequired: false,
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

  // ADR-024 decision 5 / OPERATOR-UI section 14: "being signed out is a
  // persistent state, never a modal at the moment of use," and
  // SessionPanel.tsx's own header comment states the design claim this
  // pair of tests exists to hold: the panel "renders unconditionally ...
  // NOT gated on `blockContent`." Every other test in this file uses
  // `session: null`, under which SessionPanel renders nothing at all
  // (its first branch) — so all four of those tests stay green whether
  // or not `<SessionPanel />` is even present in Layout.tsx. Only a
  // fixture with a REAL session can tell the difference, which is why
  // this pair supplies one.
  it('renders the persistent session panel when a real session is present', () => {
    renderLayout(model({ kind: 'live', connectedAt: 0 }, 12345, SIGNED_OUT_SESSION))
    expect(screen.getByText('Signed out on this device.')).toBeInTheDocument()
  })

  it('renders the persistent session panel even while content is blocked (incompatible version) — it must not be gated on blockContent', () => {
    renderLayout(
      model(
        { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
        12345,
        SIGNED_OUT_SESSION,
      ),
    )
    // The main content IS blocked (acceptance criterion 5, covered
    // above)...
    expect(screen.queryByText('underlying view marker')).not.toBeInTheDocument()
    // ...but the session panel is not part of that content and must
    // still be visible: an operator needs to see "you are signed out"
    // even while the rest of the page says "no data yet".
    expect(screen.getByText('Signed out on this device.')).toBeInTheDocument()
  })
})
