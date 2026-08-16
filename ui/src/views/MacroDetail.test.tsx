import { act, cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MacroDetail } from './MacroDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { listConfigObjects, putShowMacro, getShowMacro, getShowMacroRevisions, listMacroRuns, getShowAction } =
  vi.hoisted(() => ({
    listConfigObjects: vi.fn(),
    putShowMacro: vi.fn(),
    getShowMacro: vi.fn(),
    getShowMacroRevisions: vi.fn(),
    listMacroRuns: vi.fn(),
    getShowAction: vi.fn(),
  }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects, putShowMacro, getShowMacro, getShowMacroRevisions, listMacroRuns, getShowAction }
})

// `restoreMocks: true` (vitest.config.ts) wipes any implementation set at
// module scope before each test, so the default lives in beforeEach.
// Most tests in this file never care about the action-integration lookup;
// default it to a rejection (an unmocked id 404s in real life too) so the
// getShowAction(id).then(...) effect never throws on an undefined return.
beforeEach(() => {
  getShowAction.mockRejectedValue(new Error('getShowAction not mocked in this test'))
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
  putShowMacro.mockReset()
  getShowMacro.mockReset()
  getShowMacroRevisions.mockReset()
  listMacroRuns.mockReset()
  getShowAction.mockReset()
})

function renderNewMacro(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/macros/new']}>
        <MacroDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExistingMacro(model: Model, id: string) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/macros/${id}`]}>
        <Routes>
          <Route path="/macros/:id" element={<MacroDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write', 'show:macro:run'],
})

describe('MacroDetail (new macro authoring)', () => {
  // STEP-9-SPEC.md section 5.4: "reduced must be rejected at the point
  // of authoring... Do not offer it as a value they can pick and then
  // have the server reject." This asserts the STRONGER of the two —
  // never offering it, not merely rejecting it after the fact — because
  // an operator who never sees "reduced" as an option can never author
  // it in the first place.
  it('never offers "reduced" as a local-fallback option, and states why in the operator-visible text', () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-14T00:00:00Z', kind: 'show.action', objects: [] })
    renderNewMacro(makeModel({ session: adminSession }))

    const options = screen.getAllByRole('option', { name: /none|coordinator-required|silence|reduced/ })
    const values = options.map((o) => o.textContent)
    expect(values).not.toContain('reduced')
    expect(values).toEqual(expect.arrayContaining(['none', 'coordinator-required', 'silence']))
    expect(screen.getByText(/no delivery path exists/)).toBeVisible()
  })

  it('requires a non-empty local-fallback reason before submitting, even for class "none"', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-14T00:00:00Z', kind: 'show.action', objects: [] })
    const user = userEvent.setup()
    renderNewMacro(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Macro id'), 'begin-set')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Begin set')
    await user.type(screen.getByLabelText('Step id'), 'start')
    await user.type(screen.getByLabelText('Action'), 'start-main-show')
    // Reason left blank on purpose.
    await user.click(screen.getByRole('button', { name: 'Create macro' }))

    expect(await screen.findByText(/needs a reason for its local fallback/)).toBeVisible()
    expect(putShowMacro).not.toHaveBeenCalled()
  })

  it('submits onFailure/onUnconfirmed at their stated defaults when the operator never touches those selects', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-14T00:00:00Z', kind: 'show.action', objects: [] })
    putShowMacro.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      kind: 'show.macro',
      id: 'begin-set',
      revision: 1,
      payload: { show: 'halloween-2026', label: 'Begin set', description: '', steps: [] },
      updatedAt: '2026-08-14T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewMacro(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Macro id'), 'begin-set')
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Begin set')
    await user.type(screen.getByLabelText('Step id'), 'start')
    await user.type(screen.getByLabelText('Action'), 'start-main-show')
    await user.type(
      screen.getByLabelText(/Reason \(required/),
      'nothing runs locally; the coordinator dispatches this step',
    )
    await user.click(screen.getByRole('button', { name: 'Create macro' }))

    expect(putShowMacro).toHaveBeenCalledWith(
      'begin-set',
      expect.objectContaining({
        steps: [expect.objectContaining({ onFailure: 'continue', onUnconfirmed: 'continue' })],
      }),
    )
  })
})

describe('MacroDetail (existing macro, action-integration lookup)', () => {
  // Review finding 1: opening a macro whose step references an action that
  // 404s (deleted, renamed, or never existed) used to retry that lookup on
  // every render forever — 367 requests in 300ms measured live, because a
  // failed lookup was never recorded and its absence kept re-triggering the
  // effect. This asserts the fetch is made exactly once per unresolved id,
  // however many render passes follow.
  it('does not retry a failed action-integration lookup', async () => {
    getShowMacro.mockResolvedValue({
      serverTime: '2026-08-16T00:00:00Z',
      kind: 'show.macro',
      id: 'begin-set',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Begin set',
        description: '',
        steps: [
          {
            id: 'start',
            action: 'deleted-action',
            onFailure: 'continue',
            onUnconfirmed: 'continue',
            localFallback: { class: 'coordinator-required', reason: 'n/a' },
          },
        ],
      },
      updatedAt: '2026-08-16T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    getShowMacroRevisions.mockResolvedValue({ revisions: [] })
    listMacroRuns.mockResolvedValue({ runs: [] })
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-16T00:00:00Z', kind: 'show.action', objects: [] })
    getShowAction.mockRejectedValue(new Error('404 not found'))

    renderExistingMacro(makeModel({ session: adminSession }), 'begin-set')

    await screen.findByDisplayValue('deleted-action')

    // Flush several more macrotask/microtask rounds — enough for the
    // unbounded version of the effect to have refetched many times over.
    for (let i = 0; i < 10; i += 1) {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
    }

    expect(getShowAction).toHaveBeenCalledTimes(1)
  })
})
