import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { PortGrid } from './PortGrid'
import { makeEvidence } from '../app/test-support/fixtures'
import type { Evidence } from '../app/types'

afterEach(cleanup)

function evidence(signal: string, overrides: Partial<Evidence> = {}): Evidence {
  return makeEvidence({ signal, ...overrides })
}

function outputPort(key: string, ma: number, extra: Partial<Evidence> = {}): Evidence[] {
  return [
    evidence(`fpp.port.${key}.kind`, { value: 'output' }),
    evidence(`fpp.port.${key}.current_ma`, { value: ma, unit: 'milliamps', ...extra }),
    evidence(`fpp.port.${key}.enabled`, { value: true }),
    evidence(`fpp.port.${key}.status`, { value: true }),
    evidence(`fpp.port.${key}.bank`, { value: 'Ports 1-4' }),
  ]
}

function smartReceiverPort(key: string): Evidence[] {
  return [
    evidence(`fpp.port.${key}.kind`, { value: 'smart_receiver' }),
    evidence(`fpp.port.${key}.current_ma`, {
      value: null,
      unit: 'milliamps',
      state: 'unsupported',
      reason: 'smart receiver position: pre-V5 receivers report no per-port current',
      observedAt: null,
    }),
  ]
}

describe('PortGrid', () => {
  // The single most important assertion in this seam: a measured 0 mA and
  // a smart-receiver blind spot must never read the same way. Both exist
  // on the real fleet right now (every energized port reads 0 mA; every
  // pre-V5 smart receiver reports no current at all) and sit side by
  // side.
  it('visually and textually distinguishes a measured 0 mA from a smart-receiver blind spot', () => {
    const observations = [
      evidence('fpp.ports.count', { value: 2 }),
      evidence('fpp.ports.blind_count', { value: 1 }),
      ...outputPort('port_1', 0),
      ...smartReceiverPort('port_17'),
    ]
    const { container } = render(<PortGrid observations={observations} />)

    // The measured cell shows the real number, with its own state class.
    const measuredCell = container.querySelector('.port-cell--measured')
    expect(measuredCell).not.toBeNull()
    expect(measuredCell?.textContent).toContain('0 milliamps')

    // The blind-spot cell must NOT contain "0" anywhere and must use a
    // different structural class (the hatched styling hook), not just a
    // different color.
    const blindCell = container.querySelector('.port-cell--blind_spot')
    expect(blindCell).not.toBeNull()
    expect(blindCell?.textContent).toContain('blind spot')
    expect(blindCell?.textContent).not.toMatch(/\b0\b/)
    expect(blindCell?.className).not.toContain('measured')

    // Both cells are present and are different elements.
    expect(measuredCell).not.toBe(blindCell)
  })

  it('visually and textually distinguishes a failed-to-collect port from both a measurement and a blind spot', () => {
    const observations = [
      evidence('fpp.ports.count', { value: 1 }),
      evidence('fpp.ports.blind_count', { value: 0 }),
      ...outputPort('port_3', 0, {
        value: null,
        state: 'collection_failed',
        reason: 'HTTP request to /api/fppd/ports timed out mid-poll',
        observedAt: null,
      }),
    ]
    const { container } = render(<PortGrid observations={observations} />)
    const failedCell = container.querySelector('.port-cell--failed')
    expect(failedCell).not.toBeNull()
    expect(failedCell?.textContent).toContain('failed to collect')
    expect(failedCell?.textContent).not.toMatch(/\b0\b/)
    expect(failedCell?.className).not.toContain('blind_spot')
    expect(failedCell?.className).not.toContain('measured')
  })

  it('states plainly that a host with a zero-length ports array reports no pixel output ports, as a fact rather than an error or blank panel', () => {
    render(<PortGrid observations={[evidence('fpp.ports.count', { value: 0 }), evidence('fpp.ports.blind_count', { value: 0 })]} />)
    expect(screen.getByText('This host reports no pixel output ports.')).toBeInTheDocument()
    expect(screen.queryByText(/error/i)).not.toBeInTheDocument()
  })

  it('states plainly when port inventory has not been collected at all, rather than rendering blank', () => {
    render(<PortGrid observations={[]} />)
    expect(screen.getByText('Port inventory has not been collected for this instance yet.')).toBeInTheDocument()
  })

  it('states the reason when fpp.ports.count itself is an absence, without guessing zero', () => {
    render(
      <PortGrid
        observations={[
          evidence('fpp.ports.count', {
            value: null,
            state: 'collection_failed',
            reason: 'HTTP 500 from FPP',
            observedAt: null,
          }),
        ]}
      />,
    )
    expect(screen.getByText(/Port inventory could not be read.*HTTP 500 from FPP/)).toBeInTheDocument()
    expect(screen.queryByText('This host reports no pixel output ports.')).not.toBeInTheDocument()
  })

  it('surfaces a port decode failure prominently, without dropping the rest of the grid', () => {
    const observations = [
      evidence('fpp.ports.count', { value: 1 }),
      evidence('fpp.ports.blind_count', { value: 0 }),
      evidence('fpp.ports.decode_failed', {
        value: null,
        state: 'collection_failed',
        reason: 'two port elements normalized to the same key "port_1"',
        observedAt: null,
      }),
      ...outputPort('port_1', 0),
    ]
    render(<PortGrid observations={observations} />)
    expect(screen.getByRole('alert')).toHaveTextContent('two port elements normalized to the same key "port_1"')
    expect(screen.getByText('Port 1')).toBeInTheDocument()
  })

  it('renders a port with an unrecognized or missing kind in its own section, rather than dropping it', () => {
    const observations = [
      evidence('fpp.ports.count', { value: 1 }),
      evidence('fpp.ports.blind_count', { value: 0 }),
      evidence('fpp.port.port_9.current_ma', { value: 5, unit: 'milliamps' }),
      // deliberately no fpp.port.port_9.kind
    ]
    render(<PortGrid observations={observations} />)
    expect(screen.getByText(/Ports with an unrecognized kind/)).toBeInTheDocument()
    expect(screen.getByText('Port 9')).toBeInTheDocument()
  })

  it('groups output ports and smart-receiver positions into separately labeled sections', () => {
    const observations = [
      evidence('fpp.ports.count', { value: 2 }),
      evidence('fpp.ports.blind_count', { value: 1 }),
      ...outputPort('port_1', 0),
      ...smartReceiverPort('port_17'),
    ]
    render(<PortGrid observations={observations} />)
    expect(screen.getByText(/Output ports \(1\)/)).toBeInTheDocument()
    expect(screen.getByText(/Smart-receiver positions.*\(1\)/)).toBeInTheDocument()
  })
})
