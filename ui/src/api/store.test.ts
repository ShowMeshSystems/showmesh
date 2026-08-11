import { afterEach, describe, expect, it } from 'vitest'
import { ApiStore } from './store'
import { getStoredToken } from './token'
import { FakeClock } from './test-support/fake-clock'
import {
  openSSE,
  sleepMs,
  startTestServer,
  waitFor,
  writeSSEComment,
  writeSSEFrame,
  type TestServer,
} from './test-support/test-server'
import {
  makeEvent,
  makeEventsResponse,
  makeFPPInstance,
  makeNode,
  makeProblem,
  makeSnapshot,
} from './test-support/fixtures'

// A fast backoff schedule for tests only (spec section 5.4 allows this:
// it is a timing knob on our own client, not a mock of the transport —
// every byte still crosses a real socket to a real node:http server).
// Production code always uses backoff.ts's DEFAULT_BACKOFF.
const FAST_BACKOFF = { baseMs: 15, factor: 2, capMs: 100 }

const openedStores: ApiStore[] = []
const openedServers: TestServer[] = []

function makeStore(
  baseUrl: string,
  overrides: Partial<ConstructorParameters<typeof ApiStore>[0]> = {},
): ApiStore {
  const store = new ApiStore({ baseUrl, backoff: FAST_BACKOFF, ...overrides })
  openedStores.push(store)
  return store
}

async function server(
  handler: Parameters<typeof startTestServer>[0],
): Promise<TestServer> {
  const s = await startTestServer(handler)
  openedServers.push(s)
  return s
}

afterEach(async () => {
  for (const store of openedStores.splice(0)) store.dispose()
  for (const s of openedServers.splice(0)) await s.close()
  sessionStorage.clear()
})

function respondJson(res: import('node:http').ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/json', 'ShowMesh-API-Version': '1' })
  res.end(JSON.stringify(body))
}

function respondProblem(res: import('node:http').ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/problem+json', 'ShowMesh-API-Version': '1' })
  res.end(JSON.stringify(body))
}

describe('ApiStore: snapshot-before-delta ordering (case 1)', () => {
  it('never applies a stream delta before the snapshot fetch has completed and been applied', async () => {
    // The /snapshot response is deliberately delayed until AFTER the
    // node.changed frame has already been written to the stream. This
    // test's earlier version only checked FINAL contents, which stays
    // green even under the bug it names: eager delta application PLUS a
    // merging (rather than replacing) applySnapshot produces the exact
    // same final ['n-delta', 'n0'] either way. What actually
    // distinguishes "hold the delta back until the snapshot resolves and
    // is applied" from "apply it eagerly" is the state at a point in
    // time strictly between the two — so this test now samples the
    // model mid-flight, while the /snapshot response is still
    // deliberately withheld, and requires "n-delta" to be absent there.
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          writeSSEFrame(res, 'node.changed', {
            serverTime: new Date().toISOString(),
            node: makeNode('n-delta'),
          })
        }, 20)
        return
      }
      if (req.url === '/snapshot') {
        setTimeout(() => {
          respondJson(res, 200, makeSnapshot({ nodes: [makeNode('n0')] }))
        }, 80) // resolves well after the node.changed frame was written
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    // At 50ms: the node.changed frame (written at 20ms) has certainly
    // arrived and been parsed; the /snapshot response (withheld until
    // 80ms) certainly has not resolved. The ordering claim this test is
    // named for is exactly this: nothing from the delta may be visible
    // yet.
    await sleepMs(50)
    expect(store.getSnapshot().connection.kind).not.toBe('live')
    expect(store.getSnapshot().nodes).toEqual([])

    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never reached live',
    })

    const nodeIds = store.getSnapshot().nodes.map((n) => n.nodeId)
    // Compared against the SAME collation the production sort uses
    // (String.localeCompare, via compareByNodeId in store.ts) rather
    // than a bare Array.sort(), which is a UTF-16 code-unit sort — the
    // two agree for these particular strings only by coincidence, not
    // because they are the same algorithm.
    expect(nodeIds).toEqual(['n-delta', 'n0'].sort((a, b) => a.localeCompare(b)))
    expect(nodeIds).toContain('n0') // the snapshot's own node — proves it was applied at all
  })
})

