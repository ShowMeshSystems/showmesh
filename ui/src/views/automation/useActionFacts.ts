import { useEffect, useState } from 'react'
import { getShowAction } from '../../api'
import type { ActionFacts } from './automationDerive'
import { isUnconfirmableByDesign } from './automationDerive'

/**
 * Caches each referenced action's own integration/safetyClass/unconfirmable
 * facts, keyed by action id — the same "fetch once per distinct id, cache
 * the answer, `null` means tried-and-failed so it is never retried" shape
 * MacroDetail.tsx's own `actionIntegrations` cache used, extended with
 * `safetyClass` (macro consequence, design decision 10) and
 * `unconfirmableByDesign` (design decision 9). A show action's own
 * integration/safetyClass are not on ConfigObjectSummary (id/label/revision
 * only), so this fetches each action's full payload.
 */
export function useActionFacts(actionIds: string[], enabled: boolean): Record<string, ActionFacts | null> {
  const [facts, setFacts] = useState<Record<string, ActionFacts | null>>({})

  useEffect(() => {
    if (!enabled) return
    const unknownIds = [...new Set(actionIds.filter((id) => id !== '' && !(id in facts)))]
    if (unknownIds.length === 0) return
    let cancelled = false
    void Promise.all(
      unknownIds.map((id) =>
        getShowAction(id)
          .then((resp): readonly [string, ActionFacts] => [
            id,
            {
              integration: resp.payload.target.integration,
              safetyClass: resp.payload.safetyClass,
              unconfirmableByDesign: isUnconfirmableByDesign(resp.payload.target.integration, resp.payload.target.expect?.kind),
            },
          ])
          .catch((): readonly [string, null] => [id, null]),
      ),
    ).then((results) => {
      if (cancelled) return
      setFacts((prev) => {
        let changed = false
        const next = { ...prev }
        for (const [id, fact] of results) {
          if (!(id in next)) {
            next[id] = fact
            changed = true
          }
        }
        return changed ? next : prev
      })
    })
    return () => {
      cancelled = true
    }
  }, [actionIds, enabled, facts])

  return facts
}
