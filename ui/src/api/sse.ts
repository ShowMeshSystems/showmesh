/**
 * A hand-rolled `text/event-stream` parser, used instead of `EventSource`
 * (forbidden — see store.ts's header comment and ADR-020/ADR-021: it
 * cannot set an `Authorization` header, and it silently honors
 * `Last-Event-ID`, which ADR-020 decision 3 requires this client to never
 * do).
 *
 * This follows the WHATWG "event stream interpretation" algorithm
 * (HTML Standard section on server-sent events), including its one
 * genuinely surprising rule: a dispatch on a blank line is a no-op
 * unless at least one `data:` line was seen since the last dispatch,
 * even if an `event:` line was seen. That is why "a frame with `event:`
 * but no `data:`" (api/openapi.yaml's /stream description, and this
 * project's spec section 5.2) produces no frame from this parser — this
 * is what a real event-stream parser does with such a frame, not a
 * ShowMesh shortcut. It still resets internal buffers correctly so that
 * event type does not leak into the next dispatch.
 */

export interface SSEFrame {
  /** Defaults to "message" per the algorithm, mirroring a real EventSource. */
  event: string
  data: string
}

export class SSEParser {
  // `stream: true` on decode() lets TextDecoder hold back an incomplete
  // trailing UTF-8 byte sequence across push() calls rather than
  // emitting U+FFFD for a multi-byte character split across two chunks.
  private readonly decoder = new TextDecoder('utf-8')
  private buffer = ''
  private eventType = ''
  private dataLines: string[] = []

  /**
   * Feed one chunk of raw bytes as received from a `ReadableStream`
   * reader. Returns every complete frame the chunk completed, in order;
   * an empty array means the chunk ended mid-line (or mid-frame) and
   * more data is needed.
   */
  push(chunk: Uint8Array): SSEFrame[] {
    this.buffer += this.decoder.decode(chunk, { stream: true })

    const frames: SSEFrame[] = []
    let pos = 0
    for (;;) {
      const found = this.findLineEnd(pos)
      if (found === null) break
      const line = this.buffer.slice(pos, found.lineEnd)
      pos = found.nextPos
      const frame = this.consumeLine(line)
      if (frame !== null) frames.push(frame)
    }
    this.buffer = this.buffer.slice(pos)
    return frames
  }

  /**
   * Scans for a line terminator starting at `start`. Handles `\n`,
   * `\r\n`, and a bare `\r` (all three are valid SSE line endings). A
   * `\r` found as the very last character currently buffered is
   * ambiguous — it might be the first half of a `\r\n` split across a
   * chunk boundary — so this deliberately waits for more data rather
   * than guessing.
   */
  private findLineEnd(start: number): { lineEnd: number; nextPos: number } | null {
    for (let i = start; i < this.buffer.length; i++) {
      const code = this.buffer.charCodeAt(i)
      if (code === 10 /* \n */) {
        return { lineEnd: i, nextPos: i + 1 }
      }
      if (code === 13 /* \r */) {
        if (i + 1 < this.buffer.length) {
          const next = this.buffer.charCodeAt(i + 1)
          return next === 10 ? { lineEnd: i, nextPos: i + 2 } : { lineEnd: i, nextPos: i + 1 }
        }
        return null
      }
    }
    return null
  }

  private consumeLine(line: string): SSEFrame | null {
    if (line.length === 0) {
      // Blank line: dispatch, per the algorithm, only if a data: line
      // was actually seen. An event: line alone is not enough — see
      // this class's header comment.
      if (this.dataLines.length === 0) {
        this.eventType = ''
        this.dataLines = []
        return null
      }
      const frame: SSEFrame = {
        event: this.eventType.length > 0 ? this.eventType : 'message',
        data: this.dataLines.join('\n'),
      }
      this.eventType = ''
      this.dataLines = []
      return frame
    }

    if (line[0] === ':') {
      // Comment line (the coordinator's `: keepalive`). Ignored entirely
      // — it does not participate in field/dispatch state at all, so it
      // cannot itself trigger or suppress a dispatch.
      return null
    }

    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)

    if (field === 'event') {
      this.eventType = value
    } else if (field === 'data') {
      this.dataLines.push(value)
    }
    // `id` and `retry` (and any other field name) are recognized but
    // deliberately not acted on: this coordinator never emits either
    // (api/openapi.yaml's /stream description — no `id:` field, ever),
    // and ADR-020 decision 3 forbids building resume-from-cursor
    // behavior even if a future server accidentally sent one.
    return null
  }
}
