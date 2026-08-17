/**
 * ADR-024's additions to ApiClient: POST/DELETE with a JSON body, the
 * explicit `credentials: 'same-origin'` request option, and the three new
 * dispatchable problem types (forbidden, csrf-rejected, too-many-requests).
 * Follows this project's existing posture (store.test.ts's header comment,
 * spec section 5.7): a real `node:http` server, real bytes, no mocked
 * `fetch` response behavior.
 */
import { afterEach, describe, expect, it } from 'vitest'
import {
  ApiClient,
  FPP_COMMAND_REQUEST_TIMEOUT_MS,
  MIN_FPP_COMMAND_CLIENT_TIMEOUT_MS,
  MIN_RENDER_COMMAND_CLIENT_TIMEOUT_MS,
  MIN_RESOLUME_ACTION_CLIENT_TIMEOUT_MS,
  MIN_RESOLUME_RECOVERY_RESTORE_SERVER_BOUND_MS,
  RENDER_COMMAND_REQUEST_TIMEOUT_MS,
  RESOLUME_ACTION_REQUEST_TIMEOUT_MS,
  RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS,
} from './client'
import { CSRFRejectedError, ForbiddenError, TooManyRequestsError, UnauthorizedError } from './errors'
import { startTestServer, waitFor, type TestServer } from './test-support/test-server'
import { makeProblem } from './test-support/fixtures'
import { FakeClock } from './test-support/fake-clock'

const openedServers: TestServer[] = []

async function server(handler: Parameters<typeof startTestServer>[0]): Promise<TestServer> {
  const s = await startTestServer(handler)
  openedServers.push(s)
  return s
}

afterEach(async () => {
  for (const s of openedServers.splice(0)) await s.close()
})

function respondJson(res: import('node:http').ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/json', 'ShowMesh-API-Version': '1' })
  res.end(JSON.stringify(body))
}

function respondProblem(
  res: import('node:http').ServerResponse,
  status: number,
  body: unknown,
  extraHeaders: Record<string, string> = {},
): void {
  res.writeHead(status, {
    'Content-Type': 'application/problem+json',
    'ShowMesh-API-Version': '1',
    ...extraHeaders,
  })
  res.end(JSON.stringify(body))
}

async function readBody(req: import('node:http').IncomingMessage): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of req as AsyncIterable<Buffer>) chunks.push(chunk)
  return Buffer.concat(chunks).toString('utf-8')
}

describe('ApiClient.postJson', () => {
  it('sends POST with a JSON-encoded body and Content-Type, and returns the parsed response', async () => {
    let recordedMethod = ''
    let recordedContentType: string | undefined
    let recordedBody = ''
    const s = await server((req, res) => {
      recordedMethod = req.method ?? ''
      recordedContentType = req.headers['content-type']
      void readBody(req).then((body) => {
        recordedBody = body
        respondJson(res, 200, { ok: true })
      })
    })
    const client = new ApiClient(s.baseUrl)

    const result = await client.postJson<{ ok: boolean }>(
      '/session',
      { name: 'alice', password: 'secret', deviceLabel: 'porch tablet' },
      new AbortController().signal,
    )

    expect(recordedMethod).toBe('POST')
    expect(recordedContentType).toBe('application/json')
    expect(JSON.parse(recordedBody)).toEqual({ name: 'alice', password: 'secret', deviceLabel: 'porch tablet' })
    expect(result).toEqual({ ok: true })
  })
})

