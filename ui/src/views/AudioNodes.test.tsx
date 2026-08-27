import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AudioNodes } from './AudioNodes'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// ADR-018/ADR-039: the audio.node object list -- mirrors ShowActions.tsx's
// own list test shape.
const { listConfigObjects } = vi.hoisted(() => ({ listConfigObjects: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderView(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/config/audio.node']}>
        <AudioNodes />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('AudioNodes', () => {
  it('keeps the loading state explicit while the configuration list is pending', () => {
    listConfigObjects.mockReturnValue(new Promise(() => undefined))
    renderView()

    expect(screen.getByText(/loading audio nodes/i)).toBeInTheDocument()
  })

  it('renders one row per configured audio.node object', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'audio.node',
      objects: [
        { id: 'node-1', label: 'hw:0,0', show: '', currentRevision: 2, updatedAt: '2026-08-25T00:00:00Z' },
        { id: 'node-2', label: 'hw:1,0', show: '', currentRevision: 1, updatedAt: '2026-08-24T00:00:00Z' },
      ],
    })
    renderView()

    expect(await screen.findByRole('link', { name: 'node-1' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'node-2' })).toBeInTheDocument()
    expect(screen.getByText('hw:0,0')).toBeInTheDocument()
    expect(screen.getByText('hw:1,0')).toBeInTheDocument()
    expect(listConfigObjects).toHaveBeenCalledWith('audio.node')
  })

  it('renders an empty-state message with no configured audio node', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', kind: 'audio.node', objects: [] })
    renderView()

    expect(await screen.findByText(/no audio\.node object is configured yet/i)).toBeInTheDocument()
  })

  it('labels configured nodes without live evidence as disconnected and does not invent interfaces', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'audio.node',
      objects: [{ id: 'offline-node', label: 'hw:9,0', show: '', currentRevision: 4, updatedAt: '2026-08-25T00:00:00Z' }],
    })
    renderView()

    expect(await screen.findByText('Disconnected: no live evidence')).toBeInTheDocument()
    expect(screen.getByText('Unavailable from API')).toBeInTheDocument()
    expect(screen.getByText('hw:9,0')).toBeInTheDocument()
  })

  it('is unavailable, with a stated reason, without the config:write scope', () => {
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })

  it('waits until the reader is confirmed', async () => {
    // No assertion on scope here beyond the fetch never firing without it
    // -- covered by the previous test; this just documents the New link is
    // hidden-but-disabled rather than absent, matching ShowActions.tsx's
    // own posture.
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    await waitFor(() => expect(screen.getByRole('button', { name: /new audio node/i })).toBeDisabled())
  })
})
