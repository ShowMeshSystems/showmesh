import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { MacroRunStepRow } from './MacroRunStepRow'
import { makeMacroRunStep, makeMacroRunStepCommand } from '../api/test-support/fixtures'

afterEach(() => cleanup())

describe('MacroRunStepRow', () => {
  // STEP-9-SPEC.md section 6.4: "unconfirmable is structural, meaning the
  // step declared no expected response, and is distinct from
  // unconfirmed, which means evidence was expected and did not arrive.
  // Collapsing the two would make an honest 'nothing can confirm this'
  // indistinguishable from a real failure." This test is the regression
  // guard for exactly that collapse.
  it('renders unconfirmable and unconfirmed with different labels, neither one reading as failed', () => {
    const { rerender } = render(
      <MacroRunStepRow step={makeMacroRunStep({ outcome: 'unconfirmable', outcomeReason: 'no response was ever expected' })} />,
    )
    expect(screen.getByText(/Unconfirmable/)).toBeVisible()
    expect(screen.queryByText('Failed')).toBeNull()

    rerender(<MacroRunStepRow step={makeMacroRunStep({ outcome: 'unconfirmed', outcomeReason: 'deadline elapsed' })} />)
    expect(screen.getByText('Unconfirmed')).toBeVisible()
    expect(screen.queryByText(/Unconfirmable/)).toBeNull()
    expect(screen.queryByText('Failed')).toBeNull()
  })

  it('renders a failed step visibly distinct from unconfirmed/unconfirmable', () => {
    render(<MacroRunStepRow step={makeMacroRunStep({ outcome: 'failed', outcomeReason: 'FPP reported an error' })} />)
    expect(screen.getByText('Failed')).toBeVisible()
    expect(screen.getByText(/FPP reported an error/)).toBeVisible()
  })

  it('renders a skipped step as skipped, not as a failure', () => {
    render(<MacroRunStepRow step={makeMacroRunStep({ outcome: 'skipped', outcomeReason: '' })} />)
    expect(screen.getByText('Skipped')).toBeVisible()
    expect(screen.queryByText('Failed')).toBeNull()
  })

  it('does not show step detail until expanded, and shows it after', async () => {
    const user = userEvent.setup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          command: makeMacroRunStepCommand({ state: 'not_retained', id: 'cmd-1', reason: 'pruned by retention' }),
        })}
      />,
    )
    expect(screen.queryByText(/pruned by retention/)).toBeNull()
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/pruned by retention/)).toBeVisible()
  })

  // STEP-9-SPEC.md section 6.1: "the run view renders the step's own
  // recorded outcome with the command detail marked not retained, with a
  // reason. It never renders blank and it never renders as though the
  // step had no command."
  it('marks a not_retained command explicitly, distinct from a step that never dispatched one', async () => {
    const user = userEvent.setup()
    // Default step fixture: integration "fpp", state "pending" — a
    // pending fpp step's command.state is "none" because it has not
    // dispatched YET, not because it never will (this task's finding 2).
    render(<MacroRunStepRow step={makeMacroRunStep({ command: makeMacroRunStepCommand({ state: 'none' }) })} />)
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/has not dispatched a command yet/)).toBeVisible()

    cleanup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          command: makeMacroRunStepCommand({ state: 'not_retained', id: 'cmd-9', reason: 'retention window elapsed' }),
        })}
      />,
    )
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/cmd-9/)).toBeVisible()
    expect(screen.getByText(/no longer retained/)).toBeVisible()
    expect(screen.getByText(/retention window elapsed/)).toBeVisible()
  })

  // This task's finding 2: command.state "none" collapses "never will
  // have one" (an mqtt step) and "does not have one yet" (a pending/
  // skipped fpp step) into ONE server-side reason. The component must
  // still tell them apart for the operator, from step.integration/state.
  it('distinguishes an mqtt step (never has a command) from a skipped fpp step (will not dispatch now) from a pending fpp step (not yet)', async () => {
    const user = userEvent.setup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          integration: 'mqtt',
          command: makeMacroRunStepCommand({ state: 'none', reason: 'this step has no dispatched command...' }),
        })}
      />,
    )
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/never dispatches an FPP command/)).toBeVisible()

    cleanup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          integration: 'fpp',
          state: 'skipped',
          outcome: 'skipped',
          command: makeMacroRunStepCommand({ state: 'none' }),
        })}
      />,
    )
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/skipped before it dispatched/)).toBeVisible()

    cleanup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          integration: 'fpp',
          state: 'pending',
          command: makeMacroRunStepCommand({ state: 'none' }),
        })}
      />,
    )
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/has not dispatched a command yet/)).toBeVisible()
  })

  // step.state is closed to "pending" | "resolved" | "skipped" — there is
  // no "dispatched" intermediate the server can emit, so the lifecycle
  // distinction worth rendering is pending (outcome null, not yet run)
  // versus a step that has recorded a real outcome.
  it('renders a pending step (outcome null) distinctly from a step that has resolved', () => {
    const { rerender } = render(
      <MacroRunStepRow step={makeMacroRunStep({ state: 'pending', outcome: null })} />,
    )
    expect(screen.getByText('Pending')).toBeVisible()

    rerender(<MacroRunStepRow step={makeMacroRunStep({ state: 'resolved', outcome: 'confirmed' })} />)
    expect(screen.getByText('Confirmed')).toBeVisible()
    expect(screen.queryByText('Pending')).toBeNull()
  })

  // This task's finding 7: dispatchedAt/resolvedAt were never rendered.
  it('renders dispatch and resolve timestamps once expanded', async () => {
    const user = userEvent.setup()
    render(
      <MacroRunStepRow
        step={makeMacroRunStep({
          state: 'resolved',
          outcome: 'confirmed',
          dispatchedAt: '2026-08-14T17:00:00Z',
          resolvedAt: '2026-08-14T17:00:01Z',
        })}
      />,
    )
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(new Date('2026-08-14T17:00:00Z').toLocaleString())).toBeVisible()
    expect(screen.getByText(new Date('2026-08-14T17:00:01Z').toLocaleString())).toBeVisible()
  })
})
