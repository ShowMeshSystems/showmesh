import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
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
import type { Model } from '../app/types'

afterEach(cleanup)

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
