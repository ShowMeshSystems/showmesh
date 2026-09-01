import { useEffect, useRef, useState, type FormEvent } from 'react'
import { claimBootstrap, clearToken, getStoredToken, login, logout, submitToken } from '../api'
import { describeApiError, describeSignInRefusal } from '../domain/session'
import { BlankingPlate, Button, Field, Input, Notice } from '../kit'

/**
 * Bands in the document flow, never a full-screen login page: the chrome
 * and rail stay, and the band pushes the page down. Each band carries the
 * plate for what the rest of this device looks like in that state.
 */

function useSignIn(run: () => Promise<void>) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<unknown>(null)

  function onSubmit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setErr(null)
    run()
      .catch((caught: unknown) => setErr(caught))
      .finally(() => setBusy(false))
  }

  return { busy, err, onSubmit }
}

/** The two refusals the mock draws as headline plus explanation; anything else falls back to one line. */
function SignInFailure({ err }: { err: unknown }) {
  if (err === null) return null
  const refusal = describeSignInRefusal(err)
  if (refusal !== null) {
    return <Notice tone={refusal.kind === 'crossSite' ? 'bad' : 'warn'} headline={refusal.headline} explanation={refusal.explanation} />
  }
  return (
    <p className="sm-band__error" role="alert">
      {describeApiError(err)}
    </p>
  )
}

export function SignedOutBand() {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [deviceName, setDeviceName] = useState('')
  const [token, setToken] = useState('')
  const [tokenOpen, setTokenOpen] = useState(false)
  const [hasToken, setHasToken] = useState(() => getStoredToken() !== null)
  const formRef = useRef<HTMLFormElement>(null)
  const tokenFormRef = useRef<HTMLFormElement>(null)

  function focusFirstInput(form: HTMLFormElement | null) {
    form?.querySelector<HTMLInputElement>('input')?.focus()
  }

  useEffect(() => {
    if (tokenOpen) focusFirstInput(tokenFormRef.current)
  }, [tokenOpen])

  const { busy, err, onSubmit } = useSignIn(() => login(name, password, deviceName.trim()))

  return (
    <section className="sm-band" aria-labelledby="band-signed-out">
      <div className="sm-band__head">
        <h1 className="sm-band__title" id="band-signed-out">Signed out on this device</h1>
        <p className="sm-band__lede">
          Reads need a credential too, not just commands. The coordinator may be running a show right now, this
          device simply cannot see it.
        </p>
        <div className="sm-band__actions">
          <Button variant="primary" size="gloved" onClick={() => focusFirstInput(formRef.current)}>
            Sign in
          </Button>
          <Button onClick={() => setTokenOpen((open) => !open)} aria-expanded={tokenOpen}>
            Use a token instead
          </Button>
          {hasToken && (
            <Button
              variant="quiet"
              onClick={() => {
                clearToken()
                setHasToken(false)
              }}
            >
              Clear stored token
            </Button>
          )}
        </div>
      </div>

      <form className="sm-band__form" onSubmit={onSubmit} ref={formRef}>
        <h3 className="sm-band__form-title">Sign in</h3>
        <SignInFailure err={err} />
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
        <Field label="This device’s name" help="Required. It is how this session appears in Access, and how an administrator revokes it without touching the others.">
          {(field) => (
            <Input
              {...field}
              value={deviceName}
              onChange={(e) => setDeviceName(e.target.value)}
              placeholder="e.g. porch tablet"
              required
            />
          )}
        </Field>
        <Button type="submit" variant="primary" size="gloved" disabled={busy || name === '' || password === '' || deviceName.trim() === ''}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
        <p className="sm-band__footnote">
          A stored machine token is checked before any cookie, so a dead one keeps this device signed out even after
          a good sign-in.{hasToken && <> Clear stored token is offered above because one is stored here.</>}
        </p>
      </form>

      {tokenOpen && (
        <form
          className="sm-band__form"
          ref={tokenFormRef}
          onSubmit={(event) => {
            event.preventDefault()
            submitToken(token.trim())
            setToken('')
            setHasToken(true)
          }}
        >
          <h3 className="sm-band__form-title">Use a token instead</h3>
          <Field label="Machine token" help="Stored for this tab only.">
            {(field) => <Input {...field} value={token} onChange={(e) => setToken(e.target.value)} />}
          </Field>
          <Button type="submit" variant="secondary" size="gloved" disabled={token.trim() === ''}>
            Use token
          </Button>
        </form>
      )}

      <BlankingPlate
        absence="unobserved"
        stamp="No cred"
        eyebrow="This device · unobserved"
        title="Nothing here has ever been collected"
        detail={
          <>
            <span className="sm-plate__detail-line">
              No dashboard, no fleet, no now-playing, not stale, not empty. This device has never held a credential,
              so no read has ever been made from it.
            </span>
            <span className="sm-plate__detail-line">Every destination in the rail still works. All of them will say this until you sign in.</span>
          </>
        }
        actions={
          <>
            <Button onClick={() => focusFirstInput(formRef.current)}>Sign in</Button>
            <Button variant="quiet" onClick={() => setTokenOpen(true)}>
              Paste a machine token
            </Button>
          </>
        }
      />
    </section>
  )
}

