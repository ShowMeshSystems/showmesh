import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FPPCommandOutcome } from './FPPCommandOutcome'
import type { FPPCommandResult } from '../app/types'

afterEach(() => cleanup())

const NOW = '2026-08-13T00:00:00.000Z'

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

describe('FPPCommandOutcome', () => {
  // A test's name is a claim (CLAUDE.md): this is the exact property
  // this component exists for — a "confirmed" outcome with no server
  // reason falls back to the caller's own honest label, never a blank.
  it('renders the caller-provided summary when the server left outcomeReason empty', () => {
    render(<FPPCommandOutcome result={result({ outcome: 'confirmed', outcomeReason: '' })} confirmedSummary="playback paused" />)
    expect(screen.getByText('Confirmed: playback paused')).toBeVisible()
  })

  // stopPlaylistGracefully/nextPlaylistItem's own case: the server's own
  // words must win over the caller's summary, never be replaced by it —
  // this is the exact defect this component exists to prevent (a
  // "confirmed" stopPlaylistGracefully reading as "the show stopped"
  // when it has only started winding down).
  it('renders the server-provided outcomeReason instead of confirmedSummary when both are present', () => {
    render(
      <FPPCommandOutcome
        result={result({
          outcome: 'confirmed',
          outcomeReason: 'fpp.status = "stopping gracefully": FPP accepted the graceful stop but has NOT stopped yet',
        })}
        confirmedSummary="playback stopped"
      />,
    )
    expect(screen.getByText(/FPP accepted the graceful stop but has NOT stopped yet/)).toBeVisible()
    expect(screen.queryByText('Confirmed: playback stopped')).toBeNull()
  })

  it('renders an unconfirmed outcome as an alert, distinct from confirmed', () => {
    render(
      <FPPCommandOutcome
        result={result({ outcome: 'unconfirmed', outcomeReason: 'observed fpp.status = "playing", want "paused"' })}
        confirmedSummary="playback paused"
      />,
    )
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('Unconfirmed')
    expect(alert.textContent).toContain('want "paused"')
    expect(screen.queryByText(/^Confirmed/)).toBeNull()
  })

  it('renders a pending state for an empty outcome, never confirmed or unconfirmed', () => {
    render(<FPPCommandOutcome result={result({ outcome: '' })} confirmedSummary="playback paused" />)
    expect(screen.getByText(/Pending/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('flags a replayed result distinctly, alongside whatever outcome it carries', () => {
    render(<FPPCommandOutcome result={result({ replay: true, outcome: 'confirmed' })} confirmedSummary="playback paused" />)
    expect(screen.getByText(/already used/)).toBeVisible()
  })

  it('surfaces attributionDegraded as its own note', () => {
    render(<FPPCommandOutcome result={result({ attributionDegraded: true })} confirmedSummary="playback paused" />)
    expect(screen.getByText(/could not record this command in its audit log/)).toBeVisible()
  })
})
