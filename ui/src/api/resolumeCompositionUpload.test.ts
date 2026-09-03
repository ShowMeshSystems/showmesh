/**
 * Track D seam D-2a. Follows this project's own standing posture for
 * `src/api` (client.test.ts/store.test.ts's header comments, spec section
 * 5.7): a real `node:http` server, real bytes, no mocked `fetch`/`XHR`
 * behavior. This is the ONE place the "real request, not a stub" part of
 * the task spec's test list (item 2 — "posts multipart/form-data with the
 * part named file, and the file's bytes arrive... assert against a real
 * request") is actually proven: ResolumeCompositionUpload.test.tsx mocks
 * `../api` the same way every other view/component test in this codebase
 * does (Configuration.test.tsx's own header comment explains why: it
 * isolates the COMPONENT's branching, and the network behavior is already
 * proven here).
 *
 * CORS: jsdom's XMLHttpRequest enforces real CORS (verified empirically —
 * a request with no `Access-Control-Allow-Origin` response header to a
 * different origin than jsdom's own document URL fails with a CORS
 * error, exactly like a real browser). Production never hits this: the
 * UI is served SAME-ORIGIN with the coordinator behind ADR-022's proxy,
 * so no CORS headers exist or are needed there. This file's own test
 * server adds them purely so a `127.0.0.1:<random port>` test origin can
 * stand in for "same origin" — a test-harness concession, not something
 * production code does or needs.
 */
import { afterEach, describe, expect, it } from 'vitest'
import { uploadFileWithProgress, type UploadProgress } from './resolumeCompositionUpload'
import { AbortError, ApiError, ForbiddenError, UnauthorizedError } from './errors'
import { IncompatibleVersionError } from './errors'
import { getStoredToken, setStoredToken, clearStoredToken } from './token'
import { startTestServer, type TestServer } from './test-support/test-server'
import { makeProblem } from './test-support/fixtures'

const openedServers: TestServer[] = []

/**
 * Adds the CORS headers this file's own header comment explains, on top
 * of startTestServer's real node:http server. Reflects the request's own
 * `Origin` (rather than a wildcard) and sets
 * `Access-Control-Allow-Credentials: true`: `uploadFileWithProgress` sets
 * `xhr.withCredentials = true` unconditionally (mirroring `client.ts`'s
 * `credentials: 'same-origin'`, ADR-022), and the Fetch/CORS spec
 * forbids a wildcard `Access-Control-Allow-Origin` on any credentialed
 * request — jsdom enforces this correctly (found empirically: `*` alone
 * fails every request below with a CORS error, even though it worked
 * for the plain, non-credentialed smoke check this file's header comment
 * describes writing first).
 */
async function corsServer(
  handler: (req: import('node:http').IncomingMessage, res: import('node:http').ServerResponse) => void,
): Promise<TestServer> {
  const s = await startTestServer((req, res) => {
    const origin = req.headers.origin ?? '*'
    res.setHeader('Access-Control-Allow-Origin', origin)
    res.setHeader('Access-Control-Allow-Credentials', 'true')
    // Without this, jsdom's CORS-compliant XHR (found empirically, same
    // as this function's other two headers) hides every response header
    // outside the small CORS "simple" set from `getResponseHeader` on a
    // cross-origin response — which would make `checkApiVersionHeaderValue`
    // see `null` for `ShowMesh-API-Version` on every response below,
    // indistinguishable from the coordinator genuinely never sending it.
    // Same-origin production traffic never needs this: same-origin
    // responses expose every header to script by default.
    res.setHeader('Access-Control-Expose-Headers', 'ShowMesh-API-Version, Retry-After')
    if (req.method === 'OPTIONS') {
      res.writeHead(204, {
        'Access-Control-Allow-Headers': 'Content-Type, ShowMesh-API-Version, Authorization',
        'Access-Control-Allow-Methods': 'POST',
      })
      res.end()
      return
    }
    handler(req, res)
  })
  openedServers.push(s)
  return s
}

afterEach(async () => {
  clearStoredToken()
  for (const s of openedServers.splice(0)) await s.close()
})

function readBody(req: import('node:http').IncomingMessage): Promise<Buffer> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks)))
  })
}

