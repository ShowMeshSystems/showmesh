import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  NightSessionDetail,
  buildCues,
  buildPayload,
  emptyForm,
  newCueForm,
  parseNonNegativeInt,
  type FormState,
} from './NightSessionDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// The Night session editor now lives at /shows/:showId/night-sessions/new
// and /shows/:showId/night-sessions/:id, nested in the show workspace
// (ShowWorkspaceFrame), and the `show` field (and every asset's own
// `show` field) is fixed from the route rather than picked with a
// ShowSelect — the same convention ShowCueDetail.tsx and
// ShowPlaylistDetail.tsx already established for every other tab editor
// in this workspace.
const {
  getShow,
  listAssets,
  putNightSessionConfig,
  getNightSessionConfig,
  getNightSessionConfigRevisions,
  listConfigObjects,
} = vi.hoisted(() => ({
  getShow: vi.fn(),
  listAssets: vi.fn(),
  putNightSessionConfig: vi.fn(),
  getNightSessionConfig: vi.fn(),
  getNightSessionConfigRevisions: vi.fn(),
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getShow,
    listAssets,
    putNightSessionConfig,
    getNightSessionConfig,
    getNightSessionConfigRevisions,
    listConfigObjects,
  }
})

const showResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show' as const,
  id: 'halloween-2026',
  revision: 3,
  payload: { name: 'Halloween 2026', notes: '' },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'operator-1',
  source: 'api' as const,
}

