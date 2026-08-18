import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Macros } from './Macros'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeMacroRunSummary } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Same isolation pattern as Configuration.test.tsx: mock the one API call
// this view makes, to test this component's OWN branching (which state
// renders what) rather than re-proving store.test.ts's network behavior.
const { listConfigObjects } = vi.hoisted(() => ({ listConfigObjects: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderMacros(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Macros />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'], // deliberately NOT config:write
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-2', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'], // deliberately NOT show:macro:run
})

const listResponse = {
  serverTime: '2026-08-14T00:00:00Z',
  kind: 'show.macro' as const,
  objects: [{ id: 'begin-set', label: 'Begin set', show: 'halloween-2026', currentRevision: 3, updatedAt: '2026-08-14T00:00:00Z' }],
}

describe('Macros', () => {
  // The exact acceptance criterion this surface exists to satisfy
  // (STEP-9-SPEC.md acceptance criterion 21 / this wave's own brief item
  // 1): "an operator-role principal can list, read and run a macro
  // through both clients... this is the criterion that catches the UI
  // rendering an empty list for the role the actual operator signs in
  // as." operatorSession above holds show:macro:run and NOT config:write
  // — the exact shape of the real "operator" role.
  it('renders the macro list for a principal holding ONLY show:macro:run (the operator role)', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderMacros(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    expect(listConfigObjects).toHaveBeenCalledWith('show.macro', undefined)
    // This task's finding 9: "New macro" (config:write-gated) is now
    // rendered DISABLED with a stated reason for the operator role,
    // never hidden — the standing rule (OPERATOR-UI section 14) this
    // view used to be the one exception to.
    const newMacroButton = screen.getByRole('button', { name: 'New macro' })
    expect(newMacroButton).toBeVisible()
    expect(newMacroButton).toBeDisabled()
    expect(screen.getByText(/does not include "config:write"/)).toBeVisible()
  })

  it('also renders the macro list for a principal holding ONLY config:write (an admin who never runs a show)', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderMacros(makeModel({ session: adminSession }))

    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    expect(screen.getByText('New macro')).toBeVisible()
  })

  it('renders a stated reason, never an empty list, for a principal holding neither scope', () => {
    renderMacros(
      makeModel({
        session: makeAuthenticatedSession({
          principal: { id: 'p-3', name: 'viewer-1', kind: 'human', role: 'viewer' },
          scopes: ['node:read'],
        }),
      }),
    )
    expect(listConfigObjects).not.toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toMatch(/does not include/)
  })

  it('narrows the list by show when the operator types into the show filter (E7-3)', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    const user = userEvent.setup()
    renderMacros(makeModel({ session: operatorSession }))

    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.macro', undefined))
    await user.type(screen.getByLabelText('Narrow by show'), 'halloween-2026')
    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.macro', 'halloween-2026'))
  })

  it('states plainly when no macros are configured yet, rather than rendering an empty table', async () => {
    listConfigObjects.mockResolvedValue({ ...listResponse, objects: [] })
    renderMacros(makeModel({ session: operatorSession }))
    await waitFor(() => expect(screen.getByText('No show macros are configured yet.')).toBeVisible())
  })

  // This task's finding 1, the highest-priority one: `model.macroRuns`
  // had NO consumer anywhere in the UI before this fix. This proves the
  // list surfaces an in-flight run SOURCED FROM THE LIVE MODEL — no
  // fetch of its own — matching the concrete failure this finding names:
  // a run started by the FPP plugin, with no browser open at the time,
  // must still be visible the moment a client connects (ADR-020 decision
  // 3's snapshot inclusion is what makes model.macroRuns already hold it
  // on first render).
  it('shows a currently-running macro run, sourced live from model.macroRuns, both in the "Running now" panel and on its own row', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    const runningRun = makeMacroRunSummary({
      id: 'run-live',
      macroObjectId: 'begin-set',
      state: 'running',
      completed: null,
      confirmed: null,
      issuerPrincipalName: 'fpp-plugin',
      trigger: 'plugin',
    })
    renderMacros(makeModel({ session: operatorSession, macroRuns: [runningRun] }))

    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    // The "Running now" panel, keyed by run id.
    expect(screen.getByText(/begin-set — started/)).toBeVisible()
    // The per-row Status cell, keyed by macro id, links to the same run.
    const runningBadge = screen.getByText('Running')
    expect(runningBadge.closest('a')).toHaveAttribute(
      'href',
      '/macros/begin-set/runs/run-live',
    )
  })

  it('shows no "Running now" panel and a plain dash in Status when nothing is running', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderMacros(makeModel({ session: operatorSession, macroRuns: [] }))
    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    expect(screen.queryByText('Running now')).toBeNull()
    expect(screen.queryByText('Running')).toBeNull()
  })

  // This task's finding 11: RunMacroButton's own ScopedButton prints a
  // full refusal sentence PER ROW, which is a wall of identical
  // paragraphs once there is more than one macro and the principal lacks
  // show:macro:run. The fix states the reason ONCE.
  it('states the run-scope refusal once, not once per row, for a principal without show:macro:run', async () => {
    listConfigObjects.mockResolvedValue({
      ...listResponse,
      objects: [
        ...listResponse.objects,
        { id: 'end-set', label: 'End set', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-14T00:00:00Z' },
      ],
    })
    renderMacros(makeModel({ session: adminSession })) // config:write, NOT show:macro:run
    await waitFor(() => expect(screen.getByText('End set')).toBeVisible())

    const reasonMatches = screen.getAllByText(/does not include "show:macro:run"/)
    expect(reasonMatches).toHaveLength(1)
    // Both rows still have a disabled, stated-reason Run control per
    // ADR-024 decision 12 — never hidden — just not a repeated paragraph.
    const runButtons = screen.getAllByRole('button', { name: 'Run' })
    expect(runButtons).toHaveLength(2)
    for (const button of runButtons) expect(button).toBeDisabled()
  })
})
