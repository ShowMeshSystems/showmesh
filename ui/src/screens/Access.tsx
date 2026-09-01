import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  createPrincipal,
  disablePrincipal,
  enablePrincipal,
  getCurrentNightSession,
  issuePrincipalToken,
  listPrincipals,
  listPrincipalTokens,
  resetPrincipalPassword,
  revokePrincipalToken,
  setPrincipalRole,
  type CreatePrincipalRequest,
  type IssueTokenRequest,
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
  Drawer,
  Field,
  Input,
  RuledStrip,
  Section,
  Segmented,
  SelectableRow,
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
  currentCredentialIsUnreported,
  isLongUnused,
  isSignedInPrincipal,
  latestTokenUse,
  principalStateLabel,
  UNUSED_CREDENTIAL_WARNING_DAYS,
  type TokenRead,
} from './accessModel'

const CREATABLE_ROLES: readonly PrincipalObject['role'][] = ['viewer', 'operator', 'admin', 'scheduler', 'recovery']

/** One plain sentence per role for the create-principal picker; only recovery is non-obvious from its name. */
const ROLE_HELP: Readonly<Record<PrincipalObject['role'], string>> = {
  viewer: 'Can read everything and change nothing.',
  operator: 'Runs the show night-to-night: macros, night commands, and the usual live controls.',
  admin: 'Everything, including managing other principals.',
  scheduler: 'Runs macros on a schedule, nothing else.',
  recovery: 'Holds exactly one scope: Resolume action requests. That is the narrow bundle the built-in automatic-recovery principal uses, not a general operator or machine login.',
}

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
    // A reload never blanks the region it is refreshing (design guide §6):
    // once a load has succeeded, later reloads keep that data on screen
    // until the fresh read lands, rather than collapsing through 'loading'
    // and unmounting whatever the operator is looking at (the credentials
    // panel, including a just-issued token value it is showing).
    setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'loading' }))
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
  // Open by default for the signed-in principal's own row, same as the
  // pane this replaces used to show without a click.
  const [inspectorOpen, setInspectorOpen] = useState(true)

  const principals = data.kind === 'loaded' ? data.principals : []
  const effectiveSelectedId = selectedId ?? model.session?.principal?.id ?? null
  const selectedPrincipal = principals.find((principal) => principal.id === effectiveSelectedId)
  const selectedTokens = selectedPrincipal === undefined ? null : (data.kind === 'loaded' ? data.tokens[selectedPrincipal.id] : undefined) ?? null

  const [issuedValue, setIssuedValue] = useState<IssueTokenResponse | null>(null)
  useEffect(() => {
    setIssuedValue(null)
  }, [effectiveSelectedId])

  const closeInspector = () => {
    setInspectorOpen(false)
    setCreating(false)
  }
  const selectPrincipal = (principalId: string) => {
    setCreating(false)
    setSelectedId(principalId)
    setInspectorOpen(true)
  }
  const openCreate = () => {
    setCreating(true)
    setSelectedId(null)
    setInspectorOpen(true)
  }

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
        detail="Scopes are granted as bundles defined on the coordinator; there is no per-principal scope bundle to read, so the inspector shows your own scopes when you select your row. Every other principal shows its role instead."
        aside={
          <Button
            variant="primary"
            onClick={openCreate}
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
              <Table minWidth={720}>
                <thead>
                  <tr>
                    <th scope="col">Principal</th>
                    <th scope="col">Kind</th>
                    <th scope="col">Role</th>
                    <th scope="col">State</th>
                    <th scope="col">Last token use</th>
                    <th scope="col">Password</th>
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
                      selected={principal.id === effectiveSelectedId && !creating && inspectorOpen}
                      onSelect={() => selectPrincipal(principal.id)}
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

      <Drawer open={inspectorOpen} onClose={closeInspector} labelledBy="ac-cred" width="wide">
        {creating ? (
          <CreatePrincipalPanel
            writeGate={writeGate}
            onCreated={(principal) => {
              setCreating(false)
              setSelectedId(principal.id)
              reload()
            }}
            onDiscard={closeInspector}
          />
        ) : (
          <Section id="ac-cred" title={selectedPrincipal === undefined ? 'Credentials' : `Credentials for ${selectedPrincipal.name}`}>
            {selectedPrincipal === undefined ? (
              <RuledStrip absence="empty" label="Nothing selected" fact="Select a principal for its credentials." />
            ) : (
              <>
                <div className="sm-inspector__row">
                  <span className="sm-inspector__label sm-data">Created</span>
                  <p className="sm-inspector__value sm-data">{formatDateClock(selectedPrincipal.createdAt) ?? 'unrecorded'}</p>
                </div>
                <ScopesRow principal={selectedPrincipal} session={model.session} />
                <AdministrationPanel
                  principal={selectedPrincipal}
                  session={model.session}
                  writeGate={writeGate}
                  onChanged={reload}
                />
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
              </>
            )}
          </Section>
        )}
      </Drawer>

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
            fact="At least one step was dispatched with no authorizing principal recorded. How many, and which, are not reported."
            detail={
              <>
                <strong>This never clears for the rest of the session.</strong> It is a permanent statement that
                tonight's record has a hole in it.
              </>
            }
          />
        )}
      </Section>

      <Section id="ac-bootstrap" title="Bootstrap">
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
    <SelectableRow selected={selected} onActivate={onSelect} ariaLabel={`View credentials for ${principal.name}`}>
      <td>
        <strong>{principal.name}</strong>{' '}
        {isYou && <span className="sm-chip">You</span>}
        {selected && <span className="sm-viewing">Editing</span>}
        <br />
        <span className="sm-small sm-faint">
          {count === null ? 'Credentials unread' : `${count} ${count === 1 ? 'credential' : 'credentials'}`}
        </span>
      </td>
      <td>
        {principal.kind === 'machine' ? 'Machine' : 'Human'}
        {principal.reserved && (
          <>
            <br />
            <span className="sm-small sm-faint">reserved</span>
          </>
        )}
      </td>
      <td className="sm-data">{principal.role}</td>
      <td>
        <StatusPair tone={state.tone} label={state.label} />
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
      <td className="sm-data">{principal.hasPassword ? 'Set' : 'Token only'}</td>
    </SelectableRow>
  )
}

