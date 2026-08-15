import { afterEach, describe, expect, it } from 'vitest'
import { ApiStore } from './store'
import { getStoredToken, setStoredToken } from './token'
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
  makeAuthenticatedSession,
  makeEvent,
  makeEventsResponse,
  makeFPPInstance,
  makeNode,
  makeProblem,
  makeSessionResponse,
  makeSnapshot,
} from './test-support/fixtures'
import {
  makeRemote01Instance,
  makeRemote04Instance,
} from '../app/test-support/fppFleetFixtures'
import type { components } from './generated/schema'

type Evidence = components['schemas']['Evidence']

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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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
      if (req.url?.startsWith('/stream')) {
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

describe('ApiStore: observation deltas (ADR-023)', () => {
  function findInstance(store: ApiStore, instanceId: string) {
    return store.getSnapshot().fpp.find((i) => i.instanceId === instanceId)
  }

  function findSignal(observations: readonly Evidence[], signal: string): Evidence | undefined {
    return observations.find((o) => o.signal === signal)
  }

  it('always opts in: the /stream request carries the literal ?deltas=1 query, and nothing looser', async () => {
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
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
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    const streamReq = s.requestsFor('/stream')[0]
    // Positive assertion (the exact literal the wire contract requires),
    // not merely "the request happened at all".
    expect(streamReq?.url).toBe('/stream?deltas=1')
  })

  it('a delta frame MERGES rather than replaces: after a delta carrying 1 of many signals, every other signal is still present and byte-identical (THE TRAP)', async () => {
    const baseInstance = makeRemote04Instance()
    const totalSignals = baseInstance.observations.length
    const uptimeBefore = findSignal(baseInstance.observations, 'fpp.uptime.seconds')
    if (uptimeBefore === undefined) throw new Error('fixture missing fpp.uptime.seconds')

    const updatedUptime: Evidence = { ...uptimeBefore, value: (uptimeBefore.value as number) + 1 }

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          // ONE signal out of `totalSignals`, exactly the volume ADR-023
          // exists to shrink: a real coordinator would otherwise have
          // repeated all of them inside fpp.changed.
          writeSSEFrame(res, 'fpp.observations.changed', {
            serverTime: new Date().toISOString(),
            instanceId: baseInstance.instanceId,
            changed: [updatedUptime],
            removed: [],
          })
        }, 20)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ fpp: { instances: [baseInstance] } }))
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

    await waitFor(
      () => findSignal(findInstance(store, baseInstance.instanceId)?.observations ?? [], 'fpp.uptime.seconds')?.value === updatedUptime.value,
      { message: 'the delta was never applied' },
    )

    const observations = findInstance(store, baseInstance.instanceId)?.observations ?? []

    // Positive: the changed signal moved.
    expect(findSignal(observations, 'fpp.uptime.seconds')?.value).toBe(updatedUptime.value)

    // Positive: the count is UNCHANGED (a client that replaced on this
    // frame would show 1, not `totalSignals`) -- this is the "renders 4
    // signals out of 349 and looks perfectly healthy" failure mode from
    // the ADR, caught structurally rather than by eyeballing a UI.
    expect(observations.length).toBe(totalSignals)

    // Positive, not just "does not appear missing": every OTHER signal
    // from the base snapshot is still present and byte-for-byte identical
    // to what the snapshot originally carried -- not merely "some 348
    // signals exist", but exactly these ones, unchanged.
    for (const original of baseInstance.observations) {
      if (original.signal === 'fpp.uptime.seconds') continue
      expect(findSignal(observations, original.signal)).toEqual(original)
    }
  })

  it("'removed' actually deletes: a signal named there is GONE from the rendered view, not merely stale", async () => {
    const baseInstance = makeRemote04Instance()
    const totalSignals = baseInstance.observations.length
    const removedSignal = 'fpp.port.port_5.current_ma'
    if (findSignal(baseInstance.observations, removedSignal) === undefined) {
      throw new Error(`fixture missing ${removedSignal}`)
    }
    // A neighboring signal that must survive, to prove this is a targeted
    // deletion and not an accidental wipe of everything nearby.
    const neighborSignal = 'fpp.port.port_5.enabled'

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.observations.changed', {
            serverTime: new Date().toISOString(),
            instanceId: baseInstance.instanceId,
            changed: [],
            removed: [removedSignal],
          })
        }, 20)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ fpp: { instances: [baseInstance] } }))
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

    await waitFor(() => (findInstance(store, baseInstance.instanceId)?.observations.length ?? -1) < totalSignals, {
      message: 'the removal was never applied',
    })

    const observations = findInstance(store, baseInstance.instanceId)?.observations ?? []

    // Negative, paired with a positive: the removed signal is gone...
    expect(findSignal(observations, removedSignal)).toBeUndefined()
    // ...and the count dropped by EXACTLY one, not "gone but replaced by
    // something else" and not "the whole array got smaller by accident".
    expect(observations.length).toBe(totalSignals - 1)
    // Positive: an untouched neighbor survived the removal, byte-identical.
    expect(findSignal(observations, neighborSignal)).toEqual(findSignal(baseInstance.observations, neighborSignal))
  })

  it('a full fpp.changed frame still REPLACES: an observation absent from it is gone afterwards, even on a delta-subscribed connection', async () => {
    const before = makeRemote04Instance({ health: 'healthy' })
    // A full frame that genuinely omits a signal the previous one carried
    // -- exactly the "cape swapped for a smaller one" shape, delivered via
    // fpp.changed rather than a delta.
    const droppedSignal = 'fpp.port.port_9.current_ma'
    const after = {
      ...before,
      health: 'degraded' as const,
      observations: before.observations.filter((o) => o.signal !== droppedSignal),
    }
    if (findSignal(before.observations, droppedSignal) === undefined) {
      throw new Error(`fixture missing ${droppedSignal}`)
    }

    // The `fpp.changed` frame used to be written on a fixed 20ms
    // `setTimeout`, racing this test's own "live, then assert the
    // pre-frame baseline" sequence, which waits on however many HTTP
    // round trips reloadSnapshot needs (snapshot, events, and — since
    // ADR-024 — session). Under load those round trips can take LONGER
    // than 20ms, and by the time `waitFor(live)` below notices, the
    // frame's bytes are already sitting in the client's socket buffer:
    // nothing stops `runConnectionAttempt`'s read loop from draining
    // them and applying the frame in the same tick, before this test's
    // own poll even gets a turn — flipping `health` to 'degraded' before
    // the "positive: the pre-frame baseline still has it" assertion
    // below runs, which is exactly the flake this comment is warning
    // about (this project's Step 4 lesson: do not race a clock).
    //
    // Fixed structurally rather than retuned: the frame is not written
    // to the stream socket AT ALL until this test explicitly releases
    // it, below, immediately after making and checking that baseline
    // assertion. There is no wall-clock quantity left to guess, on
    // either macOS or Linux CI.
    let releaseFrame: (() => void) | undefined
    const frameReleased = new Promise<void>((resolve) => {
      releaseFrame = resolve
    })

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        void frameReleased.then(() => {
          writeSSEFrame(res, 'fpp.changed', {
            serverTime: new Date().toISOString(),
            instance: after,
          })
        })
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ fpp: { instances: [before] } }))
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
    // Positive: the pre-frame baseline really did carry the signal that's
    // about to disappear -- otherwise its later absence would prove nothing.
    // Nothing on the wire can have applied the frame yet: it has not been
    // released.
    expect(findSignal(findInstance(store, before.instanceId)?.observations ?? [], droppedSignal)).toBeDefined()

    releaseFrame?.()
    await waitFor(() => findInstance(store, before.instanceId)?.health === 'degraded', {
      message: 'the fpp.changed frame was never applied',
    })

    const observations = findInstance(store, before.instanceId)?.observations ?? []
    // Negative, paired with the positive above: it is gone now.
    expect(findSignal(observations, droppedSignal)).toBeUndefined()
    // Positive: the replacement is exactly `after`'s own observation set,
    // not a merge of `before` and `after` -- a merging client would still
    // show `before.observations.length` entries; this one must show
    // exactly `after.observations.length`.
    expect(observations.length).toBe(after.observations.length)
    expect(observations.map((o) => o.signal).sort()).toEqual(after.observations.map((o) => o.signal).sort())
  })

  it('equivalence: a real sequence of snapshot + mixed delta/full frames (including a removal) converges on exactly what a fresh snapshot would show at that point', async () => {
    const base = makeRemote04Instance({ health: 'healthy' })
    const uptimeSignal = 'fpp.uptime.seconds'
    const removedSignal = 'fpp.port.port_12.current_ma'
    const otherChangedSignal = 'fpp.port.port_12.enabled'

    const uptimeAfterDelta1 = { ...findSignal(base.observations, uptimeSignal)!, value: 999999 }
    const enabledAfterDelta3 = { ...findSignal(base.observations, otherChangedSignal)!, value: true }

    // A full fpp.changed, as the coordinator would genuinely send it: the
    // COMPLETE current observation set (already reflecting delta1's
    // uptime change, per ADR-023's own "the full frame already carries
    // the current set" reasoning), plus a structural change (health).
    const structuralUpdate = {
      ...base,
      health: 'degraded' as const,
      observations: base.observations.map((o) => (o.signal === uptimeSignal ? uptimeAfterDelta1 : o)),
    }

    // What a fresh GET /snapshot would show if fetched AFTER this entire
    // sequence: the structural update's own observation set, with the
    // later removal and the later unrelated change both applied on top.
    // This is computed independently of the store's own merge function --
    // it is built by hand here as the safety argument, not by calling
    // production code a second time.
    const expectedFinalObservations = structuralUpdate.observations
      .filter((o) => o.signal !== removedSignal)
      .map((o) => (o.signal === otherChangedSignal ? enabledAfterDelta3 : o))

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1',
          apiVersion: 1,
          serverTime: new Date().toISOString(),
          snapshotRequired: true,
        })
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.observations.changed', {
            serverTime: new Date().toISOString(),
            instanceId: base.instanceId,
            changed: [uptimeAfterDelta1],
            removed: [],
          })
        }, 20)
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.changed', {
            serverTime: new Date().toISOString(),
            instance: structuralUpdate,
          })
        }, 40)
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.observations.changed', {
            serverTime: new Date().toISOString(),
            instanceId: base.instanceId,
            changed: [],
            removed: [removedSignal],
          })
        }, 60)
        setTimeout(() => {
          writeSSEFrame(res, 'fpp.observations.changed', {
            serverTime: new Date().toISOString(),
            instanceId: base.instanceId,
            changed: [enabledAfterDelta3],
            removed: [],
          })
        }, 80)
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ fpp: { instances: [base] } }))
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

    await waitFor(
      () => findSignal(findInstance(store, base.instanceId)?.observations ?? [], otherChangedSignal)?.value === true,
      { message: 'the full sequence never finished applying', timeoutMs: 3000 },
    )
    // Let the event loop settle so nothing from this sequence is still in flight.
    await sleepMs(20)

    const finalInstance = findInstance(store, base.instanceId)
    expect(finalInstance).toBeDefined()
    // Positive: structural field really did change.
    expect(finalInstance?.health).toBe('degraded')
    // Negative, paired with a positive: the removed signal is gone...
    expect(findSignal(finalInstance?.observations ?? [], removedSignal)).toBeUndefined()
    // ...while the total count matches the independently-computed expectation exactly.
    expect(finalInstance?.observations.length).toBe(expectedFinalObservations.length)

    // The actual equivalence check: every signal the hand-computed
    // "fresh snapshot" would carry is present in the live model with the
    // identical value, and nothing extra is present either (set equality
    // via the sorted signal-name lists, followed by a per-signal deep
    // comparison so a value mismatch is not hidden by the count matching).
    const actualSignals = (finalInstance?.observations ?? []).map((o) => o.signal).sort()
    const expectedSignals = expectedFinalObservations.map((o) => o.signal).sort()
    expect(actualSignals).toEqual(expectedSignals)
    for (const expected of expectedFinalObservations) {
      expect(findSignal(finalInstance?.observations ?? [], expected.signal)).toEqual(expected)
    }
  })

  it('a reconnect re-fetches an authoritative snapshot and does not carry a prior delta merge across the gap', async () => {
    const before = makeRemote04Instance({ instanceId: 'fpp-fleet', health: 'healthy' })
    const uptimeBefore = findSignal(before.observations, 'fpp.uptime.seconds')!
    const deltaValueBeforeDrop = 424242

    // A totally distinct instance snapshot for the SECOND connection --
    // different fixture entirely (fewer, differently-named port signals),
    // so "the delta's effect survived" and "the fresh snapshot's own data
    // arrived" cannot be confused with each other by coincidence.
    const after = makeRemote01Instance({ instanceId: 'fpp-fleet', health: 'healthy' })
    const uptimeAfter = findSignal(after.observations, 'fpp.uptime.seconds')!
    if (uptimeAfter.value === deltaValueBeforeDrop) {
      throw new Error('fixture collision would make this test meaningless')
    }

    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
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
            writeSSEFrame(res, 'fpp.observations.changed', {
              serverTime: new Date().toISOString(),
              instanceId: 'fpp-fleet',
              changed: [{ ...uptimeBefore, value: deltaValueBeforeDrop }],
              removed: [],
            })
          }, 20)
          setTimeout(() => {
            // No closing frame at all -- an ordinary interruption per
            // api/openapi.yaml's /stream description.
            req.socket.destroy()
          }, 50)
        }
        return
      }
      if (req.url === '/snapshot') {
        respondJson(res, 200, makeSnapshot({ fpp: { instances: [streamAttempt === 1 ? before : after] } }))
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

    await waitFor(
      () => findSignal(findInstance(store, 'fpp-fleet')?.observations ?? [], 'fpp.uptime.seconds')?.value === deltaValueBeforeDrop,
      { message: 'the pre-drop delta was never applied' },
    )

    await waitFor(() => streamAttempt >= 2, { message: 'store never reconnected' })
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never returned to live after reconnect',
    })

    const finalInstance = findInstance(store, 'fpp-fleet')
    // Positive: the fresh snapshot's own uptime value is what's showing now.
    expect(findSignal(finalInstance?.observations ?? [], 'fpp.uptime.seconds')?.value).toBe(uptimeAfter.value)
    // Negative, paired with the positive above: the pre-drop delta's value
    // did not survive the reconnect merged on top of the new snapshot --
    // if it had, the store would be attempting to reconcile across a gap,
    // which ADR-020/ADR-023 both forbid.
    expect(findSignal(finalInstance?.observations ?? [], 'fpp.uptime.seconds')?.value).not.toBe(deltaValueBeforeDrop)
    // Positive: the entire observation SET is `after`'s own, not a merge
    // of `before` and `after` -- signal-for-signal, not just the one value
    // checked above.
    expect((finalInstance?.observations ?? []).map((o) => o.signal).sort()).toEqual(
      after.observations.map((o) => o.signal).sort(),
    )
  })
})

