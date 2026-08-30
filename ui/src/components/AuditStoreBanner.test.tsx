import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { AuditStoreBanner } from './AuditStoreBanner'
import type { AuditStoreStatus } from '../app/types'

afterEach(cleanup)

describe('AuditStoreBanner', () => {
  it('renders nothing before the first snapshot has ever been applied', () => {
    const { container } = render(<AuditStoreBanner auditStore={null} />)
    expect(container.querySelector('.audit-store-banner')).toBeNull()
  })

  it('renders nothing when usable', () => {
    const auditStore: AuditStoreStatus = { state: 'usable', reason: null }
    const { container } = render(<AuditStoreBanner auditStore={auditStore} />)
    expect(container.querySelector('.audit-store-banner')).toBeNull()
  })

  // ADR-024 decision 11's amendment: "unknown" (no audit write attempted
  // yet since startup) is the ordinary state of a freshly started
  // coordinator, not evidence of a problem, so it must not render an
  // alert either.
  it('renders nothing when unknown', () => {
    const auditStore: AuditStoreStatus = { state: 'unknown', reason: null }
    const { container } = render(<AuditStoreBanner auditStore={auditStore} />)
    expect(container.querySelector('.audit-store-banner')).toBeNull()
  })

  it('renders a loud alert naming the reason when unusable', () => {
    const auditStore: AuditStoreStatus = {
      state: 'unusable',
      reason: 'the coordinator could not write an audit_log entry: disk full',
    }
    const { container, getByText } = render(<AuditStoreBanner auditStore={auditStore} />)
    const banner = container.querySelector('.audit-store-banner')
    expect(banner).not.toBeNull()
    expect(banner?.getAttribute('role')).toBe('alert')
    expect(getByText(/cannot currently write to its audit store/)).toBeInTheDocument()
    expect(getByText(/disk full/)).toBeInTheDocument()
  })

  it('never asserts the show or actions are stopped', () => {
    const auditStore: AuditStoreStatus = { state: 'unusable', reason: 'boom' }
    const { getByText } = render(<AuditStoreBanner auditStore={auditStore} />)
    expect(getByText(/continue to run normally/)).toBeInTheDocument()
  })
})
