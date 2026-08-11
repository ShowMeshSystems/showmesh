import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { DataFreshnessNotice } from './DataFreshnessNotice'
import type { ConnectionState } from '../app/types'

// See EvidenceValue.test.tsx for why this is registered explicitly here.
afterEach(cleanup)

const NOW = new Date('2026-08-11T12:05:00.000Z').getTime()
const RECEIVED_90S_AGO = NOW - 90_000

describe('DataFreshnessNotice', () => {
  it('shows last-updated age, without disconnection wording, while live', () => {
    const live: ConnectionState = { kind: 'live', connectedAt: NOW - 300_000 }
    render(<DataFreshnessNotice connection={live} snapshotReceivedAt={RECEIVED_90S_AGO} now={NOW} />)
    expect(screen.getByText(/Last updated 1m ago\./)).toBeInTheDocument()
  })

  // The disconnected-state case named in the spec: last-known values
  // render with their age, never as current.
  it('shows the data as last-known with its age, not as current, while reconnecting', () => {
    const reconnecting: ConnectionState = {
      kind: 'reconnecting',
      attempt: 3,
      nextAttemptAt: NOW + 5_000,
      lastError: 'network error',
    }
    render(<DataFreshnessNotice connection={reconnecting} snapshotReceivedAt={RECEIVED_90S_AGO} now={NOW} />)

    const notice = screen.getByRole('status')
    expect(notice.textContent).toMatch(/last known data/i)
    expect(notice.textContent).toMatch(/1m ago/)
    expect(notice.textContent).toMatch(/not connected/i)
    // Must not read as a claim that the data is current.
    expect(notice.textContent?.toLowerCase()).not.toContain('current')
  })

  it('states plainly that no data has been received yet, when there is no snapshot', () => {
    const connecting: ConnectionState = { kind: 'connecting' }
    render(<DataFreshnessNotice connection={connecting} snapshotReceivedAt={null} now={NOW} />)
    expect(screen.getByText(/No data received from the coordinator yet\./)).toBeInTheDocument()
  })
})
