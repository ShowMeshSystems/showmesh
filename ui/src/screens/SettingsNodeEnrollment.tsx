import { useEffect, useState } from 'react'
import {
  ApiError,
  cancelNodeEnrollment,
  createNodeEnrollment,
  getServiceDescriptor,
  listNodeEnrollments,
  type CreateNodeEnrollmentResponse,
  type NodeEnrollment,
} from '../api'
import { Button, ButtonRow, ConfirmDialog, CopyButton, Field, Input, OneTimeSecret, RuledStrip, Section, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope, type ScopeGateResult } from '../domain/session'
import { ageMs, formatDateClock, formatDuration } from '../domain/time'
import { COORDINATOR_PLACEHOLDER_HOST, nodeIdHint, nodeInstallCommand, replaceLoopbackHost } from './nodeEnrollmentModel'

type EnrollmentsState =
  | { kind: 'denied'; reason: string }
  | { kind: 'loading' }
  | { kind: 'loaded'; enrollments: NodeEnrollment[] }
  | { kind: 'failed'; reason: string }

function useNodeEnrollments(gate: ScopeGateResult, reloadKey: number): EnrollmentsState {
  const [state, setState] = useState<EnrollmentsState>(gate.allowed ? { kind: 'loading' } : { kind: 'denied', reason: gate.reason })

  useEffect(() => {
    if (!gate.allowed) {
      setState({ kind: 'denied', reason: gate.reason })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listNodeEnrollments()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', enrollments: response.enrollments })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gate.allowed, reloadKey])

  return state
}

/** The install command's install detail lines, exactly as the CLI states them. */
function InstallDetail({ issued, coordinatorVersion }: { issued: CreateNodeEnrollmentResponse; coordinatorVersion: string }) {
  const rawUrl = issued.coordinatorUrl !== '' ? issued.coordinatorUrl : window.location.origin
  const { url, loopback } = replaceLoopbackHost(rawUrl)
  const command = nodeInstallCommand(coordinatorVersion, url, issued.code)
  const remainingMs = ageMs(issued.serverTime, issued.expiresAt)

  return (
    <>
      <p>
        Expires {formatDateClock(issued.expiresAt)}
        {remainingMs !== null && `, in ${formatDuration(remainingMs)}`}.
      </p>
      {issued.reenroll && <p>This replaces the node&rsquo;s current broker password and API token.</p>}
      <p>Run this on the node before it expires:</p>
      <p className="sm-data">{command}</p>
      <CopyButton value={command} label="Copy command" />
      {loopback && (
        <p>
          {COORDINATOR_PLACEHOLDER_HOST} is a placeholder because this browser reached the coordinator at an address only
          reachable from this device. Set the coordinator&rsquo;s public URL, then mint a new code.
        </p>
      )}
    </>
  )
}

export function SettingsNodeEnrollment() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'node:enroll')

  const [attempt, setAttempt] = useState(0)
  const enrollments = useNodeEnrollments(gate, attempt)

  const [coordinatorVersion, setCoordinatorVersion] = useState('')
  useEffect(() => {
    let cancelled = false
    getServiceDescriptor()
      .then((descriptor) => {
        if (!cancelled) setCoordinatorVersion(descriptor.coordinator.version)
      })
      .catch(() => {
        // Best-effort: the install command falls back to the generic form below.
      })
    return () => {
      cancelled = true
    }
  }, [])

  const [nodeId, setNodeId] = useState('')
  const [minting, setMinting] = useState(false)
  const [mintError, setMintError] = useState<string | null>(null)
  const [issued, setIssued] = useState<CreateNodeEnrollmentResponse | null>(null)
  const [pendingReenroll, setPendingReenroll] = useState<{ nodeId: string; detail: string } | null>(null)
  const [reenrollError, setReenrollError] = useState<string | null>(null)

  const hint = nodeIdHint(nodeId.trim())
  const canMint = gate.allowed && !minting && nodeId.trim() !== '' && hint === null

  const mint = (id: string, reenroll: boolean) => {
    if (minting) return
    setMinting(true)
    setMintError(null)
    setReenrollError(null)
    createNodeEnrollment({ nodeId: id, reenroll })
      .then((response) => {
        setIssued(response)
        setPendingReenroll(null)
        setNodeId('')
        setAttempt((n) => n + 1)
      })
      .catch((err: unknown) => {
        if (!reenroll && err instanceof ApiError && err.status === 409) {
          setPendingReenroll({ nodeId: id, detail: describeApiError(err) })
          return
        }
        if (reenroll) {
          setReenrollError(describeApiError(err))
          return
        }
        setMintError(describeApiError(err))
      })
      .finally(() => setMinting(false))
  }

  const [cancelTarget, setCancelTarget] = useState<NodeEnrollment | null>(null)
  const [cancelling, setCancelling] = useState(false)
  const [cancelError, setCancelError] = useState<string | null>(null)

  const confirmCancel = () => {
    if (cancelTarget === null || cancelling) return
    setCancelling(true)
    setCancelError(null)
    cancelNodeEnrollment(cancelTarget.id)
      .then(() => {
        setCancelTarget(null)
        setAttempt((n) => n + 1)
      })
      .catch((err: unknown) => setCancelError(describeApiError(err)))
      .finally(() => setCancelling(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">
        Settings <span className="sm-faint">/</span> Nodes <span className="sm-faint">/</span> Enrollment
      </p>
      <h2 className="sm-section__title">Add a node with a one-time enrollment code</h2>

      <Section id="se-mint" title="Mint an enrollment code">
        <div className="sm-grid sm-form-column">
          <Field label="Node id" help={hint ?? 'The id the node will use, for example render-01.'}>
            {(props) => (
              <Input
                {...props}
                value={nodeId}
                onChange={(e) => setNodeId(e.target.value)}
                disabled={!gate.allowed}
              />
            )}
          </Field>
        </div>

        <ButtonRow>
          <Button
            variant="primary"
            onClick={() => mint(nodeId.trim(), false)}
            disabled={!canMint}
            title={!gate.allowed ? gate.reason : hint ?? undefined}
          >
            {minting ? 'Minting…' : 'Mint enrollment code'}
          </Button>
        </ButtonRow>

        {mintError !== null && <RuledStrip absence="failed" label="Refused" fact={mintError} />}

        {issued !== null && (
          <OneTimeSecret
            headline="This code will not be shown again. Copy it now."
            value={issued.code}
            detail={<InstallDetail issued={issued} coordinatorVersion={coordinatorVersion} />}
            onDismiss={() => setIssued(null)}
          />
        )}
      </Section>

      <Section id="se-list" title="Enrollment codes">
        {enrollments.kind === 'denied' && <RuledStrip absence="noPermission" label="Not shown" fact={enrollments.reason} />}
        {enrollments.kind === 'loading' && (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for enrollment codes." />
        )}
        {enrollments.kind === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={enrollments.reason} />}
        {enrollments.kind === 'loaded' && enrollments.enrollments.length === 0 && (
          <RuledStrip absence="empty" label="None" fact="No enrollment codes have been minted. Mint one above to add a node." />
        )}
        {enrollments.kind === 'loaded' && enrollments.enrollments.length > 0 && (
          <TableWrap label="Enrollment codes, scrollable">
            <Table minWidth={760}>
              <thead>
                <tr>
                  <th scope="col">Node</th>
                  <th scope="col">State</th>
                  <th scope="col">Re-enroll</th>
                  <th scope="col">Created by</th>
                  <th scope="col">Created</th>
                  <th scope="col">Expires</th>
                  <th scope="col">Redeemed</th>
                  <th scope="col">Cancel</th>
                </tr>
              </thead>
              <tbody>
                {enrollments.enrollments.map((e) => (
                  <tr key={e.id}>
                    <td>
                      <span className="sm-data">{e.nodeId}</span>
                      <br />
                      <span className="sm-small sm-faint">
                        id <span className="sm-data">{e.id}</span>
                      </span>
                    </td>
                    <td className="sm-data">{e.state}</td>
                    <td className="sm-data">{e.reenroll ? 'yes' : 'no'}</td>
                    <td className="sm-data">{e.createdBy}</td>
                    <td className="sm-data">{formatDateClock(e.createdAt) ?? 'unrecorded'}</td>
                    <td className="sm-data">{formatDateClock(e.expiresAt) ?? 'unrecorded'}</td>
                    <td className="sm-data">{e.redeemedAt === null ? 'Not redeemed' : formatDateClock(e.redeemedAt)}</td>
                    <td>
                      {e.state === 'pending' && (
                        <Button
                          variant="danger"
                          onClick={() => {
                            setCancelTarget(e)
                            setCancelError(null)
                          }}
                        >
                          Cancel
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </TableWrap>
        )}
      </Section>

      <ConfirmDialog
        open={pendingReenroll !== null}
        title="Re-enroll this node?"
        detail={
          pendingReenroll !== null && (
            <>
              <p>{pendingReenroll.detail}</p>
              {reenrollError !== null && <p>{reenrollError}</p>}
            </>
          )
        }
        confirmLabel={minting ? 'Minting…' : 'Mint re-enrollment code'}
        busy={minting}
        onConfirm={() => pendingReenroll !== null && mint(pendingReenroll.nodeId, true)}
        onCancel={() => {
          setPendingReenroll(null)
          setReenrollError(null)
        }}
      />

      <ConfirmDialog
        open={cancelTarget !== null}
        title="Cancel this enrollment code?"
        detail={
          cancelTarget !== null && (
            <>
              <p>
                The code for node <span className="sm-data">{cancelTarget.nodeId}</span> will stop working. This cannot be
                undone.
              </p>
              {cancelError !== null && <p>{cancelError}</p>}
            </>
          )
        }
        confirmLabel={cancelling ? 'Cancelling…' : 'Cancel code'}
        busy={cancelling}
        onConfirm={confirmCancel}
        onCancel={() => {
          setCancelTarget(null)
          setCancelError(null)
        }}
      />
    </>
  )
}
