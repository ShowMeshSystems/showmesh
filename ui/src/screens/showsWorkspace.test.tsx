import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ConfigObjectSummary, ConfigShowPlaylist, Model } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFPPPlaylistDefinitionEntries: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listFPPPlaylistDefinitions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFPPPlaylistReadiness: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    getShowPlaylist: (...args: never[]) => stubs.getShowPlaylist(...args),
    getFPPPlaylistDefinitionEntries: (...args: never[]) => stubs.getFPPPlaylistDefinitionEntries(...args),
    listFPPPlaylistDefinitions: (...args: never[]) => stubs.listFPPPlaylistDefinitions(...args),
    getFPPPlaylistReadiness: (...args: never[]) => stubs.getFPPPlaylistReadiness(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsPlaylists } = await import('./ShowsPlaylists')

function summary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'p1', label: 'Main Show', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function contentsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', objects: [] })
}

function assetsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
}

function showHead() {
  return Promise.resolve({
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show',
    id: 'winter-ridge-2026',
    revision: 47,
    payload: { name: 'Winter Ridge 2026', notes: '' },
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api',
  })
}

function fppPlaylist(overrides: Partial<ConfigShowPlaylist> = {}): ConfigShowPlaylist {
  return {
    show: 'winter-ridge-2026',
    name: 'Main Show',
    runner: 'fpp',
    fpp: { instanceUuid: 'uuid-1', playlistName: 'WinterRidge_Main', playlistHash: 'a'.repeat(64) },
    entries: [{ id: 'e1', cue: 'cue-1', fpp: { section: 'mainPlaylist', position: 0 } }],
    ...overrides,
  } as ConfigShowPlaylist
}

function audioPlaylist(overrides: Partial<ConfigShowPlaylist> = {}): ConfigShowPlaylist {
  return {
    show: 'winter-ridge-2026',
    name: 'Background Music',
    runner: 'showmesh-audio',
    showmeshAudio: { repeat: 'all' },
    entries: [{ id: 'e1', cue: 'cue-1' }],
    ...overrides,
  } as ConfigShowPlaylist
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/playlists') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="playlists" element={<ShowsPlaylists />} />
            <Route path="cues" element={<ShowsTabPlaceholder tab="Cues" />} />
            <Route path="assets" element={<ShowsTabPlaceholder tab="Assets" />} />
            <Route path="presentation" element={<ShowsTabPlaceholder tab="Presentation" />} />
            <Route path="automation" element={<ShowsTabPlaceholder tab="Automation" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows workspace shell', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('lists all five tabs and marks the two not yet rebuilt', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderWorkspace()
    await waitFor(() => expect(screen.getByRole('navigation', { name: 'Show workspace tabs' })).toBeInTheDocument())
    const nav = screen.getByRole('navigation', { name: 'Show workspace tabs' })
    const tabs = within(nav).getAllByRole('link')
    expect(tabs.map((t) => t.textContent?.replace(/\d+/, '').trim().split('Soon')[0]?.trim())).toEqual(
      expect.arrayContaining(['Playlists', 'Cues', 'Assets', 'Presentation', 'Automation']),
    )
    expect(within(nav).getAllByText('Soon')).toHaveLength(2)
  })

  it('shows the not-yet-rebuilt plate inside the shell, not a bare blank page, for a queued tab', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderWorkspace({}, '/shows/winter-ridge-2026/cues')
    await waitFor(() => expect(screen.getByRole('navigation', { name: 'Show workspace tabs' })).toBeInTheDocument())
    expect(screen.getByText('This tab has not been rebuilt yet')).toBeInTheDocument()
  })

  it('reads the show head once and renders revision and saved-by', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderWorkspace()
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Winter Ridge 2026' })).toBeInTheDocument())
    expect(screen.getByText(/Revision/)).toBeInTheDocument()
    expect(screen.getByText(/erbartos/)).toBeInTheDocument()
  })
})

