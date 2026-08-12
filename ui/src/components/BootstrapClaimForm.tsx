import { useId, useState, type FormEvent } from 'react'
import { describeApiError } from '../app/session'

// `POST /api/v1/bootstrap` (ADR-024 decision 9): claims the one-time
// bootstrap code — readable only from a file in the coordinator's data
// volume, never logged, never network-discoverable — and creates the
// first administrator. Deliberately a separate form from SignInForm even
// though three of its four fields are identical: this is a one-time,
// filesystem-gated action with a different failure vocabulary ("invalid,
// already claimed, or expired code," never "wrong password" for an
// account that doesn't exist yet), and BootstrapBanner only ever renders
// one of the two at a time, never both. `onSubmit` is injected for the
// same reason as SignInForm's — see that component's comment.
export interface BootstrapClaimFormProps {
  onSubmit: (code: string, name: string, password: string, deviceLabel: string) => Promise<void>
  onSuccess?: () => void
}

export function BootstrapClaimForm({ onSubmit, onSuccess }: BootstrapClaimFormProps) {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [deviceLabel, setDeviceLabel] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const formId = useId()

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (submitting) return
    setSubmitting(true)
    setError(null)
    try {
      await onSubmit(code.trim(), name.trim(), password, deviceLabel.trim())
      onSuccess?.()
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="session-form" onSubmit={(e) => void handleSubmit(e)} aria-label="Claim the bootstrap code">
      {error !== null && (
        <span role="alert" className="session-form__error">
          {error}
        </span>
      )}
      <label htmlFor={`${formId}-code`}>Bootstrap code</label>
      <input
        id={`${formId}-code`}
        type="text"
        placeholder="from the coordinator's data volume"
        autoComplete="off"
        required
        value={code}
        onChange={(event) => setCode(event.target.value)}
      />
      <label htmlFor={`${formId}-name`}>Administrator name</label>
      <input
        id={`${formId}-name`}
        type="text"
        autoComplete="username"
        required
        value={name}
        onChange={(event) => setName(event.target.value)}
      />
      <label htmlFor={`${formId}-password`}>Password</label>
      <input
        id={`${formId}-password`}
        type="password"
        autoComplete="new-password"
        required
        value={password}
        onChange={(event) => setPassword(event.target.value)}
      />
      <label htmlFor={`${formId}-device-label`}>This device’s name</label>
      <input
        id={`${formId}-device-label`}
        type="text"
        placeholder="e.g. porch tablet"
        autoComplete="off"
        required
        value={deviceLabel}
        onChange={(event) => setDeviceLabel(event.target.value)}
      />
      <button type="submit" disabled={submitting}>
        {submitting ? 'Claiming…' : 'Create administrator'}
      </button>
    </form>
  )
}
