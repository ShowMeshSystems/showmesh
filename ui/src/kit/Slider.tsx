import type { InputHTMLAttributes } from 'react'
import { useId } from 'react'

type SliderProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'value'> & {
  label: string
  value: number
  min: number
  max: number
  /** The value shown beside the label, e.g. "42%". Defaults to the raw number. */
  valueLabel?: string
  /** No reading exists yet: shows "Unknown", hides the thumb and disables the control. */
  unknown?: boolean
}

/** A single 0-100-style range control with the value it carries always readable beside its label. */
export function Slider({ label, value, min, max, valueLabel, unknown = false, className, ...props }: SliderProps) {
  const id = useId()
  return (
    <div className="sm-field">
      <div className="sm-slider__head">
        <label className="sm-field__label" htmlFor={id}>{label}</label>
        <span className="sm-data">{unknown ? 'Unknown' : (valueLabel ?? value)}</span>
      </div>
      <input
        id={id}
        type="range"
        className={['sm-slider', unknown && 'sm-slider--unknown', className].filter(Boolean).join(' ')}
        value={value}
        min={min}
        max={max}
        {...props}
        disabled={unknown || props.disabled}
        aria-valuetext={unknown ? 'Unknown' : props['aria-valuetext']}
      />
    </div>
  )
}
