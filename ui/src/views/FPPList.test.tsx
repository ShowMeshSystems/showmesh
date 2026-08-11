import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { FPPList } from './FPPList'
import { ModelContext } from '../app/ModelContext'
import { makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

afterEach(cleanup)

function renderFPPList(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <FPPList />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('FPPList', () => {
  it('lists each configured instance with its health and endpoint', () => {
    const fpp = [
      makeFPPInstance('fpp-front', { health: 'healthy', endpoint: 'http://fpp-front.local' }),
      makeFPPInstance('fpp-back', { health: 'unknown', endpoint: 'http://fpp-back.local', lastPollError: 'timed out' }),
    ]
    renderFPPList(makeModel({ fpp }))

    expect(screen.getByText('fpp-front')).toBeInTheDocument()
    expect(screen.getByText('http://fpp-front.local')).toBeInTheDocument()
    expect(screen.getByText('healthy')).toBeInTheDocument()

    expect(screen.getByText('fpp-back')).toBeInTheDocument()
    // FPPList already handled unknown health correctly before this build
    // (per the D2 finding); this pins that it still does.
    expect(screen.getByText('unknown')).toBeInTheDocument()
    expect(screen.getByText('timed out')).toBeInTheDocument()
  })

  it('states plainly when no FPP instance is configured', () => {
    renderFPPList(makeModel({ fpp: [] }))
    expect(screen.getByText('No FPP instances are configured on this coordinator.')).toBeInTheDocument()
  })
})
