export type FieldChange = { path: string; kind: 'added' | 'removed' | 'changed'; before: string | null; after: string | null }

const MAX_VALUE_LENGTH = 80

function flatten(value: unknown, path: string, out: Map<string, string>): void {
  if (Array.isArray(value) && value.length > 0) {
    value.forEach((item, index) => flatten(item, `${path}[${index}]`, out))
  } else if (value !== null && typeof value === 'object' && Object.keys(value).length > 0) {
    for (const [key, item] of Object.entries(value)) flatten(item, path === '' ? key : `${path}.${key}`, out)
  } else {
    out.set(path === '' ? '(whole value)' : path, typeof value === 'string' ? value : JSON.stringify(value))
  }
}

function truncate(text: string): string {
  return text.length > MAX_VALUE_LENGTH ? `${text.slice(0, MAX_VALUE_LENGTH)}…` : text
}

/** Flat list of changed field paths between two payloads, values truncated for display. */
export function diffPayloads(before: unknown, after: unknown): FieldChange[] {
  const a = new Map<string, string>()
  const b = new Map<string, string>()
  flatten(before, '', a)
  flatten(after, '', b)
  const changes: FieldChange[] = []
  for (const path of [...new Set([...a.keys(), ...b.keys()])].sort()) {
    const old = a.get(path)
    const next = b.get(path)
    if (old === next) continue
    changes.push({
      path,
      kind: old === undefined ? 'added' : next === undefined ? 'removed' : 'changed',
      before: old === undefined ? null : truncate(old),
      after: next === undefined ? null : truncate(next),
    })
  }
  return changes
}
