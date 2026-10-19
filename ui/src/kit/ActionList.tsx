import { Button, ReorderButtons } from './Button'
import { Field, Select } from './Field'

export type ActionOption = { id: string; label: string }

/** An ordered list of show actions, each picked from a dropdown. */
export function ActionList({
  label,
  help,
  value,
  options,
  onChange,
  disabledReason,
  addLabel = 'Add action',
}: {
  label: string
  help?: string
  value: readonly string[]
  options: readonly ActionOption[]
  onChange: (next: string[]) => void
  disabledReason?: string | undefined
  addLabel?: string
}) {
  const disabled = disabledReason !== undefined
  const set = (index: number, id: string) => onChange(value.map((v, i) => (i === index ? id : v)))
  const move = (index: number, by: number) => {
    const next = [...value]
    const [item] = next.splice(index, 1)
    next.splice(index + by, 0, item as string)
    onChange(next)
  }
  const known = new Set(options.map((o) => o.id))
  return (
    <div className="sm-action-list">
      <p className="sm-action-list__label">{label}</p>
      {help !== undefined && <p className="sm-small sm-muted">{help}</p>}
      {value.length === 0 && <p className="sm-small sm-faint">No actions.</p>}
      {value.map((id, index) => (
        <div className="sm-action-list__row" key={index}>
          <Field label={`Action ${index + 1}`}>
            {(p) => (
              <Select {...p} value={id} disabled={disabled} title={disabledReason} onChange={(e) => set(index, e.target.value)}>
                <option value="">Select an action…</option>
                {!known.has(id) && id !== '' && <option value={id}>{id} (not found)</option>}
                {options.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.label} · {o.id}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <ReorderButtons
            itemLabel={`action ${index + 1}`}
            onMoveUp={() => move(index, -1)}
            onMoveDown={() => move(index, 1)}
            onRemove={() => onChange(value.filter((_, i) => i !== index))}
            moveUpReason={disabledReason ?? (index === 0 ? 'Already first.' : undefined)}
            moveDownReason={disabledReason ?? (index === value.length - 1 ? 'Already last.' : undefined)}
            removeReason={disabledReason}
          />
        </div>
      ))}
      <div>
        <Button disabled={disabled} title={disabledReason} onClick={() => onChange([...value, ''])}>
          {addLabel}
        </Button>
      </div>
    </div>
  )
}
