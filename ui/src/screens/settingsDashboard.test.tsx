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
  getPreviewGatewayConfigRevisions: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { SettingsDashboard } = await import('./SettingsDashboard')

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
      <SettingsDashboard />
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
  stubs.getPreviewGatewayConfigRevisions.mockResolvedValue({ serverTime: '2026-10-15T12:00:00Z', kind: 'preview.gateway', revisions: [] })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const SAVE = 'Save dashboard settings'

describe('Settings › Dashboard: preview gateway', () => {
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

  it('sends only the address when no new token was typed', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://10.0.0.10:8095' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewGatewayConfig).toHaveBeenCalledWith({ url: 'http://10.0.0.10:8095' }))
    expect(stubs.putPreviewCamera).not.toHaveBeenCalled()
  })

  it('sends the token when one was typed', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('A token is set. Set a new one'), { target: { value: 'tok' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewGatewayConfig).toHaveBeenCalledWith({ url: 'http://10.0.0.9:8095', token: 'tok' }))
  })

  it('refuses a malformed address before writing', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: '10.0.0.9' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    expect(await screen.findAllByText(/must start with http/)).not.toHaveLength(0)
    expect(stubs.putPreviewGatewayConfig).not.toHaveBeenCalled()
  })

  it('refuses a stale write', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://10.0.0.10:8095' } })
    stubs.getPreviewGatewayConfig.mockResolvedValue(gatewayResponse(5))
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(stubs.putPreviewGatewayConfig).not.toHaveBeenCalled()
  })

  it('disables saving with a reason without config:write', async () => {
    renderSettings([])
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://x:1' } })
    const save = screen.getByRole('button', { name: SAVE })
    expect(save).toBeDisabled()
    expect(save.getAttribute('title')).not.toBeNull()
  })
})

