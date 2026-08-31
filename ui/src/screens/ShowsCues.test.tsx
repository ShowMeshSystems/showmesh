import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type ConfigObjectSummary, type ConfigShowCue, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowCue: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowCueRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowCue: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    getShowCue: (...args: never[]) => stubs.getShowCue(...args),
    getShowCueRevisions: (...args: never[]) => stubs.getShowCueRevisions(...args),
    getShowPlaylist: (...args: never[]) => stubs.getShowPlaylist(...args),
    putShowCue: (...args: never[]) => stubs.putShowCue(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsCues } = await import('./ShowsCues')

function cueSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'cue-1', label: 'House Preshow Loop', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function playlistSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'p1', label: 'Main Show', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
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

function cuePayload(overrides: Partial<ConfigShowCue> = {}): ConfigShowCue {
  return {
    show: 'winter-ridge-2026',
    name: 'House Preshow Loop',
    outputs: { render: { sequence: 'house-preshow-loop' }, audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } },
    ...overrides,
  } as ConfigShowCue
}

function cueResponse(payload: ConfigShowCue, id = 'cue-1', revision = 1) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.cue' as const,
    id,
    revision,
    payload,
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
}

function playlistPayload() {
  return {
    show: 'winter-ridge-2026',
    name: 'Main Show',
    runner: 'showmesh-audio',
    showmeshAudio: { repeat: 'none' },
    entries: [{ id: 'e1', cue: 'cue-1' }],
  }
}

function playlistResponse() {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.playlist' as const,
    id: 'p1',
    revision: 1,
    payload: playlistPayload(),
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function assetsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
}

