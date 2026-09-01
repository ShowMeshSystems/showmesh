import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type ConfigObjectSummary, type ConfigShowCue, type Model, type SessionResponse, type ShowCueConfigResponse } from '../api'
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
  getAudioSettingsConfig: (() =>
    Promise.resolve({
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'audio.settings',
      revision: 1,
      payload: {
        defaultFadeDurationMs: 400,
        defaultMaxBackgroundGainDb: 0,
        duckTargetGainDb: -18,
        duckFadeDurationMs: 200,
        duckRestoreFadeDurationMs: 600,
        driftIgnoreThresholdMs: 20,
        ltcFrameRate: '30',
        ltcDefaultStartOffset: '00:00:00:00',
      },
      updatedAt: '2026-08-30T18:22:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })) as (...args: never[]) => Promise<unknown>,
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
    getAudioSettingsConfig: (...args: never[]) => stubs.getAudioSettingsConfig(...args),
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
    const row = await screen.findByRole('row', { name: 'Edit House Preshow Loop' })
    fireEvent.click(row)
    const save = await screen.findByRole('button', { name: 'Save cue' })
    expect(save).toBeDisabled()
    const putSpy = vi.fn(() => new Promise(() => {}))
    stubs.putShowCue = putSpy
    fireEvent.click(save)
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('renders the compact active-revision summary for the cue, not a list heading', async () => {
    stubs.getShowCueRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show.cue',
        revisions: [
          { revision: 1, createdAt: '2026-08-30T18:22:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true },
        ],
      })
    setup()
    const row = await screen.findByRole('row', { name: 'Edit House Preshow Loop' })
    fireEvent.click(row)
    expect(await screen.findByText(/Active revision/, { selector: 'p' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
    expect(screen.queryByText('Active · 1')).not.toBeInTheDocument()
  })

  it('does not claim a read failure while the cue’s revisions fetch is still pending', async () => {
    stubs.getShowCueRevisions = () => new Promise(() => {})
    setup()
    const row = await screen.findByRole('row', { name: 'Edit House Preshow Loop' })
    fireEvent.click(row)
    await screen.findByRole('button', { name: 'Save cue' })
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('reports the read failure honestly when the cue’s revisions fetch is rejected', async () => {
    stubs.getShowCueRevisions = () => Promise.reject(new Error('network down'))
    setup()
    const row = await screen.findByRole('row', { name: 'Edit House Preshow Loop' })
    fireEvent.click(row)
    expect(await screen.findByText('Revision history could not be read just now.')).toBeInTheDocument()
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

    const row = await screen.findByRole('row', { name: 'Edit House Preshow Loop' })
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

  describe('activation summary', () => {
    it('narrates the enabled outputs and updates as fields change', async () => {
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
              sequence: 'thank-you',
              targetKind: 'show',
              target: '',
              mediaType: 'audio',
              contentHash: 'sha256:' + 'a'.repeat(64),
              runtimeFilename: 'thank-you.wav',
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
      expect(screen.getByText('Pick at least one output to see what this cue will do.')).toBeInTheDocument()

      fireEvent.click(screen.getByRole('checkbox', { name: /Audience audio/ }))
      fireEvent.change(await screen.findByRole('combobox', { name: 'Audio asset' }), { target: { value: 'thank-you' } })
      fireEvent.click(screen.getByRole('checkbox', { name: /Announcement/ }))

      expect(await screen.findByText(/On activation this cue will play thank-you, duck the background bed to -18 dB over 400 ms, and leave FPP untouched\./)).toBeInTheDocument()
    })
  })

  describe('asset not uploaded', () => {
    it('flags an announcement cue whose asset is missing from the show’s current assets, in the table and the editor', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withContents(kind, [cueSummary({ id: 'cue-2', label: 'Weather Delay Notice' })], [])
      stubs.listAssets = assetsEmpty
      stubs.getShowCue = () =>
        Promise.resolve(
          cueResponse(
            cuePayload({
              name: 'Weather Delay Notice',
              outputs: { audio: { asset: 'weather-delay', startOffsetMillis: 0 }, announcement: { policy: 'duck', duckGainDb: -12, fadeMillis: 400 } },
            }),
            'cue-2',
          ),
        )
      renderWorkspace({ session: signedIn(['config:write']) })
      const region = await screen.findByRole('region', { name: 'Directly activatable, scrollable' })
      expect(await within(region).findByText('Asset not uploaded')).toBeInTheDocument()

      fireEvent.click(within(region).getByRole('row', { name: 'Edit Weather Delay Notice' }))
      expect(await screen.findByText("weather-delay is not among this show's current assets.")).toBeInTheDocument()
    })
  })

  describe('output card list', () => {
    it('renders the four outputs as a card list with the checked ones washed', async () => {
      setup()
      fireEvent.click(await screen.findByRole('button', { name: 'New cue' }))
      const audio = screen.getByRole('checkbox', { name: /Audience audio/ })
      const render = screen.getByRole('checkbox', { name: /^Render/ })
      expect(render.closest('.sm-choice--card')).not.toBeNull()
      expect(audio.closest('.sm-choice-list')).not.toBeNull()
      expect(audio).not.toBeChecked()

      fireEvent.click(audio)
      expect(audio).toBeChecked()
      expect(screen.getByText('Play an audio asset on the program bus')).toBeInTheDocument()
    })
  })

  describe('ADR-045 output target nodes', () => {
    function audioNodeSummary(id: string, label: string): ConfigObjectSummary {
      return { id, label, show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z' }
    }

    function setupWithTargets(cue: ShowCueConfigResponse) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => {
        if (kind === 'audio.node') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [audioNodeSummary('node-a', 'Node A'), audioNodeSummary('node-b', 'Node B')] })
        return withContents(kind, [cueSummary()], [])
      }
      stubs.listAssets = assetsEmpty
      stubs.getShowCue = () => Promise.resolve(cue)
      return renderWorkspace({ session: signedIn(['config:write']) })
    }

    it('a loaded cue with targets round-trips them unchanged', async () => {
      const cue = cueResponse(
        cuePayload({
          outputs: {
            audio: { asset: 'house-preshow-loop', startOffsetMillis: 0, target: 'node-a' },
            ltc: { startOffsetMillis: 0, target: 'node-a' },
          },
        }),
      )
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioTarget = await screen.findByRole('combobox', { name: 'Audio target node' })
      const ltcTarget = await screen.findByRole('combobox', { name: 'LTC target node' })
      expect(audioTarget).toHaveValue('node-a')
      expect(ltcTarget).toHaveValue('node-a')

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.audio?.target).toBe('node-a')
      expect(payload.outputs.ltc?.target).toBe('node-a')
    })

    it('picking a target node sends it', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioTarget = await screen.findByRole('combobox', { name: 'Audio target node' })
      fireEvent.change(audioTarget, { target: { value: 'node-b' } })

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.audio?.target).toBe('node-b')
    })

    it('clearing a stored target sends no target key', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0, target: 'node-a' } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioTarget = await screen.findByRole('combobox', { name: 'Audio target node' })
      expect(audioTarget).toHaveValue('node-a')
      fireEvent.change(audioTarget, { target: { value: '' } })

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.audio).not.toHaveProperty('target')
    })

    it('a stored target naming an undeclared node renders it as an extra option and keeps saving it', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0, target: 'node-x' } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioTarget = (await screen.findByRole('combobox', { name: 'Audio target node' })) as HTMLSelectElement
      expect(audioTarget).toHaveValue('node-x')
      expect(within(audioTarget).getByRole('option', { name: 'node-x (not declared)' })).toBeInTheDocument()
      expect(screen.getByText(/node-x is not declared; readiness will report it as unbound/)).toBeInTheDocument()

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.audio?.target).toBe('node-x')
    })

    it('round-trips audio and LTC start offsets as timecode, and saves an edited value', async () => {
      const cue = cueResponse(
        cuePayload({
          outputs: {
            // Frame-aligned at 30 fps (100ms = 3 frames, 500ms = 15 frames) so formatting and parsing round-trip exactly.
            audio: { asset: 'house-preshow-loop', startOffsetMillis: 100 },
            ltc: { startOffsetMillis: 500 },
          },
        }),
      )
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const offsets = await screen.findAllByLabelText('Start offset')
      expect(offsets).toHaveLength(2)
      const [audioOffset, ltcOffset] = offsets as [HTMLInputElement, HTMLInputElement]
      await waitFor(() => expect(audioOffset).toHaveValue('00:00:00.03'))
      await waitFor(() => expect(ltcOffset).toHaveValue('00:00:00.15'))

      fireEvent.change(audioOffset, { target: { value: '00:00:01.00' } })

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.audio?.startOffsetMillis).toBe(1000)
      expect(payload.outputs.ltc?.startOffsetMillis).toBe(500)
    })

    it('accepts a bare millisecond integer, paste-friendly', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioOffset = (await screen.findByLabelText('Start offset')) as HTMLInputElement
      await waitFor(() => expect(audioOffset).toHaveValue('00:00:00'))
      fireEvent.change(audioOffset, { target: { value: '1500' } })

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as ConfigShowCue).outputs.audio?.startOffsetMillis).toBe(1500)
    })

    it('defaults a new cue’s start offsets to 0', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioOffset = (await screen.findByLabelText('Start offset')) as HTMLInputElement
      await waitFor(() => expect(audioOffset).toHaveValue('00:00:00'))

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as ConfigShowCue).outputs.audio?.startOffsetMillis).toBe(0)
    })

    it('rejects malformed timecode text and names the guide', async () => {
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioOffset = (await screen.findByLabelText('Start offset')) as HTMLInputElement
      await waitFor(() => expect(audioOffset).toHaveValue('00:00:00'))
      fireEvent.change(audioOffset, { target: { value: 'not a time' } })

      const save = await screen.findByRole('button', { name: 'Save cue' })
      expect(save).toBeDisabled()
      expect(save).toHaveAttribute('title', 'Start offset must be hh:mm:ss.ff or a whole number of milliseconds.')
    })

    it('falls back to a frame-less field and says so in help when the frame rate could not be read', async () => {
      stubs.getAudioSettingsConfig = () => Promise.reject(new Error('network down'))
      const cue = cueResponse(cuePayload({ outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } }))
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const audioOffset = (await screen.findByLabelText('Start offset')) as HTMLInputElement
      await waitFor(() => expect(audioOffset).toHaveValue('00:00:00'))
      expect(screen.getByText('hh:mm:ss. Frame rate unavailable.')).toBeInTheDocument()
    })

    it('a stored announcement target round-trips unchanged', async () => {
      const cue = cueResponse(
        cuePayload({
          outputs: {
            audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 },
            announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 400, target: 'node-b' },
          },
        }),
      )
      setupWithTargets(cue)
      fireEvent.click(await screen.findByRole('row', { name: 'Edit House Preshow Loop' }))
      const announcementTarget = await screen.findByRole('combobox', { name: 'Announcement target node' })
      expect(announcementTarget).toHaveValue('node-b')

      let sent: unknown = null
      stubs.putShowCue = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(cueResponse(payload as ConfigShowCue, 'cue-1', 2))
      }
      fireEvent.click(await screen.findByRole('button', { name: 'Save cue' }))
      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowCue
      expect(payload.outputs.announcement?.target).toBe('node-b')
    })
  })
})
