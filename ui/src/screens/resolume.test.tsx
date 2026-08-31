import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type {
  Evidence,
  Model,
  ResolumeCompositionResponse,
  ResolumeInstance,
  ResolumeInstancesConfigResponse,
  ResolumeRecoveryConfigResponse,
  ResolumeRecoveryResponse,
  SessionResponse,
} from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getResolumeComposition: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeInstancesConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeRecovery: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeRecoveryConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putResolumeRecoveryConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getResolumeRecoveryConfigRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  uploadResolumeComposition: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getResolumeComposition: (...args: never[]) => stubs.getResolumeComposition(...args),
    getResolumeInstancesConfig: (...args: never[]) => stubs.getResolumeInstancesConfig(...args),
    getResolumeRecovery: (...args: never[]) => stubs.getResolumeRecovery(...args),
    getResolumeRecoveryConfig: (...args: never[]) => stubs.getResolumeRecoveryConfig(...args),
    putResolumeRecoveryConfig: (...args: never[]) => stubs.putResolumeRecoveryConfig(...args),
    getResolumeRecoveryConfigRevisions: (...args: never[]) => stubs.getResolumeRecoveryConfigRevisions(...args),
    uploadResolumeComposition: (...args: never[]) => stubs.uploadResolumeComposition(...args),
  }
})

const { ResolumeConfig } = await import('./ResolumeConfig')

function evidence(overrides: Partial<Evidence> = {}): Evidence {
  return {
    signal: 'resolume.reachable',
    value: true,
    unit: null,
    state: 'current',
    reason: null,
    observedAt: '2026-08-30T20:59:58Z',
    collectedAt: '2026-08-30T20:59:58Z',
    source: 'resolume-rest',
    quality: 'direct',
    validForSeconds: null,
    ...overrides,
  } as unknown as Evidence
}

function instance(overrides: Partial<ResolumeInstance> = {}): ResolumeInstance {
  return {
    instanceId: 'arena-main',
    health: 'healthy',
    observations: [evidence()],
    composition: { name: 'WinterRidge Show' },
    ...overrides,
  } as unknown as ResolumeInstance
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function composition(overrides: Partial<ResolumeCompositionResponse> = {}): ResolumeCompositionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    revision: 4,
    activatedAt: '2026-08-22T11:04:00Z',
    composition: {
      name: 'WinterRidge Show',
      sourceFilename: 'WinterRidge.avc',
      contentHash: 'sha256:1f04b972abcd',
      sizeBytes: 4_200_000,
      writtenBy: { product: 'Arena', major: 7, minor: 23, micro: 2, revision: 4 },
      canvas: { width: 1920, height: 1080 },
      decks: [{ id: 'deck-1', name: 'Winter', nameGenerated: false, closed: false, clipCount: 30 }],
      layerCount: 6,
      layerGroupCount: 1,
      columnCount: 14,
      clipCount: 70,
      persistentClipCount: 2,
    },
    decks: [{ id: 'deck-1', name: 'Winter', nameGenerated: false, closed: false, clipCount: 30 }],
    layerGroups: [{ id: 'group-1', index: 0 }],
    layers: [
      { id: 'layer-ambience', index: 0, name: 'Ambience', nameGenerated: false },
      { id: 'layer-base', index: 1, name: 'Base', nameGenerated: false },
    ],
    columns: [
      { id: 'col-3', deckId: 'deck-1', index: 3, name: 'Column 3', nameGenerated: true },
      { id: 'col-9', deckId: 'deck-1', index: 9, name: 'Column 9', nameGenerated: true },
    ],
    clips: [
      { id: 'clip-1f04', deckId: 'deck-1', layerIndex: 0, columnIndex: 3, name: 'Snow', nameGenerated: false, ambiguous: true },
      { id: 'clip-9b72', deckId: 'deck-1', layerIndex: 0, columnIndex: 9, name: 'Snow', nameGenerated: false, ambiguous: true },
      { id: 'clip-clean', deckId: 'deck-1', layerIndex: 1, columnIndex: 1, name: 'Countdown 5', nameGenerated: false, ambiguous: false },
    ],
    persistentClips: [],
    ...overrides,
  } as unknown as ResolumeCompositionResponse
}

