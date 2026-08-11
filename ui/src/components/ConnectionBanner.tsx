import type { ConnectionState } from '../app/types'

// The global connection banner: the "UI cannot reach the coordinator"
// failure mode (spec section 6.3, OPERATOR-UI section 7). It is
// deliberately styled with its own --connection-problem-* tokens
// (styles/status.css), never the --status-bad-* pair a stale sensor
// reading or a failed FPP instance uses elsewhere on the page, so a
// browser/network problem can never be mistaken for a show problem or
// vice versa. Per-resource evidence problems are rendered inline by
// EvidenceValue/DomainBadges and never touch this component.
export interface ConnectionBannerProps {
  connection: ConnectionState
}

export function ConnectionBanner({ connection }: ConnectionBannerProps) {
  switch (connection.kind) {
    case 'connecting':
      return (
        <div className="connection-banner" role="status">
          <span>Connecting to the ShowMesh coordinator…</span>
        </div>
      )
    case 'live':
      return (
        <div className="connection-banner connection-banner--live" role="status">
          <span>● Live — connected to the coordinator</span>
        </div>
      )
    case 'reconnecting':
      return (
        <div className="connection-banner" role="alert">
          <span>
            ⚠ Cannot reach the ShowMesh coordinator right now. This is a browser/network
            problem, not a report about the show — the show continues regardless.
            Reconnecting (attempt {connection.attempt})…
          </span>
          <span className="connection-banner__detail">{connection.lastError}</span>
        </div>
      )
    case 'unauthorized':
      return (
        <div className="connection-banner" role="alert">
          <span>
            🔒{' '}
            {connection.reason === 'rejected'
              ? 'The API token entered below was rejected.'
              : 'This coordinator requires an API token to view its data.'}
          </span>
        </div>
      )
    case 'incompatible': {
      // D3: an empty supportedVersions (the client throws this with `[]`
      // when the response's ShowMesh-API-Version header is simply absent
      // or unparseable -- see api/client.ts's checkVersionHeader) is an
      // absence of information, not a report that the coordinator
      // supports zero versions. The old wording rendered "...only
      // supports versions ." in that case: an assertion about the
      // coordinator made from no evidence. `detail` still carries the
      // real, verified fact.
      const versionsClause =
        connection.supportedVersions.length === 0
          ? 'the coordinator did not report which versions it supports'
          : `the coordinator only supports version${connection.supportedVersions.length === 1 ? '' : 's'} ${connection.supportedVersions.join(', ')}`
      return (
        <div className="connection-banner" role="alert">
          <span>
            ⚠ This UI requires control API version {connection.requiredVersion}, but{' '}
            {versionsClause}. Reconnecting will not help — update the coordinator or this UI so
            their versions match.
          </span>
          <span className="connection-banner__detail">{connection.detail}</span>
        </div>
      )
    }
    case 'failed':
      return (
        <div className="connection-banner" role="alert">
          <span>⚠ Cannot reach the ShowMesh coordinator.</span>
          <span className="connection-banner__detail">{connection.detail}</span>
        </div>
      )
  }
}
