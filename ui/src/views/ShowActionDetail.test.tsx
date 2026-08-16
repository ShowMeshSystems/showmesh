import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowActionDetail } from './ShowActionDetail'
import { ModelContext } from '../app/ModelContext'
import {
  makeModel,
  makeResolumeCompositionClip,
  makeResolumeCompositionDeck,
  makeResolumeCompositionResponse,
} from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ForbiddenError } from '../api/errors'
import type { Model } from '../app/types'

const { putShowAction, getResolumeComposition } = vi.hoisted(() => ({
  putShowAction: vi.fn(),
  getResolumeComposition: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, putShowAction, getResolumeComposition }
})

afterEach(() => {
  cleanup()
  putShowAction.mockReset()
  getResolumeComposition.mockReset()
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
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
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
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
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
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
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
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
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
  it('saves the disambiguating layer when the picked clip name is shared by another clip on this deck', async () => {
    getResolumeComposition.mockResolvedValue(
      makeResolumeCompositionResponse({
        decks: [makeResolumeCompositionDeck({ id: 'deck-1', name: 'Main', nameGenerated: false })],
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
    await user.type(screen.getByLabelText('Show'), 'halloween-2026')
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
    expect(payload.target.ref?.layer).toBeDefined()
    expect(payload.target.ref?.layer).not.toBe('')
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
