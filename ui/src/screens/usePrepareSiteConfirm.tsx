import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { getNightSessionActiveConfig, getNightSessionConfig } from '../api'
import { Choice, ConfirmDialog } from '../kit'

// Prepare site stops only the active night's own FPP players, so the dialog
// names them. A failed lookup falls back to wording that does not name them.
function useNightFppInstances(open: boolean): readonly string[] | null {
  const [instances, setInstances] = useState<readonly string[] | null>(null)
  useEffect(() => {
    if (!open) return
    let cancelled = false
    getNightSessionActiveConfig()
      .then((active) => getNightSessionConfig(active.payload.session))
      .then((definition) => {
        const ids = [definition.payload.resting.fppInstanceId, definition.payload.showPlaylist.fppInstanceId]
        if (!cancelled) setInstances(ids.filter((id, i) => id !== '' && ids.indexOf(id) === i))
      })
      .catch(() => {
        if (!cancelled) setInstances(null)
      })
    return () => {
      cancelled = true
    }
  }, [open])
  return instances
}

export function prepareSiteStopLabel(instances: readonly string[] | null): string {
  const where = instances === null || instances.length === 0 ? "this night's FPP players" : instances.join(' and ')
  return `Stop whatever is playing on ${where}. Other FPP players keep playing. Leave this off when FPP's own schedule started this.`
}

// One confirm dialog for every Prepare site button. send receives whether
// to stop FPP; the choice starts checked each time the dialog opens.
export function usePrepareSiteConfirm(send: (stopFppPlayback: boolean) => void): { start: () => void; dialog: ReactNode } {
  const [open, setOpen] = useState(false)
  const [stop, setStop] = useState(true)
  const instances = useNightFppInstances(open)

  const start = useCallback(() => {
    setStop(true)
    setOpen(true)
  }, [])

  const dialog = (
    <ConfirmDialog
      open={open}
      title="Prepare the site?"
      detail={
        <>
          <p className="sm-body">This sets up everything for the night. It is accepted here, then the session reports what it does.</p>
          <Choice type="checkbox" checked={stop} onChange={(event) => setStop(event.target.checked)} label={prepareSiteStopLabel(instances)} />
        </>
      }
      confirmLabel="Prepare site"
      onConfirm={() => {
        setOpen(false)
        send(stop)
      }}
      onCancel={() => setOpen(false)}
    />
  )
  return { start, dialog }
}
