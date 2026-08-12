import { useState } from 'react'
import { claimBootstrap, login, logout, submitToken } from '../api'
import { describeApiError, describeSignInState } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { SignInForm } from './SignInForm'
import { BootstrapClaimForm } from './BootstrapClaimForm'
import { TokenPrompt } from './TokenPrompt'

// ADR-024 decision 5 / OPERATOR-UI section 14: "being signed out is a
// persistent state, never a modal at the moment of use." This renders
// unconditionally in Layout.tsx, in the normal document flow alongside
// ConnectionBanner, for every one of the three cases decision 5 names —
// see app/session.ts's describeSignInState for why one boolean covers all
// three without a "which case is this" branch anywhere in this file.
//
// Deliberately not a modal, popover, or anything that steals focus: this
// is a bar, and the sign-in/bootstrap forms it can reveal push the rest
// of the page down rather than covering it, so an operator who is only
// here to look at the dashboard is never interrupted by it.
export function SessionPanel() {
  const model = useModelContext()
  const state = describeSignInState(model.session)

  if (state.kind === 'loading') return null
  if (state.kind === 'bootstrap_required') return <BootstrapBanner />
  if (state.kind === 'signed_out') return <SignedOutBanner />
  return <SignedInBanner name={state.session.principal?.name ?? null} role={state.session.principal?.role ?? null} />
}

function BootstrapBanner() {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="session-panel session-panel--urgent" role="alert">
      <span>
        No administrator exists on this coordinator. Claim the bootstrap code from its data
        volume to create one.
      </span>
      <button type="button" onClick={() => setExpanded((v) => !v)}>
        {expanded ? 'Hide' : 'Claim bootstrap code'}
      </button>
      {expanded && <BootstrapClaimForm onSubmit={claimBootstrap} onSuccess={() => setExpanded(false)} />}
    </div>
  )
}

function SignedOutBanner() {
  const [showSignIn, setShowSignIn] = useState(false)
  const [showBreakGlass, setShowBreakGlass] = useState(false)

  return (
    <div className="session-panel" role="status">
      <span>Signed out on this device.</span>
      <button
        type="button"
        onClick={() => {
          setShowSignIn((v) => !v)
          setShowBreakGlass(false)
        }}
      >
        {showSignIn ? 'Hide' : 'Sign in'}
      </button>
      {/* ADR-024 decision 5: "the bearer-paste field survives as
          break-glass ... so a machine token can act from a phone when the
          session path is broken." Kept reachable at any time a device is
          signed out, not only after a 401 the read loop happens to hit —
          the whole point is that the session path being broken (a
          forgotten password, a corrupt principal store) must not also
          take this affordance down with it. */}
      <button
        type="button"
        onClick={() => {
          setShowBreakGlass((v) => !v)
          setShowSignIn(false)
        }}
      >
        {showBreakGlass ? 'Hide' : 'Use a token instead'}
      </button>
      {showSignIn && <SignInForm onSubmit={login} onSuccess={() => setShowSignIn(false)} />}
      {showBreakGlass && (
        <TokenPrompt
          reason="missing"
          onSubmit={(token) => {
            submitToken(token)
            setShowBreakGlass(false)
          }}
        />
      )}
    </div>
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
    <div className="session-panel session-panel--signed-in" role="status">
      <span>
        Signed in as {name ?? 'unknown principal'}
        {role !== null && ` (${role})`}
      </span>
      <button type="button" onClick={() => void handleSignOut()} disabled={signingOut}>
        {signingOut ? 'Signing out…' : 'Sign out'}
      </button>
      {error !== null && (
        <span role="alert" className="session-panel__error">
          {error}
        </span>
      )}
    </div>
  )
}
