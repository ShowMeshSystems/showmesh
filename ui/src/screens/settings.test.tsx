import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Model, type SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getFPPEndpointsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putFPPEndpointsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFPPEndpointsConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeInstancesConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putResolumeInstancesConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeInstancesConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFPPMQTTConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putFPPMQTTConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getFPPMQTTConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAssetsSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putAssetsSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAssetsSettingsConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getRenderSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putRenderSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getRenderSettingsConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAudioSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putAudioSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAudioSettingsConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowModeConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowModeConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowModeConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAudioNode: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putAudioNode: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getAudioNodeConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getServiceDescriptor: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getCurrentNightSession: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getFPPEndpointsConfig: (...args: never[]) => stubs.getFPPEndpointsConfig(...args),
    putFPPEndpointsConfig: (...args: never[]) => stubs.putFPPEndpointsConfig(...args),
    getFPPEndpointsConfigRevisions: (...args: never[]) => stubs.getFPPEndpointsConfigRevisions(...args),
    getResolumeInstancesConfig: (...args: never[]) => stubs.getResolumeInstancesConfig(...args),
    putResolumeInstancesConfig: (...args: never[]) => stubs.putResolumeInstancesConfig(...args),
    getResolumeInstancesConfigRevisions: (...args: never[]) => stubs.getResolumeInstancesConfigRevisions(...args),
    getFPPMQTTConfig: (...args: never[]) => stubs.getFPPMQTTConfig(...args),
    putFPPMQTTConfig: (...args: never[]) => stubs.putFPPMQTTConfig(...args),
    getFPPMQTTConfigRevisions: (...args: never[]) => stubs.getFPPMQTTConfigRevisions(...args),
    getAssetsSettingsConfig: (...args: never[]) => stubs.getAssetsSettingsConfig(...args),
    putAssetsSettingsConfig: (...args: never[]) => stubs.putAssetsSettingsConfig(...args),
    getAssetsSettingsConfigRevisions: (...args: never[]) => stubs.getAssetsSettingsConfigRevisions(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getRenderSettingsConfig: (...args: never[]) => stubs.getRenderSettingsConfig(...args),
    putRenderSettingsConfig: (...args: never[]) => stubs.putRenderSettingsConfig(...args),
    getRenderSettingsConfigRevisions: (...args: never[]) => stubs.getRenderSettingsConfigRevisions(...args),
    getAudioSettingsConfig: (...args: never[]) => stubs.getAudioSettingsConfig(...args),
    putAudioSettingsConfig: (...args: never[]) => stubs.putAudioSettingsConfig(...args),
    getAudioSettingsConfigRevisions: (...args: never[]) => stubs.getAudioSettingsConfigRevisions(...args),
    getShowModeConfig: (...args: never[]) => stubs.getShowModeConfig(...args),
    putShowModeConfig: (...args: never[]) => stubs.putShowModeConfig(...args),
    getShowModeConfigRevisions: (...args: never[]) => stubs.getShowModeConfigRevisions(...args),
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    getAudioNode: (...args: never[]) => stubs.getAudioNode(...args),
    putAudioNode: (...args: never[]) => stubs.putAudioNode(...args),
    getAudioNodeConfigRevisions: (...args: never[]) => stubs.getAudioNodeConfigRevisions(...args),
    getServiceDescriptor: (...args: never[]) => stubs.getServiceDescriptor(...args),
    getCurrentNightSession: (...args: never[]) => stubs.getCurrentNightSession(...args),
  }
})

const { Settings } = await import('./Settings')
const { SettingsConnections } = await import('./SettingsConnections')
const { SettingsDelivery } = await import('./SettingsDelivery')
const { SettingsRecovery } = await import('./SettingsRecovery')
const { SettingsAudioDefaults } = await import('./SettingsAudioDefaults')
const { SettingsNodeRouting } = await import('./SettingsNodeRouting')
const { SettingsMode } = await import('./SettingsMode')
const { SettingsAppearance } = await import('./SettingsAppearance')

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function renderAt(path: string, model: Partial<Model> = {}) {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), session: signedIn(['config:write']), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/settings" element={<Settings />}>
            <Route path="connections" element={<SettingsConnections />} />
            <Route path="delivery" element={<SettingsDelivery />} />
            <Route path="recovery" element={<SettingsRecovery />} />
            <Route path="audio-defaults" element={<SettingsAudioDefaults />} />
            <Route path="node-routing" element={<SettingsNodeRouting />} />
            <Route path="mode" element={<SettingsMode />} />
            <Route path="appearance" element={<SettingsAppearance />} />
          </Route>
          <Route path="/access" element={<div>access page</div>} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function notConfigured(detail: string) {
  return Promise.reject(new ApiError(detail, 404, 'https://showmesh.dev/problems/resource-not-found'))
}

