import { useEffect, useState } from 'react'
import type { ConfigRevisionMeta, ConfigRevisionsResponse } from '../api'
import { formatClock } from '../domain/time'
import { Section } from './Blocks'
import { RuledStrip } from './StateBlocks'
import { StatusPair } from './Status'

type Props = {
  /** Bound to whatever object this history belongs to, e.g. `() => getShowRevisions(id)`. */
  fetch: () => Promise<ConfigRevisionsResponse>
  /**
   * Changes to trigger a refetch, e.g. an id plus a post-save attempt
   * counter. `fetch` is intentionally not a dependency: callers pass a new
   * closure every render.
   */
  reloadKey: string | number
  title?: string
  /** Section heading id. Give each a distinct one when a screen renders more than one history. */
  id?: string
}

/**
 * Disclosure only: who changed a config object, when, and its note. This is
 * not the D-014 stale-write guard, which re-reads the object itself to
 * detect a moved revision; this reads the revision LIST.
 */
export function RevisionHistory({ fetch, reloadKey, title = 'Revisions', id = 'st-rev' }: Props) {
  const [revisions, setRevisions] = useState<ConfigRevisionMeta[] | null>(null)

  useEffect(() => {
    let cancelled = false
    setRevisions(null)
    fetch()
      .then((response) => {
        if (!cancelled) setRevisions(response.revisions)
      })
      .catch(() => {
        if (!cancelled) setRevisions(null)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadKey])

  return (
    <Section id={id} title={title}>
      {revisions === null ? (
        <RuledStrip absence="unobserved" label="Unread" fact="Revision history could not be read just now." />
      ) : revisions.length === 0 ? (
        <RuledStrip absence="empty" label="None" fact="No prior revision recorded." />
      ) : (
        <div>
          {revisions.map((rev) => (
            <div key={rev.revision} className="sm-inline-row sm-stack-3">
              <StatusPair tone={rev.active ? 'good' : 'pending'} label={rev.active ? `Active · ${rev.revision}` : String(rev.revision)} />
              <p className="sm-small sm-muted">
                {formatClock(rev.createdAt) ?? 'unrecorded time'} by {rev.createdByPrincipalName ?? 'unknown principal'}
                {rev.note !== '' && `. ${rev.note}`}
              </p>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}
