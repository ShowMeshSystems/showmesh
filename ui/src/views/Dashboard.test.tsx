import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Dashboard } from './Dashboard'
import { ModelContext } from '../app/ModelContext'
import { makeCurrentRun, makeCurrentRuns, makeEvidence, makeFPPInstance, makeModel, makeNode, makeResolumeInstance } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderDashboard(model: Model) {
  return render(<ModelContext.Provider value={model}><MemoryRouter><Dashboard /></MemoryRouter></ModelContext.Provider>)
}

describe('Dashboard', () => {
  it('puts readiness, authoritative runs, status, path, attention, and activity in operator reading order', () => {
    renderDashboard(makeModel({ currentRuns: makeCurrentRuns() }))
    const ordered = Array.from(document.querySelector('.dashboard-page')?.children ?? [])
    const positions = [
      ordered.findIndex((element) => element.classList.contains('dashboard-readiness')),
      ordered.findIndex((element) => element.classList.contains('dashboard-current-runs')),
      ordered.findIndex((element) => element.classList.contains('shared-status-strip')),
      ordered.findIndex((element) => element.querySelector('#dashboard-presentation') !== null),
      ordered.findIndex((element) => element.querySelector('#dashboard-attention') !== null),
      ordered.findIndex((element) => element.querySelector('#dashboard-activity') !== null),
    ]
    expect(positions.every((position) => position >= 0)).toBe(true)
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
    expect(screen.getByRole('link', { name: 'Open Show Night' })).toHaveAttribute('href', '/night')
    expect(screen.queryByText('System evidence details')).not.toBeInTheDocument()
  })

  it('keeps concurrent FPP and showmesh-audio playback separate with runner, source, freshness, and authoritative next activity', () => {
    renderDashboard(makeModel({
      macroRuns: [{ id: 'macro-only' } as Model['macroRuns'][number]],
      currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({ id: 'fpp-run', runner: 'fpp' }), makeCurrentRun({ id: 'audio-run', runner: 'showmesh-audio', status: 'running', next: null })] }),
    }))
    expect(document.querySelectorAll('.dashboard-current-run')).toHaveLength(2)
    expect(screen.getByText('fpp runner')).toBeInTheDocument()
    expect(screen.getByText('showmesh-audio runner')).toBeInTheDocument()
    expect(screen.getAllByText('Freshness')).toHaveLength(2)
    expect(screen.getByText(/cue-2: next\.fseq \(source fpp\)/)).toBeInTheDocument()
    expect(screen.getByText('No authoritative next activity reported.')).toBeInTheDocument()
    expect(screen.queryByText(/macro-only/)).not.toBeInTheDocument()
  })

  it('distinguishes unavailable and none-observed current-runs states', () => {
    const { rerender } = renderDashboard(makeModel({ currentRunsFetchFailed: true }))
    expect(screen.getByText(/Authoritative current playback is unavailable.*coordinator could not be read/)).toBeInTheDocument()
    rerender(<ModelContext.Provider value={makeModel({ currentRuns: makeCurrentRuns({ runs: [] }) })}><MemoryRouter><Dashboard /></MemoryRouter></ModelContext.Provider>)
    expect(screen.getAllByText('None observed')).toHaveLength(2)
    expect(screen.getByText(/does not prove that no external process is running/)).toBeInTheDocument()
  })

  it('renders stale and failed run evidence without a healthy badge', () => {
    const { rerender } = renderDashboard(makeModel({ currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({ freshness: { state: 'stale', reason: 'last report is old', observedAt: null, collectedAt: null } })] }) }))
    expect(document.querySelector('.dashboard-current-run .status-badge')?.className).toContain('status-badge--unknown')
    expect(screen.getByText('stale: last report is old')).toBeInTheDocument()
    rerender(<ModelContext.Provider value={makeModel({ currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({ status: 'failed' })] }) })}><MemoryRouter><Dashboard /></MemoryRouter></ModelContext.Provider>)
    expect(document.querySelector('.dashboard-current-run .status-badge')?.className).toContain('status-badge--bad')
  })

  it('escalates an authoritative failed run into readiness and attention without discarding its runner or freshness evidence', () => {
    renderDashboard(makeModel({
      currentRuns: makeCurrentRuns({ runs: [makeCurrentRun({
        runner: 'showmesh-audio',
        show: 'an-unbroken-show-identifier',
        status: 'failed',
        freshness: { state: 'stale', reason: 'runner report is old', observedAt: null, collectedAt: null },
      })] }),
    }))

    expect(screen.getByLabelText('Show path readiness')).toHaveTextContent('Not ready')
    expect(screen.getByLabelText('Show path readiness')).toHaveTextContent('1 attention item')
    const attention = screen.getByRole('list', { name: 'Operator attention' })
    expect(attention).toHaveTextContent('showmesh-audio runner')
    expect(attention).toHaveTextContent('source showmesh-audio')
    expect(attention).toHaveTextContent('freshness stale: runner report is old')
    expect(attention.querySelector('a[href="/control"]')).not.toBeNull()
  })

  it('routes failed, disconnected, and unknown evidence to detail pages without collapsing them', () => {
    renderDashboard(makeModel({
      fpp: [makeFPPInstance('failed-fpp', { health: 'failed' }), makeFPPInstance('unknown-fpp', { health: 'unknown' })],
      nodes: [makeNode('offline-node', { controlPlane: { state: 'offline', reason: 'lost' } })],
      resolume: [makeResolumeInstance('resolume-1', { health: 'degraded' })],
    }))
    const attention = screen.getByRole('list', { name: 'Operator attention' })
    expect(attention.textContent).toContain('failed')
    expect(attention.textContent).toContain('control-plane connection lost')
    expect(attention.textContent).toContain('unknown')
    expect(attention.querySelector('a[href="/fpp/failed-fpp"]')).not.toBeNull()
  })

  it('keeps the presentation path shallow when evidence is dense', () => {
    const nodes = Array.from({ length: 12 }, (_, index) => makeNode(`node-${index}`, { render: [{ resource: { kind: 'surface' as const, id: `surface-${index}` }, ...makeEvidence({ signal: 'surface.pipeline.state', value: 'running' }) }] }))
    renderDashboard(makeModel({ nodes }))
    expect(screen.getByText('12 observed endpoints')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Inventory' })).not.toBeInTheDocument()
    expect(screen.queryByText('System evidence details')).not.toBeInTheDocument()
  })
})