describe('Settings tab strip', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders seven tabs plus the Access link, which navigates out rather than rendering a panel', () => {
    stubs.getFPPEndpointsConfig = () => notConfigured('nothing has ever been configured')
    stubs.getResolumeInstancesConfig = () => notConfigured('nothing has ever been configured')
    stubs.getFPPMQTTConfig = () => notConfigured('nothing has ever been configured')
    renderAt('/settings/connections')

    const nav = screen.getByRole('navigation', { name: 'Settings tabs' })
    const tabs = within(nav).getAllByRole('link')
    expect(tabs).toHaveLength(8)
    expect(tabs.map((t) => t.textContent)).toEqual([
      'Connections',
      'Content delivery',
      'Render recovery',
      'Appearance',
      'Audio defaults',
      'Node routing',
      'Mode',
      'Access ↗',
    ])

    fireEvent.click(within(nav).getByRole('link', { name: /Access/ }))
    expect(screen.getByText('access page')).toBeInTheDocument()
  })

  it("renders the mock's h2 label on Appearance, a tab with no coordinator read", () => {
    renderAt('/settings/appearance')
    expect(screen.getByRole('heading', { level: 2, name: 'How this browser looks' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /save/i })).not.toBeInTheDocument()
  })
})

describe('Settings › Connections', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('marks the Test control inert, and renders passwordSet as a fact with no password value', async () => {
    stubs.getFPPEndpointsConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'fpp.endpoints',
        revision: 3,
        payload: { endpoints: [{ id: 'barn-player', url: '10.20.0.14' }] },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
        restartRequired: false,
        restartRequiredReason: '',
      })
    stubs.getResolumeInstancesConfig = () => notConfigured('nothing has ever been configured')
    stubs.getFPPMQTTConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'fpp.mqtt',
        revision: 1,
        payload: { brokerURL: 'mqtt://10.20.0.5:1883', username: '', topicPrefix: 'showmesh/fpp', hosts: {}, passwordSet: true },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
        restartRequired: false,
        restartRequiredReason: '',
      })

    renderAt('/settings/connections')

    await waitFor(() => expect(screen.getByDisplayValue('barn-player')).toBeInTheDocument())
    const testButton = screen.getAllByRole('button', { name: 'Test' })[0]
    expect(testButton).toBeDisabled()
    expect(screen.getByText(/A password is set\. Set a new one/)).toBeInTheDocument()
    expect(screen.queryByDisplayValue(/^\*+$/)).not.toBeInTheDocument()
  })
})

describe('Settings › Content delivery', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders no byte total for store size and says the coordinator does not report it', async () => {
    stubs.getAssetsSettingsConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'assets.settings',
        revision: 2,
        payload: { contentBaseUrl: '', maxUploadBytes: 500000000, syncIntervalSeconds: 900, inventoryIntervalSeconds: 3600 },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
      })
    stubs.listAssets = () => Promise.resolve({ assets: [{ id: 'a1' }, { id: 'a2' }] })

    renderAt('/settings/delivery')

    await waitFor(() => expect(screen.getByText(/2 assets across all shows\./)).toBeInTheDocument())
    expect(screen.getByText(/does not report store size or capacity/)).toBeInTheDocument()
    expect(screen.queryByText(/GB/)).not.toBeInTheDocument()
  })
})

