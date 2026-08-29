import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { initialModel } from '../api/domain'
import { ModelContext } from '../app/ModelContext'

let listCalls = 0
let cueCalls = 0

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return {
    ...actual,
    listConfigObjects: (kind: string) => {
      listCalls += 1
      return Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind,
        objects: [{ id: 'a', label: 'A', show: 'S', currentRevision: 1, updatedAt: '2026-08-28T12:00:00Z' }],
      })
    },
    getShowCue: (id: string) => {
      cueCalls += 1
      return Promise.resolve({
        serverTime: '2026-08-28T21:07:00Z',
        kind: 'show.cue',
        id,
        revision: 1,
        payload: { show: 'S', name: id, outputs: { announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 400 } } },
        updatedAt: '2026-08-28T12:00:00Z',
        createdByPrincipalId: null,
        createdByPrincipalName: null,
        source: 'api',
      })
    },
  }
})

const { LiveControl } = await import('./LiveControl')

/* Three lists and one cue read per mount. A screen that re-reads on every
 * render is unusable against a real coordinator. */
describe('LiveControl reads', () => {
  it('reads each list once per mount, not once per render', async () => {
    render(
      <ModelContext.Provider
        value={{
          ...initialModel(),
          currentRuns: {
            serverTime: '2026-08-28T21:07:00Z',
            activeShow: { configured: true, show: 'S', generation: 1 },
            runs: [],
          } as never,
        }}
      >
        <MemoryRouter>
          <LiveControl />
        </MemoryRouter>
      </ModelContext.Provider>,
    )
    await screen.findAllByText('A')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect({ listCalls, cueCalls }).toEqual({ listCalls: 3, cueCalls: 1 })
  })
})
