import { useState } from 'react'
import { runSitePower } from '../api'
import { Button, ButtonRow, ConfirmDialog, Notice, WindowDialog } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'

type Which = 'on' | 'off'

const CONFIRM: Record<Which, { title: string; detail: string; label: string }> = {
  on: { title: 'Turn on site power?', detail: 'This runs the power-on actions from Site settings now.', label: 'Power on' },
  off: { title: 'Turn off site power?', detail: 'This runs every shutdown sequence from Site settings now, with its waits.', label: 'Power off' },
}

/** The Testing button: System power on and off outside a night, each behind a second confirmation (ADR-065). */
export function SitePowerTesting() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'show:emergencystop:invoke')
  const [open, setOpen] = useState(false)
  const [confirm, setConfirm] = useState<Which | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<{ tone: 'good' | 'bad'; text: string } | null>(null)
  const reason = gate.allowed ? undefined : gate.reason

  const run = (which: Which) => {
    setBusy(true)
    runSitePower(which)
      .then((resp) => setResult({ tone: 'good', text: resp.message }))
      .catch((err: unknown) => setResult({ tone: 'bad', text: describeApiError(err) }))
      .finally(() => {
        setBusy(false)
        setConfirm(null)
      })
  }

  return (
    <>
      <ButtonRow>
        <Button
          variant="quiet"
          onClick={() => {
            setResult(null)
            setOpen(true)
          }}
        >
          Testing
        </Button>
      </ButtonRow>
      <WindowDialog open={open} title="Testing" onClose={() => setOpen(false)}>
        <p className="sm-body">Turn site power on or off outside a night, to check the wiring. Both are refused while a night is open.</p>
        <ButtonRow>
          <Button disabled={reason !== undefined} title={reason} onClick={() => setConfirm('on')}>
            System power on
          </Button>
          <Button disabled={reason !== undefined} title={reason} onClick={() => setConfirm('off')}>
            System power off
          </Button>
        </ButtonRow>
        {result !== null && (result.tone === 'good' ? <p className="sm-small sm-muted" role="status">{result.text}</p> : <Notice tone="bad" headline="Not started." explanation={result.text} />)}
      </WindowDialog>
      <ConfirmDialog
        open={confirm !== null}
        title={confirm === null ? '' : CONFIRM[confirm].title}
        detail={confirm === null ? null : <p className="sm-body">{CONFIRM[confirm].detail}</p>}
        confirmLabel={confirm === null ? '' : CONFIRM[confirm].label}
        busy={busy}
        onConfirm={() => confirm !== null && run(confirm)}
        onCancel={() => setConfirm(null)}
      />
    </>
  )
}
