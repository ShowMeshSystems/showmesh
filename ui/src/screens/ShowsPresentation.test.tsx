import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type ConfigObjectSummary, type ConfigShowSurface, type Model, type Node, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowSurface: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowSurface: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    getShowSurface: (...args: never[]) => stubs.getShowSurface(...args),
    putShowSurface: (...args: never[]) => stubs.putShowSurface(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsPresentation } = await import('./ShowsPresentation')

function surfaceSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'garage', label: 'Garage door', show: 'winter-ridge-2026', currentRevision: 6, updatedAt: '2026-08-30T20:41:00Z', ...overrides }
}

function surfacePayload(overrides: Partial<ConfigShowSurface> = {}): ConfigShowSurface {
  return {
    show: 'winter-ridge-2026',
    name: 'Garage door',
    node: 'media-garage',
    channelRange: { startChannel: 15361, channelCount: 4096 },
    geometry: { width: 32, height: 32, pixelFormat: 'rgbw' },
    frameRate: 40,
    output: { transport: 'ndi', ndi: { sourceName: 'media-garage-ndi' } },
    ...overrides,
  } as ConfigShowSurface
}

function surfaceResponse(id = 'garage', revision = 6, payload = surfacePayload()) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.surface' as const,
    id,
    revision,
    payload,
    updatedAt: '2026-08-30T20:41:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
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

function withContents(kind: string, surfaces: ConfigObjectSummary[]) {
  if (kind === 'show.surface') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: surfaces })
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
}

function node(overrides: Partial<Node> = {}): Node {
  return {
    nodeId: 'media-garage',
    label: 'media-garage',
    platform: null,
    agentVersion: null,
    bootId: null,
    startedAt: null,
    firstSeenAt: '2026-08-30T18:00:00Z',
    updatedAt: '2026-08-30T20:41:00Z',
    capabilities: [{ id: 'transport.ndi.send', version: 1 }],
    controlPlane: { state: 'online', reason: null },
    evidence: { heartbeat: { observedAt: '2026-08-30T20:41:00Z' }, hello: { observedAt: '2026-08-30T18:00:00Z' } },
    declaration: { state: 'declared' },
    render: [],
    audio: [],
    fppConnect: [],
    ...overrides,
  } as unknown as Node
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/presentation') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="presentation" element={<ShowsPresentation />} />
            <Route path="playlists" element={<ShowsTabPlaceholder tab="Playlists" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows · Presentation tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function setup(scopes: string[] = ['config:write'], surfaces: ConfigObjectSummary[] = [surfaceSummary()], nodes: Node[] = [node()]) {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, surfaces)
    stubs.listAssets = assetsEmpty
    stubs.getShowSurface = (id: string) => Promise.resolve(surfaceResponse(id))
    return renderWorkspace({ session: signedIn(scopes), nodes })
  }

  it('renders the section heading and lists a configured surface', async () => {
    setup()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Surfaces' })).toBeInTheDocument())
    const region = await screen.findByRole('region', { name: 'Surfaces, scrollable' })
    await waitFor(() => expect(within(region).getByText('Garage door')).toBeInTheDocument())
  })

  it('a show with no surface is a settled empty, distinct from loading and from a read failure', async () => {
    setup(['config:write'], [])
    expect(await screen.findByText('This show has no surface configured.')).toBeInTheDocument()
  })

  it('a read failure is reported distinctly, with a retry', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => Promise.reject(new ApiError('Coordinator unreachable.', 503, 'unavailable'))
    stubs.listAssets = assetsEmpty
    renderWorkspace({ session: signedIn(['config:write']) })
    expect(await screen.findByText('Coordinator unreachable.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('a surface no node has claimed reads distinctly from one that is failing', async () => {
    // media-garage exists but has never reported this specific surface id.
    setup(['config:write'], [surfaceSummary()], [node({ render: [] })])
    const region = await screen.findByRole('region', { name: 'Surfaces, scrollable' })
    await waitFor(() => expect(within(region).getByText('Not claimed')).toBeInTheDocument())
    expect(within(region).queryByText(/Stale/)).not.toBeInTheDocument()
  })

  it('a surface whose node reports it stale is distinguishable from an unclaimed one', async () => {
    setup(
      ['config:write'],
      [surfaceSummary()],
      [
        node({
          render: [
            {
              resource: { kind: 'surface', id: 'garage' },
              signal: 'surface.pipeline.state',
              value: 'running',
              unit: null,
              state: 'stale',
              reason: 'No report in 26 minutes.',
              observedAt: '2026-08-30T20:40:58Z',
              collectedAt: '2026-08-30T20:40:58Z',
              source: 'mqtt-inventory',
              quality: 'direct',
              validForSeconds: null,
            },
          ],
        }),
      ],
    )
    const region = await screen.findByRole('region', { name: 'Surfaces, scrollable' })
    await waitFor(() => expect(within(region).getByText(/Stale/)).toBeInTheDocument())
    expect(within(region).queryByText('Not claimed')).not.toBeInTheDocument()
  })

  it('a surface whose node currently confirms running reads good, with the observed frame rate', async () => {
    setup(
      ['config:write'],
      [surfaceSummary()],
      [
        node({
          render: [
            {
              resource: { kind: 'surface', id: 'garage' },
              signal: 'surface.pipeline.state',
              value: 'running',
              unit: null,
              state: 'current',
              reason: null,
              observedAt: '2026-08-30T20:59:50Z',
              collectedAt: '2026-08-30T20:59:50Z',
              source: 'mqtt-inventory',
              quality: 'direct',
              validForSeconds: 30,
            },
            {
              resource: { kind: 'surface', id: 'garage' },
              signal: 'surface.frames.rate',
              value: 40,
              unit: null,
              state: 'current',
              reason: null,
              observedAt: '2026-08-30T20:59:50Z',
              collectedAt: '2026-08-30T20:59:50Z',
              source: 'mqtt-inventory',
              quality: 'direct',
              validForSeconds: 30,
            },
          ],
        }),
      ],
    )
    const region = await screen.findByRole('region', { name: 'Surfaces, scrollable' })
    await waitFor(() => expect(within(region).getByText(/40 fps/)).toBeInTheDocument())
  })

  it('save is disabled without config:write and is actually inert', async () => {
    setup([])
    const row = await screen.findByRole('button', { name: 'Garage door' })
    fireEvent.click(row)
    const save = await screen.findByRole('button', { name: 'Save surface' })
    expect(save).toBeDisabled()
    const putSpy = vi.fn(() => new Promise(() => {}))
    stubs.putShowSurface = putSpy
    fireEvent.click(save)
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('a new surface needs a node and an NDI source name before it can be created', async () => {
    setup()
    fireEvent.click(await screen.findByRole('button', { name: 'New surface' }))
    const create = await screen.findByRole('button', { name: 'Create surface' })
    expect(create).toBeDisabled()
    expect(create).toHaveAttribute('title', 'A surface needs a name.')
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Side wall' } })
    expect(create).toHaveAttribute('title', 'A surface needs a node.')
  })
})
