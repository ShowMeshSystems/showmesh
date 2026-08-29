import { useState, type FormEvent } from 'react'
import { claimBootstrap, login, submitToken } from '../api'
import { describeApiError } from '../domain/session'
import { BlankingPlate, Button, Field, Input } from '../kit'

/**
 * Signed-out and bootstrap are readable states, not walls: they render as a
 * band in the document flow with the chrome and rail intact. The full
 * treatment, including the proxy and rate-limit sign-in errors, lands with
 * the Session States screen.
 */

function useSubmit(run: () => Promise<void>) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function onSubmit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    run()
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setBusy(false))
  }

  return { busy, error, onSubmit }
}

export function SignedOutBand() {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [token, setToken] = useState('')
  const { busy, error, onSubmit } = useSubmit(() => login(name, password, deviceLabel()))

  return (
    <div className="sm-band">
      <BlankingPlate
        absence="signedOut"
        stamp="Out"
        eyebrow="This device · signed out"
        title="Signed out on this device"
        detail="The coordinator answered, and it does not recognise this browser. Everything below reads at whatever this device may read without a principal."
        actions={
          <>
            <form className="sm-band__form" onSubmit={onSubmit}>
              <Field label="Name">
                {(field) => (
                  <Input {...field} value={name} onChange={(e) => setName(e.target.value)} autoComplete="username" />
                )}
              </Field>
              <Field label="Password">
                {(field) => (
                  <Input
                    {...field}
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="current-password"
                  />
                )}
              </Field>
              <Button type="submit" variant="primary" size="gloved" disabled={busy}>
                {busy ? 'Signing in…' : 'Sign in'}
              </Button>
            </form>
            <form
              className="sm-band__form"
              onSubmit={(event) => {
                event.preventDefault()
                submitToken(token.trim())
                setToken('')
              }}
            >
              <Field label="Machine token" help="Stored for this tab only.">
                {(field) => <Input {...field} value={token} onChange={(e) => setToken(e.target.value)} />}
              </Field>
              <Button type="submit" variant="secondary" size="gloved" disabled={token.trim() === ''}>
                Use token
              </Button>
            </form>
          </>
        }
      />
      {error !== null && <p className="sm-band__error" role="alert">{error}</p>}
    </div>
  )
}

export function BootstrapBand() {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const { busy, error, onSubmit } = useSubmit(() => claimBootstrap(code.trim(), name, password, deviceLabel()))

  return (
    <div className="sm-band">
      <BlankingPlate
        absence="unobserved"
        stamp="New"
        eyebrow="This coordinator · no principals"
        title="No administrator exists on this coordinator"
        detail="Nobody can sign in until the first administrator is claimed. The claim code is printed in the coordinator's log at startup."
        actions={
          <form className="sm-band__form" onSubmit={onSubmit}>
            <Field label="Claim code">
              {(field) => <Input {...field} value={code} onChange={(e) => setCode(e.target.value)} />}
            </Field>
            <Field label="Administrator name">
              {(field) => (
                <Input {...field} value={name} onChange={(e) => setName(e.target.value)} autoComplete="username" />
              )}
            </Field>
            <Field label="Password">
              {(field) => (
                <Input
                  {...field}
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="new-password"
                />
              )}
            </Field>
            <Button type="submit" variant="primary" size="gloved" disabled={busy}>
              {busy ? 'Claiming…' : 'Claim this coordinator'}
            </Button>
          </form>
        }
      />
      {error !== null && <p className="sm-band__error" role="alert">{error}</p>}
    </div>
  )
}

function deviceLabel(): string {
  return `Operator UI · ${window.location.host}`
}
