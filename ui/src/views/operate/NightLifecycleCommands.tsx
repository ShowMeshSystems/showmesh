import { NightCommandButton } from '../../components/NightCommandButton'
import type { NightSessionState } from '../../app/types'

// The eight night commands, exactly, at gloved size (44-48px touch
// target) — CSS in operate.css sizes `.night-command-rack` buttons, since
// NightCommandButton itself takes no size prop. No confirm dialog: the
// gloved target and the stated consequence (each command's own copy,
// below) are the safety, not a modal (UI-DESIGN-GUIDE.md section 1 / 5).
// Night commands answer 202, never 200 — accepted or an idempotent
// duplicate, with no downstream confirmation loop. NightCommandButton
// itself never claims success beyond that.
export function NightLifecycleCommands({ onApplied = () => undefined }: { onApplied?: (session: NightSessionState) => void }) {
  return (
    <div className="night-command-rack">
      <section className="night-command-rack__group" aria-label="Prepare">
        <h3 className="t-meta night-command-rack__group-label">Prepare</h3>
        <div className="night-command-rack__buttons">
          <div>
            <NightCommandButton command="prepare-site" label="Prepare site" onApplied={onApplied} />
            <p className="t-small text-muted">Creates tonight&rsquo;s session and readies the presentation path.</p>
          </div>
          <div>
            <NightCommandButton command="run-readiness" label="Run readiness" onApplied={onApplied} />
            <p className="t-small text-muted">Checks whether the site is ready to start, without changing anything.</p>
          </div>
        </div>
      </section>

      <section className="night-command-rack__group" aria-label="Start">
        <h3 className="t-meta night-command-rack__group-label">Start</h3>
        <div className="night-command-rack__buttons">
          <div>
            <NightCommandButton command="start-preshow" label="Start preshow" onApplied={onApplied} />
            <p className="t-small text-muted">Begins the preshow period before the first show of the night.</p>
          </div>
          <div>
            <NightCommandButton command="start-night" label="Start night" onApplied={onApplied} />
            <p className="t-small text-muted">Starts the first cycle of the night, moving from resting into show.</p>
          </div>
        </div>
      </section>

      <section className="night-command-rack__group" aria-label="End the night">
        <h3 className="t-meta night-command-rack__group-label">End the night</h3>
        <div className="night-command-rack__buttons">
          <div>
            <NightCommandButton command="request-final-show" label="Request final show" onApplied={onApplied} />
            <p className="t-small text-muted">Closes admission. The next normally timed show becomes the last.</p>
          </div>
          <div>
            <NightCommandButton command="fade-out-night" label="Fade out night" onApplied={onApplied} />
            <p className="t-small text-muted">Arrives mid-show, so this show becomes final and the fade waits for it to finish.</p>
          </div>
          <div>
            <NightCommandButton command="power-down-presentation" label="Power down presentation" onApplied={onApplied} />
            <p className="t-small text-muted">Powers down presentation infrastructure once the fade has completed.</p>
          </div>
        </div>
      </section>

      <section className="night-command-rack__group" aria-label="Recovery">
        <h3 className="t-meta night-command-rack__group-label">Recovery (provisional)</h3>
        <div className="night-command-rack__buttons">
          <div>
            <NightCommandButton command="end-session" label="End session" onApplied={onApplied} />
            <p className="t-small text-muted">
              Recovery from a degraded or ambiguous session. Never obtains an outcome the lifecycle commands above
              would have refused (ADR-041).
            </p>
          </div>
        </div>
      </section>
    </div>
  )
}