describe('ApiStore: interruption always re-snapshots (cases 2 and 3)', () => {
  it('a mid-stream connection drop produces a full resnapshot on reconnect, not a resumed model', async () => {
    let streamAttempt = 0
    let snapshotAttempt = 0

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        const thisAttempt = streamAttempt
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: `s${thisAttempt}`,
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        if (thisAttempt === 1) {
          // After giving the first snapshot/events fetch time to land,
          // kill the connection outright with no closing frame at all —
          // exactly what api/openapi.yaml says a client must treat the
          // same as an orderly shutdown or a network fault.
          setTimeout(() => {
            req.socket.destroy()
          }, 60)
        }
        // Second attempt: just stays open; the test only cares that it reconnected and re-snapshotted.
        return
      }
      if (req.url === '/snapshot') {
        snapshotAttempt += 1
        const nodes = snapshotAttempt === 1 ? [makeNode('n-first')] : [makeNode('n-second')]
        respondJson(res, 200, makeSnapshot({ nodes }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n-first'), {
      message: 'first snapshot never applied',
    })
    await waitFor(() => streamAttempt >= 2, { message: 'store never reconnected the stream' })
    await waitFor(() => snapshotAttempt >= 2, { message: 'store never re-fetched the snapshot' })
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never returned to live after reconnect',
    })

    // The whole nodes array is replaced wholesale by the fresh snapshot
    // — "n-first" from the dead connection's snapshot must be gone, not
    // merged with "n-second".
    const nodeIds = store.getSnapshot().nodes.map((n) => n.nodeId)
    expect(nodeIds).toEqual(['n-second'])

    // EventSource's automatic Last-Event-ID resume is exactly what this
    // client must never do (ADR-020 decision 3) — confirm no such
    // header was ever sent on the reconnect.
    const secondStreamReq = s.requestsFor('/stream')[1]
    expect(secondStreamReq?.headers['last-event-id']).toBeUndefined()
  })

  it('stream.reset produces a full resnapshot without waiting for the connection to drop', async () => {
    let snapshotAttempt = 0
    // `as` rather than a `: T | null` annotation: with the annotation,
    // this TypeScript version does not treat the closure's `streamRes =
    // res` below as narrowing the declared type at this scope, and every
    // read after the `if (streamRes === null) throw` guard below
    // silently types as `never` (a real repro, kept out of this comment
    // only because it belongs in a bug tracker, not test code).
    let streamRes = null as import('node:http').ServerResponse | null

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRes = res
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        snapshotAttempt += 1
        const nodes = snapshotAttempt === 1 ? [makeNode('n-before-reset')] : [makeNode('n-after-reset')]
        respondJson(res, 200, makeSnapshot({ nodes }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-before-reset'])

    // Now push a stream.reset on the SAME still-open connection — no
    // socket drop at all — and confirm the store re-snapshots anyway.
    if (streamRes === null) throw new Error('no open /stream response captured')
    writeSSEFrame(streamRes, 'stream.reset', {
      seq: 1,
      serverTime: new Date().toISOString(),
      reason: 'subscriber_too_slow',
      snapshotRequired: true,
    })

    await waitFor(() => snapshotAttempt >= 2, { message: 'stream.reset did not trigger a resnapshot' })
    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n-after-reset'))
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-after-reset'])
  })
})

describe('ApiStore: unknown frames and fields are ignored, not errors (case 4)', () => {
  it('ignores an unknown event: name and an unknown JSON field without crashing or corrupting the model', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          // An event: name not in api/openapi.yaml's /stream table.
          writeSSEFrame(res, 'projector.warmed_up', { anything: 'goes here' })
        }, 20)
        setTimeout(() => {
          // A recognized event carrying an extra field no schema names.
          writeSSEFrame(res, 'node.changed', {
            serverTime: new Date().toISOString(),
            node: makeNode('n1'),
            somethingFutureFieldNoClientKnowsAboutYet: { nested: true },
          })
        }, 40)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n1'), {
      message: 'the node.changed frame after the unknown one was never applied',
    })

    expect(store.getSnapshot().connection.kind).toBe('live')
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n1'])
  })
})