describe('ApiStore: ADR-024 sessions', () => {
  it('fetches /session independently of the read loop, so it resolves even when /stream never does (reads closed, no credential)', async () => {
    let sessionAttempts = 0
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        // Simulates a coordinator running with reads closed: every
        // /stream attempt is rejected, so runConnectionAttempt never
        // even reaches reloadSnapshot's own /session refresh — the
        // ONLY thing that can still answer "am I signed in" here is
        // connect()'s independent fetch.
        respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'reads closed' }))
        return
      }
      if (req.url === '/session') {
        sessionAttempts += 1
        respondJson(res, 200, makeSessionResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'unauthorized', {
      message: 'connection never reached unauthorized',
    })
    await waitFor(() => store.getSnapshot().session !== null, {
      message: 'session was never fetched even though GET /session is always open',
    })
    expect(store.getSnapshot().session?.authenticated).toBe(false)
    expect(sessionAttempts).toBeGreaterThanOrEqual(1)
  })

  it('sessionFetchFailed becomes true from a REAL failing GET /session and clears from a REAL succeeding one (ADR-024 decision 12)', async () => {
    // The trap this test is named for: a fixture that just SETS
    // `sessionFetchFailed` (as app/session.test.ts and
    // ScopedButton.test.tsx legitimately do, to test what the flag DOES
    // once it's true) proves nothing about whether the STORE itself ever
    // produces or clears it. Deleting the `this.setModel({ ...,
    // sessionFetchFailed: true })` assignment in store.ts, or pinning it
    // permanently true, both pass every other test in this suite — this
    // one drives two real GET /session round trips against a real
    // node:http server, one failing and one succeeding, and watches the
    // flag move both ways.
    //
    // connect() fires TWO /session fetches during one connect-to-live
    // sequence: an immediate, independent one (this store's connect(),
    // fired synchronously with no preceding request) and a second one
    // from reloadSnapshot, which cannot happen until AFTER the stream
    // handshake plus /snapshot plus /events have all round-tripped. That
    // ordering is structural (the second attempt requires strictly more
    // preceding network round trips than the first, which requires none),
    // and this test's first draft relied on exactly that to decide which
    // attempt gets the failing response — confirmed, by instrumentation,
    // to hold reliably across many runs.
    //
    // That draft was still flaky, though, for a DIFFERENT reason worth
    // recording: on a fast loopback connection, both requests can
    // complete within a couple of milliseconds of each other, so
    // `sessionFetchFailed` is true for LESS TIME than waitFor's own 10ms
    // poll interval — a poll can step from "not yet set" straight to
    // "already cleared" and never sample the true value in between, even
    // though the flag genuinely did become true. Confirmed by
    // instrumenting store.ts directly: the failing fetch set the flag,
    // and the succeeding one cleared it, 2ms later — not slow, not
    // reordered, just too brief for a poll to catch reliably. So per this
    // project's Step 4 lesson (do not race a clock; construct the
    // ordering structurally), the SECOND /session response is held open
    // on the server until this test has explicitly observed and asserted
    // the failure, exactly as finding 6's fpp.changed fix does.
    let sessionAttempts = 0
    let releaseSecondSession: (() => void) | undefined
    const secondSessionReleased = new Promise<void>((resolve) => {
      releaseSecondSession = resolve
    })

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'GET') {
        sessionAttempts += 1
        if (sessionAttempts === 1) {
          // A genuine failure on the wire — not a fixture setting a flag.
          res.writeHead(500).end()
          return
        }
        void secondSessionReleased.then(() => respondJson(res, 200, makeSessionResponse()))
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().sessionFetchFailed === true, {
      message: 'sessionFetchFailed was never set by a real failing GET /session',
    })
    // Negative, paired with the positive above: `session` itself is
    // untouched by the failure (fetchSession's own doc comment — "a
    // momentary blip must not flash 'signed out' over a session that is
    // actually fine"), so this is testing the FAILURE flag specifically,
    // not a side effect of the session field also changing.
    expect(store.getSnapshot().session).toBeNull()

    releaseSecondSession?.()
    await waitFor(() => store.getSnapshot().sessionFetchFailed === false, {
      message: 'sessionFetchFailed was never cleared by a real succeeding GET /session',
    })
    expect(store.getSnapshot().session).not.toBeNull()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')
  })

  it('re-fetches /session on every stream reconnect, bounding the staleness window ADR-024 decision 12 describes', async () => {
    let streamAttempt = 0
    let sessionAttempt = 0
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
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
          setTimeout(() => req.socket.destroy(), 60)
        }
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
      if (req.url === '/session') {
        sessionAttempt += 1
        respondJson(res, 200, makeSessionResponse({ serverTime: new Date(Date.now() + sessionAttempt).toISOString() }))
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => sessionAttempt >= 1, { message: 'the initial connect()-time session fetch never happened' })
    const afterFirstConnect = sessionAttempt

    await waitFor(() => streamAttempt >= 2, { message: 'store never reconnected the stream' })
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'store never returned to live after reconnect',
    })
    // A test's name is a claim: this is the assertion that fails if
    // reloadSnapshot's own fetchSession call (store.ts) were ever deleted
    // — sessionAttempt would stop at whatever connect() alone produced.
    await waitFor(() => sessionAttempt > afterFirstConnect, {
      message: 'session was never re-fetched after the stream reconnected',
    })
  })

  it('login() updates model.session from the POST response and leaves an already-live connection live', async () => {
    let capturedBody = ''
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'GET') return respondJson(res, 200, makeSessionResponse())
      if (req.url === '/session' && req.method === 'POST') {
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          capturedBody = Buffer.concat(chunks).toString('utf-8')
          respondJson(res, 200, makeAuthenticatedSession({
            principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
          }))
        })()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    await store.login('alice', 'secret123', 'porch tablet')

    expect(JSON.parse(capturedBody)).toEqual({ name: 'alice', password: 'secret123', deviceLabel: 'porch tablet' })
    expect(store.getSnapshot().session?.authenticated).toBe(true)
    expect(store.getSnapshot().session?.principal?.name).toBe('alice')
    // login() now deliberately wakes the read loop (wakeReadLoop, added
    // after a real-browser finding — see that method's own comment), so
    // an already-live connection may pass through a brief reconnect
    // before settling back to 'live'; this waits it out rather than
    // asserting the synchronous value immediately after the await, which
    // would be racing microtask ordering rather than testing behavior.
    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'connection never returned to live after the post-login reconnect',
    })
  })

  it('login() wakes a read loop parked in "unauthorized" (reads closed), so a cookie-authenticated sign-in actually reaches "live" — found only against a real browser and a coordinator running with reads closed', async () => {
    // The regression this guards: login()/claimBootstrap() used to only
    // update model.session, never touch the read loop. Against a
    // coordinator with SHOWMESH_API_CLOSE_READS=true, that loop sits in
    // `{ kind: 'unauthorized' }`, parked on `waitUntilAborted(signal)`
    // (store.ts's runLoop) — and the only two things that had ever woken
    // it were submitToken/clearToken. A cookie-authenticated login left
    // the dashboard showing "Signed in as ..." in the persistent banner
    // while the rest of the page stayed stuck on "waiting for the first
    // response," forever. No unit test targeting login() in isolation
    // could see this, because it has no read loop sitting in
    // 'unauthorized' to fail to wake — this test constructs exactly that
    // loop first, the same way the real browser session that found the
    // bug did.
    let authenticated = false
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        if (!authenticated) {
          respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'reads closed' }))
          return
        }
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'GET') {
        return respondJson(res, 200, authenticated ? makeAuthenticatedSession() : makeSessionResponse())
      }
      if (req.url === '/session' && req.method === 'POST') {
        authenticated = true
        respondJson(res, 200, makeAuthenticatedSession())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().connection.kind === 'unauthorized', {
      message: 'the read loop never reached the closed-reads unauthorized state to begin with',
    })

    await store.login('alice', 'secret123', 'porch tablet')

    await waitFor(() => store.getSnapshot().connection.kind === 'live', {
      message: 'the read loop never woke up after a successful cookie-authenticated login — it would still be stuck showing "unauthorized" or waiting, exactly the real-browser defect this test guards',
    })
  })

  it('login() rejects on invalid credentials and leaves session/connection untouched', async () => {
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'GET') return respondJson(res, 200, makeSessionResponse())
      if (req.url === '/session' && req.method === 'POST') {
        return respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'invalid name or password' }))
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    await expect(store.login('alice', 'wrong', 'porch tablet')).rejects.toThrow('invalid name or password')

    expect(store.getSnapshot().session?.authenticated).toBe(false)
    // A wrong password at the login form must never be confused, at the
    // connection-state level, with the read loop's own 401 handling —
    // it must not flip to 'unauthorized' just because SOME request
    // somewhere got a 401.
    expect(store.getSnapshot().connection.kind).toBe('live')
  })

  it('logout() revokes and re-fetches, leaving model.session signed out', async () => {
    let deleteCalls = 0
    let sessionGetCalls = 0
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'DELETE') {
        deleteCalls += 1
        res.writeHead(204, { 'ShowMesh-API-Version': '1' })
        res.end()
        return
      }
      if (req.url === '/session' && req.method === 'GET') {
        sessionGetCalls += 1
        // Signed in until the DELETE lands, signed out after — the
        // fixture a real coordinator would produce across this sequence.
        respondJson(res, 200, deleteCalls === 0 ? makeAuthenticatedSession() : makeSessionResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().session?.authenticated === true, {
      message: 'never observed the initial signed-in state',
    })

    await store.logout()

    expect(deleteCalls).toBe(1)
    expect(sessionGetCalls).toBeGreaterThanOrEqual(2) // the initial fetch(es), plus logout's own re-fetch
    expect(store.getSnapshot().session?.authenticated).toBe(false)
  })

  it('claimBootstrap() applies the returned session on success', async () => {
    let capturedBody = ''
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'reads closed' }))
        return
      }
      if (req.url === '/session') return respondJson(res, 200, makeSessionResponse({ bootstrapRequired: true }))
      if (req.url === '/bootstrap' && req.method === 'POST') {
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          capturedBody = Buffer.concat(chunks).toString('utf-8')
          respondJson(res, 200, makeAuthenticatedSession({ bootstrapRequired: false, principal: { id: 'p1', name: 'root', kind: 'human', role: 'admin' } }))
        })()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().session?.bootstrapRequired === true)

    await store.claimBootstrap('the-code', 'root', 'secret123', 'porch tablet')

    expect(JSON.parse(capturedBody)).toEqual({
      code: 'the-code', name: 'root', password: 'secret123', deviceLabel: 'porch tablet',
    })
    expect(store.getSnapshot().session?.authenticated).toBe(true)
    expect(store.getSnapshot().session?.bootstrapRequired).toBe(false)
    expect(store.getSnapshot().session?.principal?.role).toBe('admin')
  })

  it('login() clears a stored break-glass token on success, so a stale token cannot permanently shadow the cookie it just proved valid', async () => {
    // Finding: client.ts's Authorization header always wins over the
    // cookie (ADR-024 decision 6, "an Authorization header, if present at
    // all, is the only credential path considered") -- with no
    // fallthrough to the cookie when it is invalid. Left in storage, a
    // token from a PREVIOUS (now revoked, or simply wrong) credential
    // would keep shadowing every request this store makes after login(),
    // including its own next GET /session, so the coordinator would keep
    // answering "unauthenticated" and the persistent sign-in banner would
    // never reflect a login this test just watched succeed.
    setStoredToken('stale-shadowing-token')
    expect(getStoredToken()).toBe('stale-shadowing-token') // sanity: the setup actually took

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session' && req.method === 'GET') return respondJson(res, 200, makeSessionResponse())
      if (req.url === '/session' && req.method === 'POST') {
        return respondJson(res, 200, makeAuthenticatedSession({
          principal: { id: 'p1', name: 'alice', kind: 'human', role: 'operator' },
        }))
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()
    await waitFor(() => store.getSnapshot().connection.kind === 'live')

    await store.login('alice', 'secret123', 'porch tablet')

    expect(getStoredToken()).toBeNull()
  })

  it('claimBootstrap() clears a stored break-glass token on success, for the same reason login() does', async () => {
    setStoredToken('stale-shadowing-token')

    const s = await server((req, res) => {
      if (req.url === '/bootstrap' && req.method === 'POST') {
        return respondJson(res, 200, makeAuthenticatedSession({ bootstrapRequired: false, principal: { id: 'p1', name: 'root', kind: 'human', role: 'admin' } }))
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    // Deliberately NOT calling store.connect(): claimBootstrap() makes its
    // own independent POST /bootstrap request, so no /stream connection
    // is needed to exercise it — and skipping one sidesteps a real
    // confound this test's first draft had. With a token already stored,
    // an UNRELATED 401 from /stream (used there to simulate reads-closed)
    // would ALSO clear the token via runLoop's own "a rejected token must
    // be cleared" path (spec section 5.6) — which made that draft pass
    // even with claimBootstrap()'s own clearShadowingToken() call
    // deleted. Confirmed by breaking claimBootstrap()'s call and watching
    // this version fail while that draft did not.
    await store.claimBootstrap('the-code', 'root', 'secret123', 'porch tablet')

    expect(getStoredToken()).toBeNull()
  })

  it('a 401 that puts the read loop into "unauthorized" also refreshes the persistent session banner, not just the connection state', async () => {
    // Item 7: "a closed change stream may mean a revoked session ... the
    // client's existing reconnect and snapshot path then hits a 401,
    // which must surface as an explicit authentication state." This test
    // is the part of that claim specific to this step: the PERSISTENT
    // banner (model.session) must also update, not only model.connection
    // — otherwise SessionPanel would still read "signed in" from
    // whatever /session last reported, stale, after the revocation.
    let streamAttempt = 0
    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        streamAttempt += 1
        if (streamAttempt === 1) {
          openSSE(res)
          writeSSEFrame(res, 'stream.start', {
            streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
          })
          setTimeout(() => req.socket.destroy(), 60)
          return
        }
        // Second attempt onward: the coordinator has closed reads and
        // this device's session was revoked — simulates decision 5's
        // "closes the connection ... the client's existing reconnect
        // ... then hits a 401."
        respondProblem(res, 401, makeProblem({ type: 'https://showmesh.dev/problems/unauthorized', status: 401, detail: 'session revoked' }))
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session') {
        // Signed in for the first (successful) connection; signed out
        // from the moment the stream starts failing, mirroring what a
        // real coordinator would answer once the session is actually gone.
        respondJson(res, 200, streamAttempt <= 1 ? makeAuthenticatedSession() : makeSessionResponse())
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().session?.authenticated === true, {
      message: 'never observed the initial signed-in state',
    })
    await waitFor(() => store.getSnapshot().connection.kind === 'unauthorized', {
      message: 'connection never surfaced the explicit authentication state',
    })
    await waitFor(() => store.getSnapshot().session?.authenticated === false, {
      message: 'the persistent session banner never learned about the revocation — it would still read "signed in"',
    })
  })

  it('applySessionResponse keeps the newer serverTime even when the OLDER response resolves SECOND', async () => {
    // connect()'s own independent fetch and reloadSnapshot's refresh are
    // fired close together on purpose (store.ts's connect() comment) —
    // this constructs the one ordering that actually distinguishes
    // "guarded by serverTime" from "guarded by nothing" (plain last-
    // write-wins would already pass a same-order race). The FIRST
    // /session request to ARRIVE (connect()'s) is answered immediately
    // with a NEWER serverTime; the SECOND to arrive (reloadSnapshot's) is
    // answered LATER, but with OLDER data — exactly what a real
    // coordinator would never do, constructed here specifically because a
    // real timing race cannot be relied on to expose an ordering bug (this
    // project's own "do not race a kernel; construct it structurally"
    // rule, restated for wall-clock races instead of buffer-overflow ones).
    const older = new Date('2020-01-01T00:00:00.000Z').toISOString()
    const newer = new Date('2030-01-01T00:00:00.000Z').toISOString()
    let sessionRequestsSeen = 0

    const s = await server((req, res) => {
      if (req.url?.startsWith('/stream')) {
        openSSE(res)
        writeSSEFrame(res, 'stream.start', {
          streamId: 's1', apiVersion: 1, serverTime: new Date().toISOString(), snapshotRequired: true,
        })
        return
      }
      if (req.url === '/snapshot') return respondJson(res, 200, makeSnapshot())
      if (req.url?.startsWith('/events')) return respondJson(res, 200, makeEventsResponse())
      if (req.url === '/session') {
        sessionRequestsSeen += 1
        if (sessionRequestsSeen === 1) {
          respondJson(res, 200, makeSessionResponse({ serverTime: newer }))
        } else {
          setTimeout(() => respondJson(res, 200, makeSessionResponse({ serverTime: older })), 40)
        }
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    store.connect()

    await waitFor(() => store.getSnapshot().session?.serverTime === newer, {
      message: 'the first (newer) session response was never applied',
    })
    await waitFor(() => sessionRequestsSeen >= 2, { message: 'the second session request never arrived' })
    // Give the delayed, older-data second response time to resolve and
    // (if the guard were missing) overwrite the newer one already applied.
    await sleepMs(80)

    expect(store.getSnapshot().session?.serverTime).toBe(newer)
  })
})

describe('ApiStore: Step 7 seam A configuration (RES-008 D1)', () => {
  const fppEndpointsConfigResponse = {
    serverTime: new Date().toISOString(),
    kind: 'fpp.endpoints',
    revision: 1,
    payload: { endpoints: [{ id: 'player-01', url: 'http://10.0.1.20' }] },
    updatedAt: new Date().toISOString(),
    createdByPrincipalId: 'p-1',
    createdByPrincipalName: 'admin-1',
    source: 'api',
    restartRequired: true,
    restartRequiredReason: 'this coordinator does not hot-reload configuration',
  }

  // None of these three methods needs store.connect() / the SSE read loop
  // at all — see store.ts's "Step 7 seam A" section comment for why they
  // are plain pass-throughs independent of Model — so every test below
  // constructs a store and calls the method directly.

  it('getFPPEndpointsConfig() returns the decoded response', async () => {
    let gotPath = ''
    const s = await server((req, res) => {
      gotPath = req.url ?? ''
      respondJson(res, 200, fppEndpointsConfigResponse)
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.getFPPEndpointsConfig()

    expect(gotPath).toBe('/config/fpp.endpoints')
    expect(resp.revision).toBe(1)
    expect(resp.payload.endpoints).toEqual([{ id: 'player-01', url: 'http://10.0.1.20' }])
  })

  it('getFPPEndpointsConfig() rejects with a typed error on 404 (nothing configured yet)', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 404, makeProblem({
        type: 'https://showmesh.dev/problems/resource-not-found', status: 404,
        detail: 'no fpp.endpoints configuration has been created yet',
      }))
    })
    const store = makeStore(s.baseUrl)

    await expect(store.getFPPEndpointsConfig()).rejects.toMatchObject({ status: 404 })
  })

  it('putFPPEndpointsConfig() PUTs the payload and returns the new active revision', async () => {
    let gotMethod = ''
    let gotBody = ''
    const s = await server((req, res) => {
      gotMethod = req.method ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        gotBody = Buffer.concat(chunks).toString('utf-8')
        respondJson(res, 200, { ...fppEndpointsConfigResponse, revision: 2 })
      })
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.putFPPEndpointsConfig({ endpoints: [{ id: 'shed', url: 'http://10.0.1.21' }] })

    expect(gotMethod).toBe('PUT')
    expect(JSON.parse(gotBody)).toEqual({ endpoints: [{ id: 'shed', url: 'http://10.0.1.21' }] })
    expect(resp.revision).toBe(2)
  })

  it('putFPPEndpointsConfig() rejects with the coordinator’s validation error, not a fabricated success', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 400, makeProblem({
        type: 'https://showmesh.dev/problems/invalid-parameter', status: 400,
        detail: 'instance id "bad id" is not valid',
      }))
    })
    const store = makeStore(s.baseUrl)

    await expect(store.putFPPEndpointsConfig({ endpoints: [{ id: 'bad id', url: 'http://x' }] }))
      .rejects.toThrow('not valid')
  })

  it('getFPPEndpointsConfigRevisions() returns revision history', async () => {
    let gotPath = ''
    const s = await server((req, res) => {
      gotPath = req.url ?? ''
      respondJson(res, 200, {
        serverTime: new Date().toISOString(),
        kind: 'fpp.endpoints',
        revisions: [
          { revision: 2, createdAt: new Date().toISOString(), createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true },
          { revision: 1, createdAt: new Date().toISOString(), createdByPrincipalId: null, createdByPrincipalName: null, source: 'env_migration', note: 'migrated', active: false },
        ],
      })
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.getFPPEndpointsConfigRevisions()

    expect(gotPath).toBe('/config/fpp.endpoints/revisions')
    expect(resp.revisions).toHaveLength(2)
    expect(resp.revisions[0]?.active).toBe(true)
    expect(resp.revisions[1]?.createdByPrincipalName).toBeNull()
  })
})

