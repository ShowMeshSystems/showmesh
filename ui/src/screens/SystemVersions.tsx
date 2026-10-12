import { useEffect, useState } from 'react'
import {
  getReleaseSettingsConfig,
  putReleaseSettingsConfig,
  refreshSystemVersions,
  type ReleaseSettingsConfigResponse,
  type SystemComponentVersion,
  type SystemRelease,
  type SystemVersionsResponse,
} from '../api'
import { Button, ButtonRow, Choice, CopyButton, DefinitionStrip, Input, Notice, RuledStrip, Section, StatusPair, Table, TableWrap, type Definition } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave } from '../domain/save'
import { formatDateClock } from '../domain/time'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'

type Load = { kind: 'loading' } | { kind: 'loaded'; response: SystemVersionsResponse } | { kind: 'failed'; reason: string }

/** The store keeps system versions current, so this block follows it and never fetches on its own. */
function useSystemVersions(): Load {
  const model = useModelContext()
  if (model.systemVersions !== null) return { kind: 'loaded', response: model.systemVersions }
  if (model.systemVersionsFailure !== null) return { kind: 'failed', reason: describeApiError(model.systemVersionsFailure.error) }
  return { kind: 'loading' }
}

function ComponentRow({ kind, component }: { kind: string; component: SystemComponentVersion }) {
  return (
    <tr>
      <td>
        {kind} <span className="sm-data">{component.id}</span>
      </td>
      <td className="sm-data">{component.version ?? 'Not reported'}</td>
      <td>
        {component.mismatch ? (
          <StatusPair tone="warn" label="Mismatch" />
        ) : component.version === null ? (
          <span className="sm-small sm-muted">{component.reason}</span>
        ) : (
          <StatusPair tone="good" label="Matches" />
        )}
      </td>
    </tr>
  )
}

function InstallCommand({ release }: { release: SystemRelease }) {
  if (release.installCommand === null) return null
  return (
    <>
      <p className="sm-data">{release.installCommand}</p>
      <CopyButton value={release.installCommand} label="Copy command" />
    </>
  )
}

/** Monitor's Versions block: what every component runs, one warning per mismatch, and a newer release when there is one. */
export function MonitorVersions() {
  const load = useSystemVersions()
  return (
    <Section id="mo-versions" title="Versions">
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator what every component runs." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <VersionsBody response={load.response} />
      )}
    </Section>
  )
}

function VersionsBody({ response }: { response: SystemVersionsResponse }) {
  const warnings = [...response.nodes, ...response.plugins].flatMap((c) => (c.warning === null ? [] : [c.warning]))
  const { release } = response
  return (
    <>
      <TableWrap label="Component versions, scrollable">
        <Table minWidth={420}>
          <thead>
            <tr>
              <th scope="col">Component</th>
              <th scope="col">Version</th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>Coordinator</td>
              <td className="sm-data">{response.coordinator.version}</td>
              <td />
            </tr>
            {response.nodes.map((node) => (
              <ComponentRow key={`node:${node.id}`} kind="Node" component={node} />
            ))}
            {response.plugins.map((plugin) => (
              <ComponentRow key={`plugin:${plugin.id}`} kind="FPP plugin" component={plugin} />
            ))}
          </tbody>
        </Table>
      </TableWrap>
      {warnings.map((warning) => (
        <Notice key={warning} tone="warn" live="status" headline={warning} />
      ))}
      {release.available && release.tag !== null && (
        <>
          <p>Release {release.tag} is available.</p>
          <InstallCommand release={release} />
        </>
      )}
    </>
  )
}

/** The release check's last result, as one sentence. */
function releaseResult(release: SystemRelease): string {
  if (!release.checkEnabled) return 'The release check is turned off.'
  if (release.tag === null) return release.problem ?? 'The first release check has not finished.'
  return release.available ? `Release ${release.tag} is available.` : `${release.tag} is the newest release. Nothing to install.`
}

const MIN_INTERVAL_MINUTES = 5

type SettingsLoad = { kind: 'loading' } | { kind: 'loaded'; response: ReleaseSettingsConfigResponse } | { kind: 'failed'; reason: string }

/** Settings: the release check's editable setting and its last result. */
export function SettingsReleaseCheck() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const versions = useSystemVersions()
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<SettingsLoad>({ kind: 'loading' })
  const [enabled, setEnabled] = useState(true)
  const [minutes, setMinutes] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stale, setStale] = useState<StaleWrite | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoad({ kind: 'loading' })
    getReleaseSettingsConfig()
      .then((response) => {
        if (cancelled) return
        setLoad({ kind: 'loaded', response })
        setEnabled(response.payload.enabled)
        setMinutes(String(response.payload.checkIntervalSeconds / 60))
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoad({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const save = async () => {
    if (load.kind !== 'loaded') return
    const interval = Number(minutes)
    if (!Number.isFinite(interval) || interval < MIN_INTERVAL_MINUTES) {
      setError(`The check interval is too short. Set it to ${MIN_INTERVAL_MINUTES} minutes or more.`)
      return
    }
    setSaving(true)
    setError(null)
    setStale(null)
    try {
      const loaded = load.response
      const outcome = await guardedSave({
        loaded,
        read: getReleaseSettingsConfig,
        write: () => putReleaseSettingsConfig({ enabled, checkIntervalSeconds: Math.round(interval * 60) }, loaded.revision),
      })
      if (outcome.kind === 'saved') {
        setAttempt((n) => n + 1)
        refreshSystemVersions()
      } else if (outcome.kind === 'stale') {
        setStale(outcome)
      } else {
        setError(outcome.reason)
      }
    } catch (err: unknown) {
      setError(describeApiError(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Section
      id="st-release-check"
      title="Release check"
      detail="The coordinator asks GitHub for the newest release. It installs nothing."
    >
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the release check setting." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <>
          <Choice
            type="checkbox"
            checked={enabled}
            onChange={(e) => {
              setEnabled(e.target.checked)
              setDirty(true)
            }}
            label="Check GitHub for a newer release"
          />
          <div className="sm-grid sm-form-column sm-stack-3">
            <label className="sm-field">
              <span className="sm-field__label">Minutes between checks</span>
              <Input
                type="number"
                min={MIN_INTERVAL_MINUTES}
                value={minutes}
                onChange={(e) => {
                  setMinutes(e.target.value)
                  setDirty(true)
                }}
              />
            </label>
          </div>
          <ButtonRow>
            <Button
              variant="primary"
              disabled={!dirty || saving || !gate.allowed}
              title={gate.allowed ? undefined : gate.reason}
              onClick={() => void save()}
            >
              {saving ? 'Saving…' : 'Save release check'}
            </Button>
          </ButtonRow>
        </>
      )}
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            setAttempt((n) => n + 1)
          }}
        />
      )}
      {error !== null && <RuledStrip absence="failed" label="Save failed" fact={error} />}
      {versions.kind === 'loaded' && <ReleaseCheckResult release={versions.response.release} />}
      {versions.kind === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={versions.reason} />}
    </Section>
  )
}

function ReleaseCheckResult({ release }: { release: SystemRelease }) {
  const items: Definition[] = [{ term: 'Last answer', value: formatDateClock(release.checkedAt) ?? 'None yet' }]
  return (
    <>
      <DefinitionStrip items={items} />
      <p>{releaseResult(release)}</p>
      {release.tag !== null && release.problem !== null && <p className="sm-small sm-muted">{release.problem}</p>}
      {release.available && <InstallCommand release={release} />}
    </>
  )
}
