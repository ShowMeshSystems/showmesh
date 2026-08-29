import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FPPVersionSkewNotice } from './FPPList'
import { ModelContext } from '../app/ModelContext'
import { makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import { makeEvidence } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

// Formerly FPPList.test.tsx, covering the standalone `/fpp` route's
// table. Monitor's Fleet facet now owns per-instance listing (see
// Monitor.test.tsx's fleet-table coverage); this file's remaining job is
// FPPVersionSkewNotice, the one fact the Fleet table's per-row shape
// cannot state on its own.
afterEach(cleanup)

function renderNotice(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <FPPVersionSkewNotice />
    </ModelContext.Provider>,
  )
}

describe('FPPVersionSkewNotice', () => {
  it('renders nothing when the fleet reports no version disagreement', () => {
    const fpp = [
      makeFPPInstance('fpp-front', {
        observations: [makeEvidence({ signal: 'fpp.version', value: '8.1' })],
      }),
      makeFPPInstance('fpp-back', {
        observations: [makeEvidence({ signal: 'fpp.version', value: '8.1' })],
      }),
    ]
    const { container } = renderNotice(makeModel({ fpp }))
    expect(container).toBeEmptyDOMElement()
  })

  it('states a version disagreement across the fleet without ranking either version as right or wrong', () => {
    const fpp = [
      makeFPPInstance('fpp-front', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true }),
          makeEvidence({ signal: 'fpp.version', value: '8.1' }),
        ],
      }),
      makeFPPInstance('fpp-back', {
        observations: [
          makeEvidence({ signal: 'fpp.reachable', value: true }),
          makeEvidence({ signal: 'fpp.version', value: '9.0-master' }),
        ],
      }),
    ]
    renderNotice(makeModel({ fpp }))
    expect(screen.getByText(/do not agree across the fleet/)).toBeInTheDocument()
    expect(screen.getByText(/8\.1 \(1\)/)).toBeInTheDocument()
    expect(screen.getByText(/9\.0-master \(1\)/)).toBeInTheDocument()
  })
})
