import { FPPCommandOutcome } from './FPPCommandOutcome'
import type { MacroRunStepCommand } from '../app/types'

// STEP-9-SPEC.md section 6.1: "commands is pruned by retention while
// config_revisions and node_declarations are not, so a run older than
// the command retention window points at a row that no longer exists.
// ... the run view renders the step's own recorded outcome with the
// command detail marked not retained, with a reason. It never renders
// blank and it never renders as though the step had no command. Absent
// evidence is stated, never omitted." This component is that rendering,
// and its three branches are exhaustive over MacroRunStepCommand.state
// ("none" | "retained" | "not_retained") — an unrecognized future value
// falls through to a fourth, generic branch rather than rendering
// nothing, matching OPERATOR-UI section 9's "an unrecognized value must
// degrade to a generic panel... never blank the view."
export interface MacroRunCommandDetailProps {
  command: MacroRunStepCommand
}

export function MacroRunCommandDetail({ command }: MacroRunCommandDetailProps) {
  if (command.state === 'none') {
    return <p className="text-muted">This step did not dispatch an FPP command.</p>
  }
  if (command.state === 'retained') {
    if (command.detail === undefined) {
      // Schema-invalid response: "retained" promises `detail`. Render
      // this honestly rather than silently treating it as "none" — a
      // command that clearly WAS dispatched (state says so) must never
      // read as though it never happened.
      return (
        <p role="alert" className="fpp-command-control__error">
          This step dispatched a command, but the coordinator&rsquo;s response did not include
          its detail.
        </p>
      )
    }
    return <FPPCommandOutcome result={command.detail} confirmedSummary="this step's effect was confirmed" />
  }
  if (command.state === 'not_retained') {
    return (
      <p className="text-muted" role="status">
        This step dispatched command {command.id ?? '(id unknown)'}, but its detail is no
        longer retained.
        {command.reason !== undefined && command.reason !== '' ? ` ${command.reason}` : ''}
      </p>
    )
  }
  // Unrecognized state: never blank (OPERATOR-UI section 9).
  return <p className="text-muted">Command state: {command.state}.</p>
}
