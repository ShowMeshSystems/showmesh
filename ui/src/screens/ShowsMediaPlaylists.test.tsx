import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Asset, type ConfigMediaPlaylist, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getMediaPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getMediaPlaylistRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putMediaPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  deleteMediaPlaylist: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    getMediaPlaylist: (...args: never[]) => stubs.getMediaPlaylist(...args),
    getMediaPlaylistRevisions: (...args: never[]) => stubs.getMediaPlaylistRevisions(...args),
    putMediaPlaylist: (...args: never[]) => stubs.putMediaPlaylist(...args),
    deleteMediaPlaylist: (...args: never[]) => stubs.deleteMediaPlaylist(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsMediaPlaylists } = await import('./ShowsMediaPlaylists')

function summary(overrides: Partial<{ id: string; label: string; show: string; currentRevision: number; updatedAt: string }> = {}) {
  return { id: 'mp1', label: 'Resting bed', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function audioAsset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: 'asset-1', show: 'winter-ridge-2026', sequence: 'winter-loop', targetKind: 'node', target: 'node-1',
    mediaType: 'audio', contentHash: 'sha256:' + 'a'.repeat(64), runtimeFilename: 'winter-loop.wav', sizeBytes: 100,
    createdAt: '2026-08-30T18:00:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos',
    supersededAt: null, current: true,
    ...overrides,
  }
}

function bed(overrides: Partial<ConfigMediaPlaylist> = {}): ConfigMediaPlaylist {
  return {
    label: 'Resting bed', show: 'winter-ridge-2026',
    items: [{ kind: 'asset', show: 'winter-ridge-2026', sequence: 'winter-loop', target: 'node-1' }],
    repeat: 'playlist', resume: 'resume', itemTransition: 'sequential', maxGainDb: -6,
    ...overrides,
  }
}

function bedResponse(payload: ConfigMediaPlaylist, id = 'mp1', revision = 1) {
  return {
    serverTime: '2026-08-30T21:00:00Z', kind: 'media.playlist' as const, id, revision, payload,
    updatedAt: '2026-08-30T18:22:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api' as const,
  }
}

function showHead() {
  return Promise.resolve({
    serverTime: '2026-08-30T21:00:00Z', kind: 'show', id: 'winter-ridge-2026', revision: 47,
    payload: { name: 'Winter Ridge 2026', notes: '' }, updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api',
  })
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z', authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session', scopes, scopesState: 'current', bootstrapRequired: false,
  } as unknown as SessionResponse
}

