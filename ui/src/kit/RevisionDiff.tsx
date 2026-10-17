import { type FieldChange } from './diffPayloads'

const KIND_WORD: Record<FieldChange['kind'], string> = { added: 'Added', removed: 'Removed', changed: 'Changed' }

/** `changes` null means the first version, which has nothing before it to compare with. */
export function RevisionDiff({ revision, changes }: { revision: number; changes: FieldChange[] | null }) {
  if (changes === null) return <p className="sm-small sm-muted sm-stack-2">Version {revision} is the first version, so nothing came before it.</p>
  if (changes.length === 0) return <p className="sm-small sm-muted sm-stack-2">Version {revision} has no field changes from the version before it.</p>
  return (
    <dl className="sm-revdiff" aria-label={`Changes in version ${revision}`}>
      {changes.map((change) => (
        <div key={change.path} className="sm-revdiff__row">
          <dt className="sm-data">{change.path}</dt>
          <dd>
            <span className="sm-revdiff__kind">{KIND_WORD[change.kind]}</span>
            {change.before !== null && <span className="sm-revdiff__old sm-data">{change.before}</span>}
            {change.before !== null && change.after !== null && <span aria-hidden="true"> → </span>}
            {change.after !== null && <span className="sm-revdiff__new sm-data">{change.after}</span>}
          </dd>
        </div>
      ))}
    </dl>
  )
}
