import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import type { Model, ReleaseSettingsConfigResponse, SessionResponse, SystemRelease, SystemVersionsResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'
import { MonitorVersions, SettingsReleaseCheck } from './SystemVersions'

const stubs = vi.hoisted(() => ({
  getReleaseSettingsConfig: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putReleaseSettingsConfig: (() => new Promise(() => {})) as (...args: unknown[]) => Promise<unknown>,
  refreshSystemVersions: (() => {}) as () => void,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    getReleaseSettingsConfig: (...args: never[]) => stubs.getReleaseSettingsConfig(...args),
    putReleaseSettingsConfig: (...args: unknown[]) => stubs.putReleaseSettingsConfig(...args),
    refreshSystemVersions: () => stubs.refreshSystemVersions(),
  }
})

const INSTALL =
  'curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh | sudo bash -s -- --yes'
const WARNING = 'Node node-b runs 0.1.5. Install 0.2.0 on it to match the coordinator.'

const NO_ANSWER: SystemRelease = {
  checkEnabled: true,
  checkIntervalSeconds: 21600,
  checkedAt: null,
  tag: null,
  publishedAt: null,
  installCommand: null,
  available: false,
  problem: null,
}

const AVAILABLE: SystemRelease = {
  ...NO_ANSWER,
  checkedAt: '2026-10-12T18:00:00Z',
  tag: 'v0.3.0',
  publishedAt: '2026-10-10T12:00:00Z',
  installCommand: INSTALL,
  available: true,
}

function versions(release: SystemRelease): SystemVersionsResponse {
  return {
    serverTime: '2026-10-12T18:05:00Z',
    coordinator: { version: '0.2.0' },
    nodes: [
      { id: 'node-a', version: '0.2.0', reason: null, mismatch: false, warning: null },
      { id: 'node-b', version: '0.1.5', reason: null, mismatch: true, warning: WARNING },
    ],
    plugins: [
      { id: 'player-1', version: null, reason: 'The FPP plugin does not report its version.', mismatch: false, warning: null },
    ],
    release,
  }
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-10-12T18:05:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'Ada', role: 'admin' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-10-12T17:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function modelWith(model: Partial<Model>): Model {
  return { ...initialModel(), session: signedIn(['config:write']), ...model }
}

function renderWith(ui: React.ReactElement, model: Partial<Model>) {
  return render(<ModelContext.Provider value={modelWith(model)}>{ui}</ModelContext.Provider>)
}

function settings(revision: number, enabled: boolean, checkIntervalSeconds: number): ReleaseSettingsConfigResponse {
  return {
    serverTime: '2026-10-12T18:05:00Z',
    kind: 'release.settings',
    revision,
    payload: { enabled, checkIntervalSeconds },
    updatedAt: '2026-10-12T18:00:00Z',
    createdByPrincipalId: null,
    createdByPrincipalName: null,
    source: revision === 0 ? 'default' : 'api',
  }
}

afterEach(() => {
  cleanup()
  stubs.getReleaseSettingsConfig = () => new Promise(() => {})
  stubs.putReleaseSettingsConfig = () => new Promise(() => {})
  stubs.refreshSystemVersions = () => {}
})

describe('Monitor Versions block', () => {
  it('lists the coordinator, each node and each FPP plugin with one warning per mismatch', () => {
    renderWith(<MonitorVersions />, { systemVersions: versions(NO_ANSWER) })

    const rows = within(screen.getByRole('table')).getAllByRole('row').map((row) => row.textContent)
    expect(rows).toEqual([
      'ComponentVersionStatus',
      'Coordinator0.2.0',
      expect.stringMatching(/^Node node-a0\.2\.0.*Matches$/),
      expect.stringMatching(/^Node node-b0\.1\.5.*Mismatch$/),
      'FPP plugin player-1Not reportedThe FPP plugin does not report its version.',
    ])
    expect(screen.getAllByRole('status').map((el) => el.textContent)).toEqual([WARNING])
    expect(screen.queryByText(/is available/)).toBeNull()
  })

  it('follows the store: a new answer in the model replaces what is shown, with no reload', () => {
    const view = renderWith(<MonitorVersions />, { systemVersions: versions(NO_ANSWER) })
    expect(screen.queryByText(/is available/)).toBeNull()

    view.rerender(
      <ModelContext.Provider value={modelWith({ systemVersions: versions(AVAILABLE) })}>
        <MonitorVersions />
      </ModelContext.Provider>,
    )

    expect(screen.getByText('Release v0.3.0 is available.')).toBeTruthy()
    expect(screen.getByText(INSTALL)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy command' })).toBeTruthy()
  })

  it('stays quiet about a release that is not newer than the coordinator', () => {
    renderWith(<MonitorVersions />, { systemVersions: versions({ ...AVAILABLE, tag: 'v0.2.0', available: false }) })

    expect(screen.getByRole('table')).toBeTruthy()
    expect(screen.queryByText(/is available/)).toBeNull()
    expect(screen.queryByText(INSTALL)).toBeNull()
  })

  it('says it is reading before the first answer and reports a failed read instead of an empty table', () => {
    const view = renderWith(<MonitorVersions />, {})
    expect(screen.getByText('Reading')).toBeTruthy()

    view.rerender(
      <ModelContext.Provider value={modelWith({ systemVersionsFailure: { error: new Error('boom') } })}>
        <MonitorVersions />
      </ModelContext.Provider>,
    )
    expect(screen.getByText('Read failed')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
  })
})

describe('Settings release check', () => {
  it('loads the stored setting into an editable form and shows the last result', async () => {
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(2, true, 21600))
    renderWith(<SettingsReleaseCheck />, { systemVersions: versions(AVAILABLE) })

    const interval = (await screen.findByLabelText('Minutes between checks')) as HTMLInputElement
    expect(interval.value).toBe('360')
    expect((screen.getByLabelText('Check GitHub for a newer release') as HTMLInputElement).checked).toBe(true)
    expect(screen.getByText('Release v0.3.0 is available.')).toBeTruthy()
    expect(screen.getByText(INSTALL)).toBeTruthy()
    expect(screen.queryByText(/SHOWMESH_RELEASE_CHECK/)).toBeNull()
    expect((screen.getByRole('button', { name: 'Save release check' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('saves an edit against the loaded revision and asks the store for the new answer', async () => {
    let stored = settings(2, true, 21600)
    const puts: unknown[][] = []
    let refreshed = 0
    stubs.getReleaseSettingsConfig = () => Promise.resolve(stored)
    stubs.putReleaseSettingsConfig = (...args: unknown[]) => {
      puts.push(args)
      stored = settings(3, false, 900)
      return Promise.resolve(stored)
    }
    stubs.refreshSystemVersions = () => {
      refreshed += 1
    }
    renderWith(<SettingsReleaseCheck />, { systemVersions: versions(NO_ANSWER) })

    fireEvent.click(await screen.findByLabelText('Check GitHub for a newer release'))
    fireEvent.change(screen.getByLabelText('Minutes between checks'), { target: { value: '15' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save release check' }))

    await waitFor(() => expect(puts).toEqual([[{ enabled: false, checkIntervalSeconds: 900 }, 2]]))
    await waitFor(() => expect(refreshed).toBe(1))
    await waitFor(() => expect((screen.getByLabelText('Minutes between checks') as HTMLInputElement).value).toBe('15'))
  })

  it('refuses an interval under five minutes without calling the coordinator', async () => {
    let puts = 0
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(0, true, 21600))
    stubs.putReleaseSettingsConfig = () => {
      puts += 1
      return Promise.resolve(settings(1, true, 60))
    }
    renderWith(<SettingsReleaseCheck />, {})

    fireEvent.change(await screen.findByLabelText('Minutes between checks'), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save release check' }))

    expect(await screen.findByText('The check interval is too short. Set it to 5 minutes or more.')).toBeTruthy()
    expect(puts).toBe(0)
  })

  it('refuses an interval over one week without calling the coordinator', async () => {
    let puts = 0
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(0, true, 21600))
    stubs.putReleaseSettingsConfig = () => {
      puts += 1
      return Promise.resolve(settings(1, true, 604800))
    }
    renderWith(<SettingsReleaseCheck />, {})

    fireEvent.change(await screen.findByLabelText('Minutes between checks'), { target: { value: '10081' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save release check' }))

    expect(await screen.findByText('The check interval is too long. Set it to 10080 minutes or less.')).toBeTruthy()
    expect(puts).toBe(0)
  })

  it('shows the coordinator\'s reason when a save is refused', async () => {
    const detail =
      "The release check is still set in the coordinator's environment. Remove SHOWMESH_RELEASE_CHECK and SHOWMESH_RELEASE_CHECK_INTERVAL, then restart the coordinator."
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(1, true, 21600))
    stubs.putReleaseSettingsConfig = () =>
      Promise.reject(new ApiError(detail, 409, 'https://showmesh.dev/problems/conflict'))
    renderWith(<SettingsReleaseCheck />, {})

    fireEvent.click(await screen.findByLabelText('Check GitHub for a newer release'))
    fireEvent.click(screen.getByRole('button', { name: 'Save release check' }))

    expect(await screen.findByText('Save failed')).toBeTruthy()
    expect(screen.getByText(new RegExp('Remove SHOWMESH_RELEASE_CHECK'))).toBeTruthy()
  })

  it('refuses a save when someone else changed the setting since it was loaded', async () => {
    let reads = 0
    stubs.getReleaseSettingsConfig = () => {
      reads += 1
      return Promise.resolve(reads === 1 ? settings(2, true, 21600) : { ...settings(3, false, 21600), createdByPrincipalName: 'Grace' })
    }
    renderWith(<SettingsReleaseCheck />, {})

    fireEvent.click(await screen.findByLabelText('Check GitHub for a newer release'))
    fireEvent.click(screen.getByRole('button', { name: 'Save release check' }))

    expect(await screen.findByText('Stale write')).toBeTruthy()
  })

  it('says the check is off, and why a check produced no answer', async () => {
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(1, false, 21600))
    const view = renderWith(<SettingsReleaseCheck />, { systemVersions: versions({ ...NO_ANSWER, checkEnabled: false }) })
    expect(await screen.findByText('The release check is turned off.')).toBeTruthy()
    expect(screen.getByText('None yet')).toBeTruthy()

    const problem = 'GitHub refused the request. The coordinator tries again in 6 hours.'
    view.rerender(
      <ModelContext.Provider value={modelWith({ systemVersions: versions({ ...NO_ANSWER, problem }) })}>
        <SettingsReleaseCheck />
      </ModelContext.Provider>,
    )
    expect(screen.getByText(problem)).toBeTruthy()
  })

  it('keeps the last answer and adds the problem when a later check fails', async () => {
    const problem = 'GitHub could not be reached. The coordinator will try again.'
    stubs.getReleaseSettingsConfig = () => Promise.resolve(settings(1, true, 21600))
    renderWith(<SettingsReleaseCheck />, {
      systemVersions: versions({ ...AVAILABLE, tag: 'v0.2.0', available: false, problem }),
    })

    expect(await screen.findByText('v0.2.0 is the newest release. Nothing to install.')).toBeTruthy()
    expect(screen.getByText(problem)).toBeTruthy()
  })
})
