import { useState } from 'react'
import { RuledStrip, Segmented } from '../kit'
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

      <RuledStrip
        absence="unavailable"
        label="Local only"
        fact="Everything on this page is stored in this browser."
        detail="It creates no coordinator revision, is not attributed to you, and no one else sees it, which is why it has no Save button."
      />

      <section aria-labelledby="st-theme" className="sm-section">
        <h3 id="st-theme" className="sm-eyebrow">Theme</h3>
        <Segmented
          label="Theme"
          value={baseTheme}
          options={BASE_OPTIONS}
          onChange={(value) => {
            setBaseTheme(value)
            setTheme(value)
          }}
        />
        <p className="sm-small sm-muted sm-stack-3">Dark is the show-time default. Light is for daylight setup work.</p>
      </section>

      <section aria-labelledby="st-hc" className="sm-section">
        <h3 id="st-hc" className="sm-eyebrow">High contrast</h3>
        <label className="sm-panel sm-inline-row sm-stack-3">
          <input
            type="checkbox"
            checked={highContrast}
            onChange={(e) => {
              setTheme(e.target.checked ? 'contrast' : baseTheme)
            }}
          />
          <span>
            <span className="sm-body sm-flat" style={{ fontWeight: 500 }}>Maximum legibility mode</span>
            <span className="sm-small sm-muted" style={{ display: 'block', marginTop: 5 }}>
              Pure black, pure white, saturated status colours, thicker borders. For outdoors at night, in the cold,
              with gloves. Turn it on deliberately, not because a system setting inferred it.
            </span>
          </span>
        </label>
      </section>
    </>
  )
}
