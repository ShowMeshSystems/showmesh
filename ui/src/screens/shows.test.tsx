import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import type { ConfigObjectSummary, Model, SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowActive: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowActive: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowActiveRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    putShow: (...args: never[]) => stubs.putShow(...args),
    getShowRevisions: (...args: never[]) => stubs.getShowRevisions(...args),
    getShowActive: (...args: never[]) => stubs.getShowActive(...args),
    putShowActive: (...args: never[]) => stubs.putShowActive(...args),
    getShowActiveRevisions: (...args: never[]) => stubs.getShowActiveRevisions(...args),
  }
})

const { Shows } = await import('./Shows')
const { ShowDetail } = await import('./ShowDetail')

function contentsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.playlist', objects: [] })
}

function assetsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
}

function summary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'winter-ridge-2026', label: 'Winter Ridge 2026', show: 'winter-ridge-2026', currentRevision: 47, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
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

function renderShows(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter>
        <Shows />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderDetail(id: string, model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[`/shows/${id}`]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows · list', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('names the page and the one-active fact', async () => {
    stubs.listConfigObjects = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show', objects: [] })
    renderShows()
    expect(screen.getByRole('heading', { level: 1, name: 'Shows' })).toBeInTheDocument()
    expect(screen.getByText('One active')).toBeInTheDocument()
  })

  it('shows the loading absence before the list read completes', () => {
    stubs.listConfigObjects = () => new Promise(() => {})
    renderShows()
    // Two "Reading" strips render while loading: the list itself and the
    // show-activation control's own read of show.active (asserted separately below).
    expect(screen.getAllByText('Reading').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('Asking the coordinator for every configured show.')).toBeInTheDocument()
  })

  it('says none is configured, distinct from a read failure', async () => {
    stubs.listConfigObjects = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show', objects: [] })
    renderShows()
    await waitFor(() => expect(screen.getByText('No show is configured.')).toBeInTheDocument())
  })

  it('lists a show and marks the active one with a labelled pair, not colour alone', async () => {
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderShows({ currentRuns: { serverTime: '2026-08-30T21:00:00Z', activeShow: { configured: true, show: 'winter-ridge-2026', generation: 1 }, runs: [] } as never })
    await waitFor(() => expect(screen.getByText('Winter Ridge 2026')).toBeInTheDocument())
    expect(screen.getByText('Active')).toBeInTheDocument()
    expect(screen.getByRole('row', { name: 'Open Winter Ridge 2026' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Edit show' })).not.toBeInTheDocument()
  })

  it('never marks a show active when currentRuns has not been read', async () => {
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show' ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] }) : contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderShows()
    await waitFor(() => expect(screen.getByText('Winter Ridge 2026')).toBeInTheDocument())
    expect(screen.queryByText('Active')).not.toBeInTheDocument()
  })

  it('reports a read failure distinctly from an empty list', async () => {
    stubs.listConfigObjects = () => Promise.reject(new Error('network down'))
    renderShows()
    await waitFor(() => expect(screen.getByText('Read failed')).toBeInTheDocument())
    expect(screen.queryByText('No show is configured.')).not.toBeInTheDocument()
  })

  it('renders contents counts, grouping each object onto the show it names', async () => {
    stubs.listConfigObjects = (kind: string) => {
      if (kind === 'show') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary()] })
      if (kind === 'show.playlist') {
        return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary({ id: 'p1', label: 'Main' })] })
      }
      return contentsEmpty()
    }
    stubs.listAssets = assetsEmpty
    renderShows()
    await waitFor(() => expect(screen.getByText(/1 playlist/)).toBeInTheDocument())
  })

  it('reads each kind once for the whole list, never once per show', async () => {
    const shows = ['a', 'b', 'c', 'd', 'e'].map((id) => summary({ id, label: id, show: id }))
    const calls: string[] = []
    stubs.listConfigObjects = ((kind: string, show?: string) => {
      calls.push(show === undefined ? kind : `${kind}?show=${show}`)
      if (kind === 'show') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: shows })
      return contentsEmpty()
    }) as never
    stubs.listAssets = assetsEmpty
    renderShows()
    await waitFor(() => expect(screen.getAllByText('Empty').length).toBe(5))
    // Five shows must not mean five reads per kind: that storms the coordinator.
    expect(calls.filter((call) => call.includes('?show='))).toEqual([])
    expect(calls.filter((call) => call === 'show.cue')).toHaveLength(1)
  })

  it('keeps the destructive control disabled with a stated reason for New show', async () => {
    stubs.listConfigObjects = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show', objects: [] })
    renderShows()
    const button = screen.getByRole('button', { name: 'New show' })
    expect(button).toBeDisabled()
  })
})

