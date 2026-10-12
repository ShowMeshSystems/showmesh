import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SystemRelease, SystemVersionsResponse } from '../api'
import { MonitorVersions, SettingsReleaseCheck } from './SystemVersions'

const stubs = vi.hoisted(() => ({
  getSystemVersions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, getSystemVersions: (...args: never[]) => stubs.getSystemVersions(...args) }
})

const INSTALL = 'curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh | sudo bash'
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

function respond(release: SystemRelease) {
  const response: SystemVersionsResponse = {
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
  stubs.getSystemVersions = () => Promise.resolve(response)
}

afterEach(() => {
  cleanup()
  stubs.getSystemVersions = () => new Promise(() => {})
})

describe('Monitor Versions block', () => {
  it('lists the coordinator, each node and each FPP plugin with one warning per mismatch', async () => {
    respond(NO_ANSWER)
    render(<MonitorVersions />)

    const table = await screen.findByRole('table')
    const rows = within(table).getAllByRole('row').map((row) => row.textContent)
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

  it('says a newer release is available and shows its install command', async () => {
    respond(AVAILABLE)
    render(<MonitorVersions />)

    expect(await screen.findByText('Release v0.3.0 is available.')).toBeTruthy()
    expect(screen.getByText(INSTALL)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Copy command' })).toBeTruthy()
  })

  it('stays quiet about a release that is not newer than the coordinator', async () => {
    respond({ ...AVAILABLE, tag: 'v0.2.0', available: false })
    render(<MonitorVersions />)

    await screen.findByRole('table')
    expect(screen.queryByText(/is available/)).toBeNull()
    expect(screen.queryByText(INSTALL)).toBeNull()
  })

  it('reports a failed read instead of an empty table', async () => {
    stubs.getSystemVersions = () => Promise.reject(new Error('boom'))
    render(<MonitorVersions />)

    expect(await screen.findByText('Read failed')).toBeTruthy()
    expect(screen.queryByRole('table')).toBeNull()
  })
})

describe('Settings release check', () => {
  it('shows the setting and the last result', async () => {
    respond(AVAILABLE)
    render(<SettingsReleaseCheck />)

    expect(await screen.findByText('Release v0.3.0 is available.')).toBeTruthy()
    expect(screen.getByText('On')).toBeTruthy()
    expect(screen.getByText('Every 6 hours')).toBeTruthy()
    expect(screen.getByText(INSTALL)).toBeTruthy()
    expect(screen.getByText('SHOWMESH_RELEASE_CHECK')).toBeTruthy()
  })

  it('says the check is off', async () => {
    respond({ ...NO_ANSWER, checkEnabled: false })
    render(<SettingsReleaseCheck />)

    expect(await screen.findByText('The release check is turned off.')).toBeTruthy()
    expect(screen.getByText('Off')).toBeTruthy()
    expect(screen.getByText('None yet')).toBeTruthy()
  })

  it('says why a check produced no answer', async () => {
    respond({ ...NO_ANSWER, problem: 'GitHub could not be reached. The coordinator will try again.' })
    render(<SettingsReleaseCheck />)

    expect(await screen.findByText('GitHub could not be reached. The coordinator will try again.')).toBeTruthy()
  })

  it('keeps the last answer and adds the problem when a later check fails', async () => {
    respond({ ...AVAILABLE, tag: 'v0.2.0', available: false, problem: 'GitHub could not be reached. The coordinator will try again.' })
    render(<SettingsReleaseCheck />)

    expect(await screen.findByText('v0.2.0 is the newest release. Nothing to install.')).toBeTruthy()
    expect(screen.getByText('GitHub could not be reached. The coordinator will try again.')).toBeTruthy()
  })
})