// Track D seam D-2a (ADR-032): getResolumeComposition is a plain
// ApiClient.getJson pass-through, same as getFPPEndpointsConfig above, so
// it is proven the same way. uploadResolumeComposition is NOT — it
// bypasses ApiClient entirely for XMLHttpRequest's real upload progress
// (resolumeCompositionUpload.ts's own header comment) — this ApiStore
// method's own job is only wiring `this.baseUrl` through correctly, since
// resolumeCompositionUpload.test.ts already proves uploadFileWithProgress
// itself sends real multipart bytes and classifies every response. The
// upload test below needs `Access-Control-*` response headers for the
// identical reason resolumeCompositionUpload.test.ts's own corsServer
// does: this store's `getJson` goes through `fetch`, which node's
// implementation does not CORS-gate (client.test.ts/store.test.ts's
// existing pattern never needed this), but uploadResolumeComposition goes
// through jsdom's real, CORS-enforcing XMLHttpRequest.
describe('Resolume composition (Track D seam D-2a, ADR-032)', () => {
  const compositionSummary = {
    name: 'Christmas 25',
    sourceFilename: 'Christmas 25.avc',
    contentHash: 'sha256:abc',
    sizeBytes: 1024,
    writtenBy: { product: 'Arena', major: 7, minor: 23, micro: 2, revision: 0 },
    canvas: { width: 1920, height: 1080 },
    decks: [],
    layerCount: 0,
    layerGroupCount: 0,
    columnCount: 0,
    clipCount: 0,
    persistentClipCount: 0,
  }

  it('getResolumeComposition() GETs the stored composition and returns the decoded response', async () => {
    let gotPath = ''
    const s = await server((req, res) => {
      gotPath = req.url ?? ''
      respondJson(res, 200, {
        serverTime: '2026-08-14T00:00:00Z',
        revision: 3,
        activatedAt: '2026-08-14T00:00:00Z',
        composition: compositionSummary,
        decks: [],
        layerGroups: [],
        layers: [],
        columns: [],
        clips: [],
        persistentClips: [],
      })
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.getResolumeComposition()

    expect(gotPath).toBe('/config/resolume/composition')
    expect(resp.composition.name).toBe('Christmas 25')
    expect(resp.revision).toBe(3)
  })

  it('getResolumeComposition() rejects with a typed error on 404 (nothing uploaded yet)', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 404, makeProblem({
        type: 'https://showmesh.dev/problems/resource-not-found', status: 404,
        detail: 'no Resolume composition has been uploaded yet; upload a composition file to create one',
      }))
    })
    const store = makeStore(s.baseUrl)

    await expect(store.getResolumeComposition()).rejects.toMatchObject({ status: 404 })
  })

  it("uploadResolumeComposition() POSTs the file via this store's own baseUrl and returns the new revision", async () => {
    let gotPath = ''
    let gotBody = ''
    const s = await startTestServer((req, res) => {
      const origin = req.headers.origin ?? '*'
      res.setHeader('Access-Control-Allow-Origin', origin)
      res.setHeader('Access-Control-Allow-Credentials', 'true')
      res.setHeader('Access-Control-Expose-Headers', 'ShowMesh-API-Version, Retry-After')
      if (req.method === 'OPTIONS') {
        res.writeHead(204, {
          'Access-Control-Allow-Headers': 'Content-Type, ShowMesh-API-Version, Authorization',
          'Access-Control-Allow-Methods': 'POST',
        })
        res.end()
        return
      }
      gotPath = req.url ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        gotBody = Buffer.concat(chunks).toString('latin1')
        respondJson(res, 200, {
          serverTime: '2026-08-14T01:00:00Z',
          revision: 4,
          activatedAt: '2026-08-14T01:00:00Z',
          composition: compositionSummary,
        })
      })
    })
    openedServers.push(s)
    const store = makeStore(s.baseUrl)
    const progressCalls: { loaded: number; total: number | null }[] = []

    const file = new File(['<Composition/>'], 'Christmas 25.avc', { type: 'application/octet-stream' })
    const resp = await store.uploadResolumeComposition(file, (p) => progressCalls.push(p))

    expect(gotPath).toBe('/config/resolume/composition')
    expect(gotBody).toContain('name="file"')
    expect(gotBody).toContain('Christmas 25.avc')
    expect(resp.revision).toBe(4)
  })
})

