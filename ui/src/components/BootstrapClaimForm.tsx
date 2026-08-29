import { useId, useState, type FormEvent } from 'react'
import { describeApiError } from '../app/session'
import '../styles/session.css'

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
//
// Every field is gloved (44px), the submit is 48px — this runs on install
// day, sometimes from a phone standing next to the coordinator's box.
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
    <form className="session-band__form" onSubmit={(e) => void handleSubmit(e)} aria-label="Claim the bootstrap code">
      {error !== null && (
        <div role="alert" className="session-form__alert">
          <p className="t-small" style={{ margin: 0 }}>
            {error}
          </p>
        </div>
      )}
      <div className="field field--gloved">
        <label className="field__label" htmlFor={`${formId}-code`}>
          Bootstrap code
        </label>
        <input
          id={`${formId}-code`}
          className="field__input field__input--data"
          type="text"
          placeholder="from the coordinator's data volume"
          autoComplete="off"
          required
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
        <span className="field__help">
          Readable only from a file in the coordinator’s data volume. Having it proves filesystem
          access, which is what stops this being a way to become administrator over the network.
        </span>
      </div>
      <div className="field field--gloved">
        <label className="field__label" htmlFor={`${formId}-name`}>
          Administrator name
        </label>
        <input
          id={`${formId}-name`}
          className="field__input"
          type="text"
          autoComplete="username"
          required
          value={name}
          onChange={(event) => setName(event.target.value)}
        />
      </div>
      <div className="field field--gloved">
        <label className="field__label" htmlFor={`${formId}-password`}>
          Password
        </label>
        <input
          id={`${formId}-password`}
          className="field__input"
          type="password"
          autoComplete="new-password"
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </div>
      <div className="field field--gloved">
        <label className="field__label" htmlFor={`${formId}-device-label`}>
          This device’s name
        </label>
        <input
          id={`${formId}-device-label`}
          className="field__input"
          type="text"
          placeholder="e.g. install laptop"
          autoComplete="off"
          required
          value={deviceLabel}
          onChange={(event) => setDeviceLabel(event.target.value)}
        />
      </div>
      <button type="submit" className="btn btn--primary btn--gloved-lg" disabled={submitting}>
        {submitting ? 'Claiming…' : 'Claim and sign in'}
      </button>
      <div style={{ display: 'grid', gap: 6 }}>
        <p className="t-small" style={{ margin: 0, color: 'var(--text-muted)' }}>
          This creates one administrator, deletes the code, and signs this device in. There is no
          second code: a lost password after this needs filesystem access again.
        </p>
        <p className="t-small" style={{ margin: 0, color: 'var(--text-faint)' }}>
          A wrong code is rate-limited per network, shared with ordinary sign-in, and is never a
          lockout.
        </p>
      </div>
    </form>
  )
}
