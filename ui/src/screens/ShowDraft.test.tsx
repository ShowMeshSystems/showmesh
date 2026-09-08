import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getShow: (...args: never[]) => stubs.getShow(...args),
    putShow: (...args: never[]) => stubs.putShow(...args),
  }
})

const { ShowDraft } = await import('./ShowDraft')

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

function showResponse(overrides: Partial<{ name: string; notes: string; revision: number }> = {}) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show' as const,
    id: overrides.name === undefined ? 'winter-ridge-2027' : overrides.name,
    revision: overrides.revision ?? 1,
    payload: { name: overrides.name ?? 'Winter Ridge 2027', notes: overrides.notes ?? '' },
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
}

function renderDraft(model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={['/shows/new']}>
        <Routes>
          <Route path="/shows/new" element={<ShowDraft />} />
          <Route path="/shows/:id" element={<div>show detail page</div>} />
          <Route path="/shows" element={<div>shows list page</div>} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('ShowDraft', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders Name, Id and Notes and nothing else, seeding the id from the name until edited by hand', () => {
    renderDraft({ session: signedIn(['config:write']) })
    expect(screen.getByRole('heading', { name: 'New show' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Identity' })).toBeInTheDocument()
    const name = screen.getByLabelText('Name')
    fireEvent.change(name, { target: { value: 'Winter Ridge 2027' } })
    const id = screen.getByLabelText('Id') as HTMLInputElement
    expect(id.value).toBe('winter-ridge-2027')

    fireEvent.change(id, { target: { value: 'wr-27' } })
    fireEvent.change(name, { target: { value: 'Winter Ridge 2028' } })
    expect(id.value).toBe('wr-27')

    expect(screen.getByLabelText(/Notes/)).toBeInTheDocument()
  })

  it('requires an id before Create show is enabled', () => {
    renderDraft({ session: signedIn(['config:write']) })
    expect(screen.getByText('An id is required.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create show' })).toBeDisabled()
  })

  it('does not write and says so when the id is already taken', async () => {
    stubs.getShow = () => Promise.resolve(showResponse())
    const putSpy = vi.fn(() => Promise.resolve(showResponse()))
    stubs.putShow = putSpy
    renderDraft({ session: signedIn(['config:write']) })
    fireEvent.change(screen.getByLabelText('Id'), { target: { value: 'winter-ridge-2027' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create show' }))
    await waitFor(() => expect(screen.getByText('Id taken')).toBeInTheDocument())
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('creates on a free id and navigates to the new show', async () => {
    stubs.getShow = () => Promise.reject(new ApiError('not found', 404, 'https://showmesh.dev/problems/resource-not-found'))
    stubs.putShow = () => Promise.resolve(showResponse())
    renderDraft({ session: signedIn(['config:write']) })
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Winter Ridge 2027' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create show' }))
    await waitFor(() => expect(screen.getByText('show detail page')).toBeInTheDocument())
  })

  it('disables Create show with a stated reason when the principal lacks config:write', () => {
    renderDraft({ session: signedIn(['node:read']) })
    fireEvent.change(screen.getByLabelText('Id'), { target: { value: 'winter-ridge-2027' } })
    const create = screen.getByRole('button', { name: 'Create show' })
    expect(create).toBeDisabled()
    expect(create.title).not.toBe('')
  })

  it('discard navigates back to the shows list', () => {
    renderDraft({ session: signedIn(['config:write']) })
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(screen.getByText('shows list page')).toBeInTheDocument()
  })
})
