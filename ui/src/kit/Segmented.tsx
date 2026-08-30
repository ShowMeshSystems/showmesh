type Option<T extends string> = { value: T; label: string }

type Props<T extends string> = {
  label: string
  value: T
  options: readonly Option<T>[]
  onChange: (value: T) => void
  /** Every segment goes inert. `NotWired` sets this, so it must be honoured. */
  disabled?: boolean
}

/** Segments carry aria-pressed, so the selection is not colour alone. */
export function Segmented<T extends string>({ label, value, options, onChange, disabled = false }: Props<T>) {
  return (
    <div className="sm-segmented" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className="sm-segmented__item"
          aria-pressed={option.value === value}
          disabled={disabled}
          onClick={() => {
            if (!disabled) onChange(option.value)
          }}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