describe('ApiStore: keepalive comments are inert (case 5)', () => {
  // T2 fix: this test's original name claimed to pin the parser's own
  // handling of ": keepalive". It doesn't, and structurally can't from
  // here — a parser bug that turned ": keepalive" into a dispatched
  // frame (e.g. a "message"-typed frame with data " keepalive") would
  // ALSO leave this test green, because handleFrame's switch (store.ts)
  // silently ignores any event: name it doesn't recognize, which
  // "message" isn't. So this test only pins the store-level claim: IF a
  // comment produces no frame (a claim owned by sse.test.ts, see its
  // "does not leak comment content as data" case), THEN the store makes
  // no update from it. Narrowed name reflects that split of ownership.
  it('": keepalive" produces no model change and no listener notification at the store level', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        const interval = setInterval(() => writeSSEComment(res, 'keepalive'), 15)
        res.on('close', () => clearInterval(interval))
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ nodes: [makeNode('n0')] }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    let notifications = 0
    store.subscribe(() => {
      notifications += 1
    })
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    const countAfterLive = notifications
    const modelAfterLive = store.getSnapshot()

    // Let several keepalive comments go by.
    await sleepMs(80)

    expect(notifications).toBe(countAfterLive)
    expect(store.getSnapshot()).toBe(modelAfterLive) // same reference: no update was ever applied
  })
})

describe('ApiStore: a stream frame split across chunk boundaries (case 6)', () => {
  it('parses a node.changed frame written across three separate socket writes as exactly one update', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        void (async () => {
          const whole = `event: node.changed\ndata: ${JSON.stringify({
            serverTime: new Date().toISOString(),
            node: makeNode('n-chunked'),
          })}\n\n`
          const bytes = Buffer.from(whole, 'utf-8')
          const cut1 = Math.floor(bytes.length / 3)
          const cut2 = Math.floor((bytes.length * 2) / 3)
          res.write(bytes.subarray(0, cut1))
          await sleepMs(15)
          res.write(bytes.subarray(cut1, cut2))
          await sleepMs(15)
          res.write(bytes.subarray(cut2))
        })()
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    await waitFor(() => store.getSnapshot().nodes.length > 0, {
      message: 'the chunked node.changed frame never arrived',
    })

    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-chunked'])
  })
})

describe('ApiStore: authentication (case 7)', () => {
  it('reaches unauthorized without a retry storm, sends a supplied token, and clears a rejected one', async () => {
    let streamRequests = 0
    let currentToken: string | null = null

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRequests += 1
        const auth = req.headers.authorization
        if (currentToken === null) {
          respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'missing token' }))
          return
        }
        if (auth !== `Bearer ${currentToken}`) {
          respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'bad token' }))
          return
        }
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'unauthorized')
    expect(store.getSnapshot().connection).toEqual({ kind: 'unauthorized', reason: 'missing' })

    // No backoff retry storm: wait several multiples of the (already
    // fast, test-only) backoff base and confirm nothing was retried
    // automatically — the ONLY thing that should produce another
    // request is a deliberate submitToken/clearToken call.
    await sleepMs(FAST_BACKOFF.baseMs * 6)
    expect(streamRequests).toBe(1)

    // The coordinator is configured to require this exact token from here on.
    currentToken = 'right-secret'

    // Supply the WRONG token first, to prove a rejected token is
    // distinguishable from a merely-missing one.
    store.submitToken('wrong-secret')
    await waitFor(() => streamRequests >= 2)
    const secondReq = s.requestsFor('/stream')[1]
    expect(secondReq?.headers.authorization).toBe('Bearer wrong-secret')

    await waitFor(() => store.getSnapshot().connection.kind === 'unauthorized' &&
      (store.getSnapshot().connection as { reason: string }).reason === 'rejected')
    expect(getStoredToken()).toBeNull() // the rejected token must have been cleared

    // Now the right one.
    store.submitToken('right-secret')
    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    const thirdReq = s.requestsFor('/stream')[2]
    expect(thirdReq?.headers.authorization).toBe('Bearer right-secret')
  })
})

describe('ApiStore: incompatible API version is terminal (case 8)', () => {
  it('stops after the unsupported-api-version problem and issues no further requests', async () => {
    let streamRequests = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRequests += 1
        respondProblem(
          res,
          400,
          makeProblem({
            type: 'https://showmesh.dev/problems/unsupported-api-version',
            status: 400,
            detail: 'this coordinator serves version 2',
            supportedVersions: [2],
          }),
        )
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'incompatible')
    expect(store.getSnapshot().connection).toMatchObject({
      kind: 'incompatible',
      requiredVersion: 1,
      supportedVersions: [2],
    })

    await sleepMs(FAST_BACKOFF.baseMs * 6)
    expect(streamRequests).toBe(1) // terminal: never retried
  })
})

