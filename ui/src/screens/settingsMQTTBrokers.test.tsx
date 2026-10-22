import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { IntegrationBroker, IntegrationBrokersListResponse, Model, SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listIntegrationBrokers: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putIntegrationBroker: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  deleteIntegrationBroker: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listIntegrationBrokers: (...args: never[]) => stubs.listIntegrationBrokers(...args),
    putIntegrationBroker: (...args: never[]) => stubs.putIntegrationBroker(...args),
    deleteIntegrationBroker: (...args: never[]) => stubs.deleteIntegrationBroker(...args),
  }
})

const { SettingsMQTTBrokers } = await import('./SettingsMQTTBrokers')

function broker(overrides: Partial<IntegrationBroker> = {}): IntegrationBroker {
  return {
    id: 'home-assistant', url: 'tcp://10.0.0.5:1883', username: 'showmesh', passwordSet: true, builtIn: false,
    topicPrefix: '', revision: 1, updatedAt: '2026-10-22T18:00:00Z', createdByPrincipalName: 'erbartos', source: 'api',
    ...overrides,
  }
}

const builtIn = broker({ id: 'built-in', url: 'tcp://mosquitto:1883', username: 'integration', builtIn: true, topicPrefix: 'showmesh-actions/', revision: 0, updatedAt: null, createdByPrincipalName: null, source: 'built-in' })

function listResponse(brokers: IntegrationBroker[], editsBlocked = ''): IntegrationBrokersListResponse {
  return { serverTime: '2026-10-22T18:00:00Z', brokers, editsBlocked }
}

function renderScreen(scopes: string[] = ['config:write']) {
  const session = {
    serverTime: '2026-10-22T18:00:00Z', authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' },
    credentialForm: 'session', scopes, scopesState: 'current', bootstrapRequired: false,
  } as unknown as SessionResponse
  const model: Model = { ...initialModel(), session, serverTime: '2026-10-22T18:00:00Z', serverTimeReceivedAt: Date.now() }
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/settings/mqtt-brokers']}>
        <Routes>
          <Route path="/settings/mqtt-brokers" element={<SettingsMQTTBrokers />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('SettingsMQTTBrokers', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('shows the built-in broker read-only, with its topic rule', async () => {
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn, broker()]))
    renderScreen()
    expect(await screen.findByText('Built-in broker')).toBeTruthy()
    expect(screen.getByText('Topics must start with showmesh-actions/')).toBeTruthy()
    expect(screen.getByText('tcp://mosquitto:1883')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: 'Remove' })).toHaveLength(1)
  })

  it('adds a broker and sends only the fields it has', async () => {
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn]))
    const put = vi.fn(() => Promise.resolve({}))
    stubs.putIntegrationBroker = put
    renderScreen()
    fireEvent.click(await screen.findByRole('button', { name: 'Add a broker' }))
    fireEvent.change(screen.getByLabelText('Id'), { target: { value: 'ha' } })
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'tcp://10.0.0.9:1883' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add broker' }))
    await waitFor(() => expect(put).toHaveBeenCalledWith('ha', { url: 'tcp://10.0.0.9:1883', username: '', password: 'secret' }))
  })

  it('refuses the built-in id and an address with a login before sending', async () => {
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn]))
    renderScreen()
    fireEvent.click(await screen.findByRole('button', { name: 'Add a broker' }))
    fireEvent.change(screen.getByLabelText('Id'), { target: { value: 'built-in' } })
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'tcp://10.0.0.9:1883' } })
    expect((screen.getByRole('button', { name: 'Add broker' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('Id'), { target: { value: 'ha' } })
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'tcp://u:p@10.0.0.9:1883' } })
    expect((screen.getByRole('button', { name: 'Add broker' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('changing the username keeps the saved password', async () => {
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn, broker()]))
    const put = vi.fn(() => Promise.resolve({}))
    stubs.putIntegrationBroker = put
    renderScreen()
    fireEvent.change(await screen.findByLabelText('Username'), { target: { value: 'other' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(put).toHaveBeenCalledWith('home-assistant', { username: 'other' }))
  })

  it('says why editing is off while the coordinator variable is set', async () => {
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn, broker()], 'Brokers are set by SHOWMESH_INTEGRATION_BROKERS on the coordinator. Remove it and restart the coordinator to edit them here.'))
    renderScreen()
    expect(await screen.findByText(/SHOWMESH_INTEGRATION_BROKERS/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Add a broker' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows the server refusal when a broker in use is removed', async () => {
    const { ApiError } = await import('../api')
    stubs.listIntegrationBrokers = () => Promise.resolve(listResponse([builtIn, broker()]))
    stubs.deleteIntegrationBroker = () => Promise.reject(new ApiError('Not deleted: actions projectors-on still use this broker. Move them to another broker first.', 409))
    renderScreen()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remove broker' }))
    expect(await screen.findByText(/projectors-on still use this broker/)).toBeTruthy()
  })
})
