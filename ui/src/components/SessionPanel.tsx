import { useState } from 'react'
import { claimBootstrap, clearToken, getStoredToken, login, logout, submitToken } from '../api'
import { describeApiError, describeSignInState } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { SignInForm } from './SignInForm'
import { BootstrapClaimForm } from './BootstrapClaimForm'
import { TokenPrompt } from './TokenPrompt'
import { BlankingPlate } from './SharedLayouts'
import '../styles/session.css'

// ADR-024 decision 5 / OPERATOR-UI section 14 / UI-DESIGN-GUIDE.md §2 and
// "Session states are not walls": `GET /session` answers 200 with
// `authenticated: false` — being signed out is a persistent, readable
// state, not an error a caller must catch. This renders unconditionally in
// Layout.tsx, in the normal document flow alongside ConnectionBanner, for
// every one of the three cases decision 5 names — see app/session.ts's
// describeSignInState for why one boolean covers all three without a
// "which case is this" branch anywhere in this file.
//
// Deliberately not a modal, popover, or anything that steals focus: these
// are bands in the document flow that push the rest of the page down, and
// the sign-in/bootstrap forms they reveal do the same, so an operator who
// is only here to look at the dashboard is never interrupted by one.
// `bootstrapRequired` outranks `signed_out` (describeSignInState's own
// contract) because on a fresh install there is nobody to sign in as.
//
// `loading` (no /session response has arrived at all) renders nothing
// here, same as before this pass: the mock's "connecting" state shows two
// ruled strips for Session / Live updates, but that content lives in
// Layout.tsx's `blockContent` placeholder (`model.snapshotReceivedAt ===
// null`), a file this build does not own — see this build's own report.
//
// Operator-reported: the signed-in case used to render here too, spending
// a full-width band on every page on "Signed in as X" and Sign out. That
// case now renders from SessionIdentity below, in Layout.tsx's header,
// where a name and one button fit without a band.
export function SessionPanel() {
  const model = useModelContext()
  const state = describeSignInState(model.session)

  if (state.kind === 'loading') return null
  if (state.kind === 'bootstrap_required') return <BootstrapBanner />
  if (state.kind === 'signed_out') return <SignedOutBanner />
  return null
}

// The signed-in identity and its sign-out control, for Layout.tsx's header.
// Same `describeSignInState` and same non-gating on `blockContent` as
// SessionPanel above, for the same reason: an operator must be able to see
// who they are signed in as, and sign out, even while the rest of the page
// is showing "no data yet". Renders nothing for every other state -- those
// stay SessionPanel's band, above.
export function SessionIdentity() {
  const model = useModelContext()
  const state = describeSignInState(model.session)

  if (state.kind !== 'signed_in') return null
  return (
    <SignedInBanner name={state.session.principal?.name ?? null} role={state.session.principal?.role ?? null} />
  )
}

// The "empty" absence class (a settled zero — UI-DESIGN-GUIDE.md §4),
// never "unobserved": the coordinator answered and it holds nothing.
//
// Unlike the signed-out band below, the claim form here is always shown,
// never behind a toggle: there is no ordinary use of this coordinator
// until it is claimed, so there is nothing the form would otherwise be
// hiding a plain page underneath.
function BootstrapBanner() {
  return (
    <>
      <section className="session-band session-band--alert" role="alert" aria-labelledby="bootstrap-band-h">
        <p className="t-meta" style={{ margin: 0, color: 'var(--bad-fg)' }}>
          Unclaimed installation
        </p>
        <h1 id="bootstrap-band-h" className="t-display session-band__title">
          No administrator exists on this coordinator
        </h1>
        <p className="session-band__body">
          It holds zero principals, so nobody can sign in and nothing can be configured. Claim the
          one-time code from its data volume to create the first administrator.
        </p>
        <BootstrapClaimForm onSubmit={claimBootstrap} />
      </section>

      <div className="session-plate-wrap">
        <BlankingPlate
          variant="empty"
          stamp="Empty"
          eyebrow="Installation · empty"
          title="No shows, no nodes, no principals"
          explanation={
            <>
              This is a settled zero, not missing evidence: the coordinator answered and it holds
              nothing. Every destination in the rail exists and all of them are empty. The first
              administrator creates the first show; nodes appear on their own once an agent is
              pointed at this coordinator.
            </>
          }
        />
      </div>
    </>
  )
}

