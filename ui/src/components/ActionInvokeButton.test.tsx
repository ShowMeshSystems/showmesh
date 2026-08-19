import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ActionInvokeButton } from './ActionInvokeButton'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'

const { invokeAction } = vi.hoisted(() => ({ invokeAction: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, invokeAction }
})

afterEach(() => {
  cleanup()
  invokeAction.mockReset()
})

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:action:invoke'],
})

describe('ActionInvokeButton', () => {
  it('renders disabled with a stated reason, and is never hidden, when show:action:invoke is not held', () => {
    render(
      <ModelContext.Provider value={makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) })}>
        <ActionInvokeButton actionId="blackout-now" />
      </ModelContext.Provider>,
    )
    const button = screen.getByRole('button', { name: 'Go' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
    expect(invokeAction).not.toHaveBeenCalled()
  })

  it('renders "confirmed" honestly, distinguishable from unconfirmable, on a successful dispatch', async () => {
    invokeAction.mockResolvedValue({
      id: 'cmd-1',
      idempotencyKey: 'key-1',
      actionId: 'blackout-now',
      label: 'Blackout',
      replay: false,
      outcome: 'confirmed',
      outcomeReason: 'every layer went dark',
      attributionDegraded: false,
      dispatchedAt: '2026-08-18T00:00:00Z',
      resolvedAt: '2026-08-18T00:00:01Z',
    })
    render(
      <ModelContext.Provider value={makeModel({ session: operatorSession })}>
        <ActionInvokeButton actionId="blackout-now" />
      </ModelContext.Provider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Go' }))
    await waitFor(() => expect(screen.getByText(/Confirmed:/)).toBeVisible())
    expect(invokeAction).toHaveBeenCalledWith('blackout-now')
  })

  it('renders "unconfirmable" with its own distinct word, never merged into "confirmed"', async () => {
    invokeAction.mockResolvedValue({
      id: 'cmd-2',
      idempotencyKey: 'key-2',
      actionId: 'relay-on',
      replay: false,
      outcome: 'unconfirmable',
      outcomeReason: 'this action declares no expected response',
      attributionDegraded: false,
      dispatchedAt: '2026-08-18T00:00:00Z',
      resolvedAt: '2026-08-18T00:00:01Z',
    })
    render(
      <ModelContext.Provider value={makeModel({ session: operatorSession })}>
        <ActionInvokeButton actionId="relay-on" />
      </ModelContext.Provider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Go' }))
    await waitFor(() => expect(screen.getByText(/Unconfirmable/)).toBeVisible())
    expect(screen.queryByText(/^Confirmed:/)).toBeNull()
  })

  it('renders the coordinator error text on a rejected request, never a silent failure', async () => {
    invokeAction.mockRejectedValue(new ApiError('server refused', 403))
    render(
      <ModelContext.Provider value={makeModel({ session: operatorSession })}>
        <ActionInvokeButton actionId="blackout-now" />
      </ModelContext.Provider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Go' }))
    await waitFor(() => expect(screen.getByRole('alert')).toBeVisible())
  })
})
