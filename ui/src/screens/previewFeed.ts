import { getPreviewSnapshot, postPreviewOffer } from '../api'

export const SNAPSHOT_INTERVAL_MS = 2000
export const TRACK_WAIT_MS = 8000
export const ICE_WAIT_MS = 2000
export const WEBRTC_RETRY_MS = 30000
export const DISCONNECT_GRACE_MS = 5000

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
  /** Calls onFrame once the stream has decoded a picture. Returns the function that cancels the wait. */
  watchFrame: (stream: MediaStream, onFrame: () => void) => () => void
}

function watchFrameInVideo(stream: MediaStream, onFrame: () => void): () => void {
  const video = document.createElement('video')
  video.muted = true
  video.playsInline = true
  const done = () => {
    cancel()
    onFrame()
  }
  const cancel = () => {
    video.removeEventListener('loadeddata', done)
    video.removeEventListener('playing', done)
    video.srcObject = null
  }
  video.addEventListener('loadeddata', done)
  video.addEventListener('playing', done)
  video.srcObject = stream
  void video.play().catch(() => {})
  return cancel
}

export const defaultFeedDeps: FeedDeps = {
  createPeer: typeof RTCPeerConnection === 'undefined' ? null : () => new RTCPeerConnection({ iceServers: [] }),
  postOffer: postPreviewOffer,
  getSnapshot: getPreviewSnapshot,
  createObjectURL: (blob) => URL.createObjectURL(blob),
  revokeObjectURL: (url) => URL.revokeObjectURL(url),
  watchFrame: watchFrameInVideo,
}

/**
 * Feeds one tile: WebRTC first, and snapshot polling every 2 s whenever WebRTC
 * fails or sends no decoded picture. While polling it tries WebRTC again every 30 s.
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
  let pollGen = 0
  let cancelFrameWatch: (() => void) | undefined
  let disconnectTimer: ReturnType<typeof setTimeout> | undefined

  const closePeer = () => {
    clearTimeout(trackTimer)
    clearTimeout(disconnectTimer)
    cancelFrameWatch?.()
    cancelFrameWatch = undefined
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

  const poll = async (gen: number) => {
    try {
      const blob = await deps.getSnapshot(key, abort.signal)
      if (stopped || gen !== pollGen) return
      dropSnapshot()
      snapshotUrl = deps.createObjectURL(blob)
      handlers.onSnapshot(snapshotUrl)
    } catch {
      // A failed poll keeps the last frame on screen; the tile's state word reports the source.
    }
    if (!stopped && gen === pollGen) pollTimer = setTimeout(() => void poll(gen), SNAPSHOT_INTERVAL_MS)
  }

  const startPolling = () => {
    if (polling || stopped) return
    polling = true
    void poll(++pollGen)
  }

  const stopPolling = () => {
    polling = false
    pollGen++
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
      let frameSeen = false
      let stream: MediaStream | null = null
      const deliver = () => {
        if (stopped || peer !== pc || stream === null || !frameSeen || pc.connectionState !== 'connected') return
        clearTimeout(trackTimer)
        clearTimeout(retryTimer)
        stopPolling()
        handlers.onStream(stream)
      }
      pc.ontrack = (event) => {
        if (stopped || peer !== pc || stream !== null) return
        stream = event.streams[0] ?? new MediaStream([event.track])
        cancelFrameWatch = deps.watchFrame(stream, () => {
          frameSeen = true
          deliver()
        })
      }
      pc.onconnectionstatechange = () => {
        if (peer !== pc) return
        const state = pc.connectionState
        if (state === 'failed' || state === 'closed') return fallback()
        clearTimeout(disconnectTimer)
        if (state === 'connected') deliver()
        else if (state === 'disconnected') disconnectTimer = setTimeout(() => peer === pc && fallback(), DISCONNECT_GRACE_MS)
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
