import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightCommandButton } from './NightCommandButton'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeNightSessionState } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model, NightSessionState } from '../app/types'

const { dispatchNightCommand } = vi.hoisted(() => ({ dispatchNightCommand: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, dispatchNightCommand }
})

afterEach(() => {
  cleanup()
  dispatchNightCommand.mockReset()
})

function renderButton(model: Model, onApplied: (session: NightSessionState) => void = () => {}) {
  return render(
    <ModelContext.Provider value={model}>
      <NightCommandButton command="start-night" label="Start night" onApplied={onApplied} />
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['night:command'],
})

describe('NightCommandButton', () => {
  // ADR-024 decision 12 / OPERATOR-UI section 14: a control the principal
  // may not use renders disabled with a stated reason and is never
  // hidden. ScopedButton itself already proves this in isolation; this is
  // the check that NightCommandButton actually wires it up.
  it('renders disabled with a stated reason, and is never hidden, when night:command is not held', () => {
    renderButton(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    const button = screen.getByRole('button', { name: 'Start night' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
    expect(screen.getByText(/does not include/)).toBeVisible()
    expect(dispatchNightCommand).not.toHaveBeenCalled()
  })

  it('calls onApplied with the resulting session on a 202', async () => {
    const session = makeNightSessionState({ state: 'live' })
    dispatchNightCommand.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      command: { command: 'start-night', outcome: 'applied', attributionDegraded: false },
      session,
    })
    const onApplied = vi.fn()
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }), onApplied)
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(onApplied).toHaveBeenCalledWith(session))
  })

  // The three distinguishable 409 causes and the one 503 cause
  // (api/openapi.yaml POST /night/commands/{command}) must render with
  // DIFFERENT wording, never collapsed into one generic failure message —
  // this is the acceptance criterion this component exists to satisfy.
  it('renders the night-not-ready 409 distinguishably', async () => {
    dispatchNightCommand.mockRejectedValue(
      new ApiError('readiness has not been recorded for this epoch', 409, 'https://showmesh.dev/problems/night-not-ready'),
    )
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(screen.getByText(/Not ready yet:/)).toBeVisible())
  })

  it('renders the night-state-rejected 409 distinguishably', async () => {
    dispatchNightCommand.mockRejectedValue(
      new ApiError('start-night is not valid from state preparing', 409, 'https://showmesh.dev/problems/night-state-rejected'),
    )
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(screen.getByText(/Refused for the session.s current state:/)).toBeVisible())
  })

  it('renders the night-ambiguous 409 distinguishably, naming the recovery path', async () => {
    dispatchNightCommand.mockRejectedValue(
      new ApiError('the session is degraded', 409, 'https://showmesh.dev/problems/night-ambiguous'),
    )
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(screen.getByText(/degraded and ambiguous/)).toBeVisible())
    expect(screen.getByText(/End session/)).toBeVisible()
  })

  it('renders the audit-unavailable 503 distinguishably', async () => {
    dispatchNightCommand.mockRejectedValue(
      new ApiError(
        'the audit store could not be written',
        503,
        'https://showmesh.dev/problems/night-command-refused-audit-unavailable',
      ),
    )
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(screen.getByText(/audit store is unavailable/)).toBeVisible())
  })

  it('renders a plain error for a non-dispatchable failure', async () => {
    dispatchNightCommand.mockRejectedValue(new ApiError('the coordinator is unreachable'))
    const user = userEvent.setup()
    renderButton(makeModel({ session: operatorSession }))
    await user.click(screen.getByRole('button', { name: 'Start night' }))
    await waitFor(() => expect(screen.getByText('the coordinator is unreachable')).toBeVisible())
  })
})
