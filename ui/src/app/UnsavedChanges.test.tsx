import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { UnsavedChangesProvider, useUnsavedChanges } from './UnsavedChanges'

function SettingsForm() {
  const { clearUnsavedChanges } = useUnsavedChanges()
  return (
    <div data-unsaved-form>
      <label>
        Settings value
        <input defaultValue="saved" />
      </label>
      <Link to="/elsewhere">Leave settings</Link>
      <button type="button" onClick={clearUnsavedChanges}>Save settings</button>
    </div>
  )
}

function renderGuard() {
  return render(
    <MemoryRouter initialEntries={['/settings']}>
      <UnsavedChangesProvider>
        <Routes>
          <Route path="/settings" element={<SettingsForm />} />
          <Route path="/elsewhere" element={<p>Elsewhere</p>} />
        </Routes>
      </UnsavedChangesProvider>
    </MemoryRouter>,
  )
}

describe('UnsavedChangesProvider', () => {
  afterEach(cleanup)

  it('blocks dirty internal navigation, keeps edits when staying, and focuses the warning action', async () => {
    const user = userEvent.setup()
    renderGuard()

    const input = screen.getByLabelText('Settings value')
    await user.clear(input)
    await user.type(input, 'entered value')
    await user.click(screen.getByRole('link', { name: 'Leave settings' }))

    expect(screen.getByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeInTheDocument()
    const stay = screen.getByRole('button', { name: 'Stay' })
    expect(stay).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Discard changes' })).toHaveFocus()
    await user.click(stay)
    expect(screen.getByLabelText('Settings value')).toHaveValue('entered value')
    expect(screen.queryByText('Elsewhere')).not.toBeInTheDocument()
  })

  it('discards only after the explicit action and then continues to the requested page', async () => {
    const user = userEvent.setup()
    renderGuard()

    await user.type(screen.getByLabelText('Settings value'), ' changed')
    await user.click(screen.getByRole('link', { name: 'Leave settings' }))
    await user.click(screen.getByRole('button', { name: 'Discard changes' }))

    expect(screen.getByText('Elsewhere')).toBeInTheDocument()
  })

  it('sets the browser beforeunload protection only while a marked form is dirty', async () => {
    const user = userEvent.setup()
    renderGuard()
    const cleanEvent = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(cleanEvent)
    expect(cleanEvent.defaultPrevented).toBe(false)

    await user.type(screen.getByLabelText('Settings value'), ' changed')
    const dirtyEvent = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(dirtyEvent)
    expect(dirtyEvent.defaultPrevented).toBe(true)
  })

  it('removes protection after a successful form save clears its dirty state', async () => {
    const user = userEvent.setup()
    renderGuard()

    await user.type(screen.getByLabelText('Settings value'), ' changed')
    await user.click(screen.getByRole('button', { name: 'Save settings' }))
    await user.click(screen.getByRole('link', { name: 'Leave settings' }))

    expect(screen.getByText('Elsewhere')).toBeInTheDocument()
  })
})
