import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  createPrincipal,
  disablePrincipal,
  enablePrincipal,
  issuePrincipalToken,
  listPrincipals,
  listPrincipalTokens,
  resetPrincipalPassword,
  revokePrincipalToken,
  setPrincipalRole,
  type PrincipalObject,
  type TokenObject,
} from '../api'
import { describeApiError, describeSignInState, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { useUnsavedChanges } from '../app/UnsavedChanges'
import { EmptyBlock, FailedBlock, LoadingBlock, PlannedFeature, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'
import '../styles/session.css'

// Track G seam G-5: identity administration's own view — the Access page.
// Reads render under principal:read; every control follows ScopedButton's
// existing posture (an unknown or stale scope list renders as not
// permitted, never permissive), mirroring Configuration.tsx's identical
// treatment of config:write. Disabling the coordinator's last enabled
// administrator, changing its role away from one that holds
// principal:write, or revoking its last reachable credential, is refused
// server-side with 409 (ADR-039 decision 8) — this view renders that
// refusal exactly like every other write error, via describeApiError, and
// never second-guesses it client-side.
//
// UI-DESIGN-GUIDE.md §8 / ROUTE-MAP.md: `/access` is the one Settings tab
// that leaves the screen (marked with an arrow wherever Settings links to
// it), rendered here with its own page header rather than the Settings
// tab strip.
const PRINCIPAL_READ_SCOPE = 'principal:read'
const PRINCIPAL_WRITE_SCOPE = 'principal:write'

// The assignable roles: the wire enum (PrincipalObject.role) minus
// 'recovery', which belongs only to the reserved recovery principal and is
// deliberately not assignable here. The Record keying makes any drift from
// the generated enum a type error rather than a silently stale list.
type AssignableRole = Exclude<PrincipalObject['role'], 'recovery'>
const ROLE_SET: Record<AssignableRole, true> = { viewer: true, operator: true, admin: true, scheduler: true }
const ROLES = Object.keys(ROLE_SET) as AssignableRole[]

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; principals: PrincipalObject[] }

export function Access() {
  const model = useModelContext()
  // Same posture as Configuration.tsx's own scopeGate: every request this
  // page could make (including the list read) is refused identically
  // without principal:read, so the whole page treats a missing/stale/
  // unavailable grant as a reason not to even attempt the fetch.
  const readGate = evaluateScope(model.session, model.sessionFetchFailed, PRINCIPAL_READ_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [creating, setCreating] = useState(false)
  const signInState = describeSignInState(model.session)
  const selfPrincipalId = signInState.kind === 'signed_in' ? (signInState.session.principal?.id ?? null) : null
  const selfScopes = signInState.kind === 'signed_in' ? signInState.session.scopes : null

  const permissionState = !readGate.allowed && (
    signInState.kind === 'loading' ? <LoadingBlock title="Loading permissions" reason="Waiting for the coordinator to report what this device may do." />
      : signInState.kind === 'bootstrap_required' ? <UnavailableBlock title="Setup required" reason="No administrator exists on this coordinator. Claim the bootstrap code from its data volume to create one before managing access." />
        : signInState.kind === 'signed_out' ? <UnavailableBlock title="Signed out" reason="This device is not signed in, so it cannot manage access." />
          : model.sessionFetchFailed || signInState.session.scopesState !== 'current' ? <StaleBlock title="Stale permission evidence" reason="Access remains unavailable until the coordinator can confirm this device’s current permissions." />
            : <UnavailableBlock title="Insufficient permission" reason={readGate.reason} />
  )

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const resp = await listPrincipals()
        if (cancelled) return
        setState({ kind: 'loaded', principals: resp.principals })
      } catch (err) {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, reloadGeneration])

  function reload(): void {
    setReloadGeneration((g) => g + 1)
  }

  return (
    <>
      <section className="page-header" aria-labelledby="access-h">
        <p className="page-header__breadcrumb">
          <Link to="/settings">Settings</Link> <span aria-hidden="true">/</span> Access
        </p>
        <h1 id="access-h" className="t-display page-header__title">
          Access
        </h1>
        <p className="page-header__meta" style={{ maxWidth: '80ch' }}>
          Who can change this installation, and what each of them may change. Every write is
          attributed to a principal; nothing here affects who can <em>see</em> the show.
        </p>

        <div className="ruled-strip" style={{ marginTop: 20 }}>
          <span className="ruled-strip__state t-meta">Reads are open</span>
          <div>
            <p className="ruled-strip__explanation" style={{ marginTop: 0 }}>
              Anyone who can reach this coordinator can view the show without signing in. That is
              deliberate: a credential problem must never cost an operator sight of a running show.
              Close it with <code className="t-data">SHOWMESH_API_CLOSE_READS</code> if the port is
              exposed beyond the show VLAN.
            </p>
          </div>
        </div>
      </section>

      <div className="page-body">
        {permissionState}

        {readGate.allowed && (
          <>
            {state.kind === 'loading' && <LoadingBlock title="Loading principals" reason="Loading coordinator access records…" />}
            {state.kind === 'error' && (
              <FailedBlock title="Principals could not be loaded" reason={<>{state.message} <button type="button" className="btn btn--secondary btn--compact" onClick={reload}>Retry</button></>} />
            )}
            {state.kind === 'loaded' && (
              <>
                <section aria-labelledby="ac-princ" style={{ marginTop: 26 }}>
                  <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 14, flexWrap: 'wrap' }}>
                    <h2 id="ac-princ" className="t-heading" style={{ margin: 0 }}>
                      Principals
                    </h2>
                    <ScopedButton
                      requiredScope={PRINCIPAL_WRITE_SCOPE}
                      className="btn btn--primary btn--compact"
                      onClick={() => setCreating((v) => !v)}
                    >
                      {creating ? 'Hide' : 'Add principal'}
                    </ScopedButton>
                  </div>
                  <p className="t-small" style={{ margin: '8px 0 0', color: 'var(--text-muted)', maxWidth: '80ch' }}>
                    Scopes are granted as bundles defined on the coordinator. Only the scopes this
                    signed-in principal itself holds are known here: the coordinator has no read
                    surface for another principal’s resolved scope list.
                  </p>

                  {creating && (
                    <CreatePrincipalForm
                      onCreated={() => {
                        setCreating(false)
                        reload()
                      }}
                    />
                  )}

                  {state.principals.length === 0 ? (
                    <EmptyBlock title="No principals" reason="The coordinator returned no principals." />
                  ) : (
                    <div className="table-wrap card" style={{ marginTop: 14 }}>
                      <table className="table table--full">
                        <thead>
                          <tr>
                            <th>Principal</th>
                            <th>Role / scopes held</th>
                            <th>State</th>
                            <th>Created</th>
                            <th aria-label="Actions" />
                          </tr>
                        </thead>
                        <tbody>
                          {state.principals.map((p) => (
                            <PrincipalRow
                              key={p.id}
                              principal={p}
                              isSelf={p.id === selfPrincipalId}
                              selfScopes={selfScopes}
                              onChanged={reload}
                            />
                          ))}
                        </tbody>
                      </table>
                      <p className="table__footer-note">
                        A principal with no write scopes can still read everything: that is what
                        "reads are open" means, not a permission you granted.
                      </p>
                    </div>
                  )}

                  <PlannedFeature
                    title="Scopes held, for every principal"
                    why={
                      <>
                        <code className="t-data">GET /principals</code> returns no scopes field, and
                        there is no role to scopes resolution endpoint, so a resolved scope list can
                        only be shown for the signed-in principal, from{' '}
                        <code className="t-data">session.scopes</code> (the real chips above, on your
                        own row).
                      </>
                    }
                    preview={
                      <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>
                        <span className="access-scope-chip access-scope-chip--muted">night:command</span>
                        <span className="access-scope-chip access-scope-chip--muted">show:macro:run</span>
                      </div>
                    }
                  />

                  <PlannedFeature
                    title="Last used, and a revoke suggestion built on it"
                    why={
                      <>
                        <code className="t-data">PrincipalObject</code> carries no{' '}
                        <code className="t-data">lastUsedAt</code> field (only individual tokens do),
                        so there is nothing to roll up into "unused N days" or a suggestion to revoke
                        a principal that has gone quiet.
                      </>
                    }
                    preview={
                      <span className="status-pair status-pair--warn">⚠ Consider revoking</span>
                    }
                  />
                </section>

                <AttributionSection />
              </>
            )}
          </>
        )}
      </div>
    </>
  )
}

