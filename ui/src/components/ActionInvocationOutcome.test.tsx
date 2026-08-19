import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ActionInvocationOutcome } from './ActionInvocationOutcome'
import type { ActionInvocationResult } from '../app/types'

afterEach(() => cleanup())

function makeResult(overrides: Partial<ActionInvocationResult>): ActionInvocationResult {
  return {
    id: 'cmd-1',
    idempotencyKey: 'key-1',
    actionId: 'blackout-now',
    revision: 1,
    replay: false,
    state: 'resolved',
    outcome: 'confirmed',
    outcomeReason: 'went dark',
    dispatchAttribution: 'complete',
    dispatchAttributionReason: 'the pre-dispatch audit entry was written durably before dispatch',
    outcomeAttribution: 'complete',
    outcomeAttributionReason: 'the outcome audit entry was written durably',
    attributionDegraded: false,
    dispatchedAt: '2026-08-19T00:00:00Z',
    resolvedAt: '2026-08-19T00:00:01Z',
    ...overrides,
  }
}

describe('ActionInvocationOutcome', () => {
  it('renders "confirmed" with the server-provided reason, never as an alert', () => {
    render(<ActionInvocationOutcome result={makeResult({ outcome: 'confirmed', outcomeReason: 'went dark' })} />)
    expect(screen.getByText(/Confirmed: went dark/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('renders "unconfirmable" with its own distinct word, never merged into "confirmed"', () => {
    render(
      <ActionInvocationOutcome
        result={makeResult({ outcome: 'unconfirmable', outcomeReason: 'this action declares no expected response' })}
      />,
    )
    expect(screen.getByText(/Unconfirmable/)).toBeVisible()
    expect(screen.queryByText(/^Confirmed:/)).toBeNull()
  })

  it('renders every documented outcome word distinctly', () => {
    const words: ActionInvocationResult['outcome'][] = ['unconfirmed', 'refused', 'failed']
    for (const outcome of words) {
      const { unmount } = render(<ActionInvocationOutcome result={makeResult({ outcome, outcomeReason: 'reason' })} />)
      expect(screen.getByRole('alert')).toBeVisible()
      unmount()
    }
  })

  it('renders "pending" with the server-stated reason when outcome is null (SM-100)', () => {
    render(
      <ActionInvocationOutcome
        result={makeResult({ state: 'pending', outcome: null, outcomeReason: 'still dispatching' })}
      />,
    )
    expect(screen.getByText(/Pending: still dispatching/)).toBeVisible()
  })

  // A review finding: an outcome word outside the six documented values
  // used to render nothing at all, and a blank reads as fine.
  it('renders a visible fallback for an outcome word outside the documented six', () => {
    render(
      <ActionInvocationOutcome
        result={{ ...makeResult({}), outcome: 'somethingNew' } as unknown as ActionInvocationResult}
      />,
    )
    expect(screen.getByRole('alert')).toBeVisible()
    expect(screen.getByText(/Unrecognized outcome/)).toBeVisible()
  })

  it('shows the replay notice when replay is true', () => {
    render(<ActionInvocationOutcome result={makeResult({ replay: true })} />)
    expect(screen.getByText(/already requested/)).toBeVisible()
  })

  it('shows the degraded-attribution note when attributionDegraded is true', () => {
    render(<ActionInvocationOutcome result={makeResult({ attributionDegraded: true })} />)
    expect(screen.getByText(/could not record this invocation/)).toBeVisible()
  })
})
