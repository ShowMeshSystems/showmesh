import { useCallback, useEffect, useState } from 'react'

// Show-time high-contrast mode (spec section 6.6): a custom-property swap
// on a root attribute (styles/tokens.css's [data-contrast="high"] block),
// persisted in localStorage because it is an operator preference, not a
// secret -- unlike the API token, which spec section 5.6 requires in
// sessionStorage specifically so it does not outlive the tab.
const STORAGE_KEY = 'showmesh-ui-contrast'

function readStoredPreference(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) === 'high'
  } catch {
    // Storage can throw in a locked-down browser context; default to the
    // normal-contrast theme rather than failing to render at all.
    return false
  }
}

export function useHighContrast(): [boolean, () => void] {
  const [highContrast, setHighContrast] = useState<boolean>(readStoredPreference)

  useEffect(() => {
    document.documentElement.setAttribute('data-contrast', highContrast ? 'high' : 'normal')
    try {
      window.localStorage.setItem(STORAGE_KEY, highContrast ? 'high' : 'normal')
    } catch {
      // Best effort; the in-memory state still governs this session.
    }
  }, [highContrast])

  const toggle = useCallback(() => setHighContrast((value) => !value), [])

  return [highContrast, toggle]
}