function recovery(overrides: Partial<ResolumeRecoveryResponse> = {}): ResolumeRecoveryResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    resolumeConfigured: true,
    autoRestoreEnabled: true,
    autoRestoreConfigured: true,
    settleDelaySeconds: 5,
    record: [
      { layer: 'Base', layerNameGenerated: false, state: 'clip', clip: 'Winter Base Loop', clipNameGenerated: false, deck: 'Winter', establishedAt: '2026-08-30T21:02:22Z', source: 'action' },
      { layer: 'Overlay', layerNameGenerated: false, state: 'dark' },
      { layer: 'Text', layerNameGenerated: false, state: 'unknown', reason: 'never observed' },
    ],
    lastRestore: {
      startedAt: '2026-08-30T20:41:33Z',
      finishedAt: '2026-08-30T20:41:34Z',
      trigger: 'automatic',
      outcome: 'partial',
      principal: 'erbartos',
      layers: [
        { layer: 'Base', layerNameGenerated: false, result: 'restored', clip: 'Winter Base Loop', actionOutcome: 'confirmed' },
        { layer: 'Ambience', layerNameGenerated: false, result: 'skipped', clip: 'Snow', reason: 'its name is ambiguous' },
        { layer: 'Text', layerNameGenerated: false, result: 'restored', clip: 'Countdown 5', actionOutcome: 'unconfirmable' },
        { layer: 'Overlay', layerNameGenerated: false, result: 'restored', clip: 'Sponsor Card', actionOutcome: 'confirmed' },
      ],
      omittedLayerCount: 0,
    },
    ...overrides,
  } as unknown as ResolumeRecoveryResponse
}

function recoveryConfig(overrides: Partial<ResolumeRecoveryConfigResponse> = {}): ResolumeRecoveryConfigResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'resolume.recovery',
    revision: 3,
    payload: { autoRestoreEnabled: true },
    updatedAt: '2026-08-22T11:09:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api',
    ...overrides,
  } as unknown as ResolumeRecoveryConfigResponse
}

function instancesConfig(): ResolumeInstancesConfigResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'resolume.instances',
    revision: 1,
    payload: { instances: [{ id: 'arena-main', url: '10.20.0.30:8080' }] },
    updatedAt: '2026-08-22T11:00:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api',
    restartRequired: false,
    restartRequiredReason: '',
  } as unknown as ResolumeInstancesConfigResponse
}

