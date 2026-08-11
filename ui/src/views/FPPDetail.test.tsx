import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { FPPDetail } from './FPPDetail'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderFPPDetail(instanceId: string, model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/fpp/${instanceId}`]}>
        <Routes>
          <Route path="/fpp/:instanceId" element={<FPPDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('FPPDetail', () => {
  it('renders instance summary and drills each observation down through the shared evidence renderer', () => {
    const instance = makeFPPInstance('fpp-1', {
      health: 'degraded',
      lastPollError: 'HTTP 503',
      observations: [
        makeEvidence({ signal: 'fpp.multisync.enabled', value: true, state: 'current' }),
        makeEvidence({
          signal: 'fpp.status.playlist',
          value: null,
          state: 'collection_failed',
          reason: 'HTTP 503 from FPP',
          observedAt: null,
        }),
      ],
    })
    renderFPPDetail('fpp-1', makeModel({ fpp: [instance] }))

    expect(screen.getByText('degraded')).toBeInTheDocument()
    expect(screen.getByText('HTTP 503')).toBeInTheDocument()
    expect(screen.getByText('fpp.multisync.enabled')).toBeInTheDocument()
    expect(screen.getByText('fpp.status.playlist')).toBeInTheDocument()
    expect(screen.getByText('collection failed')).toBeInTheDocument()
    expect(screen.getByText('HTTP 503 from FPP')).toBeInTheDocument()
  })

  it('states plainly when no FPP instance matches the route', () => {
    renderFPPDetail('missing', makeModel({ fpp: [makeFPPInstance('fpp-1')] }))
    expect(screen.getByText(/No FPP instance with ID "missing"/)).toBeInTheDocument()
  })
})
