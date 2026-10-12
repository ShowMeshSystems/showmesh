import { useEffect, useState } from 'react'
import { getSystemVersions, type SystemComponentVersion, type SystemRelease, type SystemVersionsResponse } from '../api'
import { CopyButton, DefinitionStrip, Notice, RuledStrip, Section, StatusPair, Table, TableWrap, type Definition } from '../kit'
import { describeApiError } from '../domain/session'
import { formatDateClock } from '../domain/time'

type Load = { kind: 'loading' } | { kind: 'loaded'; response: SystemVersionsResponse } | { kind: 'failed'; reason: string }

function useSystemVersions(): Load {
  const [state, setState] = useState<Load>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    getSystemVersions()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
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

function intervalText(seconds: number): string {
  if (seconds % 3600 === 0) return seconds === 3600 ? 'Every hour' : `Every ${seconds / 3600} hours`
  return `Every ${Math.round(seconds / 60)} minutes`
}

/** The release check's last result, as one sentence. */
function releaseResult(release: SystemRelease): string {
  if (!release.checkEnabled) return 'The release check is turned off.'
  if (release.tag === null) return release.problem ?? 'The first release check has not finished.'
  return release.available ? `Release ${release.tag} is available.` : `${release.tag} is the newest release. Nothing to install.`
}

/** Settings: the release check's setting and its last result. The setting is read-only here. */
export function SettingsReleaseCheck() {
  const load = useSystemVersions()
  return (
    <Section
      id="st-release-check"
      title="Release check"
      detail="The coordinator asks GitHub for the newest release. It installs nothing."
    >
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the release check." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <ReleaseCheckBody release={load.response.release} />
      )}
    </Section>
  )
}

function ReleaseCheckBody({ release }: { release: SystemRelease }) {
  const items: Definition[] = [
    { term: 'Check', value: release.checkEnabled ? 'On' : 'Off' },
    { term: 'Interval', value: intervalText(release.checkIntervalSeconds) },
    { term: 'Last answer', value: formatDateClock(release.checkedAt) ?? 'None yet' },
  ]
  return (
    <>
      <DefinitionStrip items={items} />
      <p>{releaseResult(release)}</p>
      {release.tag !== null && release.problem !== null && <p className="sm-small sm-muted">{release.problem}</p>}
      {release.available && <InstallCommand release={release} />}
      <p className="sm-small sm-muted">
        Change it with <span className="sm-data">SHOWMESH_RELEASE_CHECK</span> and{' '}
        <span className="sm-data">SHOWMESH_RELEASE_CHECK_INTERVAL</span> on the coordinator, then restart it.
      </p>
    </>
  )
}
