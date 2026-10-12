import { useEffect, useRef, type ReactNode } from 'react'

/** Holds banners under the top bar while the page scrolls, capped in height, and tells the rail how tall it is. */
export function StickyBanners({ children }: { children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = ref.current
    const shell = el?.closest<HTMLElement>('.sm-shell')
    if (el === null || shell === null || shell === undefined || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => shell.style.setProperty('--sticky-banner-h', `${el.offsetHeight}px`))
    observer.observe(el)
    return () => {
      observer.disconnect()
      shell.style.removeProperty('--sticky-banner-h')
    }
  }, [])
  return (
    <div ref={ref} className="sm-sticky-banners">
      {children}
    </div>
  )
}
