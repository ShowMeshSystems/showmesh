import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { CapabilityPanel, RenderSurfaceCapabilityPanel, resolveCapabilityPanel } from './CapabilityPanel'
import type { Capability } from '../app/types'

// See EvidenceValue.test.tsx for why this is registered explicitly here.
afterEach(cleanup)

describe('CapabilityPanel', () => {
  // The acceptance-criterion case: an identifier this build has never
  // been taught, with an attribute shape nothing hardcodes, still
  // renders every raw field rather than blanking or throwing.
  it('renders raw normalized fields for a capability identifier it has never seen', () => {
    const capability: Capability = {
      id: 'experimental.thermal-imaging.v9',
      version: 3,
      attributes: {
        resolutionPx: 640,
        vendor: 'unknown-future-vendor',
        calibrated: false,
      },
    }
    render(<CapabilityPanel capability={capability} />)

    expect(screen.getByText('experimental.thermal-imaging.v9')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('resolutionPx')).toBeInTheDocument()
    expect(screen.getByText('640')).toBeInTheDocument()
    expect(screen.getByText('vendor')).toBeInTheDocument()
    expect(screen.getByText('unknown-future-vendor')).toBeInTheDocument()
    expect(screen.getByText('calibrated')).toBeInTheDocument()
    expect(screen.getByText('false')).toBeInTheDocument()
  })

  it('states plainly when a capability advertises no attributes, rather than an empty table', () => {
    render(<CapabilityPanel capability={{ id: 'node.heartbeat', version: 1 }} />)
    expect(screen.getByText('none advertised')).toBeInTheDocument()
  })

  it('renders a nested attribute value rather than dropping it or crashing', () => {
    render(
      <CapabilityPanel
        capability={{ id: 'matrix.render', version: 2, attributes: { modes: ['a', 'b'] } }}
      />,
    )
    expect(screen.getByText('["a","b"]')).toBeInTheDocument()
  })
})

describe('resolveCapabilityPanel', () => {
  it('resolves render.surface to RenderSurfaceCapabilityPanel', () => {
    expect(resolveCapabilityPanel('render.surface')).toBe(RenderSurfaceCapabilityPanel)
  })

  it('falls back to the generic CapabilityPanel for every other id, known or not', () => {
    expect(resolveCapabilityPanel('transport.ndi.send')).toBe(CapabilityPanel)
    expect(resolveCapabilityPanel('experimental.thermal-imaging.v9')).toBe(CapabilityPanel)
  })
})

describe('RenderSurfaceCapabilityPanel', () => {
  it('renders each measured pixel format', () => {
    render(
      <RenderSurfaceCapabilityPanel
        capability={{ id: 'render.surface', version: 1, attributes: { pixelFormats: ['UYVY', 'I420'] } }}
      />,
    )
    expect(screen.getByText('render.surface')).toBeInTheDocument()
    expect(screen.getByText('UYVY, I420')).toBeInTheDocument()
  })

  // The degradation case this component exists to get right: a
  // render.surface capability with no pixelFormats attribute (or a
  // malformed one) must still render, never throw, and must never invent
  // a format nothing measured.
  it('states plainly when no pixel format was ever measured, rather than guessing', () => {
    render(<RenderSurfaceCapabilityPanel capability={{ id: 'render.surface', version: 1 }} />)
    expect(screen.getByText('none advertised')).toBeInTheDocument()
  })

  it('degrades to "none advertised" rather than throwing on a malformed attribute', () => {
    render(
      <RenderSurfaceCapabilityPanel
        capability={{ id: 'render.surface', version: 1, attributes: { pixelFormats: 'not-an-array' } }}
      />,
    )
    expect(screen.getByText('none advertised')).toBeInTheDocument()
  })
})