/**
 * The scopes rows carried over from the old per-principal panel: only the
 * signed-in principal's own scope bundle is ever known, because no endpoint
 * reports another principal's resolved scopes.
 */
function ScopesRow({ principal, session }: { principal: PrincipalObject; session: Model['session'] }) {
  const isYou = isSignedInPrincipal(session, principal)
  return (
    <div className="sm-inspector__row">
      <span className="sm-inspector__label sm-data">Scopes</span>
      {isYou && session !== null ? (
        session.scopes.length === 0 ? (
          <p className="sm-inspector__value sm-small sm-faint">No scopes held.</p>
        ) : (
          <p className="sm-inspector__value sm-chip-row">
            {session.scopes.map((scope) => (
              <span key={scope} className="sm-chip">
                {scope}
              </span>
            ))}
          </p>
        )
      ) : (
        <p className="sm-inspector__value sm-small sm-muted">
          Not reported for another principal. Role <span className="sm-data">{principal.role}</span> is shown instead.
        </p>
      )}
    </div>
  )
}

/**
 * D-019 option B, ruled by Eric on 2026-09-01. The four write endpoints
 * `identity/types.go` gates on `principal:write`: role change, disable,
 * enable, and password reset. Every one re-renders from the response
 * body it receives (never optimistic local state) and hands the parent
 * a reload so the table's row and this group agree.
 */
