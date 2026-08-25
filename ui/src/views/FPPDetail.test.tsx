import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { FPPDetail } from './FPPDetail'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import {
  makeGhostFpp01Instance,
  makeMainInstance,
  makeRemote04Instance,
  FLEET_NOW,
} from '../app/test-support/fppFleetFixtures'
import type { Model, SessionResponse } from '../app/types'
import type { components } from '../api/generated/schema'

type SchemaObservation = components['schemas']['FPPPlaylistEntryObservation']

// The reset-observation-sequence recovery control (FPPResetObservationSequenceControl,
// rendered inside FPPDetail's "Recovery" panel) fetches and deletes through
// '../api', mocked here the same way FPPStopPlaylistControl.test.tsx mocks
// its own write, not faking network behavior (store.test.ts's own job),
// isolating what this view is responsible for: rendering what is about to
// be discarded, gating the destructive path on fpp:command, and rendering
// the OBSERVED post-delete state rather than the bare fact a request went out.
const { listFPPPlaylistEntryObservations, deleteFPPPlaylistEntryObservation } = vi.hoisted(() => ({
  listFPPPlaylistEntryObservations: vi.fn(),
  deleteFPPPlaylistEntryObservation: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listFPPPlaylistEntryObservations, deleteFPPPlaylistEntryObservation }
})

afterEach(() => {
  cleanup()
  listFPPPlaylistEntryObservations.mockReset()
  deleteFPPPlaylistEntryObservation.mockReset()
})