describe('ApiStore: observedAt: null survives into the model (case 9)', () => {
  it('never substitutes a fallback timestamp for a null observedAt', async () => {
    const nodeWithUnknownAge = makeNode('n-unknown-age', {
      evidence: {
        hello: {
          signal: 'node.hello',
          value: true,
          unit: null,
          state: 'unknown_age',
          reason: 'retained MQTT delivery replayed on reconnect',
          observedAt: null,
          collectedAt: '2026-08-11T12:00:00.000Z',
          source: 'mqtt',
          quality: 'direct',
          validForSeconds: null,
        },
        lastWill: {
          signal: 'node.lastWill',
          value: null,
          unit: null,
          state: 'not_collected',
          reason: 'no last will observed yet',
          observedAt: null,
          collectedAt: null,
          source: 'mqtt',
          quality: 'direct',
          validForSeconds: null,
        },
        heartbeat: {
          signal: 'node.heartbeat',
          value: true,
          unit: null,
          state: 'current',
          reason: null,
          observedAt: '2026-08-11T12:00:00.000Z',
          collectedAt: '2026-08-11T12:00:00.000Z',
          source: 'mqtt',
          quality: 'direct',
          validForSeconds: 30,
        },
      },
    })

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ nodes: [nodeWithUnknownAge] }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    const node = store.getSnapshot().nodes.find((n) => n.nodeId === 'n-unknown-age')
    expect(node).toBeDefined()
    expect(node?.evidence.hello.observedAt).toBeNull()
    expect(node?.evidence.hello.state).toBe('unknown_age')
    // The genuine absence state's own null stays null too.
    expect(node?.evidence.lastWill.observedAt).toBeNull()
    expect(node?.evidence.lastWill.collectedAt).toBeNull()
  })
})