// The "unobserved" absence class (never collected — UI-DESIGN-GUIDE.md
// §4), never "empty": this device has never held a credential, so no read
// has ever been made from it, and that is a different fact than the
// coordinator holding nothing.
function SignedOutBanner() {
  const [showSignIn, setShowSignIn] = useState(false)
  const [showBreakGlass, setShowBreakGlass] = useState(false)
  // Finding: a stored break-glass token is checked on every request
  // (client.ts) ahead of any cookie, with no fallthrough — a token left
  // over from a previous, now-invalid credential permanently shadows a
  // perfectly good session cookie, and this banner showing "signed out"
  // is exactly what that looks like from here. store.ts's login()/
  // claimBootstrap() now clear it on a SUCCESSFUL sign-in, but an
  // operator stuck behind a dead stored token needs a way to clear it
  // directly, without first needing the sign-in this same token is
  // blocking. Only offered when a token is actually stored — an always-
  // present button would assert a fact ("there is something to clear")
  // that usually is not true.
  const storedTokenPresent = getStoredToken() !== null

  function toggleSignIn(): void {
    setShowSignIn((v) => !v)
    setShowBreakGlass(false)
  }

  function toggleBreakGlass(): void {
    setShowBreakGlass((v) => !v)
    setShowSignIn(false)
  }

  return (
    <>
      <section className="session-band" role="status" aria-labelledby="signed-out-band-h">
        <div className="session-band__row">
          <div style={{ minWidth: 0 }}>
            <h1 id="signed-out-band-h" className="t-heading session-band__title">
              Signed out on this device
            </h1>
            <p className="session-band__body">
              Reads need a credential too, not just commands. The coordinator may be running a show
              right now, and this device simply cannot see it.
            </p>
          </div>
          <div className="session-band__controls">
            <button type="button" className="btn btn--primary" aria-expanded={showSignIn} onClick={toggleSignIn}>
              {showSignIn ? 'Hide' : 'Sign in'}
            </button>
            <button
              type="button"
              className="btn btn--secondary"
              aria-expanded={showBreakGlass}
              onClick={toggleBreakGlass}
            >
              {showBreakGlass ? 'Hide' : 'Use a token instead'}
            </button>
            {storedTokenPresent && (
              <button type="button" className="btn btn--quiet" onClick={() => clearToken()}>
                Clear stored token
              </button>
            )}
          </div>
        </div>

        {showSignIn && <SignInForm onSubmit={login} onSuccess={() => setShowSignIn(false)} />}
        {/* ADR-024 decision 5: "the bearer-paste field survives as
            break-glass ... so a machine token can act from a phone when the
            session path is broken." Kept reachable at any time a device is
            signed out, not only after a 401 the read loop happens to hit —
            the whole point is that the session path being broken (a
            forgotten password, a corrupt principal store) must not also
            take this affordance down with it. */}
        {showBreakGlass && (
          <TokenPrompt
            reason="missing"
            onSubmit={(token) => {
              submitToken(token)
              setShowBreakGlass(false)
            }}
          />
        )}
      </section>

      <div className="session-plate-wrap">
        <BlankingPlate
          variant="unobserved"
          stamp="No cred"
          eyebrow="This device · unobserved"
          title="Nothing here has ever been collected"
          explanation={
            <>
              No dashboard, no fleet, no now-playing, not stale, not empty. This device has never
              held a credential, so no read has ever been made from it. Every destination in the
              rail still works; all of them will say this until you sign in.
            </>
          }
          actions={
            <>
              <button type="button" className="btn btn--secondary" onClick={toggleSignIn}>
                Sign in
              </button>
              <button type="button" className="btn btn--quiet" onClick={toggleBreakGlass}>
                Paste a machine token
              </button>
            </>
          }
        />
      </div>
    </>
  )
}

interface SignedInBannerProps {
  name: string | null
  role: string | null
}

function SignedInBanner({ name, role }: SignedInBannerProps) {
  const [signingOut, setSigningOut] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSignOut(): Promise<void> {
    if (signingOut) return
    setSigningOut(true)
    setError(null)
    try {
      await logout()
    } catch (err) {
      // Item 4: a cookie-authenticated write (this one) rejected for a
      // missing Sec-Fetch-Site header must explain itself rather than
      // just looking broken — describeApiError is what supplies that
      // sentence for CSRFRejectedError specifically.
      setError(describeApiError(err))
    } finally {
      setSigningOut(false)
    }
  }

  return (
    <div className="t-small" role="status" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 14px' }}>
      <span style={{ color: 'var(--text-muted)' }}>
        Signed in as {name ?? 'unknown principal'}
        {role !== null && ` (${role})`}
      </span>
      <button type="button" className="btn btn--quiet btn--compact" onClick={() => void handleSignOut()} disabled={signingOut}>
        {signingOut ? 'Signing out…' : 'Sign out'}
      </button>
      {error !== null && (
        <span role="alert" style={{ color: 'var(--bad-fg)' }}>
          {error}
        </span>
      )}
    </div>
  )
}
