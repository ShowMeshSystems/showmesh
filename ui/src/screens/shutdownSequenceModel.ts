import type { ConfigShutdownSequence, ConfigShutdownStep } from '../api'

/** Turns a sequence name into the id the coordinator accepts. */
export function sequenceIdFromLabel(label: string, taken: readonly string[]): string {
  const base = label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 56) || 'sequence'
  let id = base
  for (let n = 2; taken.includes(id); n++) id = `${base}-${n}`
  return id
}


/** Returns the first problem that would make the coordinator refuse seqs, or null. */
export function sequencesProblem(seqs: readonly ConfigShutdownSequence[]): string | null {
  for (const seq of seqs) {
    const name = seq.label.trim() === '' ? 'A sequence' : seq.label
    if (seq.label.trim() === '') return 'A sequence has no name. Add one.'
    if (seq.steps.length === 0) return `${name} has no steps. Add one or remove the sequence.`
    for (const [i, step] of seq.steps.entries()) {
      const isWait = step.action === undefined
      if (!isWait && step.action === '') return `Step ${i + 1} of ${name} has no action. Pick one.`
      if (isWait && (step.delayMs === undefined || step.delayMs <= 0)) return `Step ${i + 1} of ${name} needs a wait longer than zero.`
    }
  }
  return null
}

/** Returns step with requireConfirmation set, or with the field removed. */
export function withRequireConfirmation(step: ConfigShutdownStep, on: boolean): ConfigShutdownStep {
  const next: ConfigShutdownStep = { ...step }
  if (on) next.requireConfirmation = true
  else delete next.requireConfirmation
  return next
}

/** Returns step marked as removing show equipment power, or with that mark removed. */
export function withPowerRemoval(step: ConfigShutdownStep, on: boolean): ConfigShutdownStep {
  const next: ConfigShutdownStep = { ...step }
  if (on) {
    next.powerDomain = 'presentation'
    next.domainProvenance = 'operator-declared'
  } else {
    delete next.powerDomain
    delete next.domainProvenance
  }
  return next
}