describe('ApiStore: supplementary coverage', () => {
  it('applies initial event history newest-first, dedupes a covered event.recorded frame, and still applies a genuinely new one (T1)', async () => {
    // Frames are written on demand from the test body (after each
    // `waitFor` checkpoint) rather than on fixed timers measured from
    // when /stream opened, so this isn't racing against however long
    // the snapshot/events round trip actually takes on a given machine.
    // `as` rather than a `: T | null` annotation: with the annotation,
    // this TypeScript version does not treat the closure's `streamRes =
    // res` below as narrowing the declared type at this scope, and every
    // read after the `if (streamRes === null) throw` guard below
    // silently types as `never` (a real repro, kept out of this comment
    // only because it belongs in a bug tracker, not test code).
    let streamRes = null as import('node:http').ServerResponse | null
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRes = res
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ latestEventSeq: 2 }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(
          res,
          200,
          makeEventsResponse({
            events: [makeEvent(1), makeEvent(2)],
            latestSeq: 2,
            gap: true,
            oldestRetainedSeq: 1,
          }),
        )
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(store.getSnapshot().events.map((e) => e.seq)).toEqual([2, 1]) // newest first
    expect(store.getSnapshot().eventsGap).toBe(true)
    expect(store.getSnapshot().oldestRetainedSeq).toBe(1)

    if (streamRes === null) throw new Error('no open /stream response captured')

    // Same seq as one already in the initial /events page — must not duplicate.
    writeSSEFrame(streamRes, 'event.recorded', {
      serverTime: new Date().toISOString(),
      event: makeEvent(2, { summary: 'duplicate of seq 2' }),
    })
    await sleepMs(30)
    // Still exactly two events: the duplicate event.recorded(seq: 2) must not have been added again.
    expect(store.getSnapshot().events.map((e) => e.seq)).toEqual([2, 1])

    // A genuinely NEW seq, not covered by the initial /events page. This
    // is the assertion that actually exercises live event delivery: the
    // dedupe check above ("the duplicate does not appear twice") stays
    // true even if the event.recorded handler were deleted outright,
    // since an absent duplicate is also what "no live delivery at all"
    // looks like. Only this seq-3 arrival distinguishes "live delivery
    // works" from "live delivery doesn't exist."
    writeSSEFrame(streamRes, 'event.recorded', {
      serverTime: new Date().toISOString(),
      event: makeEvent(3, { summary: 'genuinely new' }),
    })
    await waitFor(() => store.getSnapshot().events.some((e) => e.seq === 3), {
      message: 'a genuinely new event.recorded frame was never applied to the model',
    })
    expect(store.getSnapshot().events.map((e) => e.seq)).toEqual([3, 2, 1])
  })

  it('reconciles a re-snapshot with events already held live, instead of discarding them (D1)', async () => {
    // The initial /events page covers only seq 1. A live event.recorded
    // for seq 2 arrives afterward. The connection is then killed and
    // reconnects; on reconnect the fetched /events window (simulated
    // here as covering only seq 1 again, as if seq 2 had since fallen
    // out of the server's own recency window) must NOT cause seq 2 to
    // disappear from the model — it already happened and was already
    // shown to the operator. Wholesale-replacing model.events with just
    // the freshly fetched page, instead of reconciling by seq, is
    // exactly the defect this test is written to catch.
    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        const thisAttempt = streamAttempt
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: `s${thisAttempt}`,
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        if (thisAttempt === 1) {
          setTimeout(() => {
            writeSSEFrame(res, 'event.recorded', {
              serverTime: new Date().toISOString(),
              event: makeEvent(2, { summary: 'live, seen before the reconnect' }),
            })
          }, 20)
          setTimeout(() => {
            req.socket.destroy()
          }, 60)
        }
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ latestEventSeq: 1 }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(
          res,
          200,
          makeEventsResponse({ events: [makeEvent(1)], latestSeq: 1, oldestRetainedSeq: 1 }),
        )
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().events.some((e) => e.seq === 2), {
      message: 'the live event.recorded frame before the reconnect was never applied',
    })
    await waitFor(() => streamAttempt >= 2, { message: 'store never reconnected' })
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never returned to live after reconnect',
    })

    // seq 2 must still be present after the reconnect's re-snapshot,
    // even though that re-snapshot's own /events fetch did not include it.
    expect(store.getSnapshot().events.map((e) => e.seq).sort((a, b) => a - b)).toEqual([1, 2])
  })

  it('computes clockSkewMs from serverTime rather than leaving it null once connected', async () => {
    const skewedServerTime = new Date(Date.now() + 60_000).toISOString() // 60s ahead
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: skewedServerTime,
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ serverTime: skewedServerTime }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse({ serverTime: skewedServerTime }))
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    const skew = store.getSnapshot().clockSkewMs
    expect(skew).not.toBeNull()
    expect(skew as number).toBeGreaterThan(55_000)
  })

  it('retryable failures (e.g. a 500) reconnect with backoff rather than failing terminally', async () => {
    let attempts = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        attempts += 1
        if (attempts === 1) {
          respondProblem(res, 500, makeProblem({ status: 500 }))
          return
        }
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'reconnecting', {
      message: 'a 500 never produced a reconnecting state',
    })
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never recovered after the transient 500',
    })
    expect(attempts).toBeGreaterThanOrEqual(2)
  })

  it('keeps the last known model visible (with its snapshotReceivedAt) while reconnecting, never clearing it', async () => {
    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: `s${streamAttempt}`,
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        if (streamAttempt === 1) {
          setTimeout(() => req.socket.destroy(), 40)
        }
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ nodes: [makeNode('n-persisted')] }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n-persisted'))
    const receivedAtAfterFirstSnapshot = store.getSnapshot().snapshotReceivedAt

    await waitFor(() => store.getSnapshot().connection.kind === 'reconnecting', {
      message: 'the drop never produced a visible reconnecting state',
    })
    // OPERATOR-UI section 7: retain last known state while reconnecting.
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-persisted'])
    expect(store.getSnapshot().snapshotReceivedAt).toBe(receivedAtAfterFirstSnapshot)
  })
})

