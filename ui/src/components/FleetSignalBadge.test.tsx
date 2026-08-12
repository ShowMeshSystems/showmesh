import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { FleetSignalBadge } from './FleetSignalBadge'
import { makeEvidence } from '../app/test-support/fixtures'

afterEach(cleanup)

describe('FleetSignalBadge', () => {
  it('renders a value and its state for a present evidence envelope', () => {
    render(<FleetSignalBadge evidence={makeEvidence({ signal: 'fpp.status', value: 'idle', state: 'current' })} />)
    expect(screen.getByText('idle')).toBeInTheDocument()
  })

  it('prefixes the label when one is given', () => {
    render(<FleetSignalBadge label="ports" evidence={makeEvidence({ signal: 'fpp.ports.count', value: 48 })} />)
    expect(screen.getByText('ports: 48')).toBeInTheDocument()
  })

  // The absence-is-stated rule, at the smallest possible unit: an
  // undefined evidence (this signal was never even attempted) must still
  // render a visible "not collected" badge, never nothing.
  it('states "not collected" rather than rendering blank when evidence is undefined', () => {
    const { container } = render(<FleetSignalBadge evidence={undefined} />)
    expect(screen.getByText('not collected')).toBeInTheDocument()
    expect(container.querySelector('.status-badge')).not.toBeNull()
  })
})
