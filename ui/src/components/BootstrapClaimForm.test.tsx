import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BootstrapClaimForm } from './BootstrapClaimForm'
import { UnauthorizedError } from '../api'

afterEach(cleanup)

describe('BootstrapClaimForm', () => {
  it('submits the trimmed code/name/deviceLabel and the exact password', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    const onSuccess = vi.fn()
    render(<BootstrapClaimForm onSubmit={onSubmit} onSuccess={onSuccess} />)

    await user.type(screen.getByLabelText('Bootstrap code'), '  abc123  ')
    await user.type(screen.getByLabelText('Administrator name'), '  root  ')
    await user.type(screen.getByLabelText('Password'), 'super-secret-value')
    await user.type(screen.getByLabelText('This device’s name'), '  porch tablet  ')
    await user.click(screen.getByRole('button', { name: 'Claim and sign in' }))

    expect(onSubmit).toHaveBeenCalledWith('abc123', 'root', 'super-secret-value', 'porch tablet')
    expect(onSuccess).toHaveBeenCalledOnce()
  })

  it('shows the coordinator-supplied error text on an invalid/claimed/expired code', async () => {
    const user = userEvent.setup()
    const onSubmit = vi
      .fn()
      .mockRejectedValue(new UnauthorizedError(false, 'the bootstrap code is invalid, already claimed, or expired'))
    render(<BootstrapClaimForm onSubmit={onSubmit} />)

    await user.type(screen.getByLabelText('Bootstrap code'), 'wrong-code')
    await user.type(screen.getByLabelText('Administrator name'), 'root')
    await user.type(screen.getByLabelText('Password'), 'password12345')
    await user.type(screen.getByLabelText('This device’s name'), 'porch tablet')
    await user.click(screen.getByRole('button', { name: 'Claim and sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/invalid, already claimed, or expired/)
  })

  it('requires every field before the browser lets submit fire', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(<BootstrapClaimForm onSubmit={onSubmit} />)

    await user.click(screen.getByRole('button', { name: 'Claim and sign in' }))
    expect(onSubmit).not.toHaveBeenCalled()
  })
})
