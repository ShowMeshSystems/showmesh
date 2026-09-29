import { useState } from 'react'
import { Button } from './Button'

type CopyButtonProps = {
  value: string
  label?: string
}

/** Copies a value to the clipboard and states the outcome beside the button. */
export function CopyButton({ value, label = 'Copy' }: CopyButtonProps) {
  const [status, setStatus] = useState<string | null>(null)

  const copy = () => {
    if (navigator.clipboard === undefined) {
      setStatus('Copy failed: the browser refused clipboard access on this connection.')
      return
    }
    navigator.clipboard.writeText(value).then(
      () => setStatus('Copied.'),
      () => setStatus('Copy failed. Select the text and copy it by hand.'),
    )
  }

  return (
    <>
      <Button onClick={copy}>{label}</Button>
      {status !== null && (
        <span className="sm-small sm-muted" role="status">
          {' '}
          {status}
        </span>
      )}
    </>
  )
}
