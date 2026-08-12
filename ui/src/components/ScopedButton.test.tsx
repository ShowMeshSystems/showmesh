import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ScopedButton } from './ScopedButton'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { Model, SessionResponse } from '../app/types'

afterEach(cleanup)

const NOW = '2026-08-12T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['show:macro:run'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function renderScoped(model: Model, onClick = vi.fn()) {
  render(
    <ModelContext.Provider value={model}>
      <ScopedButton requiredScope="show:macro:run" onClick={onClick}>
        Run macro
      </ScopedButton>
    </ModelContext.Provider>,
  )
  return onClick
}

describe('ScopedButton', () => {
  it('renders enabled and clickable when the session holds the required scope', async () => {
    const user = userEvent.setup()
    const model = makeModel({ session: signedIn() })
    const onClick = renderScoped(model)

    const button = screen.getByRole('button', { name: 'Run macro' })
    expect(button).toBeEnabled()
    await user.click(button)
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('renders disabled with a visible, non-tooltip-only reason when the scope is missing', async () => {
    const user = userEvent.setup()
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    const onClick = renderScoped(model)

    const button = screen.getByRole('button', { name: 'Run macro' })
    expect(button).toBeDisabled()
    // Visible text, not only a `title` attribute a touchscreen operator
    // cannot hover — OPERATOR-UI section 11's "an absent control is
    // indistinguishable from a feature that does not exist" applies just
    // as much to a reason nobody can actually read.
    expect(screen.getByText(/show:macro:run/)).toBeVisible()
    await user.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('renders disabled, never enabled, when signed out — the control must not silently default to permitted', () => {
    const model = makeModel({ session: null })
    renderScoped(model)
    expect(screen.getByRole('button', { name: 'Run macro' })).toBeDisabled()
  })

  it('renders disabled when the session fetch is unconfirmed, even if a permissive session is cached (ADR-024 decision 12)', () => {
    // The trap case: a session that WOULD grant the scope, but the most
    // recent attempt to confirm it failed. If evaluateScope's
    // sessionFetchFailed check were ever dropped, this is the one test
    // that would start rendering an enabled button from stale data.
    const model = makeModel({ session: signedIn(), sessionFetchFailed: true })
    renderScoped(model)
    expect(screen.getByRole('button', { name: 'Run macro' })).toBeDisabled()
  })
})
