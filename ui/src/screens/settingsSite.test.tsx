import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SessionResponse } from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  getSitePowerConfig: vi.fn(),
  putSitePowerConfig: vi.fn(),
  listConfigObjects: vi.fn(),
  runSitePower: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, ...stubs }
})

const { SettingsSite } = await import('./SettingsSite')
const { SitePowerTesting } = await import('./SitePowerTesting')

const session = (scopes: string[]) =>
  ({ authenticated: true, principal: { id: 'p1', name: 'op', kind: 'human', role: 'admin' }, scopes, scopesState: 'current', bootstrapRequired: false }) as unknown as SessionResponse

function renderWith(node: React.ReactNode, scopes: string[]) {
  return render(<ModelContext.Provider value={{ ...initialModel(), session: session(scopes), sessionFetchFailed: false }}>{node}</ModelContext.Provider>)
}

const empty = { serverTime: '', kind: 'show.sitepower', revision: 0, updatedAt: '', createdByPrincipalId: null, createdByPrincipalName: null, source: 'default', payload: { shutdownSequences: [], powerOnActions: [] } }

describe('SettingsSite', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('builds a projector sequence with a wait and a power cut and saves it', async () => {
    stubs.getSitePowerConfig.mockResolvedValue(empty)
    stubs.listConfigObjects.mockResolvedValue({ objects: [{ id: 'lamps-off', label: 'Lamps off', show: 'halloween', currentRevision: 1, updatedAt: '' }, { id: 'projectors-off', label: 'Projectors off', show: 'halloween', currentRevision: 1, updatedAt: '' }] })
    stubs.putSitePowerConfig.mockImplementation((payload: unknown) => Promise.resolve({ ...empty, revision: 1, payload }))
    renderWith(<SettingsSite />, ['config:write'])

    fireEvent.click(await screen.findByRole('button', { name: 'Add sequence' }))
    fireEvent.change(screen.getByLabelText('Sequence name'), { target: { value: 'Projectors' } })
    await waitFor(() => expect(screen.getAllByRole('option', { name: /Lamps off/ }).length).toBeGreaterThan(0))
    fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'lamps-off' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'Must confirm' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add step' }))
    fireEvent.change(screen.getByLabelText('Step 2'), { target: { value: 'wait' } })
    fireEvent.change(screen.getByLabelText('Seconds'), { target: { value: '300' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add step' }))
    fireEvent.change(screen.getAllByLabelText('Action')[1] as HTMLElement, { target: { value: 'projectors-off' } })
    fireEvent.click(screen.getAllByRole('checkbox', { name: 'Removes power' })[1] as HTMLElement)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(stubs.putSitePowerConfig).toHaveBeenCalledWith({
        shutdownSequences: [
          {
            id: 'projectors',
            label: 'Projectors',
            steps: [{ action: 'lamps-off', requireConfirmation: true }, { delayMs: 300000 }, { action: 'projectors-off', powerDomain: 'presentation', domainProvenance: 'operator-declared' }],
          },
        ],
        powerOnActions: [],
      }),
    )
  })

  it('will not save a step with no action', async () => {
    stubs.getSitePowerConfig.mockResolvedValue(empty)
    stubs.listConfigObjects.mockResolvedValue({ objects: [] })
    renderWith(<SettingsSite />, ['config:write'])
    fireEvent.click(await screen.findByRole('button', { name: 'Add sequence' }))
    expect(screen.getByText('Step 1 of New sequence has no action. Pick one.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('SitePowerTesting', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  it('runs System power off only after the second confirmation', async () => {
    stubs.runSitePower.mockResolvedValue({ serverTime: '', runId: 'r1', started: ['projectors'], message: 'Power off started. Results go to the activity log.' })
    renderWith(<SitePowerTesting />, ['show:emergencystop:invoke'])
    fireEvent.click(screen.getByRole('button', { name: 'Testing' }))
    fireEvent.click(screen.getByRole('button', { name: 'System power off' }))
    expect(stubs.runSitePower).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Power off' }))
    await waitFor(() => expect(stubs.runSitePower).toHaveBeenCalledWith('off'))
    expect(await screen.findByText('Power off started. Results go to the activity log.')).toBeTruthy()
  })

  it('disables both buttons without the emergency stop scope', () => {
    renderWith(<SitePowerTesting />, [])
    fireEvent.click(screen.getByRole('button', { name: 'Testing' }))
    expect((screen.getByRole('button', { name: 'System power on' }) as HTMLButtonElement).disabled).toBe(true)
  })
})
