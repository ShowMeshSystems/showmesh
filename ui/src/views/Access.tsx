import { useEffect, useRef, useState } from 'react'
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
import { EmptyBlock, FailedBlock, LoadingBlock, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'

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
  const signInState = describeSignInState(model.session)
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
    <div>
      <h2 className="panel__title">Access</h2>
      <p className="text-muted">
        Principals, their role and enabled state, their passwords, and their API tokens. Reads
        require <code>principal:read</code>; every write requires <code>principal:write</code>{' '}
        and is audited. Disabling the coordinator&rsquo;s last enabled administrator, changing
        its role away from one that holds <code>principal:write</code>, or revoking the last
        credential able to reach that scope, is refused rather than performed.
      </p>

      {permissionState}

      {readGate.allowed && (
        <>
          {state.kind === 'loading' && <LoadingBlock title="Loading principals" reason="Loading coordinator access records…" />}
          {state.kind === 'error' && (
            <FailedBlock title="Principals could not be loaded" reason={<>{state.message} <button type="button" onClick={reload}>Retry</button></>} />
          )}
          {state.kind === 'loaded' && (
            <>
              <CreatePrincipalForm onCreated={reload} />
              <h3 className="panel__title">Principals</h3>
              {state.principals.length === 0 ? (
                <EmptyBlock title="No principals" reason="The coordinator returned no principals." />
              ) : (
                <div className="table-scroll">
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Kind</th>
                        <th>Role</th>
                        <th>Disabled</th>
                        <th>Has password</th>
                        <th>Created</th>
                        <th aria-label="Actions" />
                      </tr>
                    </thead>
                    <tbody>
                      {state.principals.map((p) => (
                        <PrincipalRow key={p.id} principal={p} onChanged={reload} />
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}
        </>
      )}
    </div>
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
    <div data-unsaved-form="access-create-principal" style={{ marginBottom: '1.5rem' }}>
      <h3 className="panel__title">Create a principal</h3>
      {error !== null && (
        <p role="alert" className="session-form__error">
          {error}
        </p>
      )}
      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'end' }}>
        <label>
          Name
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          Kind
          <select value={kind} onChange={(e) => setKind(e.target.value as 'human' | 'machine')}>
            <option value="human">human</option>
            <option value="machine">machine</option>
          </select>
        </label>
        <label>
          Role
          <select value={role} onChange={(e) => setRole(e.target.value as (typeof ROLES)[number])}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <label>
          Password (optional for a machine principal)
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <ScopedButton
          requiredScope={PRINCIPAL_WRITE_SCOPE}
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

function PrincipalRow({ principal, onChanged }: { principal: PrincipalObject; onChanged: () => void }) {
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
      <tr>
        <td>
          {principal.name}
          {principal.reserved && <span className="text-muted"> (reserved)</span>}
        </td>
        <td>{principal.kind}</td>
        <td>
          {locked ? (
            principal.role
          ) : (
            <span data-unsaved-form={`access-principal-${principal.id}-role`}>
              <select value={roleDraft} onChange={(e) => setRoleDraft(e.target.value as (typeof ROLES)[number])}>
                {ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>{' '}
              <ScopedButton
                requiredScope={PRINCIPAL_WRITE_SCOPE}
                onClick={() => void handleSetRole()}
                busy={busyAction === 'role'}
                busyReason="Changing this principal's role…"
              >
                Set role
              </ScopedButton>
            </span>
          )}
        </td>
        <td>{principal.disabled ? 'yes' : 'no'}</td>
        <td>{principal.hasPassword ? 'yes' : 'no'}</td>
        <td>{formatAbsolute(principal.createdAt)}</td>
        <td>
          {!locked && (
            <>
              <ScopedButton
                requiredScope={PRINCIPAL_WRITE_SCOPE}
                onClick={() => void handleToggleDisabled()}
                busy={busyAction === 'enable' || busyAction === 'disable'}
                busyReason={principal.disabled ? 'Enabling…' : 'Disabling…'}
              >
                {principal.disabled ? 'Enable' : 'Disable'}
              </ScopedButton>{' '}
              <button type="button" onClick={() => setPasswordOpen((v) => !v)}>
                {passwordOpen ? 'Cancel reset' : 'Reset password'}
              </button>{' '}
            </>
          )}
          <button type="button" onClick={() => setTokensOpen((v) => !v)}>
            {tokensOpen ? 'Hide tokens' : 'Tokens'}
          </button>
        </td>
      </tr>
      {error !== null && (
        <tr>
          <td colSpan={7}>
            <p role="alert" className="session-form__error">
              {error}
            </p>
          </td>
        </tr>
      )}
      {passwordOpen && !locked && (
        <tr>
          <td colSpan={7} data-unsaved-form={`access-principal-${principal.id}-password`}>
            <label>
              New password
              <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </label>{' '}
            <ScopedButton
              requiredScope={PRINCIPAL_WRITE_SCOPE}
              onClick={() => void handleResetPassword()}
              busy={busyAction === 'password'}
              busyReason="Resetting this principal's password…"
            >
              Reset password
            </ScopedButton>
            <p className="text-muted">Every existing session and token for this principal will be invalidated.</p>
          </td>
        </tr>
      )}
      {tokensOpen && (
        <tr>
          <td colSpan={7}>
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
    <div>
      {error !== null && (
        <p role="alert" className="session-form__error">
          {error}
        </p>
      )}
      {issuedValue !== null && (
        <p className="panel" role="status">
          This token is displayed exactly once and cannot be retrieved again; store it now:{' '}
          <code>{issuedValue}</code>
        </p>
      )}
      {loadError !== null ? (
        <FailedBlock title="Tokens could not be loaded" reason={<>{loadError} <button type="button" onClick={retryLoad}>Retry</button></>} headingLevel={3} />
      ) : tokens === null ? (
        <LoadingBlock title="Loading tokens" reason="Loading coordinator access records…" headingLevel={3} />
      ) : tokens.length === 0 ? (
        <EmptyBlock title="No tokens" reason="No tokens are currently issued for this principal." headingLevel={3} />
      ) : (
        <div className="table-scroll">
          <table className="config-table">
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
                  <td>{t.hint}</td>
                  <td>{t.label || '-'}</td>
                  <td>{formatAbsolute(t.createdAt)}</td>
                  <td>{t.expiresAt === null ? 'never' : formatAbsolute(t.expiresAt)}</td>
                  <td>{t.lastUsedAt === null ? 'never' : formatAbsolute(t.lastUsedAt)}</td>
                  <td>
                    {!locked && (
                      <ScopedButton
                        requiredScope={PRINCIPAL_WRITE_SCOPE}
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
        <div data-unsaved-form={`access-principal-${principalID}-token-issue`} style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', alignItems: 'end', marginTop: '0.5rem' }}>
          <label>
            Label
            <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} />
          </label>
          <label>
            Expires (RFC3339, optional, default never)
            <input type="text" placeholder="2027-01-15T00:00:00Z" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </label>
          <ScopedButton
            requiredScope={PRINCIPAL_WRITE_SCOPE}
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
