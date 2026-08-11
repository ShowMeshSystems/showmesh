import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Dashboard } from './Dashboard'
import { ModelContext } from '../app/ModelContext'
import { makeCollectorStatus, makeFPPInstance, makeModel, makeNode } from '../app/test-support/fixtures'
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
})
