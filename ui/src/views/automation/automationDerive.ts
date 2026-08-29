import type { ActionBinding, ActionIntegration, SafetyClass } from '../../app/types'

/**
 * The binding sweep (`GET /actions/bindings`) needs no credential and runs
 * continuously, so a macro's readiness can be answered from its steps'
 * bindings WITHOUT ever firing the macro (UI-DESIGN-GUIDE.md section 7's
 * "derive, don't ask" rule, design decision 8). `broken` always outranks
 * `unknown`: a macro that would actually fail is a stronger fact than one
 * this coordinator merely could not check yet, and `unknown` must never
 * read as a soft `ok` (the four absences, section 4).
 */
export type MacroReadiness =
  | { kind: 'ok'; okCount: number; total: number }
  | { kind: 'broken'; stepId: string; stepIndex: number; reason: string }
  | { kind: 'unknown'; stepId: string; stepIndex: number; reason: string | null }
  | { kind: 'unchecked' }

export interface ReadinessStep {
  id: string
  action: string
}

export function deriveMacroReadiness(steps: ReadinessStep[], bindings: ReadonlyMap<string, ActionBinding>): MacroReadiness {
  let anyEvidence = false
  let okCount = 0
  for (const [i, step] of steps.entries()) {
    const binding = bindings.get(step.action)
    if (binding === undefined) continue
    anyEvidence = true
    if (binding.state === 'broken') {
      return { kind: 'broken', stepId: step.id, stepIndex: i, reason: binding.reason }
    }
    if (binding.state === 'ok') okCount++
  }
  for (const [i, step] of steps.entries()) {
    const binding = bindings.get(step.action)
    if (binding?.state === 'unknown') {
      return { kind: 'unknown', stepId: step.id, stepIndex: i, reason: binding.reason }
    }
  }
  if (!anyEvidence) return { kind: 'unchecked' }
  return { kind: 'ok', okCount, total: steps.length }
}

/** The macro header/step-repeated label — "4 of 4 bindings ok" / "Step 2 broken". */
export function describeMacroReadiness(readiness: MacroReadiness): { label: string; tone: 'good' | 'warn' | 'bad' | 'unknown' } {
  switch (readiness.kind) {
    case 'ok':
      return { label: `${readiness.okCount} of ${readiness.total} bindings ok`, tone: 'good' }
    case 'broken':
      return { label: `Step ${readiness.stepIndex + 1} broken`, tone: 'bad' }
    case 'unknown':
      return { label: `Step ${readiness.stepIndex + 1} unknown`, tone: 'unknown' }
    case 'unchecked':
      return { label: 'No binding evidence yet', tone: 'unknown' }
  }
}

/**
 * A macro's consequence, rolled up from its steps' safety classes (design
 * decision 10): a macro containing a `stop` step states "running this ends
 * the current playlist" on its OWN header, so the operator learns what the
 * button does from what it is made of, never from its name alone.
 * `powerOff` outranks `stop` outranks `blackout`: each is a strictly larger
 * consequence than the last, and only the worst one needs stating.
 */
export function deriveMacroConsequence(safetyClasses: SafetyClass[]): string | null {
  if (safetyClasses.includes('powerOff')) return 'Running this powers off presentation hardware.'
  if (safetyClasses.includes('stop')) return 'Running this ends the current playlist.'
  if (safetyClasses.includes('blackout')) return 'Running this blacks out the current output.'
  return null
}

/**
 * `expect.kind: 'none'` on an mqtt action means it reports `unconfirmable`
 * on every run, forever, by design (design decision 9) — stated here at
 * AUTHORING time, not discovered later in run history. Only mqtt actions
 * carry an `expect` at all; every other integration is unconfirmable in
 * this sense never (fpp/resolume/audio outcomes come from a different
 * mechanism, not `expect.kind`).
 */
export function isUnconfirmableByDesign(integration: ActionIntegration, expectKind: string | undefined): boolean {
  return integration === 'mqtt' && (expectKind === 'none' || expectKind === undefined)
}

export interface ActionFacts {
  integration: ActionIntegration
  safetyClass: SafetyClass
  unconfirmableByDesign: boolean
}
