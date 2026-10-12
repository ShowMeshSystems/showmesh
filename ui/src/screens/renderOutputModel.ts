/** One render observation as the output wording reads it. */
export type OutputObservation = { signal: string; value?: unknown }

export type SurfaceOutput = {
  /** What the surface is putting out, fact first. */
  label: string
  detail: string | null
  /** The sequence the surface is drawing right now; null unless the output mode is content. */
  playing: string | null
}

const IDLE_LABEL: Record<string, string> = {
  black: 'Idle, showing black',
  hold: 'Idle, holding the last frame',
  diagnostic: 'Idle, showing the test pattern',
}

const FAILURE_LABEL: Record<string, string> = {
  alert: 'Cannot draw, showing the alert pattern',
  black: 'Cannot draw, showing black',
}

function read(observations: readonly OutputObservation[], signal: string): unknown {
  return observations.find((entry) => entry.signal === signal)?.value ?? null
}

function text(value: unknown): string | null {
  return typeof value === 'string' && value !== '' ? value : null
}

/** What a surface is really putting out, from its own pipeline state and output mode. */
export function describeSurfaceOutput(observations: readonly OutputObservation[]): SurfaceOutput {
  const pipeline = read(observations, 'surface.pipeline.state')
  const mode = text(read(observations, 'surface.output.mode'))
  const rate = read(observations, 'surface.frames.rate')
  const fps = typeof rate === 'number' ? ` at ${Number(rate.toFixed(2))} fps` : ''
  if (pipeline === null) return { label: 'Unknown', detail: null, playing: null }
  if (pipeline !== 'running') return { label: String(pipeline), detail: null, playing: null }
  switch (mode) {
    case 'content': {
      const playing = text(read(observations, 'surface.content.fseq_filename')) ?? text(read(observations, 'surface.content.cue_id'))
      return { label: `Drawing${fps}`, detail: null, playing }
    }
    case 'blackout':
      return { label: 'Held black', detail: 'Returns with the next cue.', playing: null }
    case 'idle':
      return { label: IDLE_LABEL[text(read(observations, 'surface.output.idle_mode')) ?? ''] ?? 'Idle', detail: null, playing: null }
    case 'stale':
      return { label: 'Drawing the wrong sequence', detail: null, playing: null }
    case 'failure':
      return { label: FAILURE_LABEL[text(read(observations, 'surface.output.failure')) ?? ''] ?? 'Cannot draw', detail: null, playing: null }
    default:
      return { label: `Running${fps}, output not reported`, detail: null, playing: null }
  }
}
