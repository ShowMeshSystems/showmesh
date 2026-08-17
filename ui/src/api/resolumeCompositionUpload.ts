/**
 * Track D seam D-2a (ADR-032 decision 8): `POST /config/resolume/composition`,
 * `multipart/form-data` with one file part named `file`.
 *
 * This is the one place in `src/api` that does NOT go through
 * [ApiClient]/`fetch`. ADR-030 requires an upload to "state progress and
 * failure rather than inferring them" — `fetch` has no way to observe
 * upload progress (a request body stream's write progress is not
 * surfaced to the caller the way `XMLHttpRequest.upload`'s `progress`
 * event is), so an upload built on `fetch` can only ever show a binary
 * "sending" / "done" state. Rather than let a spinner masquerade as
 * progress (this codebase's own standing lesson, CLAUDE.md: a fake
 * progress bar is "a UI claiming knowledge it does not have," the same
 * defect class as an observation claiming freshness it cannot support),
 * this file uses `XMLHttpRequest` specifically for its real
 * `upload.onprogress` byte counts, and the component
 * (ResolumeCompositionUpload.tsx) renders a native `<progress>` element
 * with a real `value`/`max` when the browser reports `lengthComputable`,
 * or the SAME element with no `value` (a genuinely indeterminate,
 * browser-native animation — not a hand-rolled one) when it does not.
 *
 * Every request-shaping decision `ApiClient.request` makes is mirrored
 * here by hand, since `XMLHttpRequest` shares none of `fetch`'s
 * machinery: the `ShowMesh-API-Version` request header, the stored
 * break-glass token taking precedence over the cookie exactly as
 * `client.ts`'s own comment on that describes (ADR-024 decision 6), and
 * `withCredentials = true` — the `XMLHttpRequest` analogue of
 * `credentials: 'same-origin'` (ADR-022: this UI is same-origin with the
 * coordinator behind its own proxy, so this never sends a credential
 * cross-origin; it is set explicitly for the identical reason
 * `client.ts`'s own comment gives for `fetch`'s `credentials` option —
 * an assumption about a default is not a fact until read off the
 * constructed request). Response classification reuses
 * [classifyProblemResponse]/[checkApiVersionHeaderValue] from `client.ts`
 * directly, so this path and the `fetch` path can never disagree about
 * which RFC 9457 `type` produces which error class.
 *
 * Track G seam G-8 reuses this for `POST /assets` (ADR-028), which needs
 * extra form fields (`show`, `sequence`, `mediaType`, `targetKind`,
 * `target`) appended to the same `FormData` BEFORE the `file` part — the
 * server refuses a `file` part that arrives first. `fields` below is
 * appended in insertion order, ahead of `file`, for exactly that reason.
 */
import { checkApiVersionHeaderValue, classifyProblemResponse } from './client'
import { AbortError, ApiError } from './errors'
import type { Problem } from './problem'
import { getStoredToken } from './token'

const API_VERSION_HEADER = 'ShowMesh-API-Version'
const REQUIRED_API_VERSION = '1'

export interface UploadProgress {
  loaded: number
  /** `null` when the browser could not report a total (`!lengthComputable`) — never fabricated as `loaded`. */
  total: number | null
}

/**
 * POSTs `file` as `multipart/form-data` (part name `file`, per
 * api/openapi.yaml's `uploadResolumeComposition` requestBody) to
 * `${baseUrl}${path}`, reporting real byte progress via `onProgress` as
 * `XMLHttpRequest.upload`'s own `progress` events arrive. Resolves with
 * the parsed JSON body on 2xx; rejects with the same typed errors
 * `ApiClient` throws (`UnauthorizedError`, `ForbiddenError`,
 * `CSRFRejectedError`, `IncompatibleVersionError`, or a plain `ApiError`
 * carrying the response's own `status`) on anything else, and with
 * [AbortError] if `signal` is already aborted or is aborted mid-request.
 */
export function uploadFileWithProgress<T>(
  baseUrl: string,
  path: string,
  file: File,
  onProgress: (progress: UploadProgress) => void,
  signal: AbortSignal,
  fields?: Record<string, string>,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    if (signal.aborted) {
      reject(new AbortError())
      return
    }

    const xhr = new XMLHttpRequest()
    const token = getStoredToken()
    const tokenWasPresent = token !== null

    xhr.open('POST', `${baseUrl}${path}`, true)
    xhr.withCredentials = true
    xhr.setRequestHeader(API_VERSION_HEADER, REQUIRED_API_VERSION)
    // ADR-024 decision 6, mirrored from client.ts's ApiClient.request: a
    // stored break-glass token, if present at all, is the only credential
    // this request considers — it always wins over whatever cookie the
    // browser would otherwise attach.
    if (token !== null) {
      xhr.setRequestHeader('Authorization', `Bearer ${token}`)
    }

    function cleanup(): void {
      signal.removeEventListener('abort', onAbort)
    }

    function onAbort(): void {
      xhr.abort()
    }
    signal.addEventListener('abort', onAbort, { once: true })

    xhr.upload.onprogress = (event: ProgressEvent) => {
      onProgress({ loaded: event.loaded, total: event.lengthComputable ? event.total : null })
    }

    xhr.onabort = () => {
      cleanup()
      reject(new AbortError())
    }

    xhr.onerror = () => {
      cleanup()
      reject(new ApiError(`network error uploading to ${path}`))
    }

    xhr.ontimeout = () => {
      cleanup()
      reject(new ApiError(`upload to ${path} timed out with no response`))
    }

    xhr.onload = () => {
      cleanup()
      try {
        checkApiVersionHeaderValue(xhr.getResponseHeader(API_VERSION_HEADER))
      } catch (err) {
        reject(err)
        return
      }

      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as T)
        } catch {
          reject(new ApiError(`${path} returned a response that could not be parsed as JSON`, xhr.status))
        }
        return
      }

      let problem: Problem | null = null
      try {
        problem = JSON.parse(xhr.responseText) as Problem
      } catch {
        // Malformed or absent problem body — classifyProblemResponse
        // falls back to a generic ApiError below, same as client.ts.
      }
      reject(
        classifyProblemResponse(
          xhr.status,
          problem,
          tokenWasPresent,
          path,
          xhr.getResponseHeader('Retry-After'),
        ),
      )
    }

    const form = new FormData()
    // Every non-file field, in insertion order, ahead of `file` — see this
    // file's header comment on why the server requires that ordering.
    if (fields !== undefined) {
      for (const [key, value] of Object.entries(fields)) {
        form.append(key, value)
      }
    }
    form.append('file', file)
    xhr.send(form)
  })
}
