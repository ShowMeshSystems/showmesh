import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AudioSettings } from './AudioSettings'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// ADR-039: the audio.settings engine-wide singleton's own editor,
// mirroring Configuration.assetssettings.test.tsx's coverage shape for
// the identical "config write surface singleton" pattern -- load, 404-as-
// never-happens (this kind never 404s), save, refusal, and scope gating.
const { getAudioSettingsConfig, putAudioSettingsConfig, getAudioSettingsConfigRevisions } = vi.hoisted(() => ({
  getAudioSettingsConfig: vi.fn(),
  putAudioSettingsConfig: vi.fn(),
  getAudioSettingsConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getAudioSettingsConfig, putAudioSettingsConfig, getAudioSettingsConfigRevisions }
})

afterEach(() => {
  cleanup()
  getAudioSettingsConfig.mockReset()
  putAudioSettingsConfig.mockReset()
  getAudioSettingsConfigRevisions.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

const activeConfig = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'audio.settings',
  revision: 3,
  payload: {
    driftIgnoreThresholdMs: 40,
    defaultFadeCurve: 'linear' as const,
    defaultFadeDurationMs: 500,
    defaultMaxBackgroundGainDb: -1.94,
    duckTargetGainDb: -13.98,
    ltcFrameRate: '30' as const,
    ltcDefaultStartOffset: '00:00:00:00',
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', kind: 'audio.settings', revisions: [] }

function renderView(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <AudioSettings />
    </ModelContext.Provider>,
  )
}

describe('AudioSettings', () => {
  it('renders the current payload', async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderView()

    await waitFor(() => expect(getAudioSettingsConfig).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('40')).toBeInTheDocument()
    expect(screen.getByLabelText('Default fade curve')).toHaveValue('linear')
    expect(screen.getByDisplayValue('500')).toBeInTheDocument()
    expect(screen.getByDisplayValue('-1.94')).toBeInTheDocument()
    expect(screen.getByDisplayValue('-13.98')).toBeInTheDocument()
    expect(screen.getByLabelText('LTC frame rate')).toHaveValue('30')
    expect(screen.getByDisplayValue('00:00:00:00')).toBeInTheDocument()
    // Gain fields are labelled in dB, never as a linear multiplier (the
    // settled unit ruling), with 0 dB named as unity.
    expect(screen.getAllByText(/\(dB/i).length).toBe(2)
    expect(screen.queryByText(/linear amplitude multiplier/i)).toBeNull()
    expect(screen.getByLabelText('Default maximum background gain in dB')).toBeInTheDocument()
    expect(screen.getByLabelText('Duck target gain in dB')).toBeInTheDocument()
  })

  it('saves the full payload and reloads on success', async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    putAudioSettingsConfig.mockResolvedValue({ ...activeConfig, revision: 4 })
    const user = userEvent.setup()
    renderView()

    const durationInput = await screen.findByDisplayValue('500')
    await user.clear(durationInput)
    await user.type(durationInput, '750')

    await user.click(screen.getByRole('button', { name: /save audio settings/i }))

    await waitFor(() =>
      expect(putAudioSettingsConfig).toHaveBeenCalledWith({
        driftIgnoreThresholdMs: 40,
        defaultFadeCurve: 'linear',
        defaultFadeDurationMs: 750,
        defaultMaxBackgroundGainDb: -1.94,
        duckTargetGainDb: -13.98,
        ltcFrameRate: '30',
        ltcDefaultStartOffset: '00:00:00:00',
      }),
    )
    await waitFor(() => expect(getAudioSettingsConfig).toHaveBeenCalledTimes(2))
  })

  it("renders the coordinator's own refusal reason and does not read as saved", async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    putAudioSettingsConfig.mockRejectedValue(new ApiError('duckTargetGainDb must be negative', 400))
    const user = userEvent.setup()
    renderView()

    await screen.findByDisplayValue('500')
    await user.click(screen.getByRole('button', { name: /save audio settings/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/duckTargetGainDb must be negative/i)
    // Still on revision 3: a refused write never reloads, so the screen
    // never reads as having saved anything.
    expect(getAudioSettingsConfig).toHaveBeenCalledTimes(1)
  })

  it('refuses an invalid field client-side before ever dispatching a PUT', async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    const user = userEvent.setup()
    renderView()

    const durationInput = await screen.findByDisplayValue('500')
    await user.clear(durationInput)
    await user.type(durationInput, '-5')

    await user.click(screen.getByRole('button', { name: /save audio settings/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/zero or greater/i)
    expect(putAudioSettingsConfig).not.toHaveBeenCalled()
  })

  it('renders revision history inside a closed details section', async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'audio.settings',
      revisions: [
        {
          revision: 3, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1',
          createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true,
        },
      ],
    })
    renderView()

    await screen.findByDisplayValue('500')
    const summary = screen.getByText('Revision history')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    expect(screen.getByText('admin-1', { selector: 'td' })).not.toBeVisible()
  })

  // A linear-looking value is the mistake this unit change exists to
  // prevent: 0.5 in the duck field used to mean a halving and is now a
  // positive half-decibel, which does not duck anything. It is refused
  // here rather than sent and refused by the coordinator.
  it('refuses a linear-looking duck target and a ceiling past the typo guard', async () => {
    getAudioSettingsConfig.mockResolvedValue(activeConfig)
    getAudioSettingsConfigRevisions.mockResolvedValue(emptyRevisions)
    const user = userEvent.setup()
    renderView()

    const duck = await screen.findByLabelText('Duck target gain in dB')
    await user.clear(duck)
    await user.type(duck, '0.5')
    await user.click(screen.getByRole('button', { name: /save/i }))
    expect(putAudioSettingsConfig).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent(/must be negative and at least -60 dB/i)

    await user.clear(duck)
    await user.type(duck, '-13.98')
    const ceiling = screen.getByLabelText('Default maximum background gain in dB')
    await user.clear(ceiling)
    await user.type(ceiling, '20')
    await user.click(screen.getByRole('button', { name: /save/i }))
    expect(putAudioSettingsConfig).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent(/must not exceed 12 dB/i)
  })

  it('is unavailable, with a stated reason, without the config:write scope', () => {
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Default fade curve')).not.toBeInTheDocument()
    expect(getAudioSettingsConfig).not.toHaveBeenCalled()
  })
})