beforeEach(() => {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  listConfigObjects.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', kind: 'show.playlist', objects: [] })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const writerSession = makeAuthenticatedSession({ scopes: ['config:write'] })

function renderNew(model: Model = makeModel({ session: writerSession }), showId = 'halloween-2026') {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/${showId}/night-sessions/new`]}>
        <Routes>
          <Route path="/shows/:showId/night-sessions/new" element={<NightSessionDetail isNew />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(model: Model, showId: string, id: string) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/${showId}/night-sessions/${id}`]}>
        <Routes>
          <Route path="/shows/:showId/night-sessions/:id" element={<NightSessionDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

/**
 * Fills every field `buildPayload` requires for an otherwise-valid
 * submission, so each test below only has to touch the ONE field its own
 * validation rule is about. `show` and every asset's `show` are fixed
 * from the route now, so this only has to fill the fields that remain
 * pickable.
 */
async function fillMinimalValidForm(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.type(screen.getByLabelText('Definition id'), 'halloween-night')
  await user.type(screen.getByLabelText('Label'), 'Halloween Night')
  await user.type(screen.getByLabelText('Show playlist FPP instance'), 'fpp-main')
  await user.type(screen.getByLabelText('Show playlist'), 'show-playlist')
  await user.type(screen.getByLabelText('FPP instance'), 'fpp-main')
  await user.type(screen.getByLabelText('Resting playlist'), 'resting-playlist')
  await user.type(screen.getByLabelText('Sequence'), 'resting-seq')
  await user.type(screen.getByLabelText('Target node'), 'node-1')
}

describe('NightSessionDetail (new definition authoring)', () => {
  it('submits a minimal otherwise-valid form with no background audio and no cues, show fixed from the route', async () => {
    putNightSessionConfig.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session',
      id: 'halloween-night',
      revision: 1,
      payload: {},
      updatedAt: '2026-08-22T00:00:00Z',
      createdByPrincipalId: null,
      createdByPrincipalName: null,
      source: 'api',
    })
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)
    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(putNightSessionConfig).toHaveBeenCalledTimes(1))
    const [, payload] = putNightSessionConfig.mock.calls[0] as [string, Record<string, unknown>]
    expect(payload).toMatchObject({ show: 'halloween-2026' })
    expect(payload.resting).toMatchObject({ fppInstanceId: 'fpp-main', playlist: 'resting-playlist' })
    expect(payload.resting).not.toHaveProperty('backgroundAudio')
  })

  // Review finding 11 / duplicate itemId rule.
  it('rejects two background audio items sharing the same item id, without submitting', async () => {
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)

    await user.click(screen.getByLabelText(/Configure background audio/))
    await user.click(screen.getByRole('button', { name: 'Add background audio item' }))

    const itemIdInputs = screen.getAllByPlaceholderText(/item id/)
    const sequenceInputs = screen.getAllByPlaceholderText('sequence')
    const targetInputs = screen.getAllByPlaceholderText('target node')
    for (const [i, input] of itemIdInputs.entries()) {
      await user.type(input, 'dup-id')
      await user.type(sequenceInputs[i]!, `bg-seq-${i}`)
      await user.type(targetInputs[i]!, 'node-1')
    }
    await user.clear(screen.getByLabelText('Ceiling (dB)'))
    await user.type(screen.getByLabelText('Ceiling (dB)'), '-6')

    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(screen.getByText(/used more than once/)).toBeVisible())
    expect(putNightSessionConfig).not.toHaveBeenCalled()
  })

  // Review finding 11 / the crossfade rule, both directions.
  it('rejects itemTransition "crossfade" with no crossfade duration given', async () => {
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)

    await user.click(screen.getByLabelText(/Configure background audio/))
    await user.type(screen.getByPlaceholderText(/item id/), 'item-1')
    await user.type(screen.getByPlaceholderText('sequence'), 'bg-seq')
    await user.type(screen.getByPlaceholderText('target node'), 'node-1')
    await user.selectOptions(screen.getByLabelText('Item transition'), 'crossfade')
    await user.clear(screen.getByLabelText('Ceiling (dB)'))
    await user.type(screen.getByLabelText('Ceiling (dB)'), '-6')
    // Crossfade duration deliberately left blank.

    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(screen.getByText(/crossfade duration is required/i)).toBeVisible())
    expect(putNightSessionConfig).not.toHaveBeenCalled()
  })

  it('rejects a crossfade duration given while itemTransition is not "crossfade"', async () => {
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)

    await user.click(screen.getByLabelText(/Configure background audio/))
    await user.type(screen.getByPlaceholderText(/item id/), 'item-1')
    await user.type(screen.getByPlaceholderText('sequence'), 'bg-seq')
    await user.type(screen.getByPlaceholderText('target node'), 'node-1')
    await user.clear(screen.getByLabelText('Ceiling (dB)'))
    await user.type(screen.getByLabelText('Ceiling (dB)'), '-6')

    // The crossfade-ms field only renders once itemTransition is
    // "crossfade" — select it briefly to type a value, then switch away,
    // reproducing an operator who picked crossfade, entered a duration,
    // and changed their mind.
    await user.selectOptions(screen.getByLabelText('Item transition'), 'crossfade')
    await user.type(screen.getByLabelText('Crossfade (ms)'), '250')
    await user.selectOptions(screen.getByLabelText('Item transition'), 'sequential')

    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(screen.getByText(/only applies when item transition is "crossfade"/)).toBeVisible())
    expect(putNightSessionConfig).not.toHaveBeenCalled()
  })

  // Review finding 11 / the maxGainDb <= 0 rule, including the resolved
  // suspicion: "-Infinity" must be REJECTED as a range violation, not
  // silently accepted and later serialized as `null` on the wire.
  it('rejects a max gain of 5 (a positive value), through the real control', async () => {
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)

    await user.click(screen.getByLabelText(/Configure background audio/))
    await user.type(screen.getByPlaceholderText(/item id/), 'item-1')
    await user.type(screen.getByPlaceholderText('sequence'), 'bg-seq')
    await user.type(screen.getByPlaceholderText('target node'), 'node-1')
    const maxGainInput = screen.getByLabelText('Ceiling (dB)')
    await user.clear(maxGainInput)
    await user.type(maxGainInput, '5')

    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(screen.getByText(/max gain must be a number, 0 dB or lower/i)).toBeVisible())
    expect(putNightSessionConfig).not.toHaveBeenCalled()
  })

  it('accepts a max gain of exactly 0', async () => {
    putNightSessionConfig.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session',
      id: 'halloween-night',
      revision: 1,
      payload: {},
      updatedAt: '2026-08-22T00:00:00Z',
      createdByPrincipalId: null,
      createdByPrincipalName: null,
      source: 'api',
    })
    const user = userEvent.setup()
    renderNew()
    await fillMinimalValidForm(user)

    await user.click(screen.getByLabelText(/Configure background audio/))
    await user.type(screen.getByPlaceholderText(/item id/), 'item-1')
    await user.type(screen.getByPlaceholderText('sequence'), 'bg-seq')
    await user.type(screen.getByPlaceholderText('target node'), 'node-1')
    const maxGainInput = screen.getByLabelText('Ceiling (dB)')
    await user.clear(maxGainInput)
    await user.type(maxGainInput, '0')

    await user.click(screen.getByRole('button', { name: 'Create definition' }))
    await waitFor(() => expect(putNightSessionConfig).toHaveBeenCalledTimes(1))
    const [, payload] = putNightSessionConfig.mock.calls[0] as [string, { resting: { backgroundAudio?: { maxGainDb: number } } }]
    expect(payload.resting.backgroundAudio?.maxGainDb).toBe(0)
  })
})

