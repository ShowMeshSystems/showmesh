import { useState, type FormEvent } from 'react'

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
    <form className="token-form" onSubmit={handleSubmit} aria-label="Coordinator API token">
      <label htmlFor="showmesh-api-token" className="visually-hidden">
        API token
      </label>
      {reason === 'rejected' && (
        <span role="alert">That token was rejected. Try again, or check the coordinator's configured secret.</span>
      )}
      <input
        id="showmesh-api-token"
        type="password"
        autoComplete="off"
        placeholder="API token"
        value={value}
        onChange={(event) => setValue(event.target.value)}
      />
      <button type="submit">Connect</button>
    </form>
  )
}
