import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react'
import { useId } from 'react'

type FieldProps = {
  /** Label the outcome of the choice, not the field. */
  label: string
  /** Helper text earns its line or it goes. If it repeats the label, drop it. */
  help?: ReactNode
  /** State the reason literally, then the action. */
  error?: ReactNode
  children: (props: { id: string; 'aria-describedby': string | undefined; 'aria-invalid': boolean | undefined }) => ReactNode
}

export function Field({ label, help, error, children }: FieldProps) {
  const id = useId()
  const helpId = help === undefined ? undefined : `${id}-help`
  const errorId = error === undefined ? undefined : `${id}-error`
  const describedBy = [helpId, errorId].filter(Boolean).join(' ') || undefined
  return (
    <div className="sm-field">
      <label className="sm-field__label" htmlFor={id}>{label}</label>
      {children({ id, 'aria-describedby': describedBy, 'aria-invalid': error === undefined ? undefined : true })}
      {help !== undefined && <span className="sm-field__help" id={helpId}>{help}</span>}
      {error !== undefined && (
        <span className="sm-field__error" id={errorId}>
          <span aria-hidden="true">✕</span>
          {error}
        </span>
      )}
    </div>
  )
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={['sm-input', className].filter(Boolean).join(' ')} {...props} />
}

/** Never render an empty select, or one fed by a field the API does not return. */
export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={['sm-select', className].filter(Boolean).join(' ')} {...props} />
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className={['sm-textarea', className].filter(Boolean).join(' ')} {...props} />
}

export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className="sm-field-grid">{children}</div>
}

type ChoiceProps = InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }

export function Choice({ label, ...props }: ChoiceProps) {
  return (
    <label className="sm-choice">
      <input {...props} />
      <span>{label}</span>
    </label>
  )
}

export function ChoiceRow({ children }: { children: ReactNode }) {
  return <div className="sm-choice-row">{children}</div>
}

export type ChoiceGroupOption = { value: string; label: ReactNode; /** Muted context under the label, e.g. a route or a role: never the only way to tell two options apart. */ secondary?: ReactNode }

type ChoiceGroupProps = {
  /** Label the outcome of the choice, not the field. */
  label: string
  help?: ReactNode
  error?: ReactNode
  options: readonly ChoiceGroupOption[]
  value: readonly string[]
  onChange: (value: string[]) => void
}

/** A checkbox group for picking any number of a known option set. */
export function ChoiceGroup({ label, help, error, options, value, onChange }: ChoiceGroupProps) {
  const id = useId()
  const helpId = help === undefined ? undefined : `${id}-help`
  const errorId = error === undefined ? undefined : `${id}-error`
  const describedBy = [helpId, errorId].filter(Boolean).join(' ') || undefined
  const invalid = error === undefined ? undefined : true
  const known = new Set(options.map((option) => option.value))
  const unknown = value.filter((v) => !known.has(v))
  const toggle = (v: string) => onChange(value.includes(v) ? value.filter((x) => x !== v) : [...value, v])
  return (
    <div className="sm-field">
      <fieldset className="sm-choice-group" aria-describedby={describedBy} aria-invalid={invalid}>
        <legend className="sm-field__label">{label}</legend>
        {options.map((option) => {
          const hasSecondary = option.secondary !== undefined && option.secondary !== ''
          return (
            <label key={option.value} className={hasSecondary ? 'sm-choice sm-choice--stacked' : 'sm-choice'}>
              <input
                type="checkbox"
                checked={value.includes(option.value)}
                onChange={() => toggle(option.value)}
                aria-describedby={describedBy}
                aria-invalid={invalid}
              />
              {hasSecondary ? (
                <span className="sm-choice__text">
                  <span>{option.label}</span>
                  <span className="sm-choice__secondary">{option.secondary}</span>
                </span>
              ) : (
                <span>{option.label}</span>
              )}
            </label>
          )
        })}
        {unknown.map((v) => (
          <label key={v} className="sm-choice">
            <input type="checkbox" checked onChange={() => toggle(v)} aria-describedby={describedBy} aria-invalid={invalid} />
            <span>{`${v} (not declared)`}</span>
          </label>
        ))}
      </fieldset>
      {help !== undefined && <span className="sm-field__help" id={helpId}>{help}</span>}
      {error !== undefined && (
        <span className="sm-field__error" id={errorId}>
          <span aria-hidden="true">✕</span>
          {error}
        </span>
      )}
    </div>
  )
}
