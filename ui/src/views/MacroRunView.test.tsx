import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MacroRunView } from './MacroRunView'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import {
  makeAuthenticatedSession,
  makeMacroRun,
  makeMacroRunStep,
  makeMacroRunStepCommand,
} from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// This task's finding 10: MacroRunView is the only file carrying a poll
// lifecycle (start, teardown on finish, behaviour on a failed tick), it
// applies the OR read gate to the run surface, and it holds finding 5's
// retained-state behaviour and the run/macro mismatch notice — none of it
// was exercised anywhere before this file. It is also the surface
// acceptance criteria 1, 2, 3, 14 and 22 are read off (STEP-9-SPEC.md
// section 11): this suite renders what those criteria's own server-side
// facts (completed/confirmed/outcome/reason, a not-retained command) look
// like once this view composes them, not the server-side facts
// themselves — those are proved against a running coordinator, per
// section 11's own header ("every one is proved against a running
// coordinator ... not against the test suite").
const { getMacroRun } = vi.hoisted(() => ({ getMacroRun: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getMacroRun }
})

afterEach(() => {
  cleanup()
  getMacroRun.mockReset()
})

function renderRunView(model: Model, initialPath = '/macros/begin-set/runs/run-1') {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route path="/macros/:id/runs/:runId" element={<MacroRunView />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'], // deliberately NOT config:write — same OR-gate shape as Macros.test.tsx
})