describe('ApiClient.putJson', () => {
  it('sends PUT with a JSON-encoded body and Content-Type, and returns the parsed response', async () => {
    let recordedMethod = ''
    let recordedContentType: string | undefined
    let recordedBody = ''
    const s = await server((req, res) => {
      recordedMethod = req.method ?? ''
      recordedContentType = req.headers['content-type']
      void readBody(req).then((body) => {
        recordedBody = body
        respondJson(res, 200, { ok: true })
      })
    })
    const client = new ApiClient(s.baseUrl)

    const result = await client.putJson<{ ok: boolean }>(
      '/config/fpp.endpoints',
      { endpoints: [{ id: 'player-01', url: 'http://10.0.1.20' }] },
      new AbortController().signal,
    )

    expect(recordedMethod).toBe('PUT')
    expect(recordedContentType).toBe('application/json')
    expect(JSON.parse(recordedBody)).toEqual({ endpoints: [{ id: 'player-01', url: 'http://10.0.1.20' }] })
    expect(result).toEqual({ ok: true })
  })
})

describe('ApiClient.deleteJson', () => {
  it('sends DELETE with no body and no Content-Type when body is omitted, and returns undefined on 204', async () => {
    let recordedMethod = ''
    let recordedContentType: string | undefined
    let recordedBody = ''
    const s = await server((req, res) => {
      recordedMethod = req.method ?? ''
      recordedContentType = req.headers['content-type']
      void readBody(req).then((body) => {
        recordedBody = body
        res.writeHead(204, { 'ShowMesh-API-Version': '1' })
        res.end()
      })
    })
    const client = new ApiClient(s.baseUrl)

    const result = await client.deleteJson('/session', undefined, new AbortController().signal)

    expect(recordedMethod).toBe('DELETE')
    expect(recordedContentType).toBeUndefined()
    expect(recordedBody).toBe('')
    expect(result).toBeUndefined()
  })

  it('sends DELETE with a JSON body when one is supplied', async () => {
    let recordedBody = ''
    const s = await server((req, res) => {
      void readBody(req).then((body) => {
        recordedBody = body
        res.writeHead(204, { 'ShowMesh-API-Version': '1' })
        res.end()
      })
    })
    const client = new ApiClient(s.baseUrl)

    await client.deleteJson('/session', { sessionId: 'sess-2' }, new AbortController().signal)

    expect(JSON.parse(recordedBody)).toEqual({ sessionId: 'sess-2' })
  })
})

describe('ApiClient request credentials', () => {
  it('always requests credentials: same-origin, verified via a recording passthrough over the real fetch', async () => {
    // A passthrough, not a mock of behavior: this still calls the real
    // global `fetch` and gets a real response over a real socket (per
    // this project's "do not mock fetch" posture) — it additionally
    // records what ApiClient constructed, which is the only way to
    // observe `credentials` at all: it is a browser-only cookie-jar
    // instruction with no wire representation for a real HTTP server (or
    // Node's own fetch) to see.
    const recordedInits: RequestInit[] = []
    const recordingFetch: typeof fetch = (input, init) => {
      if (init !== undefined) recordedInits.push(init)
      return fetch(input, init)
    }

    const s = await server((_req, res) => respondJson(res, 200, { ok: true }))
    const client = new ApiClient(s.baseUrl, recordingFetch)

    await client.getJson('/snapshot', new AbortController().signal)
    await client.postJson('/session', { a: 1 }, new AbortController().signal)
    await client.deleteJson('/session', undefined, new AbortController().signal)

    expect(recordedInits.length).toBe(3)
    for (const init of recordedInits) {
      expect(init.credentials).toBe('same-origin')
    }
  })
})