describe('Shows · Playlists tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('says none is configured, distinct from a read failure', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderWorkspace()
    await waitFor(() => expect(screen.getByText('This show has no playlist configured.')).toBeInTheDocument())
  })

  it('lists a playlist and selects it for editing', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show.playlist'
        ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
        : contentsEmpty()
    stubs.listAssets = assetsEmpty
    stubs.getShowPlaylist = (id: string) =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', id, revision: 1, payload: fppPlaylist(), updatedAt: '2026-08-30T18:22:00Z', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api' })
    stubs.getFPPPlaylistDefinitionEntries = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        instanceUuid: 'uuid-1',
        playlistHash: 'a'.repeat(64),
        entries: [
          { section: 'mainPlaylist', position: 0, type: 'sequence', sequenceName: 'wizards-in-winter.fseq', mediaName: '' },
          { section: 'mainPlaylist', position: 1, type: 'sequence', sequenceName: 'carol-of-the-bells.fseq', mediaName: '' },
        ],
      })
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', definitions: [] })

    renderWorkspace()
    await waitFor(() => expect(screen.getByText('Main Show')).toBeInTheDocument())
    expect(screen.getByText('Editing')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())
    expect(screen.getByText('Bound')).toBeInTheDocument()
    expect(screen.getByText('Unbound')).toBeInTheDocument()
  })

  it('shows a Hash changed verdict only when a newer definition is stored under a different hash', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show.playlist'
        ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
        : contentsEmpty()
    stubs.listAssets = assetsEmpty
    stubs.getShowPlaylist = (id: string) =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', id, revision: 1, payload: fppPlaylist(), updatedAt: '2026-08-30T18:22:00Z', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api' })
    stubs.getFPPPlaylistDefinitionEntries = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', instanceUuid: 'uuid-1', playlistHash: 'a'.repeat(64), entries: [] })
    stubs.listFPPPlaylistDefinitions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        definitions: [
          { instanceUuid: 'uuid-1', playlistName: 'WinterRidge_Main', playlistHash: 'b'.repeat(64), capturedAt: '2026-08-30T20:54:00Z', receivedAt: '2026-08-30T20:54:05Z', entryCount: 6, referenced: false },
        ],
      })

    renderWorkspace()
    await waitFor(() => expect(screen.getByText('Hash changed')).toBeInTheDocument())
  })

  it('folds playlist readiness into this tab and states the failing condition, never fabricating ready', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show.playlist'
        ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
        : contentsEmpty()
    stubs.listAssets = assetsEmpty
    stubs.getShowPlaylist = (id: string) =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', id, revision: 1, payload: fppPlaylist(), updatedAt: '2026-08-30T18:22:00Z', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api' })
    stubs.getFPPPlaylistDefinitionEntries = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', instanceUuid: 'uuid-1', playlistHash: 'a'.repeat(64), entries: [] })
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', definitions: [] })
    stubs.getFPPPlaylistReadiness = (playlistId: string) =>
      Promise.resolve({ playlistId, ready: false, failingCondition: 'entry-not-in-definition', reason: 'Entry Main·3 is not in the stored definition.', serverTime: '2026-08-30T21:00:00Z' })

    renderWorkspace()
    await waitFor(() => expect(screen.getByRole('heading', { level: 3, name: 'Playlist readiness' })).toBeInTheDocument())
    const heading = screen.getByRole('heading', { level: 3, name: 'Playlist readiness' })
    within(heading.parentElement as HTMLElement).getByRole('button', { name: 'Check readiness' }).click()
    await waitFor(() => expect(screen.getByText('entry not in definition')).toBeInTheDocument())
    expect(screen.getByText('Entry Main·3 is not in the stored definition.')).toBeInTheDocument()
  })

  it('renders a showmesh-audio playlist’s entries with cue names, and marks entry length as not reported', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => {
      if (kind === 'show.playlist') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
      if (kind === 'show.cue') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [{ id: 'cue-1', label: 'Winter Bed Track 1', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z' }] })
      return contentsEmpty()
    }
    stubs.listAssets = assetsEmpty
    stubs.getShowPlaylist = (id: string) =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', id, revision: 1, payload: audioPlaylist(), updatedAt: '2026-08-30T18:22:00Z', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api' })

    renderWorkspace()
    const table = await screen.findByRole('region', { name: 'Playlist entries, scrollable' })
    await waitFor(() => expect(within(table).getByText('Winter Bed Track 1')).toBeInTheDocument())
    expect(within(table).getByText('not reported')).toBeInTheDocument()
  })
})
