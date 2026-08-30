import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  type ActionBinding,
  type ConfigObjectSummary,
  type ConfigShowAction,
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
  getShowAction: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  listActionBindings: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
  putShowMacro: (() => new Promise(() => {})) as (...args: never[]) => Promise<unknown>,
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
    getShowAction: (...args: never[]) => stubs.getShowAction(...args),
    listActionBindings: (...args: never[]) => stubs.listActionBindings(...args),
    putShowMacro: (...args: never[]) => stubs.putShowMacro(...args),
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
    const stepButton = await screen.findByRole('button', { name: /Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const save = await screen.findByRole('button', { name: 'Save macro' })
    expect(save).toBeDisabled()
    const putSpy = vi.fn(() => new Promise(() => {}))
    stubs.putShowMacro = putSpy
    fireEvent.click(save)
    expect(putSpy).not.toHaveBeenCalled()
  })

  it('a step cannot be saved without a local-fallback reason', async () => {
    setup()
    const stepButton = await screen.findByRole('button', { name: /Start Preshow Playlist/ })
    fireEvent.click(stepButton)
    const reasonInput = await screen.findByLabelText('Reason')
    fireEvent.change(reasonInput, { target: { value: '' } })
    const save = screen.getByRole('button', { name: 'Save macro' })
    expect(save).toBeDisabled()
    expect(save).toHaveAttribute('title', 'State what happens locally while the coordinator is unreachable, in your own words.')
  })
})
