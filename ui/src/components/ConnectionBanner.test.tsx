import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ConnectionBanner } from './ConnectionBanner'
import type { ConnectionState } from '../app/types'

// See EvidenceValue.test.tsx for why this is registered explicitly here.
afterEach(cleanup)

describe('ConnectionBanner', () => {
  // spec section 6.3: the connection banner (a browser/coordinator
  // problem) must not share a visual language with per-resource
  // evidence/health rendering (a coordinator/thing problem). This test
  // pins the mechanism that keeps them apart: the banner never emits the
  // `status-badge` class family EvidenceValue/DomainBadges use.
  it('never reuses the per-resource status-badge classes', () => {
    const states: ConnectionState[] = [
      { kind: 'connecting' },
      { kind: 'live', connectedAt: 0 },
      { kind: 'reconnecting', attempt: 1, nextAttemptAt: 0, lastError: 'boom' },
      { kind: 'unauthorized', reason: 'missing' },
      { kind: 'incompatible', requiredVersion: 2, supportedVersions: [1], detail: 'nope' },
      { kind: 'failed', detail: 'boom' },
    ]
    for (const connection of states) {
      const { container, unmount } = render(<ConnectionBanner connection={connection} />)
      expect(container.querySelector('.status-badge')).toBeNull()
      expect(container.querySelector('.connection-banner')).not.toBeNull()
      unmount()
    }
  })

  it('distinguishes a missing token from a rejected one', () => {
    const { getByText } = render(<ConnectionBanner connection={{ kind: 'unauthorized', reason: 'missing' }} />)
    expect(getByText(/requires an API token/)).toBeInTheDocument()
  })

  it('says the token was rejected, not that one is merely required, once one was tried', () => {
    const { getByText } = render(<ConnectionBanner connection={{ kind: 'unauthorized', reason: 'rejected' }} />)
    expect(getByText(/was rejected/)).toBeInTheDocument()
  })

  // OPERATOR-UI section 7: this exact sentence is what keeps "the UI lost
  // its connection" from being misread as "the show stopped." Pinned so a
  // rewrite (e.g. to something like "The show has stopped.") is caught,
  // not just the surrounding attempt count/lastError which a rewrite
  // could easily preserve.
  it('states plainly, while reconnecting, that this is a browser/network problem and the show continues', () => {
    render(
      <ConnectionBanner
        connection={{ kind: 'reconnecting', attempt: 3, nextAttemptAt: 0, lastError: 'timed out' }}
      />,
    )
    expect(
      screen.getByText(/This is a browser\/network problem, not a report about the show/),
    ).toBeInTheDocument()
    expect(screen.getByText(/the show continues regardless/)).toBeInTheDocument()
    expect(screen.getByText(/attempt 3/)).toBeInTheDocument()
    expect(screen.getByText('timed out')).toBeInTheDocument()
  })

  it('shows a distinct live confirmation, not shared wording with any failure state', () => {
    render(<ConnectionBanner connection={{ kind: 'live', connectedAt: 0 }} />)
    expect(screen.getByText(/Live/)).toBeInTheDocument()
    expect(screen.getByText(/connected to the coordinator/)).toBeInTheDocument()
    expect(screen.queryByText(/browser\/network problem/)).not.toBeInTheDocument()
  })

  it('states the version mismatch explicitly and says reconnecting will not help', () => {
    render(
      <ConnectionBanner
        connection={{
          kind: 'incompatible',
          requiredVersion: 2,
          supportedVersions: [1],
          detail: 'coordinator reported version 1',
        }}
      />,
    )
    expect(screen.getByText(/requires control API version 2/)).toBeInTheDocument()
    expect(screen.getByText(/the coordinator only supports version 1/)).toBeInTheDocument()
    expect(screen.getByText(/Reconnecting will not help/)).toBeInTheDocument()
  })

  // D3: an empty supportedVersions (thrown by the client when the
  // ShowMesh-API-Version response header is missing or unparseable, per
  // api/client.ts's checkVersionHeader) must not render as "...only
  // supports versions ." -- an assertion about the coordinator made from
  // no information. Acceptance criterion 5.
  it('does not assert what the coordinator supports when it reported no version information', () => {
    render(
      <ConnectionBanner
        connection={{
          kind: 'incompatible',
          requiredVersion: 1,
          supportedVersions: [],
          detail: 'response carried no ShowMesh-API-Version header',
        }}
      />,
    )
    expect(screen.queryByText(/only supports version/)).not.toBeInTheDocument()
    expect(screen.getByText(/did not report which versions it supports/)).toBeInTheDocument()
    expect(screen.getByText(/response carried no ShowMesh-API-Version header/)).toBeInTheDocument()
  })
})
