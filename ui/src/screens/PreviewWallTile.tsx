import { useEffect, useRef, useState, type ReactNode } from 'react'
import { PreviewTile } from '../kit'
import type { PreviewTile as PreviewTileData } from '../api'
import { startFeed } from './previewFeed'

/** True while the element is in view and the page is showing. Without IntersectionObserver the element counts as in view. */
function useWatching(ref: React.RefObject<Element | null>): boolean {
  const [inView, setInView] = useState(typeof IntersectionObserver === 'undefined')
  const [pageShown, setPageShown] = useState(document.visibilityState !== 'hidden')

  useEffect(() => {
    const element = ref.current
    if (element === null || typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver((entries) => {
      const last = entries[entries.length - 1]
      if (last !== undefined) setInView(last.isIntersecting)
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [ref])

  useEffect(() => {
    const onChange = () => setPageShown(document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])

  return inView && pageShown
}

/** Whole seconds since `since`, recounted every second while `since` is set. */
function useSecondsSince(since: number | null): number | undefined {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (since === null) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [since])
  return since === null ? undefined : Math.max(0, Math.floor((now - since) / 1000))
}

const NO_PICTURE_STATES: ReadonlySet<PreviewTileData['state']> = new Set(['disabled', 'not_found', 'error'])

type Props = {
  tile: PreviewTileData
  /** False leaves the coordinator's sentence off the tile, for when the section already states it once. */
  withMessage?: boolean
  actions?: ReactNode
  handle?: ReactNode
  dragProps?: Partial<React.ComponentProps<typeof PreviewTile>>
}

/** A tile that connects its video only while it is on screen, and closes the connection when it leaves. */
export function PreviewWallTile({ tile, withMessage = true, actions, handle, dragProps }: Props) {
  const frameRef = useRef<HTMLDivElement>(null)
  const videoRef = useRef<HTMLVideoElement>(null)
  const watching = useWatching(frameRef)
  const [stream, setStream] = useState<MediaStream | null>(null)
  const [snapshot, setSnapshot] = useState<string | null>(null)
  const [lastFrameAt, setLastFrameAt] = useState<number | null>(null)
  const connectable = !NO_PICTURE_STATES.has(tile.state)

  useEffect(() => {
    if (!watching || !connectable) return
    const stop = startFeed(tile.key, { onStream: setStream, onSnapshot: setSnapshot, onFrame: setLastFrameAt })
    return () => {
      stop()
      setStream(null)
      setSnapshot(null)
      setLastFrameAt(null)
    }
  }, [tile.key, watching, connectable])

  useEffect(() => {
    if (videoRef.current !== null) videoRef.current.srcObject = stream
  }, [stream])

  const media =
    stream !== null ? (
      <video ref={videoRef} autoPlay muted playsInline aria-label={`${tile.label} live video`} />
    ) : snapshot !== null ? (
      <img src={snapshot} alt={`${tile.label} latest frame`} />
    ) : undefined
  const frameAge = useSecondsSince(media === undefined ? null : lastFrameAt)

  return (
    <PreviewTile
      label={tile.label}
      state={tile.state}
      message={withMessage ? tile.message : ''}
      media={media}
      frameAgeSeconds={frameAge}
      mediaRef={frameRef}
      actions={actions}
      handle={handle}
      {...dragProps}
    />
  )
}
