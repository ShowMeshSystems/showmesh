import { useEffect, useState } from 'react'
import type { ConfigRevisionMeta, ConfigRevisionsResponse } from '../api'
import { formatClock } from '../domain/time'
import { Section } from './Blocks'
import { RuledStrip } from './StateBlocks'
import { StatusPair } from './Status'
import { Button } from './Button'

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
  /**
   * `'compact'` (the default) draws the one-line active-revision summary
   * every mock but Settings' Node routing tab draws. `'list'` draws the
   * expandable revision list Node routing's mock draws; do not opt another
   * screen into it without a matching mock.
   */
  mode?: 'compact' | 'list'
  /** Optional full-payload reader owned by the editor; history itself only fetches metadata. */
  onSelect?: (revision: ConfigRevisionMeta) => void
}

type LoadState = 'loading' | 'failed' | ConfigRevisionMeta[]

/**
 * Disclosure only: who changed a config object, when, and its note. This is
 * not the D-014 stale-write guard, which re-reads the object itself to
 * detect a moved revision; this reads the revision LIST.
 */
export function RevisionHistory({ fetch, reloadKey, title = 'Revisions', id = 'st-rev', mode = 'compact', onSelect }: Props) {
  const [state, setState] = useState<LoadState>('loading')

  useEffect(() => {
    let cancelled = false
    setState('loading')
    fetch()
      .then((response) => {
        if (!cancelled) setState(response.revisions)
      })
      .catch(() => {
        if (!cancelled) setState('failed')
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadKey])

  const body =
    state === 'loading' ? (
      <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the revision history." />
    ) : state === 'failed' ? (
      <RuledStrip absence="failed" label="Read failed" fact="Revision history could not be read just now." />
    ) : state.length === 0 ? (
      <RuledStrip absence="empty" label="None" fact="No prior revision recorded." />
    ) : mode === 'compact' ? (
      <CompactSummary revisions={state} />
    ) : (
      <RevisionList revisions={state} {...(onSelect === undefined ? {} : { onSelect })} />
    )

  if (mode === 'compact') return body

  return (
    <Section id={id} title={title}>
      {body}
    </Section>
  )
}

/** `revisions` must be non-empty; callers gate on `.length === 0` first. */
function CompactSummary({ revisions }: { revisions: ConfigRevisionMeta[] }) {
  const active = revisions.find((rev) => rev.active) ?? revisions[0]
  if (active === undefined) return null
  return (
    <p className="sm-small sm-muted">
      Active revision <span className="sm-data">{active.revision}</span> ·{' '}
      {active.createdByPrincipalName ?? 'unknown principal'} {formatClock(active.createdAt) ?? 'at an unrecorded time'}
    </p>
  )
}

function RevisionList({ revisions, onSelect }: { revisions: ConfigRevisionMeta[]; onSelect?: (revision: ConfigRevisionMeta) => void }) {
  return (
    <div>
      {revisions.map((rev) => (
        <div key={rev.revision} className="sm-inline-row sm-stack-3">
          <StatusPair tone={rev.active ? 'good' : 'pending'} label={rev.active ? `Active · ${rev.revision}` : String(rev.revision)} />
          <p className="sm-small sm-muted">
            {formatClock(rev.createdAt) ?? 'unrecorded time'} by {rev.createdByPrincipalName ?? 'unknown principal'}
            {rev.note !== '' && `. ${rev.note}`}
          </p>
          {onSelect !== undefined && <Button variant="quiet" onClick={() => onSelect(rev)}>View revision</Button>}
        </div>
      ))}
    </div>
  )
}
