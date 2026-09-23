import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { FPPPluginReportsRefusedBanner } from './FPPPluginReportsRefusedBanner'

describe('FPPPluginReportsRefusedBanner', () => {
  afterEach(cleanup)

  it('renders nothing when no instance carries the condition', () => {
    const { container } = render(
      <MemoryRouter>
        <FPPPluginReportsRefusedBanner instances={[]} />
      </MemoryRouter>,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it('renders one banner naming the instance and linking to its Monitor inspector', () => {
    render(
      <MemoryRouter>
        <FPPPluginReportsRefusedBanner instances={[{ instanceId: 'bench-fpp' }]} />
      </MemoryRouter>,
    )
    expect(
      screen.getByText('FPP bench-fpp reports are being refused. Open Monitor and clear the playlist observation.'),
    ).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Open Monitor' })
    expect(link.getAttribute('href')).toBe('/monitor/fleet?resource=fpp%3Abench-fpp')
  })

  it('renders one banner per refused instance', () => {
    render(
      <MemoryRouter>
        <FPPPluginReportsRefusedBanner instances={[{ instanceId: 'bench-fpp' }, { instanceId: 'barn-player' }]} />
      </MemoryRouter>,
    )
    expect(screen.getAllByRole('region')).toHaveLength(2)
  })
})
