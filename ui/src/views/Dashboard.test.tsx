import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Dashboard } from './Dashboard'
import { ModelContext } from '../app/ModelContext'
import {
  makeCollectorStatus,
  makeCurrentRun,
  makeCurrentRuns,
  makeEvidence,
  makeFPPInstance,
  makeModel,
  makeNode,
  makeResolumeInstance,
} from '../app/test-support/fixtures'
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
  it('renders authoritative concurrent runner playback and keeps playback separate from macro runs', () => {
    renderDashboard(makeModel({
      macroRuns: [],
      currentRuns: makeCurrentRuns({
        runs: [
          makeCurrentRun({ runner: 'fpp', id: 'fpp-run' }),
          makeCurrentRun({ runner: 'showmesh-audio', id: 'audio-run', status: 'running', next: null }),
        ],
      }),
    }))

    expect(screen.getByText('2 reported')).toBeInTheDocument()
    expect(screen.getByText('fpp', { selector: '.operator-list__meta' })).toBeInTheDocument()
    expect(screen.getByText('showmesh-audio', { selector: '.operator-list__meta' })).toBeInTheDocument()
    expect(screen.getAllByText(/Playback/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/Freshness/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/Reconciliation/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/Activation/).length).toBeGreaterThan(0)
    expect(screen.getByText(/No authoritative next item reported/)).toBeInTheDocument()
    expect(screen.getByText(/cue-2: next\.fseq/)).toBeInTheDocument()
    expect(screen.queryByText(/No macro run is currently reported/)).not.toBeInTheDocument()
  })

  it('states when the authoritative current-runs projection is unavailable', () => {
    renderDashboard(makeModel({ currentRunsFetchFailed: true }))
    expect(screen.getAllByText(/Authoritative current playback is unavailable/).length).toBeGreaterThan(0)
  })

  it.each([
    ['unknown', 'unknown'],
    ['unavailable', 'unknown'],
  ] as const)('does not render an authoritative current run with status %s as healthy', (status, tone) => {
    renderDashboard(makeModel({ currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({ status })] }) }))

    const badge = document.querySelector('.current-run .status-badge')
    expect(badge?.className).toContain(`status-badge--${tone}`)
    expect(badge?.className).not.toContain('status-badge--good')
  })

  it.each([
    ['running', 'good'],
    ['playing', 'good'],
    ['failed', 'bad'],
    ['stopped', 'unknown'],
    ['idle', 'unknown'],
  ] as const)('maps authoritative current run status %s to the %s tone', (status, tone) => {
    renderDashboard(makeModel({ currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({ status })] }) }))

    expect(document.querySelector('.current-run .status-badge')?.className).toContain(`status-badge--${tone}`)
  })

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

  // fpp-player's real condition: fpp.ports.count === 0. The pixel-current
  // panel must state this as a fact ("reports no pixel output ports"),
  // never render it as though nothing was collected.
  it('states "reports no pixel output ports" in the pixel current panel for an instance with a real zero-port count, distinct from "not collected"', () => {
    renderDashboard(makeModel({ fpp: [makeMainInstance()] }))
    expect(screen.getByText('reports no pixel output ports')).toBeInTheDocument()
  })

  // Step 5 review finding 6: the zero-ports statement above used to be a
  // bare <span> with no state marker at all, so a zero-port reading of
  // unknown age (one modelling decision away from the fpp-ghost ghost's
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

  // Track D seam D-4 (build contract §2.1, acceptance criterion 1): "not
  // configured" rather than an error or an empty box when GET
  // /resolume/instances answers with an empty array by design.
  it('renders the Resolume panel as "not configured" when no instance is configured', () => {
    renderDashboard(makeModel({ resolume: [] }))
    const heading = screen.getByRole('heading', { name: 'Resolume' })
    const section = heading.closest('section')
    expect(section?.textContent).toContain('not configured')
  })

  // Acceptance criterion 1: reachability with provenance and freshness,
  // through the shared EvidenceValue.
  it('renders Resolume reachability through EvidenceValue, with its state and freshness', () => {
    const instance = makeResolumeInstance('resolume-1', {
      composition: { name: 'Christmas 25' },
      observations: [makeEvidence({ signal: 'resolume.reachable', value: true, state: 'current' })],
    })
    renderDashboard(makeModel({ resolume: [instance] }))
    const heading = screen.getByRole('heading', { name: 'Resolume' })
    const section = heading.closest('section')
    expect(section?.textContent).toContain('true')
    expect(section?.textContent).toContain('Christmas 25')
    expect(section?.textContent).toMatch(/observed/)
  })

  // Acceptance criterion 2: an unreachable Arena renders "unknown", never
  // "healthy", and "unknown" does not collapse into "warning" in the
  // Attention panel's own sort order.
  it('surfaces a Resolume instance with unknown health as its own attention item, distinct from warning', () => {
    const instances = [makeResolumeInstance('resolume-1', { health: 'unknown' })]
    renderDashboard(makeModel({ resolume: instances }))
    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(1)
    expect(labels[0]).toContain('unknown')
    expect(labels[0]).not.toContain('warning')
  })

  it('surfaces a degraded Resolume instance as a warning, and a failed one as critical', () => {
    const instances = [
      makeResolumeInstance('resolume-degraded', { health: 'degraded' }),
      makeResolumeInstance('resolume-failed', { health: 'failed' }),
    ]
    renderDashboard(makeModel({ resolume: instances }))
    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(2)
    expect(labels[0]).toContain('critical')
    expect(labels[1]).toContain('warning')
  })

  // Track B seam B2b-front: a failed render pipeline is its own critical
  // attention item, and 'unknown' (stale/collection_failed/etc evidence)
  // stays distinct from both 'critical' and the ordinary healthy case —
  // the same rule attentionFromFPP/attentionFromResolume already enforce.
  it('surfaces a failed render pipeline as critical', () => {
    const nodes = [
      makeNode('media-01', {
        render: [
          makeEvidence({ signal: 'surface.pipeline.state', value: 'failed' }),
        ].map((e) => ({ resource: { kind: 'surface' as const, id: 'wall-1' }, ...e })),
      }),
    ]
    renderDashboard(makeModel({ nodes }))
    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(1)
    expect(labels[0]).toContain('critical')
    expect(screen.getByText(/pipeline has failed/)).toBeInTheDocument()
  })

  it('surfaces stale/unavailable render pipeline evidence as unknown, distinct from critical, and skips not_collected entirely', () => {
    const nodes = [
      makeNode('media-01', {
        render: [
          {
            resource: { kind: 'surface' as const, id: 'wall-1' },
            ...makeEvidence({ signal: 'surface.pipeline.state', value: 'running', state: 'stale' }),
          },
          {
            resource: { kind: 'surface' as const, id: 'wall-2' },
            ...makeEvidence({
              signal: 'surface.pipeline.state',
              value: null,
              state: 'not_collected',
              reason: 'never reported',
            }),
          },
        ],
      }),
    ]
    renderDashboard(makeModel({ nodes }))
    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(1)
    expect(labels[0]).toContain('unknown')
    expect(labels[0]).not.toContain('critical')
    expect(screen.getByText(/wall-1.*pipeline state is stale/)).toBeInTheDocument()
  })

  // A render held across an active-show switch (ADR-043 H0.7)
  // reports 'superseded' instead of 'running' — the wall still looks
  // healthy, so this must surface as its own attention item rather than
  // falling into the same silent bucket a genuinely healthy 'running'
  // pipeline does.
  it('surfaces a superseded render pipeline as a warning', () => {
    const nodes = [
      makeNode('media-01', {
        render: [
          makeEvidence({ signal: 'surface.pipeline.state', value: 'superseded' }),
        ].map((e) => ({ resource: { kind: 'surface' as const, id: 'wall-1' }, ...e })),
      }),
    ]
    renderDashboard(makeModel({ nodes }))
    const labels = attentionBadgeLabels()
    expect(labels).toHaveLength(1)
    expect(labels[0]).toContain('warning')
    expect(screen.getByText(/wall-1.*no longer active/)).toBeInTheDocument()
  })

  it('does not flag a running or stopped pipeline', () => {
    const nodes = [
      makeNode('media-01', {
        render: [
          {
            resource: { kind: 'surface' as const, id: 'wall-1' },
            ...makeEvidence({ signal: 'surface.pipeline.state', value: 'running' }),
          },
        ],
      }),
    ]
    renderDashboard(makeModel({ nodes }))
    expect(screen.getByText(/Nothing needs attention/)).toBeInTheDocument()
  })
})
