import type { ButtonHTMLAttributes, ReactNode } from 'react'

type Variant = 'primary' | 'secondary' | 'quiet' | 'danger'
type Size = 'compact' | 'default' | 'gloved' | 'icon'

const VARIANT: Record<Variant, string> = {
  primary: 'sm-btn--primary',
  secondary: '',
  quiet: 'sm-btn--quiet',
  danger: 'sm-btn--danger',
}

const SIZE: Record<Size, string> = {
  compact: 'sm-btn--compact',
  default: '',
  gloved: 'sm-btn--gloved',
  icon: 'sm-btn--icon',
}

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant
  /** Gloved is 48px: transport, lifecycle, macro Run, sign-in and bootstrap.
   *  Icon is square and glyph-only: callers must supply `aria-label`. */
  size?: Size
}

/**
 * A control the principal may not use renders disabled with a stated reason,
 * never hidden. Put the reason in the label or immediately beside it.
 */
export function Button({ variant = 'secondary', size = 'default', className, type = 'button', ...props }: Props) {
  const classes = ['sm-btn', VARIANT[variant], SIZE[size], className].filter(Boolean).join(' ')
  return <button type={type} className={classes} {...props} />
}

export function ButtonRow({ children }: { children: ReactNode }) {
  return <div className="sm-btn-row">{children}</div>
}

/** Separates a destructive control from the save path. */
export function ButtonRule() {
  return <span className="sm-rule-v" aria-hidden="true" />
}

const REORDER_GLYPHS = {
  vertical: { back: { word: 'up', glyph: '▲' }, forward: { word: 'down', glyph: '▼' } },
  horizontal: { back: { word: 'earlier', glyph: '◀' }, forward: { word: 'later', glyph: '▶' } },
} as const

const LEAVE_GLYPHS = { remove: { word: 'Remove', glyph: '✕' }, hide: { word: 'Hide', glyph: '⊘' } } as const

/**
 * The kit's one reorderable-row control pattern (design guide §6): pair
 * with a `⠿` handle in the row's index column; this renders only the
 * move/remove icon buttons. Horizontal suits a grid read left to right.
 */
export function ReorderButtons({
  itemLabel,
  onMoveUp,
  onMoveDown,
  onRemove,
  moveUpReason,
  moveDownReason,
  removeReason,
  orientation = 'vertical',
  leave = 'remove',
}: {
  /** Names the row for its button labels, e.g. "step 2" or the entry's own label. */
  itemLabel: string
  onMoveUp: () => void
  onMoveDown: () => void
  onRemove: () => void
  /** Defined disables the button and states why. */
  moveUpReason?: string | undefined
  moveDownReason?: string | undefined
  removeReason?: string | undefined
  orientation?: 'vertical' | 'horizontal'
  /** Hide takes the item out of view and keeps it; remove deletes it. */
  leave?: 'remove' | 'hide'
}) {
  const { back, forward } = REORDER_GLYPHS[orientation]
  const out = LEAVE_GLYPHS[leave]
  return (
    <ButtonRow>
      <Button size="icon" variant="quiet" aria-label={`Move ${itemLabel} ${back.word}`} title={moveUpReason} onClick={onMoveUp} disabled={moveUpReason !== undefined}>
        <span aria-hidden="true">{back.glyph}</span>
      </Button>
      <Button size="icon" variant="quiet" aria-label={`Move ${itemLabel} ${forward.word}`} title={moveDownReason} onClick={onMoveDown} disabled={moveDownReason !== undefined}>
        <span aria-hidden="true">{forward.glyph}</span>
      </Button>
      <Button size="icon" variant="quiet" aria-label={`${out.word} ${itemLabel}`} title={removeReason} onClick={onRemove} disabled={removeReason !== undefined}>
        <span aria-hidden="true">{out.glyph}</span>
      </Button>
    </ButtonRow>
  )
}
