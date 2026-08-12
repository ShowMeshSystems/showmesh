import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Dashboard } from './Dashboard'
import { ModelContext } from '../app/ModelContext'
import { makeCollectorStatus, makeEvidence, makeFPPInstance, makeModel, makeNode } from '../app/test-support/fixtures'
import { makeMainInstance, makeRemote04Instance } from '../app/test-support/fppFleetFixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderDashboard(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function attentionBadgeLabels(): string[] {
  const section = screen.getByRole('heading', { name: 'Attention' }).closest('section')
  if (!section) throw new Error('Attention section not found')
  return Array.from(section.querySelectorAll('.status-badge')).map((el) => el.textContent ?? '')
}

describe('Dashboard', () => {
  // D2 / OBSERVABILITY section 6.2's ordering rule and ADR-011: critical
  // first, then warning, and an 'unknown' health must produce its own
  // attention item rather than reading as healthy. Before this fix, every
  // FPP instance at health "unknown" produced zero attention items.
  it('prioritizes critical over warning over unknown, and surfaces an FPP instance with unknown health', () => {
    const fpp = [
      makeFPPInstance('fpp-failed', { health: 'failed' }),
      makeFPPInstance('fpp-degraded', { health: 'degraded' }),
      makeFPPInstance('fpp-unknown', { health: 'unknown' }),
      // 'suppressed' is an expected condition (OBSERVABILITY section
      // 4.2), not an attention item.
      makeFPPInstance('fpp-suppressed', { health: 'suppressed' }),
      makeFPPInstance('fpp-healthy', { health: 'healthy' }),
    ]
    const nodes = [makeNode('node-offline', { controlPlane: { state: 'offline', reason: 'lost' } })]

    renderDashboard(makeModel({ fpp, nodes }))

    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(4)
    expect(labels[0]).toContain('critical')
    expect(labels[1]).toContain('warning')
    expect(labels[2]).toContain('warning')
    expect(labels[3]).toContain('unknown')

    expect(screen.getByText('FPP instance "fpp-unknown" health is unknown')).toBeInTheDocument()
    // Never says "no active conditions" once there is something to report.
    expect(screen.queryByText(/Nothing needs attention/)).not.toBeInTheDocument()
  })

  it('states plainly that nothing needs attention when every instance is healthy, suppressed, or online', () => {
    const fpp = [makeFPPInstance('fpp-a', { health: 'healthy' }), makeFPPInstance('fpp-b', { health: 'suppressed' })]
    const nodes = [makeNode('node-a', { controlPlane: { state: 'online', reason: null } })]
    renderDashboard(makeModel({ fpp, nodes }))

    expect(screen.getByText(/Nothing needs attention/)).toBeInTheDocument()
    const section = screen.getByRole('heading', { name: 'Attention' }).closest('section')
    expect(section?.querySelector('.status-badge')).toBeNull()
  })

  // D1: a collector's state and reason previously never reached the
  // operator; the only rendering was the bare count
  // `model.collectors.length`. This asserts the id, its own run state,
  // and its reason are all visible.
  it('renders each collector its own state and reason, not a bare count', () => {
    const model = makeModel({
      collectors: [
        makeCollectorStatus('fpp-rest', { state: 'running', reason: null }),
        makeCollectorStatus('weather-collector', {
          state: 'not_configured',
          reason: 'no endpoint configured for this collector',
        }),
      ],
    })
    renderDashboard(model)

    expect(screen.getByText('fpp-rest')).toBeInTheDocument()
    expect(screen.getByText('running')).toBeInTheDocument()
    expect(screen.getByText('weather-collector')).toBeInTheDocument()
    expect(screen.getByText('not_configured')).toBeInTheDocument()
    expect(screen.getByText('no endpoint configured for this collector')).toBeInTheDocument()
  })

  // D2: FPP health unknown/suppressed counts must be visible on the
  // default view, not folded into a single opaque "FPP instances
  // configured" total.
  it('breaks out FPP instances with unknown and suppressed health in the inventory summary', () => {
    const fpp = [
      makeFPPInstance('fpp-a', { health: 'unknown' }),
      makeFPPInstance('fpp-b', { health: 'unknown' }),
      makeFPPInstance('fpp-c', { health: 'suppressed' }),
      makeFPPInstance('fpp-d', { health: 'healthy' }),
    ]
    renderDashboard(makeModel({ fpp }))

    expect(screen.getByText('FPP instances with health unknown').nextElementSibling?.textContent).toBe('2')
    expect(screen.getByText('FPP instances suppressed').nextElementSibling?.textContent).toBe('1')
  })

  it('surfaces the permanently-lost-history gap on the recent events panel', () => {
    renderDashboard(makeModel({ eventsGap: true }))
    expect(screen.getByText(/event history has been permanently lost to retention/)).toBeInTheDocument()
  })

  // Step 5: four newly modeled signal groups each get a panel (spec
  // section 6 "Dashboard"). All four must exist -- ShowMesh now models
  // every one of these subsystems, so there is never a reason to omit a
  // panel for it.
  it('renders a panel for each of the four newly modeled signal groups', () => {
    renderDashboard(makeModel({ fpp: [makeMainInstance()] }))
    expect(screen.getByRole('heading', { name: 'Playback state' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Controller health' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Pixel current' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Network / MQTT state' })).toBeInTheDocument()
  })

  // A modeled subsystem with no evidence states the absence rather than
  // rendering blank (spec section 6 "Dashboard"). Built with an instance
  // that has zero observations at all -- the panels must not crash or
  // silently omit the instance's row.
  it('states "not collected" in each of the four panels for an instance with no observations, rather than a blank row', () => {
    renderDashboard(makeModel({ fpp: [makeFPPInstance('fpp-empty', { observations: [] })] }))
    const notCollected = screen.getAllByText(/not collected/)
    // fpp.status (playback), fpp.fppd.state + fpp.power.bad (controller),
    // fpp.mqtt.configured + fpp.mqtt.connected (network) -- pixel current
    // renders its own "not collected" sentence, checked separately below.
    expect(notCollected.length).toBeGreaterThanOrEqual(5)
    const pixelCurrentSection = screen.getByRole('heading', { name: 'Pixel current' }).closest('section')
    expect(pixelCurrentSection?.textContent).toContain('Port inventory not collected for any instance yet.')
  })

  // FPP-Main's real condition: fpp.ports.count === 0. The pixel-current
  // panel must state this as a fact ("reports no pixel output ports"),
  // never render it as though nothing was collected.
  it('states "reports no pixel output ports" in the pixel current panel for an instance with a real zero-port count, distinct from "not collected"', () => {
    renderDashboard(makeModel({ fpp: [makeMainInstance()] }))
    expect(screen.getByText('reports no pixel output ports')).toBeInTheDocument()
  })

  // Step 5 review finding 6: the zero-ports statement above used to be a
  // bare <span> with no state marker at all, so a zero-port reading of
  // unknown age (one modelling decision away from the FPP-01 ghost's
  // exact shape on this signal) rendered exactly as confidently as a
  // fresh, current one. It must now carry its own Evidence.state via a
  // StatusBadge, matching FleetSignalBadge/PortGrid's established pattern
  // for the same distinction.
  it('carries fpp.ports.count\'s own state on the zero-ports statement, never rendering an unknown_age zero with the current tone', () => {
    const fpp = [
      makeFPPInstance('fpp-01-ghost', {
        observations: [
          makeEvidence({
            signal: 'fpp.ports.count',
            value: 0,
            state: 'unknown_age',
            observedAt: null,
            reason: 'retained MQTT delivery of unknown age',
          }),
        ],
      }),
    ]
    renderDashboard(makeModel({ fpp }))
    const statement = screen.getByText('reports no pixel output ports')
    const badge = statement.closest('.status-badge')
    expect(badge?.className).toContain('status-badge--unknown')
    expect(badge?.className).not.toContain('status-badge--good')
  })

  it('rolls up port totals across the fleet in the pixel current panel', () => {
    renderDashboard(makeModel({ fpp: [makeRemote04Instance()] }))
    expect(screen.getByText(/48 port element\(s\) across 1 reporting instance\(s\), 32 of which are smart-receiver blind spots\./)).toBeInTheDocument()
  })

  it('totals fpp.warnings.count across the fleet in the inventory summary, distinct from instances not reporting', () => {
    const fpp = [
      makeFPPInstance('fpp-a', { observations: [makeEvidence({ signal: 'fpp.warnings.count', value: 2 })] }),
      makeFPPInstance('fpp-b', { observations: [] }),
    ]
    renderDashboard(makeModel({ fpp }))
    expect(screen.getByText('FPP warnings across fleet').nextElementSibling?.textContent).toContain('2')
    expect(screen.getByText('FPP warnings across fleet').nextElementSibling?.textContent).toContain(
      '1 instance not reporting',
    )
  })

  it('shows "not collected" for fleet warnings when no instance has reported a count', () => {
    renderDashboard(makeModel({ fpp: [makeFPPInstance('fpp-a', { observations: [] })] }))
    expect(screen.getByText('FPP warnings across fleet').nextElementSibling?.textContent).toBe('not collected')
  })
})