function AdministrationPanel({
  principal,
  session,
  writeGate,
  onChanged,
}: {
  principal: PrincipalObject
  session: Model['session']
  writeGate: ScopeGate
  onChanged: () => void
}) {
  const isSelf = isSignedInPrincipal(session, principal)
  const reservedReason = principal.reserved
    ? 'The Resolume recovery principal cannot be re-roled, disabled or re-credentialed here.'
    : null
  const gateReason = !writeGate.allowed ? writeGate.reason : reservedReason
  const controlsDisabled = !writeGate.allowed || principal.reserved

  const [role, setRole] = useState<PrincipalObject['role']>(principal.role)
  const [roleConfirming, setRoleConfirming] = useState(false)
  const [roleConfirmText, setRoleConfirmText] = useState('')
  const [roleSaving, setRoleSaving] = useState(false)
  const [roleError, setRoleError] = useState<string | null>(null)
  const [roleOutcome, setRoleOutcome] = useState<string | null>(null)

  const [disableConfirming, setDisableConfirming] = useState(false)
  const [disableConfirmText, setDisableConfirmText] = useState('')
  const [disabling, setDisabling] = useState(false)
  const [disableError, setDisableError] = useState<string | null>(null)

  const [enabling, setEnabling] = useState(false)
  const [enableError, setEnableError] = useState<string | null>(null)

  const [newPassword, setNewPassword] = useState('')
  const [passwordConfirming, setPasswordConfirming] = useState(false)
  const [passwordConfirmText, setPasswordConfirmText] = useState('')
  const [resetting, setResetting] = useState(false)
  const [passwordError, setPasswordError] = useState<string | null>(null)
  const [passwordOutcome, setPasswordOutcome] = useState<string | null>(null)

  useEffect(() => {
    setRole(principal.role)
    setRoleConfirming(false)
    setRoleConfirmText('')
    setRoleError(null)
    setRoleOutcome(null)
    setDisableConfirming(false)
    setDisableConfirmText('')
    setDisableError(null)
    setEnableError(null)
    setNewPassword('')
    setPasswordConfirming(false)
    setPasswordConfirmText('')
    setPasswordError(null)
    setPasswordOutcome(null)
    // Deliberately keyed on id alone: a reload after a successful write
    // brings a new principal.role for the same id, and the draft above
    // must not be clobbered by it while the outcome line is still on screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [principal.id])

  const applyRole = () => {
    if (roleConfirmText !== principal.name) return
    setRoleSaving(true)
    setRoleError(null)
    setPasswordOutcome(null)
    setPrincipalRole(principal.id, { role })
      .then((response) => {
        setRoleOutcome(`Role is now \`${response.principal.role}\`, as the coordinator reports it.`)
        setRoleConfirming(false)
        setRoleConfirmText('')
        onChanged()
      })
      .catch((err: unknown) => setRoleError(describeApiError(err)))
      .finally(() => setRoleSaving(false))
  }

  const confirmDisable = () => {
    if (disableConfirmText !== principal.name) return
    setDisabling(true)
    setDisableError(null)
    disablePrincipal(principal.id)
      .then(() => {
        setDisableConfirming(false)
        setDisableConfirmText('')
        onChanged()
      })
      .catch((err: unknown) => setDisableError(describeApiError(err)))
      .finally(() => setDisabling(false))
  }

  const enable = () => {
    setEnabling(true)
    setEnableError(null)
    enablePrincipal(principal.id)
      .then(() => onChanged())
      .catch((err: unknown) => setEnableError(describeApiError(err)))
      .finally(() => setEnabling(false))
  }

  const confirmResetPassword = () => {
    if (newPassword === '' || passwordConfirmText !== principal.name) return
    setResetting(true)
    setPasswordError(null)
    setRoleOutcome(null)
    resetPrincipalPassword(principal.id, { password: newPassword })
      .then(() => {
        setPasswordOutcome('Every session and token this principal held is now invalid.')
        setNewPassword('')
        setPasswordConfirming(false)
        setPasswordConfirmText('')
        onChanged()
      })
      .catch((err: unknown) => setPasswordError(describeApiError(err)))
      .finally(() => setResetting(false))
  }

  return (
    <div className="sm-inspector__group">
      <h3 className="sm-subsection__title">Administration</h3>
      <Segmented<PrincipalObject['role']>
        label="Role"
        value={role}
        options={CREATABLE_ROLES.map((option) => ({ value: option, label: option }))}
        onChange={(value) => {
          setRole(value)
          setRoleOutcome(null)
        }}
        disabled={controlsDisabled}
      />
      <ButtonRow>
        <Button
          onClick={() => {
            setRoleConfirming(true)
            setRoleConfirmText('')
            setRoleError(null)
          }}
          disabled={controlsDisabled || role === principal.role}
          title={gateReason ?? (role === principal.role ? 'Pick a different role first.' : undefined)}
        >
          Apply
        </Button>
      </ButtonRow>
      {roleConfirming && (
        <div className="sm-panel">
          <Field label={`Type ${principal.name} to confirm`} help="Asks for the principal's name before it proceeds.">
            {(props) => <Input {...props} value={roleConfirmText} onChange={(e) => setRoleConfirmText(e.target.value)} />}
          </Field>
          {roleError !== null && <RuledStrip absence="failed" label="Refused" fact={roleError} />}
          <ButtonRow>
            <Button
              variant="primary"
              disabled={controlsDisabled || roleConfirmText !== principal.name || roleSaving}
              title={gateReason ?? (roleConfirmText !== principal.name ? "Type the principal's name exactly to enable this." : undefined)}
              onClick={applyRole}
            >
              {roleSaving ? 'Changing role…' : 'Change role'}
            </Button>
            <Button variant="quiet" onClick={() => setRoleConfirming(false)} disabled={roleSaving}>
              Cancel
            </Button>
          </ButtonRow>
        </div>
      )}
      {roleOutcome !== null && (
        <p className="sm-small sm-data" role="status">
          {roleOutcome}
        </p>
      )}

      <ButtonRow>
        {principal.disabled ? (
          <Button
            onClick={enable}
            disabled={controlsDisabled || enabling}
            title={gateReason ?? undefined}
          >
            {enabling ? 'Enabling…' : 'Enable'}
          </Button>
        ) : (
          <Button
            variant="danger"
            onClick={() => {
              setDisableConfirming(true)
              setDisableConfirmText('')
              setDisableError(null)
            }}
            disabled={controlsDisabled || isSelf}
            title={isSelf ? 'You cannot disable your own signed-in principal.' : (gateReason ?? undefined)}
          >
            Disable
          </Button>
        )}
      </ButtonRow>
      {enableError !== null && <RuledStrip absence="failed" label="Refused" fact={enableError} />}
      {disableConfirming && !principal.disabled && (
        <div className="sm-panel">
          <Field label={`Type ${principal.name} to confirm`} help="Asks for the principal's name before it proceeds.">
            {(props) => <Input {...props} value={disableConfirmText} onChange={(e) => setDisableConfirmText(e.target.value)} />}
          </Field>
          {disableError !== null && <RuledStrip absence="failed" label="Refused" fact={disableError} />}
          <ButtonRow>
            <Button
              variant="danger"
              disabled={controlsDisabled || isSelf || disableConfirmText !== principal.name || disabling}
              title={
                isSelf
                  ? 'You cannot disable your own signed-in principal.'
                  : (gateReason ?? (disableConfirmText !== principal.name ? "Type the principal's name exactly to enable this." : undefined))
              }
              onClick={confirmDisable}
            >
              {disabling ? 'Disabling…' : 'Disable principal'}
            </Button>
            <Button variant="quiet" onClick={() => setDisableConfirming(false)} disabled={disabling}>
              Cancel
            </Button>
          </ButtonRow>
        </div>
      )}

      <Field label="New password">
        {(props) => <Input {...props} type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} />}
      </Field>
      <ButtonRow>
        <Button
          onClick={() => {
            setPasswordConfirming(true)
            setPasswordConfirmText('')
            setPasswordError(null)
          }}
          disabled={controlsDisabled || newPassword === ''}
          title={gateReason ?? (newPassword === '' ? 'Enter a new password first.' : undefined)}
        >
          Set password
        </Button>
      </ButtonRow>
      {passwordConfirming && (
        <div className="sm-panel">
          <Field label={`Type ${principal.name} to confirm`} help="Asks for the principal's name before it proceeds.">
            {(props) => <Input {...props} value={passwordConfirmText} onChange={(e) => setPasswordConfirmText(e.target.value)} />}
          </Field>
          {passwordError !== null && <RuledStrip absence="failed" label="Refused" fact={passwordError} />}
          <ButtonRow>
            <Button
              variant="primary"
              disabled={controlsDisabled || newPassword === '' || passwordConfirmText !== principal.name || resetting}
              title={gateReason ?? (passwordConfirmText !== principal.name ? "Type the principal's name exactly to enable this." : undefined)}
              onClick={confirmResetPassword}
            >
              {resetting ? 'Resetting…' : 'Reset password'}
            </Button>
            <Button variant="quiet" onClick={() => setPasswordConfirming(false)} disabled={resetting}>
              Cancel
            </Button>
          </ButtonRow>
        </div>
      )}
      <p className="sm-small sm-faint">Resetting signs this principal out of every session and invalidates every token it holds.</p>
      {passwordOutcome !== null && (
        <p className="sm-small sm-data" role="status">
          {passwordOutcome}
        </p>
      )}
    </div>
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
          <p className="sm-small sm-faint">{ROLE_HELP[role]}</p>
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
  const [expiresLocal, setExpiresLocal] = useState('')
  const [issuing, setIssuing] = useState(false)
  const [issueError, setIssueError] = useState<string | null>(null)

  const [revokeTarget, setRevokeTarget] = useState<TokenObject | null>(null)
  const [revokeConfirmText, setRevokeConfirmText] = useState('')
  const [revoking, setRevoking] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  const [copyStatus, setCopyStatus] = useState<string | null>(null)

  const copyIssuedValue = () => {
    if (issuedValue === null) return
    if (navigator.clipboard === undefined) {
      setCopyStatus('Copy failed: the browser refused clipboard access on this connection.')
      return
    }
    navigator.clipboard.writeText(issuedValue.value).then(
      () => setCopyStatus('Copied.'),
      () => setCopyStatus('Copy failed. Select the value and copy it by hand.'),
    )
  }

  const issue = () => {
    setIssuing(true)
    setIssueError(null)
    const payload: IssueTokenRequest = label === '' ? {} : { label }
    if (expiresLocal !== '') payload.expiresAt = new Date(expiresLocal).toISOString()
    issuePrincipalToken(principal.id, payload)
      .then((response) => {
        setCopyStatus(null)
        onIssued(response)
        setLabel('')
        setExpiresLocal('')
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
      {currentCredentialIsUnreported(session) && (
        <p className="sm-small sm-faint">
          This device is authenticated by a token. Which of these it is, is not reported, so no row is marked as the one in use. Revoking the wrong
          one signs this device out.
        </p>
      )}

      {readDenied && <RuledStrip absence="noPermission" label="Credentials not shown" fact="This device may not read credentials." />}
      {readFailed !== null && <RuledStrip absence="failed" label="Read failed" fact={readFailed} />}

      {issuedValue !== null && (
        <div className="sm-panel" role="status">
          <p className="sm-small sm-muted">New credential value. Copy it now; it will not be shown again.</p>
          <p className="sm-data">{issuedValue.value}</p>
          <ButtonRow>
            <Button onClick={copyIssuedValue}>Copy</Button>
            <Button
              variant="quiet"
              onClick={() => {
                setCopyStatus(null)
                onIssued(null)
              }}
            >
              Dismiss
            </Button>
          </ButtonRow>
          {copyStatus !== null && (
            <p className="sm-small sm-muted" role="status">
              {copyStatus}
            </p>
          )}
        </div>
      )}

      {tokens !== null && (
        <TableWrap label="Credentials for this principal, scrollable">
          <Table minWidth={560}>
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
        <Field label="Expires · optional" help="Leave it blank and this token never expires.">
          {(props) => <Input {...props} type="datetime-local" value={expiresLocal} onChange={(e) => setExpiresLocal(e.target.value)} />}
        </Field>
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