function respondJson(res: import('node:http').ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/json', 'ShowMesh-API-Version': '1' })
  res.end(JSON.stringify(body))
}

function respondProblem(res: import('node:http').ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/problem+json', 'ShowMesh-API-Version': '1' })
  res.end(JSON.stringify(body))
}

function makeFile(bytes: number[], name = 'Christmas 25.avc'): File {
  return new File([new Uint8Array(bytes)], name, { type: 'application/octet-stream' })
}

describe('uploadFileWithProgress', () => {
  it('POSTs real multipart/form-data with a part named "file" carrying the exact bytes, and resolves with the parsed response', async () => {
    let receivedContentType = ''
    let receivedBody = ''
    let receivedMethod = ''
    let receivedVersionHeader: string | null = null
    const s = await corsServer((req, res) => {
      receivedMethod = req.method ?? ''
      receivedContentType = req.headers['content-type'] ?? ''
      receivedVersionHeader = req.headers['showmesh-api-version'] as string | undefined ?? null
      void readBody(req).then((buf) => {
        receivedBody = buf.toString('latin1')
        respondJson(res, 200, { serverTime: '2026-08-14T00:00:00Z', revision: 1, activatedAt: '2026-08-14T00:00:00Z', composition: { name: 'Christmas 25' } })
      })
    })

    const file = makeFile([1, 2, 3, 4, 5, 255, 0, 128])
    const progressEvents: UploadProgress[] = []
    const result = await uploadFileWithProgress<{ revision: number }>(
      s.baseUrl,
      '/config/resolume/composition',
      file,
      (p) => progressEvents.push(p),
      new AbortController().signal,
    )

    expect(receivedMethod).toBe('POST')
    expect(receivedContentType).toContain('multipart/form-data')
    expect(receivedVersionHeader).toBe('1')
    expect(receivedBody).toContain('name="file"')
    expect(receivedBody).toContain('Christmas 25.avc')
    // The exact bytes, including 0x00 and 0xFF which would be mangled by
    // a naive text-based body join — this is the "the file's bytes
    // arrive" half of the task spec's test list item 2.
    expect(receivedBody).toContain(String.fromCharCode(1, 2, 3, 4, 5, 255, 0, 128))
    expect(result.revision).toBe(1)
    // jsdom's XMLHttpRequest.upload.progress implementation does NOT
    // report lengthComputable for a FormData body carrying a File in
    // this project's own measurement (verified empirically while writing
    // this test: loaded/total both arrive as 0, lengthComputable false) —
    // unlike a real browser, which computes the multipart body's total
    // size up front and reports real byte progress. This assertion is
    // therefore deliberately loose (progress fired at all, not that it
    // reached a nonzero byte count) — see this file's own header comment
    // and the task report for why the DETERMINATE progress-bar branch
    // needs checking in a real browser rather than trusted from this
    // suite.
    expect(progressEvents.length).toBeGreaterThan(0)
  })

  it('sends the stored break-glass token as a Bearer Authorization header when one is present', async () => {
    let receivedAuth: string | null = null
    const s = await corsServer((req, res) => {
      receivedAuth = (req.headers.authorization as string | undefined) ?? null
      void readBody(req).then(() => respondJson(res, 200, { ok: true }))
    })
    setStoredToken('field-token-123')

    await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal)

    expect(receivedAuth).toBe('Bearer field-token-123')
    expect(getStoredToken()).toBe('field-token-123')
  })

  it('sends no Authorization header when no token is stored', async () => {
    let receivedAuth: string | null | undefined = 'unset'
    const s = await corsServer((req, res) => {
      receivedAuth = req.headers.authorization
      void readBody(req).then(() => respondJson(res, 200, { ok: true }))
    })

    await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal)

    expect(receivedAuth).toBeUndefined()
  })

  it('rejects with a plain ApiError(status 400) on a rejected file, carrying the coordinator\'s own reason', async () => {
    const s = await corsServer((req, res) => {
      void readBody(req).then(() =>
        respondProblem(res, 400, makeProblem({
          type: 'https://showmesh.dev/problems/invalid-parameter',
          status: 400,
          detail: 'the uploaded file is not a valid Resolume composition (.avc) file',
        })),
      )
    })

    await expect(
      uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal),
    ).rejects.toMatchObject({ status: 400, message: expect.stringContaining('not a valid Resolume composition') })
  })

  it('rejects with ApiError(status 413) when the file exceeds the upload bound', async () => {
    const s = await corsServer((req, res) => {
      void readBody(req).then(() =>
        respondProblem(res, 413, makeProblem({
          type: 'https://showmesh.dev/problems/payload-too-large',
          status: 413,
          detail: 'the uploaded file exceeds this coordinator\'s 16777216 byte upload limit; nothing was stored',
        })),
      )
    })

    await expect(
      uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal),
    ).rejects.toMatchObject({ status: 413 })
  })

  it('rejects with ForbiddenError on a 403 naming the missing scope', async () => {
    const s = await corsServer((req, res) => {
      void readBody(req).then(() =>
        respondProblem(res, 403, makeProblem({
          type: 'https://showmesh.dev/problems/forbidden',
          status: 403,
          detail: 'this principal\'s role does not include "config:write"',
        })),
      )
    })

    const err = await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ForbiddenError)
    expect((err as ForbiddenError).message).toContain('config:write')
  })

  it('rejects with UnauthorizedError on a 401', async () => {
    const s = await corsServer((req, res) => {
      void readBody(req).then(() =>
        respondProblem(res, 401, makeProblem({
          type: 'https://showmesh.dev/problems/unauthorized',
          status: 401,
          detail: 'no valid credential was presented',
        })),
      )
    })

    const err = await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(UnauthorizedError)
  })

  it('rejects with IncompatibleVersionError when the response carries no ShowMesh-API-Version header', async () => {
    const s = await corsServer((req, res) => {
      void readBody(req).then(() => {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ ok: true }))
      })
    })

    const err = await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(IncompatibleVersionError)
  })

  it('reports the real HTTP status, not a missing-version-header message, for an infrastructure 413 with no ShowMesh headers', async () => {
    // Stands in for nginx's own body-size 413: an HTML error page produced
    // before the request ever reached the coordinator, carrying none of
    // ShowMesh's own headers or a Problem body.
    const s = await corsServer((req, res) => {
      req.on('data', () => {})
      req.on('end', () => {
        res.writeHead(413, { 'Content-Type': 'text/html' })
        res.end('<html><head><title>413 Request Entity Too Large</title></head></html>')
      })
    })

    const err = await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal).catch(
      (e: unknown) => e,
    )
    expect(err).not.toBeInstanceOf(IncompatibleVersionError)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(413)
    expect((err as ApiError).message).toContain('413')
  })

  it('rejects with a network-error ApiError when the connection is destroyed mid-request', async () => {
    const s = await corsServer((req) => {
      req.socket.destroy()
    })

    const err = await uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, new AbortController().signal).catch(
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ApiError)
    expect(err).not.toBeInstanceOf(ForbiddenError)
    expect(err).not.toBeInstanceOf(UnauthorizedError)
  })

  it('rejects with AbortError when the signal is aborted before the request starts', async () => {
    const controller = new AbortController()
    controller.abort()

    await expect(
      uploadFileWithProgress('http://127.0.0.1:1', '/x', makeFile([1]), () => {}, controller.signal),
    ).rejects.toBeInstanceOf(AbortError)
  })

  it('rejects with AbortError when the signal is aborted mid-request', async () => {
    let resolveRequest: (() => void) | undefined
    const s = await corsServer((req, res) => {
      // Hold the request open until the test explicitly lets it go, so
      // the abort can land while it is genuinely in flight.
      void readBody(req).then(() => {
        resolveRequest = () => respondJson(res, 200, { ok: true })
      })
    })
    const controller = new AbortController()

    const promise = uploadFileWithProgress(s.baseUrl, '/x', makeFile([1]), () => {}, controller.signal)
    // Give the request a moment to actually reach the server before
    // aborting it — a same-tick abort would race open() rather than
    // exercise the in-flight cancellation path this test is for.
    await new Promise((resolve) => setTimeout(resolve, 20))
    controller.abort()

    await expect(promise).rejects.toBeInstanceOf(AbortError)
    resolveRequest?.()
  })
})
