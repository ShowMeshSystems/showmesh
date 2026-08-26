import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AudioSessionPanel } from './AudioSessionPanel'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeModel } from '../app/test-support/fixtures'
import type { AudioSessionCommandResult, Model, ObservationEntry, SessionResponse } from '../app/types'

// Mirrors RenderSurfacePanel.test.tsx's own mocking shape: mocked at the
// '../api' boundary, isolating this component's own job (ScopedButton
// wiring to "audio:command", grouping by audio_session resource, the
// stop arm-then-confirm pair, and rendering the audio outcome vocabulary
// honestly per ADR-003) from store.ts's own network behavior.
const {
  pauseAudioSession,
  resumeAudioSession,
  stopAudioSession,
  muteAudioSessionOutput,
  unmuteAudioSessionOutput,
  prepareAudioSession,
  startAudioSession,
  advanceAudioSession,
  clearAudioSession,
  seekAudioSession,
  setAudioSessionGain,
  fadeAudioSessionGain,
  applyAudioSession,
} = vi.hoisted(() => ({
  pauseAudioSession: vi.fn(),
  resumeAudioSession: vi.fn(),
  stopAudioSession: vi.fn(),
  muteAudioSessionOutput: vi.fn(),
  unmuteAudioSessionOutput: vi.fn(),
  prepareAudioSession: vi.fn(),
  startAudioSession: vi.fn(),
  advanceAudioSession: vi.fn(),
  clearAudioSession: vi.fn(),
  seekAudioSession: vi.fn(),
  setAudioSessionGain: vi.fn(),
  fadeAudioSessionGain: vi.fn(),
  applyAudioSession: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    pauseAudioSession,
    resumeAudioSession,
    stopAudioSession,
    muteAudioSessionOutput,
    unmuteAudioSessionOutput,
    prepareAudioSession,
    startAudioSession,
    advanceAudioSession,
    clearAudioSession,
    seekAudioSession,
    setAudioSessionGain,
    fadeAudioSessionGain,
    applyAudioSession,
  }
})

afterEach(() => {
  cleanup()
  pauseAudioSession.mockReset()
  resumeAudioSession.mockReset()
  stopAudioSession.mockReset()
  muteAudioSessionOutput.mockReset()
  unmuteAudioSessionOutput.mockReset()
  prepareAudioSession.mockReset()
  startAudioSession.mockReset()
  advanceAudioSession.mockReset()
  clearAudioSession.mockReset()
  seekAudioSession.mockReset()
  setAudioSessionGain.mockReset()
  fadeAudioSessionGain.mockReset()
  applyAudioSession.mockReset()
})

const NOW = '2026-08-25T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['audio:command'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function entry(overrides: Partial<ObservationEntry> = {}): ObservationEntry {
  return {
    resource: { kind: 'audio_session', id: 'session-1' },
    ...makeEvidence({ signal: 'audio_session.state', value: 'playing' }),
    ...overrides,
  }
}

function commandResult(overrides: Partial<AudioSessionCommandResult> = {}): AudioSessionCommandResult {
  return {
    commandId: 'cmd-1',
    idempotencyKey: 'key-1',
    action: 'audio.session.pause',
    nodeId: 'media-01',
    sessionId: 'session-1',
    replay: false,
    outcome: 'position',
    reason: '',
    dispatchedAt: NOW,
    resolvedAt: NOW,
    attributionDegraded: false,
    ...overrides,
  }
}

function renderPanel(model: Model, entries: ObservationEntry[]) {
  render(
    <ModelContext.Provider value={model}>
      <AudioSessionPanel nodeId="media-01" entries={entries} />
    </ModelContext.Provider>,
  )
}

