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

/**
 * The kit's one reorderable-row control pattern (design guide §6): pair
 * with a `⠿` handle in the row's index column; this renders only the
 * move/remove icon buttons.
 */
export function ReorderButtons({
  itemLabel,
  onMoveUp,
  onMoveDown,
  onRemove,
  moveUpReason,
  moveDownReason,
  removeReason,
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
}) {
  return (
    <ButtonRow>
      <Button size="icon" variant="quiet" aria-label={`Move ${itemLabel} up`} title={moveUpReason} onClick={onMoveUp} disabled={moveUpReason !== undefined}>
        <span aria-hidden="true">▲</span>
      </Button>
      <Button size="icon" variant="quiet" aria-label={`Move ${itemLabel} down`} title={moveDownReason} onClick={onMoveDown} disabled={moveDownReason !== undefined}>
        <span aria-hidden="true">▼</span>
      </Button>
      <Button size="icon" variant="quiet" aria-label={`Remove ${itemLabel}`} title={removeReason} onClick={onRemove} disabled={removeReason !== undefined}>
        <span aria-hidden="true">✕</span>
      </Button>
    </ButtonRow>
  )
}
