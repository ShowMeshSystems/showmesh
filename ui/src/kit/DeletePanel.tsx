import { useState, type ReactNode } from 'react'
import { Button, ButtonRow } from './Button'
import { Field, Input } from './Field'
import { RuledStrip } from './StateBlocks'

type DeletePanelProps = {
  /** "Delete this show", "Delete this cue". */
  title: string
  /** What the operator must type before the button enables, e.g. the object's own label or id. */
  confirmValue: string
  /** "Delete show", "Delete cue" — the button's own label while idle. */
  actionLabel: string
  deleting: boolean
  /** The coordinator's own refusal text, rendered verbatim; never write a local explanation. */
  error: string | null
  allowed: boolean
  /** Why the control is disabled when `allowed` is false. */
  disallowedReason?: string | undefined
  onDelete: () => void
  /** Explanatory copy shown above the confirm field. */
  children?: ReactNode
}

/**
 * The type-to-confirm destructive panel every config-object delete uses
 * (D-019). One shared implementation so every kind asks the same way.
 */
export function DeletePanel({ title, confirmValue, actionLabel, deleting, error, allowed, disallowedReason, onDelete, children }: DeletePanelProps) {
  const [typed, setTyped] = useState('')
  const matches = typed === confirmValue

  return (
    <div className="sm-panel sm-stack-5">
      <h3 className="sm-subsection__title">{title}</h3>
      {children}
      <Field label={`Type ${confirmValue} to confirm`} help="Asks for the object's own label or id before it proceeds.">
        {(p) => <Input {...p} value={typed} onChange={(e) => setTyped(e.target.value)} />}
      </Field>
      {error !== null && <RuledStrip absence="failed" label="Delete failed" fact={error} />}
      <ButtonRow>
        <Button
          variant="danger"
          onClick={onDelete}
          disabled={!matches || deleting || !allowed}
          title={!allowed ? disallowedReason : !matches ? 'Type it exactly to enable this.' : undefined}
        >
          {deleting ? 'Deleting…' : actionLabel}
        </Button>
      </ButtonRow>
    </div>
  )
}