// CLAUDE.md DEFECT 2: before this block, runDiscovery/declareNode/
// deleteNodeDeclaration had NO test asserting the actual HTTP method,
// path, or request body — every one of the 331 tests already passing
// could not have noticed a wrong path (`/discovery/runsXX`), a wrong
// body key (`{labelX, notesX}`), or the missing-required `confirm: true`
// on the delete. views/NodesList.test.tsx mocks all three at the '../api'
// boundary (isolating its OWN branching, correctly — see that file's own
// comment), which means nothing anywhere proved these methods issue the
// request the server actually expects. Same style as the seam A block
// immediately above: real path/method/body assertions against a real
// node:http server, no store.connect() needed since none of these three
// touch the read loop (see store.ts's own "Step 7 seam B" section
// comment for why).
describe('ApiStore: Step 7 seam B (RES-008 D2/D6) — discovery and declaration', () => {
  it('runDiscovery() POSTs to /discovery/runs with no body and returns the run and proposals as-is', async () => {
    let gotMethod = ''
    let gotPath = ''
    let gotBody = ''
    const s = await server((req, res) => {
      gotMethod = req.method ?? ''
      gotPath = req.url ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        gotBody = Buffer.concat(chunks).toString('utf-8')
        respondJson(res, 200, {
          serverTime: '2026-08-12T22:00:00Z',
          run: {
            id: 'run-1', startedAt: '2026-08-12T22:00:00Z', finishedAt: '2026-08-12T22:00:01Z',
            complete: true, reason: null, foundCount: 1,
            initiatedByPrincipalId: 'p-1', initiatedByPrincipalName: 'admin-1',
          },
          proposals: [{ nodeId: 'shed-01', source: 'node' }],
        })
      })
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.runDiscovery()

    expect(gotMethod).toBe('POST')
    expect(gotPath).toBe('/discovery/runs')
    // No body at all — client.ts only sets Content-Type/encodes a body
    // when init.body !== undefined, and runDiscovery calls postJson with
    // `undefined`. A stray `{}` or any other body here would be this
    // method silently starting to send something the contract never
    // asked for.
    expect(gotBody).toBe('')
    expect(resp.run.id).toBe('run-1')
    expect(resp.proposals).toEqual([{ nodeId: 'shed-01', source: 'node' }])
  })

  it('runDiscovery() rejects on a 403 (missing config:write) rather than resolving with a fabricated run', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 403, {
        type: 'https://showmesh.dev/problems/forbidden', title: 'Forbidden', status: 403,
        detail: 'this action requires the config:write scope', serverTime: '2026-08-12T22:00:00Z',
      })
    })
    const store = makeStore(s.baseUrl)

    await expect(store.runDiscovery()).rejects.toThrow(/config:write/)
  })

  it('declareNode() POSTs the exact {label, notes} body to /nodes/{nodeId}/declaration', async () => {
    let gotMethod = ''
    let gotPath = ''
    let gotBody = ''
    const s = await server((req, res) => {
      gotMethod = req.method ?? ''
      gotPath = req.url ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        gotBody = Buffer.concat(chunks).toString('utf-8')
        respondJson(res, 200, {
          serverTime: '2026-08-12T22:00:00Z',
          declaration: {
            declared: true, label: 'Shed', notes: 'north fence',
            declaredAt: '2026-08-12T22:00:00Z', declaredByPrincipalId: 'p-1', declaredByPrincipalName: 'admin-1',
            discoveryState: 'present', discoveryReason: null, lastDiscoveryRunId: 'run-1',
            lastDiscoveredAt: '2026-08-12T22:00:00Z', notSeenAsOfRunId: null, notSeenAsOfRunFinishedAt: null,
          },
        })
      })
    })
    const store = makeStore(s.baseUrl)

    const resp = await store.declareNode('shed-01', 'Shed', 'north fence')

    expect(gotMethod).toBe('POST')
    // encodeURIComponent(nodeId) — asserted with an id containing a
    // character that would otherwise reveal a raw, unencoded interpolation.
    expect(gotPath).toBe('/nodes/shed-01/declaration')
    expect(JSON.parse(gotBody)).toEqual({ label: 'Shed', notes: 'north fence' })
    expect(resp.declaration.declared).toBe(true)
    expect(resp.declaration.label).toBe('Shed')
  })

  it('declareNode() percent-encodes a nodeId containing characters that are not URL-safe', async () => {
    let gotPath = ''
    const s = await server((req, res) => {
      gotPath = req.url ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        respondJson(res, 200, {
          serverTime: '2026-08-12T22:00:00Z',
          declaration: {
            declared: true, label: '', notes: '',
            declaredAt: '2026-08-12T22:00:00Z', declaredByPrincipalId: 'p-1', declaredByPrincipalName: 'admin-1',
            discoveryState: 'unknown', discoveryReason: 'no discovery run history is available',
            lastDiscoveryRunId: null, lastDiscoveredAt: null, notSeenAsOfRunId: null, notSeenAsOfRunFinishedAt: null,
          },
        })
      })
    })
    const store = makeStore(s.baseUrl)

    await store.declareNode('node/with slash', '', '')

    expect(gotPath).toBe('/nodes/node%2Fwith%20slash/declaration')
  })

  it('declareNode() rejects on a 500 (e.g. the audit-write same-transaction refusal) rather than resolving', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 500, makeProblem({ status: 500, detail: 'could not record this write in the audit log' }))
    })
    const store = makeStore(s.baseUrl)

    await expect(store.declareNode('shed-01', '', '')).rejects.toThrow(/audit log/)
  })

  it('deleteNodeDeclaration() sends DELETE with {"confirm":true} to /nodes/{nodeId}/declaration — never false, never omitted', async () => {
    let gotMethod = ''
    let gotPath = ''
    let gotBody = ''
    const s = await server((req, res) => {
      gotMethod = req.method ?? ''
      gotPath = req.url ?? ''
      const chunks: Buffer[] = []
      req.on('data', (c: Buffer) => chunks.push(c))
      req.on('end', () => {
        gotBody = Buffer.concat(chunks).toString('utf-8')
        res.writeHead(204, { 'ShowMesh-API-Version': '1' })
        res.end()
      })
    })
    const store = makeStore(s.baseUrl)

    await store.deleteNodeDeclaration('shed-01')

    expect(gotMethod).toBe('DELETE')
    expect(gotPath).toBe('/nodes/shed-01/declaration')
    // The server REQUIRES this exact body (BUILD-PLAN Step 7 seam B B2):
    // a `false`, an omitted key, or any other shape is a silent refusal
    // this client must never risk sending.
    expect(JSON.parse(gotBody)).toEqual({ confirm: true })
  })

  it('deleteNodeDeclaration() rejects on a 409 (e.g. confirm not honored) rather than resolving as if it succeeded', async () => {
    const s = await server((_req, res) => {
      respondProblem(res, 409, makeProblem({ status: 409, detail: 'declaration removal was not confirmed' }))
    })
    const store = makeStore(s.baseUrl)

    await expect(store.deleteNodeDeclaration('shed-01')).rejects.toThrow(/not confirmed/)
  })
})

