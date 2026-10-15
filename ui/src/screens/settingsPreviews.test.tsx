import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getPreviewGatewayConfig: vi.fn(),
  putPreviewGatewayConfig: vi.fn(),
  listConfigObjects: vi.fn(),
  getPreviewCamera: vi.fn(),
  putPreviewCamera: vi.fn(),
  deletePreviewCamera: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { SettingsPreviews } = await import('./SettingsPreviews')

const session = (scopes: string[]) =>
  ({
    serverTime: '2026-10-15T12:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'tablet', createdAt: '2026-10-15T12:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  }) as unknown as SessionResponse

const envelope = <K extends string, P>(kind: K, payload: P, revision = 1) => ({
  serverTime: '2026-10-15T12:00:00Z',
  kind,
  revision,
  payload,
  updatedAt: '2026-10-15T12:00:00Z',
  createdByPrincipalId: null,
  createdByPrincipalName: null,
  source: 'api' as const,
})

const gatewayResponse = (revision = 1) => envelope('preview.gateway', { url: 'http://10.0.0.9:8095', tokenSet: true }, revision)
const cameraResponse = (revision = 1) => ({
  ...envelope('preview.camera', { name: 'Driveway', enabled: true, urlSet: true, urlDisplay: 'rtsp://192.168.1.20:37379' }, revision),
  id: 'driveway',
})

function renderSettings(scopes = ['config:write'], model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: session(scopes), ...model }}>
      <SettingsPreviews />
    </ModelContext.Provider>,
  )
}

const notFound = () => new ApiError('not found', 404)

beforeEach(() => {
  stubs.getPreviewGatewayConfig.mockResolvedValue(gatewayResponse())
  stubs.putPreviewGatewayConfig.mockResolvedValue(gatewayResponse(2))
  stubs.listConfigObjects.mockResolvedValue({
    serverTime: '2026-10-15T12:00:00Z',
    kind: 'preview.camera',
    objects: [{ id: 'driveway', label: 'Driveway', show: '', currentRevision: 1, updatedAt: '2026-10-15T12:00:00Z' }],
  })
  stubs.getPreviewCamera.mockResolvedValue(cameraResponse())
  stubs.putPreviewCamera.mockResolvedValue(cameraResponse(2))
  stubs.deletePreviewCamera.mockResolvedValue(undefined)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('Settings › preview gateway', () => {
  it('shows the saved address and reports the token as set without showing it', async () => {
    renderSettings()
    expect(await screen.findByDisplayValue('http://10.0.0.9:8095')).toBeInTheDocument()
    expect(screen.getByLabelText('A token is set. Set a new one')).toHaveValue('')
  })

  it('says previews are off when nothing is configured', async () => {
    stubs.getPreviewGatewayConfig.mockRejectedValue(notFound())
    renderSettings()
    expect(await screen.findByText(/shows no previews/)).toBeInTheDocument()
    expect(screen.getByLabelText('No token is set. Set one')).toBeInTheDocument()
  })

  it('sends only the address when no new token was typed, then the token when one was', async () => {
    renderSettings()
    const address = await screen.findByLabelText('Gateway address')
    fireEvent.change(address, { target: { value: 'http://10.0.0.10:8095' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save preview gateway' }))
    await waitFor(() => expect(stubs.putPreviewGatewayConfig).toHaveBeenCalledWith({ url: 'http://10.0.0.10:8095' }))
    fireEvent.change(await screen.findByLabelText('A token is set. Set a new one'), { target: { value: 'tok' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save preview gateway' }))
    await waitFor(() => expect(stubs.putPreviewGatewayConfig).toHaveBeenLastCalledWith({ url: 'http://10.0.0.9:8095', token: 'tok' }))
  })

  it('refuses a malformed address before writing', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: '10.0.0.9' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save preview gateway' }))
    expect(await screen.findAllByText(/must start with http/)).not.toHaveLength(0)
    expect(stubs.putPreviewGatewayConfig).not.toHaveBeenCalled()
  })

  it('refuses a stale write', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://10.0.0.10:8095' } })
    stubs.getPreviewGatewayConfig.mockResolvedValue(gatewayResponse(5))
    fireEvent.click(screen.getByRole('button', { name: 'Save preview gateway' }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(stubs.putPreviewGatewayConfig).not.toHaveBeenCalled()
  })

  it('disables saving with a reason without config:write', async () => {
    renderSettings([])
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://x:1' } })
    const save = screen.getByRole('button', { name: 'Save preview gateway' })
    expect(save).toBeDisabled()
    expect(save.getAttribute('title')).not.toBeNull()
  })
})

describe('Settings › preview cameras', () => {
  it('lists each camera with its display address and never an address field value', async () => {
    renderSettings()
    expect(await screen.findByText('rtsp://192.168.1.20:37379')).toBeInTheDocument()
    expect(screen.getByDisplayValue('Driveway')).toBeInTheDocument()
    expect(screen.getByLabelText('Set a new address')).toHaveValue('')
  })

  it('says so when there are no cameras', async () => {
    stubs.listConfigObjects.mockRejectedValue(notFound())
    renderSettings()
    expect(await screen.findByText('No cameras')).toBeInTheDocument()
  })

  it('adds a camera with its id, name and address', async () => {
    stubs.getPreviewCamera.mockImplementation(async (id: string) => {
      if (id === 'porch') throw notFound()
      return cameraResponse()
    })
    renderSettings()
    await screen.findByText('rtsp://192.168.1.20:37379')
    fireEvent.change(screen.getByLabelText('Camera id'), { target: { value: 'porch' } })
    fireEvent.change(screen.getByLabelText('Camera name'), { target: { value: 'Porch' } })
    fireEvent.change(screen.getByLabelText('Camera address'), { target: { value: 'rtsp://10.0.0.7:554/s' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add camera' }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('porch', { name: 'Porch', enabled: true, url: 'rtsp://10.0.0.7:554/s' }))
  })

  it('refuses a new camera without an rtsp address', async () => {
    renderSettings()
    await screen.findByText('rtsp://192.168.1.20:37379')
    fireEvent.change(screen.getByLabelText('Camera id'), { target: { value: 'porch' } })
    fireEvent.change(screen.getByLabelText('Camera name'), { target: { value: 'Porch' } })
    fireEvent.change(screen.getByLabelText('Camera address'), { target: { value: 'http://10.0.0.7' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add camera' }))
    expect(await screen.findByText(/must start with rtsp/)).toBeInTheDocument()
    expect(stubs.putPreviewCamera).not.toHaveBeenCalled()
  })

  it('edits a camera without resending the address it never shows', async () => {
    renderSettings()
    const name = await screen.findByDisplayValue('Driveway')
    fireEvent.change(name, { target: { value: 'Front drive' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Driveway' }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('driveway', { name: 'Front drive', enabled: true }))
  })

  it('sends a new address only when one was typed', async () => {
    renderSettings()
    await screen.findByDisplayValue('Driveway')
    fireEvent.change(screen.getByLabelText('Set a new address'), { target: { value: 'rtsp://10.9.9.9:554/x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Driveway' }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('driveway', { name: 'Driveway', enabled: true, url: 'rtsp://10.9.9.9:554/x' }))
  })

  it('removes a camera only after the confirmation names the consequence', async () => {
    renderSettings()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove Driveway' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/stored address is deleted/)).toBeInTheDocument()
    expect(stubs.deletePreviewCamera).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove camera' }))
    await waitFor(() => expect(stubs.deletePreviewCamera).toHaveBeenCalledWith('driveway'))
  })
})