describe('ApiClient error dispatch (ADR-024)', () => {
  it('dispatches a 403 forbidden problem to ForbiddenError, carrying the missing-scope detail', async () => {
    const s = await server((_req, res) =>
      respondProblem(res, 403, makeProblem({
        type: 'https://showmesh.dev/problems/forbidden',
        status: 403,
        detail: 'this principal does not hold the required scope: show:macro:run',
      })),
    )
    const client = new ApiClient(s.baseUrl)

    await expect(client.getJson('/audit', new AbortController().signal)).rejects.toThrow(ForbiddenError)
    try {
      await client.getJson('/audit', new AbortController().signal)
      expect.unreachable()
    } catch (err) {
      expect(err).toBeInstanceOf(ForbiddenError)
      expect((err as ForbiddenError).message).toContain('show:macro:run')
      // Must never be confused with a plain 401 (no credential at all) —
      // this is a DIFFERENT class, not an UnauthorizedError with status 403.
      expect(err).not.toBeInstanceOf(UnauthorizedError)
    }
  })

  it('dispatches a 403 csrf-rejected problem to CSRFRejectedError, distinct from ForbiddenError', async () => {
    const s = await server((_req, res) =>
      respondProblem(res, 403, makeProblem({
        type: 'https://showmesh.dev/problems/csrf-rejected',
        status: 403,
        detail: 'a cookie-authenticated write requires Sec-Fetch-Site: same-origin',
      })),
    )
    const client = new ApiClient(s.baseUrl)

    try {
      await client.deleteJson('/session', undefined, new AbortController().signal)
      expect.unreachable()
    } catch (err) {
      expect(err).toBeInstanceOf(CSRFRejectedError)
      // Same HTTP status as the forbidden case above, so this test's own
      // name is only true if dispatch is keyed on `type`, not `status` —
      // this assertion is what fails if a future edit dispatches by status.
      expect(err).not.toBeInstanceOf(ForbiddenError)
    }
  })

  it('dispatches a 429 too-many-requests problem to TooManyRequestsError, parsing Retry-After', async () => {
    const s = await server((_req, res) =>
      respondProblem(
        res,
        429,
        makeProblem({ type: 'https://showmesh.dev/problems/too-many-requests', status: 429, detail: 'slow down' }),
        { 'Retry-After': '7' },
      ),
    )
    const client = new ApiClient(s.baseUrl)

    try {
      await client.postJson('/session', {}, new AbortController().signal)
      expect.unreachable()
    } catch (err) {
      expect(err).toBeInstanceOf(TooManyRequestsError)
      expect((err as TooManyRequestsError).retryAfterSeconds).toBe(7)
    }
  })

  it('reports a null retryAfterSeconds when Retry-After is absent', async () => {
    const s = await server((_req, res) =>
      respondProblem(res, 429, makeProblem({ type: 'https://showmesh.dev/problems/too-many-requests', status: 429, detail: 'slow down' })),
    )
    const client = new ApiClient(s.baseUrl)

    try {
      await client.postJson('/session', {}, new AbortController().signal)
      expect.unreachable()
    } catch (err) {
      expect(err).toBeInstanceOf(TooManyRequestsError)
      expect((err as TooManyRequestsError).retryAfterSeconds).toBeNull()
    }
  })
})

