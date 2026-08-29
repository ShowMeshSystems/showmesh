import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ShowActionDetail } from './ShowActionDetail'
import { ModelContext } from '../app/ModelContext'
import {
  makeModel,
  makeResolumeCompositionClip,
  makeResolumeCompositionDeck,
  makeResolumeCompositionLayer,
  makeResolumeCompositionResponse,
  makeShowList,
} from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ForbiddenError } from '../api/errors'
import type { Model } from '../app/types'

const { putShowAction, getResolumeComposition, getShowAction, getShowActionRevisions, getActionBinding, listConfigObjects } =
  vi.hoisted(() => ({
    putShowAction: vi.fn(),
    getResolumeComposition: vi.fn(),
    getShowAction: vi.fn(),
    getShowActionRevisions: vi.fn(),
    getActionBinding: vi.fn(),
    listConfigObjects: vi.fn(),
  }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    putShowAction,
    getResolumeComposition,
    getShowAction,
    getShowActionRevisions,
    getActionBinding,
    listConfigObjects,
  }
})

beforeEach(() => {
  listConfigObjects.mockResolvedValue(makeShowList(['halloween-2026']))
})

afterEach(() => {
  cleanup()
  putShowAction.mockReset()
  getResolumeComposition.mockReset()
  getShowAction.mockReset()
  getShowActionRevisions.mockReset()
  getActionBinding.mockReset()
  listConfigObjects.mockReset()
})

