import { useState, type ReactNode } from 'react'
import { Button, ButtonRow } from './Button'

type OneTimeSecretProps = {
  /** States the fact before the value: what this is, and that it will not be shown again. */
  headline: string
  value: string
  /** Rendered under the value: an expiry, an install command, anything else the operator needs while it is on screen. */
  detail?: ReactNode
  onDismiss: () => void
  dismissLabel?: string
}

/**
 * A value the coordinator hands back exactly once (a token, an enrollment
 * code): shown large and copyable, gone once dismissed, never re-derivable
 * from a later read of the same resource.
 */
export function OneTimeSecret({ headline, value, detail, onDismiss, dismissLabel = 'Dismiss' }: OneTimeSecretProps) {
  const [copyStatus, setCopyStatus] = useState<string | null>(null)

  const copy = () => {
    if (navigator.clipboard === undefined) {
      setCopyStatus('Copy failed: the browser refused clipboard access on this connection.')
      return
    }
    navigator.clipboard.writeText(value).then(
      () => setCopyStatus('Copied.'),
      () => setCopyStatus('Copy failed. Select the value and copy it by hand.'),
    )
  }

  return (
    <div className="sm-panel sm-secret" role="status">
      <p className="sm-small sm-muted">{headline}</p>
      <p className="sm-secret__value">{value}</p>
      {detail !== undefined && <div className="sm-secret__detail">{detail}</div>}
      <ButtonRow>
        <Button onClick={copy}>Copy</Button>
        <Button
          variant="quiet"
          onClick={() => {
            setCopyStatus(null)
            onDismiss()
          }}
        >
          {dismissLabel}
        </Button>
      </ButtonRow>
      {copyStatus !== null && (
        <p className="sm-small sm-muted" role="status">
          {copyStatus}
        </p>
      )}
    </div>
  )
}
