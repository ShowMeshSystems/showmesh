import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ControlPlaneBadge } from './DomainBadges'

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
