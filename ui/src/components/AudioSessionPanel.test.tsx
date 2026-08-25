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
const { pauseAudioSession, resumeAudioSession, stopAudioSession, muteAudioSessionOutput, unmuteAudioSessionOutput } =
  vi.hoisted(() => ({
    pauseAudioSession: vi.fn(),
    resumeAudioSession: vi.fn(),
    stopAudioSession: vi.fn(),
    muteAudioSessionOutput: vi.fn(),
    unmuteAudioSessionOutput: vi.fn(),
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
  }
})

afterEach(() => {
  cleanup()
  pauseAudioSession.mockReset()
  resumeAudioSession.mockReset()
  stopAudioSession.mockReset()
  muteAudioSessionOutput.mockReset()
  unmuteAudioSessionOutput.mockReset()
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

  it('renders pause/resume/mute/unmute/stop disabled, never enabled, without audio:command', async () => {
    const model = makeModel({ session: signedIn({ scopes: ['node:read'] }) })
    renderPanel(model, [entry()])
    expect(screen.getByRole('button', { name: 'Pause' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Resume' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Mute' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Unmute' })).toBeDisabled()
    // Stop's arm button is a plain button (the ScopedButton only guards
    // the confirm step), so it stays clickable; arming it must still
    // reach a disabled confirm control.
    await userEvent.click(screen.getByRole('button', { name: 'Stop…' }))
    expect(screen.getByRole('button', { name: /Confirm: stop/ })).toBeDisabled()
  })

  it('dispatches pause and renders a confirmed outcome honestly', async () => {
    pauseAudioSession.mockResolvedValue(commandResult({ action: 'audio.session.pause', outcome: 'position' }))
    const model = makeModel({ session: signedIn() })
    renderPanel(model, [entry({ signal: 'audio_session.desired_revision', value: 4 })])

    await userEvent.click(screen.getByRole('button', { name: 'Pause' }))

    expect(pauseAudioSession).toHaveBeenCalledWith('media-01', 'session-1', 5)
    expect(await screen.findByText('Confirmed: position')).toBeInTheDocument()
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
})