function renderFPPDetail(instanceId: string, model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/fpp/${instanceId}`]}>
        <Routes>
          <Route path="/fpp/:instanceId" element={<FPPDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('FPPDetail', () => {
  it('renders instance summary and drills each observation down through the shared evidence renderer', () => {
    const instance = makeFPPInstance('fpp-1', {
      health: 'degraded',
      lastPollError: 'HTTP 503',
      observations: [
        makeEvidence({ signal: 'fpp.multisync.enabled', value: true, state: 'current' }),
        makeEvidence({
          signal: 'fpp.status.playlist',
          value: null,
          state: 'collection_failed',
          reason: 'HTTP 503 from FPP',
          observedAt: null,
        }),
      ],
    })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance] }))

    expect(screen.getByText('degraded')).toBeInTheDocument()
    expect(screen.getByText('HTTP 503')).toBeInTheDocument()
    expect(screen.getByText('fpp.multisync.enabled')).toBeInTheDocument()
    expect(screen.getByText('fpp.status.playlist')).toBeInTheDocument()
    expect(screen.getByText('collection failed')).toBeInTheDocument()
    expect(screen.getByText('HTTP 503 from FPP')).toBeInTheDocument()
  })

  it('states plainly when no FPP instance matches the route', () => {
    renderFPPDetail('missing', makeModel({ fpp: [makeFPPInstance('fpp-1')] }))
    expect(screen.getByText(/No FPP instance with ID "missing"/)).toBeInTheDocument()
  })

  // A signal matching no known group prefix must still render, in a
  // clearly labelled "other" group -- the ADR-002 lesson one layer up
  // (spec section 6 "Grouping"). This is the exact behavior a reviewer is
  // told to try to break.
  it('still renders a signal matching no known group prefix, under a clearly labelled "Other" heading', () => {
    const instance = makeFPPInstance('fpp-mystery', {
      observations: [
        makeEvidence({ signal: 'fpp.reachable', value: true }),
        makeEvidence({ signal: 'fpp.something_a_future_step_invents', value: 'surprise', state: 'current' }),
      ],
    })
    renderFPPDetail('fpp-mystery', makeModel({ fpp: [instance] }))
    expect(screen.getByRole('heading', { name: 'Other' })).toBeInTheDocument()
    expect(screen.getByText('fpp.something_a_future_step_invents')).toBeInTheDocument()
    expect(screen.getByText('surprise')).toBeInTheDocument()
  })

  // Task readability fix: a signal group (here "Other") renders as a real
  // table, not a stack of unaligned EvidenceValue blocks, matching the
  // config-table/table-scroll pattern this app already uses elsewhere
  // (e.g. views/FPPList.tsx).
  it('renders the Other observation group as a table with Signal/Value column headers and one row per signal', () => {
    const instance = makeFPPInstance('fpp-mystery', {
      observations: [
        makeEvidence({ signal: 'fpp.something_a_future_step_invents', value: 'surprise', state: 'current' }),
        makeEvidence({ signal: 'fpp.another_unrecognized_signal', value: 'also surprising', state: 'current' }),
      ],
    })
    renderFPPDetail('fpp-mystery', makeModel({ fpp: [instance] }))

    const otherHeading = screen.getByRole('heading', { name: 'Other' })
    const otherPanel = otherHeading.closest('section')!
    const table = within(otherPanel).getByRole('table')
    expect(within(table).getByRole('columnheader', { name: 'Signal' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Value' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'fpp.something_a_future_step_invents' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'fpp.another_unrecognized_signal' })).toBeInTheDocument()
    // One row per signal, both real signals present, none dropped.
    expect(within(table).getAllByRole('row')).toHaveLength(3) // header row + 2 signal rows
  })

  it('renders the Warnings group as a table with Signal/Value column headers', () => {
    renderFPPDetail('fpp-main', makeModel({ fpp: [makeMainInstance()], serverTime: FLEET_NOW }))
    const warningsPanel = screen.getByRole('heading', { name: 'Warnings' }).closest('section')!
    const table = within(warningsPanel).getByRole('table')
    expect(within(table).getByRole('columnheader', { name: 'Signal' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: 'Value' })).toBeInTheDocument()
    expect(within(table).getByRole('rowheader', { name: 'fpp.warnings.summary' })).toBeInTheDocument()
    expect(within(table).getByText('A Log Level is set to Debug')).toBeInTheDocument()
  })

  it('groups observations under labelled headings instead of one flat list, for a realistic instance', () => {
    renderFPPDetail('fpp-remote-04', makeModel({ fpp: [makeRemote04Instance()], serverTime: FLEET_NOW }))
    expect(screen.getByRole('heading', { name: 'Playback' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Controller & network' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Sensors' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Platform' })).toBeInTheDocument()
    // "Pixel ports" is its own top-level section (rendered by PortGrid),
    // not a heading inside the generic Observations groups.
    expect(screen.getByRole('heading', { name: 'Pixel ports' })).toBeInTheDocument()
  })

  describe('pixel ports, against the real remote-04 (K16-Max) capture shape', () => {
    it('renders all 16 real output ports and all 32 real smart-receiver positions, each measured 0 mA vs. blind spot distinguishable', () => {
      const { container } = renderFPPDetail(
        'fpp-remote-04',
        makeModel({ fpp: [makeRemote04Instance()], serverTime: FLEET_NOW }),
      )
      expect(screen.getByText(/Output ports \(16\)/)).toBeInTheDocument()
      expect(screen.getByText(/Smart-receiver positions.*\(32\)/)).toBeInTheDocument()

      const measuredCells = container.querySelectorAll('.port-cell--measured')
      const blindCells = container.querySelectorAll('.port-cell--blind_spot')
      expect(measuredCells).toHaveLength(16)
      expect(blindCells).toHaveLength(32)
      // Every real ma on this fleet reads 0 (de-energized display) --
      // reproduced faithfully, and still rendered as a real measurement,
      // never conflated with a blind spot.
      for (const cell of measuredCells) {
        expect(cell.textContent).toContain('0 milliamps')
      }
      for (const cell of blindCells) {
        expect(cell.textContent).not.toMatch(/\b0\b/)
      }
    })
  })

  it('states plainly that fpp-player reports no pixel output ports (a fact, not an error), matching its real empty-array capture', () => {
    renderFPPDetail('fpp-main', makeModel({ fpp: [makeMainInstance()], serverTime: FLEET_NOW }))
    expect(screen.getByText('This host reports no pixel output ports.')).toBeInTheDocument()
  })

  it('surfaces fpp.warnings.summary prominently, without it appearing on the FPPHealthBadge', () => {
    renderFPPDetail('fpp-main', makeModel({ fpp: [makeMainInstance()], serverTime: FLEET_NOW }))
    const warningsPanel = screen.getByRole('heading', { name: 'Warnings' }).closest('section')
    expect(warningsPanel).not.toBeNull()
    expect(within(warningsPanel!).getByText('A Log Level is set to Debug')).toBeInTheDocument()
    // Health badge is unaffected by the presence of warnings -- still
    // exactly what the fixture's `health` field says.
    expect(screen.getByText('healthy')).toBeInTheDocument()
  })

  // Spec section 4.2 / this build's best acceptance demonstration: a
  // retained-only MQTT host's evidence must read `unknown_age`
  // indefinitely, never `current`. This is presentation of the
  // coordinator's own state -- FPPDetail renders it, it does not compute
  // it -- but a UI that dropped the unknown_age handling and rendered a
  // present value as "current" would be exactly the regression this
  // fixture exists to catch.
  it('renders every observation on the fpp-ghost ghost as age-unknown, never as current', () => {
    renderFPPDetail('fpp-01-ghost', makeModel({ fpp: [makeGhostFpp01Instance()], serverTime: FLEET_NOW }))
    const ageUnknownBadges = screen.getAllByText('age unknown')
    expect(ageUnknownBadges.length).toBeGreaterThan(0)
    expect(screen.queryByText('current')).not.toBeInTheDocument()
  })
})

const RECOVERY_NOW = '2026-08-12T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: RECOVERY_NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: RECOVERY_NOW },
    credentialForm: 'session',
    scopes: ['fpp:command'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function makeStoredObservation(overrides: Partial<SchemaObservation> = {}): SchemaObservation {
  return {
    instanceUuid: 'uuid-fpp-1',
    endpointId: 'fpp-1',
    schemaVersion: 1,
    sequence: 42,
    playlistName: 'Main Show',
    section: 'mainPlaylist',
    position: 3,
    entryKey: 'mainPlaylist:3',
    action: 'playing',
    coalescedSincePreviousAcknowledged: 0,
    observedAt: RECOVERY_NOW,
    receivedAt: RECOVERY_NOW,
    ...overrides,
  }
}

function recoveryPanel() {
  return screen.getByRole('heading', { name: 'Reset observation sequence' }).closest('section')!
}

// FPPResetObservationSequenceControl (components/FPPResetObservationSequenceControl.tsx),
// rendered inside FPPDetail's own "Recovery" panel: the show-night path
// to clear a wedged sequence anchor (TRACK-H-H2-SPEC.md §5.1), previously
// reachable only from `showmeshctl fpp reset-observation-sequence --confirm`.
describe("FPPDetail's reset-observation-sequence recovery control", () => {
  it('renders the stored observation about to be discarded', async () => {
    listFPPPlaylistEntryObservations.mockResolvedValue({
      observations: [makeStoredObservation()],
      serverTime: RECOVERY_NOW,
    })
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    await waitFor(() => expect(within(recoveryPanel()).getByText('mainPlaylist:3')).toBeInTheDocument())
    expect(within(recoveryPanel()).getByText('42')).toBeInTheDocument()
    expect(within(recoveryPanel()).getByText('playing')).toBeInTheDocument()
  })

  it('requires a second, distinct click before dispatching the delete', async () => {
    const user = userEvent.setup()
    listFPPPlaylistEntryObservations.mockResolvedValue({
      observations: [makeStoredObservation()],
      serverTime: RECOVERY_NOW,
    })
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    const armButton = await within(recoveryPanel()).findByRole('button', { name: 'Clear stored observation…' })
    await user.click(armButton)

    expect(screen.getByRole('alertdialog', { name: 'Confirm clearing stored observation' })).toBeInTheDocument()
    expect(deleteFPPPlaylistEntryObservation).not.toHaveBeenCalled()
  })

  it('dispatches the confirmed delete to the right instance and renders the observed post-delete state', async () => {
    const user = userEvent.setup()
    listFPPPlaylistEntryObservations
      .mockResolvedValueOnce({ observations: [makeStoredObservation()], serverTime: RECOVERY_NOW })
      .mockResolvedValueOnce({ observations: [], serverTime: RECOVERY_NOW })
    deleteFPPPlaylistEntryObservation.mockResolvedValue(undefined)
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    await user.click(await within(recoveryPanel()).findByRole('button', { name: 'Clear stored observation…' }))
    await user.click(screen.getByRole('button', { name: 'Confirm: clear stored observation' }))

    expect(deleteFPPPlaylistEntryObservation).toHaveBeenCalledWith('uuid-fpp-1')
    await waitFor(() =>
      expect(
        within(recoveryPanel()).getByText('Cleared: no stored observation remains for this instance.'),
      ).toBeInTheDocument(),
    )
  })

  it('renders the refusal reason on a failed delete, and never claims the observation is gone', async () => {
    const user = userEvent.setup()
    listFPPPlaylistEntryObservations.mockResolvedValue({
      observations: [makeStoredObservation()],
      serverTime: RECOVERY_NOW,
    })
    deleteFPPPlaylistEntryObservation.mockRejectedValue(new Error('coordinator refused'))
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    await user.click(await within(recoveryPanel()).findByRole('button', { name: 'Clear stored observation…' }))
    await user.click(screen.getByRole('button', { name: 'Confirm: clear stored observation' }))

    const alert = await within(recoveryPanel()).findByRole('alert')
    expect(alert.textContent).toContain('coordinator refused')
    expect(within(recoveryPanel()).queryByText(/^Cleared:/)).not.toBeInTheDocument()
    // Delete was refused: the last known observed state still stands.
    expect(within(recoveryPanel()).getByText('mainPlaylist:3')).toBeInTheDocument()
  })

  it('renders the control unavailable, with no way to arm it, when the principal lacks fpp:command', async () => {
    listFPPPlaylistEntryObservations.mockResolvedValue({
      observations: [makeStoredObservation()],
      serverTime: RECOVERY_NOW,
    })
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn({ scopes: ['node:read'] }) }))

    await waitFor(() => expect(within(recoveryPanel()).getByText('mainPlaylist:3')).toBeInTheDocument())
    expect(within(recoveryPanel()).queryByRole('button', { name: 'Clear stored observation…' })).not.toBeInTheDocument()
    expect(within(recoveryPanel()).getByText(/Requires the/)).toBeInTheDocument()
  })

  it('renders sensibly, with no armable destructive control, for an instance with no stored observation', async () => {
    listFPPPlaylistEntryObservations.mockResolvedValue({ observations: [], serverTime: RECOVERY_NOW })
    const instance = makeFPPInstance('fpp-1', { instanceUuid: 'uuid-fpp-1' })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    await waitFor(() =>
      expect(
        within(recoveryPanel()).getByText('No stored observation is currently held for this instance.'),
      ).toBeInTheDocument(),
    )
    expect(within(recoveryPanel()).getByRole('button', { name: 'Clear stored observation…' })).toBeDisabled()
  })

  it('renders a plain statement, not a broken panel, when the instance has never reported an instance UUID', () => {
    const instance = makeFPPInstance('fpp-1')
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance], session: signedIn() }))

    expect(within(recoveryPanel()).getByText(/has not yet reported an instance UUID/)).toBeInTheDocument()
    expect(listFPPPlaylistEntryObservations).not.toHaveBeenCalled()
  })
})
