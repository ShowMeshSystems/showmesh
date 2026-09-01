import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Asset, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  uploadAsset: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAssetContent: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    uploadAsset: (...args: never[]) => stubs.uploadAsset(...args),
    getAssetContent: (...args: never[]) => stubs.getAssetContent(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsAssets } = await import('./ShowsAssets')

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

function asset(overrides: Partial<Asset> = {}): Asset {
  return {
    id: 'asset-1',
    show: 'winter-ridge-2026',
    sequence: 'carol-of-the-bells',
    targetKind: 'node',
    target: 'media-front',
    mediaType: 'fseq',
    contentHash: 'sha256:' + '9c41'.padEnd(64, 'a') + 'f',
    runtimeFilename: 'carol-of-the-bells.fseq',
    sizeBytes: 41200000,
    createdAt: '2026-08-24T09:18:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    supersededAt: null,
    current: true,
    ...overrides,
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

function assetsResponse(assets: Asset[]) {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets })
}

async function sha256HexFor(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  const hex = Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
  return `sha256:${hex}`
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/assets') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="assets" element={<ShowsAssets />} />
            <Route path="playlists" element={<ShowsTabPlaceholder tab="Playlists" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows · Assets tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function setup(scopes: string[], assets: Asset[]) {
    stubs.getShow = showHead
    // The workspace shell reads every config kind for its tab counts. Leaving
    // this unstubbed let a real network call decide when the screen painted.
    stubs.listConfigObjects = (kind: string) =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
    stubs.listAssets = () => assetsResponse(assets)
    return renderWorkspace({ session: signedIn(scopes) })
  }

  it('renders the section heading and a current asset grouped under its logical sequence', async () => {
    setup(['asset:write'], [asset()])
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Assets in this show' })).toBeInTheDocument())
    const region = await screen.findByRole('region', { name: "This show's current assets, grouped by sequence, scrollable" })
    await waitFor(() => expect(within(region).getByText('carol-of-the-bells')).toBeInTheDocument())
    expect(within(region).getByText('media-front')).toBeInTheDocument()
  })

  it('a settled-empty show is distinguishable from loading and from a read failure', async () => {
    setup(['asset:write'], [])
    const region = await screen.findByRole('region', { name: "This show's current assets, grouped by sequence, scrollable" })
    await waitFor(() => expect(within(region).getByText('None')).toBeInTheDocument())
    expect(within(region).getByText('No asset matches here.')).toBeInTheDocument()
  })

  it('a read failure is reported distinctly, with a retry', async () => {
    stubs.getShow = showHead
    stubs.listAssets = () => Promise.reject(new ApiError('Coordinator unreachable.', 503, 'unavailable'))
    renderWorkspace({ session: signedIn(['asset:write']) })
    expect(await screen.findByText('Coordinator unreachable.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('selecting a current row shows its identity and marks a superseded sibling distinctly in history', async () => {
    const current = asset({ id: 'a-current', contentHash: 'sha256:' + '55'.repeat(32), createdAt: '2026-08-26T14:02:00Z' })
    const superseded = asset({ id: 'a-old', contentHash: 'sha256:' + 'aa'.repeat(32), current: false, supersededAt: '2026-08-26T14:02:00Z', createdAt: '2026-08-24T09:18:00Z' })
    setup(['asset:write'], [current, superseded])
    fireEvent.click(await screen.findByRole('row', { name: 'View carol-of-the-bells for media-front' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'carol-of-the-bells' })).toBeInTheDocument())
    expect(screen.getByText('Current')).toBeInTheDocument()
    expect(screen.getByText('Superseded')).toBeInTheDocument()
  })

  it('the upload control is disabled without asset:write and is actually inert', async () => {
    setup([], [])
    fireEvent.click(await screen.findByRole('button', { name: 'Upload' }))
    const submit = (await screen.findAllByRole('button', { name: 'Upload' }))[1] as HTMLElement
    expect(submit).toBeDisabled()
    const uploadSpy = vi.fn(() => new Promise(() => {}))
    stubs.uploadAsset = uploadSpy
    fireEvent.click(submit)
    expect(uploadSpy).not.toHaveBeenCalled()
  })

  it('a node target is required with no default when targeting one node', async () => {
    setup(['asset:write'], [])
    fireEvent.click(await screen.findByRole('button', { name: 'Upload' }))
    fireEvent.change(await screen.findByLabelText('Logical sequence'), { target: { value: 'rooftop-finale' } })
    const file = new File(['bytes'], 'rooftop-finale.fseq')
    fireEvent.change(screen.getByLabelText(/Choose a file/i, { selector: 'input' }), { target: { files: [file] } })
    fireEvent.click(screen.getByRole('button', { name: 'One node' }))
    const submit = screen.getAllByRole('button', { name: 'Upload' })[1] as HTMLElement
    expect(submit).toBeDisabled()
    expect(submit).toHaveAttribute('title', 'Identity needs a target. There is no default.')
  })

  it('an upload reports rolledBack honestly rather than a plain success', async () => {
    setup(['asset:write'], [])
    fireEvent.click(await screen.findByRole('button', { name: 'Upload' }))
    fireEvent.change(await screen.findByLabelText('Logical sequence'), { target: { value: 'rooftop-finale' } })
    const file = new File(['bytes'], 'rooftop-finale.fseq')
    fireEvent.change(screen.getByLabelText(/Choose a file/i, { selector: 'input' }), { target: { files: [file] } })

    const rolledBackAsset = asset({ id: 'a-rollback', sequence: 'rooftop-finale', targetKind: 'show', target: '' })
    stubs.uploadAsset = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', asset: rolledBackAsset, rolledBack: true })

    const submit = screen.getAllByRole('button', { name: 'Upload' })[1] as HTMLElement
    fireEvent.click(submit)
    expect(await screen.findByText('Rolled back')).toBeInTheDocument()
    expect(
      screen.getByText('These bytes matched a superseded version, which is now current again; the previously current version is now superseded.'),
    ).toBeInTheDocument()
  })

  it('confirming Make current on a superseded row posts its fetched bytes and renders the rollback outcome', async () => {
    const current = asset({ id: 'a-current', contentHash: 'sha256:' + '55'.repeat(32), createdAt: '2026-08-26T14:02:00Z' })
    const superseded = asset({
      id: 'a-old',
      contentHash: 'sha256:' + 'aa'.repeat(32),
      current: false,
      supersededAt: '2026-08-26T14:02:00Z',
      createdAt: '2026-08-24T09:18:00Z',
    })
    setup(['asset:write'], [current, superseded])
    fireEvent.click(await screen.findByRole('row', { name: 'View carol-of-the-bells for media-front' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'carol-of-the-bells' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Make current' }))
    fireEvent.change(screen.getByLabelText(`Type ${superseded.sequence} to confirm the rollback`), {
      target: { value: superseded.sequence },
    })

    const getContentSpy = vi.fn(() => Promise.resolve(new Blob(['old-bytes'])))
    stubs.getAssetContent = getContentSpy
    const restored = asset({ id: 'a-old', current: true, contentHash: superseded.contentHash })
    const uploadSpy = vi.fn((file: File, fields: Record<string, unknown>) => {
      void file
      void fields
      return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', asset: restored, rolledBack: true })
    })
    stubs.uploadAsset = uploadSpy

    fireEvent.click(screen.getByRole('button', { name: 'Confirm rollback' }))

    expect(await screen.findByText('Rolled back')).toBeInTheDocument()
    expect(getContentSpy).toHaveBeenCalledWith('a-old')
    expect(uploadSpy).toHaveBeenCalledTimes(1)
    const [file, fields] = uploadSpy.mock.calls[0]!
    expect(file).toBeInstanceOf(File)
    expect(file.name).toBe(superseded.runtimeFilename)
    expect(fields).toEqual({
      show: superseded.show,
      sequence: superseded.sequence,
      mediaType: superseded.mediaType,
      targetKind: superseded.targetKind,
      target: superseded.target,
    })
  })

  it('per-entry history annotations reflect hash equality across the group, not a persisted flag', async () => {
    const current = asset({ id: 'a-current', contentHash: 'sha256:' + '55'.repeat(32), createdAt: '2026-08-26T14:02:00Z', current: true, supersededAt: null })
    const mid = asset({
      id: 'a-mid',
      contentHash: 'sha256:' + 'bb'.repeat(32),
      current: false,
      createdAt: '2026-08-24T09:18:00Z',
      supersededAt: '2026-08-26T14:02:00Z',
    })
    const original = asset({
      id: 'a-orig',
      contentHash: current.contentHash,
      current: false,
      createdAt: '2026-08-11T20:41:00Z',
      supersededAt: '2026-08-24T09:18:00Z',
    })
    setup(['asset:write'], [current, mid, original])
    fireEvent.click(await screen.findByRole('row', { name: 'View carol-of-the-bells for media-front' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'carol-of-the-bells' })).toBeInTheDocument())

    expect(screen.getByText(/Rollback, these exact bytes were current before/)).toBeInTheDocument()
    expect(screen.getByText('Same bytes as current')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Make current' })).toHaveLength(1)
  })

  it('a file matching a superseded identity warns before submit and relabels the control to Roll back', async () => {
    const matchHash = await sha256HexFor('rollback-bytes')
    const superseded = asset({
      id: 'a-old',
      current: false,
      targetKind: 'show',
      target: '',
      contentHash: matchHash,
      createdAt: '2026-08-11T20:41:00Z',
      supersededAt: '2026-08-24T09:18:00Z',
    })
    setup(['asset:write'], [superseded])
    fireEvent.click(await screen.findByRole('button', { name: 'Upload' }))
    fireEvent.change(await screen.findByLabelText('Logical sequence'), { target: { value: superseded.sequence } })
    const file = new File(['rollback-bytes'], 'carol-of-the-bells.fseq')
    fireEvent.change(screen.getByLabelText(/Choose a file/i, { selector: 'input' }), { target: { files: [file] } })

    expect(await screen.findByText('This will be a rollback')).toBeInTheDocument()
    const submit = (await screen.findAllByRole('button', { name: 'Roll back' }))[0] as HTMLElement
    expect(submit).toBeInTheDocument()
  })
})
