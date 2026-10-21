import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getSequenceTestConfig: vi.fn(),
  putSequenceTestConfig: vi.fn(),
  listConfigObjects: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { ShowsSequenceTesting } = await import('./ShowsSequenceTesting')

const session = (scopes: string[]) =>
  ({ authenticated: true, principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' }, scopes, scopesState: 'current', bootstrapRequired: false }) as unknown as SessionResponse

const response = (entries: { sequenceFilename: string; cue: string }[], revision: number, running: boolean, notRunningReason?: string) => ({
  serverTime: '', kind: 'show.sequencetest', id: 'halloween', revision, updatedAt: '', createdByPrincipalId: null, createdByPrincipalName: null, source: 'api',
  payload: { show: 'halloween', entries }, running, notRunningReason,
})

function renderTab(scopes: string[]) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: session(scopes), sessionFetchFailed: false }}>
      <MemoryRouter initialEntries={['/shows/halloween/sequence-testing']}>
        <Routes>
          <Route path="/shows/:id/sequence-testing" element={<ShowsSequenceTesting />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('ShowsSequenceTesting', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('adds a test sequence for a cue of the show and saves the list', async () => {
    stubs.getSequenceTestConfig.mockResolvedValue(response([], 0, true))
    stubs.listConfigObjects.mockResolvedValue({ objects: [{ id: 'thriller', label: 'Thriller', show: 'halloween', currentRevision: 1, updatedAt: '' }] })
    stubs.putSequenceTestConfig.mockResolvedValue(response([{ sequenceFilename: 'Thriller.fseq', cue: 'thriller' }], 1, true))
    renderTab(['config:write'])

    fireEvent.click(await screen.findByRole('button', { name: 'Add test sequence' }))
    fireEvent.change(screen.getByLabelText('Sequence file, row 1'), { target: { value: 'Thriller.fseq' } })
    fireEvent.change(screen.getByLabelText('Cue, row 1'), { target: { value: 'thriller' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(stubs.putSequenceTestConfig).toHaveBeenCalledWith('halloween', { show: 'halloween', entries: [{ sequenceFilename: 'Thriller.fseq', cue: 'thriller' }] }))
    expect(await screen.findByText('Version 1.')).toBeTruthy()
  })

  it('says the list only runs while the show is active and shows a refused save', async () => {
    stubs.getSequenceTestConfig.mockResolvedValue(response([{ sequenceFilename: 'Thriller.fseq', cue: 'thriller' }], 1, false, 'These test sequences only run while this show is the active show.'))
    stubs.listConfigObjects.mockResolvedValue({ objects: [{ id: 'thriller', label: 'Thriller', show: 'halloween', currentRevision: 1, updatedAt: '' }, { id: 'intro', label: 'Intro', show: 'halloween', currentRevision: 1, updatedAt: '' }] })
    stubs.putSequenceTestConfig.mockRejectedValue(new Error('Thriller.fseq already plays cue intro from a playlist of this show.'))
    renderTab(['config:write'])

    expect(await screen.findByText('These test sequences only run while this show is the active show.')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Cue, row 1'), { target: { value: 'intro' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Not saved')).toBeTruthy()
  })

  it('refuses edits without config:write', async () => {
    stubs.getSequenceTestConfig.mockResolvedValue(response([{ sequenceFilename: 'Thriller.fseq', cue: 'thriller' }], 1, true))
    stubs.listConfigObjects.mockResolvedValue({ objects: [] })
    renderTab([])

    expect(((await screen.findByLabelText('Sequence file, row 1')) as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
