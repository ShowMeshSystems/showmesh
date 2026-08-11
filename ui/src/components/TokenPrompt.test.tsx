import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TokenPrompt } from './TokenPrompt'

// See EvidenceValue.test.tsx for why this is registered explicitly here.
afterEach(cleanup)

describe('TokenPrompt', () => {
  it('submits the entered token and never renders it back out as text', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    const { container } = render(<TokenPrompt reason="missing" onSubmit={onSubmit} />)

    const input = screen.getByPlaceholderText('API token') as HTMLInputElement
    // The masking mechanism itself: an input with type other than
    // "password" would echo the secret in plain text as the operator
    // types, regardless of anything rendered elsewhere.
    expect(input.type).toBe('password')

    await user.type(input, 'super-secret-value')

    // Checked immediately after typing -- before submit, before any
    // clearing -- so this cannot be satisfied by post-submit state
    // clearing (e.g. `setValue('')`) standing in for secret hygiene. No
    // element anywhere in the rendered tree may echo the secret as text;
    // the masked <input>'s own `value` property is exempt since jsdom
    // does not expose it via textContent, but an added `<span>{value}</span>`
    // live-echo would show up here.
    expect(container.textContent).not.toContain('super-secret-value')

    await user.click(screen.getByRole('button', { name: 'Connect' }))

    expect(onSubmit).toHaveBeenCalledWith('super-secret-value')
    expect(container.textContent).not.toContain('super-secret-value')
  })

  it('does not submit an empty or whitespace-only token', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(<TokenPrompt reason="missing" onSubmit={onSubmit} />)

    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(onSubmit).not.toHaveBeenCalled()

    await user.type(screen.getByPlaceholderText('API token'), '   ')
    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('shows the rejection message only when the previous token was rejected', () => {
    const { rerender } = render(<TokenPrompt reason="missing" onSubmit={() => {}} />)
    expect(screen.queryByText(/was rejected/)).not.toBeInTheDocument()

    rerender(<TokenPrompt reason="rejected" onSubmit={() => {}} />)
    expect(screen.getByText(/was rejected/)).toBeInTheDocument()
  })
})
