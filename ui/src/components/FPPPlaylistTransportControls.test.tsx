import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  FPPNextPlaylistItemControl,
  FPPPausePlaylistControl,
  FPPPrevPlaylistItemControl,
  FPPResumePlaylistControl,
} from './FPPPlaylistTransportControls'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeModel } from '../app/test-support/fixtures'
import type { Evidence, FPPCommandResult, Model, SessionResponse } from '../app/types'

const { pauseFPPPlaylist, resumeFPPPlaylist, nextFPPPlaylistItem, prevFPPPlaylistItem } = vi.hoisted(() => ({
  pauseFPPPlaylist: vi.fn(),
  resumeFPPPlaylist: vi.fn(),
  nextFPPPlaylistItem: vi.fn(),
  prevFPPPlaylistItem: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, pauseFPPPlaylist, resumeFPPPlaylist, nextFPPPlaylistItem, prevFPPPlaylistItem }
})

afterEach(() => {
  cleanup()
  pauseFPPPlaylist.mockReset()
  resumeFPPPlaylist.mockReset()
  nextFPPPlaylistItem.mockReset()
  prevFPPPlaylistItem.mockReset()
})

const NOW = '2026-08-13T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['fpp:command'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function result(overrides: Partial<FPPCommandResult> = {}): FPPCommandResult {
  return {
    id: 'cmd-1',
    idempotencyKey: 'key-1',
    action: 'fpp.pause_playlist',
    instanceId: 'bench-fpp',
    params: {},
    replay: false,
    outcome: 'confirmed',
    outcomeState: 'current',
    outcomeReason: '',
    attributionDegraded: false,
    dispatchedAt: NOW,
    resolvedAt: NOW,
    ...overrides,
  }
}

function renderWithModel(ui: (model: Model) => React.ReactElement, model: Model) {
  render(<ModelContext.Provider value={model}>{ui(model)}</ModelContext.Provider>)
}

