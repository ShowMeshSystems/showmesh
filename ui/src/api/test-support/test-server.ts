/**
 * A real `node:http` server for store.test.ts, per spec section 5.7:
 * "Spin a real node:http server in your tests, emitting real SSE bytes.
 * Do not mock fetch." Not itself a test file (no .test. in the name), so
 * vitest's default include pattern does not try to run it as a suite.
 */
import http, { type IncomingHttpHeaders, type IncomingMessage, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

export interface RecordedRequest {
  method: string
  url: string
  headers: IncomingHttpHeaders
}

export interface TestServer {
  baseUrl: string
  requests: RecordedRequest[]
  requestsFor: (path: string) => RecordedRequest[]
  close: () => Promise<void>
}

/**
 * `handler` gets full control of every request (this harness does no
 * routing of its own) so each test can hold onto a `/stream` response
 * object and keep writing to it — or destroy its socket — under its own
 * timing, which a fixed router abstraction would make awkward.
 */
export async function startTestServer(
  handler: (req: IncomingMessage, res: ServerResponse, requests: RecordedRequest[]) => void,
): Promise<TestServer> {
  const requests: RecordedRequest[] = []
  const server = http.createServer((req, res) => {
    requests.push({
      method: req.method ?? 'GET',
      url: req.url ?? '',
      headers: req.headers,
    })
    handler(req, res, requests)
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })

  const address = server.address() as AddressInfo
  const baseUrl = `http://127.0.0.1:${address.port}`

  return {
    baseUrl,
    requests,
    requestsFor: (path: string) => requests.filter((r) => r.url.split('?')[0] === path),
    close: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

const SSE_HEADERS = {
  'Content-Type': 'text/event-stream',
  'ShowMesh-API-Version': '1',
  'Cache-Control': 'no-cache',
} as const

/** Opens an SSE response: headers + flush, so the client's fetch resolves immediately. */
export function openSSE(res: ServerResponse): void {
  res.writeHead(200, SSE_HEADERS)
  res.socket?.setNoDelay(true)
  res.flushHeaders()
}

export function writeSSEFrame(res: ServerResponse, event: string, data: unknown): void {
  const payload = typeof data === 'string' ? data : JSON.stringify(data)
  res.write(`event: ${event}\ndata: ${payload}\n\n`)
}

export function writeSSEComment(res: ServerResponse, text: string): void {
  res.write(`: ${text}\n\n`)
}

export function jsonHeaders(): Record<string, string> {
  return { 'Content-Type': 'application/json', 'ShowMesh-API-Version': '1' }
}

export function problemHeaders(): Record<string, string> {
  return { 'Content-Type': 'application/problem+json', 'ShowMesh-API-Version': '1' }
}

export function sleepMs(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export async function waitFor(
  predicate: () => boolean,
  options: { timeoutMs?: number; intervalMs?: number; message?: string } = {},
): Promise<void> {
  const timeoutMs = options.timeoutMs ?? 3000
  const intervalMs = options.intervalMs ?? 10
  const start = Date.now()
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error(options.message ?? 'waitFor: condition not met before timeout')
    }
    await sleepMs(intervalMs)
  }
}