describe('ApiStore: initial /events fetch requests a recent window derived from latestEventSeq (D1)', () => {
  it('requests since = latestEventSeq - window, not the endpoint\'s bare default (since=0)', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ latestEventSeq: 250 }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse({ latestSeq: 250 }))
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    const eventsReq = s.requestsFor('/events')[0]
    expect(eventsReq).toBeDefined()
    const params = new URLSearchParams(eventsReq?.url.split('?')[1] ?? '')
    // latestEventSeq (250) minus the window (100, INITIAL_EVENTS_WINDOW in
    // store.ts) — NOT since=0, which is what an un-derived call to
    // GET /events would send and which returns the OLDEST retained page
    // once history exceeds one page (D1's actual defect).
    expect(params.get('since')).toBe('150')
    expect(params.get('limit')).toBe('100')
  })

  it('clamps since to 0 (never negative) when latestEventSeq is smaller than the window', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ latestEventSeq: 5 }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse({ latestSeq: 5 }))
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    const eventsReq = s.requestsFor('/events')[0]
    const params = new URLSearchParams(eventsReq?.url.split('?')[1] ?? '')
    expect(params.get('since')).toBe('0')
  })
})

describe('ApiStore: version header negotiation (acceptance criterion 5, no prior test)', () => {
  it('treats an otherwise-2xx response with no ShowMesh-API-Version header as incompatible', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        // Deliberately omit the ShowMesh-API-Version header on an
        // otherwise-OK response — client.ts's checkVersionHeader is the
        // only thing standing between this and a client that silently
        // assumes it is talking to a v1 coordinator.
        res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' })
        res.flushHeaders()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'incompatible', {
      message: 'a response with no ShowMesh-API-Version header was not treated as incompatible',
    })
    expect(store.getSnapshot().connection).toMatchObject({ kind: 'incompatible', requiredVersion: 1 })
  })

  it('treats a mismatched ShowMesh-API-Version header as incompatible', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        res.writeHead(200, {
          'Content-Type': 'text/event-stream',
          'Cache-Control': 'no-cache',
          'ShowMesh-API-Version': '2',
        })
        res.flushHeaders()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'incompatible', {
      message: 'a mismatched ShowMesh-API-Version header was not treated as incompatible',
    })
    expect(store.getSnapshot().connection).toMatchObject({ kind: 'incompatible', requiredVersion: 1 })
  })
})

describe('ApiStore: eventsGap is sticky across a reconnect (no prior test)', () => {
  it('never clears eventsGap once observed true, even when a later fetch reports gap: false', async () => {
    let eventsAttempt = 0
    // `as` rather than a `: T | null` annotation: with the annotation,
    // this TypeScript version does not treat the closure's `streamRes =
    // res` below as narrowing the declared type at this scope, and every
    // read after the `if (streamRes === null) throw` guard below
    // silently types as `never` (a real repro, kept out of this comment
    // only because it belongs in a bug tracker, not test code).
    let streamRes = null as import('node:http').ServerResponse | null

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRes = res
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        eventsAttempt += 1
        // First fetch reports a real gap; the second (post-reconnect)
        // reports none — simulating the affected page simply having
        // moved on, not the lost events having reappeared.
        respondJson(
          res,
          200,
          makeEventsResponse({ gap: eventsAttempt === 1, oldestRetainedSeq: eventsAttempt === 1 ? 5 : null }),
        )
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(store.getSnapshot().eventsGap).toBe(true)

    if (streamRes === null) throw new Error('no open /stream response captured')
    writeSSEFrame(streamRes, 'stream.reset', {
      seq: 1,
      serverTime: new Date().toISOString(),
      reason: 'subscriber_too_slow',
      snapshotRequired: true,
    })

    await waitFor(() => eventsAttempt >= 2, { message: 'stream.reset did not trigger a re-fetch of /events' })
    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    // Sticky: the second fetch's gap: false must not have cleared it.
    expect(store.getSnapshot().eventsGap).toBe(true)
  })
})

describe('ApiStore: fpp.changed updates model.fpp (no prior test)', () => {
  it('applies an fpp.changed frame to the model', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.changed', {
            serverTime: new Date().toISOString(),
            instance: makeFPPInstance('fpp-1', { health: 'degraded' }),
          })
        }, 20)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    await waitFor(() => store.getSnapshot().fpp.some((i) => i.instanceId === 'fpp-1'), {
      message: 'the fpp.changed frame was never applied to the model',
    })
    expect(store.getSnapshot().fpp[0]?.health).toBe('degraded')
  })
})

