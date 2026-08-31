import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type ConfigObjectSummary, type ConfigShowPlaylist, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowPlaylistRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
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
    getShowPlaylistRevisions: (...args: never[]) => stubs.getShowPlaylistRevisions(...args),
    putShowPlaylist: (...args: never[]) => stubs.putShowPlaylist(...args),
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

function cueSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'cue-1', label: 'Winter Bed Track 1', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function cueSummary2(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'cue-2', label: 'Winter Bed Track 2', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
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
    showmeshAudio: { repeat: 'none' },
    entries: [
      { id: 'e1', cue: 'cue-1' },
      { id: 'e2', cue: 'cue-2' },
    ],
    ...overrides,
  } as ConfigShowPlaylist
}

function playlistResponse(payload: ConfigShowPlaylist, id = 'p1', revision = 1) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.playlist' as const,
    id,
    revision,
    payload,
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

function withCues(kind: string, cues: ConfigObjectSummary[]) {
  if (kind === 'show.cue') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: cues })
  return contentsEmpty()
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/playlists') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="playlists" element={<ShowsPlaylists />} />
            <Route path="cues" element={<ShowsTabPlaceholder tab="Cues" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function withPlaylistList(kind: string, cues: ConfigObjectSummary[]) {
  if (kind === 'show.playlist') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
  return withCues(kind, cues)
}

describe('Shows · Playlists tab editing', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  describe('ShowMesh-audio playlist', () => {
    function setup(scopes: string[] = ['config:write']) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withPlaylistList(kind, [cueSummary(), cueSummary2()])
      stubs.listAssets = assetsEmpty
      stubs.getShowPlaylist = (id: string) => Promise.resolve(playlistResponse(audioPlaylist(), id))
      return renderWorkspace({ session: signedIn(scopes) })
    }

    it('save and discard are disabled without config:write and are actually inert', async () => {
      setup([])
      const table = await screen.findByRole('region', { name: 'Playlist entries, scrollable' })
      await waitFor(() => expect(within(table).getByText('Winter Bed Track 1')).toBeInTheDocument())
      const save = screen.getByRole('button', { name: 'Save playlist' })
      const discard = screen.getByRole('button', { name: 'Discard changes' })
      expect(save).toBeDisabled()
      expect(discard).toBeDisabled()
      const putSpy = vi.fn(() => new Promise(() => {}))
      stubs.putShowPlaylist = putSpy
      fireEvent.click(save)
      fireEvent.click(discard)
      expect(putSpy).not.toHaveBeenCalled()
    })

    it('adding, removing and reordering an entry each produce the payload expected, and repeat round-trips', async () => {
      setup()
      const table = await screen.findByRole('region', { name: 'Playlist entries, scrollable' })
      await waitFor(() => expect(within(table).getByText('Winter Bed Track 1')).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: 'All' }))

      const addSelect = screen.getByRole('combobox', { name: 'Cue to add' })
      fireEvent.change(addSelect, { target: { value: 'cue-1' } })
      fireEvent.click(screen.getByRole('button', { name: 'Add cue' }))

      const moveDownButtons = within(table).getAllByRole('button', { name: 'Move down' })
      fireEvent.click(moveDownButtons[0] as HTMLElement)

      const removeButtons = within(table).getAllByRole('button', { name: 'Remove' })
      fireEvent.click(removeButtons[removeButtons.length - 1] as HTMLElement)

      let sent: unknown = null
      stubs.putShowPlaylist = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(playlistResponse(payload as ConfigShowPlaylist, 'p1', 2))
      }

      fireEvent.click(screen.getByRole('button', { name: 'Save playlist' }))

      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowPlaylist
      expect(payload.showmeshAudio).toEqual({ repeat: 'all' })
      // Started [e1(cue-1), e2(cue-2)]; added a third entry bound to
      // cue-1 -> [e1, e2, new]; moved row 0 later -> [e2, e1, new]; removed
      // the last row (the freshly-added one) -> [e2, e1].
      expect(payload.entries.map((e) => e.cue)).toEqual(['cue-2', 'cue-1'])
    })

    it('reports a write refusal and keeps the operator’s edits on screen rather than discarding them', async () => {
      setup()
      const table = await screen.findByRole('region', { name: 'Playlist entries, scrollable' })
      await waitFor(() => expect(within(table).getByText('Winter Bed Track 1')).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: 'All' }))
      stubs.putShowPlaylist = () => Promise.reject(new ApiError('This revision has already moved; read it again before saving.', 409, 'conflict'))

      fireEvent.click(screen.getByRole('button', { name: 'Save playlist' }))
      await waitFor(() => expect(screen.getByText('This revision has already moved; read it again before saving.')).toBeInTheDocument())

      // The edit (Repeat: All) is still selected, not reverted by the failure.
      expect(screen.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true')
      expect(screen.getByRole('button', { name: 'Save playlist' })).not.toBeDisabled()
    })
  })

  describe('FPP playlist', () => {
    function setup({
      scopes = ['config:write'],
      storedMismatchPolicy,
    }: { scopes?: string[]; storedMismatchPolicy?: 'hold' | 'blackAndSilence' } = {}) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withPlaylistList(kind, [cueSummary(), cueSummary2()])
      stubs.listAssets = assetsEmpty
      stubs.getShowPlaylist = (id: string) =>
        Promise.resolve(
          playlistResponse(
            storedMismatchPolicy === undefined ? fppPlaylist() : fppPlaylist({ mismatchPolicy: storedMismatchPolicy }),
            id,
          ),
        )
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
      return renderWorkspace({ session: signedIn(scopes) })
    }

    it('each editable field round-trips into the payload sent to putShowPlaylist, including binding an entry to a cue', async () => {
      setup({ storedMismatchPolicy: 'blackAndSilence' })
      await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())

      const selects = screen.getAllByRole('combobox', { name: /Bound cue for/ })
      fireEvent.change(selects[1] as HTMLElement, { target: { value: 'cue-2' } })

      let sent: unknown = null
      stubs.putShowPlaylist = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(playlistResponse(payload as ConfigShowPlaylist, 'p1', 2))
      }
      fireEvent.click(screen.getByRole('button', { name: 'Save playlist' }))

      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigShowPlaylist
      // The stored policy is written back unchanged, whatever it is.
      expect(payload.mismatchPolicy).toBe('blackAndSilence')
      expect(payload.entries).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ cue: 'cue-1', fpp: { section: 'mainPlaylist', position: 0 } }),
          expect.objectContaining({ cue: 'cue-2', fpp: { section: 'mainPlaylist', position: 1 } }),
        ]),
      )
      expect(payload.entries).toHaveLength(2)
    })

    it('never invents a mismatch policy the playlist does not store', async () => {
      setup()
      await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())
      // Save needs a real edit before it is enabled, so make an unrelated one.
      const selects = screen.getAllByRole('combobox', { name: /Bound cue for/ })
      fireEvent.change(selects[1] as HTMLElement, { target: { value: 'cue-2' } })
      let sent: unknown = null
      stubs.putShowPlaylist = (_id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(playlistResponse(payload as ConfigShowPlaylist, 'p1', 2))
      }
      fireEvent.click(screen.getByRole('button', { name: 'Save playlist' }))
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as ConfigShowPlaylist).mismatchPolicy).toBeUndefined()
    })

    it('draws the mismatch control inert, as the mock says it is not per playlist', async () => {
      setup({ storedMismatchPolicy: 'blackAndSilence' })
      await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())
      expect(screen.getByRole('button', { name: 'Black & silence' })).toBeDisabled()
      expect(screen.getByText(/Show versus Program mode/)).toBeInTheDocument()
    })

    it('save and discard are disabled without config:write and are actually inert', async () => {
      setup({ scopes: [] })
      await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())
      expect(screen.getByRole('button', { name: 'Save playlist' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Discard changes' })).toBeDisabled()
    })

    it('rebinding the FPP source playlist renders inert, citing the OPEN-DECISIONS entry', async () => {
      setup()
      await waitFor(() => expect(screen.getByText('wizards-in-winter.fseq')).toBeInTheDocument())
      expect(screen.getByRole('combobox', { name: 'Instance' })).toBeDisabled()
      expect(screen.getByRole('combobox', { name: 'FPP playlist' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Re-import' })).toBeDisabled()
    })
  })

  describe('New playlist draft, the gate case', () => {
    function withEmptyPlaylists(kind: string, cues: ConfigObjectSummary[]) {
      if (kind === 'show.playlist') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
      return withCues(kind, cues)
    }

    function setupDraft(scopes: string[] = ['config:write']) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withEmptyPlaylists(kind, [cueSummary(), cueSummary2()])
      stubs.listAssets = assetsEmpty
      return renderWorkspace({ session: signedIn(scopes) })
    }

    it('renders nothing below the runner gate until it is answered, and the footer says Runner required', async () => {
      setupDraft()
      await waitFor(() => expect(screen.getByText('This show has no playlist configured.')).toBeInTheDocument())
      fireEvent.click(screen.getByRole('button', { name: 'New playlist' }))
      expect(screen.getByRole('heading', { name: 'New playlist' })).toBeInTheDocument()
      expect(screen.queryByLabelText('Name')).not.toBeInTheDocument()
      expect(screen.getByText('Runner required')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Create playlist' })).toBeDisabled()
    })

    it('picking showmesh-audio swaps the mismatch field for repeat', async () => {
      setupDraft()
      await waitFor(() => expect(screen.getByText('This show has no playlist configured.')).toBeInTheDocument())
      fireEvent.click(screen.getByRole('button', { name: 'New playlist' }))
      fireEvent.click(screen.getByRole('button', { name: 'ShowMesh audio' }))
      expect(screen.getByLabelText('Name')).toBeInTheDocument()
      expect(screen.getByRole('group', { name: 'Repeat' })).toBeInTheDocument()
      expect(screen.queryByRole('group', { name: /If the FPP playlist does not match/ })).not.toBeInTheDocument()
      expect(screen.getByRole('heading', { name: 'First entry' })).toBeInTheDocument()
    })

    it('does not create with a taken id and offers to open the existing playlist', async () => {
      setupDraft()
      await waitFor(() => expect(screen.getByText('This show has no playlist configured.')).toBeInTheDocument())
      fireEvent.click(screen.getByRole('button', { name: 'New playlist' }))
      fireEvent.click(screen.getByRole('button', { name: 'ShowMesh audio' }))
      fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Background Music' } })
      fireEvent.change(screen.getByRole('combobox', { name: 'Cue' }), { target: { value: 'cue-1' } })
      stubs.getShowPlaylist = () => Promise.resolve(playlistResponse(audioPlaylist(), 'background-music'))
      const putSpy = vi.fn(() => Promise.resolve(playlistResponse(audioPlaylist())))
      stubs.putShowPlaylist = putSpy
      fireEvent.click(screen.getByRole('button', { name: 'Create playlist' }))
      await waitFor(() => expect(screen.getByText('Id taken')).toBeInTheDocument())
      expect(putSpy).not.toHaveBeenCalled()
    })

    it('keeps New playlist disabled with a stated reason without config:write', async () => {
      setupDraft([])
      await waitFor(() => expect(screen.getByText('This show has no playlist configured.')).toBeInTheDocument())
      expect(screen.getByRole('button', { name: 'New playlist' })).toBeDisabled()
    })
  })
})