function withEmptyLists(kind: string) {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/media-playlists') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="media-playlists" element={<ShowsMediaPlaylists />} />
            <Route path="playlists" element={<ShowsTabPlaceholder tab="Playlists" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

async function openRow(label: string) {
  const row = await screen.findByRole('row', { name: `Edit ${label}` })
  fireEvent.click(row)
}

describe('Shows · Media Playlists tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('states the show.playlist / media.playlist distinction verbatim', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withEmptyLists(kind)
    stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
    renderWorkspace({ session: signedIn(['config:write']) })
    await waitFor(() => expect(screen.getByText('This show has no media playlist configured.')).toBeInTheDocument())
    expect(screen.getByText('is a list of cues a runner steps through.', { exact: false })).toBeInTheDocument()
    expect(screen.getByText('is a list of things the audio engine plays as a bed.', { exact: false })).toBeInTheDocument()
  })

  describe('list rendering', () => {
    it('renders the empty state when the show has no media playlist', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withEmptyLists(kind)
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
      renderWorkspace({ session: signedIn(['config:write']) })
      await waitFor(() => expect(screen.getByText('This show has no media playlist configured.')).toBeInTheDocument())
    })

    it('renders a media playlist row with its item count', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => (kind === 'media.playlist' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : withEmptyLists(kind))
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [audioAsset()] })
      stubs.getMediaPlaylist = (id: string) => Promise.resolve(bedResponse(bed(), id))
      renderWorkspace({ session: signedIn(['config:write']) })
      await waitFor(() => expect(screen.getByRole('row', { name: 'Edit Resting bed' })).toBeInTheDocument())
      const row = screen.getByRole('row', { name: 'Edit Resting bed' })
      expect(within(row).getByText('1')).toBeInTheDocument()
    })
  })

  describe('creating and editing through the put route', () => {
    function setup(scopes: string[] = ['config:write']) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => (kind === 'media.playlist' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : withEmptyLists(kind))
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [audioAsset(), audioAsset({ id: 'asset-2', sequence: 'chase-loop', target: 'node-2' })] })
      stubs.getMediaPlaylist = (id: string) => Promise.resolve(bedResponse(bed(), id))
      return renderWorkspace({ session: signedIn(scopes) })
    }

    it('editing the repeat field and saving sends the built payload to putMediaPlaylist', async () => {
      setup()
      await openRow('Resting bed')
      await waitFor(() => expect(screen.getByRole('button', { name: 'None' })).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: 'None' }))

      let sent: unknown = null
      let sentId: string | null = null
      stubs.putMediaPlaylist = (id: string, payload: unknown) => {
        sentId = id
        sent = payload
        return Promise.resolve(bedResponse(payload as ConfigMediaPlaylist, id, 2))
      }
      fireEvent.click(screen.getByRole('button', { name: 'Save media playlist' }))

      await waitFor(() => expect(sent).not.toBeNull())
      expect(sentId).toBe('mp1')
      const payload = sent as ConfigMediaPlaylist
      expect(payload.repeat).toBe('none')
      expect(payload.show).toBe('winter-ridge-2026')
      expect(payload.items).toEqual([{ kind: 'asset', show: 'winter-ridge-2026', sequence: 'winter-loop', target: 'node-1' }])
    })

    it('creating a new media playlist sends label, show, and the first item to putMediaPlaylist', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withEmptyLists(kind)
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [audioAsset()] })
      renderWorkspace({ session: signedIn(['config:write']) })
      await waitFor(() => expect(screen.getByText('This show has no media playlist configured.')).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: 'New media playlist' }))
      fireEvent.change(screen.getByLabelText('Label'), { target: { value: 'Chase bed' } })
      fireEvent.change(screen.getByRole('combobox', { name: /Audio asset for item 1/ }), { target: { value: 'asset-1' } })
      fireEvent.change(screen.getByLabelText('Maximum gain (dB)'), { target: { value: '-6' } })

      stubs.getMediaPlaylist = () => Promise.reject(new ApiError('not found', 404, 'not-found'))
      let sent: unknown = null
      stubs.putMediaPlaylist = (id: string, payload: unknown) => {
        sent = payload
        return Promise.resolve(bedResponse(payload as ConfigMediaPlaylist, id))
      }
      fireEvent.click(screen.getByRole('button', { name: 'Create media playlist' }))

      await waitFor(() => expect(sent).not.toBeNull())
      const payload = sent as ConfigMediaPlaylist
      expect(payload.label).toBe('Chase bed')
      expect(payload.show).toBe('winter-ridge-2026')
      expect(payload.items).toEqual([{ kind: 'asset', show: 'winter-ridge-2026', sequence: 'winter-loop', target: 'node-1' }])
    })
  })

  describe('deleting', () => {
    function setup(scopes: string[] = ['config:write']) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => (kind === 'media.playlist' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : withEmptyLists(kind))
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [audioAsset()] })
      stubs.getMediaPlaylist = (id: string) => Promise.resolve(bedResponse(bed(), id))
      return renderWorkspace({ session: signedIn(scopes) })
    }

    it('is inert until the label is typed exactly, then calls deleteMediaPlaylist', async () => {
      setup()
      await openRow('Resting bed')
      await waitFor(() => expect(screen.getByRole('button', { name: 'Delete media playlist' })).toBeInTheDocument())

      const deleteButton = screen.getByRole('button', { name: 'Delete media playlist' })
      expect(deleteButton).toBeDisabled()

      const deleteSpy = vi.fn(() => Promise.resolve())
      stubs.deleteMediaPlaylist = deleteSpy
      fireEvent.change(screen.getByLabelText('Type Resting bed to confirm'), { target: { value: 'Resting bed' } })
      expect(deleteButton).not.toBeDisabled()
      fireEvent.click(deleteButton)

      await waitFor(() => expect(deleteSpy).toHaveBeenCalledWith('mp1'))
      await waitFor(() => expect(screen.getByText('This show has no media playlist configured.')).toBeInTheDocument())
    })
  })

  describe('the server refuses a kind "cue" item', () => {
    it('surfaces the refusal exactly as the coordinator states it, without a client-side substitute', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => (kind === 'media.playlist' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : withEmptyLists(kind))
      stubs.listAssets = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [audioAsset()] })
      stubs.getMediaPlaylist = (id: string) => Promise.resolve(bedResponse(bed(), id))
      renderWorkspace({ session: signedIn(['config:write']) })
      await openRow('Resting bed')
      await waitFor(() => expect(screen.getByRole('button', { name: 'None' })).toBeInTheDocument())

      fireEvent.click(screen.getByRole('button', { name: 'None' }))
      stubs.putMediaPlaylist = () => Promise.reject(new ApiError('items[0].kind: kind "cue" is reserved but not yet supported', 400, 'invalid-parameter'))

      fireEvent.click(screen.getByRole('button', { name: 'Save media playlist' }))
      await waitFor(() => expect(screen.getByText('items[0].kind: kind "cue" is reserved but not yet supported')).toBeInTheDocument())
    })
  })
})
