import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DISCONNECT_GRACE_MS, ICE_WAIT_MS, SNAPSHOT_INTERVAL_MS, TRACK_WAIT_MS, WEBRTC_RETRY_MS, startFeed, type FeedDeps } from './previewFeed'

class FakePeer {
  static all: FakePeer[] = []
  iceGatheringState: RTCIceGatheringState = 'complete'
  connectionState: RTCPeerConnectionState = 'new'
  localDescription: { sdp: string } | null = null
  ontrack: ((event: RTCTrackEvent) => void) | null = null
  onconnectionstatechange: (() => void) | null = null
  closed = false
  remote: { type: string; sdp: string } | null = null
  transceivers: Array<[string, unknown]> = []
  constructor() {
    FakePeer.all.push(this)
  }
  addTransceiver(kind: string, init: unknown) {
    this.transceivers.push([kind, init])
  }
  async createOffer() {
    return { type: 'offer' as const, sdp: 'offer-sdp' }
  }
  async setLocalDescription(description: { sdp: string }) {
    this.localDescription = description
  }
  async setRemoteDescription(description: { type: string; sdp: string }) {
    this.remote = description
  }
  addEventListener() {}
  close() {
    this.closed = true
  }
}

const blob = new Blob(['x'], { type: 'image/jpeg' })

function makeDeps(overrides: Partial<FeedDeps> = {}): FeedDeps & { revoked: string[]; frames: Array<() => void> } {
  const revoked: string[] = []
  const frames: Array<() => void> = []
  let n = 0
  return {
    createPeer: () => new FakePeer() as unknown as RTCPeerConnection,
    postOffer: vi.fn(async () => 'answer-sdp'),
    getSnapshot: vi.fn(async () => blob),
    createObjectURL: () => `blob:frame-${++n}`,
    revokeObjectURL: (url) => revoked.push(url),
    watchFrame: (_stream, onFrame) => {
      frames.push(onFrame)
      return () => {}
    },
    revoked,
    frames,
    ...overrides,
  }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakePeer.all = []
})
afterEach(() => vi.useRealTimers())

function connect(peer: FakePeer, stream: unknown = { id: 's' }) {
  peer.ontrack?.({ streams: [stream] } as unknown as RTCTrackEvent)
  peer.connectionState = 'connected'
  peer.onconnectionstatechange?.()
}

const handlers = () => ({ onStream: vi.fn(), onSnapshot: vi.fn() })