describe('AudioSessionPanel', () => {
  it('renders a sensible message for a node with no audio session evidence', () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [])
    expect(screen.getByRole('status')).toHaveTextContent('no audio session evidence')
  })

  it('ignores node-scoped node.audio.* entries and groups only audio_session entries', () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [
      entry({ resource: { kind: 'node', id: 'media-01' }, signal: 'node.audio.engine.state', value: 'usable' }),
      entry({ resource: { kind: 'audio_session', id: 'session-1' }, signal: 'audio_session.state', value: 'playing' }),
    ])
    expect(screen.getByText('Session: session-1')).toBeInTheDocument()
    expect(screen.queryByText('node.audio.engine.state')).not.toBeInTheDocument()
  })

  it('renders every control disabled, never enabled, without audio:command', async () => {
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    renderPanel(model, [entry()])
    expect(screen.getByRole('button', { name: 'Prepare' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Resume' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Advance' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Mute' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Unmute' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Seek' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Set gain' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Fade' })).toBeDisabled()
    // Stop's and clear's arm buttons are plain buttons (the ScopedButton
    // only guards the confirm step), so they stay clickable; arming
    // either must still reach a disabled confirm control.
    await userEvent.click(screen.getByRole('button', { name: 'Stop…' }))
    expect(screen.getByRole('button', { name: /Confirm: stop/ })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: 'Clear…' }))
    expect(screen.getByRole('button', { name: /Confirm: clear/ })).toBeDisabled()
    // Apply's own arm button is likewise plain (only its confirm step is
    // scope-gated), so arm it too and check the confirm control it reveals.
    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))
    expect(screen.getByRole('button', { name: /Confirm: apply/ })).toBeDisabled()
  })

  it('dispatches pause and renders a confirmed outcome honestly', async () => {
    pauseAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.pause', outcome: 'position' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 4 })])

    await userEvent.click(screen.getByRole('button', { name: 'Pause' }))

    expect(pauseAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 5)
    expect(await screen.findByText('Confirmed: position')).toBeInTheDocument()
  })

  it('surfaces attributionDegraded as its own note, even on a confirmed outcome', async () => {
    pauseAudioSession.mockResolvedValue(
      commandResult({ action: 'audio.session.pause', outcome: 'position', attributionDegraded: true }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 4 })])

    await userEvent.click(screen.getByRole('button', { name: 'Pause' }))

    expect(await screen.findByText(/could not record this command in its audit log/)).toBeInTheDocument()
  })

  it('does not render the attribution note when attributionDegraded is false', async () => {
    resumeAudioSession.mockResolvedValue(
      commandResult({ action: 'audio.session.resume', outcome: 'started', attributionDegraded: false }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.state', value: 'paused' })])

    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))

    await screen.findByText('Confirmed: started')
    expect(screen.queryByText(/could not record this command in its audit log/)).not.toBeInTheDocument()
  })

  it('defaults the revision to 1 when no desired-revision evidence has ever been observed', async () => {
    resumeAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.resume', outcome: 'started' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.state', value: 'paused' })])

    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))

    expect(resumeAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 1)
  })

  it('renders an accepted-but-unconfirmed dispatch as unconfirmed with its stated reason, never as success', async () => {
    muteAudioSessionOutput.mockResolvedValue(
      commandResult({
        action: 'audio.output.mute',
        outcome: 'unconfirmable',
        reason: 'no evidence arrived before the deadline',
      }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Mute' }))

    const unconfirmed = await screen.findByRole('alert')
    expect(unconfirmed).toHaveTextContent('Unconfirmed: no evidence arrived before the deadline')
  })

  it('renders a refused dispatch as a failure, never as success', async () => {
    unmuteAudioSessionOutput.mockResolvedValue(
      commandResult({ action: 'audio.output.unmute', outcome: 'refused', reason: 'stale revision' }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Unmute' }))

    const refused = await screen.findByRole('alert')
    expect(refused).toHaveTextContent('Refused: stale revision')
  })

  it('requires a second, distinct click to actually stop a session', async () => {
    stopAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.stop', outcome: 'stopped' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    // The arm click alone must not dispatch anything.
    await userEvent.click(screen.getByRole('button', { name: 'Stop…' }))
    expect(stopAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog', { name: 'Confirm audio session stop' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /Confirm: stop/ }))
    expect(stopAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 1)
    expect(await screen.findByText('Confirmed: stopped')).toBeInTheDocument()
  })

  it('cancelling the armed stop dispatches nothing', async () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Stop…' }))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(stopAudioSession).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('dispatches prepare/start/advance with the computed next revision', async () => {
    prepareAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.prepare', outcome: 'unconfirmable', reason: 'no pipeline backend' }))
    startAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.start', outcome: 'started' }))
    advanceAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.advance', outcome: 'position' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 2 })])

    await userEvent.click(screen.getByRole('button', { name: 'Prepare' }))
    expect(prepareAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 3)
    expect(await screen.findByText('Unconfirmed: no pipeline backend')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(startAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 3)

    await userEvent.click(screen.getByRole('button', { name: 'Advance' }))
    expect(advanceAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 3)
  })

  it('requires a second, distinct click to actually clear a session', async () => {
    clearAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.clear', outcome: 'stopped' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    // The arm click alone must not dispatch anything.
    await userEvent.click(screen.getByRole('button', { name: 'Clear…' }))
    expect(clearAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog', { name: 'Confirm audio session clear' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /Confirm: clear/ }))
    expect(clearAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 1)
    expect(await screen.findByText('Confirmed: stopped')).toBeInTheDocument()
  })

  it('cancelling the armed clear dispatches nothing', async () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Clear…' }))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(clearAudioSession).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('seek refuses an empty position before dispatching, then sends the entered value', async () => {
    seekAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.seek', outcome: 'position' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 7 })])

    await userEvent.click(screen.getByRole('button', { name: 'Seek' }))
    expect(seekAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('Enter a target position in milliseconds.')

    await userEvent.type(screen.getByLabelText('Position (ms)'), '1500')
    await userEvent.click(screen.getByRole('button', { name: 'Seek' }))

    expect(seekAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 8, 1500)
    expect(await screen.findByText('Confirmed: position')).toBeInTheDocument()
  })

  it('seek refuses a negative or non-numeric position without dispatching', async () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.type(screen.getByLabelText('Position (ms)'), '-5')
    await userEvent.click(screen.getByRole('button', { name: 'Seek' }))

    expect(seekAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('Position must be a whole number of milliseconds, 0 or greater.')
  })

  it('gain refuses an empty value before dispatching, then sends the entered linear value', async () => {
    setAudioSessionGain.mockResolvedValue(commandResult({ action: 'audio.gain.set', outcome: 'position' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 1 })])

    await userEvent.click(screen.getByRole('button', { name: 'Set gain' }))
    expect(setAudioSessionGain).not.toHaveBeenCalled()
    expect(screen.getByText('Enter a gain value (linear, not dB).')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Gain (linear, not dB)'), '0.75')
    await userEvent.click(screen.getByRole('button', { name: 'Set gain' }))

    expect(setAudioSessionGain).toHaveBeenCalledWith('media-01', 'session-1', 2, 0.75)
    expect(await screen.findByText('Confirmed: position')).toBeInTheDocument()
  })

  it('gain fade refuses a missing duration without dispatching, then sends both entered values', async () => {
    fadeAudioSessionGain.mockResolvedValue(commandResult({ action: 'audio.gain.fade', outcome: 'unconfirmable', reason: 'no pipeline backend' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 9 })])

    await userEvent.type(screen.getByLabelText('Target gain (linear, not dB)'), '0.5')
    await userEvent.click(screen.getByRole('button', { name: 'Fade' }))
    expect(fadeAudioSessionGain).not.toHaveBeenCalled()
    expect(screen.getByText('Enter a fade duration in milliseconds.')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Duration (ms)'), '2000')
    await userEvent.click(screen.getByRole('button', { name: 'Fade' }))

    expect(fadeAudioSessionGain).toHaveBeenCalledWith('media-01', 'session-1', 10, 0.5, 2000)
    expect(await screen.findByText('Unconfirmed: no pipeline backend')).toBeInTheDocument()
  })

  it('apply refuses text that does not parse as JSON before dispatching anything', async () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.type(screen.getByLabelText(/Params \(JSON/), '{{not valid json')
    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))

    expect(applyAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('not valid JSON')
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('apply requires a second, distinct click, and dispatches the parsed object as params with the computed next revision', async () => {
    applyAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.apply', outcome: 'started' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 6 })])

    await userEvent.type(screen.getByLabelText(/Params \(JSON/), '{{"sourceRole":"primary","media":"track-1"}')
    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))
    expect(applyAudioSession).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog', { name: 'Confirm audio session apply' })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /Confirm: apply/ }))

    expect(applyAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 7, {
      sourceRole: 'primary',
      media: 'track-1',
    })
    expect(await screen.findByText('Confirmed: started')).toBeInTheDocument()
  })

  it('apply sends no params key at all when the field is left empty', async () => {
    applyAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.apply', outcome: 'started' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))
    await userEvent.click(screen.getByRole('button', { name: /Confirm: apply/ }))

    expect(applyAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 1, undefined)
  })

  it('cancelling the armed apply dispatches nothing', async () => {
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(applyAudioSession).not.toHaveBeenCalled()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('renders an apply unconfirmable outcome as unconfirmed with its stated reason, never as success', async () => {
    applyAudioSession.mockResolvedValue(
      commandResult({
        action: 'audio.session.apply',
        outcome: 'unconfirmable',
        reason: 'no pipeline backend',
      }),
    )
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry()])

    await userEvent.click(screen.getByRole('button', { name: 'Apply…' }))
    await userEvent.click(screen.getByRole('button', { name: /Confirm: apply/ }))

    expect(await screen.findByText('Unconfirmed: no pipeline backend')).toBeInTheDocument()
  })
})
