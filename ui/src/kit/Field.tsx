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