describe('Settings › Dashboard: cameras', () => {
  it('lists each camera with its stored address and never fills the address field', async () => {
    renderSettings()
    expect(await screen.findByText('Stored address rtsp://192.168.1.20:37379')).toBeInTheDocument()
    expect(screen.getByDisplayValue('Driveway')).toBeInTheDocument()
    expect(screen.getByLabelText('Set a new address')).toHaveValue('')
    expect(screen.getByLabelText('Camera id')).toBeDisabled()
  })

  it('says so when there are no cameras, without an empty panel', async () => {
    stubs.listConfigObjects.mockRejectedValue(notFound())
    const { container } = renderSettings()
    expect(await screen.findByText('No cameras')).toBeInTheDocument()
    expect(container.querySelector('#st-dash-cameras .sm-panel')).toBeNull()
  })

  it('adds a camera row and saves it with its id, name and address', async () => {
    stubs.getPreviewCamera.mockImplementation(async (id: string) => {
      if (id === 'porch') throw notFound()
      return cameraResponse()
    })
    renderSettings()
    await screen.findByText('Stored address rtsp://192.168.1.20:37379')
    fireEvent.click(screen.getByRole('button', { name: 'Add a camera' }))
    const ids = screen.getAllByLabelText('Camera id')
    fireEvent.change(ids[1]!, { target: { value: 'porch' } })
    fireEvent.change(screen.getAllByLabelText('Name')[1]!, { target: { value: 'Porch' } })
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'rtsp://10.0.0.7:554/s' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('porch', { name: 'Porch', enabled: true, url: 'rtsp://10.0.0.7:554/s' }))
    expect(stubs.putPreviewCamera).toHaveBeenCalledTimes(1)
  })

  it('refuses a new camera without an rtsp address', async () => {
    renderSettings()
    await screen.findByText('Stored address rtsp://192.168.1.20:37379')
    fireEvent.click(screen.getByRole('button', { name: 'Add a camera' }))
    fireEvent.change(screen.getAllByLabelText('Camera id')[1]!, { target: { value: 'porch' } })
    fireEvent.change(screen.getAllByLabelText('Name')[1]!, { target: { value: 'Porch' } })
    fireEvent.change(screen.getByLabelText('Address'), { target: { value: 'http://10.0.0.7' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    expect(await screen.findByText(/must start with rtsp/)).toBeInTheDocument()
    expect(stubs.putPreviewCamera).not.toHaveBeenCalled()
  })

  it('edits a camera without resending the address it never shows', async () => {
    renderSettings()
    fireEvent.change(await screen.findByDisplayValue('Driveway'), { target: { value: 'Front drive' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('driveway', { name: 'Front drive', enabled: true }))
    expect(stubs.putPreviewGatewayConfig).not.toHaveBeenCalled()
  })

  it('sends a new address only when one was typed', async () => {
    renderSettings()
    await screen.findByDisplayValue('Driveway')
    fireEvent.change(screen.getByLabelText('Set a new address'), { target: { value: 'rtsp://10.9.9.9:554/x' } })
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledWith('driveway', { name: 'Driveway', enabled: true, url: 'rtsp://10.9.9.9:554/x' }))
  })

  it('removes a camera only after the confirmation names the consequence', async () => {
    renderSettings()
    fireEvent.click(await screen.findByRole('button', { name: 'Remove Driveway' }))
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/stored addresses are deleted/)).toBeInTheDocument()
    expect(stubs.deletePreviewCamera).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove and save' }))
    await waitFor(() => expect(stubs.deletePreviewCamera).toHaveBeenCalledWith('driveway'))
  })
})

describe('Settings › Dashboard: layout and partial saves', () => {
  it('builds each camera as a panel with Remove in its field row and Add a camera after the list', async () => {
    renderSettings()
    const remove = await screen.findByRole('button', { name: 'Remove Driveway' })
    expect(remove.closest('.sm-inline-row')).toContainElement(screen.getByLabelText('Camera id'))
    const add = screen.getByRole('button', { name: 'Add a camera' })
    expect(remove.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByText(/turns NDI sources/)).toBeNull()
  })

  it('keeps the camera name as the heading while the row is unsaved', async () => {
    renderSettings()
    fireEvent.change(await screen.findByDisplayValue('Driveway'), { target: { value: 'Front drive' } })
    expect(screen.getByRole('heading', { level: 3, name: 'Driveway' })).toBeInTheDocument()
    expect(screen.getByText('Unsaved')).toBeInTheDocument()
  })

  it('labels the enabled switch by what it does', async () => {
    renderSettings()
    expect(await screen.findByLabelText('Stream this camera')).toBeChecked()
    expect(screen.queryByLabelText('Show on the Dashboard')).toBeNull()
  })

  it('shows the gateway history', async () => {
    renderSettings()
    await screen.findByDisplayValue('Driveway')
    await waitFor(() => expect(stubs.getPreviewGatewayConfigRevisions).toHaveBeenCalled())
  })

  it('says which part saved when a later part fails, and a retry does not refuse the part that saved', async () => {
    renderSettings()
    fireEvent.change(await screen.findByLabelText('Gateway address'), { target: { value: 'http://10.0.0.10:8095' } })
    fireEvent.change(screen.getByDisplayValue('Driveway'), { target: { value: 'Front drive' } })
    stubs.putPreviewGatewayConfig.mockImplementation(async () => {
      stubs.getPreviewGatewayConfig.mockResolvedValue({ ...gatewayResponse(2), payload: { url: 'http://10.0.0.10:8095', tokenSet: true } })
      return { ...gatewayResponse(2), payload: { url: 'http://10.0.0.10:8095', tokenSet: true } }
    })
    stubs.putPreviewCamera.mockRejectedValueOnce(new ApiError('The coordinator could not store the camera. Try again.', 500))
    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    expect(await screen.findByText('Save failed')).toBeInTheDocument()
    expect(screen.getByText('Already saved: the gateway. The rest is still unsaved.')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: SAVE }))
    await waitFor(() => expect(stubs.putPreviewCamera).toHaveBeenCalledTimes(2))
    expect(stubs.putPreviewGatewayConfig).toHaveBeenCalledTimes(1)
    expect(screen.queryByText('Stale write')).toBeNull()
  })
})
