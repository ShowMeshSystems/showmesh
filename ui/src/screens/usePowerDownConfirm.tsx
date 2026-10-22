import { useCallback, useState, type ReactNode } from 'react'
import { Choice, ConfirmDialog } from '../kit'

// One confirm dialog for every Power down presentation button. send receives
// whether to force; the choice starts unchecked each time the dialog opens.
export function usePowerDownConfirm(send: (force: boolean) => void): { start: () => void; dialog: ReactNode } {
  const [open, setOpen] = useState(false)
  const [force, setForce] = useState(false)

  const start = useCallback(() => {
    setForce(false)
    setOpen(true)
  }, [])

  const dialog = (
    <ConfirmDialog
      open={open}
      title="Power down presentation?"
      detail={
        <>
          <p className="sm-body">This ends the night and runs the power down sequences.</p>
          <Choice type="checkbox" checked={force} onChange={(event) => setForce(event.target.checked)} label="Force" />
          <p className="sm-field__help">
            Power down now, without waiting for the show to finish or for FPP to confirm it stopped. Safety rules still apply.
          </p>
        </>
      }
      confirmLabel="Power down"
      onConfirm={() => {
        setOpen(false)
        setForce(false)
        send(force)
      }}
      onCancel={() => setOpen(false)}
    />
  )
  return { start, dialog }
}
