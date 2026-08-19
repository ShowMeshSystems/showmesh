import { invokeAction } from '../api'
import { useActionCall } from '../app/useActionCall'
import { ScopedButton } from './ScopedButton'
import { ActionInvocationOutcome } from './ActionInvocationOutcome'

// ADR-037 decision 8's controller surface for a logical action: "a
// dropdown plus a Go button" narrowed to one already-selected action (the
// dropdown is the action list/detail view itself — see ShowActions.tsx
// and ShowActionDetail.tsx). Gated on show:action:invoke via ScopedButton
// (ADR-024 decision 12): a principal without the scope sees a disabled
// button stating why, never nothing.
export interface ActionInvokeButtonProps {
  actionId: string
  label?: string
}

export function ActionInvokeButton({ actionId, label = 'Go' }: ActionInvokeButtonProps) {
  const call = useActionCall()
  const busy = call.state.kind === 'submitting'

  return (
    <div className="action-invoke-button">
      <ScopedButton
        requiredScope="show:action:invoke"
        busy={busy}
        busyReason="This action is already being invoked."
        onClick={() => call.run(() => invokeAction(actionId))}
      >
        {label}
      </ScopedButton>
      {call.state.kind === 'error' && (
        <p role="alert" className="panel panel--error">
          {call.state.message}
        </p>
      )}
      {call.state.kind === 'result' && <ActionInvocationOutcome result={call.state.result} />}
    </div>
  )
}
