import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getEmergencyStopConfig: vi.fn(),
  putEmergencyStopConfig: vi.fn(),
  listConfigObjects: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { SettingsEmergencyStop } = await import('./SettingsEmergencyStop')

const session = (scopes: string[]) =>
  ({ authenticated: true, principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' }, scopes, scopesState: 'current', bootstrapRequired: false }) as unknown as SessionResponse

const response = (hardStop: string[], revision = 1) => ({
  serverTime: '', kind: 'show.emergencystop', revision, updatedAt: '', createdByPrincipalId: null, createdByPrincipalName: null, source: 'stored',
  payload: { stop: { actions: [] }, stopPowerDown: { actions: [] }, hardStop: { actions: hardStop } },
})

function renderTab(scopes: string[]) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: session(scopes), sessionFetchFailed: false }}>
      <SettingsEmergencyStop />
    </ModelContext.Provider>,
  )
}

describe('SettingsEmergencyStop', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('adds a hard stop follow-up from the action list and saves all three levels', async () => {
    stubs.getEmergencyStopConfig.mockResolvedValue(response([]))
    stubs.listConfigObjects.mockResolvedValue({ objects: [{ id: 'work-light-on', label: 'Work light on', show: 'halloween', currentRevision: 1, updatedAt: '' }] })
    stubs.putEmergencyStopConfig.mockResolvedValue(response(['work-light-on'], 2))
    renderTab(['config:write'])

    await screen.findByText('Hard stop')
    const addButtons = screen.getAllByRole('button', { name: 'Add action' })
    fireEvent.click(addButtons[2] as HTMLElement)
    fireEvent.change(screen.getByLabelText('Action 1'), { target: { value: 'work-light-on' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(stubs.putEmergencyStopConfig).toHaveBeenCalledWith({ stop: { actions: [] }, stopPowerDown: { actions: [] }, hardStop: { actions: ['work-light-on'] } }))
    expect(await screen.findByText('Saved as version 2.')).toBeTruthy()
  })

  it('shows an action that no longer exists and refuses edits without config:write', async () => {
    stubs.getEmergencyStopConfig.mockResolvedValue(response(['deleted-action']))
    stubs.listConfigObjects.mockResolvedValue({ objects: [] })
    renderTab([])

    expect(await screen.findByText('deleted-action (not found)')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getAllByRole('button', { name: 'Add action' })[0] as HTMLButtonElement).disabled).toBe(true)
  })
})