describe('ApiStore: snapshot fpp.instances and collectors are not dropped (no prior test)', () => {
  it('carries snapshot.fpp.instances and snapshot.collectors into the model', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(
          res,
          200,
          makeSnapshot({
            fpp: { instances: [makeFPPInstance('fpp-a')] },
            collectors: [{ id: 'fpp-poller', state: 'running', reason: null }],
          }),
        )
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(store.getSnapshot().fpp.map((i) => i.instanceId)).toEqual(['fpp-a'])
    expect(store.getSnapshot().collectors).toEqual([{ id: 'fpp-poller', state: 'running', reason: null }])
  })
})

describe('ApiStore: malformed JSON in a frame is tolerated, not fatal (no prior test)', () => {
  it('ignores a node.changed frame with a malformed data: JSON body, and still applies a later valid frame', async () => {
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          // Not valid JSON — tryParse (store.ts) must catch this and
          // return null rather than throwing out of handleFrame.
          res.write('event: node.changed\ndata: {not valid json\n\n')
        }, 20)
        setTimeout(() => {
          writeSSEFrame(res, 'node.changed', {
            serverTime: new Date().toISOString(),
            node: makeNode('n-good'),
          })
        }, 45)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    // Directly after the malformed frame, before the valid one: the
    // connection must still be live, not torn down by an uncaught throw.
    await sleepMs(30)
    expect(store.getSnapshot().connection.kind).toBe('live')

    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n-good'), {
      message: 'a later, valid frame never arrived after the malformed one',
    })
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-good'])
  })

  it('a stream.reset with a malformed data: JSON body still triggers a resnapshot (store.ts: unconditional on parsing it)', async () => {
    let snapshotAttempt = 0
    // `as` rather than a `: T | null` annotation: with the annotation,
    // this TypeScript version does not treat the closure's `streamRes =
    // res` below as narrowing the declared type at this scope, and every
    // read after the `if (streamRes === null) throw` guard below
    // silently types as `never` (a real repro, kept out of this comment
    // only because it belongs in a bug tracker, not test code).
    let streamRes = null as import('node:http').ServerResponse | null

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRes = res
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        snapshotAttempt += 1
        const nodes = [makeNode(snapshotAttempt === 1 ? 'n-before' : 'n-after')]
        respondJson(res, 200, makeSnapshot({ nodes }))
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(store.getSnapshot().nodes.map((n) => n.nodeId)).toEqual(['n-before'])

    if (streamRes === null) throw new Error('no open /stream response captured')
    // event: stream.reset whose data: line is not valid JSON.
    streamRes.write('event: stream.reset\ndata: {not valid json\n\n')

    await waitFor(() => snapshotAttempt >= 2, {
      message: 'a stream.reset with an unparseable body did not trigger a resnapshot',
    })
    await waitFor(() => store.getSnapshot().nodes.some((n) => n.nodeId === 'n-after'))
  })
})

describe('ApiStore: dispose() actually stops the loop (no prior test)', () => {
  it('issues no further requests and notifies no more listeners once the connection dies after dispose()', async () => {
    let streamRequests = 0
    // See the `streamRes` comment elsewhere in this file for why `as`
    // rather than a `: T | null` annotation is needed here.
    let firstStreamReq = null as import('node:http').IncomingMessage | null

    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamRequests += 1
        if (streamRequests === 1) firstStreamReq = req
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    // Not using makeStore/openedStores here: this test's whole point is
    // to observe behavior strictly after dispose(), so it must not let
    // afterEach's cleanup dispose() a second time change what's measured.
    const store = new ApiStore({ baseUrl: s.baseUrl, backoff: FAST_BACKOFF })
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    let notified = false
    store.subscribe(() => {
      notified = true
    })

    store.dispose()
    // Simulate the now-disposed connection dying, exactly as a real
    // network fault would. A store whose dispose() actually stops the
    // loop must not reconnect; a no-op dispose() would retry as usual
    // and streamRequests would climb to 2.
    firstStreamReq?.socket.destroy()

    await sleepMs(FAST_BACKOFF.baseMs * 6)
    expect(streamRequests).toBe(1)
    expect(notified).toBe(false)

    await s.close()
  })
})

