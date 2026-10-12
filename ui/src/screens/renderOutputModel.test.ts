import { describe, expect, it } from 'vitest'
import { describeSurfaceOutput } from './renderOutputModel'

const base = [
  { signal: 'surface.pipeline.state', value: 'running' },
  { signal: 'surface.frames.rate', value: 39.999 },
  { signal: 'surface.content.fseq_filename', value: 'kpop 2026 MH Test.fseq' },
  { signal: 'surface.content.cue_id', value: 'kpop-audio' },
]
const withMode = (mode: string, extra: { signal: string; value: unknown }[] = []) => [...base, { signal: 'surface.output.mode', value: mode }, ...extra]

describe('describeSurfaceOutput', () => {
  it('names the sequence and frame rate only while the surface is drawing content', () => {
    expect(describeSurfaceOutput(withMode('content'))).toEqual({ label: 'Drawing at 40 fps', detail: null, playing: 'kpop 2026 MH Test.fseq' })
  })

  it('falls back to the cue id when no filename is held', () => {
    const obs = withMode('content').filter((o) => o.signal !== 'surface.content.fseq_filename')
    expect(describeSurfaceOutput(obs).playing).toBe('kpop-audio')
  })

  it('presents a held filename as nothing playing while the output is black', () => {
    const out = describeSurfaceOutput(withMode('blackout'))
    expect(out.label).toBe('Held black')
    expect(out.playing).toBeNull()
  })

  it.each([
    ['black', 'Idle, showing black'],
    ['hold', 'Idle, holding the last frame'],
    ['diagnostic', 'Idle, showing the test pattern'],
  ])('says what idle shows for idle mode %s', (idle, label) => {
    const out = describeSurfaceOutput(withMode('idle', [{ signal: 'surface.output.idle_mode', value: idle }]))
    expect(out.label).toBe(label)
    expect(out.playing).toBeNull()
  })

  it('reports a stale mismatch without presenting the file as playing', () => {
    const out = describeSurfaceOutput(withMode('stale'))
    expect(out.label).toBe('Drawing the wrong sequence')
    expect(out.playing).toBeNull()
  })

  it.each([
    ['alert', 'Cannot draw, showing the alert pattern'],
    ['black', 'Cannot draw, showing black'],
  ])('reports a failure that falls back to %s', (failure, label) => {
    const out = describeSurfaceOutput(withMode('failure', [{ signal: 'surface.output.failure', value: failure }]))
    expect(out.label).toBe(label)
    expect(out.playing).toBeNull()
  })

  it('does not claim a drawing state when the output mode is not reported', () => {
    expect(describeSurfaceOutput(base)).toMatchObject({ label: 'Running at 40 fps, output not reported', playing: null })
  })

  it('leads with the pipeline state when it is not running', () => {
    expect(describeSurfaceOutput([{ signal: 'surface.pipeline.state', value: 'failed' }, ...withMode('content')].slice(0, 1))).toMatchObject({ label: 'failed', playing: null })
  })
})
