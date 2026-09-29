import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Model, NodeEnrollment, SessionResponse } from '../api'
import { ApiError } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listNodeEnrollments: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  createNodeEnrollment: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  cancelNodeEnrollment: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getServiceDescriptor: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listNodeEnrollments: (...args: never[]) => stubs.listNodeEnrollments(...args),
    createNodeEnrollment: (...args: never[]) => stubs.createNodeEnrollment(...args),
    cancelNodeEnrollment: (...args: never[]) => stubs.cancelNodeEnrollment(...args),
    getServiceDescriptor: (...args: never[]) => stubs.getServiceDescriptor(...args),
  }
})

const { SettingsNodeEnrollment } = await import('./SettingsNodeEnrollment')

function session(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: '2026-08-30T21:07:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T20:00:00Z' },
    credentialForm: 'session',
    scopes: ['node:enroll'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  } as unknown as SessionResponse
}

function enrollment(overrides: Partial<NodeEnrollment> = {}): NodeEnrollment {
  return {
    id: 'enr-1',
    nodeId: 'render-01',
    reenroll: false,
    createdBy: 'erbartos',
    createdAt: '2026-08-30T20:00:00Z',
    expiresAt: '2026-08-30T21:22:00Z',
    state: 'pending',
    redeemedAt: null,
    ...overrides,
  } as unknown as NodeEnrollment
}

function renderScreen(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider
      value={{
        ...initialModel(),
        session: session(),
        serverTime: '2026-08-30T21:07:00Z',
        serverTimeReceivedAt: Date.now(),
        ...model,
      }}
    >
      <MemoryRouter initialEntries={['/settings/node-enrollment']}>
        <Routes>
          <Route path="/settings/node-enrollment" element={<SettingsNodeEnrollment />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('SettingsNodeEnrollment', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    stubs.listNodeEnrollments = () => new Promise(() => {})
    stubs.createNodeEnrollment = () => new Promise(() => {})
    stubs.cancelNodeEnrollment = () => new Promise(() => {})
    stubs.getServiceDescriptor = () => new Promise(() => {})
  })

  it('shows the minted code once, gone after dismissal', async () => {
    stubs.listNodeEnrollments = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', enrollments: [] })
    stubs.createNodeEnrollment = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        id: 'enr-1',
        nodeId: 'render-01',
        code: 'ABCD-2345',
        reenroll: false,
        expiresAt: '2026-08-30T21:22:00Z',
        coordinatorUrl: 'https://coordinator.example',
      })
    renderScreen()
    await waitFor(() => expect(screen.getByText('No enrollment codes have been minted. Mint one above to add a node.')).toBeInTheDocument())

    expect(screen.queryByText('ABCD-2345')).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Node id'), { target: { value: 'render-01' } })
    fireEvent.click(screen.getByRole('button', { name: 'Mint enrollment code' }))

    await waitFor(() => expect(screen.getByText('ABCD-2345')).toBeInTheDocument())
    expect(screen.getByText('This code will not be shown again. Copy it now.')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('ABCD-2345')).not.toBeInTheDocument()
  })

  it('asks to re-enroll only after the server refuses with 409, then mints with reenroll on confirm', async () => {
    stubs.listNodeEnrollments = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', enrollments: [] })
    let lastRequest: { nodeId: string; reenroll: boolean } | undefined
    stubs.createNodeEnrollment = (payload: { nodeId: string; reenroll: boolean }) => {
      lastRequest = payload
      if (!payload.reenroll) {
        return Promise.reject(new ApiError('Node "render-01" is already enrolled. Mint a re-enrollment code to replace its credentials.', 409))
      }
      return Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        id: 'enr-2',
        nodeId: 'render-01',
        code: 'WXYZ-9001',
        reenroll: true,
        expiresAt: '2026-08-30T21:22:00Z',
        coordinatorUrl: 'https://coordinator.example',
      })
    }
    renderScreen()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Mint enrollment code' })).toBeInTheDocument())

    fireEvent.change(screen.getByLabelText('Node id'), { target: { value: 'render-01' } })
    fireEvent.click(screen.getByRole('button', { name: 'Mint enrollment code' }))

    await waitFor(() => expect(screen.getByRole('dialog', { name: 'Re-enroll this node?' })).toBeInTheDocument())
    expect(screen.getByText(/already enrolled/)).toBeInTheDocument()
    expect(screen.queryByText('WXYZ-9001')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Mint re-enrollment code' }))

    await waitFor(() => expect(screen.getByText('WXYZ-9001')).toBeInTheDocument())
    expect(lastRequest).toEqual({ nodeId: 'render-01', reenroll: true })
  })

  it('leaves the mint button disabled with the reason while the node id fails the client-side hint', async () => {
    stubs.listNodeEnrollments = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', enrollments: [] })
    renderScreen()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Mint enrollment code' })).toBeInTheDocument())

    fireEvent.change(screen.getByLabelText('Node id'), { target: { value: 'Render_01' } })
    const button = screen.getByRole('button', { name: 'Mint enrollment code' })
    expect(button).toBeDisabled()
    expect(button.title).not.toBe('')
  })

  it('lists enrollment codes and cancels a pending one after confirmation', async () => {
    stubs.listNodeEnrollments = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', enrollments: [enrollment()] })
    let cancelledId: string | undefined
    stubs.cancelNodeEnrollment = (id: string) => {
      cancelledId = id
      return Promise.resolve({
        serverTime: '2026-08-30T21:07:00Z',
        enrollment: enrollment({ state: 'cancelled' }),
      })
    }
    renderScreen()
    await waitFor(() => expect(screen.getByText('render-01')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.getByRole('dialog', { name: 'Cancel this enrollment code?' })).toBeInTheDocument())

    stubs.listNodeEnrollments = () => Promise.resolve({ serverTime: '2026-08-30T21:07:00Z', enrollments: [enrollment({ state: 'cancelled' })] })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel code' }))

    await waitFor(() => expect(cancelledId).toBe('enr-1'))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Cancel this enrollment code?' })).not.toBeInTheDocument())
  })

  it('disables mint with its reason and shows the list as not shown without node:enroll, and never calls the list read', async () => {
    const listSpy = vi.fn(() => new Promise(() => {}))
    stubs.listNodeEnrollments = listSpy
    renderScreen({ session: session({ scopes: [] }) })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Mint enrollment code' })).toBeInTheDocument())

    const button = screen.getByRole('button', { name: 'Mint enrollment code' })
    expect(button).toBeDisabled()
    expect(button.title).toMatch(/node:enroll/)
    expect(screen.getByText(/node:enroll/)).toBeInTheDocument()
    expect(listSpy).not.toHaveBeenCalled()
  })
})
