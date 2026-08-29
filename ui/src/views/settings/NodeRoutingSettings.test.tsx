import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NodeRoutingSettings } from './NodeRoutingSettings'
import { ModelContext } from '../../app/ModelContext'
import { makeCapability, makeModel, makeNode } from '../../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../../api/test-support/fixtures'
import type { Model } from '../../app/types'

// Revision-1 Settings.dc.html, Node routing tab: list-then-detail with a
// DERIVED "Not declared yet" group (nodes advertising an audio capability
// minus nodes that already have an audio.node object) and a Declare
// action that reuses the agent's own reported node id.
const { listConfigObjects, getAudioNode, getAudioNodeConfigRevisions, putAudioNode } = vi.hoisted(() => ({
  listConfigObjects: vi.fn(),
  getAudioNode: vi.fn(),
  getAudioNodeConfigRevisions: vi.fn(),
  putAudioNode: vi.fn(),
}))
vi.mock('../../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api')>()
  return { ...actual, listConfigObjects, getAudioNode, getAudioNodeConfigRevisions, putAudioNode }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
  getAudioNode.mockReset()
  getAudioNodeConfigRevisions.mockReset()
  putAudioNode.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderPage(model: Model = makeModel({ session: adminSession })) {
  return render(
    <MemoryRouter>
      <ModelContext.Provider value={model}>
        <NodeRoutingSettings />
      </ModelContext.Provider>
    </MemoryRouter>,
  )
}

describe('NodeRoutingSettings', () => {
  it('renders the Settings tab strip with Node routing current', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-29T00:00:00Z', kind: 'audio.node', objects: [] })
    renderPage(makeModel({ session: adminSession, nodes: [] }))

    const tab = await screen.findByRole('link', { name: 'Node routing' })
    expect(tab).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Connections' })).not.toHaveAttribute('aria-current')
  })

  it('lists a declared audio.node object and marks the selected one Editing', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-29T00:00:00Z',
      kind: 'audio.node',
      objects: [{ id: 'audio-node-01', label: 'hw:0,0', show: '', currentRevision: 4, updatedAt: '2026-08-29T00:00:00Z' }],
    })
    getAudioNode.mockResolvedValue({
      serverTime: '2026-08-29T00:00:00Z',
      kind: 'audio.node',
      id: 'audio-node-01',
      revision: 4,
      payload: {
        programRoute: 'hw:0,0',
        programChannels: [1, 2],
        clockDomain: 'onboard-clock',
        clockDomainProvenance: 'commissioning notes',
      },
      updatedAt: '2026-08-29T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    getAudioNodeConfigRevisions.mockResolvedValue({ serverTime: '2026-08-29T00:00:00Z', revisions: [] })
    const node = makeNode('audio-node-01', { capabilities: [makeCapability('audio.output.local')] })
    renderPage(makeModel({ session: adminSession, nodes: [node] }))

    expect(await screen.findByText(/1 declared/)).toBeInTheDocument()
    const row = (await screen.findByRole('button', { name: 'audio-node-01' })).closest('tr')
    expect(row).toHaveAttribute('aria-current', 'true')
    expect(screen.getByText('Editing')).toBeInTheDocument()
    // The declared audio.node is selected by default, so its editor renders below.
    expect(await screen.findByLabelText('Program route')).toBeInTheDocument()
  })

  it('derives "Not declared yet" from audio-capable nodes minus declared audio.node objects', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-29T00:00:00Z', kind: 'audio.node', objects: [] })
    const mediaSide = makeNode('media-side', {
      capabilities: [makeCapability('audio.engine'), makeCapability('audio.output.local')],
    })
    const mediaFront = makeNode('media-front', { capabilities: [makeCapability('matrix.render')] })
    renderPage(makeModel({ session: adminSession, nodes: [mediaSide, mediaFront] }))

    expect(await screen.findByText(/Not declared yet/)).toBeInTheDocument()
    expect(screen.getByText('media-side')).toBeInTheDocument()
    expect(screen.getByText('audio.engine · audio.output.local')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Declare' })).toBeInTheDocument()

    // media-front advertises no audio capability at all, so it is footnoted, not listed as a row.
    expect(screen.queryByRole('cell', { name: /media-front/ })).not.toBeInTheDocument()
    expect(screen.getByText('media-front', { selector: 'code' })).toBeInTheDocument()
  })

  it('declaring a node presets its id and offers nothing to type', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-29T00:00:00Z', kind: 'audio.node', objects: [] })
    const mediaSide = makeNode('media-side', { capabilities: [makeCapability('audio.output.local')] })
    const user = userEvent.setup()
    renderPage(makeModel({ session: adminSession, nodes: [mediaSide] }))

    await user.click(await screen.findByRole('button', { name: 'Declare' }))

    expect(await screen.findByRole('heading', { name: 'Declare media-side' })).toBeInTheDocument()
    expect(screen.getByText(/There is nothing to type/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Select an observed audio node')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Node id')).not.toBeInTheDocument()
  })

  it('renders an EmptyBlock, never a fabricated row, when no audio.node is declared', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-29T00:00:00Z', kind: 'audio.node', objects: [] })
    renderPage(makeModel({ session: adminSession, nodes: [] }))

    expect(await screen.findByRole('heading', { name: 'No audio nodes declared' })).toBeVisible()
    expect(screen.getByRole('heading', { name: 'No node selected' })).toBeVisible()
  })

  it('is unavailable, with a stated reason, without the config:write scope, and never fetches the list', () => {
    renderPage(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })

  it('disables Declare, with a stated reason, without the config:write scope', async () => {
    listConfigObjects.mockReturnValue(new Promise(() => undefined))
    const mediaSide = makeNode('media-side', { capabilities: [makeCapability('audio.output.local')] })
    renderPage(
      makeModel({
        session: makeAuthenticatedSession({ scopes: ['node:read'] }),
        nodes: [mediaSide],
      }),
    )

    // The list itself is gated on config:write, so "Not declared yet" never renders
    // without it -- only the top-level permission block does.
    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByRole('button', { name: 'Declare' })).not.toBeInTheDocument()
  })

  it('retries a failed read without changing the current route', async () => {
    listConfigObjects
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce({ serverTime: '2026-08-29T00:00:00Z', kind: 'audio.node', objects: [] })
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByRole('alert')).toHaveTextContent(/failed to fetch/i)
    await user.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByRole('heading', { name: 'No audio nodes declared' })).toBeInTheDocument()
    expect(listConfigObjects).toHaveBeenCalledTimes(2)
  })

  it('waits for permission evidence before treating an unconfirmed scope as denied', async () => {
    renderPage(makeModel({ session: null }))
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Loading permissions' })).toBeInTheDocument())
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})