// Step 8 client-side review, Finding 1: FPP_COMMAND_REQUEST_TIMEOUT_MS's
// own doc comment claimed "client.test.ts proves this client actually
// waits this long when given a slow response" while this file contained
// zero references to that constant or to 35_000 — proved vacuous by
// setting the constant to 6_000 (below the coordinator's own 20s
// confirmation deadline) and observing 389/389 tests, typecheck, lint,
// and build all still pass. At 6s the browser would abort BEFORE the
// coordinator answers, rendering a transport-timeout error for a
// successful conversation with a healthy coordinator — ADR-024 decision
// 7's inversion, on the browser side, which is the side that already
// shipped this exact defect once (Step 7 seam C review defect 1, at 15s).
//
// Both halves below were verified non-vacuous the same way: with
// FPP_COMMAND_REQUEST_TIMEOUT_MS temporarily set to 6_000, both tests in
// this describe block failed; restored to 35_000, both pass. See this
// task's own report for the exact commands run.
describe('FPP_COMMAND_REQUEST_TIMEOUT_MS', () => {
  it('is never below the reconciled server confirmation deadline plus margin', () => {
    // The static half of the reconciliation: MIN_FPP_COMMAND_CLIENT_TIMEOUT_MS
    // is a SEPARATE literal (client.ts's own doc comment on that constant),
    // not derived from FPP_COMMAND_REQUEST_TIMEOUT_MS, so this assertion
    // actually fails the day someone lowers the constant under test —
    // unlike a self-referential check, which would pass no matter what
    // FPP_COMMAND_REQUEST_TIMEOUT_MS was set to.
    expect(FPP_COMMAND_REQUEST_TIMEOUT_MS).toBeGreaterThanOrEqual(MIN_FPP_COMMAND_CLIENT_TIMEOUT_MS)
  })

  it('actually waits this long: a response arriving just before the deadline still succeeds', async () => {
    // The behavioral half the doc comment claims: a real node:http server
    // (this project's "do not mock fetch" posture) holds the response
    // open until this test releases it, well past every SHORTER budget
    // this client could plausibly have used by mistake (the 15s
    // DEFAULT_REQUEST_TIMEOUT_MS, or a broken constant like 6_000) but
    // still one millisecond short of FPP_COMMAND_REQUEST_TIMEOUT_MS
    // itself — proving this client does not abort a slow-but-healthy
    // conversation early.
    const release: { fn: (() => void) | null } = { fn: null }
    const s = await server((_req, res) => {
      release.fn = () => respondJson(res, 200, { ok: true })
    })

    // A FakeClock (test-support/fake-clock.ts), matching store.test.ts's
    // own reasoning: virtual time only advances when this test calls
    // clock.advance(), so a 35-second-scale deadline can be exercised
    // without a 35-second-slow test and without racing real scheduling
    // jitter.
    const clock = new FakeClock()
    const client = new ApiClient(s.baseUrl, fetch, FPP_COMMAND_REQUEST_TIMEOUT_MS, clock)

    const resultPromise = client.postJson<{ ok: boolean }>(
      '/fpp/bench-fpp/commands',
      { action: 'stopPlaylist' },
      new AbortController().signal,
    )
    await waitFor(() => release.fn !== null, {
      message: 'the POST was never received by the test server',
    })

    clock.advance(FPP_COMMAND_REQUEST_TIMEOUT_MS - 1)
    if (release.fn === null) throw new Error('release.fn not set')
    release.fn()

    const result = await resultPromise
    expect(result).toEqual({ ok: true })
  })
})

// D-4 review finding 2: MIN_RESOLUME_ACTION_CLIENT_TIMEOUT_MS and
// MIN_RESOLUME_RECOVERY_RESTORE_SERVER_BOUND_MS were exported but never
// referenced by any test — proved vacuous by setting
// RESOLUME_ACTION_CLIENT_MARGIN_MS to -49_000 (an 6_000ms budget against
// the 55s server deadline) and RESOLUME_RECOVERY_CLIENT_MARGIN_MS to
// -1_209_000 (a 6_000ms budget against the ~1,215,000ms server bound):
// all 596 tests, typecheck, lint and build passed regardless. The tests
// below fail on both mutations and pass at the shipped values (80s
// action, 1,245,000ms restore); see this task's own report for the exact
// commands run to confirm both directions.
describe('RESOLUME_ACTION_REQUEST_TIMEOUT_MS', () => {
  it('is never below the reconciled server write deadline plus round-trip margin', () => {
    // Mirrors cmd_resolume_action_test.go's own
    // TestMinResolumeActionClientTimeoutExceedsServerDefault: the target
    // is server deadline PLUS a margin for the round trip, never the
    // server deadline alone — a bound merely equal to it means the server
    // can still be answering when this client has already given up.
    expect(RESOLUME_ACTION_REQUEST_TIMEOUT_MS).toBeGreaterThanOrEqual(MIN_RESOLUME_ACTION_CLIENT_TIMEOUT_MS)
  })

  it('actually waits this long: a response arriving just before the deadline still succeeds', async () => {
    const release: { fn: (() => void) | null } = { fn: null }
    const s = await server((_req, res) => {
      release.fn = () => respondJson(res, 200, { ok: true })
    })

    const clock = new FakeClock()
    const client = new ApiClient(s.baseUrl, fetch, RESOLUME_ACTION_REQUEST_TIMEOUT_MS, clock)

    const resultPromise = client.postJson<{ ok: boolean }>(
      '/resolume/instance-1/actions',
      { action: 'launchClip' },
      new AbortController().signal,
    )
    await waitFor(() => release.fn !== null, {
      message: 'the POST was never received by the test server',
    })

    clock.advance(RESOLUME_ACTION_REQUEST_TIMEOUT_MS - 1)
    if (release.fn === null) throw new Error('release.fn not set')
    release.fn()

    const result = await resultPromise
    expect(result).toEqual({ ok: true })
  })
})