describe('Settings › Render recovery', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('says nothing has been written rather than showing a bare zero for revision 0/source default', async () => {
    stubs.getRenderSettingsConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'render.settings',
        revision: 0,
        payload: { idleOutput: 'black', restartPolicy: { initialDelaySeconds: 2, maxDelaySeconds: 30, maxConsecutiveFastFailures: 5 } },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: null,
        createdByPrincipalName: null,
        source: 'default',
        idleOutputEffectiveNote: 'idleOutput takes effect on next apply',
      })

    renderAt('/settings/recovery')

    await waitFor(() => expect(screen.getByText(/Nothing has been written for render recovery yet/)).toBeInTheDocument())
  })
})

describe('Settings › Audio defaults', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders the one-member fade curve as a stated fact, not a select', async () => {
    stubs.getAudioSettingsConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'audio.settings',
        revision: 1,
        payload: {
          driftIgnoreThresholdMs: 40,
          defaultFadeCurve: 'linear',
          defaultFadeDurationMs: 1500,
          defaultMaxBackgroundGainDb: -6,
          duckTargetGainDb: -18,
          ltcFrameRate: '30',
          ltcDefaultStartOffset: '00:00:00:00',
        },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
      })

    renderAt('/settings/audio-defaults')

    await waitFor(() => expect(screen.getByText('linear')).toBeInTheDocument())
    expect(screen.queryByRole('combobox', { name: /fade curve/i })).not.toBeInTheDocument()
  })

  function audioSettingsConfig(overrides: Partial<{ duckFadeDurationMs: number; duckRestoreFadeDurationMs: number }> = {}) {
    return {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'audio.settings',
      revision: 1,
      payload: {
        driftIgnoreThresholdMs: 40,
        defaultFadeCurve: 'linear',
        defaultFadeDurationMs: 1500,
        defaultMaxBackgroundGainDb: -6,
        duckTargetGainDb: -18,
        duckFadeDurationMs: overrides.duckFadeDurationMs ?? 250,
        duckRestoreFadeDurationMs: overrides.duckRestoreFadeDurationMs ?? 1200,
        ltcFrameRate: '30',
        ltcDefaultStartOffset: '00:00:00:00',
      },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    }
  }

  it('renders duck fade and restore fade as labelled controls, validates them as positive integers, saves them, and discards back to the loaded value', async () => {
    stubs.getAudioSettingsConfig = () => Promise.resolve(audioSettingsConfig())
    const put = vi.fn(() => Promise.resolve(audioSettingsConfig({ duckFadeDurationMs: 400 })))
    stubs.putAudioSettingsConfig = put

    renderAt('/settings/audio-defaults')

    const duckFadeInput = (await screen.findByLabelText('Duck fade duration (ms)')) as HTMLInputElement
    const duckRestoreInput = screen.getByLabelText('Duck restore fade duration (ms)') as HTMLInputElement
    expect(duckFadeInput.value).toBe('250')
    expect(duckRestoreInput.value).toBe('1200')

    fireEvent.change(duckFadeInput, { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    expect(await screen.findByText(/Duck fade duration must be a whole number of milliseconds, greater than zero\./)).toBeInTheDocument()
    expect(put).not.toHaveBeenCalled()

    fireEvent.change(duckFadeInput, { target: { value: '400' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save defaults' }))
    await waitFor(() => expect(put).toHaveBeenCalledWith(expect.objectContaining({ duckFadeDurationMs: 400, duckRestoreFadeDurationMs: 1200 })))

    fireEvent.change(duckFadeInput, { target: { value: '999' } })
    expect(screen.getByRole('button', { name: 'Discard changes' })).not.toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Discard changes' }))
    expect(duckFadeInput.value).toBe('400')
  })
})

describe('Settings › Node routing', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function nodeConfig(overrides: Partial<{ programRoute: string; programChannels: number[]; ltcRoute: string; ltcChannel: number }> = {}) {
    return {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'audio.node',
      id: 'audio-node-01',
      revision: 4,
      payload: {
        programRoute: overrides.programRoute ?? 'hw:CARD=USB,DEV=0',
        programChannels: overrides.programChannels ?? [1, 2],
        clockDomain: 'usb-audio-0',
        clockDomainProvenance: 'single interface',
        ...(overrides.ltcRoute !== undefined ? { ltcRoute: overrides.ltcRoute, ltcChannel: overrides.ltcChannel } : {}),
      },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    }
  }

  it('renders no empty select for a node with no advertised route', async () => {
    stubs.listConfigObjects = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.node', objects: [{ id: 'audio-node-01', label: 'hw:CARD=USB,DEV=0', show: '', currentRevision: 4, updatedAt: '2026-08-30T18:00:00Z' }] })
    stubs.getAudioNode = () => Promise.resolve(nodeConfig())
    stubs.getAudioNodeConfigRevisions = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.node', revisions: [] })

    renderAt('/settings/node-routing', {
      nodes: [
        {
          nodeId: 'audio-node-01',
          label: null,
          platform: null,
          agentVersion: null,
          bootId: null,
          startedAt: null,
          firstSeenAt: '2026-08-30T00:00:00Z',
          updatedAt: '2026-08-30T00:00:00Z',
          capabilities: [],
          controlPlane: { state: 'online', reason: '' },
          evidence: { hello: { signal: 'node.hello', value: null, unit: null, state: 'current', reason: null, observedAt: null, collectedAt: null, source: 'mqtt', quality: 'direct', validForSeconds: null }, lastWill: { signal: '', value: null, unit: null, state: 'not_collected', reason: null, observedAt: null, collectedAt: null, source: '', quality: 'direct', validForSeconds: null }, heartbeat: { signal: '', value: null, unit: null, state: 'not_collected', reason: null, observedAt: null, collectedAt: null, source: '', quality: 'direct', validForSeconds: null } },
          declaration: {} as never,
          render: [],
          audio: [],
          fppConnect: [],
        },
      ],
    } as unknown as Partial<Model>)

    await waitFor(() => expect(screen.getByText(/has advertised no routes/)).toBeInTheDocument())
    expect(screen.queryByRole('combobox', { name: 'Route' })).not.toBeInTheDocument()
  })

  it('withholds save on a channel duplicate and a channel overlap, keeps LTC route mirroring program route so a mismatch cannot be typed, and marks the output-group picker while keeping the manual field live', async () => {
    stubs.listConfigObjects = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.node', objects: [{ id: 'audio-node-01', label: 'hw:CARD=USB,DEV=0', show: '', currentRevision: 4, updatedAt: '2026-08-30T18:00:00Z' }] })
    stubs.getAudioNode = () => Promise.resolve(nodeConfig({ ltcRoute: 'hw:CARD=USB,DEV=0', ltcChannel: 3 }))
    stubs.getAudioNodeConfigRevisions = () => Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.node', revisions: [] })

    renderAt('/settings/node-routing', { nodes: [] })

    await waitFor(() => expect(screen.getByText(/Will be accepted/)).toBeInTheDocument())

    const channelsInput = screen.getByLabelText('Program channels')
    fireEvent.change(channelsInput, { target: { value: '1, 1' } })
    expect(await screen.findByText(/appears more than once in program channels/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save routing' })).toBeDisabled()

    fireEvent.change(channelsInput, { target: { value: '1, 3' } })
    expect(await screen.findByText(/already claimed by program channels/)).toBeInTheDocument()

    fireEvent.change(channelsInput, { target: { value: '1, 2' } })
    await waitFor(() => expect(screen.getByText(/Will be accepted/)).toBeInTheDocument())

    // LTC route is read-only, mirroring program route (audionode.go
    // refuses them differing). No independent input exists to mistype,
    // so a mismatch cannot be produced from this UI.
    const routeText = screen.getByLabelText('Route') as HTMLInputElement
    fireEvent.change(routeText, { target: { value: 'hw:CARD=PCH,DEV=0' } })
    await waitFor(() => expect(screen.getByText(/Will be accepted/)).toBeInTheDocument())
    expect(screen.getByText('hw:CARD=PCH,DEV=0', { selector: 'p' })).toBeInTheDocument()

    expect(screen.getByText('Output groups does nothing yet.')).toBeInTheDocument()
    expect(screen.getByLabelText('Program channels')).not.toBeDisabled()
  })
})

describe('Settings › Mode', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('shows the in-progress warning only when the session reports a live cycle, and disables Apply when the mode is already active', async () => {
    stubs.getShowModeConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show.mode',
        revision: 2,
        payload: { mode: 'show' },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
        resolumeWebSocketEffect: 'closed in show mode',
      })

    renderAt('/settings/mode', { nightSession: { state: 'live', cycle: 3 } as unknown as Model['nightSession'] })

    await waitFor(() => expect(screen.getByText('Cycle 3 is live.')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /Apply mode/ })).toBeDisabled()
  })

  it('seeds the night session itself, because the model only ever gets one from a stream frame', async () => {
    stubs.getShowModeConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show.mode',
        revision: 6,
        payload: { mode: 'show' },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
        resolumeWebSocketEffect: 'The Resolume WebSocket stays connected in show mode.',
      })
    stubs.getCurrentNightSession = () => Promise.resolve({ session: { state: 'live', cycle: 3 } })

    renderAt('/settings/mode', { nightSession: null })

    await waitFor(() => expect(screen.getByText('Cycle 3 is live.')).toBeInTheDocument())
  })

  it('renders no in-progress warning when the session reports no live cycle', async () => {
    stubs.getCurrentNightSession = () => new Promise(() => {})
    stubs.getShowModeConfig = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'show.mode',
        revision: 2,
        payload: { mode: 'program' },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
        resolumeWebSocketEffect: 'held open in program mode',
      })

    renderAt('/settings/mode', { nightSession: null })

    await waitFor(() => expect(screen.getByText('held open in program mode')).toBeInTheDocument())
    expect(screen.queryByText(/is live\./)).not.toBeInTheDocument()
  })

  it('refuses a stale save and writes nothing', async () => {
    let putCalled = false
    const loaded = {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'show.mode',
      revision: 2,
      payload: { mode: 'show' },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
      resolumeWebSocketEffect: 'closed in show mode',
    }
    const current = { ...loaded, revision: 3, createdByPrincipalName: 'someone-else', updatedAt: '2026-08-30T19:00:00Z' }
    let reads = 0
    stubs.getShowModeConfig = () => {
      reads += 1
      return Promise.resolve(reads === 1 ? loaded : current)
    }
    stubs.putShowModeConfig = () => {
      putCalled = true
      return Promise.resolve(current)
    }

    renderAt('/settings/mode', { nightSession: null })

    await waitFor(() => expect(screen.getByRole('heading', { level: 2, name: 'What this installation is for right now' })).toBeInTheDocument())
    const programOption = screen.getByRole('radio', { name: /Program mode/ })
    fireEvent.click(programOption)
    fireEvent.click(screen.getByRole('button', { name: /Apply mode/ }))

    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(putCalled).toBe(false)
  })
})

