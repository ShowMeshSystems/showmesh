import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { type Asset, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  uploadAsset: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    uploadAsset: (...args: never[]) => stubs.uploadAsset(...args),
  }
})

const { Assets } = await import('./Assets')

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

function assetsResponse(assets: Asset[]) {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets })
}

function showsResponse(shows: { id: string; label: string }[]) {
  return Promise.resolve({
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show' as const,
    objects: shows.map((s) => ({ id: s.id, label: s.label, show: s.id, currentRevision: 1, updatedAt: '2026-08-24T09:18:00Z' })),
  })
}

function renderLibrary(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={['/assets']}>
        <Routes>
          <Route path="/assets" element={<Assets />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Assets library', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function setup(scopes: string[], assets: Asset[], shows: { id: string; label: string }[] = []) {
    stubs.listAssets = () => assetsResponse(assets)
    stubs.listConfigObjects = () => showsResponse(shows)
    return renderLibrary({ session: signedIn(scopes) })
  }

  it('lists current assets across every show, each row tagged with its show', async () => {
    const winterRidge = asset({ id: 'a1', show: 'winter-ridge-2026' })
    const hallowedHollow = asset({
      id: 'a2',
      show: 'hallowed-hollow-2026',
      sequence: 'graveyard-shuffle',
      target: 'media-back',
      runtimeFilename: 'graveyard-shuffle.fseq',
    })
    setup(['asset:write'], [winterRidge, hallowedHollow])

    await waitFor(() => expect(screen.getByRole('heading', { name: 'Assets' })).toBeInTheDocument())
    const region = await screen.findByRole('region', { name: "Every show's current assets, grouped by sequence, scrollable" })
    await waitFor(() => expect(within(region).getByText('carol-of-the-bells')).toBeInTheDocument())
    expect(within(region).getByText('graveyard-shuffle')).toBeInTheDocument()
    expect(within(region).getByText('winter-ridge-2026')).toBeInTheDocument()
    expect(within(region).getByText('hallowed-hollow-2026')).toBeInTheDocument()
  })

  it('the show filter narrows the list to one show', async () => {
    const winterRidge = asset({ id: 'a1', show: 'winter-ridge-2026' })
    const hallowedHollow = asset({
      id: 'a2',
      show: 'hallowed-hollow-2026',
      sequence: 'graveyard-shuffle',
      target: 'media-back',
      runtimeFilename: 'graveyard-shuffle.fseq',
    })
    setup(['asset:write'], [winterRidge, hallowedHollow])

    const region = await screen.findByRole('region', { name: "Every show's current assets, grouped by sequence, scrollable" })
    await waitFor(() => expect(within(region).getByText('carol-of-the-bells')).toBeInTheDocument())

    fireEvent.change(screen.getByLabelText('Filter by show'), { target: { value: 'winter-ridge-2026' } })
    await waitFor(() => expect(within(region).queryByText('graveyard-shuffle')).not.toBeInTheDocument())
    expect(within(region).getByText('carol-of-the-bells')).toBeInTheDocument()
  })

  it('upload asks which show the asset belongs to, with no default', async () => {
    setup(['asset:write'], [], [{ id: 'winter-ridge-2026', label: 'Winter Ridge 2026' }])
    fireEvent.click(await screen.findByRole('button', { name: 'Upload' }))

    expect(await screen.findByText('Choose a show…')).toBeInTheDocument()
    fireEvent.change(await screen.findByLabelText('Logical sequence'), { target: { value: 'rooftop-finale' } })
    const file = new File(['bytes'], 'rooftop-finale.fseq')
    fireEvent.change(screen.getByLabelText(/Choose a file/i, { selector: 'input' }), { target: { files: [file] } })

    const submit = screen.getAllByRole('button', { name: 'Upload' })[1] as HTMLElement
    expect(submit).toBeDisabled()
    expect(submit).toHaveAttribute('title', 'Identity needs a show. There is no default.')

    fireEvent.change(screen.getByLabelText('Show'), { target: { value: 'winter-ridge-2026' } })
    expect(submit).not.toBeDisabled()
  })

  it('omits the four facts the coordinator does not report', async () => {
    setup(['asset:write'], [asset()])
    await screen.findByRole('heading', { name: 'Assets' })

    expect(screen.queryByText('On node')).not.toBeInTheDocument()
    expect(screen.queryByText('On target')).not.toBeInTheDocument()
    expect(screen.queryByText('Hash mismatch')).not.toBeInTheDocument()
    expect(screen.queryByText('Not synced')).not.toBeInTheDocument()
    expect(screen.queryByText('Rolled back')).not.toBeInTheDocument()
  })
})
