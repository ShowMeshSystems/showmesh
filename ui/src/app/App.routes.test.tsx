import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeModel } from './test-support/fixtures'
import App from './App'

/* The seven-destination route table (UI-DESIGN-GUIDE.md section 3).
 *
 * These tests replace the pre-overhaul suite, which asserted the /config/*
 * settings pages and the legacy hash-fragment redirects. Both are gone by
 * intent: eighteen routes collapsed into seven destinations, and old addresses
 * are deliberately NOT redirected, because the not-found page is the migration
 * guide. The old-address behaviour is now asserted at the bottom of this file
 * as a 404 rather than a redirect. */

const { useModelMock, submitTokenMock } = vi.hoisted(() => ({
  useModelMock: vi.fn(),
  submitTokenMock: vi.fn(),
}))

vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, useModel: useModelMock, submitToken: submitTokenMock }
})

vi.mock('./Layout', async () => {
  const { Outlet } = await import('react-router-dom')
  return { Layout: () => <Outlet /> }
})

vi.mock('../views/Dashboard', () => ({ Dashboard: () => <div data-testid="dashboard">Dashboard</div> }))
vi.mock('../views/NightSession', () => ({ NightSession: () => <div data-testid="night">Show Night</div> }))
vi.mock('../views/LiveControl', () => ({ LiveControl: () => <div data-testid="control">Live Control</div> }))
vi.mock('../views/Shows', () => ({ Shows: () => <div data-testid="shows">Shows</div> }))
vi.mock('../views/ShowDetail', () => ({ ShowDetail: () => <div data-testid="show-detail">Show detail</div> }))
vi.mock('../views/ShowPlaylists', () => ({ ShowPlaylists: () => <div data-testid="show-playlists">Playlists</div> }))
vi.mock('../views/ShowCues', () => ({ ShowCues: () => <div data-testid="show-cues">Cues</div> }))
vi.mock('../views/ShowSurfaces', () => ({ ShowSurfaces: () => <div data-testid="show-presentation">Presentation</div> }))
vi.mock('../views/ShowActions', () => ({ ShowActions: () => <div data-testid="show-automation">Automation</div> }))
vi.mock('../views/Assets', () => ({ Assets: () => <div data-testid="assets">Assets</div> }))
vi.mock('../views/AssetManifest', () => ({ AssetManifest: () => <div data-testid="assets-manifest">Asset manifest</div> }))
vi.mock('../views/Monitor', () => ({ Monitor: () => <div data-testid="monitor-fleet">Fleet</div> }))
vi.mock('../views/Observations', () => ({ Observations: () => <div data-testid="monitor-signals">Signals</div> }))
vi.mock('../views/Events', () => ({ Events: () => <div data-testid="monitor-activity">Activity</div> }))
vi.mock('../views/Capabilities', () => ({ Capabilities: () => <div data-testid="monitor-capabilities">Capabilities</div> }))
vi.mock('../views/PlaylistReadiness', () => ({ Readiness: () => <div data-testid="monitor-readiness">Readiness</div> }))
vi.mock('../views/NodeDetail', () => ({ NodeDetail: () => <div data-testid="node-detail">Node</div> }))
vi.mock('../views/FPPDetail', () => ({ FPPDetail: () => <div data-testid="fpp-detail">FPP</div> }))
vi.mock('../views/ResolumeView', () => ({ ResolumeView: () => <div data-testid="resolume">Resolume</div> }))
vi.mock('../views/Access', () => ({ Access: () => <div data-testid="access">Access</div> }))
vi.mock('../views/NotFound', () => ({ NotFound: () => <div data-testid="not-found">Not found</div> }))

vi.mock('../views/SettingsPages', () => ({
  ConnectionsSettings: () => <div data-testid="settings-connections">Connections</div>,
  ContentDeliverySettings: () => <div data-testid="settings-content-delivery">Content delivery</div>,
  RenderRecoverySettings: () => <div data-testid="settings-render-recovery">Render recovery</div>,
  AppearanceSettings: () => <div data-testid="settings-appearance">Appearance</div>,
  AudioDefaultsSettings: () => <div data-testid="settings-audio-defaults">Audio defaults</div>,
  ModeSettings: () => <div data-testid="settings-mode">Mode</div>,
}))
vi.mock('../views/settings/NodeRoutingSettings', () => ({
  NodeRoutingSettings: () => <div data-testid="settings-node-routing">Node routing</div>,
}))