describe('NightSessionDetail (site control and interlocks are PlannedFeature, not controls)', () => {
  it('stamps site control and interlock authoring as not built, and never renders a working control for either', async () => {
    renderNew()
    expect(await screen.findByText('Site control')).toBeVisible()
    expect(screen.getByText('Interlock authoring')).toBeVisible()
    expect(screen.getAllByText('Not built').length).toBeGreaterThanOrEqual(2)
  })
})

describe('parseNonNegativeInt (pure)', () => {
  it('rejects an empty string', () => {
    expect(parseNonNegativeInt('', 'X')).toEqual({ error: 'X is required.' })
  })

  it('rejects a negative integer', () => {
    expect(parseNonNegativeInt('-1', 'X')).toEqual({ error: 'X must be a whole number, zero or greater.' })
  })

  it('rejects a non-integer', () => {
    expect(parseNonNegativeInt('1.5', 'X')).toEqual({ error: 'X must be a whole number, zero or greater.' })
  })

  it('accepts zero', () => {
    expect(parseNonNegativeInt('0', 'X')).toEqual({ value: 0 })
  })

  it('accepts a positive integer, ignoring surrounding whitespace', () => {
    expect(parseNonNegativeInt(' 250 ', 'X')).toEqual({ value: 250 })
  })
})

describe('buildCues (pure)', () => {
  it('rejects a cue with no name', () => {
    const cue = { ...newCueForm(), action: 'a1' }
    expect(buildCues([cue], 'enterShow')).toEqual({ error: 'enterShow Transition Step 1 needs a name.' })
  })

  it('rejects a cue with no action', () => {
    const cue = { ...newCueForm(), name: 'lights-down' }
    expect(buildCues([cue], 'enterShow')).toEqual({ error: 'enterShow Transition Step "lights-down" needs an action.' })
  })

  it('rejects a non-integer offset', () => {
    const cue = { ...newCueForm(), name: 'lights-down', action: 'a1', offsetMs: '1.5' }
    expect(buildCues([cue], 'enterShow')).toEqual({
      error: 'enterShow Transition Step "lights-down"\'s offset must be a whole number of milliseconds.',
    })
  })

  it('accepts a negative (signed) offset', () => {
    const cue = { ...newCueForm(), name: 'lights-down', action: 'a1', offsetMs: '-500' }
    const result = buildCues([cue], 'enterShow')
    if ('error' in result) throw new Error(result.error)
    expect(result.cues[0]).toMatchObject({ name: 'lights-down', action: 'a1', offsetMs: -500 })
  })

  it('omits fadeDurationMs entirely when left blank, rather than sending 0', () => {
    const cue = { ...newCueForm(), name: 'lights-down', action: 'a1' }
    const result = buildCues([cue], 'enterShow')
    if ('error' in result) throw new Error(result.error)
    expect(result.cues[0]).not.toHaveProperty('fadeDurationMs')
  })
})

