import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { EvidenceValue } from './EvidenceValue'
import type { Evidence } from '../app/types'

// vitest.config.ts (seam A) does not set `test.globals: true`, so
// @testing-library/react's automatic `afterEach(cleanup)` registration
// never triggers (it detects a global `afterEach`, which does not exist
// here) -- each test file in this seam registers it explicitly instead,
// rather than editing a file this seam does not own.
afterEach(cleanup)

// Fixture builder: every field explicit, so each test only varies what it
// is actually testing. No import from `ui/src/api` -- this is a locally
// constructed object matching this seam's own `Evidence` type
// (app/types.ts), which structurally mirrors api/openapi.yaml's Evidence
// schema, per this builder's instructions on running tests today without
// seam B.
function evidence(overrides: Partial<Evidence>): Evidence {
  return {
    signal: 'fpp.multisync.enabled',
    value: true,
    unit: null,
    state: 'current',
    reason: null,
    observedAt: '2026-08-11T12:00:00.000Z',
    collectedAt: '2026-08-11T12:00:00.500Z',
    source: 'fpp-collector',
    quality: 'direct',
    validForSeconds: 30,
    ...overrides,
  }
}

const SERVER_TIME = '2026-08-11T12:00:05.000Z'

describe('EvidenceValue', () => {
  it('renders a current value with an age and never renders a reason, even if the payload includes one', () => {
    // `reason: null` alone does not exercise the `state !== 'current'`
    // guard -- a fixture with reason already null passes whether or not
    // that guard exists at all. This sends a non-null reason on a
    // "current" evidence envelope specifically so the guard has something
    // to actually suppress; a component that dropped the state check
    // (rendering on `reason !== null` alone) would show it.
    const { container } = render(
      <EvidenceValue
        evidence={evidence({ state: 'current', reason: 'should never be shown while current' })}
        serverTime={SERVER_TIME}
      />,
    )
    expect(screen.getByText('true')).toBeInTheDocument()
    expect(screen.getByText('current')).toBeInTheDocument()
    expect(screen.getByText(/observed/)).toBeInTheDocument()
    expect(container.querySelector('.evidence__reason')).toBeNull()
    expect(screen.queryByText('should never be shown while current')).not.toBeInTheDocument()
  })

  // D4: the contract guarantees `reason` is non-null whenever `state` is
  // not "current". A coordinator that violates that guarantee must render
  // as a visible contract violation, not as a stale/failed/etc. badge
  // with its explanation silently missing.
  it('renders a visible contract violation when a non-current state carries no reason', () => {
    const { container } = render(
      <EvidenceValue evidence={evidence({ state: 'stale', reason: null })} serverTime={SERVER_TIME} />,
    )
    expect(screen.getByText('stale')).toBeInTheDocument()
    const violation = container.querySelector('.evidence__reason--violation')
    expect(violation).not.toBeNull()
    expect(violation?.textContent).toMatch(/contract violation/i)
    expect(violation?.getAttribute('role')).toBe('alert')
  })

  it('renders a stale value together with its reason', () => {
    render(
      <EvidenceValue
        evidence={evidence({ state: 'stale', reason: 'no poll response in over 30s', value: 42 })}
        serverTime={SERVER_TIME}
      />,
    )
    expect(screen.getByText('42')).toBeInTheDocument()
    expect(screen.getByText('stale')).toBeInTheDocument()
    expect(screen.getByText('no poll response in over 30s')).toBeInTheDocument()
  })

  // The load-bearing case named in the spec: `unknown_age` HAS a value.
  // A client that treats it as absent is making exactly the reading error
  // that state was invented to prevent.
  it('renders the value for unknown_age, not as absent, alongside its reason', () => {
    render(
      <EvidenceValue
        evidence={evidence({
          state: 'unknown_age',
          value: 'retained',
          reason: 'retained MQTT delivery replayed on reconnect',
          observedAt: null,
        })}
        serverTime={SERVER_TIME}
      />,
    )
    const valueEl = screen.getByText('retained')
    expect(valueEl).toBeInTheDocument()
    expect(screen.getByText('age unknown')).toBeInTheDocument()
    expect(screen.getByText('retained MQTT delivery replayed on reconnect')).toBeInTheDocument()
    // The value must not be rendered through the "absent" styling class,
    // and must not print the "no value" placeholder used for genuine
    // absence. Asserted on the class itself, not merely on the text
    // content, because deleting the absent/present class switch entirely
    // (always rendering "evidence__value") would otherwise leave every
    // assertion above green with nothing here to catch it.
    expect(valueEl.className).toBe('evidence__value')
    expect(valueEl.className).not.toContain('absent')
    expect(screen.queryByText('no value')).not.toBeInTheDocument()
  })

  it('renders not_collected as a genuine absence, with its reason', () => {
    render(
      <EvidenceValue
        evidence={evidence({
          state: 'not_collected',
          value: null,
          reason: 'no collector configured for this signal',
          observedAt: null,
          collectedAt: null,
        })}
        serverTime={SERVER_TIME}
      />,
    )
    const valueEl = screen.getByText('no value')
    expect(valueEl).toBeInTheDocument()
    // The absence side of the same class switch T4 pins on the
    // unknown_age case above: a genuinely absent value must render
    // through the "absent" styling class, not merely display the "no
    // value" text.
    expect(valueEl.className).toContain('evidence__value--absent')
    expect(screen.getByText('not collected')).toBeInTheDocument()
    expect(screen.getByText('no collection has ever been attempted')).toBeInTheDocument()
    expect(screen.getByText('no collector configured for this signal')).toBeInTheDocument()
  })

  it('renders collection_failed as a genuine absence, with its reason', () => {
    render(
      <EvidenceValue
        evidence={evidence({
          state: 'collection_failed',
          value: null,
          reason: 'HTTP 500 from FPP',
          observedAt: null,
        })}
        serverTime={SERVER_TIME}
      />,
    )
    const valueEl = screen.getByText('no value')
    expect(valueEl.className).toContain('evidence__value--absent')
    expect(screen.getByText('collection failed')).toBeInTheDocument()
    expect(screen.getByText('HTTP 500 from FPP')).toBeInTheDocument()
  })

  it('renders unsupported as a genuine absence, with its reason', () => {
    render(
      <EvidenceValue
        evidence={evidence({
          state: 'unsupported',
          value: null,
          reason: 'this FPP version does not report this signal',
          observedAt: null,
        })}
        serverTime={SERVER_TIME}
      />,
    )
    const valueEl = screen.getByText('no value')
    expect(valueEl.className).toContain('evidence__value--absent')
    expect(screen.getByText('not supported')).toBeInTheDocument()
    expect(screen.getByText('this FPP version does not report this signal')).toBeInTheDocument()
  })

  it('never renders a bare value with no freshness context', () => {
    const { container } = render(
      <EvidenceValue evidence={evidence({ state: 'current' })} serverTime={SERVER_TIME} />,
    )
    // The freshness line is always present alongside the value.
    expect(container.querySelector('.evidence__age')).not.toBeNull()
  })

  // spec section 5.3 / this component's own top comment: collectedAt is
  // collection bookkeeping, never evidence of the subject's own state,
  // and must never be presented as a freshness answer about the value.
  // Pins both the wording (so "observed X" cannot silently stand in for
  // it) and the branch selection (observedAt takes priority over
  // collectedAt whenever both are known).
  describe('collectedAt bookkeeping wording', () => {
    it('renders the bookkeeping phrase, distinct from an observed age, when observedAt is unknown but collectedAt is known', () => {
      render(
        <EvidenceValue
          evidence={evidence({
            state: 'unknown_age',
            value: 'retained',
            observedAt: null,
            collectedAt: '2026-08-11T12:00:00.000Z',
          })}
          serverTime={SERVER_TIME}
        />,
      )
      expect(
        screen.getByText(
          /observation time unknown; the coordinator last attempted collection/,
        ),
      ).toBeInTheDocument()
      // Must never read as though collectedAt were the value's own
      // observed freshness.
      expect(screen.queryByText(/^observed /)).not.toBeInTheDocument()
    })

    it('prefers the observed age over the bookkeeping phrase when both observedAt and collectedAt are known', () => {
      render(
        <EvidenceValue
          evidence={evidence({
            observedAt: '2026-08-11T12:00:00.000Z',
            collectedAt: '2026-08-11T11:00:00.000Z',
          })}
          serverTime={SERVER_TIME}
        />,
      )
      expect(screen.getByText(/^observed /)).toBeInTheDocument()
      expect(screen.queryByText(/last attempted collection/)).not.toBeInTheDocument()
    })

    it('states plainly that no collection has ever been attempted when neither timestamp is known', () => {
      render(
        <EvidenceValue
          evidence={evidence({ state: 'not_collected', value: null, observedAt: null, collectedAt: null })}
          serverTime={SERVER_TIME}
        />,
      )
      expect(screen.getByText('no collection has ever been attempted')).toBeInTheDocument()
    })
  })

  // The reported defect (see this repo's fix commit / build log): with the
  // page connected and then the coordinator gone, the evidence panel kept
  // reading "observed just now" for over a minute, because the age was
  // computed against a `serverTime` that had stopped updating. These pin
  // the fix -- `serverTimeReceivedAt` plus `now` let the age keep
  // advancing between responses -- and the disconnected "as of last
  // contact" qualifier that accompanies it.
  describe('advancing age without a new response (serverTimeReceivedAt + now)', () => {
    const RECEIVED_AT = 5_000_000 // arbitrary browser-clock epoch ms
    const SERVER_TIME_AT_RECEIPT = new Date(RECEIVED_AT).toISOString()
    const OBSERVED_AT = new Date(RECEIVED_AT - 90_000).toISOString() // 90s before serverTime was captured

    it('advances the observed age as `now` moves forward, with no new serverTime', () => {
      const { rerender } = render(
        <EvidenceValue
          evidence={evidence({ observedAt: OBSERVED_AT })}
          serverTime={SERVER_TIME_AT_RECEIPT}
          serverTimeReceivedAt={RECEIVED_AT}
          now={RECEIVED_AT}
        />,
      )
      expect(screen.getByText('observed 1m ago')).toBeInTheDocument()

      // 90 more seconds of real (browser-clock) time pass. Nothing about
      // the evidence or serverTime changes -- this is the disconnected
      // case, where no new response ever arrives.
      rerender(
        <EvidenceValue
          evidence={evidence({ observedAt: OBSERVED_AT })}
          serverTime={SERVER_TIME_AT_RECEIPT}
          serverTimeReceivedAt={RECEIVED_AT}
          now={RECEIVED_AT + 90_000}
        />,
      )
      expect(screen.getByText('observed 3m ago')).toBeInTheDocument()
      expect(screen.queryByText('observed 1m ago')).not.toBeInTheDocument()
    })

    it('advances the collectedAt bookkeeping age the same way, for evidence with no observedAt', () => {
      const collectedAt = new Date(RECEIVED_AT - 20_000).toISOString() // 20s before serverTime was captured
      const { rerender } = render(
        <EvidenceValue
          evidence={evidence({ state: 'unknown_age', observedAt: null, collectedAt })}
          serverTime={SERVER_TIME_AT_RECEIPT}
          serverTimeReceivedAt={RECEIVED_AT}
          now={RECEIVED_AT}
        />,
      )
      expect(screen.getByText(/last attempted collection 20s ago/)).toBeInTheDocument()

      rerender(
        <EvidenceValue
          evidence={evidence({ state: 'unknown_age', observedAt: null, collectedAt })}
          serverTime={SERVER_TIME_AT_RECEIPT}
          serverTimeReceivedAt={RECEIVED_AT}
          now={RECEIVED_AT + 40_000}
        />,
      )
      expect(screen.getByText(/last attempted collection 1m ago/)).toBeInTheDocument()
    })

    it('without serverTimeReceivedAt, stays frozen at the fixed serverTime regardless of `now` (documents the fallback, not the fix)', () => {
      const { rerender } = render(
        <EvidenceValue evidence={evidence({ observedAt: OBSERVED_AT })} serverTime={SERVER_TIME_AT_RECEIPT} />,
      )
      expect(screen.getByText('observed 1m ago')).toBeInTheDocument()

      rerender(
        <EvidenceValue evidence={evidence({ observedAt: OBSERVED_AT })} serverTime={SERVER_TIME_AT_RECEIPT} />,
      )
      expect(screen.getByText('observed 1m ago')).toBeInTheDocument()
    })
  })

  describe('disconnected as-of qualifier', () => {
    it('shows no qualifier while connected (the default)', () => {
      const { container } = render(
        <EvidenceValue evidence={evidence({ state: 'current' })} serverTime={SERVER_TIME} connected />,
      )
      expect(container.querySelector('.evidence__as-of')).toBeNull()
    })

    it('shows an explicit as-of-last-contact qualifier while disconnected, without changing the state badge', () => {
      render(
        <EvidenceValue
          evidence={evidence({ state: 'current' })}
          serverTime={SERVER_TIME}
          connected={false}
        />,
      )
      // The badge is still the coordinator's own verdict -- never
      // recomputed to "stale" or any client-invented state.
      expect(screen.getByText('current')).toBeInTheDocument()
      const qualifier = screen.getByRole('note')
      expect(qualifier.textContent).toMatch(/as of last contact/i)
      expect(qualifier.className).toContain('evidence__as-of')
    })
  })

  describe('unit rendering', () => {
    it('appends the unit to the formatted value when one is present', () => {
      render(<EvidenceValue evidence={evidence({ value: 42, unit: 'ms' })} serverTime={SERVER_TIME} />)
      expect(screen.getByText('42 ms')).toBeInTheDocument()
    })

    it('renders the bare value with no trailing unit text when unit is null', () => {
      render(<EvidenceValue evidence={evidence({ value: 42, unit: null })} serverTime={SERVER_TIME} />)
      expect(screen.getByText('42')).toBeInTheDocument()
      expect(screen.queryByText(/42 /)).not.toBeInTheDocument()
    })
  })
})