function CreatePrincipalForm({ onCreated }: { onCreated: () => void }) {
  const { clearUnsavedChanges } = useUnsavedChanges('access-create-principal')
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'human' | 'machine'>('human')
  const [role, setRole] = useState<(typeof ROLES)[number]>('viewer')
  const [password, setPassword] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const savingRef = useRef(false)

  async function handleCreate(): Promise<void> {
    // Synchronous, shared-across-closures guard against a double click
    // creating two principals — see Configuration.tsx's savingRef for why
    // React state (`saving`) alone is not enough.
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setError(null)
    try {
      await createPrincipal({ name: name.trim(), kind, role, password })
      setName('')
      setPassword('')
      clearUnsavedChanges()
      onCreated()
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <div data-unsaved-form="access-create-principal" style={{ marginTop: 14 }}>
      <h3 className="t-subhead" style={{ margin: 0 }}>
        New principal
      </h3>
      {error !== null && (
        <div role="alert" className="session-form__alert" style={{ marginTop: 8 }}>
          <p className="t-small" style={{ margin: 0 }}>
            {error}
          </p>
        </div>
      )}
      <div className="access-inline-form">
        <label className="field">
          <span className="field__label">Name</span>
          <input className="field__input" type="text" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="field">
          <span className="field__label">Kind</span>
          <select className="field__input" value={kind} onChange={(e) => setKind(e.target.value as 'human' | 'machine')}>
            <option value="human">human</option>
            <option value="machine">machine</option>
          </select>
        </label>
        <label className="field">
          <span className="field__label">Role</span>
          <select className="field__input" value={role} onChange={(e) => setRole(e.target.value as (typeof ROLES)[number])}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="field__label">Password (optional for a machine principal)</span>
          <input className="field__input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <ScopedButton
          requiredScope={PRINCIPAL_WRITE_SCOPE}
          className="btn btn--primary"
          onClick={() => void handleCreate()}
          busy={saving}
          busyReason="Creating this principal…"
        >
          {saving ? 'Creating…' : 'Create principal'}
        </ScopedButton>
      </div>
    </div>
  )
}

function PrincipalRow({
  principal,
  isSelf,
  selfScopes,
  onChanged,
}: {
  principal: PrincipalObject
  isSelf: boolean
  selfScopes: string[] | null
  onChanged: () => void
}) {
  const { clearUnsavedChanges: clearRoleUnsavedChanges } = useUnsavedChanges(`access-principal-${principal.id}-role`)
  const { clearUnsavedChanges: clearPasswordUnsavedChanges } = useUnsavedChanges(`access-principal-${principal.id}-password`)
  const [roleDraft, setRoleDraft] = useState(principal.role)
  const [busyAction, setBusyAction] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tokensOpen, setTokensOpen] = useState(false)
  const [passwordOpen, setPasswordOpen] = useState(false)
  const [password, setPassword] = useState('')
  const busyRef = useRef(false)

  // Reserved (the built-in Resolume recovery principal, identity/types.go's
  // ReservedResolumeRecoveryPrincipalID): every write the coordinator
  // itself refuses is refused here too, before a doomed request is even
  // sent — this mirrors the server's own ErrReservedPrincipal message
  // rather than inventing a second explanation of the same rule.
  const locked = principal.reserved

  async function runAction(action: string, fn: () => Promise<void>): Promise<void> {
    if (busyRef.current) return
    busyRef.current = true
    setBusyAction(action)
    setError(null)
    try {
      await fn()
      onChanged()
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      busyRef.current = false
      setBusyAction(null)
    }
  }

  async function handleSetRole(): Promise<void> {
    await runAction('role', async () => {
      await setPrincipalRole(principal.id, { role: roleDraft })
      clearRoleUnsavedChanges()
    })
  }

  async function handleToggleDisabled(): Promise<void> {
    await runAction(principal.disabled ? 'enable' : 'disable', async () => {
      if (principal.disabled) {
        await enablePrincipal(principal.id)
      } else {
        await disablePrincipal(principal.id)
      }
    })
  }

  async function handleResetPassword(): Promise<void> {
    await runAction('password', async () => {
      await resetPrincipalPassword(principal.id, { password })
      setPassword('')
      setPasswordOpen(false)
      clearPasswordUnsavedChanges()
    })
  }

  return (
    <>
      <tr className={isSelf ? 'access-row--self' : undefined} aria-current={isSelf ? 'true' : undefined}>
        <td>
          <span className="t-body" style={{ fontWeight: 600 }}>
            {principal.name}
          </span>{' '}
          {isSelf && <span className="access-you-badge">You</span>}
          {principal.reserved && <span className="t-small" style={{ color: 'var(--text-faint)' }}> (reserved)</span>}
          <br />
          <span className="t-data t-small" style={{ color: 'var(--text-faint)' }}>
            {principal.kind}
          </span>
        </td>
        <td>
          {locked ? (
            <span className="t-body">{principal.role}</span>
          ) : (
            <span data-unsaved-form={`access-principal-${principal.id}-role`} style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
              <select className="field__input" style={{ height: 'var(--control-compact)', width: 'auto' }} value={roleDraft} onChange={(e) => setRoleDraft(e.target.value as (typeof ROLES)[number])}>
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
              <ScopedButton
                requiredScope={PRINCIPAL_WRITE_SCOPE}
                className="btn btn--secondary btn--compact"
                onClick={() => void handleSetRole()}
                busy={busyAction === 'role'}
                busyReason="Changing this principal's role…"
              >
                Set role
              </ScopedButton>
            </span>
          )}
          <div style={{ marginTop: 6, display: 'flex', gap: 4, flexWrap: 'wrap' }}>
            {isSelf && selfScopes !== null ? (
              selfScopes.length === 0 ? (
                <span className="t-small" style={{ color: 'var(--text-faint)' }}>No write scopes</span>
              ) : (
                selfScopes.map((scope) => (
                  <span key={scope} className="access-scope-chip">
                    {scope}
                  </span>
                ))
              )
            ) : (
              <span className="t-small" style={{ color: 'var(--text-faint)' }}>
                Not built for another principal, see "Scopes held" below.
              </span>
            )}
          </div>
        </td>
        <td>
          <span className={`status-pair ${principal.disabled ? 'status-pair--bad' : 'status-pair--good'}`}>
            {principal.disabled ? 'Disabled' : 'Enabled'}
          </span>
          <br />
          <span className="t-small" style={{ color: 'var(--text-faint)' }}>
            {principal.hasPassword ? 'Password set' : 'No password'}
          </span>
        </td>
        <td className="t-data t-small">{formatAbsolute(principal.createdAt)}</td>
        <td>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
            {!locked && (
              <>
                <ScopedButton
                  requiredScope={PRINCIPAL_WRITE_SCOPE}
                  className="btn btn--secondary btn--compact"
                  onClick={() => void handleToggleDisabled()}
                  busy={busyAction === 'enable' || busyAction === 'disable'}
                  busyReason={principal.disabled ? 'Enabling…' : 'Disabling…'}
                >
                  {principal.disabled ? 'Enable' : 'Disable'}
                </ScopedButton>
                <button type="button" className="btn btn--quiet btn--compact" onClick={() => setPasswordOpen((v) => !v)} aria-expanded={passwordOpen}>
                  {passwordOpen ? 'Cancel reset' : 'Reset password'}
                </button>
              </>
            )}
            <button type="button" className="btn btn--quiet btn--compact" onClick={() => setTokensOpen((v) => !v)} aria-expanded={tokensOpen}>
              {tokensOpen ? 'Hide credentials' : 'Credentials'}
            </button>
          </div>
        </td>
      </tr>
      {error !== null && (
        <tr>
          <td colSpan={5}>
            <div role="alert" className="session-form__alert">
              <p className="t-small" style={{ margin: 0 }}>
                {error}
              </p>
            </div>
          </td>
        </tr>
      )}
      {passwordOpen && !locked && (
        <tr>
          <td colSpan={5} data-unsaved-form={`access-principal-${principal.id}-password`}>
            <label className="field" style={{ maxWidth: 280 }}>
              <span className="field__label">New password</span>
              <input className="field__input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>
            <div style={{ marginTop: 8 }}>
              <ScopedButton
                requiredScope={PRINCIPAL_WRITE_SCOPE}
                className="btn btn--primary btn--compact"
                onClick={() => void handleResetPassword()}
                busy={busyAction === 'password'}
                busyReason="Resetting this principal's password…"
              >
                Reset password
              </ScopedButton>
            </div>
            <p className="t-small" style={{ color: 'var(--text-muted)', marginTop: 6 }}>
              Every existing session and token for this principal will be invalidated.
            </p>
          </td>
        </tr>
      )}
      {tokensOpen && (
        <tr>
          <td colSpan={5}>
            <TokensPanel principalID={principal.id} locked={locked} />
          </td>
        </tr>
      )}
    </>
  )
}

