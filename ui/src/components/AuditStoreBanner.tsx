import type { AuditStoreStatus } from '../app/types'

// ADR-024 decision 11's amendment (owner ruling, 2026-08-26): "Audit log
// database becoming unavailable SHOULD NOT STOP ANY ACTIONS, rather than
// stopping it should be LOUD in the UI and non-audit logs about it." This
// is the UI half of that: a standing, coordinator-wide banner an operator
// sees WITHOUT having invoked any action, distinct from the per-action
// `attributionDegraded` flag every command response already carries (that
// flag only answers "was this one action unaudited", never "is audit down
// right now").
//
// Only `state === 'unusable'` renders. `'usable'` renders nothing,
// matching ConnectionBanner's own "no banner when healthy" posture.
export interface AuditStoreBannerProps {
  auditStore: AuditStoreStatus | null
}

export function AuditStoreBanner({ auditStore }: AuditStoreBannerProps) {
  if (auditStore === null || auditStore.state !== 'unusable') {
    return null
  }
  return (
    <div className="audit-store-banner" role="alert">
      <span>
        ⚠ This coordinator cannot currently write to its audit store. The show and every
        action continue to run normally; only attribution logging is affected. Fix this
        before the next show.
      </span>
      {auditStore.reason && <span className="audit-store-banner__detail">{auditStore.reason}</span>}
    </div>
  )
}
