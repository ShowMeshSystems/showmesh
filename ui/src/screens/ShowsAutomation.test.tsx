import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  type ActionBinding,
  type ConfigObjectSummary,
  type ConfigShowAction,
  type ConfigShowActionTarget,
  type ConfigShowMacro,
  type Model,
  type SessionResponse,
} from '../api'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

const stubs = vi.hoisted(() => ({
  listConfigObjects: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listAssets: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShow: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowMacro: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowMacroRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  getShowActionRevisions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listActionBindings: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowMacro: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listResolumeActions: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  submitMacroRun: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  invokeAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (...args: never[]) => stubs.listConfigObjects(...args),
    listAssets: (...args: never[]) => stubs.listAssets(...args),
    getShow: (...args: never[]) => stubs.getShow(...args),
    getShowMacro: (...args: never[]) => stubs.getShowMacro(...args),
    getShowMacroRevisions: (...args: never[]) => stubs.getShowMacroRevisions(...args),
    getShowAction: (...args: never[]) => stubs.getShowAction(...args),
    getShowActionRevisions: (...args: never[]) => stubs.getShowActionRevisions(...args),
    listActionBindings: (...args: never[]) => stubs.listActionBindings(...args),
    putShowMacro: (...args: never[]) => stubs.putShowMacro(...args),
    putShowAction: (...args: never[]) => stubs.putShowAction(...args),
    listResolumeActions: (...args: never[]) => stubs.listResolumeActions(...args),
    submitMacroRun: (...args: never[]) => stubs.submitMacroRun(...args),
    invokeAction: (...args: never[]) => stubs.invokeAction(...args),
  }
})

const { ShowsWorkspace, ShowsTabPlaceholder } = await import('./ShowsWorkspace')
const { ShowsAutomation } = await import('./ShowsAutomation')

function macroSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'preshow-lights-up', label: 'Preshow Lights Up', show: 'winter-ridge-2026', currentRevision: 3, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function actionSummary(overrides: Partial<ConfigObjectSummary> = {}): ConfigObjectSummary {
  return { id: 'start-preshow', label: 'Start Preshow Playlist', show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z', ...overrides }
}

function actionPayload(overrides: Partial<ConfigShowAction> = {}): ConfigShowAction {
  return {
    show: 'winter-ridge-2026',
    label: 'Start Preshow Playlist',
    description: '',
    safetyClass: 'none',
    target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'startPlaylist' },
    ...overrides,
  } as ConfigShowAction
}

function actionResponse(id = 'start-preshow', revision = 1, payload = actionPayload()) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.action' as const,
    id,
    revision,
    payload,
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
}

function macroPayload(overrides: Partial<ConfigShowMacro> = {}): ConfigShowMacro {
  return {
    show: 'winter-ridge-2026',
    label: 'Preshow Lights Up',
    description: 'Starts the preshow playlist.',
    steps: [
      { id: 'start-preshow', action: 'start-preshow', onFailure: 'abort', onUnconfirmed: 'continue', localFallback: { class: 'none', reason: 'FPP keeps running on its own.' } },
    ],
    ...overrides,
  } as ConfigShowMacro
}

function macroResponse(id = 'preshow-lights-up', revision = 3, payload = macroPayload()) {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show.macro' as const,
    id,
    revision,
    payload,
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api' as const,
  }
}

function binding(overrides: Partial<ActionBinding> = {}): ActionBinding {
  return { actionId: 'start-preshow', label: 'Start Preshow Playlist', show: 'winter-ridge-2026', state: 'ok', reason: 'Resolved against barn-player.', ...overrides }
}

function showHead() {
  return Promise.resolve({
    serverTime: '2026-08-30T21:00:00Z',
    kind: 'show',
    id: 'winter-ridge-2026',
    revision: 47,
    payload: { name: 'Winter Ridge 2026', notes: '' },
    updatedAt: '2026-08-30T18:22:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'erbartos',
    source: 'api',
  })
}

function signedIn(scopes: string[]): SessionResponse {
  return {
    serverTime: '2026-08-30T21:00:00Z',
    authenticated: true,
    principal: { id: 'p1', name: 'erbartos', kind: 'human', role: 'operator' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: '2026-08-30T21:00:00Z' },
    credentialForm: 'session',
    scopes,
    scopesState: 'current',
    bootstrapRequired: false,
  } as unknown as SessionResponse
}

function assetsEmpty() {
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', assets: [] })
}

function withContents(kind: string, macros: ConfigObjectSummary[], actions: ConfigObjectSummary[]) {
  if (kind === 'show.macro') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: macros })
  if (kind === 'show.action') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: actions })
  return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [] })
}

