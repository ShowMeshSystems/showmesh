import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightSession } from './NightSession'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeNightSessionState } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { getCurrentNightSession, dispatchNightCommand } = vi.hoisted(() => ({
  getCurrentNightSession: vi.fn(),
  dispatchNightCommand: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getCurrentNightSession, dispatchNightCommand }
})

afterEach(() => {
  cleanup()
  getCurrentNightSession.mockReset()
  dispatchNightCommand.mockReset()
})

function renderView(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <NightSession />
    </ModelContext.Provider>,
  )
}

/** A promise this test controls the resolution timing of, for the two races this file proves. */
function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (err: unknown) => void } {
  let resolve!: (value: T) => void
  let reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const commandScopedSession = makeAuthenticatedSession({ scopes: ['night:command'] })

describe('NightSession', () => {
  // GET /night/session answers 200 with state "inactive" rather than 404
  // when no session has ever been created (api/openapi.yaml's own
  // description) — this is a real, renderable state, not an error.
  it('renders the inactive state from the initial GET, with no session ever created', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({ id: '', state: 'inactive' }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('inactive')).toBeVisible())
    expect(screen.getByText(/no session has ever been created/)).toBeVisible()
  })

  it('renders the fetch error distinguishably from "inactive", when nothing has ever loaded', async () => {
    getCurrentNightSession.mockRejectedValue(new Error('the coordinator is unreachable'))
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('the coordinator is unreachable')).toBeVisible())
  })

  // Review finding 1 (this seam's own defect, found after the first
  // report claimed the frame "always wins"): a live model.nightSession
  // frame that lands WHILE the initial GET is still in flight must not be
  // rolled back once that slower GET finally resolves with an OLDER
  // session. Comparison is by `updatedAt`, not arrival order.
  it('does not let a slow initial GET roll back a live frame that already landed with a newer updatedAt', async () => {
    const getResult = deferred<{ serverTime: string; session: ReturnType<typeof makeNightSessionState> }>()
    getCurrentNightSession.mockReturnValue(getResult.promise)

    const olderSession = makeNightSessionState({ state: 'inactive', updatedAt: '2026-08-22T00:00:00.000Z' })
    const newerSession = makeNightSessionState({ state: 'live', updatedAt: '2026-08-22T00:05:00.000Z' })

    const { rerender } = renderView(makeModel())
    // The live frame lands first, while the GET above is still pending.
    rerender(
      <ModelContext.Provider value={makeModel({ nightSession: newerSession })}>
        <NightSession />
      </ModelContext.Provider>,
    )
    await waitFor(() => expect(screen.getByText('live')).toBeVisible())

    // Now the slow GET finally resolves, with an OLDER session than the
    // frame already on screen. It must not win.
    getResult.resolve({ serverTime: '2026-08-22T00:00:00Z', session: olderSession })
    await waitFor(() => expect(getCurrentNightSession).toHaveBeenCalled())
    // Give the resolved promise's .then a turn to run before asserting
    // nothing changed.
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.getByText('live')).toBeVisible()
    expect(screen.queryByText('inactive')).toBeNull()
  })

  // Review finding 1's "the same class applies to handleApplied": a
  // command's own response is not immune to a live frame having already
  // landed with a newer updatedAt in the meantime.
  it('does not let a command response roll back a live frame that already landed with a newer updatedAt', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({ state: 'inactive', updatedAt: '2026-08-22T00:00:00.000Z' }),
    })
    const commandResult = deferred<{
      serverTime: string
      command: { command: string; outcome: string; attributionDegraded: boolean }
      session: ReturnType<typeof makeNightSessionState>
    }>()
    dispatchNightCommand.mockReturnValue(commandResult.promise)

    const { rerender } = renderView(makeModel({ session: commandScopedSession }))
    await waitFor(() => expect(screen.getByText('inactive')).toBeVisible())

    const button = screen.getByRole('button', { name: 'Start night' })
    const user = userEvent.setup()
    await user.click(button)

    // A live frame lands, newer than what is on screen, WHILE the
    // command's own response is still in flight.
    const newerSession = makeNightSessionState({ state: 'live', updatedAt: '2026-08-22T00:10:00.000Z' })
    rerender(
      <ModelContext.Provider value={makeModel({ session: commandScopedSession, nightSession: newerSession })}>
        <NightSession />
      </ModelContext.Provider>,
    )
    await waitFor(() => expect(screen.getByText('live')).toBeVisible())

    // The command's own response now resolves with an OLDER session
    // (e.g. the state it observed before its own effect committed). It
    // must not roll the view back.
    commandResult.resolve({
      serverTime: '2026-08-22T00:00:01Z',
      command: { command: 'start-night', outcome: 'applied', attributionDegraded: false },
      session: makeNightSessionState({ state: 'preshow', updatedAt: '2026-08-22T00:00:01.000Z' }),
    })
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.getByText('live')).toBeVisible()
    expect(screen.queryByText('preshow')).toBeNull()
  })

  // Review finding 2 (ADR-024 constraint 23): a transient read failure
  // AFTER a session has already loaded must not cost the operator
  // visibility of the lifecycle state — the last known state stays on
  // screen, marked stale, with the error shown ALONGSIDE it rather than
  // instead of it.
  it('keeps the last known session on screen, marked stale, when a background refresh fails', async () => {
    getCurrentNightSession.mockResolvedValueOnce({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({ state: 'live', updatedAt: '2026-08-22T00:00:00.000Z' }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('live')).toBeVisible())

    getCurrentNightSession.mockRejectedValueOnce(new Error('the coordinator is unreachable'))
    const reloadButton = screen.getByRole('button', { name: 'Reload' })
    const user = userEvent.setup()
    await user.click(reloadButton)

    await waitFor(() => expect(screen.getByText(/the coordinator is unreachable/)).toBeVisible())
    // The last known state is still fully rendered, not replaced by the
    // error — this is the exact defect this test exists to catch.
    expect(screen.getByText('live')).toBeVisible()
  })

  // Review finding 5: the ADR-031 decision 3 test must assert the
  // rendered DISTINCTION (tone), not merely that the string "unconfirmed"
  // is present and "failed" is absent — a mutation that gives both
  // `unconfirmed` and `unconfirmable` the failure tone and icon would
  // still leave both label strings intact and pass a text-only assertion.
  it('renders resolved/unconfirmed, failed, and refused cues with three different tones, never collapsing unconfirmed into a failure look', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        state: 'live',
        cues: {
          state: 'recorded',
          reason: '',
          cues: [
            {
              name: 'house-lights-down',
              phase: 'enterShow',
              role: 'lighting',
              action: 'house-lights-off',
              actionRevision: 2,
              state: 'resolved',
              outcome: 'unconfirmed',
              reason: 'no expected response declared',
              dispatchedAt: '2026-08-22T00:00:01Z',
              resolvedAt: '2026-08-22T00:00:02Z',
            },
            {
              name: 'fog-machine-on',
              phase: 'enterShow',
              role: 'other',
              action: 'fog-on',
              actionRevision: 1,
              state: 'resolved',
              outcome: 'failed',
              reason: 'MQTT publish refused',
              dispatchedAt: '2026-08-22T00:00:03Z',
              resolvedAt: '2026-08-22T00:00:04Z',
            },
            {
              name: 'announcement-vo',
              phase: 'enterShow',
              role: 'announcement',
              action: 'vo-play',
              actionRevision: 1,
              state: 'resolved',
              outcome: 'refused',
              reason: 'target declined the command',
              dispatchedAt: '2026-08-22T00:00:05Z',
              resolvedAt: '2026-08-22T00:00:06Z',
            },
          ],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('house-lights-down')).toBeVisible())

    function badgeSignature(labelText: string): { tone: string; icon: string | null } {
      const badge = screen.getByText(labelText).closest('.status-badge')
      if (badge === null) throw new Error(`expected "${labelText}" to render inside a .status-badge element`)
      return { tone: badge.className, icon: badge.querySelector('.status-badge__icon')?.textContent ?? null }
    }

    const unconfirmed = badgeSignature('unconfirmed')
    const failed = badgeSignature('failed')
    const refused = badgeSignature('refused')

    // The actual acceptance criterion: `unconfirmed` never carries the
    // failure tone, and every pair among these three is distinguishable
    // by AT LEAST tone or icon (never identical on both, which is what
    // "sharing a tone and differing only by label" means concretely).
    expect(unconfirmed.tone).not.toContain('status-badge--bad')
    expect(unconfirmed).not.toEqual(failed)
    expect(unconfirmed).not.toEqual(refused)
    // failed and refused are both real failures (both legitimately carry
    // the "bad" tone) but must still be distinguishable from each other
    // by icon, not just by label.
    expect(failed).not.toEqual(refused)

    // The cue's own STATE ("resolved") is a lifecycle position, never a
    // success claim (review finding 3) — must not carry the good tone
    // even though this outcome legitimately failed.
    const stateBadges = screen.getAllByText('resolved').map((el) => el.closest('.status-badge'))
    for (const badge of stateBadges) {
      if (badge === null) throw new Error('expected the cue state to render inside a .status-badge element')
      expect(badge.className).not.toContain('status-badge--good')
    }
  })

  // Review finding 6: the cue table lists every cue in the current
  // cycle's outbox across all phases; the heading must not claim a filter
  // this table does not apply, and each row's own Phase column must still
  // say which phase it belongs to.
  it('lists cues from every phase, with a Phase column per row, and a heading that does not claim a filter', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        state: 'live',
        cues: {
          state: 'recorded',
          reason: '',
          cues: [
            {
              name: 'enter-show-cue',
              phase: 'enterShow',
              role: 'lighting',
              action: 'a1',
              actionRevision: 1,
              state: 'resolved',
              outcome: 'confirmed',
              dispatchedAt: '2026-08-22T00:00:01Z',
              resolvedAt: '2026-08-22T00:00:02Z',
            },
            {
              name: 'enter-resting-cue',
              phase: 'enterResting',
              role: 'lighting',
              action: 'a2',
              actionRevision: 1,
              state: 'not_dispatched',
              dispatchedAt: null,
              resolvedAt: null,
            },
          ],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('enter-show-cue')).toBeVisible())
    // "enter-resting-cue" is also the next pending cue, so it legitimately
    // renders twice (the "Next cue" panel and the full cue table below
    // it) — this asserts it is present at all, not that it is unique.
    expect(screen.getAllByText('enter-resting-cue').length).toBeGreaterThan(0)
    expect(screen.getByRole('heading', { name: 'Cues' })).toBeVisible()
    expect(screen.getByRole('columnheader', { name: 'Phase' })).toBeVisible()

    // Review finding 8: a null dispatchedAt/resolvedAt is a KNOWN
    // absence, never rendered as "unknown". "not dispatched" also
    // legitimately labels the cue's OWN state badge (NightCueStateBadge),
    // so this asserts presence via getAllByText rather than requiring a
    // single match.
    expect(screen.getAllByText('not dispatched').length).toBeGreaterThan(0)
    expect(screen.getByText('not resolved')).toBeVisible()
  })

  // Review finding 7: a "not_configured" (or any non-recorded) state must
  // render the coordinator's own `reason`, never a hardcoded string that
  // discards the distinction between "not configured", "not yet
  // recorded", and "the store could not be read".
  it('renders the coordinator-supplied reason for a not_configured readiness result, not a hardcoded string', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        readiness: {
          state: 'not_configured',
          reason: 'this session configures no readiness checks at all',
          sameEpoch: false,
          fresh: false,
          checks: [],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() =>
      expect(screen.getByText('this session configures no readiness checks at all')).toBeVisible(),
    )
    expect(screen.queryByText('not configured')).toBeNull()
  })

  // The background-audio step log gets its own section, rendering
  // the same evidence (state, outcome, reason, pinned revision, dispatch
  // and resolution time) the Cues table already shows, and reusing the
  // same badges rather than a second vocabulary.
  it('renders background-audio steps with their sequence, kind, state and outcome', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        state: 'resting-intershow',
        backgroundAudio: {
          state: 'recorded',
          reason: '',
          steps: [
            {
              sequence: 'background',
              phase: 'restingBackground:node-a',
              cueName: 'resting-bed-start',
              nodeId: 'node-a',
              kind: 'start',
              actionRevision: 3,
              state: 'resolved',
              outcome: 'confirmed',
              dispatchedAt: '2026-08-22T00:00:01Z',
              resolvedAt: '2026-08-22T00:00:02Z',
            },
          ],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('resting-bed-start')).toBeVisible())
    expect(screen.getByRole('heading', { name: 'Background audio' })).toBeVisible()
    expect(screen.getByText('background')).toBeVisible()
    expect(screen.getByText('node-a')).toBeVisible()
    expect(screen.getByText('start')).toBeVisible()
    expect(screen.getByText('3')).toBeVisible()
    expect(screen.getByText('confirmed')).toBeVisible()
  })

  // The two failures this section exists for: a refused restore leaves the
  // bed stuck ducked, and an unconfirmed gain leaves it at an unknown
  // level. Neither may blend in with a confirmed step.
  it('renders a refused restore step and an unconfirmed gain step distinguishably from a confirmed one', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        state: 'resting-intershow',
        backgroundAudio: {
          state: 'recorded',
          reason: '',
          steps: [
            {
              sequence: 'background',
              phase: 'restingBackground:node-a',
              cueName: 'resting-bed-start',
              nodeId: 'node-a',
              kind: 'start',
              actionRevision: 1,
              state: 'resolved',
              outcome: 'confirmed',
              dispatchedAt: '2026-08-22T00:00:01Z',
              resolvedAt: '2026-08-22T00:00:02Z',
            },
            {
              // A refused restore on node-b is answerable from its
              // own nodeId alone, distinct from node-a's confirmed start
              // above — the two-node acceptance scenario this section
              // exists to make visible.
              sequence: 'announcement',
              phase: 'announcementSession:start:enterResting:node-b',
              cueName: 'vo-announcement',
              nodeId: 'node-b',
              kind: 'resume',
              actionRevision: 2,
              state: 'resolved',
              outcome: 'refused',
              reason: 'node declined to raise gain past the configured ceiling',
              dispatchedAt: '2026-08-22T00:05:01Z',
              resolvedAt: '2026-08-22T00:05:02Z',
            },
            {
              sequence: 'background',
              phase: 'restingBackground:node-a',
              cueName: 'resting-bed-gain',
              nodeId: 'node-a',
              kind: 'gain',
              actionRevision: 4,
              state: 'resolved',
              outcome: 'unconfirmed',
              reason: 'no expected response declared',
              dispatchedAt: '2026-08-22T00:10:01Z',
              resolvedAt: '2026-08-22T00:10:02Z',
            },
          ],
        },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('vo-announcement')).toBeVisible())

    function badgeSignature(labelText: string): { tone: string; icon: string | null } {
      const badge = screen.getByText(labelText).closest('.status-badge')
      if (badge === null) throw new Error(`expected "${labelText}" to render inside a .status-badge element`)
      return { tone: badge.className, icon: badge.querySelector('.status-badge__icon')?.textContent ?? null }
    }

    const confirmed = badgeSignature('confirmed')
    const refused = badgeSignature('refused')
    const unconfirmed = badgeSignature('unconfirmed')

    // The refused restore is a real failure — 'bad' tone — and both it and
    // the unconfirmed gain must read differently from the confirmed step.
    expect(refused.tone).toContain('status-badge--bad')
    expect(refused).not.toEqual(confirmed)
    expect(unconfirmed).not.toEqual(confirmed)
    expect(unconfirmed).not.toEqual(refused)
    expect(screen.getByText('node declined to raise gain past the configured ceiling')).toBeVisible()
    expect(screen.getByText('no expected response declared')).toBeVisible()
    // The refused restore is reported against node-b specifically, while
    // node-a's own two steps (start, gain) are unaffected by it.
    expect(screen.getByText('node-b')).toBeVisible()
    expect(screen.getAllByText('node-a')).toHaveLength(2)
  })

  // A session with no background audio configured (or none started this
  // cycle) must not render an empty or broken table.
  it('renders an explicit line, not an empty table, when no background-audio steps are recorded', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        backgroundAudio: { state: 'recorded', reason: '', steps: [] },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Background audio' })).toBeVisible())
    expect(
      screen.getByText('No background audio steps recorded for this cycle yet, or none is configured.'),
    ).toBeVisible()
    expect(screen.queryByRole('table')).toBeNull()
  })

  // A non-"recorded" backgroundAudio state (e.g. the read failed) renders
  // the coordinator's own reason, same posture as readiness/cues above.
  it('renders the coordinator-supplied reason when background-audio state is not "recorded"', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState({
        backgroundAudio: { state: 'not_available', reason: 'the outbox store could not be read', steps: [] },
      }),
    })
    renderView(makeModel())
    await waitFor(() => expect(screen.getByText('the outbox store could not be read')).toBeVisible())
  })

  it('renders the lifecycle command buttons, disabled with a stated reason when night:command is not held', async () => {
    getCurrentNightSession.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      session: makeNightSessionState(),
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    const button = await screen.findByRole('button', { name: 'Prepare site' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
  })
})
