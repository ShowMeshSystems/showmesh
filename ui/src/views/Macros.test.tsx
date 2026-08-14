import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Macros } from './Macros'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Same isolation pattern as Configuration.test.tsx: mock the one API call
// this view makes, to test this component's OWN branching (which state
// renders what) rather than re-proving store.test.ts's network behavior.
const { listConfigObjects } = vi.hoisted(() => ({ listConfigObjects: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderMacros(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <Macros />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'], // deliberately NOT config:write
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-2', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'], // deliberately NOT show:macro:run
})

const listResponse = {
  serverTime: '2026-08-14T00:00:00Z',
  kind: 'show.macro' as const,
  objects: [{ id: 'begin-set', label: 'Begin set', show: 'halloween-2026', currentRevision: 3, updatedAt: '2026-08-14T00:00:00Z' }],
}

describe('Macros', () => {
  // The exact acceptance criterion this surface exists to satisfy
  // (STEP-9-SPEC.md acceptance criterion 21 / this wave's own brief item
  // 1): "an operator-role principal can list, read and run a macro
  // through both clients... this is the criterion that catches the UI
  // rendering an empty list for the role the actual operator signs in
  // as." operatorSession above holds show:macro:run and NOT config:write
  // — the exact shape of the real "operator" role.
  it('renders the macro list for a principal holding ONLY show:macro:run (the operator role)', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderMacros(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    expect(listConfigObjects).toHaveBeenCalledWith('show.macro')
    // The operator role must not see "New macro" (config:write-gated).
    expect(screen.queryByText('New macro')).toBeNull()
  })

  it('also renders the macro list for a principal holding ONLY config:write (an admin who never runs a show)', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderMacros(makeModel({ session: adminSession }))

    await waitFor(() => expect(screen.getByText('Begin set')).toBeVisible())
    expect(screen.getByText('New macro')).toBeVisible()
  })

  it('renders a stated reason, never an empty list, for a principal holding neither scope', () => {
    renderMacros(
      makeModel({
        session: makeAuthenticatedSession({
          principal: { id: 'p-3', name: 'viewer-1', kind: 'human', role: 'viewer' },
          scopes: ['node:read'],
        }),
      }),
    )
    expect(listConfigObjects).not.toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toMatch(/does not include/)
  })

  it('states plainly when no macros are configured yet, rather than rendering an empty table', async () => {
    listConfigObjects.mockResolvedValue({ ...listResponse, objects: [] })
    renderMacros(makeModel({ session: operatorSession }))
    await waitFor(() => expect(screen.getByText('No show macros are configured yet.')).toBeVisible())
  })
})
