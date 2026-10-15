import { getPreviewSnapshot, postPreviewOffer } from '../api'

export const SNAPSHOT_INTERVAL_MS = 2000
export const TRACK_WAIT_MS = 8000
export const ICE_WAIT_MS = 2000
export const WEBRTC_RETRY_MS = 30000

export type FeedHandlers = {
  /** The live video stream, or null when there is none. */
  onStream: (stream: MediaStream | null) => void
  /** The latest snapshot's object URL, or null when there is none. */
  onSnapshot: (url: string | null) => void
}

export type FeedDeps = {
  createPeer: (() => RTCPeerConnection) | null
  postOffer: (key: string, sdp: string, signal: AbortSignal) => Promise<string>
  getSnapshot: (key: string, signal: AbortSignal) => Promise<Blob>
  createObjectURL: (blob: Blob) => string
  revokeObjectURL: (url: string) => void
}

export const defaultFeedDeps: FeedDeps = {
  createPeer: typeof RTCPeerConnection === 'undefined' ? null : () => new RTCPeerConnection({ iceServers: [] }),
  postOffer: postPreviewOffer,
  getSnapshot: getPreviewSnapshot,
  createObjectURL: (blob) => URL.createObjectURL(blob),
  revokeObjectURL: (url) => URL.revokeObjectURL(url),
}

/**
 * Feeds one tile: WebRTC first, and snapshot polling every 2 s whenever WebRTC
 * fails or sends no picture. While polling it tries WebRTC again every 30 s.
 * Returns the function that closes everything.
 */
export function startFeed(key: string, handlers: FeedHandlers, deps: FeedDeps = defaultFeedDeps): () => void {
  const abort = new AbortController()
  let stopped = false
  let peer: RTCPeerConnection | null = null
  let polling = false
  let pollTimer: ReturnType<typeof setTimeout> | undefined
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let trackTimer: ReturnType<typeof setTimeout> | undefined
  let snapshotUrl: string | null = null

  const closePeer = () => {
    clearTimeout(trackTimer)
    if (peer !== null) {
      peer.ontrack = null
      peer.onconnectionstatechange = null
      peer.close()
      peer = null
    }
  }

  const dropSnapshot = () => {
    if (snapshotUrl !== null) deps.revokeObjectURL(snapshotUrl)
    snapshotUrl = null
  }

  const poll = async () => {
    try {
      const blob = await deps.getSnapshot(key, abort.signal)
      if (stopped || !polling) return
      dropSnapshot()
      snapshotUrl = deps.createObjectURL(blob)
      handlers.onSnapshot(snapshotUrl)
    } catch {
      // A failed poll keeps the last frame on screen; the tile's state word reports the source.
    }
    if (!stopped && polling) pollTimer = setTimeout(poll, SNAPSHOT_INTERVAL_MS)
  }

  const startPolling = () => {
    if (polling || stopped) return
    polling = true
    void poll()
  }

  const stopPolling = () => {
    polling = false
    clearTimeout(pollTimer)
    dropSnapshot()
    handlers.onSnapshot(null)
  }

  const fallback = () => {
    if (stopped) return
    closePeer()
    handlers.onStream(null)
    startPolling()
    clearTimeout(retryTimer)
    retryTimer = setTimeout(() => void connect(), WEBRTC_RETRY_MS)
  }

  const gathered = (pc: RTCPeerConnection) =>
    new Promise<void>((resolve) => {
      if (pc.iceGatheringState === 'complete') return resolve()
      const timer = setTimeout(resolve, ICE_WAIT_MS)
      pc.addEventListener('icegatheringstatechange', () => {
        if (pc.iceGatheringState !== 'complete') return
        clearTimeout(timer)
        resolve()
      })
    })

  const connect = async () => {
    if (stopped) return
    if (deps.createPeer === null) return fallback()
    try {
      closePeer()
      const pc = deps.createPeer()
      peer = pc
      pc.addTransceiver('video', { direction: 'recvonly' })
      pc.ontrack = (event) => {
        if (stopped || peer !== pc) return
        clearTimeout(trackTimer)
        clearTimeout(retryTimer)
        stopPolling()
        handlers.onStream(event.streams[0] ?? new MediaStream([event.track]))
      }
      pc.onconnectionstatechange = () => {
        if (peer === pc && (pc.connectionState === 'failed' || pc.connectionState === 'closed')) fallback()
      }
      trackTimer = setTimeout(() => peer === pc && fallback(), TRACK_WAIT_MS)
      await pc.setLocalDescription(await pc.createOffer())
      await gathered(pc)
      const sdp = pc.localDescription?.sdp
      if (sdp === undefined) throw new Error('no offer')
      const answer = await deps.postOffer(key, sdp, abort.signal)
      if (stopped || peer !== pc) return
      await pc.setRemoteDescription({ type: 'answer', sdp: answer })
    } catch {
      fallback()
    }
  }

  void connect()

  return () => {
    stopped = true
    abort.abort()
    clearTimeout(pollTimer)
    clearTimeout(retryTimer)
    closePeer()
    dropSnapshot()
  }
}