/**
 * Sign out lives in the chrome bar next to the identity it signs out
 * (the design mocks draw the identity there but never draw a sign-out
 * control anywhere). It is a sharp control but a reversible one, unlike
 * revoking a credential or removing a node: signing back in undoes it,
 * so a plain arm-then-confirm click is proportionate; it does not ask
 * for typed confirmation text the way Access.tsx and NodeDetail.tsx do
 * for their harder-to-undo actions.
 */
export function SignOutControl({ sessionId }: { sessionId?: string }) {
  const [confirming, setConfirming] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [err, setErr] = useState<unknown>(null)

  const confirm = () => {
    setSigningOut(true)
    setErr(null)
    logout(sessionId)
      .catch((caught: unknown) => setErr(caught))
      .finally(() => {
        setSigningOut(false)
        setConfirming(false)
      })
  }

  if (!confirming) {
    return (
      <Button variant="quiet" size="compact" onClick={() => setConfirming(true)}>
        Sign out
      </Button>
    )
  }

  return (
    <span className="sm-signout-confirm">
      <span className="sm-small sm-muted">Sign out of this device?</span>
      <Button variant="danger" size="compact" disabled={signingOut} onClick={confirm}>
        {signingOut ? 'Signing out…' : 'Confirm sign out'}
      </Button>
      <Button variant="quiet" size="compact" disabled={signingOut} onClick={() => setConfirming(false)}>
        Cancel
      </Button>
      {err !== null && (
        <span className="sm-small" role="alert">
          {describeApiError(err)}
        </span>
      )}
    </span>
  )
}

export function BootstrapBand() {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [deviceName, setDeviceName] = useState('')
  const { busy, err, onSubmit } = useSignIn(() => claimBootstrap(code.trim(), name, password, deviceName.trim()))

  return (
    <section className="sm-band sm-band--alert" aria-labelledby="band-bootstrap">
      <div className="sm-band__head">
        <p className="sm-band__eyebrow">Unclaimed installation</p>
        <h1 className="sm-band__title" id="band-bootstrap">No administrator exists on this coordinator</h1>
        <p className="sm-band__lede">
          It holds zero principals, so nobody can sign in and nothing can be configured. Claim the one-time code to
          create the first administrator.
        </p>
      </div>

      <form className="sm-band__form" onSubmit={onSubmit}>
        <h3 className="sm-band__form-title">Claim the bootstrap code</h3>
        {err !== null && (
          <p className="sm-band__error" role="alert">
            {describeApiError(err)}
          </p>
        )}
        <Field
          label="Bootstrap code"
          help="Readable only from a file in the coordinator’s data volume. Having it proves filesystem access, that is what stops this being a way to become administrator over the network."
        >
          {(field) => <Input {...field} value={code} onChange={(e) => setCode(e.target.value)} className="sm-data" />}
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
        <Field label="This device’s name">
          {(field) => (
            <Input
              {...field}
              value={deviceName}
              onChange={(e) => setDeviceName(e.target.value)}
              placeholder="e.g. install laptop"
              required
            />
          )}
        </Field>
        <Button
          type="submit"
          variant="primary"
          size="gloved"
          disabled={busy || code.trim() === '' || name === '' || password === '' || deviceName.trim() === ''}
        >
          {busy ? 'Claiming…' : 'Claim and sign in'}
        </Button>
        <p className="sm-band__footnote">
          This creates one administrator, deletes the code, and signs this device in. There is no second code, a lost
          password after this needs filesystem access again.
        </p>
        <p className="sm-band__footnote">A wrong code is rate-limited per network, shared with ordinary sign-in, and is never a lockout.</p>
      </form>

      <BlankingPlate
        absence="empty"
        stamp="Empty"
        eyebrow="Installation · empty"
        title="No shows, no nodes, no principals"
        detail={
          <>
            <span className="sm-plate__detail-line">
              This is a settled zero, not missing evidence: the coordinator answered and it holds nothing. Every
              destination in the rail exists and all of them are empty.
            </span>
            <span className="sm-plate__detail-line">
              The first administrator creates the first show. Nodes appear on their own once an agent is pointed at this coordinator.
            </span>
          </>
        }
      />
    </section>
  )
}

/** The connecting band: no session response has arrived yet, so no other band can be chosen. */
export function ConnectingBand({ liveUpdatesConnected }: { liveUpdatesConnected: boolean }) {
  return (
    <section className="sm-band" aria-labelledby="band-connecting">
      <h2 className="sm-band__title" id="band-connecting">Reading the coordinator</h2>
      <p className="sm-band__lede">Nothing below is stale, because nothing has been read yet. No panel will show a number until one arrives.</p>
      <dl className="sm-connecting">
        <div>
          <dt>Session</dt>
          <dd>
            Asked, no answer yet. Until it answers, this device does not know whether it is signed in, signed out, or
            looking at an unclaimed coordinator.
          </dd>
        </div>
        <div>
          <dt>Live updates</dt>
          <dd>
            {liveUpdatesConnected
              ? 'Connected. Observations, run outcomes and now-playing are arriving; only the session response is still outstanding.'
              : 'Not connected. Observations, run outcomes and now-playing all arrive on this one connection.'}
          </dd>
        </div>
      </dl>
      <p className="sm-band__footnote">
        If this stays here more than a few seconds, the coordinator is not answering at this address. Nothing is
        being retried silently, the connection banner will say so and keep saying so.
      </p>
    </section>
  )
}
