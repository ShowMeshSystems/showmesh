/**
 * ADR-024's additions to ApiClient: POST/DELETE with a JSON body, the
 * explicit `credentials: 'same-origin'` request option, and the three new
 * dispatchable problem types (forbidden, csrf-rejected, too-many-requests).
 * Follows this project's existing posture (store.test.ts's header comment,
 * spec section 5.7): a real `node:http` server, real bytes, no mocked
 * `fetch` response behavior.
 */
import { afterEach, describe, expect, it } from 'vitest'
import { ApiClient } from './client'
import { CSRFRejectedError, ForbiddenError, TooManyRequestsError, UnauthorizedError } from './errors'
import { startTestServer, type TestServer } from './test-support/test-server'
import { makeProblem } from './test-support/fixtures'

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