function renderNewAction(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/actions/new']}>
        <ShowActionDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExistingAction(model: Model, id: string) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/actions/${id}`]}>
        <Routes>
          <Route path="/actions/:id" element={<ShowActionDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

describe('ShowActionDetail (new action authoring)', () => {
  // STEP-9-SPEC.md section 5.3: "safetyClass is required and is never
  // defaulted in the UI." The select's first option is a disabled
  // placeholder, so nothing is pre-selected — an operator who never
  // touches this field cannot submit a "none" they never chose.
  it('never pre-selects a safety class — the select starts on the disabled placeholder', () => {
    renderNewAction(makeModel({ session: adminSession }))
    const select = screen.getByLabelText('Safety class') as HTMLSelectElement
    expect(select.value).toBe('')
  })

  it('refuses to submit with no safety class chosen, even with every other field filled in', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.type(screen.getByLabelText('FPP instance id'), 'fpp-main')
    await user.selectOptions(screen.getByLabelText('Primitive'), 'startPlaylist')
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/is required and is never defaulted/)
    expect(putShowAction).not.toHaveBeenCalled()
  })

  it('requires a broker with no default for an MQTT action', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    // Broker left blank on purpose.
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(await screen.findByText(/Broker is required/)).toBeVisible()
    expect(putShowAction).not.toHaveBeenCalled()
  })

  it('submits a valid MQTT action with an explicit safety class and broker', async () => {
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      kind: 'show.action',
      id: 'projectors-on',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Projectors on',
        description: '',
        safetyClass: 'powerOff',
        target: { integration: 'mqtt', broker: 'home-automation', publish: { topic: 't', payload: '', qos: 1, retain: false }, expect: { kind: 'none' } },
      },
      updatedAt: '2026-08-14T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Broker/), 'home-automation')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(putShowAction).toHaveBeenCalledWith(
      'projectors-on',
      expect.objectContaining({
        safetyClass: 'powerOff',
        target: expect.objectContaining({ integration: 'mqtt', broker: 'home-automation' }),
      }),
    )
  })

  // This task's finding 4: the server (decodeMQTTExpect,
  // internal/coordinator/config/showaction.go) requires "match"'s value
  // KEY present but explicitly allows it to be an empty string — an
  // empty match target is a real, valid value, not "no value". This form
  // used to refuse to submit with the match value left blank, which was
  // stricter than the server it must only mirror (ADR-030) and made a
  // stored revision with an empty match value impossible to re-save.
  it('accepts an empty "match" value — the server allows it, so this form must not refuse it', async () => {
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-14T00:00:00Z',
      kind: 'show.action',
      id: 'projectors-on',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Projectors on',
        description: '',
        safetyClass: 'powerOff',
        target: {
          integration: 'mqtt',
          broker: 'home-automation',
          publish: { topic: 't', payload: '', qos: 1, retain: false },
          expect: { kind: 'match', topic: 'home/projectors/state', value: '', deadlineSeconds: 5 },
        },
      },
      updatedAt: '2026-08-14T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'projectors-on')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Projectors on')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'powerOff')
    await user.selectOptions(screen.getByLabelText('Integration'), 'mqtt')
    await user.type(screen.getByLabelText(/Broker/), 'home-automation')
    await user.type(screen.getByLabelText(/Publish topic/), 'home/projectors/set')
    await user.selectOptions(screen.getByLabelText(/QoS/), '1')
    await user.selectOptions(screen.getByLabelText('Expected response'), 'match')
    await user.type(screen.getByLabelText(/Response topic/), 'home/projectors/state')
    await user.type(screen.getByLabelText(/seconds/i), '5')
    // "Exact value to match" left blank on purpose — the case under test.
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(screen.queryByRole('alert')).toBeNull()
    expect(putShowAction).toHaveBeenCalledWith(
      'projectors-on',
      expect.objectContaining({
        target: expect.objectContaining({
          expect: { kind: 'match', topic: 'home/projectors/state', value: '', deadlineSeconds: 5 },
        }),
      }),
    )
  })
})

describe('ShowActionDetail (Resolume authoring)', () => {
  // Review finding 7: useResolumeComposition('show-action-detail') used
  // to fire unconditionally on mount, issuing a config:write-gated
  // request for a form that might never touch Resolume at all.
  it('does not fetch the stored composition until the resolume integration is selected', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'launch-snow')
    expect(getResolumeComposition).not.toHaveBeenCalled()

    getResolumeComposition.mockResolvedValue(makeResolumeCompositionResponse())
    await user.selectOptions(screen.getByLabelText('Integration'), 'resolume')

    await waitFor(() => expect(getResolumeComposition).toHaveBeenCalledTimes(1))
  })

  // Review finding 7: a non-admin (or any 403) used to see four empty
  // "Choose one" dropdowns and no explanation at all — blank read as fine.
  it('states the reason instead of leaving the pickers blank when the composition read is forbidden', async () => {
    getResolumeComposition.mockRejectedValue(new ForbiddenError('this principal’s role does not include "config:write"'))
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.selectOptions(screen.getByLabelText('Integration'), 'resolume')

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/config:write/)
    expect(alert.className).toContain('panel--warning')
  })

  // Review finding 6: the clip picker disambiguates "Snow (on Layer B)"
  // and buildPayload used to discard the disambiguation, saving a bare
  // `clip: "Snow"` with no `layer` — ambiguous again the moment the macro
  // runs. ResolumeActionController.tsx already got this right; this form
  // must agree.
  //
  // Review finding B4: the picked clip used to be recovered by re-matching
  // `form.resolumeClip` (the NAME) against `resolumeClips`, which always
  // resolves to whichever of the two same-named clips comes first — so
  // picking the SECOND "Snow" option here still saved the FIRST clip's
  // own layer. Two explicitly named layers (rather than relying on one
  // clip falling back to a generated "layer 2" label) make the assertion
  // below unambiguous: `allClipOptions[1]` is the clip on "Layer B", and
  // `ref.layer` must name it exactly, never "Layer A".
  it('saves the disambiguating layer when the picked clip name is shared by another clip on this deck', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main', nameGenerated: false })],
        layers: [
          makeResolumeCompositionLayer({ id: 'layer-1', index: 0, name: 'Layer A', nameGenerated: false }),
          makeResolumeCompositionLayer({ id: 'layer-2', index: 1, name: 'Layer B', nameGenerated: false }),
        ],
        clips: [
          makeResolumeCompositionClip({ id: 'clip-1', name: 'Snow', deckId: 'deck-1', layerIndex: 0 }),
          makeResolumeCompositionClip({ id: 'clip-2', name: 'Snow', deckId: 'deck-1', layerIndex: 1 }),
        ],
      }),
    )
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-16T00:00:00Z',
      kind: 'show.action',
      id: 'launch-snow',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Launch snow',
        description: '',
        safetyClass: 'none',
        target: { integration: 'resolume', action: 'launchClip', ref: { clip: 'Snow', deck: 'Main', layer: 'Layer 2' } },
      },
      updatedAt: '2026-08-16T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'launch-snow')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Launch snow')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'none')
    await user.selectOptions(screen.getByLabelText('Integration'), 'resolume')
    await waitFor(() => expect(getResolumeComposition).toHaveBeenCalled())

    await user.selectOptions(screen.getByLabelText('Action'), 'launchClip')
    await user.selectOptions(screen.getByLabelText('Deck'), screen.getByRole('option', { name: /^Main/ }))
    // Two SEPARATE "Snow (on ...)" options must exist and be distinguishable
    // before either is picked (acceptance criterion 7).
    const allClipOptions = await waitFor(() => {
      const options = screen.getAllByRole('option', { name: /^Snow \(on/i })
      expect(options).toHaveLength(2)
      return options
    })
    expect(new Set(allClipOptions.map((o) => o.textContent)).size).toBe(2)
    await user.selectOptions(screen.getByLabelText('Clip'), allClipOptions[1] as HTMLElement)

    await user.click(screen.getByRole('button', { name: 'Create action' }))

    await waitFor(() => expect(putShowAction).toHaveBeenCalled())
    const [, payload] = putShowAction.mock.calls[0] as [string, { target: { ref?: Record<string, unknown> } }]
    expect(payload.target.ref?.clip).toBe('Snow')
    // The SECOND option (allClipOptions[1]) was picked above — its own
    // layer is "Layer B", never "Layer A" (the first duplicate's layer,
    // which review finding B4's bug always saved regardless of which
    // option was actually selected).
    expect(payload.target.ref?.layer).toBe('Layer B')
  })

  // Review finding 8: an HTML <select> cannot distinguish two <option>s
  // sharing a value, so the deck picker's own option value is now the
  // deck's id — this proves picking the SECOND of two same-named decks
  // actually scopes the clip list to that deck, not silently to the first.
  it('scopes the clip list to the deck actually picked when two decks share a name', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [
          makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main', nameGenerated: false }),
          makeResolumeCompositionDeck({ id: 'deck-2', name: 'Main', nameGenerated: false }),
        ],
        clips: [
          makeResolumeCompositionClip({ id: 'clip-1', name: 'First Deck Clip', deckId: 'deck-1' }),
          makeResolumeCompositionClip({ id: 'clip-2', name: 'Second Deck Clip', deckId: 'deck-2' }),
        ],
      }),
    )
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.selectOptions(screen.getByLabelText('Integration'), 'resolume')
    await waitFor(() => expect(getResolumeComposition).toHaveBeenCalled())
    await user.selectOptions(screen.getByLabelText('Action'), 'launchClip')

    const deckOptionsFound = screen.getAllByRole('option', { name: /^Main/ })
    expect(deckOptionsFound).toHaveLength(2)
    await user.selectOptions(screen.getByLabelText('Deck'), deckOptionsFound[1] as HTMLElement)

    const clipSelect = screen.getByLabelText('Clip')
    const clipTexts = Array.from(clipSelect.querySelectorAll('option'))
      .map((o) => o.textContent ?? '')
      .filter((t) => t !== 'Choose one')
    expect(clipTexts).toEqual(['Second Deck Clip'])
  })
})

// Readability seam: coordinator-reported status (the
// active-revision line and revision history) is split from the authoring
// fieldset above it. The status line stays visible; the long, rarely-
// consulted revision history starts collapsed behind a <details>.
describe('ShowActionDetail (status split from authoring)', () => {
  it('shows the active-revision status without opening anything, and starts revision history collapsed', async () => {
    getActionBinding.mockRejectedValue(new Error('not mocked for this test'))
    getShowAction.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'show.action',
      id: 'projectors-on',
      revision: 4,
      payload: {
        show: 'halloween-2026',
        label: 'Projectors on',
        description: '',
        safetyClass: 'powerOff',
        target: { integration: 'mqtt', broker: 'home-automation', publish: { topic: 't', payload: '', qos: 1, retain: false }, expect: { kind: 'none' } },
      },
      updatedAt: '2026-08-25T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    getShowActionRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      kind: 'show.action',
      revisions: [
        { revision: 4, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true },
        { revision: 3, createdAt: '2026-08-20T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: false },
      ],
    })
    const user = userEvent.setup()
    renderExistingAction(makeModel({ session: adminSession }), 'projectors-on')

    const status = await screen.findByText(/Active revision 4/)
    expect(status).toBeVisible()

    const summary = screen.getByText('Revision history')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    const revisionCell = screen.getAllByText('admin-1', { selector: 'td' })[0]!
    expect(revisionCell).not.toBeVisible()

    await user.click(summary)
    expect(revisionCell).toBeVisible()
  })
})

describe('ShowActionDetail (audio integration wiring)', () => {
  // DESIGN-DECISIONS-AND-API-FACTS.md §6: integration is "'fpp' | 'mqtt' |
  // 'resolume' | 'audio'" — four members, not three. This proves the form
  // now offers and can submit the fourth.
  it('offers audio as an integration choice and submits gainDb, in decibels, for audio.gain.set', async () => {
    putShowAction.mockResolvedValue({
      serverTime: '2026-08-28T00:00:00Z',
      kind: 'show.action',
      id: 'lower-background',
      revision: 1,
      payload: {
        show: 'halloween-2026',
        label: 'Lower background',
        description: '',
        safetyClass: 'none',
        target: { integration: 'audio', audioNodeId: 'audio-node-01', audioSessionId: 'sess-1', audioAction: 'audio.gain.set', params: { gainDb: -12 } },
      },
      updatedAt: '2026-08-28T00:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
    })
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'lower-background')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Lower background')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'none')
    await user.selectOptions(screen.getByLabelText('Integration'), 'audio')
    await user.type(screen.getByLabelText("Audio session id (this node's own pkg/audio session, assigned at runtime)"), 'sess-1')
    await user.selectOptions(screen.getByLabelText('Audio action'), 'audio.gain.set')
    await user.type(screen.getByLabelText(/Gain, in decibels/), '-12')
    await waitFor(() => expect(screen.getByLabelText('Audio node')).toHaveTextContent('halloween-2026'))
    await user.selectOptions(screen.getByLabelText('Audio node'), 'halloween-2026')
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    await waitFor(() => expect(putShowAction).toHaveBeenCalled())
    const [, payload] = putShowAction.mock.calls[0] as [string, { target: Record<string, unknown> }]
    expect(payload.target).toEqual({
      integration: 'audio',
      audioNodeId: 'halloween-2026',
      audioSessionId: 'sess-1',
      audioAction: 'audio.gain.set',
      params: { gainDb: -12 },
    })
  })

  it('refuses the pre-decibel params.gain key at authoring time, naming the decibel replacement', async () => {
    const user = userEvent.setup()
    renderNewAction(makeModel({ session: adminSession }))

    await user.type(screen.getByLabelText('Action id'), 'lower-background')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Label'), 'Lower background')
    await user.selectOptions(screen.getByLabelText('Safety class'), 'none')
    await user.selectOptions(screen.getByLabelText('Integration'), 'audio')
    await waitFor(() => expect(screen.getByLabelText('Audio node')).toHaveTextContent('halloween-2026'))
    await user.selectOptions(screen.getByLabelText('Audio node'), 'halloween-2026')
    await user.type(screen.getByLabelText("Audio session id (this node's own pkg/audio session, assigned at runtime)"), 'sess-1')
    await user.selectOptions(screen.getByLabelText('Audio action'), 'audio.output.mute')
    fireEvent.change(screen.getByLabelText(/Other params/), { target: { value: '{"gain": -6}' } })
    await user.click(screen.getByRole('button', { name: 'Create action' }))

    expect(await screen.findByText(/params\.gain is refused/)).toBeVisible()
    expect(putShowAction).not.toHaveBeenCalled()
  })
})