describe('RevisionHistory, shared across editors', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function assetsSettingsConfig() {
    return {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'assets.settings',
      revision: 2,
      payload: { contentBaseUrl: '', maxUploadBytes: 500000000, syncIntervalSeconds: 900, inventoryIntervalSeconds: 3600 },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    }
  }

  function audioSettingsConfig() {
    return {
      serverTime: '2026-08-30T21:00:00Z',
      kind: 'audio.settings',
      revision: 1,
      payload: {
        driftIgnoreThresholdMs: 40,
        defaultFadeCurve: 'linear',
        defaultFadeDurationMs: 1500,
        defaultMaxBackgroundGainDb: -6,
        duckTargetGainDb: -18,
        duckFadeDurationMs: 250,
        duckRestoreFadeDurationMs: 1200,
        ltcFrameRate: '30',
        ltcDefaultStartOffset: '00:00:00:00',
      },
      updatedAt: '2026-08-30T18:00:00Z',
      createdByPrincipalId: 'p1',
      createdByPrincipalName: 'erbartos',
      source: 'api',
    }
  }

  it('renders the compact active-revision summary, not a list heading, on Content delivery', async () => {
    stubs.getAssetsSettingsConfig = () => Promise.resolve(assetsSettingsConfig())
    stubs.listAssets = () => Promise.resolve({ assets: [] })
    stubs.getAssetsSettingsConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'assets.settings',
        revisions: [
          { revision: 2, createdAt: '2026-08-30T18:00:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: 'raised max upload size', active: true },
          { revision: 1, createdAt: '2026-08-20T14:00:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: false },
        ],
      })

    renderAt('/settings/delivery')

    const summary = await screen.findByText(/Active revision/, { selector: 'p' })
    expect(summary).toBeInTheDocument()
    expect(within(summary).getByText('2')).toBeInTheDocument()
    expect(within(summary).getByText(/erbartos/)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
    expect(screen.queryByText('Active · 2')).not.toBeInTheDocument()
    expect(screen.queryByText(/raised max upload size/)).not.toBeInTheDocument()
  })

  it('renders an empty history as a settled fact, not a read failure, on Audio defaults', async () => {
    stubs.getAudioSettingsConfig = () => Promise.resolve(audioSettingsConfig())
    stubs.getAudioSettingsConfigRevisions = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.settings', revisions: [] })

    renderAt('/settings/audio-defaults')

    await waitFor(() => expect(screen.getByText('linear')).toBeInTheDocument())
    expect(await screen.findByText('No prior revision recorded.')).toBeInTheDocument()
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('does not claim a read failure while the revisions fetch is still pending, on Content delivery', async () => {
    stubs.getAssetsSettingsConfig = () => Promise.resolve(assetsSettingsConfig())
    stubs.listAssets = () => Promise.resolve({ assets: [] })
    stubs.getAssetsSettingsConfigRevisions = () => new Promise(() => {})

    renderAt('/settings/delivery')

    await waitFor(() => expect(screen.getByDisplayValue('900')).toBeInTheDocument())
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('leaves the editor around it intact and reports the failure honestly when the revisions read is rejected, on Content delivery', async () => {
    stubs.getAssetsSettingsConfig = () => Promise.resolve(assetsSettingsConfig())
    stubs.listAssets = () => Promise.resolve({ assets: [] })
    stubs.getAssetsSettingsConfigRevisions = () => Promise.reject(new Error('network error'))

    renderAt('/settings/delivery')

    await waitFor(() => expect(screen.getByDisplayValue('900')).toBeInTheDocument())
    expect(await screen.findByText('Revision history could not be read just now.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save delivery' })).toBeInTheDocument()
  })

  it('is the same element on two different screens', async () => {
    stubs.getAssetsSettingsConfig = () => Promise.resolve(assetsSettingsConfig())
    stubs.listAssets = () => Promise.resolve({ assets: [] })
    stubs.getAssetsSettingsConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'assets.settings',
        revisions: [{ revision: 2, createdAt: '2026-08-30T18:00:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true }],
      })

    const delivery = renderAt('/settings/delivery')
    expect(await screen.findByText(/Active revision/, { selector: 'p' })).toBeInTheDocument()
    delivery.unmount()
    cleanup()

    stubs.getAudioSettingsConfig = () => Promise.resolve(audioSettingsConfig())
    stubs.getAudioSettingsConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'audio.settings',
        revisions: [{ revision: 1, createdAt: '2026-08-20T14:00:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true }],
      })

    renderAt('/settings/audio-defaults')
    expect(await screen.findByText(/Active revision/, { selector: 'p' })).toBeInTheDocument()
  })

  it('keeps the expandable revision list on Node routing, matching its own mock', async () => {
    stubs.listConfigObjects = () =>
      Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind: 'audio.node', objects: [{ id: 'audio-node-01', label: 'hw:CARD=USB,DEV=0', show: '', currentRevision: 4, updatedAt: '2026-08-30T18:00:00Z' }] })
    stubs.getAudioNode = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'audio.node',
        id: 'audio-node-01',
        revision: 4,
        payload: { programRoute: 'hw:CARD=USB,DEV=0', programChannels: [1, 2], clockDomain: 'usb-audio-0', clockDomainProvenance: 'single interface' },
        updatedAt: '2026-08-30T18:00:00Z',
        createdByPrincipalId: 'p1',
        createdByPrincipalName: 'erbartos',
        source: 'api',
      })
    stubs.getAudioNodeConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'audio.node',
        revisions: [
          { revision: 4, createdAt: '2026-08-22T19:04:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: 'LTC moved to channel 3', active: true },
          { revision: 3, createdAt: '2026-08-18T14:51:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: false },
        ],
      })

    renderAt('/settings/node-routing', { nodes: [] })

    expect(await screen.findByRole('heading', { level: 2, name: 'Revisions' })).toBeInTheDocument()
    expect(await screen.findByText('Active · 4')).toBeInTheDocument()
    expect(screen.getByText(/LTC moved to channel 3/)).toBeInTheDocument()
  })
})
