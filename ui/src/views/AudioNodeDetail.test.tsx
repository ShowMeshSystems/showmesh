import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AudioNodeDetail } from './AudioNodeDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeNode } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// ADR-018: one audio.node object's editor -- mirrors ShowCueDetail.test.tsx's
// own per-object test shape (new/existing/scope-gating).
const { getAudioNode, getAudioNodeConfigRevisions, putAudioNode } = vi.hoisted(() => ({
  getAudioNode: vi.fn(),
  getAudioNodeConfigRevisions: vi.fn(),
  putAudioNode: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getAudioNode, getAudioNodeConfigRevisions, putAudioNode }
})

afterEach(() => {
  cleanup()
  getAudioNode.mockReset()
  getAudioNodeConfigRevisions.mockReset()
  putAudioNode.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/config/audio.node/new']}>
        <AudioNodeDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/config/audio.node/${id}`]}>
        <Routes>
          <Route path="/config/audio.node/:id" element={<AudioNodeDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const storedNode = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'audio.node' as const,
  id: 'node-1',
  revision: 2,
  payload: {
    programRoute: 'hw:0,0',
    ltcRoute: 'hw:0,0',
    programChannels: [1, 2],
    ltcChannel: 3,
    clockDomain: 'onboard-clock',
    clockDomainProvenance: 'commissioning notes 2026-08-01',
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', revisions: [] }

describe('AudioNodeDetail (viewing an existing node)', () => {
  it('renders the current payload', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('node-1')

    await waitFor(() => expect(screen.getByLabelText('Program route')).toHaveValue('hw:0,0'))
    expect(screen.getByLabelText('LTC route')).toHaveValue('hw:0,0')
    expect(screen.getByLabelText('Program channels')).toHaveValue('1, 2')
    expect(screen.getByLabelText('LTC channel')).toHaveValue(3)
    expect(screen.getByLabelText('Clock domain')).toHaveValue('onboard-clock')
    expect(screen.getByLabelText('Clock domain provenance')).toHaveValue('commissioning notes 2026-08-01')

    // OWNER RULING 2026-08-29: the output-group picker Settings.dc.html
    // drew is kept, stamped as not built, directly beneath the manual
    // channel field that is the real, live path -- never a working
    // control, never styled as one of the four absences.
    const plannedNote = screen.getByRole('note', { name: 'Not built: Output group picker' })
    expect(plannedNote).toHaveTextContent(/outputGroups attribute on audio.output.local/)
    expect(within(plannedNote).queryByRole('checkbox')).not.toBeInTheDocument()
  })

  it('saves the full replacement payload and reloads on success', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    putAudioNode.mockResolvedValue({ ...storedNode, revision: 3 })
    const user = userEvent.setup()
    renderExisting('node-1')

    const clockDomainInput = await screen.findByLabelText('Clock domain')
    await user.clear(clockDomainInput)
    await user.type(clockDomainInput, 'shared-pcie-clock')

    await user.click(screen.getByRole('button', { name: /save audio node/i }))

    await waitFor(() =>
      expect(putAudioNode).toHaveBeenCalledWith('node-1', {
        programRoute: 'hw:0,0',
        ltcRoute: 'hw:0,0',
        programChannels: [1, 2],
        ltcChannel: 3,
        clockDomain: 'shared-pcie-clock',
        clockDomainProvenance: 'commissioning notes 2026-08-01',
      }),
    )
    // The PUT's own response replaces the loaded config directly (matching
    // ShowCueDetail.tsx's identical shape) and the revision history is
    // re-fetched, so the screen reflects the new revision without a
    // redundant second GET of the object itself.
    await waitFor(() => expect(getAudioNodeConfigRevisions).toHaveBeenCalledTimes(2))
    expect(await screen.findByText(/active revision 3/i)).toBeInTheDocument()
  })

  it('does not dispatch two saves when the operator activates save twice quickly', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    let resolvePut: ((value: typeof storedNode) => void) | undefined
    putAudioNode.mockReturnValue(
      new Promise<typeof storedNode>((resolve) => {
        resolvePut = resolve
      }),
    )
    const user = userEvent.setup()
    renderExisting('node-1')

    await screen.findByLabelText('Clock domain')
    const save = screen.getByRole('button', { name: /save audio node/i })
    await user.click(save)
    await user.click(save)
    expect(putAudioNode).toHaveBeenCalledTimes(1)

    resolvePut?.({ ...storedNode, revision: 3 })
    await waitFor(() => expect(getAudioNodeConfigRevisions).toHaveBeenCalledTimes(2))
  })

  it("renders the coordinator's own refusal reason and does not read as saved", async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    putAudioNode.mockRejectedValue(
      new ApiError('this node has not advertised route "hw:0,0" for audio.output.ltc', 400),
    )
    const user = userEvent.setup()
    renderExisting('node-1')

    await screen.findByLabelText('Clock domain')
    await user.click(screen.getByRole('button', { name: /save audio node/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/has not advertised route/i)
    expect(getAudioNode).toHaveBeenCalledTimes(1)
  })

  it('saves a program-only node when both LTC fields are cleared', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    putAudioNode.mockResolvedValue({ ...storedNode, revision: 3 })
    const user = userEvent.setup()
    renderExisting('node-1')

    await user.clear(await screen.findByLabelText('LTC channel'))
    await user.clear(screen.getByLabelText('LTC route'))

    await user.click(screen.getByRole('button', { name: /save audio node/i }))

    // Both keys are OMITTED, not sent empty: the coordinator refuses an
    // empty ltcRoute, and a two-output interface has no LTC channel to
    // declare in the first place.
    await waitFor(() =>
      expect(putAudioNode).toHaveBeenCalledWith('node-1', {
        programRoute: 'hw:0,0',
        programChannels: [1, 2],
        clockDomain: 'onboard-clock',
        clockDomainProvenance: 'commissioning notes 2026-08-01',
      }),
    )
  })

  it('refuses half a declared LTC pair before ever dispatching a PUT', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    const user = userEvent.setup()
    renderExisting('node-1')

    await user.clear(await screen.findByLabelText('LTC channel'))

    await user.click(screen.getByRole('button', { name: /save audio node/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/clear both to declare a program-only node/i)
    expect(putAudioNode).not.toHaveBeenCalled()
  })

  it('refuses an invalid field client-side before ever dispatching a PUT', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    const user = userEvent.setup()
    renderExisting('node-1')

    const ltcChannelInput = await screen.findByLabelText('LTC channel')
    await user.clear(ltcChannelInput)
    await user.type(ltcChannelInput, '2')

    await user.click(screen.getByRole('button', { name: /save audio node/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/must not also appear in program channels/i)
    expect(putAudioNode).not.toHaveBeenCalled()
  })

  it('renders revision history inside a closed details section', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      revisions: [
        {
          revision: 2, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1',
          createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true,
        },
      ],
    })
    renderExisting('node-1')

    await screen.findByLabelText('Clock domain')
    const summary = screen.getByText('Revision history')
    expect(summary.closest('details')).not.toHaveAttribute('open')
  })

  it("drives the route pickers from this node's own advertised capabilities, not free text", async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    const model = makeModel({
      session: adminSession,
      nodes: [
        makeNode('node-1', {
          capabilities: [
            { id: 'audio.output.local', version: 1, attributes: { outputCount: 1, routes: ['hw:0,0', 'hw:1,0'] } },
            { id: 'audio.output.ltc', version: 1, attributes: { outputCount: 1, routes: ['hw:0,0'] } },
          ],
        }),
      ],
    })
    renderExisting('node-1', model)

    const programRouteSelect = await screen.findByLabelText('Program route')
    expect(programRouteSelect.tagName).toBe('SELECT')
    // Program routes come from the local-output capability, while LTC is
    // narrowed to the selected route. A program-only node may legitimately
    // use hw:1,0 even though it is not LTC-capable.
    expect(screen.getAllByRole('option', { name: 'hw:0,0' }).length).toBeGreaterThan(0)
    expect(screen.getByRole('option', { name: 'hw:1,0' })).toBeInTheDocument()
  })

  it('falls back to a plain input when no advertised route evidence is reachable', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('node-1', makeModel({ session: adminSession, nodes: [] }))

    const programRouteInput = await screen.findByLabelText('Program route')
    expect(programRouteInput.tagName).toBe('INPUT')
  })

  it('shows advertised output groups and excludes program channels from LTC choices', async () => {
    getAudioNode.mockResolvedValue(storedNode)
    getAudioNodeConfigRevisions.mockResolvedValue(emptyRevisions)
    const model = makeModel({
      session: adminSession,
      nodes: [
        makeNode('node-1', {
          capabilities: [
            {
              id: 'audio.output.local',
              version: 1,
              attributes: {
                routes: ['hw:0,0'],
                channels: [1, 2, 3, 4],
                outputGroups: [{ id: 'stereo', label: 'Program stereo', channels: [1, 2] }],
              },
            },
            { id: 'audio.output.ltc', version: 1, attributes: { routes: ['hw:0,0'], channels: [1, 2, 3, 4] } },
          ],
        }),
      ],
    })
    renderExisting('node-1', model)

    expect(await screen.findByText(/program stereo/i)).toBeInTheDocument()
    const ltc = screen.getByLabelText('LTC channel')
    expect(ltc.tagName).toBe('SELECT')
    expect(screen.getAllByRole('option', { name: 'Off' }).length).toBeGreaterThan(0)
    expect(screen.getByRole('option', { name: 'Channel 3' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Channel 1' })).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Channel 2' })).not.toBeInTheDocument()
  })
})

describe('AudioNodeDetail (new)', () => {
  it('creates a new audio node at the typed id', async () => {
    putAudioNode.mockResolvedValue({ ...storedNode, id: 'node-9' })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Node id'), 'node-9')
    await user.type(screen.getByLabelText('Program route'), 'hw:0,0')
    await user.type(screen.getByLabelText('LTC route'), 'hw:0,0')
    await user.type(screen.getByLabelText('Program channels'), '1, 2')
    await user.type(screen.getByLabelText('LTC channel'), '3')
    await user.type(screen.getByLabelText('Clock domain'), 'onboard-clock')
    await user.type(screen.getByLabelText('Clock domain provenance'), 'commissioning notes')

    await user.click(screen.getByRole('button', { name: /create audio node/i }))

    await waitFor(() =>
      expect(putAudioNode).toHaveBeenCalledWith('node-9', {
        programRoute: 'hw:0,0',
        ltcRoute: 'hw:0,0',
        programChannels: [1, 2],
        ltcChannel: 3,
        clockDomain: 'onboard-clock',
        clockDomainProvenance: 'commissioning notes',
      }),
    )
  })
})

describe('AudioNodeDetail (scope gating)', () => {
  it('is unavailable, with a stated reason, without the config:write scope for a new node', () => {
    renderNew(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Node id')).not.toBeInTheDocument()
  })

  it('is unavailable, with a stated reason, without the config:write scope for an existing node', () => {
    renderExisting('node-1', makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(getAudioNode).not.toHaveBeenCalled()
  })
})
