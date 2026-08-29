import { formatAbsolute } from '../../app/time'
import { NightLifecycleBadge } from '../../components/DomainBadges'
import { LoadingBlock, UnavailableBlock } from '../../components/SharedLayouts'
import type { NightLifecycleState, NightSessionState } from '../../app/types'
import type { NightSessionLoadState } from './useNightSessionState'

// The lifecycle double-timeline (Dashboard and Show Night both carry it —
// UI-DESIGN-GUIDE.md section 8, DESIGN-DECISIONS-AND-API-FACTS.md
// "Two timelines on Show Night"): the night (a sequence of cycles) and the
// cycle (phases within it) are different clocks, and one flattened row
// made a repeating loop look one-way. Kept as two rows, always.
//
// The mock's row 1 shows a per-cycle start time for every prior cycle
// tonight (16:30, 19:52, 20:27…). GET /night/session (NightSessionState)
// carries only the CURRENT cycle number and when the CURRENT state was
// entered — no history of when earlier cycles began. Row 1 here renders
// what that response actually supports: how many cycles have run tonight
// and which one is current, without inventing a per-cycle timestamp the
// API does not return.
const PHASES: readonly NightLifecycleState[] = [
  'inactive',
  'preparing',
  'preshow',
  'transition-to-show',
  'live',
  'transition-to-resting',
  'resting-intershow',
  'end-of-night-resting',
  'fading-out',
  'stopped',
]

function CyclesRow({ session }: { session: NightSessionState }) {
  const cycles = session.cycle > 0 ? Array.from({ length: session.cycle }, (_, i) => i + 1) : []
  return (
    <div className="lifecycle-timeline__row">
      <span className="lifecycle-timeline__row-label t-meta">Tonight</span>
      {cycles.length === 0 ? (
        <p className="lifecycle-timeline__empty t-small">No cycle has started this session.</p>
      ) : (
        <ol className="lifecycle-timeline__cycles" aria-label="Cycles run tonight">
          {cycles.map((n) => {
            const isCurrent = n === session.cycle
            return (
              <li
                key={n}
                className={`lifecycle-timeline__cycle${isCurrent ? ' lifecycle-timeline__cycle--current' : ''}`}
                aria-current={isCurrent ? 'step' : undefined}
              >
                <span className="t-data">Cycle {n}</span>
                {isCurrent && <span className="lifecycle-timeline__now t-small">now</span>}
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

// The current phase gets exactly one labelled-pair badge (NightLifecycleBadge,
// via DomainBadges), so its state word is not ambiguous with the other nine
// phases the strip below also shows. The strip itself carries no duplicate
// text label per phase (only an aria-label, for assistive tech) — repeating
// all ten phases' full status words as visible text on every render would
// make the CURRENT phase's own word impossible to find uniquely on screen.
function PhasesRow({ session }: { session: NightSessionState }) {
  const currentIndex = PHASES.indexOf(session.state)
  return (
    <div className="lifecycle-timeline__row">
      <span className="lifecycle-timeline__row-label t-meta">{session.cycle > 0 ? `Cycle ${session.cycle}` : 'Phase'}</span>
      <div className="lifecycle-timeline__phase-detail">
        <NightLifecycleBadge state={session.state} />
        <ol className="lifecycle-timeline__phases" aria-label="Cycle phase progression">
          {PHASES.map((phase, i) => (
            <li
              key={phase}
              className={`lifecycle-timeline__phase${i === currentIndex ? ' lifecycle-timeline__phase--current' : i < currentIndex ? ' lifecycle-timeline__phase--past' : ''}`}
              aria-current={i === currentIndex ? 'step' : undefined}
              aria-label={phase}
              title={phase}
            />
          ))}
        </ol>
      </div>
      <p className="lifecycle-timeline__state-entered t-small text-muted">
        State entered {formatAbsolute(session.stateEnteredAt)}.
      </p>
    </div>
  )
}

// Always rendered inside a section that already carries its own h2
// ("Tonight's lifecycle" on Dashboard, "Lifecycle" on Show Night), so
// this component's own absence blocks default to h3 to avoid a nested
// duplicate h2.
export function LifecycleTimeline({ loadState, headingLevel = 3 }: { loadState: NightSessionLoadState; headingLevel?: 2 | 3 }) {
  if (loadState.kind === 'loading') {
    return <LoadingBlock title="Lifecycle" reason="Waiting for the night session's lifecycle state." headingLevel={headingLevel} />
  }
  if (loadState.kind === 'error') {
    return <UnavailableBlock title="Lifecycle" reason={loadState.message} headingLevel={headingLevel} />
  }
  const { session } = loadState
  return (
    <div className="lifecycle-timeline">
      {loadState.stale && loadState.staleError !== null && (
        <p className="lifecycle-timeline__stale panel panel--error" role="alert">
          Showing the last confirmed lifecycle state; the most recent refresh failed: {loadState.staleError}
        </p>
      )}
      <CyclesRow session={session} />
      <PhasesRow session={session} />
    </div>
  )
}
