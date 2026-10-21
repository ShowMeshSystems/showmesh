import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getEventsMQTTConfig: vi.fn(),
  putEventsMQTTConfig: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { SettingsHomeAssistant } = await import('./SettingsHomeAssistant')

const session = (scopes: string[]) =>
  ({ authenticated: true, principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' }, scopes, scopesState: 'current', bootstrapRequired: false }) as unknown as SessionResponse

function renderWith(scopes: string[]) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: session(scopes), sessionFetchFailed: false }}>
      <SettingsHomeAssistant />
    </ModelContext.Provider>,
  )
}

const blank = { serverTime: '', kind: 'events.mqtt', revision: 0, updatedAt: '', createdByPrincipalId: null, createdByPrincipalName: null, source: 'default', payload: { enabled: false, broker: '', topicPrefix: 'showmesh' } }

describe('SettingsHomeAssistant', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('saves the broker and prefix and shows the topics for the prefix', async () => {
    stubs.getEventsMQTTConfig.mockResolvedValue(blank)
    stubs.putEventsMQTTConfig.mockImplementation((p: { enabled: boolean; broker: string; topicPrefix: string }) => Promise.resolve({ ...blank, revision: 1, payload: p }))
    renderWith(['config:write'])

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Send events to the broker' }))
    fireEvent.change(screen.getByLabelText('Broker'), { target: { value: 'ha' } })
    fireEvent.change(screen.getByLabelText('Topic prefix'), { target: { value: 'barn' } })
    expect(screen.getByText('barn/events/#')).toBeTruthy()
    expect(screen.getByText('barn/night/state')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(stubs.putEventsMQTTConfig).toHaveBeenCalledWith({ enabled: true, broker: 'ha', topicPrefix: 'barn' }))
    expect(await screen.findByText('Saved as version 1.')).toBeTruthy()
  })

  it('will not save publishing with no broker', async () => {
    stubs.getEventsMQTTConfig.mockResolvedValue(blank)
    renderWith(['config:write'])

    fireEvent.click(await screen.findByRole('checkbox', { name: 'Send events to the broker' }))
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/no broker is named/)).toBeTruthy()
  })

  it('shows the stored values and locks editing without write access', async () => {
    stubs.getEventsMQTTConfig.mockResolvedValue({ ...blank, revision: 2, payload: { enabled: true, broker: 'ha', topicPrefix: 'showmesh' } })
    renderWith([])

    expect(((await screen.findByLabelText('Broker')) as HTMLInputElement).value).toBe('ha')
    expect((screen.getByLabelText('Broker') as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
