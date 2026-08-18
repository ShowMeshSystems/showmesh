import { useEffect, useState } from 'react'
import { getActionBinding } from '../api'
import { ActionBindingBadge } from './DomainBadges'
import type { ActionBinding } from '../app/types'

// Fetches and renders one action's own pre-show binding check (Track E
// seam E7-2, ADR-029) — a read, no credential required. Best-effort: a
// fetch failure renders nothing rather than an error, since this is a
// secondary fact about the action, not the detail view's own primary
// content.
export function ActionBindingCheck({ actionId }: { actionId: string }) {
  const [binding, setBinding] = useState<ActionBinding | null>(null)

  useEffect(() => {
    let cancelled = false
    setBinding(null)
    getActionBinding(actionId)
      .then((b) => {
        if (!cancelled) setBinding(b)
      })
      .catch(() => {
        // Best-effort — see this component's own doc comment.
      })
    return () => {
      cancelled = true
    }
  }, [actionId])

  if (!binding) return null
  return (
    <span>
      Binding: <ActionBindingBadge state={binding.state} reason={binding.reason} />
    </span>
  )
}
