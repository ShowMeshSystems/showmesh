import type { EmergencyStopInstanceOutcome } from '../api'
import { Notice, RuledStrip, StatusPair, Table, TableWrap, type Tone } from '../kit'
import { EMERGENCY_LEVEL_LABEL, instanceOutcomeTone, type EmergencyOutcomeState } from './emergencyStop'

/** The three target-kind groups stopOutcomes is dispatched to, in display order, each with its own label and column heading. */
const STOP_OUTCOME_TARGET_KIND_GROUPS: Array<{ kind: string; label: string; column: string }> = [
  { kind: 'fpp', label: 'FPP', column: 'FPP instance' },
  { kind: 'node', label: 'audio node', column: 'Audio node' },
  { kind: 'resolume', label: 'Resolume', column: 'Resolume instance' },
]

/** result.stopOutcomes grouped by targetKind, in [STOP_OUTCOME_TARGET_KIND_GROUPS] order - an operator reads what stopped, what refused, and what was never reached, per kind, rather than one flat list mixing FPP players, audio nodes, and Resolume instances together. */
function groupStopOutcomesByTargetKind(
  stopOutcomes: EmergencyStopInstanceOutcome[],
): Array<{ kind: string; label: string; column: string; outcomes: EmergencyStopInstanceOutcome[] }> {
  return STOP_OUTCOME_TARGET_KIND_GROUPS.map((g) => ({
    ...g,
    outcomes: stopOutcomes.filter((row) => row.targetKind === g.kind),
  }))
}

function followUpOutcomeTone(outcome: string | undefined): Tone {
  if (outcome === undefined) return 'unknown'
  if (outcome === 'confirmed') return 'good'
  if (outcome === 'unconfirmed') return 'warn'
  if (outcome === 'unconfirmable') return 'unknown'
  return 'bad' // refused | failed
}

/**
 * Renders exactly what the coordinator reported: every target's own
 * outcome grouped by target kind (FPP, audio node, Resolume - what
 * stopped, what refused, and what was never reached, per kind), the
 * night-session step where the level has one, and every configured
 * follow-up action's own outcome. Partial success stays partial success:
 * there is no combined pass/fail rollup.
 */
export function EmergencyStopOutcome({ outcome }: { outcome: EmergencyOutcomeState | null }) {
  if (outcome === null) return null
  if (outcome.kind === 'error') {
    const headline = outcome.hadHttpStatus
      ? `${EMERGENCY_LEVEL_LABEL[outcome.level]} was refused: ${outcome.message}`
      : `${EMERGENCY_LEVEL_LABEL[outcome.level]}: the coordinator reported no outcome: ${outcome.message}`
    return <Notice tone="bad" headline={headline} />
  }
  const { level, result } = outcome
  const groups = groupStopOutcomesByTargetKind(result.stopOutcomes)
  return (
    <div className="sm-lc-emergency__outcome">
      <p className="sm-small">
        <strong>{EMERGENCY_LEVEL_LABEL[level]}</strong> was dispatched.
      </p>
      {result.noInstancesConfigured && (
        <RuledStrip
          absence="empty"
          label="No FPP instances"
          fact="No FPP instance is configured on this coordinator, so nothing was stopped on FPP."
        />
      )}
      {groups.map(
        (group) =>
          group.outcomes.length > 0 && (
            <TableWrap key={group.kind} label={`Emergency stop, per ${group.label} target`}>
              <Table minWidth={480}>
                <thead>
                  <tr>
                    <th scope="col">{group.column}</th>
                    <th scope="col">Outcome</th>
                  </tr>
                </thead>
                <tbody>
                  {group.outcomes.map((row) => (
                    <tr key={row.instanceId}>
                      <td>
                        <span className="sm-data">{row.instanceId}</span>
                      </td>
                      <td>
                        <StatusPair tone={instanceOutcomeTone(row.outcome)} label={row.outcome} />
                        <p className="sm-small sm-muted">{row.outcomeReason}</p>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            </TableWrap>
          ),
      )}
      {result.nightSession !== undefined && (
        <p className="sm-small sm-muted">
          {result.nightSession.present
            ? `Night session: ${result.nightSession.outcome ?? 'no outcome reported'}${
                result.nightSession.error === undefined ? '' : ` (${result.nightSession.error})`
              }.`
            : 'No night session was active.'}
        </p>
      )}
      {result.followUpConfigError !== undefined && result.followUpConfigError !== '' && (
        <Notice tone="warn" live="status" headline={`Follow-up actions were not run: ${result.followUpConfigError}`} />
      )}
      {result.followUps.length > 0 && (
        <TableWrap label="Emergency stop, follow-up actions">
          <Table minWidth={480}>
            <thead>
              <tr>
                <th scope="col">Follow-up action</th>
                <th scope="col">Outcome</th>
              </tr>
            </thead>
            <tbody>
              {result.followUps.map((row) => (
                <tr key={row.actionId}>
                  <td>
                    <span className="sm-data">{row.label ?? row.actionId}</span>
                  </td>
                  <td>
                    <StatusPair tone={followUpOutcomeTone(row.outcome)} label={row.outcome ?? 'unresolved'} />
                    <p className="sm-small sm-muted">{row.outcomeReason}</p>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </TableWrap>
      )}
    </div>
  )
}