afterEach(() => {
  cleanup()
  useModelMock.mockReset()
  submitTokenMock.mockReset()
  vi.restoreAllMocks()
})

function renderAt(path: string) {
  window.history.pushState({}, '', path)
  useModelMock.mockReturnValue(makeModel())
  return render(<App />)
}

describe('the seven rail destinations', () => {
  it.each([
    ['/', 'dashboard'],
    ['/night', 'night'],
    ['/control', 'control'],
    ['/shows', 'shows'],
    ['/assets', 'assets'],
    ['/monitor/fleet', 'monitor-fleet'],
    ['/settings/connections', 'settings-connections'],
  ])('routes %s', (path, testId) => {
    renderAt(path)
    expect(screen.getByTestId(testId)).toBeInTheDocument()
  })
})

describe('Monitor facets', () => {
  it.each([
    ['/monitor/fleet', 'monitor-fleet'],
    ['/monitor/signals', 'monitor-signals'],
    ['/monitor/activity', 'monitor-activity'],
    ['/monitor/capabilities', 'monitor-capabilities'],
    ['/monitor/readiness', 'monitor-readiness'],
  ])('routes %s', (path, testId) => {
    renderAt(path)
    expect(screen.getByTestId(testId)).toBeInTheDocument()
  })

  it('sends the bare /monitor to the Fleet facet rather than rendering nothing', () => {
    renderAt('/monitor')
    expect(screen.getByTestId('monitor-fleet')).toBeInTheDocument()
  })

  it.each([
    ['/monitor/fleet/node/media-front', 'node-detail'],
    ['/monitor/fleet/fpp/barn-player', 'fpp-detail'],
    ['/monitor/fleet/resolume/arena-main', 'resolume'],
  ])('routes the resource detail %s', (path, testId) => {
    renderAt(path)
    expect(screen.getByTestId(testId)).toBeInTheDocument()
  })
})

describe('Settings tabs', () => {
  it.each([
    ['/settings/connections', 'settings-connections'],
    ['/settings/content-delivery', 'settings-content-delivery'],
    ['/settings/render-recovery', 'settings-render-recovery'],
    ['/settings/appearance', 'settings-appearance'],
    ['/settings/audio-defaults', 'settings-audio-defaults'],
    ['/settings/node-routing', 'settings-node-routing'],
    ['/settings/mode', 'settings-mode'],
  ])('routes %s', (path, testId) => {
    renderAt(path)
    expect(screen.getByTestId(testId)).toBeInTheDocument()
  })

  it('sends the bare /settings to Connections rather than rendering nothing', () => {
    renderAt('/settings')
    expect(screen.getByTestId('settings-connections')).toBeInTheDocument()
  })

  it('keeps Access on its own route, because it is the one tab that leaves the screen', () => {
    renderAt('/access')
    expect(screen.getByTestId('access')).toBeInTheDocument()
  })
})

describe('the show workspace', () => {
  /* The load-bearing change: these are real nested routes. Previously every tab
   * except the overview was a <Navigate> out to a global route with a ?show=
   * query, which meant a tab navigated OUT of the show it belonged to. */
  it.each([
    ['/shows/winter-ridge/playlists', 'show-playlists'],
    ['/shows/winter-ridge/cues', 'show-cues'],
    ['/shows/winter-ridge/assets', 'assets'],
    ['/shows/winter-ridge/presentation', 'show-presentation'],
    ['/shows/winter-ridge/automation', 'show-automation'],
  ])('routes %s without leaving the show', (path, testId) => {
    renderAt(path)
    expect(screen.getByTestId(testId)).toBeInTheDocument()
    expect(window.location.pathname).toBe(path)
  })
})

describe('old addresses', () => {
  /* Deliberately not redirected. The not-found page maps each one to its new
   * home and says plainly that nothing is redirected, because eighteen routes
   * collapsing into seven means a 404 here is usually an old bookmark. */
  it.each([
    ['/nodes'],
    ['/fpp'],
    ['/resolume'],
    ['/observations'],
    ['/events'],
    ['/audit'],
    ['/capabilities'],
    ['/config/connections'],
    ['/config/show'],
    ['/config/show.cue'],
    ['/config/show.playlist'],
    ['/config/show.surface'],
    ['/actions'],
    ['/macros'],
    ['/playlists/readiness'],
  ])('answers %s with the not-found migration guide, never a redirect', (path) => {
    renderAt(path)
    expect(screen.getByTestId('not-found')).toBeInTheDocument()
    expect(window.location.pathname).toBe(path)
  })
})
