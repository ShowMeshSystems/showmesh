import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  createPrincipal,
  getCurrentNightSession,
  issuePrincipalToken,
  listPrincipals,
  listPrincipalTokens,
  revokePrincipalToken,
  type CreatePrincipalRequest,
  type IssueTokenResponse,
  type Model,
  type NightSessionState,
  type PrincipalObject,
  type TokenObject,
} from '../api'
import {
  Button,
  ButtonRow,
  DefinitionStrip,
  Field,
  Input,
  RuledStrip,
  Section,
  Segmented,
  StatusPair,
  Table,
  TableWrap,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso, formatDateClock } from '../domain/time'
import {
  credentialCount,
  daysUnused,
  isCurrentCredential,
  isLongUnused,
  isSignedInPrincipal,
  latestTokenUse,
  principalStateLabel,
  UNUSED_CREDENTIAL_WARNING_DAYS,
  type TokenRead,
} from './accessModel'

const CREATABLE_ROLES: readonly PrincipalObject['role'][] = ['viewer', 'operator', 'admin', 'scheduler']

type AccessData =
  | { kind: 'denied'; reason: string }
  | { kind: 'loading' }
  | { kind: 'failed'; reason: string }
  | { kind: 'loaded'; principals: PrincipalObject[]; tokens: Record<string, TokenRead> }

/**
 * Every principal's tokens are read alongside the principal list itself:
 * PrincipalObject reports no credential count or last-used field of its
 * own, so both are derived from each principal's own `GET .../tokens`.
 */
