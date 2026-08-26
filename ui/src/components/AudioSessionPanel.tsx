import { useState } from 'react'
import {
  advanceAudioSession,
  applyAudioSession,
  clearAudioSession,
  fadeAudioSessionGain,
  muteAudioSessionOutput,
  pauseAudioSession,
  prepareAudioSession,
  resumeAudioSession,
  seekAudioSession,
  setAudioSessionGain,
  startAudioSession,
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
//
// This second slice adds prepare/start/advance/clear (no operator
// parameters) and seek/gain/gain.fade (each carrying the parameters
// openapi.yaml's own operation descriptions name -- positionMs, gain,
// targetGain/durationMs), plus apply. apply's own params are a full
// session definition (sourceRole/media/playlist/outputs/mixPolicy) with
// no per-field schema in openapi.yaml -- but showmeshctl (cmd_audio_
// session.go) takes that same params body as a raw JSON positional
// argument it never validates either, so a JSON textarea here is exact
// parity with the CLI, not a degraded stand-in for a form nobody has
// designed. This panel validates that the JSON parses before dispatch,
// which the CLI does not.
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

type Verb = 'pause' | 'resume' | 'mute' | 'unmute' | 'prepare' | 'start' | 'advance'

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting'; verb: Verb }
  | { kind: 'result'; verb: Verb; result: AudioSessionCommandResult }
  | { kind: 'error'; verb: Verb; message: string }

// AudioSessionControls holds pause/resume/mute/unmute/prepare/start/
// advance plus the stop and clear arm-then-confirm pairs for one
// session. Stop and clear are each kept as their own separate state, not
// folded into CallState above, matching ShowActive.tsx/
// NightSessionActive.tsx's own "the sharp control is a distinct piece of
// state from the ordinary ones" shape: a second, distinct click actually
// submits it, and the confirmation panel itself carries any refusal so
// the operator is not made to re-arm just to see why the command failed.
// clear gets the same treatment as stop because it is equally
// irreversible from the operator's seat -- clear releases the session
// and its loaded content on the node and removes its persisted record
// entirely (openapi.yaml), where stop only halts playback of an
// otherwise still-loaded session.
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

  const [clearArmed, setClearArmed] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [clearError, setClearError] = useState<string | null>(null)
  const [clearResult, setClearResult] = useState<AudioSessionCommandResult | null>(null)

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
              : verb === 'unmute'
                ? await unmuteAudioSessionOutput(nodeId, sessionId, revision)
                : verb === 'prepare'
                  ? await prepareAudioSession(nodeId, sessionId, revision)
                  : verb === 'start'
                    ? await startAudioSession(nodeId, sessionId, revision)
                    : await advanceAudioSession(nodeId, sessionId, revision)
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

  function armClear(): void {
    setClearError(null)
    setClearArmed(true)
  }

  function cancelClear(): void {
    setClearArmed(false)
    setClearError(null)
  }

  async function confirmClear(): Promise<void> {
    if (clearing) return
    setClearing(true)
    setClearError(null)
    try {
      const revision = nextRevision(entries)
      const result = await clearAudioSession(nodeId, sessionId, revision)
      setClearResult(result)
      setClearArmed(false)
    } catch (err) {
      setClearError(describeApiError(err))
    } finally {
      setClearing(false)
    }
  }

  const submitting = state.kind === 'submitting'

  return (
    <div className="audio-session__controls">
      <div className="audio-session__buttons">
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('prepare')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'prepare'}
          busyReason="Preparing…"
        >
          Prepare
        </ScopedButton>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void run('start')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'start'}
          busyReason="Starting…"
        >
          Start
        </ScopedButton>
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
          onClick={() => void run('advance')}
          busy={submitting && state.kind === 'submitting' && state.verb === 'advance'}
          busyReason="Advancing…"
        >
          Advance
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
        {!clearArmed && (
          <button type="button" onClick={armClear} disabled={clearing}>
            Clear…
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

      {/* Clear is more destructive than stop: it releases the session and
          its loaded content on the node and removes its persisted record
          entirely, not merely halting playback. Same arm-then-confirm
          shape as stop, for the same reason. */}
      {clearArmed && (
        <div className="panel panel--warning" role="alertdialog" aria-label="Confirm audio session clear">
          <p>
            <strong>About to clear session &ldquo;{sessionId}&rdquo;.</strong>
          </p>
          <p>
            This releases the session and its loaded content on the node and removes its
            persisted record entirely. This is not undoable.
          </p>
          {clearError !== null && (
            <p role="alert" className="audio-session__error">
              {clearError}
            </p>
          )}
          <div style={{ display: 'flex', gap: '0.75rem' }}>
            <ScopedButton
              requiredScope={AUDIO_COMMAND_SCOPE}
              onClick={() => void confirmClear()}
              busy={clearing}
              busyReason="Clearing…"
            >
              {clearing ? 'Clearing…' : `Confirm: clear "${sessionId}"`}
            </ScopedButton>
            <button type="button" onClick={cancelClear} disabled={clearing}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {clearResult !== null && <AudioCommandOutcome result={clearResult} />}

      {/* Parameterised commands each get their own content-sized group in
          a wrapping row (.command-groups/.command-group, global.css),
          matching FPPDetail.tsx's "an operator hunting for one control is
          not made to scroll past the others" convention, rather than
          stacking full-width inputs down the page. */}
      <div className="command-groups">
        <div className="command-group">
          <h4 className="panel__title">Seek</h4>
          <SeekControl nodeId={nodeId} sessionId={sessionId} entries={entries} />
        </div>
        <div className="command-group">
          <h4 className="panel__title">Gain</h4>
          <GainControl nodeId={nodeId} sessionId={sessionId} entries={entries} />
        </div>
        <div className="command-group">
          <h4 className="panel__title">Gain fade</h4>
          <GainFadeControl nodeId={nodeId} sessionId={sessionId} entries={entries} />
        </div>
        <div className="command-group">
          <h4 className="panel__title">Apply</h4>
          <ApplyControl nodeId={nodeId} sessionId={sessionId} entries={entries} />
        </div>
      </div>
    </div>
  )
}

type ParamCallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: AudioSessionCommandResult }
  | { kind: 'error'; message: string }

// SeekControl re-anchors the session's current item's position.
// openapi.yaml: "params.positionMs names the target position in
// milliseconds" -- a non-negative whole number, validated client-side
// before anything leaves the browser (FPPSetVolumeControl.tsx's own
// "refuse, don't clamp or coerce" posture), never sent as a bare string.
function SeekControl({
  nodeId,
  sessionId,
  entries,
}: {
  nodeId: string
  sessionId: string
  entries: ObservationEntry[]
}) {
  const [raw, setRaw] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const [state, setState] = useState<ParamCallState>({ kind: 'idle' })
  const submitting = state.kind === 'submitting'

  function parsePositionMs(): number | null {
    const trimmed = raw.trim()
    if (trimmed === '') {
      setValidationError('Enter a target position in milliseconds.')
      return null
    }
    const n = Number(trimmed)
    if (!Number.isFinite(n) || !Number.isInteger(n) || n < 0) {
      setValidationError(`"${raw}" is not valid. Position must be a whole number of milliseconds, 0 or greater.`)
      return null
    }
    setValidationError(null)
    return n
  }

  async function handleClick(): Promise<void> {
    if (submitting) return
    const positionMs = parsePositionMs()
    if (positionMs === null) return
    setState({ kind: 'submitting' })
    try {
      const result = await seekAudioSession(nodeId, sessionId, nextRevision(entries), positionMs)
      setState({ kind: 'result', result })
    } catch (err) {
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <div>
      <label>
        Position (ms){' '}
        <input
          type="number"
          min={0}
          step={1}
          value={raw}
          disabled={submitting}
          onChange={(e) => setRaw(e.target.value)}
        />
      </label>
      {validationError !== null && (
        <p role="alert" className="audio-session__error">
          {validationError}
        </p>
      )}
      <div>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void handleClick()}
          busy={submitting}
          busyReason="Seeking…"
        >
          Seek
        </ScopedButton>
      </div>
      {state.kind === 'result' && <AudioCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="audio-session__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// GainControl sets the session's configured gain. openapi.yaml states
// this plainly: "params.gain is linear, not dB - see AUDIO-ENGINE.md".
// No numeric range is given in the schema -- the node clamps to the
// session's own ceiling and reports the clamp as evidence rather than
// silently applying it -- so this control only refuses a non-numeric or
// negative entry, leaving the actual ceiling to the node.
function GainControl({
  nodeId,
  sessionId,
  entries,
}: {
  nodeId: string
  sessionId: string
  entries: ObservationEntry[]
}) {
  const [raw, setRaw] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const [state, setState] = useState<ParamCallState>({ kind: 'idle' })
  const submitting = state.kind === 'submitting'

  function parseGain(): number | null {
    const trimmed = raw.trim()
    if (trimmed === '') {
      setValidationError('Enter a gain value (linear, not dB).')
      return null
    }
    const n = Number(trimmed)
    if (!Number.isFinite(n) || n < 0) {
      setValidationError(`"${raw}" is not valid. Gain must be a non-negative number (linear, not dB).`)
      return null
    }
    setValidationError(null)
    return n
  }

  async function handleClick(): Promise<void> {
    if (submitting) return
    const gain = parseGain()
    if (gain === null) return
    setState({ kind: 'submitting' })
    try {
      const result = await setAudioSessionGain(nodeId, sessionId, nextRevision(entries), gain)
      setState({ kind: 'result', result })
    } catch (err) {
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <div>
      <label>
        Gain (linear, not dB){' '}
        <input
          type="number"
          min={0}
          step="any"
          value={raw}
          disabled={submitting}
          onChange={(e) => setRaw(e.target.value)}
        />
      </label>
      {validationError !== null && (
        <p role="alert" className="audio-session__error">
          {validationError}
        </p>
      )}
      <div>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void handleClick()}
          busy={submitting}
          busyReason="Setting gain…"
        >
          Set gain
        </ScopedButton>
      </div>
      {state.kind === 'result' && <AudioCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="audio-session__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// GainFadeControl sets the configured gain to params.targetGain
// (linear, not dB, same as GainControl above) immediately, then ramps
// toward it over params.durationMs. params.curve is sent as "linear" --
// the only curve the node ships (openapi.yaml) -- rather than offered as
// a one-option control with nothing to actually choose.
function GainFadeControl({
  nodeId,
  sessionId,
  entries,
}: {
  nodeId: string
  sessionId: string
  entries: ObservationEntry[]
}) {
  const [rawGain, setRawGain] = useState('')
  const [rawDuration, setRawDuration] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const [state, setState] = useState<ParamCallState>({ kind: 'idle' })
  const submitting = state.kind === 'submitting'

  function parseInputs(): { targetGain: number; durationMs: number } | null {
    const trimmedGain = rawGain.trim()
    if (trimmedGain === '') {
      setValidationError('Enter a target gain value (linear, not dB).')
      return null
    }
    const targetGain = Number(trimmedGain)
    if (!Number.isFinite(targetGain) || targetGain < 0) {
      setValidationError(`"${rawGain}" is not valid. Target gain must be a non-negative number (linear, not dB).`)
      return null
    }
    const trimmedDuration = rawDuration.trim()
    if (trimmedDuration === '') {
      setValidationError('Enter a fade duration in milliseconds.')
      return null
    }
    const durationMs = Number(trimmedDuration)
    if (!Number.isFinite(durationMs) || !Number.isInteger(durationMs) || durationMs < 0) {
      setValidationError(
        `"${rawDuration}" is not valid. Duration must be a whole number of milliseconds, 0 or greater.`,
      )
      return null
    }
    setValidationError(null)
    return { targetGain, durationMs }
  }

  async function handleClick(): Promise<void> {
    if (submitting) return
    const parsed = parseInputs()
    if (parsed === null) return
    setState({ kind: 'submitting' })
    try {
      const result = await fadeAudioSessionGain(
        nodeId,
        sessionId,
        nextRevision(entries),
        parsed.targetGain,
        parsed.durationMs,
      )
      setState({ kind: 'result', result })
    } catch (err) {
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <div>
      <label>
        Target gain (linear, not dB){' '}
        <input
          type="number"
          min={0}
          step="any"
          value={rawGain}
          disabled={submitting}
          onChange={(e) => setRawGain(e.target.value)}
        />
      </label>
      <br />
      <label>
        Duration (ms){' '}
        <input
          type="number"
          min={0}
          step={1}
          value={rawDuration}
          disabled={submitting}
          onChange={(e) => setRawDuration(e.target.value)}
        />
      </label>
      {validationError !== null && (
        <p role="alert" className="audio-session__error">
          {validationError}
        </p>
      )}
      <div>
        <ScopedButton
          requiredScope={AUDIO_COMMAND_SCOPE}
          onClick={() => void handleClick()}
          busy={submitting}
          busyReason="Fading…"
        >
          Fade
        </ScopedButton>
      </div>
      {state.kind === 'result' && <AudioCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="audio-session__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// ApplyControl is the only audio verb whose params are a full session
// definition rather than a few named fields -- openapi.yaml gives it no
// per-field schema (sourceRole/media/playlist/outputs/mixPolicy are
// named only in prose), and showmeshctl itself (cmd_audio_session.go)
// takes that same body as a raw JSON positional argument it passes
// through verbatim without validating it. A JSON textarea here is
// therefore exact parity with the CLI, not a degraded stand-in for a
// form nobody has designed -- and it does better than the CLI by
// refusing text that does not even parse as JSON before anything is
// sent. An empty field sends no params at all, matching what the CLI
// does when its own positional argument is omitted.
//
// Apply gets the same arm-then-confirm treatment as stop and clear:
// unlike seek/gain/gain.fade, which only adjust an already-running
// session's position or level, apply can replace what the session is
// playing (its source, media, playlist, outputs, and mix policy) in one
// call, so a single click is not enough to submit it.
function ApplyControl({
  nodeId,
  sessionId,
  entries,
}: {
  nodeId: string
  sessionId: string
  entries: ObservationEntry[]
}) {
  const [raw, setRaw] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const [armed, setArmed] = useState(false)
  const [armedParams, setArmedParams] = useState<Record<string, unknown> | undefined>(undefined)
  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [applyResult, setApplyResult] = useState<AudioSessionCommandResult | null>(null)

  function parseParams(): { params?: Record<string, unknown> } | null {
    const trimmed = raw.trim()
    if (trimmed === '') {
      setValidationError(null)
      return {}
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(trimmed)
    } catch {
      setValidationError(
        'This is not valid JSON. Enter the same params body "showmeshctl audio session apply" takes, or leave this empty to apply with no params.',
      )
      return null
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      setValidationError('Params must be a JSON object (the same shape "showmeshctl audio session apply" takes).')
      return null
    }
    setValidationError(null)
    return { params: parsed as Record<string, unknown> }
  }

  function armApply(): void {
    setApplyError(null)
    const parsed = parseParams()
    if (parsed === null) return
    setArmedParams(parsed.params)
    setArmed(true)
  }

  function cancelApply(): void {
    setArmed(false)
    setApplyError(null)
  }

  async function confirmApply(): Promise<void> {
    if (applying) return
    setApplying(true)
    setApplyError(null)
    try {
      const result = await applyAudioSession(nodeId, sessionId, nextRevision(entries), armedParams)
      setApplyResult(result)
      setArmed(false)
    } catch (err) {
      setApplyError(describeApiError(err))
    } finally {
      setApplying(false)
    }
  }

  return (
    <div>
      <label>
        Params (JSON, same body "showmeshctl audio session apply" takes){' '}
        <textarea
          rows={4}
          value={raw}
          disabled={armed || applying}
          onChange={(e) => setRaw(e.target.value)}
        />
      </label>
      <p className="text-muted">
        Fields apply accepts: sourceRole, media, playlist, outputs, mixPolicy. The coordinator and node
        validate this shape, not this screen. Leave this empty to send no params at all, the same as
        leaving the CLI's own params-json argument off.
      </p>
      {validationError !== null && (
        <p role="alert" className="audio-session__error">
          {validationError}
        </p>
      )}
      {!armed && (
        <div>
          <button type="button" onClick={armApply} disabled={applying}>
            Apply…
          </button>
        </div>
      )}
      {applyResult !== null && <AudioCommandOutcome result={applyResult} />}

      {armed && (
        <div className="panel panel--warning" role="alertdialog" aria-label="Confirm audio session apply">
          <p>
            <strong>About to apply to session &ldquo;{sessionId}&rdquo;.</strong>
          </p>
          <p>
            {armedParams === undefined
              ? 'No params were entered; this dispatches apply with no params.'
              : "This merges the entered params onto this session's desired state, creating it if it does not already exist."}
          </p>
          {applyError !== null && (
            <p role="alert" className="audio-session__error">
              {applyError}
            </p>
          )}
          <div style={{ display: 'flex', gap: '0.75rem' }}>
            <ScopedButton
              requiredScope={AUDIO_COMMAND_SCOPE}
              onClick={() => void confirmApply()}
              busy={applying}
              busyReason="Applying…"
            >
              {applying ? 'Applying…' : `Confirm: apply to "${sessionId}"`}
            </ScopedButton>
            <button type="button" onClick={cancelApply} disabled={applying}>
              Cancel
            </button>
          </div>
        </div>
      )}
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
      {result.attributionDegraded && (
        <p className="text-muted">
          Note: the coordinator could not record this command in its audit log; it ran
          anyway.
        </p>
      )}
    </div>
  )
}
