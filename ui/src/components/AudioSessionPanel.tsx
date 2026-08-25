import { useState } from 'react'
import {
  muteAudioSessionOutput,
  pauseAudioSession,
  resumeAudioSession,
  stopAudioSession,
  unmuteAudioSessionOutput,
} from '../api'
import type { AudioSessionCommandResult, ObservationEntry } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../app/session'
import { EvidenceValue } from './EvidenceValue'
import { ScopedButton } from './ScopedButton'

// The first slice of audio dispatch (the render panel's own sibling):
// the controls an operator reaches for when something is audibly wrong
// mid-show -- pause, resume, stop, and output mute/unmute. Apply,
// prepare, start, clear, seek, advance, and gain are a later slice.
const AUDIO_COMMAND_SCOPE = 'audio:command'

// Node.audio (api/openapi.yaml) is a flat ObservationEntry[] spanning
// BOTH node-scoped node.audio.* signals (resource.kind "node") and
// per-session audio_session.* signals (resource.kind "audio_session",
// one resource id per session) -- see nodeaudio/signals.go's own two
// signal vocabularies. This panel groups only the audio_session entries,
// since a session's identity and playback/output state, not the node's
// own engine/device telemetry, is what these five controls act on.
const SESSION_RESOURCE_KIND = 'audio_session'

// nodeaudio.SignalSessionDesiredRevision: the session's own
// per-session revision ledger. Mirrors cmd_audio_session.go's
// currentAudioSessionDesiredRevision -- an unset/absent/non-numeric
// desired-revision evidence reads as "this session's revision space has
// never been touched", so the next dispatch carries 1; otherwise it
// carries the last-observed value plus one, since a revision that does
// not strictly exceed the session's current one is refused rather than
// applied out of order.
const SIGNAL_SESSION_DESIRED_REVISION = 'audio_session.desired_revision'

function nextRevision(entries: ObservationEntry[]): number {
  const entry = entries.find((e) => e.signal === SIGNAL_SESSION_DESIRED_REVISION)
  if (entry === undefined || typeof entry.value !== 'number') return 1
  return entry.value + 1
}

export interface AudioSessionPanelProps {
  nodeId: string
  entries: ObservationEntry[]
}

export function AudioSessionPanel({ nodeId, entries }: AudioSessionPanelProps) {
  const model = useModelContext()
  const connected = model.connection.kind === 'live'

  const bySession = new Map<string, ObservationEntry[]>()
  const order: string[] = []
  for (const e of entries) {
    if (e.resource.kind !== SESSION_RESOURCE_KIND) continue
    const id = e.resource.id
    if (!bySession.has(id)) {
      bySession.set(id, [])
      order.push(id)
    }
    bySession.get(id)!.push(e)
  }

  if (order.length === 0) {
    return (
      <p className="text-muted" role="status">
        This node has no audio session evidence -- no session has ever been dispatched to it,
        or its agent has not reported in yet.
      </p>
    )
  }

  return (
    <>
      {order.map((sessionId) => {
        const sessionEntries = bySession.get(sessionId)!
        return (
          <div key={sessionId} className="audio-session">
            <h4 className="panel__subtitle">Session: {sessionId}</h4>
            {sessionEntries.map((entry) => (
              <EvidenceValue
                key={entry.signal}
                label={entry.signal}
                evidence={entry}
                serverTime={model.serverTime}
                serverTimeReceivedAt={model.serverTimeReceivedAt}
                connected={connected}
              />
            ))}
            <AudioSessionControls nodeId={nodeId} sessionId={sessionId} entries={sessionEntries} />
          </div>
        )
      })}
    </>
  )
}

type Verb = 'pause' | 'resume' | 'mute' | 'unmute'

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting'; verb: Verb }
  | { kind: 'result'; verb: Verb; result: AudioSessionCommandResult }
  | { kind: 'error'; verb: Verb; message: string }

