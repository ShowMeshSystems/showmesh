import { useEffect, useState } from 'react'
import { ApiError, PROBLEM_TYPE, deployNodeCueCatalog, getNodeCueCatalog, type CueCatalogDeployResult, type CueCatalogResponse } from '../api'
import { describeApiError } from '../domain/session'

/** The node's cue catalog and its deploy, including the refusal that Deploy anyway overrides. The node's copy is a deployable projection, never fabricated from capability advertisements. */
export function useCueCatalogDeploy(nodeId: string) {
  const [catalog, setCatalog] = useState<CueCatalogResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  // conflictDetail is the overridable 409's own `detail` text (the real
  // claim conflict, never invented), set only while a deploy is refused on it.
  const [conflictDetail, setConflictDetail] = useState<string | null>(null)
  const [result, setResult] = useState<CueCatalogDeployResult | null>(null)
  const [deploying, setDeploying] = useState(false)
  const [confirmingOverride, setConfirmingOverride] = useState(false)
  const reload = () => getNodeCueCatalog(nodeId).then(setCatalog).catch((err: unknown) => setError(describeApiError(err)))
  useEffect(() => { reload() }, [nodeId])
  const deploy = (override?: boolean) => {
    setDeploying(true)
    setError(null)
    setConflictDetail(null)
    deployNodeCueCatalog(nodeId, override)
      .then((response) => { setResult(response); reload() })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.cueCatalogClaimConflict) {
          setConflictDetail(err.message)
          return
        }
        setError(describeApiError(err))
      })
      .finally(() => setDeploying(false))
  }
  const deployAnyway = () => {
    if (conflictDetail === null) return
    setConfirmingOverride(true)
  }
  const confirmDeployAnyway = () => {
    setConfirmingOverride(false)
    deploy(true)
  }
  return { catalog, error, conflictDetail, result, deploying, confirmingOverride, setConfirmingOverride, deploy, deployAnyway, confirmDeployAnyway }
}


export type NodeDeployStatus = 'pending' | 'confirmed' | 'unconfirmed' | 'refused' | 'conflict' | 'failed'
export type NodeDeployRow = { nodeId: string; status: NodeDeployStatus; reason: string; overridden: string[] }

const OUTCOME_STATUS: Record<string, NodeDeployStatus> = { confirmed: 'confirmed', unconfirmed: 'unconfirmed', refused: 'refused', '': 'pending' }

function fallbackReason(status: NodeDeployStatus): string {
  if (status === 'confirmed') return 'The node holds this catalog.'
  if (status === 'unconfirmed') return 'The catalog was sent to this node.'
  if (status === 'pending') return 'The node is still resolving this catalog.'
  return 'The node did not confirm the catalog. Check the node.'
}

function rowFromResult(nodeId: string, result: CueCatalogDeployResult): NodeDeployRow {
  const status = OUTCOME_STATUS[result.outcome] ?? 'failed'
  return { nodeId, status, reason: result.reason ?? fallbackReason(status), overridden: (result.overriddenConditions ?? []).map((c) => c.claim) }
}

/** Deploys the active show's cue catalog to each node in turn through the per-node route; Deploy anyway retries only the claim-conflict nodes. */
export function useShowCatalogDeploy(nodeIds: readonly string[]) {
  const [rows, setRows] = useState<NodeDeployRow[]>([])
  const [deploying, setDeploying] = useState(false)
  const [confirmingOverride, setConfirmingOverride] = useState(false)
  const conflicts = rows.filter((r) => r.status === 'conflict')

  const run = async (targets: readonly string[], override: boolean) => {
    setDeploying(true)
    setRows((prev) => (override ? prev : targets.map((nodeId) => ({ nodeId, status: 'pending', reason: 'Waiting to deploy.', overridden: [] }))))
    for (const nodeId of targets) {
      let row: NodeDeployRow
      try {
        row = rowFromResult(nodeId, await deployNodeCueCatalog(nodeId, override ? true : undefined))
      } catch (err: unknown) {
        const conflict = err instanceof ApiError && err.problemType === PROBLEM_TYPE.cueCatalogClaimConflict
        row = { nodeId, status: conflict ? 'conflict' : 'failed', reason: conflict ? (err as ApiError).message : describeApiError(err), overridden: [] }
      }
      setRows((prev) => prev.map((r) => (r.nodeId === nodeId ? row : r)))
    }
    setDeploying(false)
  }

  return {
    rows,
    deploying,
    conflicts,
    confirmingOverride,
    setConfirmingOverride,
    deploy: () => run(nodeIds, false),
    deployAnyway: () => setConfirmingOverride(true),
    confirmDeployAnyway: () => {
      setConfirmingOverride(false)
      void run(conflicts.map((r) => r.nodeId), true)
    },
  }
}