describe('ApiStore: Step 7 seam C — stopFPPPlaylist (this application\'s first write)', () => {
  it('POSTs the exact request shape (action, a minted idempotencyKey) and returns command as-is, never inferring success from the HTTP round trip alone', async () => {
    let gotMethod = ''
    let gotPath = ''
    let gotBody: { action?: string; idempotencyKey?: string } = {}
    const s = await server((req, res) => {
      if (req.url === '/fpp/bench-fpp/commands' && req.method === 'POST') {
        gotMethod = req.method
        gotPath = req.url
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          gotBody = JSON.parse(Buffer.concat(chunks).toString('utf-8'))
          respondJson(res, 200, {
            serverTime: '2026-08-12T22:00:00Z',
            command: {
              id: 'cmd-1', idempotencyKey: gotBody.idempotencyKey, action: 'fpp.stop_playlist',
              instanceId: 'bench-fpp', replay: false, outcome: 'unconfirmed', outcomeState: 'current',
              outcomeReason: 'observed fpp.status = "playing", want "idle"', attributionDegraded: false,
              dispatchedAt: '2026-08-12T22:00:00Z', resolvedAt: '2026-08-12T22:00:20Z',
            },
          })
        })()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    const result = await store.stopFPPPlaylist('bench-fpp')

    expect(gotMethod).toBe('POST')
    expect(gotPath).toBe('/fpp/bench-fpp/commands')
    expect(gotBody.action).toBe('stopPlaylist')
    expect(typeof gotBody.idempotencyKey).toBe('string')
    expect(gotBody.idempotencyKey).not.toBe('')

    // The load-bearing property (ADR-003): a resolved Promise here is
    // NOT success — it is a successful HTTP round trip carrying whatever
    // outcome the coordinator actually reported, unconfirmed included.
    expect(result.outcome).toBe('unconfirmed')
    expect(result.outcomeReason).toContain('playing')
  })

  it('mints a distinct idempotencyKey on each call, never reusing one across two genuinely separate invocations', async () => {
    const seenKeys: string[] = []
    const s = await server((req, res) => {
      if (req.url === '/fpp/bench-fpp/commands' && req.method === 'POST') {
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          const body = JSON.parse(Buffer.concat(chunks).toString('utf-8')) as { idempotencyKey: string }
          seenKeys.push(body.idempotencyKey)
          respondJson(res, 200, {
            serverTime: '2026-08-12T22:00:00Z',
            command: {
              id: 'cmd-1', idempotencyKey: body.idempotencyKey, action: 'fpp.stop_playlist',
              instanceId: 'bench-fpp', replay: false, outcome: 'confirmed', outcomeState: 'current',
              outcomeReason: '', attributionDegraded: false,
              dispatchedAt: '2026-08-12T22:00:00Z', resolvedAt: '2026-08-12T22:00:00Z',
            },
          })
        })()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    await store.stopFPPPlaylist('bench-fpp')
    await store.stopFPPPlaylist('bench-fpp')

    expect(seenKeys).toHaveLength(2)
    expect(seenKeys[0]).not.toBe(seenKeys[1])
  })

  // The actual defect this project shipped (CLAUDE.md's DEFECT 1):
  // `crypto.randomUUID()` is `[SecureContext]`-gated and is simply
  // ABSENT — not thrown, not caught — on the plain http:// origin this
  // UI deploys to (ADR-022, deploy/README.md's http://<host>:8081). Node
  // exposes it unconditionally, so this test removes it from the global
  // explicitly to simulate exactly what a real browser on that origin
  // already does on its own, and proves the write still goes out with a
  // well-formed v4 idempotency key via the getRandomValues fallback
  // (uuid.ts) rather than throwing `TypeError: crypto.randomUUID is not
  // a function` and never leaving the browser at all.
  it('still sends a valid v4 idempotencyKey when crypto.randomUUID is unavailable (secure-context restriction)', async () => {
    let gotBody: { idempotencyKey?: string } = {}
    const s = await server((req, res) => {
      if (req.url === '/fpp/bench-fpp/commands' && req.method === 'POST') {
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          gotBody = JSON.parse(Buffer.concat(chunks).toString('utf-8'))
          respondJson(res, 200, {
            serverTime: '2026-08-12T22:00:00Z',
            command: {
              id: 'cmd-1', idempotencyKey: gotBody.idempotencyKey, action: 'fpp.stop_playlist',
              instanceId: 'bench-fpp', replay: false, outcome: 'confirmed', outcomeState: 'current',
              outcomeReason: '', attributionDegraded: false,
              dispatchedAt: '2026-08-12T22:00:00Z', resolvedAt: '2026-08-12T22:00:00Z',
            },
          })
        })()
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    const originalRandomUUID = globalThis.crypto.randomUUID
    // Assignment, not `delete`: Node's Crypto defines randomUUID on its
    // PROTOTYPE, so `delete globalThis.crypto.randomUUID` silently no-ops
    // (deleting a non-existent own property) and the real method keeps
    // answering right through the "removed" API — see uuid.test.ts's
    // withoutRandomUUID comment for the full explanation of why this
    // would otherwise be a test that passes for the wrong reason.
    // @ts-expect-error deliberately simulating a secure-context-gated
    // absence, which TypeScript's lib.dom.d.ts does not model as optional.
    globalThis.crypto.randomUUID = undefined
    try {
      await store.stopFPPPlaylist('bench-fpp')
    } finally {
      globalThis.crypto.randomUUID = originalRandomUUID
    }

    expect(gotBody.idempotencyKey).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i,
    )
  })

  it('rejects on a 403 (missing fpp:command) rather than resolving with a fabricated result', async () => {
    const s = await server((req, res) => {
      if (req.url === '/fpp/bench-fpp/commands' && req.method === 'POST') {
        respondProblem(res, 403, {
          type: 'https://showmesh.dev/problems/forbidden', title: 'Forbidden', status: 403,
          detail: 'this action requires the fpp:command scope', serverTime: '2026-08-12T22:00:00Z',
        })
        return
      }
      res.writeHead(404).end()
    })

    const store = makeStore(s.baseUrl)
    await expect(store.stopFPPPlaylist('bench-fpp')).rejects.toThrow(/fpp:command/)
  })

  // Step 7 seam C review defect 1: stopFPPPlaylist must use
  // FPP_COMMAND_REQUEST_TIMEOUT_MS (35s), never the store's own shorter
  // default request budget — a command dispatch is a long request by
  // design (the coordinator waits out its own confirmation deadline
  // before answering) and sharing every other route's short snapshot-read
  // timeout made the coordinator's honest "unconfirmed" outcome
  // unreachable: this client aborted first, every time.
  it('uses FPP_COMMAND_REQUEST_TIMEOUT_MS, not the store-wide default request budget, for this call', async () => {
    // A mutable holder object, not a bare reassigned `let`: this file's
    // pattern of a nested async closure assigning into an outer variable
    // read much later (past an `await`) is what several tests below this
    // one already do with a bare `let`, but TypeScript's control-flow
    // narrowing for THIS particular shape (checked once inside a
    // `waitFor` callback, called once several lines after) narrowed the
    // variable to `never` at the call site despite the explicit
    // `(() => void) | null` annotation — a false positive, not a real
    // type error (this file's whole suite runs on esbuild's stripped
    // transform, which does not type-check at all). Wrapping it in an
    // object sidesteps the narrowing entirely rather than fighting it.
    const release: { fn: (() => void) | null } = { fn: null }
    const s = await server((req, res) => {
      if (req.url === '/fpp/bench-fpp/commands' && req.method === 'POST') {
        void (async () => {
          const chunks: Buffer[] = []
          for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
          // Hold the response open until the test explicitly releases it,
          // well after advancing past the store's own shorter default.
          release.fn = () => {
            respondJson(res, 200, {
              serverTime: '2026-08-12T22:00:00Z',
              command: {
                id: 'cmd-1', idempotencyKey: 'k', action: 'fpp.stop_playlist',
                instanceId: 'bench-fpp', replay: false, outcome: 'confirmed', outcomeState: 'current',
                outcomeReason: '', attributionDegraded: false,
                dispatchedAt: '2026-08-12T22:00:00Z', resolvedAt: '2026-08-12T22:00:00Z',
              },
            })
          }
        })()
        return
      }
      res.writeHead(404).end()
    })

    // A FakeClock (test-support/fake-clock.ts), same reasoning as the
    // request-timeout reconnect test above: virtual time only advances
    // when this test calls clock.advance(), so this cannot flake on real
    // scheduling jitter. requestTimeoutMs: 40 is the store-WIDE default
    // this call must NOT be bound by.
    const clock = new FakeClock()
    const store = makeStore(s.baseUrl, { clock, requestTimeoutMs: 40 })

    const resultPromise = store.stopFPPPlaylist('bench-fpp')
    await waitFor(() => release.fn !== null, { message: 'the POST was never received by the test server' })
    if (release.fn === null) throw new Error('release.fn not set')

    // Advance FAR past the store's own 40ms default (which would already
    // have aborted a request bound by it) but still well under
    // FPP_COMMAND_REQUEST_TIMEOUT_MS (35_000ms) — proving THIS call used
    // the longer budget.
    clock.advance(5_000)
    release.fn()

    const result = await resultPromise
    expect(result.outcome).toBe('confirmed')
  })
})

// Step 8, ADR-015: FPPCommandRequest.params used to be generated as
// Record<string, never> (api/openapi.yaml declared it as a bare object
// with no JSON Schema `properties`), a type no non-empty object
// satisfies — so nothing anywhere could ever have caught a misspelled
// param name; the entire request shape was checked exactly once, at
// runtime, server-side. api/openapi.yaml now models it as a discriminated
// `oneOf` on `action` (StartPlaylistCommandRequest and its seven
// siblings), and dispatchFPPCommand (store.ts) uses that generated type
// directly (FPPCommandDispatchArgs) rather than a hand-typed
// `Record<string, unknown>`. This block proves the generated type
// actually rejects what it claims to, not merely that it compiles for the
// one shape a hand-written call site happens to use.
//
// This is a COMPILE-TIME-only check: vitest runs this file through
// esbuild's stripped transform (see the "uses
// FPP_COMMAND_REQUEST_TIMEOUT_MS..." test above, same file), which does
// not type-check at all — `npm test` passing proves nothing about this
// block. Only `npm run typecheck` (tsc --noEmit) does. Verified by hand
// for this task, per CLAUDE.md's "a test's name is a claim": with the
// `@ts-expect-error` comment below temporarily REMOVED, `npm run
// typecheck` fails with three real compiler errors on the `params:`
// line — `error TS2322: Type 'string' is not assignable to type
// 'never'.` (and the same for `false`/`"refuse"`), because once
// `paylist` fails to match ANY oneOf member, TypeScript's discriminated
// union resolution has nothing left to narrow to and falls back to
// `never` for every sibling property in that same object literal — not
// a silent pass. The comment is restored below; a `@ts-expect-error`
// that stops being necessary (the type regresses back to something
// permissive) is ITSELF a `typecheck` failure ("Unused '@ts-expect-error'
// directive"), so this check cannot silently rot into a no-op either.
describe('FPPCommandRequest params (type-level only, Step 8/ADR-015)', () => {
  it('accepts a real startPlaylist params object, and the generated type rejects a misspelled one at compile time', () => {
    type FPPCommandRequest = components['schemas']['FPPCommandRequest']

    // The PASSING form: every real property, correctly typed. This is
    // exactly the shape ApiStore.startFPPPlaylist builds.
    const valid: FPPCommandRequest = {
      action: 'startPlaylist',
      idempotencyKey: 'key-1',
      params: { playlist: 'showmesh-test', repeat: false, ifBusy: 'refuse' },
    }
    expect(valid.action).toBe('startPlaylist')

    // The FAILING form: `paylist` (missing the second `l`) is not a
    // property `StartPlaylistCommandRequest.params` declares — a typo
    // this schema shape must refuse to compile, not silently accept as
    // an "extra" key the way `Record<string, unknown>` would have. The
    // whole `params` object is kept on ONE line deliberately: TypeScript
    // reports the excess-property error AND a cascade of "not assignable
    // to type 'never'" errors for `repeat`/`ifBusy` once the union match
    // fails, all on whichever single line the object literal occupies —
    // `@ts-expect-error` only suppresses diagnostics on the line
    // immediately below it, so splitting this across multiple lines would
    // leave the cascade errors unsuppressed and this file would fail
    // `npm run typecheck` for the wrong reason.
    const misspelled: FPPCommandRequest = {
      action: 'startPlaylist',
      idempotencyKey: 'key-1',
      // @ts-expect-error 'paylist' is a typo for 'playlist' and must not typecheck.
      params: { paylist: 'showmesh-test', repeat: false, ifBusy: 'refuse' },
    }
    expect(misspelled.action).toBe('startPlaylist')
  })
})