describe('MacroRunView', () => {
  // Same OR-scope gate as Macros.tsx/ShowActions.tsx (STEP-9-SPEC.md
  // section 6.6: "Reads on runs require show:macro:run OR config:write").
  it('renders a stated reason, never fetches, for a principal holding neither scope', () => {
    renderRunView(
      makeModel({
        session: makeAuthenticatedSession({
          principal: { id: 'p-3', name: 'viewer-1', kind: 'human', role: 'viewer' },
          scopes: ['node:read'],
        }),
      }),
    )
    expect(getMacroRun).not.toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toMatch(/does not include/)
  })

  it('shows a loading state, then the run: outcome badges, step list, and the macro/run mismatch notice', async () => {
    getMacroRun.mockResolvedValue({
      serverTime: '2026-08-14T17:00:05Z',
      run: makeMacroRun({
        id: 'run-1',
        macroObjectId: 'a-different-macro',
        state: 'running',
        completed: null,
        confirmed: null,
        steps: [
          makeMacroRunStep({ stepIndex: 0, stepId: 'step-a', outcome: 'confirmed' }),
          makeMacroRunStep({ stepIndex: 1, stepId: 'step-b', outcome: null, state: 'pending' }),
        ],
      }),
    })
    renderRunView(makeModel({ session: operatorSession }))

    expect(screen.getByText('Loading run…')).toBeVisible()
    await waitFor(() => expect(screen.getByText('step-a')).toBeVisible())
    expect(screen.getByText('step-b')).toBeVisible()
    // completed/confirmed both render as "in progress" while state is
    // "running" (MacroRunOutcome's own contract) — this only proves THIS
    // view wires the run through to that component.
    expect(screen.getAllByText('Run in progress').length).toBeGreaterThan(0)
    // The run/macro mismatch notice (existing behaviour, unchanged by
    // this task): the URL names macro "begin-set" but the run's own
    // macroObjectId is "a-different-macro".
    expect(screen.getByText(/This run belongs to macro a-different-macro, not begin-set/)).toBeVisible()
  })

  // This task's finding 22 / STEP-9-SPEC.md acceptance criterion 22:
  // "a run whose commands row has been pruned renders with the command
  // detail marked not retained rather than blank" was previously tested
  // only one component down (MacroRunStepRow.test.tsx), never through the
  // view that actually composes it for an operator.
  it('renders a not-retained command through the full view, not just the step-row component in isolation', async () => {
    getMacroRun.mockResolvedValue({
      serverTime: '2026-08-14T17:00:05Z',
      run: makeMacroRun({
        state: 'finished',
        completed: true,
        confirmed: true,
        steps: [
          makeMacroRunStep({
            stepIndex: 0,
            stepId: 'old-step',
            outcome: 'confirmed',
            command: makeMacroRunStepCommand({
              state: 'not_retained',
              id: 'cmd-old',
              reason: 'retention window elapsed',
            }),
          }),
        ],
      }),
    })
    const user = userEvent.setup()
    renderRunView(makeModel({ session: operatorSession }))
    await waitFor(() => expect(screen.getByText('old-step')).toBeVisible())
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(await screen.findByText(/cmd-old/)).toBeVisible()
    expect(screen.getByText(/no longer retained/)).toBeVisible()
    expect(screen.getByText(/retention window elapsed/)).toBeVisible()
  })

  it('states when the run was last successfully updated', async () => {
    getMacroRun.mockResolvedValue({
      serverTime: '2026-08-14T17:00:05Z',
      run: makeMacroRun({ state: 'finished', completed: true, confirmed: true }),
    })
    renderRunView(makeModel({ session: operatorSession }))
    await waitFor(() => expect(screen.getByText(/Last updated/)).toBeVisible())
  })

  it('shows the full-page error, with nothing to retain, when the very first fetch fails', async () => {
    getMacroRun.mockRejectedValue(new Error('the coordinator is unreachable'))
    renderRunView(makeModel({ session: operatorSession }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the coordinator is unreachable')
    // Nothing from a run renders — there is nothing to show yet.
    expect(screen.queryByText('Steps')).toBeNull()
  })

  describe('the poll lifecycle', () => {
    beforeEach(() => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
    })
    afterEach(() => {
      vi.useRealTimers()
    })

    // This task's finding 10 / the header comment on RUNNING_POLL_INTERVAL_MS:
    // "stops entirely once the run's own state is finished" was asserted
    // in a comment and never exercised. This proves the teardown: once a
    // poll returns state: "finished", no further request goes out even
    // after several more interval ticks.
    it('polls a running run on an interval and tears the interval down once the run finishes', async () => {
      getMacroRun
        .mockResolvedValueOnce({
          serverTime: '2026-08-14T17:00:00Z',
          run: makeMacroRun({ state: 'running', completed: null, confirmed: null }),
        })
        .mockResolvedValueOnce({
          serverTime: '2026-08-14T17:00:03Z',
          run: makeMacroRun({ state: 'running', completed: null, confirmed: null }),
        })
        .mockResolvedValue({
          serverTime: '2026-08-14T17:00:06Z',
          run: makeMacroRun({ state: 'finished', completed: true, confirmed: true }),
        })

      renderRunView(makeModel({ session: operatorSession }))
      await vi.waitFor(() => expect(getMacroRun).toHaveBeenCalledTimes(1))

      await vi.advanceTimersByTimeAsync(3_000)
      await vi.waitFor(() => expect(getMacroRun).toHaveBeenCalledTimes(2))

      await vi.advanceTimersByTimeAsync(3_000)
      await vi.waitFor(() => expect(getMacroRun).toHaveBeenCalledTimes(3))
      await vi.waitFor(() => expect(screen.getByText('Completed')).toBeVisible())

      // The run is now "finished" — several more ticks must not produce
      // any further request.
      await vi.advanceTimersByTimeAsync(3_000)
      await vi.advanceTimersByTimeAsync(3_000)
      await vi.advanceTimersByTimeAsync(3_000)
      expect(getMacroRun).toHaveBeenCalledTimes(3)
    })

    // This task's finding 5, exercised through the real poll lifecycle
    // rather than by rendering a hand-built state directly: a poll tick
    // that fails must retain the previously rendered run (outcome badges,
    // step list, everything) and add a banner, never replace the view.
    it('retains the rendered run and shows a banner, never a replacement, when a later poll tick fails', async () => {
      getMacroRun
        .mockResolvedValueOnce({
          serverTime: '2026-08-14T17:00:00Z',
          run: makeMacroRun({
            state: 'running',
            completed: null,
            confirmed: null,
            steps: [makeMacroRunStep({ stepIndex: 0, stepId: 'still-here', outcome: 'confirmed' })],
          }),
        })
        .mockRejectedValueOnce(new Error('the coordinator is unreachable'))

      renderRunView(makeModel({ session: operatorSession }))
      await vi.waitFor(() => expect(screen.getByText('still-here')).toBeVisible())

      await vi.advanceTimersByTimeAsync(3_000)
      await vi.waitFor(() =>
        expect(screen.getByText(/Could not refresh this run just now/)).toBeVisible(),
      )
      // The previously rendered run is UNCHANGED, not replaced by the
      // error — this is the exact behaviour finding 5 names.
      expect(screen.getByText('still-here')).toBeVisible()
      expect(screen.getByText('Confirmed')).toBeVisible()
      expect(screen.getByText(/the coordinator is unreachable/)).toBeVisible()
    })
  })
})
