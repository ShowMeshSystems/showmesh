import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BrowserRouter, Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { UnsavedChangesProvider, useUnsavedChanges } from './UnsavedChanges'

function SettingsForm() {
  const { clearUnsavedChanges } = useUnsavedChanges('settings')
  return (
    <div data-unsaved-form="settings">
      <label>
        Settings value
        <input defaultValue="saved" />
      </label>
      <Link to="/elsewhere">Leave settings</Link>
      <button type="button" onClick={clearUnsavedChanges}>Save settings</button>
    </div>
  )
}

function TwoSettingsForms() {
  const first = useUnsavedChanges('first')
  const second = useUnsavedChanges('second')
  return (
    <>
      <div data-unsaved-form="first">
        <label>First value<input defaultValue="saved" /></label>
        <button type="button" onClick={first.clearUnsavedChanges}>Save first</button>
      </div>
      <div data-unsaved-form="second">
        <label>Second value<input defaultValue="saved" /></label>
        <button type="button" onClick={second.clearUnsavedChanges}>Save second</button>
      </div>
      <Link to="/elsewhere">Leave settings</Link>
    </>
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

function renderBrowserGuard() {
  window.history.replaceState({ idx: 0 }, '', '/settings')
  return render(
    <BrowserRouter>
      <UnsavedChangesProvider>
        <Routes>
          <Route path="/settings" element={<SettingsForm />} />
          <Route path="/elsewhere" element={<p>Elsewhere</p>} />
        </Routes>
      </UnsavedChangesProvider>
    </BrowserRouter>,
  )
}

describe('UnsavedChangesProvider', () => {
  afterEach(cleanup)

  it('blocks dirty internal navigation, traps focus, and returns it after staying', async () => {
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
    await user.tab()
    expect(stay).toHaveFocus()
    await user.tab({ shift: true })
    expect(screen.getByRole('button', { name: 'Discard changes' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.getByLabelText('Settings value')).toHaveValue('entered value')
    expect(input).toHaveFocus()
    expect(screen.queryByText('Elsewhere')).not.toBeInTheDocument()
  })

  it('keeps a second dirty owner protected after the first owner saves', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/settings']}>
        <UnsavedChangesProvider>
          <Routes>
            <Route path="/settings" element={<TwoSettingsForms />} />
            <Route path="/elsewhere" element={<p>Elsewhere</p>} />
          </Routes>
        </UnsavedChangesProvider>
      </MemoryRouter>,
    )

    await user.type(screen.getByLabelText('First value'), ' changed')
    await user.type(screen.getByLabelText('Second value'), ' changed')
    await user.click(screen.getByRole('button', { name: 'Save first' }))

    const dirtyEvent = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(dirtyEvent)
    expect(dirtyEvent.defaultPrevented).toBe(true)
    await user.click(screen.getByRole('link', { name: 'Leave settings' }))
    expect(screen.getByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeInTheDocument()
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

  it('keeps dirty edits visible and warns before a BrowserRouter forward navigation', async () => {
    const user = userEvent.setup()
    renderBrowserGuard()

    await user.type(screen.getByLabelText('Settings value'), ' changed')
    const historyGo = vi.spyOn(window.history, 'go').mockImplementation(() => undefined)
    window.history.pushState({ idx: 1 }, '', '/elsewhere')
    await act(async () => {
      fireEvent.popState(window, { state: { idx: 1 } })
    })

    expect(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).toBeInTheDocument()
    expect(historyGo).toHaveBeenCalledWith(-1)
    expect(screen.getByLabelText('Settings value')).toHaveValue('saved changed')
    historyGo.mockRestore()
  })
})
