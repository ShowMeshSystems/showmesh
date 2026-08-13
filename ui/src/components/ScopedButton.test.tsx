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

function renderScoped(
  model: Model,
  onClick = vi.fn(),
  extra: { busy?: boolean; busyReason?: string } = {},
) {
  render(
    <ModelContext.Provider value={model}>
      <ScopedButton requiredScope="show:macro:run" onClick={onClick} {...extra}>
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

  // Step 7 seam A review defect 8: two fast clicks on a save control must
  // not fire two submissions. This proves the COMPONENT half — a scoped,
  // permitted action still renders disabled and unclickable while `busy`
  // is true, with a reason distinct from "not permitted" (ADR-024 decision
  // 12 requires a stated reason either way; the two must not read the
  // same). Configuration.test.tsx proves the CALLER half: the actual
  // double-click guard.
  it('renders disabled and unclickable while busy, even though the scope is held', async () => {
    const user = userEvent.setup()
    const model = makeModel({ session: signedIn() })
    const onClick = renderScoped(model, vi.fn(), { busy: true, busyReason: 'Saving…' })

    const button = screen.getByRole('button', { name: 'Run macro' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByText('Saving…')).toBeVisible()
    await user.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('renders the not-permitted reason, not the busy reason, when both are true — a control cannot be usefully busy doing something it may not do', () => {
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    renderScoped(model, vi.fn(), { busy: true, busyReason: 'Saving…' })

    const button = screen.getByRole('button', { name: 'Run macro' })
    expect(button).toBeDisabled()
    expect(button).not.toHaveAttribute('aria-busy')
    expect(screen.getByText(/show:macro:run/)).toBeVisible()
    expect(screen.queryByText('Saving…')).not.toBeInTheDocument()
  })

  // CLAUDE.md DEFECT 4: the two tests above each pin ONE reason string in
  // isolation ('Saving…' for busy, the missing-scope sentence for
  // not-permitted). Neither, alone, proves the two are actually
  // DIFFERENT text an operator could never confuse — a component that
  // collapsed both to one generic "unavailable" string would pass both
  // of those tests individually. This one reads both reasons from two
  // separate renders and asserts inequality directly, plus that neither
  // string is a substring of the other (ruling out one merely being a
  // more-detailed version of the same claim).
  it('states a genuinely different reason for "busy" than for "not permitted" — never confusable', () => {
    const busyModel = makeModel({ session: signedIn() })
    const { unmount } = render(
      <ModelContext.Provider value={busyModel}>
        <ScopedButton requiredScope="show:macro:run" onClick={vi.fn()} busy busyReason="Saving…">
          Run macro
        </ScopedButton>
      </ModelContext.Provider>,
    )
    const busyReasonText = screen.getByText('Saving…').textContent ?? ''
    unmount()

    const notPermittedModel = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    render(
      <ModelContext.Provider value={notPermittedModel}>
        <ScopedButton requiredScope="show:macro:run" onClick={vi.fn()}>
          Run macro
        </ScopedButton>
      </ModelContext.Provider>,
    )
    const notPermittedReasonText = screen.getByText(/show:macro:run/).textContent ?? ''

    expect(busyReasonText).not.toBe(notPermittedReasonText)
    expect(notPermittedReasonText.includes(busyReasonText)).toBe(false)
    expect(busyReasonText.includes(notPermittedReasonText)).toBe(false)
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