function renderWorkspace(model: Partial<Model> = {}, path = '/shows/winter-ridge-2026/automation') {
  return render(
    <ModelContext.Provider value={{ ...initialModel(), ...model }}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/shows/:id" element={<ShowsWorkspace />}>
            <Route path="automation" element={<ShowsAutomation />} />
            <Route path="playlists" element={<ShowsTabPlaceholder tab="Playlists" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Shows · Automation tab', () => {
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  function setup(
    scopes: string[] = ['config:write', 'show:macro:run', 'show:action:invoke'],
    macros: ConfigObjectSummary[] = [macroSummary()],
    actions: ConfigObjectSummary[] = [actionSummary()],
    bindings: ActionBinding[] = [binding()],
  ) {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, macros, actions)
    stubs.listAssets = assetsEmpty
    stubs.getShowMacro = (id: string) => Promise.resolve(macroResponse(id))
    stubs.getShowAction = (id: string) => Promise.resolve(actionResponse(id))
    stubs.listActionBindings = () => Promise.resolve(bindings)
    return renderWorkspace({ session: signedIn(scopes) })
  }

  it('renders the section headings and lists a macro with its step', async () => {
    setup()
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Macros and actions' })).toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Macros' })).toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Actions' })).toBeInTheDocument())
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Preshow Lights Up' })).toBeInTheDocument())
    expect(screen.getAllByText(/Start Preshow Playlist/).length).toBeGreaterThan(0)
  })

  it('a show with no macro or action is a settled empty, distinct from a read failure', async () => {
    setup(['config:write'], [], [])
    expect(await screen.findByText('No macro matches here.')).toBeInTheDocument()
    expect(screen.getAllByText('No action matches here.')).toHaveLength(2)
  })

  it('a read failure is reported distinctly, with a retry', async () => {
    stubs.getShow = showHead
    stubs.listConfigObjects = () => Promise.reject(new ApiError('Coordinator unreachable.', 503, 'unavailable'))
    stubs.listAssets = assetsEmpty
    renderWorkspace({ session: signedIn(['config:write']) })
    expect(await screen.findByText('Coordinator unreachable.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })

  it('a broken binding is reported under Needs you and distinguished from an ok one', async () => {
    setup(['config:write'], [macroSummary()], [actionSummary()], [binding({ state: 'broken', reason: 'Two clips share the name.' })])
    const heading = await screen.findByRole('heading', { name: /Needs you/ })
    const section = heading.closest('section')
    expect(section).not.toBeNull()
    await waitFor(() => expect(within(section as HTMLElement).getByText('Binding broken')).toBeInTheDocument())
    expect(within(section as HTMLElement).getByText(/Two clips share the name/)).toBeInTheDocument()
  })

  it('running a macro is disabled without show:macro:run and is actually inert', async () => {
    setup(['config:write'])
    const run = await screen.findByRole('button', { name: 'Run' })
    expect(run).toBeDisabled()
    const runSpy = vi.fn(() => new Promise(() => {}))
    stubs.submitMacroRun = runSpy
    fireEvent.click(run)
    expect(runSpy).not.toHaveBeenCalled()
  })

  it('invoking an action with a broken binding is disabled with the binding reason, distinct from a scope refusal', async () => {
    setup(
      ['config:write', 'show:action:invoke'],
      [],
      [actionSummary()],
      [binding({ state: 'broken', reason: 'Target does not resolve.' })],
    )
    const invoke = await screen.findByRole('button', { name: 'Invoke' })
    expect(invoke).toBeDisabled()
    expect(invoke).toHaveAttribute('title', expect.stringContaining('Target does not resolve.'))
  })

  it('editing a step is disabled without config:write and is actually inert', async () => {
    setup(['show:macro:run'])
    const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const save = await screen.findByRole('button', { name: 'Save macro' })
    expect(save).toBeDisabled()
    const putSpy = vi.fn(() => new Promise(() => {}))
    stubs.putShowMacro = putSpy
    fireEvent.click(save)
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('opens the step editor as a wide dialog portaled into document.body and clears the selection on Escape', async () => {
    setup()
    const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const dialog = await screen.findByRole('dialog')
    expect(dialog.parentElement).toBe(document.body)
    expect(dialog.className).toContain('sm-drawer--wide')

    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('opens the step editor from a click anywhere on the row, not only the action link', async () => {
    setup()
    const stepId = await screen.findByText('start-preshow')
    fireEvent.click(stepId)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('a step cannot be saved without a local-fallback reason', async () => {
    setup()
    const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const reasonInput = await screen.findByLabelText('Reason')
    fireEvent.change(reasonInput, { target: { value: '' } })
    const save = screen.getByRole('button', { name: 'Save macro' })
    expect(save).toBeDisabled()
    expect(save).toHaveAttribute('title', 'State what happens locally while the coordinator is unreachable, in your own words.')
  })

  it('a stale macro step save is refused and writes nothing', async () => {
    let calls = 0
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [macroSummary()], [actionSummary()])
    stubs.listAssets = assetsEmpty
    stubs.getShowMacro = (id: string) => {
      calls += 1
      return Promise.resolve(macroResponse(id, calls === 1 ? 3 : 4))
    }
    stubs.getShowAction = (id: string) => Promise.resolve(actionResponse(id))
    stubs.listActionBindings = () => Promise.resolve([binding()])
    const putSpy = vi.fn((id: string, payload: ConfigShowMacro) => {
      void id
      void payload
      return Promise.resolve(macroResponse('preshow-lights-up', 4))
    })
    stubs.putShowMacro = putSpy
    renderWorkspace({ session: signedIn(['config:write', 'show:macro:run', 'show:action:invoke']) })

    const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const save = await screen.findByRole('button', { name: 'Save macro' })
    await waitFor(() => expect(save).not.toBeDisabled())
    fireEvent.click(save)
    expect(await screen.findByText(/Stale write/)).toBeInTheDocument()
    expect(putSpy).not.toHaveBeenCalled()
  })

  describe('editing a macro from the card and the aside', () => {
    function twoStepPayload(): ConfigShowMacro {
      return macroPayload({
        steps: [
          { id: 'step-a', action: 'start-preshow', onFailure: 'continue', onUnconfirmed: 'continue', localFallback: { class: 'none', reason: 'FPP keeps running on its own.' } },
          { id: 'step-b', action: 'second-action', onFailure: 'continue', onUnconfirmed: 'continue', localFallback: { class: 'none', reason: 'Lights stay as last set.' } },
        ],
      })
    }

    function setupTwoStepMacro() {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) =>
        withContents(kind, [macroSummary()], [actionSummary(), actionSummary({ id: 'second-action', label: 'Second Step Action' })])
      stubs.listAssets = assetsEmpty
      stubs.getShowMacro = (id: string) => Promise.resolve(macroResponse(id, 3, twoStepPayload()))
      stubs.getShowAction = (id: string) =>
        Promise.resolve(id === 'second-action' ? actionResponse(id, 1, actionPayload({ label: 'Second Step Action' })) : actionResponse(id))
      stubs.listActionBindings = () => Promise.resolve([binding(), binding({ actionId: 'second-action', label: 'Second Step Action' })])
      return renderWorkspace({ session: signedIn(['config:write', 'show:macro:run', 'show:action:invoke']) })
    }

    it('does not open the step editor from the drag handle or a Move/Remove button', async () => {
      const { container } = setupTwoStepMacro()
      const handle = await waitFor(() => {
        const el = container.querySelector('.sm-handle')
        expect(el).not.toBeNull()
        return el as HTMLElement
      })
      fireEvent.click(handle)
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

      fireEvent.click(screen.getAllByRole('button', { name: 'Move down' })[0]!)
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

      fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[0]!)
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })

    it('adds a step from the card and saves it with a fresh id', async () => {
      const putSpy = vi.fn((id: string, payload: ConfigShowMacro) => {
      void id
      void payload
      return Promise.resolve(macroResponse('preshow-lights-up', 4))
    })
      setup()
      stubs.putShowMacro = putSpy

      const addStep = await screen.findByRole('button', { name: 'Add step' })
      fireEvent.click(addStep)

      const reasonInput = await screen.findByLabelText('Reason')
      fireEvent.change(reasonInput, { target: { value: 'Runs again for the encore.' } })
      const actionSelect = screen.getByLabelText('Action')
      fireEvent.change(actionSelect, { target: { value: 'start-preshow' } })

      const save = screen.getByRole('button', { name: 'Save macro' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)

      await waitFor(() => expect(putSpy).toHaveBeenCalledTimes(1))
      const [, payload] = putSpy.mock.calls[0]! as [string, ConfigShowMacro]
      expect(payload.steps).toHaveLength(2)
      expect(payload.steps[1]?.id).toBeTruthy()
      expect(payload.steps[1]?.id).not.toBe('start-preshow')
      expect(new Set(payload.steps.map((s) => s.id)).size).toBe(2)
    })

    it('removes a step from the card and saves the shorter list', async () => {
      const putSpy = vi.fn((id: string, payload: ConfigShowMacro) => {
      void id
      void payload
      return Promise.resolve(macroResponse('preshow-lights-up', 4))
    })
      setupTwoStepMacro()
      stubs.putShowMacro = putSpy

      await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      expect(screen.getAllByRole('listitem')).toHaveLength(2)
      const removeButtons = screen.getAllByRole('button', { name: 'Remove' })
      fireEvent.click(removeButtons[0]!)
      expect(screen.getAllByRole('listitem')).toHaveLength(1)

      const remaining = screen.getByRole('button', { name: /^1\. Second Step Action/ })
      fireEvent.click(remaining)
      const save = await screen.findByRole('button', { name: 'Save macro' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)

      await waitFor(() => expect(putSpy).toHaveBeenCalledTimes(1))
      const [, payload] = putSpy.mock.calls[0]! as [string, ConfigShowMacro]
      expect(payload.steps).toHaveLength(1)
      expect(payload.steps[0]?.id).toBe('step-b')
    })

    it('moves a step down then back up and saves the reordered list', async () => {
      const putSpy = vi.fn((id: string, payload: ConfigShowMacro) => {
      void id
      void payload
      return Promise.resolve(macroResponse('preshow-lights-up', 4))
    })
      setupTwoStepMacro()
      stubs.putShowMacro = putSpy

      await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      const moveDown = screen.getAllByRole('button', { name: 'Move down' })[0]!
      fireEvent.click(moveDown)
      expect(await screen.findByRole('button', { name: /^1\. Second Step Action/ })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /^2\. Start Preshow Playlist/ })).toBeInTheDocument()

      const moveUp = screen.getAllByRole('button', { name: 'Move up' })[1]!
      fireEvent.click(moveUp)
      expect(await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /^2\. Second Step Action/ })).toBeInTheDocument()

      fireEvent.click(screen.getByRole('button', { name: /^1\. Start Preshow Playlist/ }))
      const save = await screen.findByRole('button', { name: 'Save macro' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)

      await waitFor(() => expect(putSpy).toHaveBeenCalledTimes(1))
      const [, payload] = putSpy.mock.calls[0]! as [string, ConfigShowMacro]
      expect(payload.steps.map((s) => s.id)).toEqual(['step-a', 'step-b'])
    })

    it('disables Move up on the first row and Move down on the last, each with its own reason', async () => {
      setupTwoStepMacro()
      await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      const moveUps = screen.getAllByRole('button', { name: 'Move up' })
      const moveDowns = screen.getAllByRole('button', { name: 'Move down' })
      expect(moveUps[0]).toBeDisabled()
      expect(moveUps[0]).toHaveAttribute('title', 'Already first.')
      expect(moveDowns[1]).toBeDisabled()
      expect(moveDowns[1]).toHaveAttribute('title', 'Already last.')
    })

    it('blocks Remove on a macro left with only one step', async () => {
      setup()
      await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      const remove = screen.getByRole('button', { name: 'Remove' })
      expect(remove).toBeDisabled()
      expect(remove).toHaveAttribute('title', 'A macro needs at least one step.')
    })

    it('blocks Add step at the 32-step ceiling with a stated reason', async () => {
      const steps = Array.from({ length: 32 }, (_, i) => ({
        id: `step-${i + 1}`,
        action: 'start-preshow',
        onFailure: 'continue' as const,
        onUnconfirmed: 'continue' as const,
        localFallback: { class: 'none' as const, reason: `Reason ${i + 1}` },
      }))
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withContents(kind, [macroSummary()], [actionSummary()])
      stubs.listAssets = assetsEmpty
      stubs.getShowMacro = (id: string) => Promise.resolve(macroResponse(id, 3, macroPayload({ steps })))
      stubs.getShowAction = (id: string) => Promise.resolve(actionResponse(id))
      stubs.listActionBindings = () => Promise.resolve([binding()])
      renderWorkspace({ session: signedIn(['config:write', 'show:macro:run', 'show:action:invoke']) })

      const addStep = await screen.findByRole('button', { name: 'Add step' })
      expect(addStep).toBeDisabled()
      expect(addStep).toHaveAttribute('title', 'A macro holds at most 32 steps.')
    })

    it('edits the macro label and description and saves them with the step', async () => {
      const putSpy = vi.fn((id: string, payload: ConfigShowMacro) => {
      void id
      void payload
      return Promise.resolve(macroResponse('preshow-lights-up', 4))
    })
      setup()
      stubs.putShowMacro = putSpy

      const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      fireEvent.click(stepButton)
      const labelInput = await screen.findByLabelText('Label')
      fireEvent.change(labelInput, { target: { value: 'Preshow Lights Up, Revised' } })
      const descriptionInput = screen.getByLabelText('Description')
      fireEvent.change(descriptionInput, { target: { value: 'Now also dims the porch light.' } })

      const save = screen.getByRole('button', { name: 'Save macro' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)

      await waitFor(() => expect(putSpy).toHaveBeenCalledTimes(1))
      const [, payload] = putSpy.mock.calls[0]! as [string, ConfigShowMacro]
      expect(payload.label).toBe('Preshow Lights Up, Revised')
      expect(payload.description).toBe('Now also dims the porch light.')
    })

    it('blocks save on an empty local-fallback reason, naming the coordinator-unreachable rule', async () => {
      setup()
      const stepButton = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      fireEvent.click(stepButton)
      const reasonInput = await screen.findByLabelText('Reason')
      fireEvent.change(reasonInput, { target: { value: '' } })
      const save = screen.getByRole('button', { name: 'Save macro' })
      expect(save).toBeDisabled()
      expect(save).toHaveAttribute('title', 'State what happens locally while the coordinator is unreachable, in your own words.')
    })

    it('blocks save when a step other than the one open has no fallback reason', async () => {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) =>
        withContents(kind, [macroSummary()], [actionSummary(), actionSummary({ id: 'second-action', label: 'Second Step Action' })])
      stubs.listAssets = assetsEmpty
      stubs.getShowMacro = (id: string) =>
        Promise.resolve(
          macroResponse(
            id,
            3,
            macroPayload({
              steps: [
                { id: 'step-a', action: 'start-preshow', onFailure: 'continue', onUnconfirmed: 'continue', localFallback: { class: 'none', reason: 'FPP keeps running on its own.' } },
                { id: 'step-b', action: 'second-action', onFailure: 'continue', onUnconfirmed: 'continue', localFallback: { class: 'none', reason: '' } },
              ],
            }),
          ),
        )
      stubs.getShowAction = (id: string) =>
        Promise.resolve(id === 'second-action' ? actionResponse(id, 1, actionPayload({ label: 'Second Step Action' })) : actionResponse(id))
      stubs.listActionBindings = () => Promise.resolve([binding(), binding({ actionId: 'second-action', label: 'Second Step Action' })])
      renderWorkspace({ session: signedIn(['config:write', 'show:macro:run', 'show:action:invoke']) })

      const stepA = await screen.findByRole('button', { name: /^1\. Start Preshow Playlist/ })
      fireEvent.click(stepA)
      const save = await screen.findByRole('button', { name: 'Save macro' })
      expect(save).toBeDisabled()
      expect(save).toHaveAttribute('title', 'Step 2 has no fallback reason.')
    })
  })

  function resolumeActionsFixture() {
    return Promise.resolve({
      serverTime: '2026-08-30T21:00:00Z',
      actions: [
        { name: 'launchClip', params: [{ name: 'clip', kind: 'string', required: true }], auditExempt: false, coordinatorRequired: true },
        { name: 'blackout', params: [], auditExempt: true, coordinatorRequired: true },
      ],
    })
  }

  function setupWithAction(overrides: Partial<ConfigShowAction> = {}, scopes: string[] = ['config:write', 'show:macro:run', 'show:action:invoke']) {
    stubs.getShow = showHead
    stubs.listConfigObjects = (kind: string) => withContents(kind, [macroSummary()], [actionSummary()])
    stubs.listAssets = assetsEmpty
    stubs.getShowMacro = (id: string) => Promise.resolve(macroResponse(id))
    stubs.getShowAction = (id: string) => Promise.resolve(actionResponse(id, 1, actionPayload(overrides)))
    stubs.listActionBindings = () => Promise.resolve([binding()])
    return renderWorkspace({ session: signedIn(scopes) })
  }

  describe('the new action draft', () => {
    it('renders nothing below the integration gate until it is answered', async () => {
      setup()
      const newAction = await screen.findByRole('button', { name: 'New action' })
      fireEvent.click(newAction)
      expect(await screen.findByText('Draft · new action')).toBeInTheDocument()
      const aside = screen.getByRole('dialog')
      expect(within(aside).queryByText('mqtt target')).not.toBeInTheDocument()
      expect(within(aside).queryByText('fpp target')).not.toBeInTheDocument()
      expect(within(aside).queryByRole('heading', { name: 'Safety class' })).not.toBeInTheDocument()

      fireEvent.click(within(aside).getByRole('button', { name: 'MQTT' }))
      expect(await within(aside).findByText('mqtt target')).toBeInTheDocument()
      expect(within(aside).getByRole('heading', { name: 'Safety class' })).toBeInTheDocument()
    })

    it('each integration renders its own target block and not another one', async () => {
      stubs.listResolumeActions = resolumeActionsFixture
      setup()
      const newAction = await screen.findByRole('button', { name: 'New action' })
      fireEvent.click(newAction)
      const aside = screen.getByRole('dialog')

      fireEvent.click(within(aside).getByRole('button', { name: 'FPP' }))
      expect(await within(aside).findByText('fpp target')).toBeInTheDocument()
      expect(within(aside).queryByText('mqtt target')).not.toBeInTheDocument()
      expect(within(aside).queryByText('resolume target')).not.toBeInTheDocument()
      expect(within(aside).queryByText('audio target')).not.toBeInTheDocument()

      fireEvent.click(within(aside).getByRole('button', { name: 'MQTT' }))
      expect(await within(aside).findByText('mqtt target')).toBeInTheDocument()
      expect(within(aside).queryByText('fpp target')).not.toBeInTheDocument()

      fireEvent.click(within(aside).getByRole('button', { name: 'Resolume' }))
      expect(await within(aside).findByText('resolume target')).toBeInTheDocument()
      expect(within(aside).queryByText('mqtt target')).not.toBeInTheDocument()

      fireEvent.click(within(aside).getByRole('button', { name: 'Audio' }))
      expect(await within(aside).findByText('audio target')).toBeInTheDocument()
      expect(within(aside).queryByText('resolume target')).not.toBeInTheDocument()
    })

    it('derives stop for stopPlaylistGracefully and none for startPlaylist, disabling the control both times', async () => {
      setup()
      fireEvent.click(await screen.findByRole('button', { name: 'New action' }))
      const aside = screen.getByRole('dialog')
      fireEvent.click(within(aside).getByRole('button', { name: 'FPP' }))
      const primitive = await within(aside).findByLabelText('Primitive')
      fireEvent.change(primitive, { target: { value: 'stopPlaylistGracefully' } })
      const stopButton = await within(aside).findByRole('button', { name: 'Stop' })
      expect(stopButton).toBeDisabled()
      expect(stopButton).toHaveAttribute('aria-pressed', 'true')

      fireEvent.change(primitive, { target: { value: 'startPlaylist' } })
      const noneButton = await within(aside).findByRole('button', { name: 'None' })
      expect(noneButton).toBeDisabled()
      expect(noneButton).toHaveAttribute('aria-pressed', 'true')
    })

    it('derives blackout for audio.output.mute, disabling the control', async () => {
      setup()
      fireEvent.click(await screen.findByRole('button', { name: 'New action' }))
      const aside = screen.getByRole('dialog')
      fireEvent.click(within(aside).getByRole('button', { name: 'Audio' }))
      const operation = await within(aside).findByLabelText('Operation')
      fireEvent.change(operation, { target: { value: 'audio.output.mute' } })
      const blackoutButton = await within(aside).findByRole('button', { name: 'Blackout' })
      expect(blackoutButton).toBeDisabled()
      expect(blackoutButton).toHaveAttribute('aria-pressed', 'true')
    })

    it('leaves mqtt safety class unanswered and withholds the save until it is answered', async () => {
      setup()
      fireEvent.click(await screen.findByRole('button', { name: 'New action' }))
      const aside = screen.getByRole('dialog')
      fireEvent.change(await within(aside).findByLabelText('Label'), { target: { value: 'Weather Sign' } })
      fireEvent.click(within(aside).getByRole('button', { name: 'MQTT' }))
      fireEvent.change(await within(aside).findByLabelText('Broker'), { target: { value: 'lot-a' } })
      fireEvent.change(within(aside).getByLabelText('Topic'), { target: { value: 'sign/set' } })

      const create = within(aside).getByRole('button', { name: 'Create action' })
      expect(create).toBeDisabled()
      expect(create).toHaveAttribute('title', 'Safety class required')

      fireEvent.click(within(aside).getByRole('button', { name: 'None' }))
      await waitFor(() => expect(create).not.toBeDisabled())
    })
  })

  describe('the new macro draft', () => {
    it('withholds the save until the fallback reason is non-empty, and a class of none does not exempt it', async () => {
      setup()
      const newMacro = await screen.findByRole('button', { name: 'New macro' })
      fireEvent.click(newMacro)
      const aside = screen.getByRole('dialog')
      fireEvent.change(await within(aside).findByLabelText('Label'), { target: { value: 'Wind Hold' } })
      const actionSelect = within(aside).getByLabelText('Action')
      fireEvent.change(actionSelect, { target: { value: 'start-preshow' } })
      fireEvent.change(within(aside).getByLabelText('Step id'), { target: { value: 'stop-playlist' } })

      const create = within(aside).getByRole('button', { name: 'Create macro' })
      expect(create).toBeDisabled()
      expect(create).toHaveAttribute('title', 'Fallback reason required')

      fireEvent.change(within(aside).getByLabelText('Fallback class'), { target: { value: 'none' } })
      expect(create).toBeDisabled()

      fireEvent.change(within(aside).getByLabelText('Fallback reason'), { target: { value: 'FPP keeps running on its own.' } })
      await waitFor(() => expect(create).not.toBeDisabled())
    })
  })

  describe('editing an action', () => {
    it('locks id and integration and states what macro and step number use it', async () => {
      setupWithAction({ target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'stopPlaylist' }, safetyClass: 'stop' })
      const editButton = await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' })
      fireEvent.click(editButton)
      const aside = screen.getByRole('dialog')
      expect(await within(aside).findByText('Editing · Start Preshow Playlist')).toBeInTheDocument()
      expect(within(aside).getAllByText('start-preshow').length).toBeGreaterThan(0)
      expect(within(aside).getAllByText('fpp').length).toBeGreaterThan(0)
      expect(within(aside).queryByLabelText('Id')).not.toBeInTheDocument()
      expect(within(aside).queryByRole('button', { name: 'FPP' })).not.toBeInTheDocument()
      const usedByFact = aside.querySelector('.sm-attn__fact')
      expect(usedByFact?.textContent).toContain('Preshow Lights Up')
      expect(usedByFact?.textContent).toContain('step')
      expect(usedByFact?.textContent).toContain('1')
    })

    it('renders the compact active-revision summary for the action, not a list heading', async () => {
      stubs.getShowActionRevisions = () =>
        Promise.resolve({
          serverTime: '2026-08-30T21:00:00Z',
          kind: 'show.action',
          revisions: [
            { revision: 1, createdAt: '2026-08-22T11:09:00Z', createdByPrincipalId: 'p1', createdByPrincipalName: 'erbartos', source: 'api', note: '', active: true },
          ],
        })
      setupWithAction({ target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'stopPlaylist' }, safetyClass: 'stop' })
      const editButton = await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' })
      fireEvent.click(editButton)
      const aside = screen.getByRole('dialog')
      expect(await within(aside).findByText(/Active revision/)).toBeInTheDocument()
      expect(within(aside).queryByRole('heading', { name: 'Revisions' })).not.toBeInTheDocument()
      expect(within(aside).queryByText('Active · 1')).not.toBeInTheDocument()
    })

    it('does not claim a read failure while the action’s revisions fetch is still pending', async () => {
      stubs.getShowActionRevisions = () => new Promise(() => {})
      setupWithAction({ target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'stopPlaylist' }, safetyClass: 'stop' })
      const editButton = await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' })
      fireEvent.click(editButton)
      const aside = screen.getByRole('dialog')
      await within(aside).findByText('Editing · Start Preshow Playlist')
      expect(within(aside).queryByText('Revision history could not be read just now.')).not.toBeInTheDocument()
    })

    it('reports the read failure honestly when the action’s revisions fetch is rejected', async () => {
      stubs.getShowActionRevisions = () => Promise.reject(new Error('network down'))
      setupWithAction({ target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'stopPlaylist' }, safetyClass: 'stop' })
      const editButton = await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' })
      fireEvent.click(editButton)
      const aside = screen.getByRole('dialog')
      expect(await within(aside).findByText('Revision history could not be read just now.')).toBeInTheDocument()
    })

    it('a stale save is refused and writes nothing', async () => {
      let calls = 0
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => withContents(kind, [macroSummary()], [actionSummary()])
      stubs.listAssets = assetsEmpty
      stubs.getShowMacro = (id: string) => Promise.resolve(macroResponse(id))
      stubs.getShowAction = (id: string) => {
        calls += 1
        return Promise.resolve(actionResponse(id, calls === 1 ? 1 : 2, actionPayload({ target: { integration: 'fpp', instanceId: 'barn-player', primitive: 'stopPlaylist' }, safetyClass: 'stop' })))
      }
      stubs.listActionBindings = () => Promise.resolve([binding()])
      const putSpy = vi.fn(() => Promise.resolve(actionResponse('start-preshow', 2)))
      stubs.putShowAction = putSpy
      renderWorkspace({ session: signedIn(['config:write']) })

      const editButton = await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' })
      fireEvent.click(editButton)
      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      expect(await screen.findByText(/Stale write/)).toBeInTheDocument()
      expect(putSpy).not.toHaveBeenCalled()
    })
  })

  describe('editing an audio action target', () => {
    function nodeSummary(id: string, label: string): ConfigObjectSummary {
      return { id, label, show: 'winter-ridge-2026', currentRevision: 1, updatedAt: '2026-08-30T18:22:00Z' }
    }

    function setupWithAudioAction(target: Partial<ConfigShowActionTarget>) {
      stubs.getShow = showHead
      stubs.listConfigObjects = (kind: string) => {
        if (kind === 'audio.node') return Promise.resolve({ serverTime: '2026-08-30T21:00:00Z', kind, objects: [nodeSummary('a', 'Node A'), nodeSummary('b', 'Node B')] })
        return withContents(kind, [], [actionSummary()])
      }
      stubs.listAssets = assetsEmpty
      stubs.getShowAction = (id: string) =>
        Promise.resolve(
          actionResponse(
            id,
            1,
            actionPayload({ target: { integration: 'audio', audioSessionId: 's1', audioAction: 'audio.session.start', ...target } as ConfigShowActionTarget }),
          ),
        )
      stubs.listActionBindings = () => Promise.resolve([binding()])
      return renderWorkspace({ session: signedIn(['config:write']) })
    }

    it('a stored array of node ids renders every box checked and saves the array unchanged', async () => {
      setupWithAudioAction({ audioNodeId: ['a', 'b'] })
      let sent: ConfigShowActionTarget | null = null
      stubs.putShowAction = (id: string, payload: { target: ConfigShowActionTarget }) => {
        sent = payload.target
        return Promise.resolve(actionResponse(id, 2, actionPayload({ target: payload.target })))
      }
      fireEvent.click(await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' }))
      const aside = screen.getByRole('dialog')
      const boxA = await within(aside).findByRole('checkbox', { name: 'Node A' })
      const boxB = within(aside).getByRole('checkbox', { name: 'Node B' })
      expect(boxA).toBeChecked()
      expect(boxB).toBeChecked()

      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as unknown as ConfigShowActionTarget).audioNodeId).toEqual(['a', 'b'])
    })

    it('a stored bare string renders one box checked and saves it back as a bare string', async () => {
      setupWithAudioAction({ audioNodeId: 'a' })
      let sent: ConfigShowActionTarget | null = null
      stubs.putShowAction = (id: string, payload: { target: ConfigShowActionTarget }) => {
        sent = payload.target
        return Promise.resolve(actionResponse(id, 2, actionPayload({ target: payload.target })))
      }
      fireEvent.click(await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' }))
      const aside = screen.getByRole('dialog')
      const boxA = await within(aside).findByRole('checkbox', { name: 'Node A' })
      const boxB = within(aside).getByRole('checkbox', { name: 'Node B' })
      expect(boxA).toBeChecked()
      expect(boxB).not.toBeChecked()

      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as unknown as ConfigShowActionTarget).audioNodeId).toBe('a')
    })

    it('checking a second node on a single-node action saves an array', async () => {
      setupWithAudioAction({ audioNodeId: 'a' })
      let sent: ConfigShowActionTarget | null = null
      stubs.putShowAction = (id: string, payload: { target: ConfigShowActionTarget }) => {
        sent = payload.target
        return Promise.resolve(actionResponse(id, 2, actionPayload({ target: payload.target })))
      }
      fireEvent.click(await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' }))
      const aside = screen.getByRole('dialog')
      const boxB = await within(aside).findByRole('checkbox', { name: 'Node B' })
      fireEvent.click(boxB)

      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as unknown as ConfigShowActionTarget).audioNodeId).toEqual(['a', 'b'])
    })

    it('a stored single-element array round-trips as an array, not a bare string', async () => {
      setupWithAudioAction({ audioNodeId: ['a'] })
      let sent: ConfigShowActionTarget | null = null
      stubs.putShowAction = (id: string, payload: { target: ConfigShowActionTarget }) => {
        sent = payload.target
        return Promise.resolve(actionResponse(id, 2, actionPayload({ target: payload.target })))
      }
      fireEvent.click(await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' }))
      const aside = screen.getByRole('dialog')
      const boxA = await within(aside).findByRole('checkbox', { name: 'Node A' })
      expect(boxA).toBeChecked()

      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as unknown as ConfigShowActionTarget).audioNodeId).toEqual(['a'])
    })

    it('a node id not in the declared list renders checked and labelled not declared, and unchecking it drops it', async () => {
      setupWithAudioAction({ audioNodeId: ['a', 'ghost-node'] })
      let sent: ConfigShowActionTarget | null = null
      stubs.putShowAction = (id: string, payload: { target: ConfigShowActionTarget }) => {
        sent = payload.target
        return Promise.resolve(actionResponse(id, 2, actionPayload({ target: payload.target })))
      }
      fireEvent.click(await screen.findByRole('row', { name: 'Edit Start Preshow Playlist' }))
      const aside = screen.getByRole('dialog')
      const ghostBox = await within(aside).findByRole('checkbox', { name: 'ghost-node (not declared)' })
      expect(ghostBox).toBeChecked()
      fireEvent.click(ghostBox)

      const save = await screen.findByRole('button', { name: 'Save action' })
      await waitFor(() => expect(save).not.toBeDisabled())
      fireEvent.click(save)
      await waitFor(() => expect(sent).not.toBeNull())
      expect((sent as unknown as ConfigShowActionTarget).audioNodeId).toEqual(['a'])
    })
  })
})