describe('preview feed', () => {
  it('offers a receive-only video connection and sets the answer', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('camera:driveway', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    const peer = FakePeer.all[0]!
    expect(peer.transceivers).toEqual([['video', { direction: 'recvonly' }]])
    expect(deps.postOffer).toHaveBeenCalledWith('camera:driveway', 'offer-sdp', expect.any(AbortSignal))
    expect(peer.remote).toEqual({ type: 'answer', sdp: 'answer-sdp' })
    expect(deps.getSnapshot).not.toHaveBeenCalled()
  })

  it('hands over the stream only once connected and a frame has decoded, and never polls', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    const stream = { id: 's' } as unknown as MediaStream
    connect(FakePeer.all[0]!, stream)
    expect(h.onStream).not.toHaveBeenCalled()
    deps.frames[0]!()
    expect(h.onStream).toHaveBeenCalledWith(stream)
    await vi.advanceTimersByTimeAsync(TRACK_WAIT_MS + SNAPSHOT_INTERVAL_MS)
    expect(deps.getSnapshot).not.toHaveBeenCalled()
  })

  it('hands over when the frame decodes before the connection reports connected', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    const peer = FakePeer.all[0]!
    peer.ontrack?.({ streams: [{ id: 's' }] } as unknown as RTCTrackEvent)
    deps.frames[0]!()
    expect(h.onStream).not.toHaveBeenCalled()
    peer.connectionState = 'connected'
    peer.onconnectionstatechange?.()
    expect(h.onStream).toHaveBeenCalledTimes(1)
  })

  it('keeps showing snapshots when a track arrives but UDP is blocked', async () => {
    const deps = makeDeps({ postOffer: vi.fn(async () => 'answer-sdp') })
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    FakePeer.all[0]!.ontrack?.({ streams: [{ id: 's' }] } as unknown as RTCTrackEvent)
    await vi.advanceTimersByTimeAsync(TRACK_WAIT_MS)
    expect(h.onStream).not.toHaveBeenCalledWith(expect.objectContaining({ id: 's' }))
    expect(FakePeer.all[0]!.closed).toBe(true)
    const calls = (deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length
    expect(calls).toBeGreaterThan(0)
    await vi.advanceTimersByTimeAsync(WEBRTC_RETRY_MS - TRACK_WAIT_MS - 1)
    expect((deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(calls)
    expect(h.onSnapshot).toHaveBeenLastCalledWith(expect.stringMatching(/^blob:/))
    expect(h.onSnapshot).not.toHaveBeenLastCalledWith(null)
  })

  it('keeps polling while the track is up but the connection has not connected', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    FakePeer.all[0]!.ontrack?.({ streams: [{}] } as unknown as RTCTrackEvent)
    deps.frames[0]!()
    await vi.advanceTimersByTimeAsync(TRACK_WAIT_MS)
    expect(h.onStream).not.toHaveBeenCalledWith(expect.anything())
    expect(deps.getSnapshot).toHaveBeenCalled()
  })

  it('falls back when the connection stays disconnected', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    const peer = FakePeer.all[0]!
    connect(peer)
    deps.frames[0]!()
    expect(h.onStream).toHaveBeenCalledTimes(1)
    peer.connectionState = 'disconnected'
    peer.onconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(DISCONNECT_GRACE_MS)
    expect(peer.closed).toBe(true)
    expect(h.onStream).toHaveBeenLastCalledWith(null)
    expect(deps.getSnapshot).toHaveBeenCalled()
  })

  it('does not fall back when a disconnect recovers within the grace period', async () => {
    const deps = makeDeps()
    startFeed('k', handlers(), deps)
    await vi.advanceTimersByTimeAsync(0)
    const peer = FakePeer.all[0]!
    connect(peer)
    deps.frames[0]!()
    peer.connectionState = 'disconnected'
    peer.onconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(DISCONNECT_GRACE_MS - 1)
    peer.connectionState = 'connected'
    peer.onconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(DISCONNECT_GRACE_MS * 2)
    expect(peer.closed).toBe(false)
  })

  it('runs one polling loop when a stale snapshot fetch lands after polling restarted', async () => {
    const stale: Array<(blob: Blob) => void> = []
    let calls = 0
    const getSnapshot = vi.fn(() => (++calls === 1 ? new Promise<Blob>((resolve) => stale.push(resolve)) : Promise.resolve(blob)))
    const postOffer = vi.fn().mockRejectedValueOnce(new Error('502')).mockResolvedValue('answer-sdp')
    const deps = makeDeps({ postOffer, getSnapshot })
    startFeed('k', handlers(), deps)
    await vi.advanceTimersByTimeAsync(WEBRTC_RETRY_MS)
    const second = FakePeer.all[1]!
    connect(second)
    deps.frames[0]!()
    second.connectionState = 'failed'
    second.onconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(0)
    stale[0]!(blob)
    await vi.advanceTimersByTimeAsync(0)
    const before = getSnapshot.mock.calls.length
    await vi.advanceTimersByTimeAsync(SNAPSHOT_INTERVAL_MS * 4)
    expect(getSnapshot.mock.calls.length - before).toBe(4)
  })

  it('falls back to polling the snapshot every 2 s when the offer is refused', async () => {
    const deps = makeDeps({ postOffer: vi.fn(async () => { throw new Error('502') }) })
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    expect(FakePeer.all[0]!.closed).toBe(true)
    expect(h.onStream).toHaveBeenLastCalledWith(null)
    expect(deps.getSnapshot).toHaveBeenCalledTimes(1)
    expect(h.onSnapshot).toHaveBeenLastCalledWith('blob:frame-1')
    await vi.advanceTimersByTimeAsync(SNAPSHOT_INTERVAL_MS)
    expect(deps.getSnapshot).toHaveBeenCalledTimes(2)
    expect(deps.revoked).toEqual(['blob:frame-1'])
  })

  it('falls back when no picture arrives in time', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(TRACK_WAIT_MS)
    expect(FakePeer.all[0]!.closed).toBe(true)
    expect(deps.getSnapshot).toHaveBeenCalled()
  })

  it('falls back when the connection fails', async () => {
    const deps = makeDeps()
    startFeed('k', handlers(), deps)
    await vi.advanceTimersByTimeAsync(0)
    const peer = FakePeer.all[0]!
    peer.connectionState = 'failed'
    peer.onconnectionstatechange?.()
    await vi.advanceTimersByTimeAsync(0)
    expect(deps.getSnapshot).toHaveBeenCalled()
  })

  it('polls straight away when the browser has no WebRTC', async () => {
    const deps = makeDeps({ createPeer: null })
    startFeed('k', handlers(), deps)
    await vi.advanceTimersByTimeAsync(0)
    expect(FakePeer.all).toHaveLength(0)
    expect(deps.getSnapshot).toHaveBeenCalledTimes(1)
  })

  it('keeps the last frame when a poll fails', async () => {
    const getSnapshot = vi.fn().mockResolvedValueOnce(blob).mockRejectedValue(new Error('down'))
    const deps = makeDeps({ createPeer: null, getSnapshot })
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(SNAPSHOT_INTERVAL_MS * 2)
    expect(getSnapshot).toHaveBeenCalledTimes(3)
    expect(h.onSnapshot).toHaveBeenCalledTimes(1)
    expect(deps.revoked).toEqual([])
  })

  it('tries WebRTC again while polling and stops polling once video arrives', async () => {
    const postOffer = vi.fn().mockRejectedValueOnce(new Error('502')).mockResolvedValue('answer-sdp')
    const deps = makeDeps({ postOffer })
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(WEBRTC_RETRY_MS)
    expect(FakePeer.all).toHaveLength(2)
    connect(FakePeer.all[1]!, {})
    deps.frames[0]!()
    expect(h.onSnapshot).toHaveBeenLastCalledWith(null)
    const calls = (deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length
    await vi.advanceTimersByTimeAsync(SNAPSHOT_INTERVAL_MS * 3)
    expect((deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length).toBe(calls)
  })

  it('closes the connection, the timers and the frame when stopped', async () => {
    const deps = makeDeps({ postOffer: vi.fn(async () => { throw new Error('x') }) })
    const stop = startFeed('k', handlers(), deps)
    await vi.advanceTimersByTimeAsync(0)
    stop()
    const calls = (deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length
    await vi.advanceTimersByTimeAsync(WEBRTC_RETRY_MS + ICE_WAIT_MS)
    expect((deps.getSnapshot as ReturnType<typeof vi.fn>).mock.calls.length).toBe(calls)
    expect(FakePeer.all).toHaveLength(1)
    expect(deps.revoked).toContain('blob:frame-1')
  })
})
