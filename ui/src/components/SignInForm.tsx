import { useId, useState, type FormEvent } from 'react'
import { describeApiError } from '../app/session'

// `POST /api/v1/session` (ADR-024 decisions 1, 5, 8): the primary sign-in
// path, replacing the shared-secret box ADR-022 decision 4 deleted. This
// form itself holds no cookie/session state and does not talk to the
// network singleton directly — `onSubmit` is injected, the same pattern
// TokenPrompt already uses for `submitToken` (see that component's own
// comment). SessionPanel is this form's real caller and wires
// `onSubmit={login}` (`../api`); a test wires a `vi.fn()` instead, so this
// component is unit-testable with no `fetch` involved at all rather than
// needing a real server the way store.ts's own tests do — the network
// behavior `login()` performs is store.test.ts's responsibility, not
// this component's. A successful `onSubmit` call updates the shared
// `Model.session` (store.ts's applySessionResponse) as a side effect this
// component never has to know about; every consumer of that model
// (including the banner this form is normally rendered inside of) reacts
// to the change the same way it reacts to any other model update.
export interface SignInFormProps {
  onSubmit: (name: string, password: string, deviceLabel: string) => Promise<void>
  onSuccess?: () => void
}

export function SignInForm({ onSubmit, onSuccess }: SignInFormProps) {
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
      await onSubmit(name.trim(), password, deviceLabel.trim())
      // No local state to clear "on success" beyond what's about to
      // unmount: the caller (SessionPanel) reacts to model.session
      // becoming authenticated and stops rendering this form. Password
      // is deliberately not cleared-then-left-mounted-behind — see
      // TokenPrompt's own posture — because this form's expected
      // lifetime after success is "gone."
      onSuccess?.()
    } catch (err) {
      setError(describeApiError(err))
      // The password field is cleared on failure but the name is kept,
      // the ordinary "re-enter your password" pattern — the operator
      // typed their name correctly if the error is "invalid name or
      // password" just as often as if it isn't, and re-typing it adds
      // nothing.
      setPassword('')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="session-form" onSubmit={(e) => void handleSubmit(e)} aria-label="Sign in">
      {error !== null && (
        <span role="alert" className="session-form__error">
          {error}
        </span>
      )}
      <label htmlFor={`${formId}-name`}>Name</label>
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
        autoComplete="current-password"
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
        {submitting ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  )
}
