import { TONE_GLYPH, type Tone } from './tone'

type Props = {
  tone: Tone
  /** The state word. Required: colour is never the only signal. */
  label: string
  glyph?: string
  size?: 'default' | 'lg'
}

/**
 * A labelled status pair. The unknown tone keeps a dashed edge so absent
 * evidence cannot borrow the shape of a settled state.
 */
export function StatusPair({ tone, label, glyph, size = 'default' }: Props) {
  const classes = ['sm-status', `sm-status--${tone}`, size === 'lg' ? 'sm-status--lg' : ''].filter(Boolean).join(' ')
  return (
    <span className={classes}>
      <span aria-hidden="true">{glyph ?? TONE_GLYPH[tone]}</span>
      {label}
    </span>
  )
}
