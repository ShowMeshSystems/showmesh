import { useState, type FormEvent } from 'react'
import '../styles/session.css'

// The token prompt for ADR-022 decision 4 / spec section 5.6: on `401`
// the UI prompts for the shared secret and hands it to seam B's token
// submission, which is responsible for sessionStorage and sending it as
// `Authorization: Bearer <token>` from then on. This component never
// stores or reads the token itself.
//
// `reason` distinguishes "no token supplied yet" from "the supplied
// token was rejected" (spec section 5.6: "a wrong secret does not
// present as a missing one"). The spec section 5.4 `ConnectionState`
// code block shows `{ kind: 'unauthorized' }` with no field to carry
// that distinction; seam B's landed `ConnectionState` (src/api/domain.ts)
// adds `reason: 'missing' | 'rejected'` to close that gap, noting the
// deviation in its own comment. This component consumes that field
// directly rather than inferring it from local form-submission state,
// which is what an earlier version of this component did before seam B
// landed.
//
// This is the "Use a token instead" / "Paste a machine token" break-glass
// path from the signed-out band (SessionPanel.tsx), and the same
// component Layout.tsx mounts on `connection.kind === 'unauthorized'`.
export interface TokenPromptProps {
  reason: 'missing' | 'rejected'
  onSubmit: (token: string) => void
}

export function TokenPrompt({ reason, onSubmit }: TokenPromptProps) {
  const [value, setValue] = useState('')

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const token = value.trim()
    if (token.length === 0) return
    onSubmit(token)
    setValue('')
  }

  return (
    <form className="field field--gloved" onSubmit={handleSubmit} aria-label="Coordinator API token">
      <label htmlFor="showmesh-api-token" className="visually-hidden field__label">
        API token
      </label>
      {reason === 'rejected' && (
        <div role="alert" className="session-form__alert">
          <p className="t-small" style={{ margin: 0 }}>
            That token was rejected. Try again, or check the coordinator's configured secret.
          </p>
        </div>
      )}
      <div style={{ display: 'flex', gap: 8 }}>
        <input
          id="showmesh-api-token"
          className="field__input"
          type="password"
          autoComplete="off"
          placeholder="API token"
          value={value}
          onChange={(event) => setValue(event.target.value)}
        />
        <button type="submit" className="btn btn--primary">
          Connect
        </button>
      </div>
    </form>
  )
}
