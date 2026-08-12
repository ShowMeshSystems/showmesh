import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { FPPList } from './FPPList'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import { makeMainInstance, makeRemote01Instance, makeRemote04Instance } from '../app/test-support/fppFleetFixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderFPPList(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <FPPList />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('FPPList', () => {
  it('lists each configured instance with its health and endpoint', () => {
    const fpp = [
      makeFPPInstance('fpp-front', { health: 'healthy', endpoint: 'http://fpp-front.local' }),
      makeFPPInstance('fpp-back', { health: 'unknown', endpoint: 'http://fpp-back.local', lastPollError: 'timed out' }),
    ]
    renderFPPList(makeModel({ fpp }))

    expect(screen.getByText('fpp-front')).toBeInTheDocument()
    expect(screen.getByText('http://fpp-front.local')).toBeInTheDocument()
    expect(screen.getByText('healthy')).toBeInTheDocument()

    expect(screen.getByText('fpp-back')).toBeInTheDocument()
    // FPPList already handled unknown health correctly before this build
    // (per the D2 finding); this pins that it still does.
    expect(screen.getByText('unknown')).toBeInTheDocument()
    expect(screen.getByText('timed out')).toBeInTheDocument()
  })

  it('states plainly when no FPP instance is configured', () => {
    renderFPPList(makeModel({ fpp: [] }))
    expect(screen.getByText('No FPP instances are configured on this coordinator.')).toBeInTheDocument()
  })

  it("shows each instance's fpp.version", () => {
    const fpp = [
      makeFPPInstance('fpp-a', { observations: [makeEvidence({ signal: 'fpp.version', value: '9.4' })] }),
    ]
    renderFPPList(makeModel({ fpp }))
    expect(screen.getByText('version: 9.4')).toBeInTheDocument()
  })

  it('states "not collected" for an instance whose fpp.version has not been observed, rather than a blank field', () => {
    renderFPPList(makeModel({ fpp: [makeFPPInstance('fpp-a', { observations: [] })] }))
    expect(screen.getByText('version: not collected')).toBeInTheDocument()
  })

  // Step 5 review finding 6, at the component level (fppSignals.test.ts
  // covers summarizeFleetVersions directly): a version pulled from a
  // retained/unknown-age source must not render as a bare, confident
  // string with no state marker -- FleetSignalBadge carries the state's
  // icon/tone alongside the value, so the version TEXT is unchanged
  // ("version: 9.2") but the badge's tone/icon differ from a current
  // reading, exactly like FleetSignalBadge/PortGrid's other current-vs-
  // unknown_age cells already distinguish.
  it("carries fpp.version's own state, never rendering an unknown_age value with the current-state tone", () => {
    const fpp = [
      makeFPPInstance('fpp-ghost', {
        observations: [
          makeEvidence({
            signal: 'fpp.version',
            value: '9.2',
            state: 'unknown_age',
            observedAt: null,
            reason: 'retained MQTT delivery of unknown age',
          }),
        ],
      }),
    ]
    renderFPPList(makeModel({ fpp }))
    const versionText = screen.getByText('version: 9.2')
    expect(versionText).toBeInTheDocument()
    // The version's OWN badge (not the instance's separate FPPHealthBadge,
    // which legitimately stays "good" per the fixture's default health)
    // carries the unknown_age tone, never the "current"/healthy one.
    const versionBadge = versionText.closest('.status-badge')
    expect(versionBadge?.className).toContain('status-badge--unknown')
    expect(versionBadge?.className).not.toContain('status-badge--good')
  })

  // The real, deliberate fleet condition (STEP-5 spec section 6 "Version
  // skew"): FPP-remote-01 runs a master build while Main and remote-04
  // run 9.4. This must be surfaced as a stated fact, not suppressed, and
  // it must not touch either instance's FPPHealthBadge.
  it('marks the list when reachable instances report different fpp.version values, without touching either health badge', () => {
    const fpp = [makeMainInstance(), makeRemote01Instance(), makeRemote04Instance()]
    renderFPPList(makeModel({ fpp }))
    expect(screen.getByRole('status')).toHaveTextContent(/do not agree/)
    expect(screen.getByRole('status')).toHaveTextContent('9.4 (2)')
    expect(screen.getByRole('status')).toHaveTextContent('9.x-master-822-g56515e4d (1)')
    // Every instance's own health badge is exactly the fixture's health
    // value ('healthy' for all three) -- skew is informational only.
    expect(screen.getAllByText('healthy')).toHaveLength(3)
  })

  it('does not show a skew banner when every reachable instance agrees', () => {
    const fpp = [
      makeFPPInstance('fpp-a', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true, state: 'current' }),
          makeEvidence({ signal: 'fpp.version', value: '9.4' }),
        ],
      }),
      makeFPPInstance('fpp-b', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true, state: 'current' }),
          makeEvidence({ signal: 'fpp.version', value: '9.4' }),
        ],
      }),
    ]
    renderFPPList(makeModel({ fpp }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  // Step 5 review finding 6/7: the FPP-01 ghost's version must never
  // count toward the skew statement at all -- its fpp.reachable is
  // unknown_age, so summarizeFleetVersions (fppSignals.ts) excludes it
  // via isReachableInstance, and no banner should appear purely because
  // an unreachable/unknown-age instance happens to disagree with a real
  // one.
  it('does not show a skew banner when the disagreeing instance is unreachable or of unknown age', () => {
    const fpp = [
      makeFPPInstance('fpp-a', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true, state: 'current' }),
          makeEvidence({ signal: 'fpp.version', value: '9.4' }),
        ],
      }),
      makeFPPInstance('fpp-01-ghost', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true, state: 'unknown_age', observedAt: null }),
          makeEvidence({ signal: 'fpp.version', value: '9.2' }),
        ],
      }),
    ]
    renderFPPList(makeModel({ fpp }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
