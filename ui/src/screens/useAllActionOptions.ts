import { useEffect, useState } from 'react'
import { listConfigObjects } from '../api'
import type { ActionOption } from '../kit'

/** Loads every show action, of any show, as dropdown options. */
export function useAllActionOptions(): ActionOption[] {
  const [options, setOptions] = useState<ActionOption[]>([])
  useEffect(() => {
    let cancelled = false
    listConfigObjects('show.action')
      .then((actions) => {
        if (!cancelled) setOptions(actions.objects.map((a) => ({ id: a.id, label: a.show === '' ? a.label : `${a.label} (${a.show})` })))
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])
  return options
}