function useAccessData(model: Model, reloadKey: number): AccessData {
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'principal:read')
  const gateReason = gate.allowed ? null : gate.reason
  const [state, setState] = useState<AccessData>(gate.allowed ? { kind: 'loading' } : { kind: 'denied', reason: gateReason ?? '' })

  useEffect(() => {
    if (!gate.allowed) {
      setState({ kind: 'denied', reason: gateReason ?? '' })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listPrincipals()
      .then((response) =>
        Promise.all(
          response.principals.map((principal) =>
            listPrincipalTokens(principal.id)
              .then((tokensResponse): [string, TokenRead] => [principal.id, tokensResponse.tokens])
              .catch((): [string, TokenRead] => [principal.id, null]),
          ),
        ).then((entries) => {
          if (cancelled) return
          setState({ kind: 'loaded', principals: response.principals, tokens: Object.fromEntries(entries) })
        }),
      )
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [gate.allowed, gateReason, reloadKey])

  return state
}

/**
 * The change stream never announces a night-session change on this screen's
 * own account, so it is seeded once the way Dashboard and SettingsMode seed
 * it; a later `nightSession.changed` frame always wins over this fetch.
 */
function useNightSession(): NightSessionState | null {
  const model = useModelContext()
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch(() => {
        // A failed seed leaves the attribution row unrendered, which is
        // honest: nothing here claims tonight's record has no hole in it.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return model.nightSession ?? seeded
}

export function Access() {
  const model = useModelContext()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const nightSession = useNightSession()

  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, 'principal:write')
  const auditGate = evaluateScope(model.session, model.sessionFetchFailed, 'audit:read')

  const [reloadKey, setReloadKey] = useState(0)
  const data = useAccessData(model, reloadKey)
  const reload = () => setReloadKey((key) => key + 1)

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  const principals = data.kind === 'loaded' ? data.principals : []
  const effectiveSelectedId = selectedId ?? model.session?.principal?.id ?? null
  const selectedPrincipal = principals.find((principal) => principal.id === effectiveSelectedId)
  const selectedTokens = selectedPrincipal === undefined ? null : (data.kind === 'loaded' ? data.tokens[selectedPrincipal.id] : undefined) ?? null

  const [issuedValue, setIssuedValue] = useState<IssueTokenResponse | null>(null)
  useEffect(() => {
    setIssuedValue(null)
  }, [effectiveSelectedId])

  return (
    <>
      <p className="sm-small sm-muted">
        <Link to="/settings/connections" className="sm-muted">
          Settings
        </Link>{' '}
        <span className="sm-faint">/</span> Access
      </p>

      <h1 className="sm-page__title">Access</h1>
      <p className="sm-page__lede">
        Who can change this installation, and what each of them may change. Every write is attributed to a
        principal; nothing here affects who can <em>see</em> the show.
      </p>

      <DefinitionStrip
        items={[
          {
            term: 'Reads are open',
            value: (
              <>
                Anyone who can reach this coordinator can view the show without signing in. That is deliberate:
                a credential problem must never cost an operator sight of a running show. Close it with{' '}
                <span className="sm-data">SHOWMESH_API_CLOSE_READS</span> if the port is exposed beyond the show
                network.
              </>
            ),
          },
        ]}
      />

      <Section
        id="ac-princ"
        title="Principals"
        detail="Scopes are granted as bundles defined on the coordinator; there is no per-principal scope bundle to read, so only your own row below shows the scopes you actually hold. Every other row shows its role instead."
        aside={
          <Button
            variant="primary"
            onClick={() => {
              setCreating(true)
              setSelectedId(null)
            }}
            disabled={!writeGate.allowed}
            title={writeGate.allowed ? undefined : writeGate.reason}
          >
            Add principal
          </Button>
        }
      >
        {data.kind === 'denied' && <RuledStrip absence="noPermission" label="Principals not shown" fact={data.reason} />}
        {data.kind === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={data.reason} />}
        {data.kind === 'loading' && <RuledStrip absence="loading" label="Reading" fact="Waiting on the coordinator's principal list." />}
        {data.kind === 'loaded' && (
          <>
            <TableWrap label="Principals, scrollable">
              <Table>
                <thead>
                  <tr>
                    <th scope="col">Principal</th>
                    <th scope="col">Scopes held</th>
                    <th scope="col">Last token use</th>
                    <th scope="col">State</th>
                  </tr>
                </thead>
                <tbody>
                  {data.principals.map((principal) => (
                    <PrincipalRow
                      key={principal.id}
                      principal={principal}
                      tokens={data.tokens[principal.id]}
                      session={model.session}
                      nowIso={nowIso}
                      selected={principal.id === effectiveSelectedId && !creating}
                      onSelect={() => {
                        setCreating(false)
                        setSelectedId(principal.id)
                      }}
                    />
                  ))}
                </tbody>
              </Table>
            </TableWrap>
            <p className="sm-section__footnote">
              A principal with no write scopes can still read everything. That is what "reads are open" means,
              not a permission anyone granted. "Consider revoking" flags a credential unused for{' '}
              {UNUSED_CREDENTIAL_WARNING_DAYS} days or more.
            </p>
          </>
        )}
      </Section>

      {creating ? (
        <CreatePrincipalPanel
          writeGate={writeGate}
          onCreated={(principal) => {
            setCreating(false)
            setSelectedId(principal.id)
            reload()
          }}
          onDiscard={() => setCreating(false)}
        />
      ) : (
        <Section id="ac-cred" title={selectedPrincipal === undefined ? 'Credentials' : `Credentials for ${selectedPrincipal.name}`}>
          {selectedPrincipal === undefined ? (
            <RuledStrip absence="empty" label="Nothing selected" fact="Select a principal above for its credentials." />
          ) : (
            <CredentialsPanel
              principal={selectedPrincipal}
              tokens={selectedTokens}
              session={model.session}
              writeGate={writeGate}
              readDenied={data.kind === 'denied'}
              readFailed={data.kind === 'failed' ? data.reason : null}
              issuedValue={issuedValue}
              onIssued={setIssuedValue}
              onChanged={reload}
            />
          )}
        </Section>
      )}

      <Section id="ac-audit" title="Attribution">
        <RuledStrip
          absence="unavailable"
          label="Audit store"
          fact="Whether the audit store can currently be written is not reported by the coordinator."
          detail={
            <>
              Four of the night commands refuse outright rather than run unattributed, so an unwritable audit
              store stops them rather than silently losing who did what.{' '}
              {auditGate.allowed ? (
                <Link to="/monitor/activity">Open the log</Link>
              ) : (
                <>Opening the log needs <span className="sm-data">audit:read</span>, which this device does not currently hold.</>
              )}
            </>
          }
        />
        {nightSession !== null && nightSession.attributionDegraded && (
          <RuledStrip
            absence="failed"
            label="This session"
            fact="One autonomous transition step was dispatched with no authorizing principal recorded."
            detail={
              <>
                <strong>This never clears for the rest of the session.</strong> It is a permanent statement that
                tonight's record has a hole in it.
              </>
            }
          />
        )}
        <BootstrapRow session={model.session} />
      </Section>
    </>
  )
}

function PrincipalRow({
  principal,
  tokens,
  session,
  nowIso,
  selected,
  onSelect,
}: {
  principal: PrincipalObject
  tokens: TokenRead | undefined
  session: Model['session']
  nowIso: string | null
  selected: boolean
  onSelect: () => void
}) {
  const readTokens: TokenRead = tokens ?? null
  const lastUsed = latestTokenUse(readTokens)
  const count = credentialCount(readTokens)
  const unused = !principal.disabled && isLongUnused(lastUsed, nowIso)
  const state = principalStateLabel(principal, unused)
  const isYou = isSignedInPrincipal(session, principal)
  const days = daysUnused(lastUsed, nowIso)

  return (
    <tr aria-current={selected ? 'true' : undefined} className={selected ? 'sm-table__row--current' : undefined}>
      <td>
        <button type="button" className="sm-linkbutton" onClick={onSelect} aria-pressed={selected}>
          {principal.name}
        </button>{' '}
        {isYou && <span className="sm-chip">You</span>}
        <br />
        <span className="sm-small sm-faint">
          {count === null ? 'Credentials unread' : `${count} ${count === 1 ? 'credential' : 'credentials'}`}
          {principal.kind === 'machine' && ' · machine principal'}
          {principal.reserved && ' · reserved'}
        </span>
      </td>
      <td>
        {isYou && session !== null ? (
          session.scopes.length === 0 ? (
            <span className="sm-small sm-faint">No scopes held.</span>
          ) : (
            <span className="sm-chip-row">
              {session.scopes.map((scope) => (
                <span key={scope} className="sm-chip">
                  {scope}
                </span>
              ))}
            </span>
          )
        ) : (
          <span className="sm-small sm-muted">
            Role <span className="sm-data">{principal.role}</span>
          </span>
        )}
      </td>
      <td className="sm-data">
        {lastUsed === null ? 'Never used' : formatDateClock(lastUsed)}
        {unused && days !== null && (
          <>
            <br />
            <span className="sm-small sm-faint">Unused {days} {days === 1 ? 'day' : 'days'}</span>
          </>
        )}
      </td>
      <td>
        <StatusPair tone={state.tone} label={state.label} />
      </td>
    </tr>
  )
}

function BootstrapRow({ session }: { session: Model['session'] }) {
  if (session === null) {
    return (
      <RuledStrip
        absence="loading"
        label="Bootstrap"
        fact="Waiting to hear from the coordinator whether bootstrap has been claimed."
      />
    )
  }
  return (
    <RuledStrip
      absence="unavailable"
      label="Bootstrap"
      fact={
        session.bootstrapRequired
          ? 'Bootstrap is still required. No principal has claimed it yet.'
          : 'Bootstrap has been claimed. Who claimed it and when are not reported.'
      }
      detail={
        session.bootstrapRequired
          ? undefined
          : "The one-time code from the coordinator's data volume is spent and cannot be reused."
      }
    />
  )
}

type ScopeGate = ReturnType<typeof evaluateScope>

function CreatePrincipalPanel({
  writeGate,
  onCreated,
  onDiscard,
}: {
  writeGate: ScopeGate
  onCreated: (principal: PrincipalObject) => void
  onDiscard: () => void
}) {
  const [name, setName] = useState('')
  const [kind, setKind] = useState<CreatePrincipalRequest['kind']>('human')
  const [role, setRole] = useState<CreatePrincipalRequest['role']>('operator')
  const [password, setPassword] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const create = () => {
    if (name === '') return
    setCreating(true)
    setError(null)
    const payload: CreatePrincipalRequest = { name, kind, role }
    if (kind === 'human' && password !== '') payload.password = password
    createPrincipal(payload)
      .then((response) => onCreated(response.principal))
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setCreating(false))
  }

  return (
    <Section id="ac-cred" title="New principal" eyebrow="Draft · new principal">
      <Segmented<CreatePrincipalRequest['kind']>
        label="Kind"
        value={kind}
        options={[
          { value: 'human', label: 'Human' },
          { value: 'machine', label: 'Machine' },
        ]}
        onChange={(value) => {
          setKind(value)
          if (value === 'machine') setPassword('')
        }}
      />
      <p className="sm-small sm-faint">Decides whether a password makes sense at all for this principal.</p>

      <div className="sm-grid sm-form-column">
        <Field label="Name">{(props) => <Input {...props} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <div>
          <Segmented<CreatePrincipalRequest['role']>
            label="Role"
            value={role}
            options={CREATABLE_ROLES.map((option) => ({ value: option, label: option }))}
            onChange={setRole}
          />
        </div>
        {kind === 'human' && (
          <Field label="Password · optional" help="Leave it blank for a principal that will only ever use an issued token.">
            {(props) => <Input {...props} type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}
          </Field>
        )}
      </div>

      {error !== null && <RuledStrip absence="failed" label="Save failed" fact={error} />}

      <ButtonRow>
        <Button
          variant="primary"
          onClick={create}
          disabled={name === '' || creating || !writeGate.allowed}
          title={!writeGate.allowed ? writeGate.reason : undefined}
        >
          {creating ? 'Creating…' : 'Create principal'}
        </Button>
        <Button variant="quiet" onClick={onDiscard} disabled={creating}>
          Discard
        </Button>
      </ButtonRow>
    </Section>
  )
}

function CredentialsPanel({
  principal,
  tokens,
  session,
  writeGate,
  readDenied,
  readFailed,
  issuedValue,
  onIssued,
  onChanged,
}: {
  principal: PrincipalObject
  tokens: TokenRead
  session: Model['session']
  writeGate: ScopeGate
  readDenied: boolean
  readFailed: string | null
  issuedValue: IssueTokenResponse | null
  onIssued: (value: IssueTokenResponse | null) => void
  onChanged: () => void
}) {
  const [label, setLabel] = useState('')
  const [issuing, setIssuing] = useState(false)
  const [issueError, setIssueError] = useState<string | null>(null)

  const [revokeTarget, setRevokeTarget] = useState<TokenObject | null>(null)
  const [revokeConfirmText, setRevokeConfirmText] = useState('')
  const [revoking, setRevoking] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  const issue = () => {
    setIssuing(true)
    setIssueError(null)
    issuePrincipalToken(principal.id, label === '' ? {} : { label })
      .then((response) => {
        onIssued(response)
        setLabel('')
        onChanged()
      })
      .catch((err: unknown) => setIssueError(describeApiError(err)))
      .finally(() => setIssuing(false))
  }

  const confirmRevoke = () => {
    if (revokeTarget === null) return
    setRevoking(true)
    setRevokeError(null)
    revokePrincipalToken(principal.id, revokeTarget.id)
      .then(() => {
        setRevokeTarget(null)
        setRevokeConfirmText('')
        onChanged()
      })
      .catch((err: unknown) => setRevokeError(describeApiError(err)))
      .finally(() => setRevoking(false))
  }

  const canRevoke = revokeTarget !== null && revokeConfirmText === revokeTarget.id && writeGate.allowed && !revoking

  return (
    <>
      <p className="sm-small sm-muted">A token is shown once, at the moment it is issued, and never again. If it is lost, revoke it and issue another.</p>

      {readDenied && <RuledStrip absence="noPermission" label="Credentials not shown" fact="This device may not read credentials." />}
      {readFailed !== null && <RuledStrip absence="failed" label="Read failed" fact={readFailed} />}

      {issuedValue !== null && (
        <div className="sm-panel" role="status">
          <p className="sm-small sm-muted">New credential value. Copy it now; it will not be shown again.</p>
          <p className="sm-data">{issuedValue.value}</p>
          <ButtonRow>
            <Button
              onClick={() => {
                void navigator.clipboard?.writeText(issuedValue.value)
              }}
            >
              Copy
            </Button>
            <Button variant="quiet" onClick={() => onIssued(null)}>
              Dismiss
            </Button>
          </ButtonRow>
        </div>
      )}

      {tokens !== null && (
        <TableWrap label="Credentials for this principal, scrollable">
          <Table>
            <thead>
              <tr>
                <th scope="col">Credential</th>
                <th scope="col">Issued</th>
                <th scope="col">Expires</th>
                <th scope="col">Last used</th>
                <th scope="col">Revoke</th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((token) => {
                const current = isCurrentCredential(session, token)
                return (
                  <tr key={token.id}>
                    <td>
                      <span className="sm-data">{token.hint}</span>
                      {token.label !== '' && (
                        <>
                          <br />
                          <span className="sm-small sm-faint">{token.label}</span>
                        </>
                      )}
                      <br />
                      <span className="sm-small sm-faint">
                        id <span className="sm-data">{token.id}</span>
                      </span>
                    </td>
                    <td className="sm-data">{formatDateClock(token.createdAt) ?? 'unrecorded'}</td>
                    <td className="sm-data">{token.expiresAt === null ? 'Never' : formatDateClock(token.expiresAt)}</td>
                    <td className="sm-data">{token.lastUsedAt === null ? 'Never used' : formatDateClock(token.lastUsedAt)}</td>
                    <td>
                      {current ? (
                        <Button disabled={true} title="This is the credential authenticating this device right now.">
                          In use
                        </Button>
                      ) : (
                        <Button
                          variant="danger"
                          disabled={!writeGate.allowed}
                          title={writeGate.allowed ? undefined : writeGate.reason}
                          onClick={() => {
                            setRevokeTarget(token)
                            setRevokeConfirmText('')
                            setRevokeError(null)
                          }}
                        >
                          Revoke
                        </Button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </Table>
          <p className="sm-section__footnote">Revoking takes effect on the next request, not on anything already in flight.</p>
        </TableWrap>
      )}

      <ButtonRow>
        <Field label="Label · optional">{(props) => <Input {...props} value={label} onChange={(e) => setLabel(e.target.value)} />}</Field>
        <Button onClick={issue} disabled={issuing || !writeGate.allowed} title={writeGate.allowed ? undefined : writeGate.reason}>
          {issuing ? 'Issuing…' : 'Issue credential'}
        </Button>
      </ButtonRow>
      {issueError !== null && <RuledStrip absence="failed" label="Refused" fact={issueError} />}

      {revokeTarget !== null && (
        <div className="sm-panel">
          <p className="sm-small sm-muted">
            Revoking <span className="sm-data">{revokeTarget.id}</span> takes effect on the next request, not on
            anything already in flight.
          </p>
          <Field label={`Type ${revokeTarget.id} to confirm`} help="Asks for the credential id before it proceeds.">
            {(props) => <Input {...props} value={revokeConfirmText} onChange={(e) => setRevokeConfirmText(e.target.value)} />}
          </Field>
          {revokeError !== null && <RuledStrip absence="failed" label="Refused" fact={revokeError} />}
          <ButtonRow>
            <Button
              variant="danger"
              disabled={!canRevoke}
              title={
                !writeGate.allowed
                  ? writeGate.reason
                  : revokeConfirmText !== revokeTarget.id
                    ? 'Type the credential id exactly to enable this.'
                    : undefined
              }
              onClick={confirmRevoke}
            >
              {revoking ? 'Revoking…' : 'Revoke credential'}
            </Button>
            <Button variant="quiet" onClick={() => setRevokeTarget(null)} disabled={revoking}>
              Cancel
            </Button>
          </ButtonRow>
        </div>
      )}
    </>
  )
}
