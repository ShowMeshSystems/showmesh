import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ResolumeActionOutcome } from './ResolumeActionOutcome'
import { makeResolumeActionResult } from '../app/test-support/fixtures'

afterEach(() => cleanup())

// Build contract §2.3/§3/acceptance criterion 6: all five outcome states
// (plus the accepted empty-replay-race "pending") render distinctly, and
// criterion 9: selectedDeckChanged: null renders "not known", never "no".
describe('ResolumeActionOutcome', () => {
  it('renders "confirmed" with the server-provided reason', () => {
    render(
      <ResolumeActionOutcome
        composition={null}
        result={makeResolumeActionResult({ outcome: 'confirmed', outcomeReason: 'clip connected' })}
      />,
    )
    expect(screen.getByText(/Confirmed: clip connected/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('renders "unconfirmed" as an alert, distinct from confirmed', () => {
    render(
      <ResolumeActionOutcome
        composition={null}
        result={makeResolumeActionResult({ outcome: 'unconfirmed', outcomeReason: 'deadline expired before evidence arrived' })}
      />,
    )
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('Unconfirmed')
    expect(screen.queryByText(/^Confirmed/)).toBeNull()
  })

  // ADR-029: "an action whose effect cannot be observed reports as
  // unconfirmable ... never as success." This is the state most likely to
  // regress into looking like success, so it gets its own explicit check
  // that the word "success" and "Confirmed:" never appear alongside it.
  it('renders "unconfirmable" as neither success nor failure', () => {
    render(
      <ResolumeActionOutcome
        composition={null}
        result={makeResolumeActionResult({
          outcome: 'unconfirmable',
          outcomeReason: 'launching an already-playing clip has no observable effect',
        })}
      />,
    )
    const text = screen.getByText(/Unconfirmable/)
    expect(text.textContent).toContain('neither success nor failure')
    expect(screen.queryByText(/^Confirmed/)).toBeNull()
    expect(screen.queryByText(/^Failed/)).toBeNull()
  })

  it('renders "refused" as an alert stating nothing was dispatched', () => {
    render(
      <ResolumeActionOutcome
        composition={null}
        result={makeResolumeActionResult({ outcome: 'refused', outcomeReason: "clip's own deck is not currently selected" })}
      />,
    )
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('Refused')
    expect(alert.textContent).toContain('nothing was dispatched')
  })

  // Review finding 3: this string is NOT a paraphrase — it is exactly
  // deckSelectionRefusal's own fmt.Sprintf template
  // (internal/coordinator/collector/resolume/action.go), built through
  // formatRef twice, reproduced verbatim from a real dispatch against the
  // operator's own composition. The prior version of this test used a
  // hand-written paraphrase that happened to contain no "(id ...)" at
  // all, so it could not have caught this leak — the whole reason this
  // finding exists.
  const REAL_DECK_SELECTION_REFUSAL_REASON =
    'this clip belongs to Deck 2 (id 2000000000002), and that deck is not selected ' +
    '(read at 2026-08-16T10:00:00Z); the most recently observed selected deck is ' +
    'Deck 1 (id 2000000000001) (as of 2026-08-16T09:59:00Z)'

  it('never renders a raw Arena object id inside a refusal reason built by the server’s own formatRef', () => {
    render(
      <ResolumeActionOutcome
        composition={null}
        result={makeResolumeActionResult({
          outcome: 'refused',
          outcomeReason: REAL_DECK_SELECTION_REFUSAL_REASON,
        })}
      />,
    )
    const alert = screen.getByRole('alert')
    expect(alert.textContent).not.toMatch(/\bid 2000000000002\b/)
    expect(alert.textContent).not.toMatch(/\bid 2000000000001\b/)
    expect(alert.textContent).not.toMatch(/2000000000\d{3}/)
    // The names survive — sanitizing removes the id, never the name.
    expect(alert.textContent).toContain('Deck 2')
    expect(alert.textContent).toContain('Deck 1')
  })

  it('renders "failed" as an alert, distinct from refused', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ outcome: 'failed', outcomeReason: 'the dispatch attempt itself failed' })} />)
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('Failed')
    expect(alert.textContent).not.toContain('Refused')
  })

  it('renders a pending state for the accepted empty-outcome replay race, never as any of the five words', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ outcome: '' })} />)
    expect(screen.getByText(/Pending/)).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('renders selectedDeckChanged: null as "not known", never as "no"', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ selectedDeckChanged: null })} />)
    expect(screen.getByText('not known')).toBeVisible()
    expect(screen.queryByText(/^no$/)).toBeNull()
  })

  it('renders selectedDeckChanged: false as "no", distinctly from null', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ selectedDeckChanged: false })} />)
    expect(screen.getByText('no')).toBeVisible()
    expect(screen.queryByText('not known')).toBeNull()
  })

  it('renders selectedDeckChanged: true as "yes"', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ selectedDeckChanged: true })} />)
    expect(screen.getByText('yes')).toBeVisible()
  })

  it('flags a replayed result distinctly', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ replay: true })} />)
    expect(screen.getByText(/already used/)).toBeVisible()
  })

  it('surfaces attributionDegraded as its own note', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ attributionDegraded: true })} />)
    expect(screen.getByText(/could not record this action in its audit log/)).toBeVisible()
  })

  it('shows resolvedId when present, for debugging, without ever appearing in what the operator types', () => {
    render(<ResolumeActionOutcome composition={null} result={makeResolumeActionResult({ resolvedId: 'obj-99' })} />)
    expect(screen.getByText('obj-99')).toBeVisible()
  })

  it('omits the resolved-to line for blackout, which addresses nothing', () => {
    // resolvedId is genuinely ABSENT for blackout (never present-as-
    // undefined) — built by destructuring it away from the fixture's own
    // default, which `exactOptionalPropertyTypes` forbids overriding with
    // a literal `undefined`.
    const fullResult = makeResolumeActionResult({ action: 'blackout' })
    const withoutResolvedId = { ...fullResult }
    delete withoutResolvedId.resolvedId
    render(<ResolumeActionOutcome composition={null} result={withoutResolvedId} />)
    expect(screen.queryByText('Resolved to')).toBeNull()
  })
})
