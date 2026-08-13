import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ControlPlaneBadge, DeclarationBadge } from './DomainBadges'

// See EvidenceValue.test.tsx for why this is registered explicitly here.
afterEach(cleanup)

describe('ControlPlaneBadge', () => {
  // openapi.yaml's ControlPlane schema and CLAUDE.md are explicit that
  // "offline" describes only the MQTT control-plane connection and must
  // not be readable as the node -- or a show it is running -- being dead.
  // This test pins the wording so a future edit cannot casually shorten
  // it back to a bare "offline"/"dead" label.
  it('words the offline state as a control-plane condition, never as the node being dead', () => {
    render(<ControlPlaneBadge state="offline" />)
    const label = screen.getByText(/control-plane/i)
    expect(label.textContent?.toLowerCase()).not.toContain('dead')
    expect(label.textContent?.toLowerCase()).not.toBe('offline')
  })

  it('renders both an icon and a text label, never color alone, for every state', () => {
    for (const state of ['online', 'offline', 'unknown'] as const) {
      const { container, unmount } = render(<ControlPlaneBadge state={state} />)
      const badge = container.querySelector('.status-badge')
      const icon = badge?.querySelector('.status-badge__icon')
      expect(icon).not.toBeNull()
      expect(icon?.textContent?.length).toBeGreaterThan(0)

      // The icon alone is not the claim this test names: a text label
      // must accompany it, in a *different* element than the icon (so a
      // blanked-but-present label element cannot pass this by
      // coincidentally sharing the icon's own non-empty text).
      const label = Array.from(badge?.querySelectorAll('span') ?? []).find(
        (span) => !span.classList.contains('status-badge__icon'),
      )
      expect(label).not.toBeUndefined()
      expect(label?.textContent?.trim().length).toBeGreaterThan(0)
      unmount()
    }
  })
})

// BUILD-PLAN Step 7 seam B (RES-008 D2/D6).
describe('DeclarationBadge', () => {
  it('renders "not declared" for an undeclared node regardless of discoveryState', () => {
    render(<DeclarationBadge declared={false} discoveryState="not_applicable" />)
    expect(screen.getByText('not declared')).toBeInTheDocument()
  })

  // The whole reason RES-008 D6 exists: a declared node a discovery run
  // did not see must be visibly flagged, never indistinguishable from
  // "present".
  it('flags a declared node the most recent run did not see, distinctly from present', () => {
    const { unmount } = render(<DeclarationBadge declared={true} discoveryState="present" />)
    expect(screen.getByText(/seen by discovery/)).toBeInTheDocument()
    unmount()

    render(<DeclarationBadge declared={true} discoveryState="not_seen" />)
    const label = screen.getByText(/not seen/i)
    expect(label.textContent).not.toMatch(/^seen by discovery$/)
  })

  it('never renders unknown discovery evidence as though it were fine', () => {
    render(<DeclarationBadge declared={true} discoveryState="unknown" />)
    expect(screen.getByText(/unknown/i)).toBeInTheDocument()
  })

  // ADR-020: within a major version the contract is additive-only and
  // clients must tolerate an unknown value, never assume the union
  // TypeScript sees today is exhaustive at runtime. Before this test's
  // fix, `DISCOVERY_STATE[discoveryState]` indexed to `undefined` for any
  // value outside the four known ones, and the immediate `spec.tone`
  // dereference threw -- which, unhandled in a render pass, unmounts the
  // whole route, not just this one badge. Cast bypasses the compile-time
  // union deliberately, the same way a real coordinator response would at
  // runtime.
  it('renders a fallback rather than crashing on an unrecognized discoveryState', () => {
    const unrecognized = 'archived' as unknown as Parameters<typeof DeclarationBadge>[0]['discoveryState']
    expect(() => render(<DeclarationBadge declared={true} discoveryState={unrecognized} />)).not.toThrow()
    expect(screen.getByText(/unrecognized/i)).toBeInTheDocument()
  })
})
