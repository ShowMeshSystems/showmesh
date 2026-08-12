import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SignInForm } from './SignInForm'
import { UnauthorizedError } from '../api'

afterEach(cleanup)

describe('SignInForm', () => {
  it('submits the trimmed name/deviceLabel and the exact password, and never echoes the password as text', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockResolvedValue(undefined)
    const onSuccess = vi.fn()
    const { container } = render(<SignInForm onSubmit={onSubmit} onSuccess={onSuccess} />)

    await user.type(screen.getByLabelText('Name'), '  alice  ')
    await user.type(screen.getByLabelText('Password'), 'super-secret-value')
    await user.type(screen.getByLabelText('This device’s name'), '  porch tablet  ')

    const passwordInput = screen.getByLabelText('Password') as HTMLInputElement
    expect(passwordInput.type).toBe('password')
    expect(container.textContent).not.toContain('super-secret-value')

    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(onSubmit).toHaveBeenCalledWith('alice', 'super-secret-value', 'porch tablet')
    expect(onSuccess).toHaveBeenCalledOnce()
    expect(container.textContent).not.toContain('super-secret-value')
  })

  it('does not call onSuccess and shows the error text when onSubmit rejects', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockRejectedValue(new UnauthorizedError(false, 'invalid name or password'))
    const onSuccess = vi.fn()
    render(<SignInForm onSubmit={onSubmit} onSuccess={onSuccess} />)

    await user.type(screen.getByLabelText('Name'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'wrong')
    await user.type(screen.getByLabelText('This device’s name'), 'porch tablet')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid name or password')
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('clears the password field after a failed attempt but keeps the name', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn().mockRejectedValue(new UnauthorizedError(false, 'invalid name or password'))
    render(<SignInForm onSubmit={onSubmit} />)

    await user.type(screen.getByLabelText('Name'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'wrong')
    await user.type(screen.getByLabelText('This device’s name'), 'porch tablet')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('alert')

    expect((screen.getByLabelText('Password') as HTMLInputElement).value).toBe('')
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('alice')
  })

  it('requires every field before the browser lets submit fire', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    render(<SignInForm onSubmit={onSubmit} />)

    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(onSubmit).not.toHaveBeenCalled()
  })
})
