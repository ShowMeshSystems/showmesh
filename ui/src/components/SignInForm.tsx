import { useId, useState, type FormEvent } from 'react'
import { TooManyRequestsError } from '../api'
import { describeApiError } from '../app/session'
import '../styles/session.css'

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
//
// Every field here is gloved (44px) and the submit is 48px
// (UI-DESIGN-GUIDE.md §1): this form gets used on a phone in the cold, not
// only at a desk.
export interface SignInFormProps {
  onSubmit: (name: string, password: string, deviceLabel: string) => Promise<void>
  onSuccess?: () => void
}

export function SignInForm({ onSubmit, onSuccess }: SignInFormProps) {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [deviceLabel, setDeviceLabel] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<{ text: string; rateLimit: boolean } | null>(null)
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
      // ADR-024 decision 8: a rate limit is never a lockout, and must not
      // read as loud as a refused credential — the proxy/CSRF refusal
      // gets the bad treatment, the rate limit gets warn.
      setError({ text: describeApiError(err), rateLimit: err instanceof TooManyRequestsError })
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
    <form className="session-band__form" onSubmit={(e) => void handleSubmit(e)} aria-label="Sign in">
      {error !== null && (
        <div
          role="alert"
          className={`session-form__alert${error.rateLimit ? ' session-form__alert--warn' : ''}`}
        >
          <p className="t-small" style={{ margin: 0 }}>
            {error.text}
          </p>
        </div>
      )}
      <div className="field field--gloved">
        <label className="field__label" htmlFor={`${formId}-name`}>
          Name
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
          autoComplete="current-password"
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
          placeholder="e.g. porch tablet"
          autoComplete="off"
          required
          value={deviceLabel}
          onChange={(event) => setDeviceLabel(event.target.value)}
        />
        <span className="field__help">
          Required. It is how this session appears in Access, and how an administrator revokes it
          without touching the others.
        </span>
      </div>
      <button type="submit" className="btn btn--primary btn--gloved-lg" disabled={submitting}>
        {submitting ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  )
}
