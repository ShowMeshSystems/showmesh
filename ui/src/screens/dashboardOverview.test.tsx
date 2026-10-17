import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { CurrentRun, MacroRunSummary, NightSessionState, Node, ObservationEntry } from '../api'
import { initialModel } from '../api/domain'
import { makeEvidence } from '../api/test-support/fixtures'
import { ModelContext } from '../app/ModelContext'
import { DashboardOverview } from './DashboardOverview'
import { audioRouteTile, lifecycleTile, runnerTile, smpteTiles } from './dashboardOverviewModel'

const NOW_MS = Date.parse('2026-10-17T12:00:00.000Z')
const NOW = new Date(NOW_MS).toISOString()

function ev(signal: string, value: ObservationEntry['value']): ObservationEntry {
  return { ...makeEvidence({ signal, value, observedAt: NOW }), resource: { kind: 'node', id: 'n1' } }
}

const lockedNode = {
  nodeId: 'n1',
  label: 'Garage',
  clock: [],
  audio: [ev('node.audio.ltc.timecode', '01:02:03:04'), ev('node.audio.sync.state', 'locked')],
} as unknown as Node

const quietNode = { nodeId: 'n2', label: null, clock: [], audio: [] } as unknown as Node

function run(runner: CurrentRun['runner'], positionMs: number | null): CurrentRun {
  return {
    runner,
    playlistId: 'main',
    playback: { state: 'playing', itemId: 'song-1', positionMs },
  } as unknown as CurrentRun
}

const sessionLive = { stateLabel: 'Show running' } as unknown as NightSessionState
const macroRunning = { state: 'running', macroObjectId: 'welcome' } as unknown as MacroRunSummary

describe('dashboardOverviewModel', () => {
  it('reports empty states without inventing values', () => {
    expect(lifecycleTile(null, [])).toEqual({ value: 'Not heard from', detail: 'No macro is running.' })
    expect(runnerTile([], 'fpp', 'No FPP playlist is running.').value).toBe('Nothing playing')
    expect(audioRouteTile([quietNode]).value).toBe('No audio nodes')
    expect(smpteTiles([quietNode], NOW_MS).source.value).toBe('No timecode')
  })

  it('reports populated states from the served data', () => {
    expect(lifecycleTile(sessionLive, [macroRunning])).toEqual({ value: 'Show running', detail: 'Running macro: welcome.' })
    expect(runnerTile([run('fpp', 75_400)], 'fpp', '').detail).toBe('main · song-1 · 1:15.4')
    expect(audioRouteTile([lockedNode])).toEqual({ value: '1 / 1', detail: 'all locked' })
    expect(smpteTiles([lockedNode], NOW_MS).source).toEqual({ value: '01:02:03:04', detail: 'from Garage' })
  })
})

function renderOverview(model: Partial<ReturnType<typeof initialModel>>, session: NightSessionState | null = null) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter>
        <DashboardOverview session={session} />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('DashboardOverview', () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ['Date'], now: NOW_MS }))
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('shows empty states and Not wired stamps on an empty model', () => {
    renderOverview({})
    expect(screen.getByText('Not heard from')).toBeInTheDocument()
    expect(screen.getAllByText('No timecode', { selector: '.sm-tile__value' }).length).toBe(1)
    expect(screen.getAllByText("Not wired").length).toBe(5)
  })

  it('shows lifecycle, playback and timecode when the model has them', () => {
    renderOverview(
      {
        nodes: [lockedNode],
        macroRuns: [macroRunning],
        currentRuns: { serverTime: NOW, activeShow: { configured: true, show: 's', generation: 1 }, runs: [run('fpp', 5_000)] },
      },
      sessionLive,
    )
    expect(screen.getByText('Show running')).toBeInTheDocument()
    expect(screen.getByText('Running macro: welcome.')).toBeInTheDocument()
    expect(screen.getByText('main · song-1 · 0:05.0')).toBeInTheDocument()
    expect(screen.getByText('01:02:03:04')).toBeInTheDocument()
  })
})