function TokensPanel({ principalID, locked }: { principalID: string; locked: boolean }) {
  const { clearUnsavedChanges } = useUnsavedChanges(`access-principal-${principalID}-token-issue`)
  const [tokens, setTokens] = useState<TokenObject[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [label, setLabel] = useState('')
  const [expires, setExpires] = useState('')
  const [issuedValue, setIssuedValue] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    async function load(): Promise<void> {
      try {
        const resp = await listPrincipalTokens(principalID)
        if (!cancelled) {
          setLoadError(null)
          setTokens(resp.tokens)
        }
      } catch (err) {
        if (!cancelled) setLoadError(describeApiError(err))
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [principalID, reloadGeneration])

  function reload(): void {
    // Show the loading state and bump the generation so the effect above
    // actually re-fetches (its deps include reloadGeneration).
    setTokens(null)
    setReloadGeneration((g) => g + 1)
  }

  function retryLoad(): void {
    setLoadError(null)
    setTokens(null)
    setReloadGeneration((g) => g + 1)
  }

  async function handleIssue(): Promise<void> {
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    setError(null)
    try {
      // exactOptionalPropertyTypes forbids an explicit `undefined` value
      // for an optional key -- built conditionally instead of via `? :
      // undefined`, matching what an absent JSON key means for
      // IssueTokenRequest.expiresAt (ADR-039 decision 5: absent, not
      // present-and-null, and this request never sends the null variant
      // at all since there is nothing this form's own empty state needs
      // it to distinguish from absent).
      const trimmedLabel = label.trim()
      const trimmedExpires = expires.trim()
      const resp = await issuePrincipalToken(principalID, {
        ...(trimmedLabel !== '' ? { label: trimmedLabel } : {}),
        ...(trimmedExpires !== '' ? { expiresAt: trimmedExpires } : {}),
      })
      setIssuedValue(resp.value)
      setLabel('')
      setExpires('')
      clearUnsavedChanges()
      reload()
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  async function handleRevoke(tokenID: string): Promise<void> {
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    setError(null)
    try {
      await revokePrincipalToken(principalID, tokenID)
      reload()
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  return (
    <div style={{ padding: '10px 0' }}>
      <h4 className="t-subhead" style={{ margin: 0 }}>
        Credentials
      </h4>
      <PlannedFeature
        title="Sessions and devices for this principal"
        headingLevel={3}
        why={
          <>
            There is no <code className="t-data">GET</code> for a principal's own sessions, only{' '}
            <code className="t-data">DELETE /session</code> (this device's own session, or a
            specific <code className="t-data">sessionId</code> the caller already knows). The
            device label from sign-in has nowhere to be listed and revoked from here yet; the
            token table below is the real, working credential path.
          </>
        }
        preview={
          <div className="ruled-strip" style={{ padding: '10px 0' }}>
            <span className="ruled-strip__state t-meta">Device</span>
            <div>
              <p className="ruled-strip__fact" style={{ margin: 0 }}>porch tablet</p>
              <p className="ruled-strip__explanation">Signed in 12 Aug 09:38</p>
            </div>
          </div>
        }
      />
      <p className="t-small" style={{ margin: '10px 0 0', color: 'var(--text-muted)' }}>
        A token is shown once, at the moment it is issued, and never again. If it is lost, revoke it
        and issue another.
      </p>
      {error !== null && (
        <div role="alert" className="session-form__alert" style={{ marginTop: 8 }}>
          <p className="t-small" style={{ margin: 0 }}>
            {error}
          </p>
        </div>
      )}
      {issuedValue !== null && (
        <p role="status" className="t-small" style={{ marginTop: 8 }}>
          This token is displayed exactly once and cannot be retrieved again; store it now:{' '}
          <code className="t-data">{issuedValue}</code>
        </p>
      )}
      {loadError !== null ? (
        <FailedBlock title="Tokens could not be loaded" reason={<>{loadError} <button type="button" className="btn btn--secondary btn--compact" onClick={retryLoad}>Retry</button></>} headingLevel={3} />
      ) : tokens === null ? (
        <LoadingBlock title="Loading tokens" reason="Loading coordinator access records…" headingLevel={3} />
      ) : tokens.length === 0 ? (
        <EmptyBlock title="No tokens" reason="No tokens are currently issued for this principal." headingLevel={3} />
      ) : (
        <div className="table-wrap card" style={{ marginTop: 10 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Hint</th>
                <th>Label</th>
                <th>Created</th>
                <th>Expires</th>
                <th>Last used</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td className="t-data">{t.hint}</td>
                  <td>{t.label || '-'}</td>
                  <td className="t-data t-small">{formatAbsolute(t.createdAt)}</td>
                  <td className="t-data t-small">{t.expiresAt === null ? 'never' : formatAbsolute(t.expiresAt)}</td>
                  <td className="t-data t-small">{t.lastUsedAt === null ? 'never' : formatAbsolute(t.lastUsedAt)}</td>
                  <td>
                    {!locked && (
                      <ScopedButton
                        requiredScope={PRINCIPAL_WRITE_SCOPE}
                        className="btn btn--destructive btn--compact"
                        onClick={() => void handleRevoke(t.id)}
                        busy={busy}
                        busyReason="Revoking this token…"
                      >
                        Revoke
                      </ScopedButton>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!locked && (
        <div data-unsaved-form={`access-principal-${principalID}-token-issue`} className="access-inline-form">
          <label className="field">
            <span className="field__label">Label</span>
            <input className="field__input" type="text" value={label} onChange={(e) => setLabel(e.target.value)} />
          </label>
          <label className="field">
            <span className="field__label">Expires (RFC3339, optional, default never)</span>
            <input className="field__input field__input--data" type="text" placeholder="2027-01-15T00:00:00Z" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </label>
          <ScopedButton
            requiredScope={PRINCIPAL_WRITE_SCOPE}
            className="btn btn--primary"
            onClick={() => void handleIssue()}
            busy={busy}
            busyReason="Issuing a new token…"
          >
            Issue token
          </ScopedButton>
        </div>
      )}
    </div>
  )
}

// The mock's Attribution section names three facts: the audit store's
// live writability, this session's own attribution completeness, and the
// bootstrap claim record. None of the three has a general-purpose read
// surface from this view: audit-store writability only ever surfaces as
// the refusal on a specific night command (503, ADR-024 decision 11);
// per-session attribution belongs to the night session domain
// (attributionDegraded on NightSessionState), not to Access; and no field
// anywhere distinguishes "created by the bootstrap claim" from "created
// later" on a PrincipalObject. OWNER RULING 2026-08-29: these are not
// data the coordinator holds and withheld -- they are read surfaces that
// were never built -- so they render as PlannedFeature, never
// UnavailableBlock, and stay split into two stamps (audit-store status
// is a coordinator-wide fact; bootstrap provenance is a per-principal
// one) rather than folded into a single note.
function AttributionSection() {
  return (
    <section aria-labelledby="ac-audit" style={{ marginTop: 26, maxWidth: 800 }}>
      <h2 id="ac-audit" className="t-heading" style={{ margin: 0 }}>
        Attribution
      </h2>
      <div className="ruled-strip" style={{ marginTop: 10 }}>
        <span className="ruled-strip__state t-meta">Activity log</span>
        <div>
          <p className="ruled-strip__explanation" style={{ marginTop: 0 }}>
            Every write here is audited. Audit rows in the activity log need an audit-read scope;
            system events do not. <Link to="/monitor/activity">Open the log</Link>
          </p>
        </div>
      </div>
      <PlannedFeature
        title="Audit store status, and this session's own attribution completeness"
        why={
          <>
            Nothing reports whether the audit store is currently writable. It only ever shows
            itself by refusing a night command. Whether every step a session took was recorded
            against a principal is reported per night session, on{' '}
            <code className="t-data">NightSessionState.attributionDegraded</code>, and not as a
            general read here.
          </>
        }
        preview={
          <div className="ruled-strip" style={{ padding: '10px 0' }}>
            <span className="ruled-strip__state t-meta" style={{ color: 'var(--good-fg)' }}>
              ✓ Audit store
            </span>
            <div>
              <p className="ruled-strip__fact" style={{ margin: 0 }}>Writable</p>
              <p className="ruled-strip__explanation">
                Four of the night commands refuse outright rather than run unattributed.
              </p>
            </div>
          </div>
        }
      />
      <PlannedFeature
        title="Bootstrap claim provenance"
        why={
          <>
            <code className="t-data">PrincipalObject</code> carries no field distinguishing the
            principal a bootstrap claim created from one created later, so which principal (and
            when) claimed the one-time code cannot be shown here.
          </>
        }
        preview={
          <div className="ruled-strip" style={{ padding: '10px 0' }}>
            <span className="ruled-strip__state t-meta">Bootstrap</span>
            <div>
              <p className="ruled-strip__explanation" style={{ margin: 0 }}>
                Claimed 12 Aug 09:38 by erbartos.
              </p>
            </div>
          </div>
        }
      />
    </section>
  )
}
