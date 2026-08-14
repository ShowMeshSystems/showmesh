import { FPPCommandOutcome } from './FPPCommandOutcome'
import type { MacroRunStep, MacroRunStepCommand } from '../app/types'

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
  /**
   * Corrected 2026-08-14 (this task's finding 2): "none" used to render a
   * single hardcoded past-tense sentence — "This step did not dispatch an
   * FPP command" — for every cause of "none", including a step that has
   * not run YET. Watching a live run, that reads as a settled negative
   * about a step that is about to dispatch. `integration` and `state`
   * (both already in hand on [MacroRunStep], previously unused here) are
   * what let this component tell "never will have one" (an mqtt step; a
   * step the run skipped before it ever dispatched) apart from "does not
   * have one yet" (a pending fpp step) — mapMacroRunStepCommand
   * (internal/coordinator/api/macroruns.go) folds both into the SAME
   * command.state ("none") and the SAME generic server reason, so this
   * distinction can only be made client-side, from the step itself.
   */
  integration: MacroRunStep['integration']
  state: MacroRunStep['state']
}

export function MacroRunCommandDetail({ command, integration, state }: MacroRunCommandDetailProps) {
  if (command.state === 'none') {
    // The server's own reason (STEP-9-SPEC.md section 6.1's "absent
    // evidence is stated, never omitted") is rendered in every branch
    // below, never thrown away — matching the "not_retained" branch's own
    // convention a few lines down. The LEAD sentence is chosen from
    // integration/state so it is never a past-tense claim about a step
    // that simply has not run yet.
    if (integration === 'mqtt') {
      // Structural, permanent: an mqtt step has no commands row by
      // construction (mapMacroRunStepCommand's own doc comment) and never
      // will, on any run of this macro.
      return (
        <p className="text-muted">
          This is an external MQTT step; it never dispatches an FPP command.
          {command.reason !== undefined && command.reason !== '' ? ` ${command.reason}` : ''}
        </p>
      )
    }
    if (state === 'skipped') {
      // An abort left this fpp step never attempted (STEP-9-SPEC.md
      // section 6.4) — also permanent for this run, but for a different
      // reason than "mqtt": it WOULD have dispatched one had the run
      // continued.
      return (
        <p className="text-muted">
          This step was skipped before it dispatched; it will not dispatch a command now.
          {command.reason !== undefined && command.reason !== '' ? ` ${command.reason}` : ''}
        </p>
      )
    }
    // fpp, not skipped: a pending (or, transiently, an in-flight)
    // step that has not dispatched YET — the one case this must not read
    // as a settled negative. role="status" rather than a bare <p>: this
    // is live, changing information for a run the operator may be
    // watching right now.
    return (
      <p className="text-muted" role="status">
        This step has not dispatched a command yet.
        {command.reason !== undefined && command.reason !== '' ? ` ${command.reason}` : ''}
      </p>
    )
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
