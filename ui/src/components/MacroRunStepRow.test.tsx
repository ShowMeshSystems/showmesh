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
    render(<MacroRunStepRow step={makeMacroRunStep({ command: makeMacroRunStepCommand({ state: 'none' }) })} />)
    await user.click(screen.getByRole('button', { name: /show detail/i }))
    expect(screen.getByText(/did not dispatch an FPP command/)).toBeVisible()

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
})
