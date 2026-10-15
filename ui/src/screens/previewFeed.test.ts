import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ICE_WAIT_MS, SNAPSHOT_INTERVAL_MS, TRACK_WAIT_MS, WEBRTC_RETRY_MS, startFeed, type FeedDeps } from './previewFeed'

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

function makeDeps(overrides: Partial<FeedDeps> = {}): FeedDeps & { revoked: string[] } {
  const revoked: string[] = []
  let n = 0
  return {
    createPeer: () => new FakePeer() as unknown as RTCPeerConnection,
    postOffer: vi.fn(async () => 'answer-sdp'),
    getSnapshot: vi.fn(async () => blob),
    createObjectURL: () => `blob:frame-${++n}`,
    revokeObjectURL: (url) => revoked.push(url),
    revoked,
    ...overrides,
  }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakePeer.all = []
})
afterEach(() => vi.useRealTimers())

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

  it('hands over the stream when a track arrives and never polls', async () => {
    const deps = makeDeps()
    const h = handlers()
    startFeed('k', h, deps)
    await vi.advanceTimersByTimeAsync(0)
    const stream = { id: 's' } as unknown as MediaStream
    FakePeer.all[0]!.ontrack?.({ streams: [stream] } as unknown as RTCTrackEvent)
    expect(h.onStream).toHaveBeenCalledWith(stream)
    await vi.advanceTimersByTimeAsync(TRACK_WAIT_MS + SNAPSHOT_INTERVAL_MS)
    expect(deps.getSnapshot).not.toHaveBeenCalled()
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
    FakePeer.all[1]!.ontrack?.({ streams: [{}] } as unknown as RTCTrackEvent)
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
