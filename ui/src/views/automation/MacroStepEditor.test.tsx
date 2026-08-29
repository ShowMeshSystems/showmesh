import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useParams } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MacroStepEditor } from './MacroStepEditor'
import { ModelContext } from '../../app/ModelContext'
import { makeModel, makeShowList } from '../../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../../api/test-support/fixtures'
import type { Model } from '../../app/types'

// MacroDetail.test.tsx's own comment: "MacroStepEditor's own editing
// behavior is covered by automation/MacroStepEditor.test.tsx" — this is
// that file. Reached in the real app inside AutomationWorkspace.tsx's
// inspector pane at /shows/:showId/automation/macros/:macroId
// (ROUTE-MAP.md), which passes `showId` and `macroId` as props rather
// than reading them itself, so these tests mount it the same way.
const {
  getShowMacro,
  getShowMacroRevisions,
  putShowMacro,
  listConfigObjects,
  listActionBindings,
  listMacroRuns,
  getShowAction,
} = vi.hoisted(() => ({
  getShowMacro: vi.fn(),
  getShowMacroRevisions: vi.fn(),
  putShowMacro: vi.fn(),
  listConfigObjects: vi.fn(),
  listActionBindings: vi.fn(),
  listMacroRuns: vi.fn(),
  getShowAction: vi.fn(),
}))
vi.mock('../../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api')>()
  return { ...actual, getShowMacro, getShowMacroRevisions, putShowMacro, listConfigObjects, listActionBindings, listMacroRuns, getShowAction }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function emptyList(kind: string) {
  return { serverTime: '2026-08-28T00:00:00Z', kind, objects: [] }
}

function mockCommonLists(shows: string[] = ['halloween-2026', 'christmas-2026']): void {
  listConfigObjects.mockImplementation((kind: string) =>
    Promise.resolve(kind === 'show' ? makeShowList(shows) : emptyList(kind)),
  )
  listActionBindings.mockResolvedValue([])
  listMacroRuns.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'macro-run', runs: [] })
  getShowAction.mockResolvedValue({
    serverTime: '2026-08-28T00:00:00Z',
    kind: 'show.action',
    id: 'lights-up',
    revision: 1,
    payload: {
      show: 'halloween-2026',
      label: 'Lights up',
      description: '',
      target: { integration: 'fpp', primitive: 'startPlaylist', params: {} },
      safetyClass: 'none',
    },
    updatedAt: '2026-08-28T00:00:00Z',
    createdByPrincipalId: 'p-1',
    createdByPrincipalName: 'admin-1',
    source: 'api',
  })
}

const storedMacro = {
  serverTime: '2026-08-28T00:00:00Z',
  kind: 'show.macro' as const,
  id: 'preshow-lights',
  revision: 3,
  payload: {
    show: 'halloween-2026',
    label: 'Preshow Lights Up',
    description: '',
    steps: [
      {
        id: 'step-1',
        action: 'lights-up',
        onFailure: 'continue' as const,
        onUnconfirmed: 'continue' as const,
        localFallback: { class: 'coordinator-required' as const, reason: 'No local path.' },
      },
    ],
  },
  updatedAt: '2026-08-28T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-28T00:00:00Z', revisions: [] }

// The macro is normally reached through the show-scoped route; a bare
// harness route stands in for AutomationWorkspace.tsx's own mounting so
// a post-move navigate() to a DIFFERENT :showId is observable.
function Harness({ model }: { model: Model }) {
  return (
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/halloween-2026/automation/macros/preshow-lights']}>
        <Routes>
          <Route path="/shows/:showId/automation/macros/:macroId" element={<RoutedEditor />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>
  )
}

function RoutedEditor() {
  const params = useParams<{ showId: string; macroId: string }>()
  const macroId = params.macroId
  return macroId === undefined ? (
    <MacroStepEditor showId={params.showId ?? ''} isNew />
  ) : (
    <MacroStepEditor showId={params.showId ?? ''} macroId={macroId} />
  )
}

function renderExisting(model: Model = makeModel({ session: adminSession })) {
  return render(<Harness model={model} />)
}

describe('MacroStepEditor (move to another show)', () => {
  it('renders the re-assignment control for an existing macro, defaulted to its current show', async () => {
    mockCommonLists()
    getShowMacro.mockResolvedValue(storedMacro)
    getShowMacroRevisions.mockResolvedValue(emptyRevisions)
    renderExisting()

    await waitFor(() => expect(screen.getByText('Preshow Lights Up')).toBeVisible())
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    expect(select).toHaveValue('halloween-2026')
    expect(screen.getByText(/removes it from halloween-2026’s Automation list/)).toBeVisible()
  })

  it('is absent when creating a new macro: the route already names the right show', async () => {
    mockCommonLists()
    const model = makeModel({ session: adminSession })
    render(
      <ModelContext.Provider value={model}>
        <MemoryRouter>
          <MacroStepEditor showId="halloween-2026" isNew />
        </MemoryRouter>
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(screen.getByLabelText('Label')).toBeVisible())
    expect(screen.queryByText(/Move to another show/i)).not.toBeInTheDocument()
  })

  it('is disabled, with the stated reason, for a reader without config:write', async () => {
    mockCommonLists()
    getShowMacro.mockResolvedValue(storedMacro)
    getShowMacroRevisions.mockResolvedValue(emptyRevisions)
    renderExisting(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByText('Preshow Lights Up')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    expect(select).toBeDisabled()
  })

  it('saves the newly selected show and lands on the macro at its new show-scoped route', async () => {
    mockCommonLists()
    getShowMacro.mockResolvedValue(storedMacro)
    getShowMacroRevisions.mockResolvedValue(emptyRevisions)
    putShowMacro.mockResolvedValue({
      ...storedMacro,
      revision: 4,
      payload: { ...storedMacro.payload, show: 'christmas-2026' },
    })
    const user = userEvent.setup()
    renderExisting()

    await waitFor(() => expect(screen.getByText('Preshow Lights Up')).toBeVisible())
    const select = await screen.findByRole('combobox', { name: 'Move to another show' })
    await user.selectOptions(select, 'christmas-2026')
    await user.click(screen.getByRole('button', { name: 'Save macro' }))

    await waitFor(() => expect(putShowMacro).toHaveBeenCalledTimes(1))
    expect(putShowMacro).toHaveBeenCalledWith('preshow-lights', expect.objectContaining({ show: 'christmas-2026' }))
    // Landed on the new show-scoped route rather than staying on the old
    // one: the revision-history refresh that would run for a same-show
    // save never fires.
    await waitFor(() => expect(screen.getByText(/removes it from christmas-2026’s Automation list/)).toBeVisible())
    expect(getShowMacroRevisions).toHaveBeenCalledTimes(1)
  })
})
