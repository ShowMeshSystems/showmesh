import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RunMacroButton } from './RunMacroButton'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

const { submitMacroRun } = vi.hoisted(() => ({ submitMacroRun: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, submitMacroRun }
})

afterEach(() => {
  cleanup()
  submitMacroRun.mockReset()
})

function renderButton(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/macros/begin-set']}>
        <Routes>
          <Route path="/macros/:id" element={<RunMacroButton macroId="begin-set" />} />
          <Route path="/macros/:id/runs/:runId" element={<div>NAVIGATED-TO-RUN-VIEW</div>} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'],
})

describe('RunMacroButton', () => {
  // STEP-9-SPEC.md section 9 / OPERATOR-UI section 14: "a control the
  // principal may not use renders disabled with a stated reason and is
  // never hidden." ScopedButton itself already proves this in isolation
  // (ScopedButton.test.tsx); this is the check that RunMacroButton
  // actually WIRES it up rather than, say, rendering nothing when the
  // scope is missing.
  it('renders disabled with a stated reason, and is never hidden, when show:macro:run is not held', () => {
    renderButton(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    const button = screen.getByRole('button', { name: 'Run' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
    expect(screen.getByText(/does not include/)).toBeVisible()
    expect(submitMacroRun).not.toHaveBeenCalled()
  })

  it('navigates to the run view on a 202, without waiting for the run to resolve', async () => {
    submitMacroRun.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      replay: false,
      run: {
        id: 'run-1',
        macroObjectId: 'begin-set',
        macroRevision: 1,
        show: 'halloween-2026',
        trigger: 'ui',
        issuerPrincipalId: 'p-1',
        issuerPrincipalName: 'operator-1',
        createdAt: '2026-08-14T00:00:00Z',
        finishedAt: null,
        state: 'running',
        completed: null,
        confirmed: null,
        reason: '',
        attributionDegraded: false,
      },
    })
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Run' }))
    await waitFor(() => expect(screen.getByText('NAVIGATED-TO-RUN-VIEW')).toBeVisible())
  })

  it('offers a direct link to the in-flight run on an overlap 409, naming it via conflictingRunId', async () => {
    submitMacroRun.mockRejectedValue(
      new ApiError(
        'a run of this macro is already in progress',
        409,
        'https://showmesh.dev/problems/macro-run-already-in-flight',
        'run-already-running',
      ),
    )
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Run' }))
    await waitFor(() => expect(screen.getByText(/already in progress/)).toBeVisible())
    const link = screen.getByRole('link', { name: /view that run/i })
    expect(link).toHaveAttribute('href', '/macros/begin-set/runs/run-already-running')
  })

  it('renders a plain error for a non-conflict failure', async () => {
    submitMacroRun.mockRejectedValue(new ApiError('the coordinator is unreachable'))
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Run' }))
    await waitFor(() => expect(screen.getByText('the coordinator is unreachable')).toBeVisible())
    expect(screen.queryByRole('link')).toBeNull()
  })
})
