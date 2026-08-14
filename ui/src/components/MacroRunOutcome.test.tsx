import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MacroRunOutcome } from './MacroRunOutcome'
import { makeMacroRunSummary } from '../api/test-support/fixtures'

afterEach(() => cleanup())

describe('MacroRunOutcome', () => {
  // The single most important requirement of this wave (STEP-9-SPEC.md
  // section 2.3): "completed and confirmed are separate facts... a macro
  // whose MQTT step declares no expected response is structurally
  // unconfirmable and will report completed: true, confirmed: false
  // every single time it runs correctly." If this test passed with only
  // ONE badge on screen, the component would have collapsed the two
  // facts — this is the exact defect ADR-029 decision 4 exists to
  // prevent, and the reason this test asserts both badges are present
  // and carry DIFFERENT text, not just that the component renders at all.
  it('renders completed:true and confirmed:false as two visually distinct facts, not one green tick', () => {
    render(
      <MacroRunOutcome
        run={makeMacroRunSummary({
          state: 'finished',
          completed: true,
          confirmed: false,
          reason: 'step "notify-node-red": the action declares no expected response',
        })}
      />,
    )
    expect(screen.getByText('Completed')).toBeVisible()
    expect(screen.getByText('Not confirmed')).toBeVisible()
    // The two facts must be independently readable, not merged into one
    // sentence or one badge — asserting they are literally different
    // strings is what would catch a regression that renders "Confirmed"
    // for both or renders only one badge.
    expect(screen.queryByText('Confirmed')).toBeNull()
    expect(screen.getByText(/notify-node-red/)).toBeVisible()
  })

  it('renders completed:false and confirmed:true as two distinct facts the other direction', () => {
    render(
      <MacroRunOutcome
        run={makeMacroRunSummary({
          state: 'finished',
          completed: false,
          confirmed: true,
          reason: 'step "start": failed',
        })}
      />,
    )
    expect(screen.getByText('Did not complete')).toBeVisible()
    expect(screen.getByText('Confirmed')).toBeVisible()
  })

  it('renders both as in-progress (unknown) while the run is still running, never as false', () => {
    render(<MacroRunOutcome run={makeMacroRunSummary({ state: 'running', completed: null, confirmed: null })} />)
    expect(screen.getByText('Run in progress')).toBeVisible()
    expect(screen.getByText('Confirmation pending')).toBeVisible()
    expect(screen.queryByText('Did not complete')).toBeNull()
    expect(screen.queryByText('Not confirmed')).toBeNull()
  })

  it('renders a completed and confirmed run with no reason text and no attribution note', () => {
    render(<MacroRunOutcome run={makeMacroRunSummary({ state: 'finished', completed: true, confirmed: true, reason: '' })} />)
    expect(screen.getByText('Completed')).toBeVisible()
    expect(screen.getByText('Confirmed')).toBeVisible()
    expect(screen.queryByText(/could not be recorded in the coordinator/)).toBeNull()
  })

  it('surfaces attributionDegraded as its own note, separate from the completed/confirmed facts', () => {
    render(
      <MacroRunOutcome
        run={makeMacroRunSummary({ state: 'finished', completed: true, confirmed: true, attributionDegraded: true })}
      />,
    )
    expect(screen.getByText(/could not be recorded in the coordinator.s audit log/)).toBeVisible()
  })
})
