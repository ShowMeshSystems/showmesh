import { useEffect, useState } from 'react'
import type { ConfigRevisionMeta, ConfigRevisionsResponse } from '../api'
import { formatClock } from '../domain/time'
import { Section } from './Blocks'
import { RuledStrip } from './StateBlocks'
import { StatusPair } from './Status'
import { Button } from './Button'
import { RevisionDiff } from './RevisionDiff'
import { diffPayloads, type FieldChange } from './diffPayloads'

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
  /** Reads one version's full payload. When given, each list row gets a View version button that shows what changed. */
  fetchRevision?: (revision: number) => Promise<{ payload: unknown }>
}

type LoadState = 'loading' | 'failed' | ConfigRevisionMeta[]

/**
 * Disclosure only: who changed a config object, when, and its note. This is
 * not the D-014 stale-write guard, which re-reads the object itself to
 * detect a moved revision; this reads the revision LIST.
 */
export function RevisionHistory({ fetch, reloadKey, title = 'History', id = 'st-rev', mode = 'compact', fetchRevision }: Props) {
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
      <RuledStrip absence="loading" label="Reading" fact="Reading history." />
    ) : state === 'failed' ? (
      <RuledStrip absence="failed" label="Read failed" fact="History could not be read." />
    ) : state.length === 0 ? (
      <RuledStrip absence="empty" label="None" fact="No earlier versions." />
    ) : mode === 'compact' ? (
      <CompactSummary revisions={state} />
    ) : (
      <RevisionList revisions={state} {...(fetchRevision === undefined ? {} : { fetchRevision })} />
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
      Active version <span className="sm-data">{active.revision}</span> ·{' '}
      {active.createdByPrincipalName ?? 'an unknown user'} {formatClock(active.createdAt) ?? 'at an unrecorded time'}
    </p>
  )
}

type Viewing = { revision: number; changes: FieldChange[] | null } | 'loading' | 'failed'

function RevisionList({ revisions, fetchRevision }: { revisions: ConfigRevisionMeta[]; fetchRevision?: (revision: number) => Promise<{ payload: unknown }> }) {
  const [open, setOpen] = useState<{ revision: number; viewing: Viewing } | null>(null)

  const view = (rev: ConfigRevisionMeta) => {
    if (fetchRevision === undefined) return
    if (open?.revision === rev.revision) {
      setOpen(null)
      return
    }
    const previous = Math.max(0, ...revisions.map((r) => r.revision).filter((n) => n < rev.revision))
    setOpen({ revision: rev.revision, viewing: 'loading' })
    Promise.all([fetchRevision(rev.revision), previous === 0 ? Promise.resolve(null) : fetchRevision(previous)])
      .then(([current, before]) => {
        const viewing: Viewing = { revision: rev.revision, changes: before === null ? null : diffPayloads(before.payload, current.payload) }
        setOpen((now) => (now?.revision === rev.revision ? { revision: rev.revision, viewing } : now))
      })
      .catch(() => setOpen((now) => (now?.revision === rev.revision ? { revision: rev.revision, viewing: 'failed' } : now)))
  }

  return (
    <div>
      {revisions.map((rev) => {
        const viewing = open?.revision === rev.revision ? open.viewing : null
        return (
          <div key={rev.revision} className="sm-stack-3">
            <div className="sm-inline-row">
              <StatusPair tone={rev.active ? 'good' : 'pending'} label={rev.active ? `Active · ${rev.revision}` : String(rev.revision)} />
              <p className="sm-small sm-muted">
                {formatClock(rev.createdAt) ?? 'unrecorded time'} by {rev.createdByPrincipalName ?? 'an unknown user'}
                {rev.note !== '' && `. ${rev.note}`}
              </p>
              {fetchRevision !== undefined && (
                <Button variant="quiet" aria-expanded={viewing !== null} onClick={() => view(rev)}>
                  {viewing === null ? 'View version' : 'Hide version'}
                </Button>
              )}
            </div>
            {viewing === 'loading' && <RuledStrip absence="loading" label="Reading" fact={`Reading version ${rev.revision}.`} />}
            {viewing === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={`Version ${rev.revision} could not be read.`} />}
            {viewing !== null && typeof viewing === 'object' && <RevisionDiff revision={viewing.revision} changes={viewing.changes} />}
          </div>
        )
      })}
    </div>
  )
}