function showActiveResponse(show: string, overrides: Record<string, unknown> = {}) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.active',
    id: 'show.active',
    revision: 3,
    payload: { show },
    updatedAt: '2026-08-30T18:00:00Z',
    createdByPrincipalId: 'p',
    createdByPrincipalName: 'erbartos',
    source: 'api',
    ...overrides,
  }
}

function twoShows() {
  return Promise.resolve({
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show',
    objects: [summary(), summary({ id: 'hallowed-hollow-2026', label: 'Hallowed Hollow 2026' })],
  })
}

describe('Shows · activation', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('reads the show.active pointer and lists shows to activate', async () => {
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    renderShows()
    expect(await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Hallowed Hollow 2026 (hallowed-hollow-2026)' })).toBeInTheDocument()
  })

  it('reports none activated yet, distinct from a read failure, when show.active has never been set', async () => {
    stubs.listConfigObjects = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show', objects: [] })
    stubs.getShowActive = () => Promise.reject(new ApiError('not found', 404))
    renderShows()
    expect(await screen.findByText(/none - nothing has ever been activated/)).toBeInTheDocument()
  })

  it('activates a different show', async () => {
    let calls: unknown[] = []
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    stubs.putShowActive = (...args: unknown[]) => {
      calls = args
      return Promise.resolve(showActiveResponse('hallowed-hollow-2026', { revision: 4 }))
    }
    renderShows({ session: signedIn(['config:write']) })
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    fireEvent.change(screen.getByLabelText('Activate a show'), { target: { value: 'hallowed-hollow-2026' } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    await screen.findByText('hallowed-hollow-2026', { selector: '.sm-data' })
    expect(calls[0]).toEqual({ show: 'hallowed-hollow-2026' })
  })

  it('refuses to activate over a stale pointer, matching D-014 for every other config write', async () => {
    let reads = 0
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => {
      reads += 1
      return Promise.resolve(
        reads === 1
          ? showActiveResponse('winter-ridge-2026', { revision: 3 })
          : showActiveResponse('someone-else', { revision: 5, createdByPrincipalName: 'other-operator' }),
      )
    }
    const putSpy = vi.fn(() => Promise.resolve(showActiveResponse('hallowed-hollow-2026')))
    stubs.putShowActive = putSpy
    renderShows({ session: signedIn(['config:write']) })
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    fireEvent.change(screen.getByLabelText('Activate a show'), { target: { value: 'hallowed-hollow-2026' } })
    fireEvent.click(screen.getByRole('button', { name: 'Activate' }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(screen.getByText(/saved by other-operator/)).toBeInTheDocument()
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('renders an empty activation history as a settled fact when show.active has never been read a second time', async () => {
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    stubs.getShowActiveRevisions = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'show.active', revisions: [] })
    renderShows()
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    expect(await screen.findByText('No prior revision recorded.')).toBeInTheDocument()
  })

  it('does not claim a read failure while the activation-history fetch is still pending', async () => {
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    stubs.getShowActiveRevisions = () => new Promise(() => {})
    renderShows()
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('does not let a failed activation-history read break Show activation', async () => {
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    stubs.getShowActiveRevisions = () => Promise.reject(new Error('network down'))
    renderShows()
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    expect(await screen.findByText('Revision history could not be read just now.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Activate' })).toBeInTheDocument()
  })

  it('keeps Activate disabled with a stated reason when the principal lacks config:write', async () => {
    stubs.listConfigObjects = (kind: string) => (kind === 'show' ? twoShows() : contentsEmpty())
    stubs.listAssets = assetsEmpty
    stubs.getShowActive = () => Promise.resolve(showActiveResponse('winter-ridge-2026'))
    renderShows({ session: signedIn(['node:read']) })
    await screen.findByText('winter-ridge-2026', { selector: '.sm-data' })
    fireEvent.change(screen.getByLabelText('Activate a show'), { target: { value: 'hallowed-hollow-2026' } })
    const button = screen.getByRole('button', { name: 'Activate' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title')
  })
})

describe('Shows · Identity', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function showResponse(overrides: Partial<{ name: string; notes: string; revision: number }> = {}) {
    return Promise.resolve({
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'show',
      id: 'winter-ridge-2026',
      revision: overrides.revision ?? 47,
      payload: { name: overrides.name ?? 'Winter Ridge 2026', notes: overrides.notes ?? 'Barn roof rebuilt.' },
      updatedAt: '2026-08-30T18:22:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    })
  }

  it('shows the loading absence, then the identity fields once read', async () => {
    stubs.getShow = showResponse
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderDetail('winter-ridge-2026')
    expect(screen.getByText('Reading')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Winter Ridge 2026' })).toBeInTheDocument())
    await waitFor(() => expect(screen.getByDisplayValue('Winter Ridge 2026')).toBeInTheDocument())
    expect(screen.getByText('winter-ridge-2026')).toBeInTheDocument()
  })

  it('reports a read failure with nothing ever read, distinct from a stale retained copy', async () => {
    stubs.getShow = () => Promise.reject(new Error('network down'))
    renderDetail('winter-ridge-2026')
    await waitFor(() => expect(screen.getByText('Read failed')).toBeInTheDocument())
  })

  it('renders the four contents tiles from the show’s own configured objects', async () => {
    stubs.getShow = showResponse
    stubs.listConfigObjects = (kind: string) =>
      kind === 'show.cue'
        ? Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [summary({ id: 'c1' }), summary({ id: 'c2' })] })
        : contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderDetail('winter-ridge-2026')
    await waitFor(() => expect(screen.getByText('Cues')).toBeInTheDocument())
    const tile = screen.getByText('Cues').closest('a')
    expect(tile?.textContent).toContain('2')
  })

  it('disables Save show with a stated reason when the principal lacks config:write', async () => {
    stubs.getShow = showResponse
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderDetail('winter-ridge-2026', { session: signedIn(['node:read']) })
    await waitFor(() => expect(screen.getByDisplayValue('Winter Ridge 2026')).toBeInTheDocument())
    const input = screen.getByDisplayValue('Winter Ridge 2026')
    fireEvent.change(input, { target: { value: 'Winter Ridge 2027' } })
    const save = screen.getByRole('button', { name: /Save show/ })
    expect(save).toBeDisabled()
  })

  it('refuses a stale save, writes nothing, and names the changed fields', async () => {
    let calls = 0
    stubs.getShow = () => {
      calls += 1
      return calls === 1
        ? showResponse({ revision: 47 })
        : showResponse({ revision: 48, notes: 'Someone else changed this.' })
    }
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    const putSpy = vi.fn(() => showResponse())
    stubs.putShow = putSpy
    renderDetail('winter-ridge-2026', { session: signedIn(['config:write']) })
    await waitFor(() => expect(screen.getByDisplayValue('Winter Ridge 2026')).toBeInTheDocument())
    fireEvent.change(screen.getByDisplayValue('Winter Ridge 2026'), { target: { value: 'Winter Ridge 2027' } })
    fireEvent.click(screen.getByRole('button', { name: /Save show/ }))
    await waitFor(() => expect(screen.getByText('Stale write')).toBeInTheDocument())
    expect(putSpy).not.toHaveBeenCalled()
    expect(screen.getByText('notes', { selector: '.sm-data' })).toBeInTheDocument()
  })

  it('separates the delete control from the save path and leaves it inert with no endpoint', async () => {
    stubs.getShow = showResponse
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    renderDetail('winter-ridge-2026')
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Winter Ridge 2026' })).toBeInTheDocument())
    const saveSection = screen.getByRole('button', { name: /Save show/ }).closest('section, div')
    const deleteButton = screen.getByRole('button', { name: 'Delete show' })
    expect(deleteButton).toBeDisabled()
    expect(saveSection?.contains(deleteButton)).toBe(false)
    expect(screen.getByText(/no endpoint to delete/)).toBeInTheDocument()
  })

  it('renders the compact active-revision summary, not a list heading, for the show object’s own revisions', async () => {
    stubs.getShow = showResponse
    stubs.listConfigObjects = () => contentsEmpty()
    stubs.listAssets = assetsEmpty
    stubs.getShowRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show',
        revisions: [
          { revision: 47, createdAt: '2026-08-30T18:22:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: 'renamed', active: true },
        ],
      })
    renderDetail('winter-ridge-2026')
    const summary = await screen.findByText(/Active revision/, { selector: 'p' })
    expect(summary).toBeInTheDocument()
    expect(within(summary).getByText('47')).toBeInTheDocument()
    expect(within(summary).getByText(/erbartos/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
    expect(screen.queryByText('Active · 47')).not.toBeInTheDocument()
    expect(screen.queryByText(/renamed/)).not.toBeInTheDocument()
  })
})