describe('ApiStore: stream idle timeout (D2)', () => {
  it('reconnects when the stream goes idle past the deadline, with no bytes at all — not even a keepalive', async () => {
    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: `s${streamAttempt}`,
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        // First attempt: go silent forever (no keepalive, no frames,
        // socket stays open at the TCP level — a half-open connection).
        // Second attempt: also stays open; the test only cares that a
        // reconnect happened at all.
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    // A FakeClock (test-support/fake-clock.ts) rather than a fast real
    // timeout: the deadline is now a virtual one this test advances
    // itself, so it can no longer be raced by real scheduling jitter —
    // this exact test used to fail intermittently under machine load
    // (see clock.ts's header comment) because it and the production
    // idle-deadline both used comparably-sized real setTimeout calls.
    const clock = new FakeClock()
    const store = makeStore(s.baseUrl, { clock, streamIdleTimeoutMs: 40 })
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    // Deterministically cross the idle deadline. The server above never
    // writes anything else on the first attempt, so nothing real can
    // resolve the pending read before this fires — there is no race to
    // lose here, unlike the real-timer version this replaced.
    clock.advance(40)

    await waitFor(() => streamAttempt >= 2, {
      message: 'an idle stream past the deadline never triggered a reconnect',
    })
  })

  it('does not reconnect while keepalive comments keep arriving inside the idle deadline', async () => {
    let streamAttempt = 0
    let streamRes = null as import('node:http').ServerResponse | null
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        streamRes = res
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: `s${streamAttempt}`,
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    // A FakeClock (test-support/fake-clock.ts): virtual time only
    // advances when this test calls clock.advance() below, so the
    // deadline can never fire from real scheduling delay alone. This
    // replaces the previous version's real 15ms-comment-interval vs.
    // real 40ms-deadline race, which is exactly the pairing that was
    // reproduced flaking under machine load (see clock.ts).
    const clock = new FakeClock()
    const store = makeStore(s.baseUrl, { clock, streamIdleTimeoutMs: 40 })
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    if (streamRes === null) throw new Error('no open /stream response captured')

    // Each iteration writes one real keepalive comment across the real
    // socket, waits for the store's read loop to actually consume it
    // (clock.armed() resolves the moment store.ts's readWithIdleTimeout
    // arms the NEXT deadline, which only happens after the previous
    // read — this one's — has resolved), and only then advances the
    // virtual clock by less than the deadline. If keepalive bytes
    // stopped resetting the deadline, these advances would accumulate
    // past streamIdleTimeoutMs (40) well before 6 iterations of 25ms
    // each (150ms) and force a reconnect; because each advance is
    // anchored to a freshly-confirmed reset, they never do.
    for (let i = 0; i < 6; i++) {
      const nextArm = clock.armed()
      writeSSEComment(streamRes, 'keepalive')
      await nextArm
      clock.advance(25)
    }

    expect(streamAttempt).toBe(1)
    expect(store.getSnapshot().connection.kind).toBe('live')
  })
})

describe('ApiClient: a request that never gets a response times out and is retried, not hung forever (D2)', () => {
  it('treats a request timeout as a normal retryable failure', async () => {
    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url === '/stream') {
        streamAttempt += 1
        if (streamAttempt === 1) {
          // Never respond at all — no headers, nothing. A store with no
          // request timeout would sit in 'connecting' forever.
          return
        }
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's2',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot())
        return
      }
      if (req.url?.startsWith('/events')) {
        respondJson(res, 200, makeEventsResponse())
        return
      }
      res.writeHead(404).end()
    })

    // A FakeClock (test-support/fake-clock.ts) rather than a fast real
    // request timeout: client.ts arms its timeout timer strictly before
    // issuing the fetch (see client.ts's request()), so once the server
    // above has actually received the request, the client-side timer is
    // guaranteed already armed and advancing the virtual clock is not a
    // race against it arriving late.
    const clock = new FakeClock()
    const store = makeStore(s.baseUrl, { clock, requestTimeoutMs: 40 })
    store.connect()

    await waitFor(() => streamAttempt >= 1, {
      message: 'the first /stream request was never issued',
    })
    clock.advance(40)

    await waitFor(() => store.getSnapshot().connection.kind === 'reconnecting', {
      message: 'a request that never responds never produced a reconnecting state',
    })
    await waitFor(() => store.getSnapshot().connection.kind === 'live')
    expect(streamAttempt).toBeGreaterThanOrEqual(2)
  })
})
