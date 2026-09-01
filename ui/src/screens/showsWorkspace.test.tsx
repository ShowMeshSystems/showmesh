import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
  getShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
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
    getShowAction: (...args: never[]) => stubs.getShowAction(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsPlaylists } = await import('./ShowsPlaylists')
const { ShowsNightSession } = await import('./ShowsNightSession')

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
            <Route path="night-session" element={<ShowsNightSession />} />
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

  it('lists all six tabs, including Night session, with none marked as still queued', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderWorkspace()
    await waitFor(() => expect(screen.getByRole('navigation', { name: 'Show workspace tabs' })).toBeInTheDocument())
    const nav = screen.getByRole('navigation', { name: 'Show workspace tabs' })
    const tabs = within(nav).getAllByRole('link')
    expect(tabs.map((t) => t.textContent?.replace(/\d+/, '').trim())).toEqual(
      expect.arrayContaining(['Playlists', 'Cues', 'Assets', 'Presentation', 'Automation', 'Night session']),
    )
    expect(tabs).toHaveLength(6)
    expect(within(nav).queryAllByText('Soon')).toHaveLength(0)
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

  it('renders Night session definitions as a list with a creation inspector', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (...args: never[]) => Promise.resolve({ serverTime: '', kind: args[0], objects: [] })
    stubs.listAssets = assetsEmpty
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({ serverTime: '', definitions: [] })
    renderWorkspace({}, '/shows/winter-ridge-2026/night-session')
    expect(await screen.findByRole('heading', { name: 'Night session definitions' })).toBeInTheDocument()
    expect(await screen.findByText('No definitions')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'New definition' }))
    expect(screen.getByRole('heading', { name: 'New night session' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: 'Night session definition sections' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Night session' })).toHaveAttribute('aria-current', 'page')

    const dialog = screen.getByRole('dialog')
    expect(dialog.parentElement).toBe(document.body)
    expect(dialog.className).toContain('sm-drawer--wide')
    expect(within(dialog).getByRole('heading', { name: 'New night session' })).toBeInTheDocument()

    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('populates FPP and timeline dropdowns from reported inventory without inventing options', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (...args: never[]) => Promise.resolve({ serverTime: '', kind: args[0], objects: [] })
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({
      serverTime: '',
      definitions: [{ instanceUuid: 'uuid-1', playlistName: 'WR26 Main Show', playlistHash: 'a'.repeat(64), capturedAt: '', receivedAt: '', entryCount: 1, referenced: false }],
    })
    stubs.listAssets = () => Promise.resolve({
      serverTime: '',
      assets: [{ id: 'a1', show: 'winter-ridge-2026', sequence: 'resting-loop', targetKind: 'node', target: 'media-front', mediaType: 'fseq', contentHash: `sha256:${'a'.repeat(64)}`, runtimeFilename: 'resting.fseq', sizeBytes: 1, createdAt: '', createdByPrincipalId: null, createdByPrincipalName: null, supersededAt: null, current: true }],
    })
    renderWorkspace({ fpp: [{ instanceId: 'barn-player', instanceUuid: 'uuid-1' } as never] }, '/shows/winter-ridge-2026/night-session')

    await screen.findByText('No definitions')
    fireEvent.click(screen.getByRole('button', { name: 'New definition' }))
    const showInstance = (await screen.findAllByRole('combobox', { name: 'FPP instance' }))[0]!
    expect(within(showInstance).getByRole('option', { name: 'barn-player' })).toBeInTheDocument()
    fireEvent.change(showInstance, { target: { value: 'barn-player' } })
    expect(within(screen.getByRole('combobox', { name: 'Show playlist' })).getByRole('option', { name: 'WR26 Main Show' })).toBeInTheDocument()
    expect(within(screen.getByRole('combobox', { name: 'Resting sequence' })).getByRole('option', { name: 'resting-loop' })).toBeInTheDocument()
    expect(within(screen.getByRole('combobox', { name: 'Resting target' })).getByRole('option', { name: 'media-front' })).toBeInTheDocument()
  })

  it('authors the complete transition and background-audio controls from real Show inventory', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (...args: never[]) => Promise.resolve({
      serverTime: '', kind: args[0], objects: args[0] === 'show.action' ? [summary({ id: 'lights-up', label: 'Lights up' })] : [],
    })
    stubs.getShowAction = () => Promise.resolve({
      serverTime: '', kind: 'show.action', id: 'lights-up', revision: 2,
      payload: { show: 'winter-ridge-2026', label: 'Lights up', description: '', safetyClass: 'none', target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'command' }, idempotent: true },
      updatedAt: '', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api',
    })
    stubs.listFPPPlaylistDefinitions = () => Promise.resolve({ serverTime: '', definitions: [] })
    stubs.listAssets = () => Promise.resolve({ serverTime: '', assets: [{ id: 'audio-1', show: 'winter-ridge-2026', sequence: 'arrival-bed', targetKind: 'node', target: 'audio-front', mediaType: 'audio', contentHash: 'sha256:x', runtimeFilename: 'arrival.mp3', sizeBytes: 1, createdAt: '', createdByPrincipalId: null, createdByPrincipalName: null, supersededAt: null, current: true }] })
    renderWorkspace({}, '/shows/winter-ridge-2026/night-session')
    await screen.findByText('No definitions')
    fireEvent.click(screen.getByRole('button', { name: 'New definition' }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Add step' })[0]!)
    expect(within(screen.getByRole('combobox', { name: 'Action' })).getByRole('option', { name: 'Lights up · lights-up' })).toBeInTheDocument()
    expect(screen.getByLabelText('Blackout hold (ms)')).toBeInTheDocument()
    expect(screen.getByLabelText('Blackout after show (ms)')).toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('Enable background audio while resting'))
    expect(within(screen.getByRole('combobox', { name: 'Audio asset' })).getByRole('option', { name: 'arrival-bed · audio-front' })).toBeInTheDocument()
    expect(screen.getByLabelText('Maximum gain (dB)')).toBeInTheDocument()
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