describe('RENDER_COMMAND_REQUEST_TIMEOUT_MS', () => {
  it('is never below the reconciled server write deadline plus round-trip margin', () => {
    // Mirrors renderdispatch_timeouts_test.go's own strict-ordering check
    // on the Go side: the target is server deadline PLUS a margin for the
    // round trip, never the server deadline alone.
    expect(RENDER_COMMAND_REQUEST_TIMEOUT_MS).toBeGreaterThanOrEqual(MIN_RENDER_COMMAND_CLIENT_TIMEOUT_MS)
  })

  it('actually waits this long: a response arriving just before the deadline still succeeds', async () => {
    const release: { fn: (() => void) | null } = { fn: null }
    const s = await server((_req, res) => {
      release.fn = () => respondJson(res, 200, { ok: true })
    })

    const clock = new FakeClock()
    const client = new ApiClient(s.baseUrl, fetch, RENDER_COMMAND_REQUEST_TIMEOUT_MS, clock)

    const resultPromise = client.postJson<{ ok: boolean }>(
      '/nodes/media-01/render/surfaces/wall-1/clear',
      { idempotencyKey: 'k1' },
      new AbortController().signal,
    )
    await waitFor(() => release.fn !== null, {
      message: 'the POST was never received by the test server',
    })

    clock.advance(RENDER_COMMAND_REQUEST_TIMEOUT_MS - 1)
    if (release.fn === null) throw new Error('release.fn not set')
    release.fn()

    const result = await resultPromise
    expect(result).toEqual({ ok: true })
  })
})

describe('RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS', () => {
  it('is strictly above the server own worst-case restore bound', () => {
    // Mirrors cmd_resolume_recovery_test.go's own
    // TestMinResolumeRecoveryRestoreClientTimeoutExceedsServerBound:
    // STRICT inequality, because the server spends time past its own
    // write-deadline computation (the post-restore audit write) before it
    // can answer, so a client budget merely equal to it is still the
    // Step 7 defect.
    expect(RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS).toBeGreaterThan(
      MIN_RESOLUME_RECOVERY_RESTORE_SERVER_BOUND_MS,
    )
  })

  it('actually waits this long: a response arriving just before the deadline still succeeds', async () => {
    const release: { fn: (() => void) | null } = { fn: null }
    const s = await server((_req, res) => {
      release.fn = () => respondJson(res, 200, { ok: true })
    })

    const clock = new FakeClock()
    const client = new ApiClient(s.baseUrl, fetch, RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS, clock)

    const resultPromise = client.postJson<{ ok: boolean }>(
      '/resolume/recovery/restore',
      {},
      new AbortController().signal,
    )
    await waitFor(() => release.fn !== null, {
      message: 'the POST was never received by the test server',
    })

    clock.advance(RESOLUME_RECOVERY_RESTORE_REQUEST_TIMEOUT_MS - 1)
    if (release.fn === null) throw new Error('release.fn not set')
    release.fn()

    const result = await resultPromise
    expect(result).toEqual({ ok: true })
  })
})
