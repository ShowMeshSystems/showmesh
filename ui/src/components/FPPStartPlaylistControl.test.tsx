import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPStartPlaylistControl } from './FPPStartPlaylistControl'
import { ApiError } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { FPPCommandResult, SessionResponse } from '../app/types'

const { startFPPPlaylist } = vi.hoisted(() => ({ startFPPPlaylist: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, startFPPPlaylist }
})

afterEach(() => {
  cleanup()
  startFPPPlaylist.mockReset()
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
    action: 'fpp.start_playlist',
    instanceId: 'bench-fpp',
    // FPPCommandResult.params echoes whatever this command's own
    // normalized params were (api/openapi.yaml: additionalProperties:
    // true, matching AuditEntry.params) — nothing under test reads
    // result.params, so this fixture leaves it empty rather than
    // inventing a value.
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

function renderControl() {
  render(
    <ModelContext.Provider value={makeModel({ session: signedIn() })}>
      <FPPStartPlaylistControl instanceId="bench-fpp" />
    </ModelContext.Provider>,
  )
}

describe('FPPStartPlaylistControl', () => {
  it('renders disabled, never enabled, when the operator lacks fpp:command', () => {
    render(
      <ModelContext.Provider value={makeModel({ session: signedIn({ scopes: [] }) })}>
        <FPPStartPlaylistControl instanceId="bench-fpp" />
      </ModelContext.Provider>,
    )
    expect(screen.getByRole('button', { name: 'Start Playlist' })).toBeDisabled()
  })

  it('refuses to dispatch with no playlist name entered', async () => {
    const user = userEvent.setup()
    renderControl()
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Enter a playlist name')
    expect(startFPPPlaylist).not.toHaveBeenCalled()
  })

  it('dispatches with ifBusy="refuse" by default, and the entered name/repeat', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockResolvedValue(result())
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))

    await waitFor(() =>
      expect(startFPPPlaylist).toHaveBeenCalledWith('bench-fpp', 'showmesh-test', true, 'refuse'),
    )
  })

  it('renders a confirmed outcome naming the requested playlist', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockResolvedValue(result())
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))
    await waitFor(() =>
      expect(screen.getByText(/Confirmed: playlist "showmesh-test" is playing/)).toBeVisible(),
    )
  })

  // Capture section 5 / CLAUDE.md item 5: a 409 means a DIFFERENT
  // playlist is confirmed playing. The UI must show what is playing (the
  // server's own detail text) and offer the explicit replace path —
  // never retry with replace automatically.
  it('on a 409, shows the server detail and offers an explicit replace action, without auto-retrying', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockRejectedValueOnce(
      new ApiError(
        'instance "bench-fpp" is currently playing "other-show"; this request\'s ifBusy="refuse" (the default) refuses to interrupt it.',
        409,
        // fppStartPlaylistBusyProblem's own type (Step 8 review finding 8:
        // this used to be the plain, shared PROBLEM_TYPE.conflict, which is
        // now reserved for an idempotency-key conflict instead — a
        // DIFFERENT 409 with the OPPOSITE remedy; see the test below this
        // one for that case).
        PROBLEM_TYPE.fppStartPlaylistBusy,
      ),
    )
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('currently playing "other-show"')
    // Only ONE call so far — no automatic retry.
    expect(startFPPPlaylist).toHaveBeenCalledTimes(1)
    expect(startFPPPlaylist).toHaveBeenLastCalledWith('bench-fpp', 'showmesh-test', false, 'refuse')

    // The operator's own explicit click is what sends ifBusy="replace".
    startFPPPlaylist.mockResolvedValueOnce(result())
    await user.click(screen.getByRole('button', { name: /Start anyway/ }))
    await waitFor(() => expect(startFPPPlaylist).toHaveBeenCalledTimes(2))
    expect(startFPPPlaylist).toHaveBeenLastCalledWith('bench-fpp', 'showmesh-test', false, 'replace')
  })

  // Finding 5 (Step 8 client-side review, fixed on the wire): the OTHER
  // 409 shape this endpoint can answer,
  // fppStartPlaylistEvidenceNotCurrentProblem
  // (internal/coordinator/api/problem.go) — the coordinator could not
  // tell what is playing, not "something else is confirmed playing" — now
  // carries its OWN `type`
  // (PROBLEM_TYPE.fppStartPlaylistEvidenceNotCurrent), distinct from the
  // `conflict` the OTHER 409 above carries. Broken to verify: reverting
  // classifyStartPlaylistConflict to branch on `detail` prose instead of
  // `err.problemType` (or reverting the button label back to the
  // unconditional "Start anyway (replace what is currently playing)")
  // makes this test's second assertion fail — see this task's report.
  it('on the evidence-not-current 409, never claims to know what is currently playing', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockRejectedValueOnce(
      new ApiError(
        'startPlaylist against instance "bench-fpp" with ifBusy="refuse" (the default) requires CURRENT evidence ' +
          'of fpp.status to decide whether a different playlist is already running, and this coordinator\'s most ' +
          'recent evidence is not current: no observation received in the last 45s. This request is refused rather ' +
          'than proceeding on the grounds that it could not tell.',
        409,
        PROBLEM_TYPE.fppStartPlaylistEvidenceNotCurrent,
      ),
    )
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('requires CURRENT evidence')
    // The overclaiming wording from the OTHER 409 case must not appear
    // here — the coordinator's own detail text, one line above, just said
    // it could not tell what is playing.
    expect(screen.queryByRole('button', { name: /replace what is currently playing/ })).toBeNull()
    expect(screen.getByRole('button', { name: /Start anyway/ })).toBeVisible()
  })

  // Finding 6 (Step 8 client-side review): confirmedSummary must describe
  // what was actually DISPATCHED, captured at dispatch time, never
  // re-read from the live input — the text box is not disabled once a
  // result arrives (nothing in this component disables it after
  // success), so editing the name afterward must not retroactively
  // rewrite what the confirmation claims happened. Broken to verify:
  // reverting confirmedSummary to read the live `playlist` state instead
  // of `state.dispatchedPlaylist` makes this test's final assertion fail
  // — see this task's report.
  it('a confirmed summary names the playlist that was actually dispatched, not whatever the input holds now', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockResolvedValue(result())
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))
    await waitFor(() =>
      expect(screen.getByText(/Confirmed: playlist "showmesh-test" is playing/)).toBeVisible(),
    )

    // Edit the name AFTER the confirmed result already rendered.
    const input = screen.getByRole('textbox', { name: /Playlist name/ })
    await user.clear(input)
    await user.type(input, 'a-totally-different-playlist')

    expect(screen.getByText(/Confirmed: playlist "showmesh-test" is playing/)).toBeVisible()
    expect(screen.queryByText(/a-totally-different-playlist" is playing/)).toBeNull()
  })

  // Step 8 review finding 8's own regression test: this control mints a
  // fresh idempotency key on every dispatch, so a plain `conflict` 409
  // (an idempotency-key reuse) is not reachable from either of its two
  // call sites in practice — but if this endpoint's behavior ever changed
  // underneath it, or a genuinely unrecognized 409 `type` arrived, this
  // control must NOT invent a "replace what is currently playing" CTA for
  // a cause it cannot actually name; that CTA's own remedy (ifBusy:
  // replace) is the OPPOSITE of a conflict's real remedy (mint a fresh
  // key). Broken to verify: reverting classifyStartPlaylistConflict's
  // fallback from 'unknown' back to 'differentPlaylistPlaying' makes this
  // test's "Start anyway" assertion fail.
  it('on a 409 whose type it does not recognize, falls back to a plain error rather than inventing a replace CTA', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockRejectedValueOnce(
      new ApiError('idempotencyKey was already used for a different command', 409, PROBLEM_TYPE.conflict),
    )
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('idempotencyKey was already used')
    expect(screen.queryByRole('button', { name: /Start anyway/ })).toBeNull()
  })

  it('renders a plain error, distinct from the busy/409 case, for any other failure', async () => {
    const user = userEvent.setup()
    startFPPPlaylist.mockRejectedValueOnce(new Error('network error'))
    renderControl()

    await user.type(screen.getByRole('textbox', { name: /Playlist name/ }), 'showmesh-test')
    await user.click(screen.getByRole('button', { name: 'Start Playlist' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('network error')
    expect(screen.queryByRole('button', { name: /Start anyway/ })).toBeNull()
  })
})