describe('FPPPausePlaylistControl', () => {
  it('renders disabled, never enabled, when the scope list is stale', () => {
    const model = makeModel({ session: signedIn(), sessionFetchFailed: true })
    renderWithModel(() => <FPPPausePlaylistControl instanceId="bench-fpp" />, model)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
  })

  it('dispatches pauseFPPPlaylist and renders a confirmed outcome', async () => {
    const user = userEvent.setup()
    pauseFPPPlaylist.mockResolvedValue(result({ outcome: 'confirmed' }))
    const model = makeModel({ session: signedIn() })
    renderWithModel(() => <FPPPausePlaylistControl instanceId="bench-fpp" />, model)

    await user.click(screen.getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(screen.getByText(/Confirmed: playback paused/)).toBeVisible())
    expect(pauseFPPPlaylist).toHaveBeenCalledWith('bench-fpp')
  })

  it('renders an unconfirmed outcome as an alert, never as success', async () => {
    const user = userEvent.setup()
    pauseFPPPlaylist.mockResolvedValue(
      result({ outcome: 'unconfirmed', outcomeReason: 'observed fpp.status = "playing", want "paused"' }),
    )
    const model = makeModel({ session: signedIn() })
    renderWithModel(() => <FPPPausePlaylistControl instanceId="bench-fpp" />, model)

    await user.click(screen.getByRole('button', { name: 'Pause' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Unconfirmed')
    expect(screen.queryByText(/^Confirmed/)).toBeNull()
  })
})

describe('FPPResumePlaylistControl', () => {
  it('dispatches resumeFPPPlaylist and renders a confirmed outcome', async () => {
    const user = userEvent.setup()
    resumeFPPPlaylist.mockResolvedValue(result({ outcome: 'confirmed' }))
    const model = makeModel({ session: signedIn() })
    renderWithModel(() => <FPPResumePlaylistControl instanceId="bench-fpp" />, model)

    await user.click(screen.getByRole('button', { name: 'Resume' }))
    await waitFor(() => expect(screen.getByText(/Confirmed: playback resumed/)).toBeVisible())
    expect(resumeFPPPlaylist).toHaveBeenCalledWith('bench-fpp')
  })
})

describe('FPPPrevPlaylistItemControl', () => {
  it('dispatches prevFPPPlaylistItem', async () => {
    const user = userEvent.setup()
    prevFPPPlaylistItem.mockResolvedValue(
      result({ outcome: 'confirmed', outcomeReason: 'fpp.playlist.index moved from 2 to 1 (source test)' }),
    )
    const model = makeModel({ session: signedIn() })
    renderWithModel(() => <FPPPrevPlaylistItemControl instanceId="bench-fpp" />, model)

    await user.click(screen.getByRole('button', { name: 'Previous Item' }))
    await waitFor(() => expect(screen.getByText(/moved from 2 to 1/)).toBeVisible())
    expect(prevFPPPlaylistItem).toHaveBeenCalledWith('bench-fpp')
  })
})

// CLAUDE.md item 3 / capture section 3.5: "nextPlaylistItem CAN STOP THE
// SHOW, and the control must say so." These tests break the warning in
// both directions — present at the last item, absent (but never silently
// "safe") when position is unknown — to prove the control actually
// distinguishes them rather than always (or never) warning.
describe('FPPNextPlaylistItemControl', () => {
  function evidence(signal: string, value: Evidence['value'], state: Evidence['state'] = 'current'): Evidence {
    return makeEvidence({ signal, value, state })
  }

  it('warns and relabels the button when currently on the last item of the playlist', () => {
    const model = makeModel({ session: signedIn() })
    const observations = [evidence('fpp.playlist.index', 3), evidence('fpp.playlist.count', 3)]
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={observations} />,
      model,
    )
    expect(screen.getByRole('alert').textContent).toContain('END the show')
    expect(screen.getByRole('button', { name: 'Next Item (ends show)' })).toBeInTheDocument()
  })

  it('warns on a one-item playlist too (capture: a single Next stops the show)', () => {
    const model = makeModel({ session: signedIn() })
    const observations = [evidence('fpp.playlist.index', 1), evidence('fpp.playlist.count', 1)]
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={observations} />,
      model,
    )
    expect(screen.getByRole('alert').textContent).toContain('END the show')
  })

  it('does not warn, and uses the plain label, when not on the last item', () => {
    const model = makeModel({ session: signedIn() })
    const observations = [evidence('fpp.playlist.index', 1), evidence('fpp.playlist.count', 3)]
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={observations} />,
      model,
    )
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.getByRole('button', { name: 'Next Item' })).toBeInTheDocument()
  })

  it('renders an explicit "not currently known" notice rather than silently assuming safe when there is no evidence at all', () => {
    const model = makeModel({ session: signedIn() })
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={[]} />,
      model,
    )
    expect(screen.getByText(/not currently known/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
    // Never claims to know it is safe: the plain, non-alarming label is
    // rendered, but that is NOT the same claim as "not last item" — the
    // text above is what carries the actual caution.
    expect(screen.getByRole('button', { name: 'Next Item' })).toBeInTheDocument()
  })

  it('renders the not-currently-known notice when evidence is stale rather than trusting a stale reading', () => {
    const model = makeModel({ session: signedIn() })
    const observations = [
      evidence('fpp.playlist.index', 3, 'stale'),
      evidence('fpp.playlist.count', 3, 'stale'),
    ]
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={observations} />,
      model,
    )
    expect(screen.getByText(/not currently known/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('dispatches nextFPPPlaylistItem on click regardless of hazard state', async () => {
    const user = userEvent.setup()
    nextFPPPlaylistItem.mockResolvedValue(
      result({
        outcome: 'confirmed',
        outcomeReason: 'fpp.status = "idle": Next Playlist Item at the last item ends the playlist',
      }),
    )
    const model = makeModel({ session: signedIn() })
    const observations = [evidence('fpp.playlist.index', 3), evidence('fpp.playlist.count', 3)]
    renderWithModel(
      () => <FPPNextPlaylistItemControl instanceId="bench-fpp" observations={observations} />,
      model,
    )

    await user.click(screen.getByRole('button', { name: 'Next Item (ends show)' }))
    await waitFor(() => expect(screen.getByText(/ends the playlist/)).toBeVisible())
    expect(nextFPPPlaylistItem).toHaveBeenCalledWith('bench-fpp')
  })
})
