import type { ButtonHTMLAttributes, ReactNode } from 'react'

type Variant = 'primary' | 'secondary' | 'quiet' | 'danger'
type Size = 'compact' | 'default' | 'gloved'

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
}

type Props = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant
  /** Gloved is 48px: transport, lifecycle, macro Run, sign-in and bootstrap. */
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