// AudioSessionControls holds pause/resume/mute/unmute plus the stop
// arm-then-confirm pair for one session. Stop is kept as its own
// separate state, not folded into CallState above, matching
// ShowActive.tsx/NightSessionActive.tsx's own "the sharp control is a
// distinct piece of state from the ordinary ones" shape: a second,
// distinct click actually submits it, and the confirmation panel itself
// carries any refusal so the operator is not made to re-arm just to see
// why stop failed.
function AudioSessionControls({
  nodeId,
  sessionId,
  entries,
}: {
  nodeId: string
  sessionId: string
  entries: ObservationEntry[]
}) {
  const [state, setState] = useState<CallState>({ kind: 'idle' })

  const [stopArmed, setStopArmed] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [stopError, setStopError] = useState<string | null>(null)
  const [stopResult, setStopResult] = useState<AudioSessionCommandResult | null>(null)

  async function run(verb: Verb): Promise<void> {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting', verb })
    try {
      const revision = nextRevision(entries)
      const result =
        verb === 'pause'
          ? await pauseAudioSession(nodeId, sessionId, revision)
          : verb === 'resume'
            ? await resumeAudioSession(nodeId, sessionId, revision)
            : verb === 'mute'
              ? await muteAudioSessionOutput(nodeId, sessionId, revision)
              : await unmuteAudioSessionOutput(nodeId, sessionId, revision)
      setState({ kind: 'result', verb, result })
    } catch (err) {
      setState({ kind: 'error', verb, message: describeApiError(err) })
    }
  }

  function armStop(): void {
    setStopError(null)
    setStopArmed(true)
  }

  function cancelStop(): void {
    setStopArmed(false)
    setStopError(null)
  }

  async function confirmStop(): Promise<void> {
    if (stopping) return
    setStopping(true)
    setStopError(null)
    try {
      const revision = nextRevision(entries)
      const result = await stopAudioSession(nodeId, sessionId, revision)
      setStopResult(result)
      setStopArmed(false)
    } catch (err) {
      // Deliberately does not dismiss the confirmation panel: the operator
      // asked to stop THIS session and the refusal is about that request,
      // not a reason to make them re-arm it.
      setStopError(describeApiError(err))
    } finally {
      setStopping(false)
    }
  }

  const submitting = state.kind === 'submitting'

  return (
    <div className="audio-session__controls">
      <div className="audio-session__buttons">
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('pause')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'pause'}
          busyReason="Pausing…"
        >
          Pause
        </ScopedButton>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('resume')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'resume'}
          busyReason="Resuming…"
        >
          Resume
        </ScopedButton>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('mute')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'mute'}
          busyReason="Muting…"
        >
          Mute
        </ScopedButton>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('unmute')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'unmute'}
          busyReason="Unmuting…"
        >
          Unmute
        </ScopedButton>
        {!stopArmed && (
          <button type="button" onClick={armStop} disabled={stopping}>
            Stop…
          </button>
        )}
      </div>

      {state.kind === 'result' && <AudioCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="audio-session__error">
          {state.message}
        </p>
      )}

      {/* Stop is destructive to a running show's audio: a picked control
          only ARMS this panel, which states what is about to happen. A
          second, distinct click (below) actually submits it. */}
      {stopArmed && (
        <div className="panel panel--warning" role="alertdialog" aria-label="Confirm audio session stop">
          <p>
            <strong>About to stop session &ldquo;{sessionId}&rdquo;.</strong>
          </p>
          <p>
            This commands the session to stop, permanently distinguishable in evidence from a
            natural end-of-item completion.
          </p>
          {stopError !== null && (
            <p role="alert" className="audio-session__error">
              {stopError}
            </p>
          )}
          <div style={{ display: 'flex', gap: '0.75rem' }}>
            <ScopedButton
              requiredScope={AUDIO_COMMAND_SCOPE}
              onClick={() => void confirmStop()}
              busy={stopping}
              busyReason="Stopping…"
            >
              {stopping ? 'Stopping…' : `Confirm: stop "${sessionId}"`}
            </ScopedButton>
            <button type="button" onClick={cancelStop} disabled={stopping}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {stopResult !== null && <AudioCommandOutcome result={stopResult} />}
    </div>
  )
}

// AudioCommandOutcome renders result.outcome literally, never inferring
// success from a bare 200 (ADR-003). Unlike RenderCommandResult's
// "confirmed"/"unconfirmed" pair, AudioSessionCommandResult's outcome
// vocabulary is action-specific ("started"/"position"/"stopped"/
// "completed" are the confirmed outcomes this endpoint family can
// report) plus "refused"/"failed" (never dispatched to the node) and
// "unconfirmable" (dispatched, but the node could not corroborate it) --
// see AudioSessionCommandResult.outcome's own description.
const CONFIRMED_AUDIO_OUTCOMES = new Set(['started', 'position', 'stopped', 'completed'])

function AudioCommandOutcome({ result }: { result: AudioSessionCommandResult }) {
  return (
    <div role="status">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used). Nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {CONFIRMED_AUDIO_OUTCOMES.has(result.outcome) && (
        <p className="audio-session__confirmed">Confirmed: {result.outcome}</p>
      )}
      {result.outcome === 'unconfirmable' && (
        <p role="alert" className="audio-session__unconfirmed">
          Unconfirmed: {result.reason}
        </p>
      )}
      {(result.outcome === 'refused' || result.outcome === 'failed') && (
        <p role="alert" className="audio-session__error">
          {result.outcome === 'refused' ? 'Refused' : 'Failed'}: {result.reason}
        </p>
      )}
      {result.outcome === '' && <p className="text-muted">Pending: this command has not yet resolved.</p>}
    </div>
  )
}
