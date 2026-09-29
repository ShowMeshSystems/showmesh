/**
 * The node ID rule and install command, mirrored from pkg/mqttproto/topic.go
 * and cmd/showmeshctl/cmd_node_enroll.go. The coordinator stays the authority.
 */

const NODE_ID_PATTERN = /^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$/

// pkg/mqttproto's IsCommandNameNodeID plus enrollment.reservedBrokerUsernames.
const RESERVED_NODE_IDS = new Set(['enroll', 'enrollments', 'coordinator', 'fpp', 'healthcheck', 'observer'])

/** Null when nodeId would be accepted; otherwise the reason it would not. */
export function nodeIdHint(nodeId: string): string | null {
  if (nodeId === '') return null
  if (!NODE_ID_PATTERN.test(nodeId)) {
    return 'Use 1 to 64 lowercase letters, digits and inner hyphens, not starting or ending with a hyphen.'
  }
  if (RESERVED_NODE_IDS.has(nodeId)) {
    return `"${nodeId}" is reserved and cannot be used as a node id.`
  }
  return null
}

// cmd/showmeshctl/cmd_node_enroll.go's releaseVersionPattern.
const RELEASE_VERSION_PATTERN = /^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$/

/** The one command a node runs to join with code, matching what `showmeshctl node enroll` prints. */
export function nodeInstallCommand(version: string, coordinatorUrl: string, code: string): string {
  const args = `--coordinator ${coordinatorUrl} --code ${code}`
  if (RELEASE_VERSION_PATTERN.test(version)) {
    return `curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v${version}/get-showmesh.sh | sudo bash -s -- ${args}`
  }
  return `sudo showmesh-install ${args}`
}

export const COORDINATOR_PLACEHOLDER_HOST = 'COORDINATOR-ADDRESS'

/** cmd_node_enroll.go's replaceLoopbackHost: a node cannot reach the coordinator at a loopback address. */
export function replaceLoopbackHost(rawUrl: string): { url: string; loopback: boolean } {
  let parsed: URL
  try {
    parsed = new URL(rawUrl)
  } catch {
    return { url: rawUrl, loopback: false }
  }
  const host = parsed.hostname.replace(/^\[|\]$/g, '').toLowerCase()
  const isLoopback = host === 'localhost' || host === '::1' || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host)
  if (!isLoopback) return { url: rawUrl, loopback: false }
  // Rebuilt by hand: the URL setter would lower-case the placeholder host.
  const replacedHost = parsed.port === '' ? COORDINATOR_PLACEHOLDER_HOST : `${COORDINATOR_PLACEHOLDER_HOST}:${parsed.port}`
  const path = parsed.pathname === '/' ? '' : parsed.pathname
  return { url: `${parsed.protocol}//${replacedHost}${path}${parsed.search}${parsed.hash}`, loopback: true }
}