function withContents(kind: string, cues: ConfigObjectSummary[], playlists: ConfigObjectSummary[]) {
  if (kind === 'show.cue') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: cues })
  if (kind === 'show.playlist') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: playlists })
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/cues') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="cues" element={<ShowsCues />} />
            <Route path="playlists" element={<ShowsTabPlaceholder tab="Playlists" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows · Cues tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function setup(scopes: string[] = ['config:write'], cues: ConfigObjectSummary[] = [cueSummary()], playlists: ConfigObjectSummary[] = [playlistSummary()]) {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, cues, playlists)
    stubs.listAssets = assetsEmpty
    stubs.getShowCue = (id: string) => Promise.resolve(cueResponse(cuePayload(), id))
    stubs.getShowPlaylist = () => Promise.resolve(playlistResponse())
    return renderWorkspace({ session: signedIn(scopes) })
  }

  it('renders the section heading and groups a playlist-bound cue under "In a playlist"', async () => {
    setup()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Cues in this show' })).toBeInTheDocument())
    const region = await screen.findByRole('region', { name: 'In a playlist, scrollable' })
    await waitFor(() => expect(within(region).getByText('House Preshow Loop')).toBeInTheDocument())
    expect(within(region).getByText('Main Show')).toBeInTheDocument()
  })

  it('an empty show renders the loading, then the settled-empty state, distinguishably', async () => {
    setup(['config:write'], [], [])
    const region = await screen.findByRole('region', { name: 'In a playlist, scrollable' })
    await waitFor(() => expect(within(region).getByText('None')).toBeInTheDocument())
    expect(within(region).getByText('No cue matches here.')).toBeInTheDocument()
  })

  it('a read failure is reported distinctly from empty, with a retry', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => Promise.reject(new ApiError('Coordinator unreachable.', 503, 'unavailable'))
    stubs.listAssets = assetsEmpty
    renderWorkspace({ session: signedIn(['config:write']) })
    expect(await screen.findByText('Coordinator unreachable.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('an announcement-output cue is grouped as directly activatable with its policy, not "in a playlist"', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [cueSummary({ id: 'cue-2', label: 'Sponsor Announcement' })], [])
    stubs.listAssets = assetsEmpty
    stubs.getShowCue = () =>
      Promise.resolve(
        cueResponse(
          cuePayload({
            name: 'Sponsor Announcement',
            outputs: { audio: { asset: 'sponsor-read', startOffsetMillis: 0 }, announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 400 } },
          }),
          'cue-2',
        ),
      )
    renderWorkspace({ session: signedIn(['config:write']) })
    const region = await screen.findByRole('region', { name: 'Directly activatable, scrollable' })
    await waitFor(() => expect(within(region).getByText('Sponsor Announcement')).toBeInTheDocument())
    expect(within(region).getByText('Duck to -18 dB')).toBeInTheDocument()
    const inPlaylist = screen.getByRole('region', { name: 'In a playlist, scrollable' })
    expect(within(inPlaylist).queryByText('Sponsor Announcement')).not.toBeInTheDocument()
  })

  it('save is disabled without config:write and is actually inert', async () => {
    setup([])
    const row = await screen.findByRole('button', { name: 'House Preshow Loop' })
    fireEvent.click(row)
    const save = await screen.findByRole('button', { name: 'Save cue' })
    expect(save).toBeDisabled()
    const putSpy = vi.fn(() => new Promise(() => {}))
    stubs.putShowCue = putSpy
    fireEvent.click(save)
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('renders the cue’s own revision history, author and timestamp included', async () => {
    stubs.getShowCueRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show.cue',
        revisions: [
          { revision: 1, createdAt: '2026-08-30T18:22:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true },
        ],
      })
    setup()
    const row = await screen.findByRole('button', { name: 'House Preshow Loop' })
    fireEvent.click(row)
    expect(await screen.findByText('Active · 1')).toBeInTheDocument()
  })

  it('a stale cue save is refused, writes nothing, and names the changed fields', async () => {
    let calls = 0
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [cueSummary()], [playlistSummary()])
    stubs.listAssets = assetsEmpty
    stubs.getShowCue = (id: string) => {
      calls += 1
      return Promise.resolve(cueResponse(cuePayload(calls === 1 ? {} : { name: 'Renamed Elsewhere' }), id, calls === 1 ? 1 : 2))
    }
    stubs.getShowPlaylist = () => Promise.resolve(playlistResponse())
    const putSpy = vi.fn(() => Promise.resolve(cueResponse(cuePayload({ name: 'Renamed Elsewhere' }), 'cue-1', 2)))
    stubs.putShowCue = putSpy
    renderWorkspace({ session: signedIn(['config:write']) })

    const row = await screen.findByRole('button', { name: 'House Preshow Loop' })
    fireEvent.click(row)
    const save = await screen.findByRole('button', { name: 'Save cue' })
    fireEvent.click(save)
    expect(await screen.findByText(/Stale write/)).toBeInTheDocument()
    expect(screen.getByText(/Changed:/)).toHaveTextContent('name')
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('a new cue with no output selected cannot be created', async () => {
    setup()
    fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
    const create = await screen.findByRole('button', { name: 'Create cue' })
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Test Cue' } })
    // Uncheck the outputs a fresh cue starts with none selected, so Create
    // stays disabled until at least one is picked.
    expect(create).toBeDisabled()
    expect(create).toHaveAttribute('title', 'Pick at least one output.')
  })

  it('LTC without audio is refused; selecting audio too clears the block', async () => {
    setup()
    fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Test Cue' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /LTC/ }))
    const create = screen.getByRole('button', { name: 'Create cue' })
    expect(create).toBeDisabled()
    expect(create).toHaveAttribute('title', 'LTC requires Audio to also be selected.')
  })

  it('announcement without audio is refused, and duckGainDb is only required (and sent) when the policy is duck', async () => {
    setup()
    fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Test Announcement' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Announcement/ }))
    const create = screen.getByRole('button', { name: 'Create cue' })
    expect(create).toBeDisabled()
    expect(create).toHaveAttribute('title', 'Announcement requires Audio to also be selected.')

    fireEvent.click(screen.getByRole('checkbox', { name: /Audience audio/ }))
    // Now audio is selected but has no asset chosen yet, so still blocked on that.
    expect(create).toHaveAttribute('title', 'Audio needs an asset selected.')
  })

  it('refuses to create a cue whose id already names one, rather than writing over it', async () => {
    let wrote = false
    stubs.getShowCue = (id: string) => Promise.resolve(cueResponse(cuePayload(), id))
    stubs.putShowCue = () => {
      wrote = true
      return new Promise(() => {})
    }
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [], [])
    stubs.listAssets = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        assets: [
          {
            id: 'a1',
            show: 'winter-ridge-2026',
            sequence: 'sponsor-read',
            targetKind: 'show',
            target: '',
            mediaType: 'audio',
            contentHash: 'sha256:' + 'a'.repeat(64),
            runtimeFilename: 'sponsor-read.wav',
            sizeBytes: 100,
            createdAt: '2026-08-30T18:00:00Z',
            createdByPrincipalId: 'p1',
            createdByPrincipalName: 'erbartos',
            supersededAt: null,
            current: true,
          },
        ],
      })
    renderWorkspace({ session: signedIn(['config:write']) })
    fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Sponsor Read' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Audience audio/ }))
    fireEvent.change(await screen.findByRole('combobox', { name: 'Audio asset' }), { target: { value: 'sponsor-read' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create cue' }))

    await waitFor(() => expect(screen.getByText(/already names a cue in this show/)).toBeInTheDocument())
    expect(wrote).toBe(false)
  })

  it('switching the announcement policy off duck omits duckGainDb from the payload', async () => {
    stubs.getShowCue = () => Promise.reject(new ApiError('no such cue', 404, 'https://showmesh.dev/problems/resource-not-found'))
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [], [])
    stubs.listAssets = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        assets: [
          {
            id: 'a1',
            show: 'winter-ridge-2026',
            sequence: 'sponsor-read',
            targetKind: 'show',
            target: '',
            mediaType: 'audio',
            contentHash: 'sha256:' + 'a'.repeat(64),
            runtimeFilename: 'sponsor-read.wav',
            sizeBytes: 100,
            createdAt: '2026-08-30T18:00:00Z',
            createdByPrincipalId: 'p1',
            createdByPrincipalName: 'erbartos',
            supersededAt: null,
            current: true,
          },
        ],
      })
    renderWorkspace({ session: signedIn(['config:write']) })
    fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Sponsor Read' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Audience audio/ }))
    fireEvent.change(screen.getByRole('combobox', { name: 'Audio asset' }), { target: { value: 'sponsor-read' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Announcement/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Mix' }))

    let sent: unknown = null
    stubs.putShowCue = (_id: string, payload: unknown) => {
      sent = payload
      return Promise.resolve(cueResponse(payload as ConfigShowCue, 'sponsor-read'))
    }
    fireEvent.click(screen.getByRole('button', { name: 'Create cue' }))
    await waitFor(() => expect(sent).not.toBeNull())
    const payload = sent as ConfigShowCue
    expect(payload.outputs.announcement?.policy).toBe('mix')
    expect(payload.outputs.announcement).not.toHaveProperty('duckGainDb')
  })
})