function renderScreen(instances: ResolumeInstance[], model: Partial<Model> = {}, scopes: string[] = ['config:write']) {
  return render(
    <ModelContext.Provider
      value={{
        ...initialModel(),
        resolume: instances,
        session: signedIn(scopes),
        serverTime: '2026-08-30T21:07:00Z',
        serverTimeReceivedAt: Date.now(),
        ...model,
      }}
    >
      <MemoryRouter initialEntries={['/monitor/fleet/resolume/arena-main']}>
        <Routes>
          <Route path="/monitor/fleet/resolume/:instanceId" element={<ResolumeConfig />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Resolume config', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => new Promise(() => {})
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.putResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.uploadResolumeComposition = () => new Promise(() => {})
  })

  it('renders the mock’s section labels', async () => {
    stubs.getResolumeComposition = () => Promise.resolve(composition())
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => Promise.resolve(recoveryConfig())
    stubs.getResolumeInstancesConfig = () => Promise.resolve(instancesConfig())

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument())
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)
    expect(headings).toEqual(['Stored composition', 'Clips that cannot be named', 'Recovery', 'What Arena is reporting'])
  })

  it('renders the compact active-revision summary for the recovery config, not a list heading', async () => {
    stubs.getResolumeComposition = () => Promise.resolve(composition())
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => Promise.resolve(recoveryConfig())
    stubs.getResolumeInstancesConfig = () => Promise.resolve(instancesConfig())
    stubs.getResolumeRecoveryConfigRevisions = () =>
      Promise.resolve({
        serverTime: '2026-08-30T21:00:00Z',
        kind: 'resolume.recovery',
        revisions: [
          { revision: 3, createdAt: '2026-08-22T11:09:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true },
        ],
      })

    renderScreen([instance()])

    expect(await screen.findAllByText(/Active revision/)).not.toHaveLength(0)
    expect(screen.queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
    expect(screen.queryByText('Active · 3')).not.toBeInTheDocument()
  })

  it('does not claim a read failure while the recovery config’s revisions fetch is still pending', async () => {
    stubs.getResolumeComposition = () => Promise.resolve(composition())
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => Promise.resolve(recoveryConfig())
    stubs.getResolumeInstancesConfig = () => Promise.resolve(instancesConfig())
    stubs.getResolumeRecoveryConfigRevisions = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByRole('heading', { level: 2, name: 'Recovery' })).toBeInTheDocument())
    expect(screen.queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
  })

  it('reports the read failure honestly when the recovery config’s revisions fetch is rejected', async () => {
    stubs.getResolumeComposition = () => Promise.resolve(composition())
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => Promise.resolve(recoveryConfig())
    stubs.getResolumeInstancesConfig = () => Promise.resolve(instancesConfig())
    stubs.getResolumeRecoveryConfigRevisions = () => Promise.reject(new Error('network down'))

    renderScreen([instance()])

    expect(await screen.findByText('Revision history could not be read just now.')).toBeInTheDocument()
  })

  it('lists only the ambiguous clips, reads N of the reported total', async () => {
    stubs.getResolumeComposition = () => Promise.resolve(composition())
    stubs.getResolumeRecovery = () => new Promise(() => {})
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByText('2 of 72')).toBeInTheDocument())
    expect(screen.getAllByText('Snow')).toHaveLength(2)
    expect(screen.queryByText('Countdown 5')).not.toBeInTheDocument()
  })

  it('renders no ambiguous clips as a settled empty state, not a dashed absence', async () => {
    stubs.getResolumeComposition = () =>
      Promise.resolve(composition({ clips: [{ id: 'clip-clean', deckId: 'deck-1', layerIndex: 1, columnIndex: 1, name: 'Countdown 5', nameGenerated: false, ambiguous: false }] as never }))
    stubs.getResolumeRecovery = () => new Promise(() => {})
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByText(/0 of 72 clips are ambiguous/)).toBeInTheDocument())
    const heading = screen.getByRole('heading', { name: 'Clips that cannot be named' })
    const section = heading.closest('section')
    expect(section).not.toBeNull()
    expect(section?.querySelector('.sm-strip__label--unobserved')).toBeNull()
  })

  it('renders the three record states as three distinct things, and unknown is never dark', async () => {
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByText('Clip connected')).toBeInTheDocument())
    expect(screen.getByText('Dark')).toBeInTheDocument()
    const unknownLabels = screen.getAllByText('Unknown')
    expect(unknownLabels.length).toBeGreaterThan(0)
  })

  it('renders an unconfirmable restore step as unavailable, not a failure', async () => {
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getAllByText('Restored').length).toBeGreaterThan(0))
    const restoreHeading = screen.getByRole('heading', { name: /Last restore/ })
    const restoreSection = restoreHeading.closest('section')
    expect(restoreSection).not.toBeNull()
    expect(restoreSection?.textContent).toContain('Unavailable')
    expect(restoreSection?.textContent).not.toContain('Failed')
  })

  it('derives the restore summary counts from the reported layers', async () => {
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByText(/4 layers had a recorded clip\. 3 restored, 1 skipped\./)).toBeInTheDocument())
  })

  it('renders resolume.composition.name as unavailable', async () => {
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => new Promise(() => {})
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([
      instance({
        observations: [
          evidence(),
          evidence({
            signal: 'resolume.composition.name',
            value: null,
            state: 'unsupported',
            reason: 'composition-level readiness terms cannot be read from Arena',
            observedAt: null,
          }),
        ],
      }),
    ])

    await waitFor(() => expect(screen.getByText(/composition-level readiness terms cannot be read from Arena/)).toBeInTheDocument())
    expect(screen.getAllByText('Unavailable').length).toBeGreaterThanOrEqual(2)
  })

  it('disables the recovery toggle with its reason when config:write is missing', async () => {
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => Promise.resolve(recovery())
    stubs.getResolumeRecoveryConfig = () => new Promise(() => {})
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()], {}, [])

    await waitFor(() => expect(screen.getByRole('button', { name: 'On' })).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'On' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Off' })).toBeDisabled()
    expect(screen.getByText(/does not include "config:write"/)).toBeInTheDocument()
  })

  it('refuses a stale recovery save and writes nothing', async () => {
    let putCalled = false
    const loaded = recoveryConfig()
    const current = { ...loaded, revision: 5, createdByPrincipalName: 'someone-else', updatedAt: '2026-08-30T20:00:00Z' }
    let reads = 0
    stubs.getResolumeRecoveryConfig = () => {
      reads += 1
      return Promise.resolve(reads === 1 ? loaded : current)
    }
    stubs.putResolumeRecoveryConfig = () => {
      putCalled = true
      return Promise.resolve(current)
    }
    stubs.getResolumeComposition = () => new Promise(() => {})
    stubs.getResolumeRecovery = () => Promise.resolve(recovery({ autoRestoreEnabled: true }))
    stubs.getResolumeInstancesConfig = () => new Promise(() => {})

    renderScreen([instance()])

    await waitFor(() => expect(screen.getByRole('button', { name: 'Off' })).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: 'Off' }))
    fireEvent.click(screen.getByRole('button', { name: /Save recovery/ }))

    expect(await screen.findByText('Stale write')).toBeInTheDocument()
    expect(putCalled).toBe(false)
  })
})
