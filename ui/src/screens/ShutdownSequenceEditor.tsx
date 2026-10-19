import { Button, Choice, Field, Input, ReorderButtons, Select, type ActionOption } from '../kit'
import type { ConfigShutdownSequence, ConfigShutdownStep } from '../api'
import { sequenceIdFromLabel, withPowerRemoval, withRequireConfirmation } from './shutdownSequenceModel'

function moveItem<T>(items: readonly T[], index: number, by: number): T[] {
  const next = [...items]
  const [item] = next.splice(index, 1)
  next.splice(index + by, 0, item as T)
  return next
}

/** Edits a list of shutdown sequences: named, ordered steps that are each an action or a wait. */
export function ShutdownSequenceEditor({
  sequences,
  options,
  onChange,
  disabledReason,
  takenIds = [],
  savedIds = [],
}: {
  sequences: readonly ConfigShutdownSequence[]
  options: readonly ActionOption[]
  onChange: (next: ConfigShutdownSequence[]) => void
  disabledReason?: string | undefined
  takenIds?: readonly string[]
  /** Ids already saved keep their id when renamed, since nights may skip them by id. */
  savedIds?: readonly string[]
}) {
  const disabled = disabledReason !== undefined
  const setSeq = (index: number, patch: Partial<ConfigShutdownSequence>) => onChange(sequences.map((s, i) => (i === index ? { ...s, ...patch } : s)))
  const setStep = (si: number, ti: number, step: ConfigShutdownStep) => setSeq(si, { steps: (sequences[si]?.steps ?? []).map((s, i) => (i === ti ? step : s)) })
  const known = new Set(options.map((o) => o.id))

  const addSequence = () => {
    const taken = [...takenIds, ...sequences.map((s) => s.id)]
    onChange([...sequences, { id: sequenceIdFromLabel('New sequence', taken), label: 'New sequence', steps: [{ action: '' }] }])
  }

  return (
    <div className="sm-stack-4">
      {sequences.length === 0 && <p className="sm-small sm-faint">No shutdown sequences.</p>}
      {sequences.map((seq, si) => (
        <div key={si} className="sm-panel sm-stack-4">
          <div className="sm-inline-row sm-inline-row--end">
            <Field label="Sequence name" help={`Saved as ${seq.id}.`}>
              {(p) => <Input {...p} value={seq.label} disabled={disabled} onChange={(e) => {
                const label = e.target.value
                if (savedIds.includes(seq.id)) return setSeq(si, { label })
                const taken = [...takenIds, ...sequences.filter((_, i) => i !== si).map((s) => s.id)]
                setSeq(si, { label, id: sequenceIdFromLabel(label, taken) })
              }} />}
            </Field>
            <Button variant="quiet" disabled={disabled} title={disabledReason} onClick={() => onChange(sequences.filter((_, i) => i !== si))}>
              Remove sequence
            </Button>
          </div>
          {seq.steps.map((step, ti) => {
            const isWait = step.action === undefined
            return (
              <div key={ti} className="sm-action-list__row">
                <Field label={`Step ${ti + 1}`}>
                  {(p) => (
                    <Select {...p} value={isWait ? 'wait' : 'action'} disabled={disabled} onChange={(e) => setStep(si, ti, e.target.value === 'wait' ? { delayMs: 60000 } : { action: '' })}>
                      <option value="action">Run an action</option>
                      <option value="wait">Wait</option>
                    </Select>
                  )}
                </Field>
                {isWait ? (
                  <Field label="Seconds">
                    {(p) => (
                      <Input
                        {...p}
                        type="number"
                        min="1"
                        step="1"
                        value={step.delayMs === undefined ? '' : String(step.delayMs / 1000)}
                        disabled={disabled}
                        onChange={(e) => setStep(si, ti, { delayMs: Math.round(Number(e.target.value) * 1000) })}
                      />
                    )}
                  </Field>
                ) : (
                  <>
                    <Field label="Action">
                      {(p) => (
                        <Select {...p} value={step.action} disabled={disabled} onChange={(e) => setStep(si, ti, { ...step, action: e.target.value })}>
                          <option value="">Select an action…</option>
                          {step.action !== undefined && step.action !== '' && !known.has(step.action) && <option value={step.action}>{step.action} (not found)</option>}
                          {options.map((o) => (
                            <option key={o.id} value={o.id}>
                              {o.label} · {o.id}
                            </option>
                          ))}
                        </Select>
                      )}
                    </Field>
                    <Choice
                      type="checkbox"
                      label="Must confirm"
                      checked={step.requireConfirmation === true}
                      disabled={disabled}
                      onChange={(e) => setStep(si, ti, withRequireConfirmation(step, e.target.checked))}
                    />
                    <Choice
                      type="checkbox"
                      label="Removes power"
                      checked={step.powerDomain !== undefined}
                      disabled={disabled}
                      onChange={(e) => setStep(si, ti, withPowerRemoval(step, e.target.checked))}
                    />
                  </>
                )}
                <ReorderButtons
                  itemLabel={`step ${ti + 1} of ${seq.label}`}
                  onMoveUp={() => setSeq(si, { steps: moveItem(seq.steps, ti, -1) })}
                  onMoveDown={() => setSeq(si, { steps: moveItem(seq.steps, ti, 1) })}
                  onRemove={() => setSeq(si, { steps: seq.steps.filter((_, i) => i !== ti) })}
                  moveUpReason={disabledReason ?? (ti === 0 ? 'Already first.' : undefined)}
                  moveDownReason={disabledReason ?? (ti === seq.steps.length - 1 ? 'Already last.' : undefined)}
                  removeReason={disabledReason}
                />
              </div>
            )
          })}
          <div>
            <Button disabled={disabled} title={disabledReason} onClick={() => setSeq(si, { steps: [...seq.steps, { action: '' }] })}>
              Add step
            </Button>
          </div>
        </div>
      ))}
      <div>
        <Button disabled={disabled} title={disabledReason} onClick={addSequence}>
          Add sequence
        </Button>
      </div>
    </div>
  )
}
