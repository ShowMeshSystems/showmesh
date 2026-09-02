import { TONE_GLYPH, type Tone } from './tone'

type Props = {
  tone: Tone
  /** The state word. Required: colour is never the only signal. */
  label: string
  glyph?: string
  size?: 'default' | 'lg'
  /** A compact word status is used where the approved layout supplies the surrounding row treatment. */
  appearance?: 'chip' | 'word'
}

/**
 * A labelled status pair. A generic unknown is a reported inability to
 * determine a state, not proof that evidence was never collected; only the
 * explicit unobserved state blocks use a dashed edge.
 */
export function StatusPair({ tone, label, glyph, size = 'default', appearance = 'chip' }: Props) {
  const classes = ['sm-status', `sm-status--${tone}`, size === 'lg' ? 'sm-status--lg' : '', appearance === 'word' ? 'sm-status--word' : ''].filter(Boolean).join(' ')
  return (
    <span className={classes}>
      <span aria-hidden="true">{glyph ?? TONE_GLYPH[tone]}</span>
      {label}
    </span>
  )
}
