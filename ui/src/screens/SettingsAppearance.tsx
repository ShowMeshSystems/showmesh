import { useState } from 'react'
import { Choice, Section, Segmented } from '../kit'
import { useTheme } from '../app/useTheme'

type BaseTheme = 'system' | 'dark' | 'light'
const BASE_OPTIONS: readonly { value: BaseTheme; label: string }[] = [
  { value: 'system', label: 'Follow the system' },
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
]

/**
 * useTheme stores one of four values, not an orthogonal high-contrast flag
 * on top of dark/light/system. This keeps the last non-contrast choice in
 * state so the segmented control has something to show while contrast is on.
 */
export function SettingsAppearance() {
  const [theme, setTheme] = useTheme()
  const [baseTheme, setBaseTheme] = useState<BaseTheme>(theme === 'contrast' ? 'system' : theme)
  const highContrast = theme === 'contrast'

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Appearance</p>
      <h2 className="sm-section__title">How this browser looks</h2>

      <Section id="st-theme" title="Theme">
        <Segmented
          label="Theme"
          value={baseTheme}
          options={BASE_OPTIONS}
          onChange={(value) => {
            setBaseTheme(value)
            setTheme(value)
          }}
        />
      </Section>

      <Section id="st-hc" title="High contrast">
        <Choice
          type="checkbox"
          checked={highContrast}
          onChange={(e) => {
            setTheme(e.target.checked ? 'contrast' : baseTheme)
          }}
          label="Maximum legibility mode"
        />
      </Section>
    </>
  )
}