describe('buildPayload (pure)', () => {
  function validForm(overrides: Partial<FormState> = {}): FormState {
    return {
      ...emptyForm(),
      show: 'halloween-2026',
      label: 'Halloween Night',
      showPlaylistFppInstanceId: 'fpp-main',
      showPlaylistPlaylist: 'show-playlist',
      restingFppInstanceId: 'fpp-main',
      restingPlaylist: 'resting-playlist',
      restingTimelineShow: 'halloween-2026',
      restingTimelineSequence: 'resting-seq',
      restingTimelineTarget: 'node-1',
      ...overrides,
    }
  }

  it('rejects a missing show', () => {
    expect(buildPayload(validForm({ show: '' }))).toEqual({ error: 'Show is required.' })
  })

  it('builds a valid payload with background audio disabled, omitting backgroundAudio entirely', () => {
    const result = buildPayload(validForm())
    if ('error' in result) throw new Error(result.error)
    expect(result.payload.resting).not.toHaveProperty('backgroundAudio')
  })

  // Review finding 11 / duplicate itemId rule (pure).
  it('rejects two background audio items sharing the same item id', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [
        { itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' },
        { itemId: 'bg-1', show: 'halloween-2026', sequence: 's2', target: 'node-1' },
      ],
      backgroundAudioMaxGainDb: '-6',
    })
    expect(buildPayload(form)).toEqual({ error: 'Background audio item id "bg-1" is used more than once.' })
  })

  // Review finding 11 / the crossfade rule (pure), both directions.
  it('rejects itemTransition "crossfade" with no crossfade duration', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [{ itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' }],
      backgroundAudioItemTransition: 'crossfade',
      backgroundAudioCrossfadeMs: '',
      backgroundAudioMaxGainDb: '-6',
    })
    expect(buildPayload(form)).toEqual({ error: 'Background audio crossfade duration is required.' })
  })

  it('rejects a crossfade duration given while itemTransition is not "crossfade"', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [{ itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' }],
      backgroundAudioItemTransition: 'sequential',
      backgroundAudioCrossfadeMs: '250',
      backgroundAudioMaxGainDb: '-6',
    })
    expect(buildPayload(form)).toEqual({
      error: 'Crossfade duration only applies when item transition is "crossfade". Clear it or pick that transition.',
    })
  })

  // Review finding 11 / the maxGainDb <= 0 rule (pure). The suspicion:
  // `Number('-Infinity')` is `-Infinity`, which is neither NaN nor
  // greater than 0 — a NaN-only check let it through, and
  // `JSON.stringify(-Infinity)` serializes to `null`, which the wire
  // schema's non-nullable `maxGainDb: number` would have rejected as a
  // TYPE error rather than the RANGE error an operator actually caused.
  it('rejects a max gain of -Infinity, confirming the resolved suspicion', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [{ itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' }],
      backgroundAudioMaxGainDb: '-Infinity',
    })
    expect(buildPayload(form)).toEqual({
      error: 'Background audio max gain must be a number, 0 dB or lower.',
    })
  })

  it('rejects a positive max gain', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [{ itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' }],
      backgroundAudioMaxGainDb: '5',
    })
    expect(buildPayload(form)).toEqual({
      error: 'Background audio max gain must be a number, 0 dB or lower.',
    })
  })

  it('accepts a max gain of exactly 0', () => {
    const form = validForm({
      backgroundAudioEnabled: true,
      backgroundAudioItems: [{ itemId: 'bg-1', show: 'halloween-2026', sequence: 's1', target: 'node-1' }],
      backgroundAudioMaxGainDb: '0',
    })
    const result = buildPayload(form)
    if ('error' in result) throw new Error(result.error)
    expect(result.payload.resting.backgroundAudio?.maxGainDb).toBe(0)
  })
})

// Readability seam: this screen mixes what the coordinator reports
// (active revision, revision history) with what the operator edits (the
// authoring fieldset). This asserts the split exists and that the status
// line an operator scans first is visible without opening anything,
// while the long, rarely-consulted revision history starts collapsed
// behind a <details>.
describe('NightSessionDetail (status split from authoring)', () => {
  const nightSessionPayload = {
    show: 'halloween-2026',
    label: 'Halloween Night',
    showPlaylist: { fppInstanceId: 'fpp-main', playlist: 'show-playlist' },
    resting: {
      fppInstanceId: 'fpp-main',
      playlist: 'resting-playlist',
      endOfNightPlaylist: 'resting-playlist',
      endOfNightRepeat: false,
      timelineAsset: { show: 'halloween-2026', sequence: 'resting-seq', target: 'node-1' },
    },
    enterShow: { cues: [], blackoutHoldMs: 0 },
    enterResting: { cues: [], blackoutAfterShowMs: 0 },
  }

  it('shows the active-revision status without opening anything, and starts revision history collapsed', async () => {
    getNightSessionConfig.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'night.session',
      id: 'halloween-night',
      revision: 3,
      payload: nightSessionPayload,
      updatedAt: '2026-08-25T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    getNightSessionConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'night.session',
      revisions: [
        { revision: 3, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true },
        { revision: 2, createdAt: '2026-08-20T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: false },
      ],
    })
    renderExisting(makeModel({ session: writerSession }), 'halloween-2026', 'halloween-night')

    const status = await screen.findByText(/Active revision/)
    expect(status).toBeVisible()
    expect(screen.getByText(/3/, { selector: '.ruled-strip__explanation' })).toBeVisible()

    const summary = screen.getByText('Revision history')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    expect(screen.getByRole('heading', { name: 'Halloween Night' })).toBeVisible() // sanity: the page itself did render
    const revisionRow = screen.getAllByText('admin-1', { selector: 'td' })[0]!
    expect(revisionRow).not.toBeVisible()

    await userEvent.setup().click(summary)
    expect(revisionRow).toBeVisible()
  })
})
