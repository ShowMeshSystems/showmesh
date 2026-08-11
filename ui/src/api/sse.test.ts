import { describe, expect, it } from 'vitest'
import { SSEParser } from './sse'

const encoder = new TextEncoder()

/** Feeds a whole string as a single chunk and returns every frame produced. */
function parseAll(text: string): ReturnType<SSEParser['push']> {
  const parser = new SSEParser()
  return parser.push(encoder.encode(text))
}

describe('SSEParser', () => {
  it('parses a simple event: + data: frame terminated by a blank line', () => {
    const frames = parseAll('event: node.changed\ndata: {"a":1}\n\n')
    expect(frames).toEqual([{ event: 'node.changed', data: '{"a":1}' }])
  })

  it('handles \\r\\n line endings identically to \\n', () => {
    const frames = parseAll('event: node.changed\r\ndata: {"a":1}\r\n\r\n')
    expect(frames).toEqual([{ event: 'node.changed', data: '{"a":1}' }])
  })

  it('skips a comment line (": keepalive") and produces no frame for it', () => {
    const parser = new SSEParser()
    const frames = parser.push(encoder.encode(': keepalive\n\n'))
    expect(frames).toEqual([])
  })

  it('does not reprocess a comment line as a field, even when its content looks like one (T3)', () => {
    // T3 fix: the previous version of this test ("skips a comment line")
    // stayed green even with the entire `if (line[0] === ':') return
    // null` branch deleted outright, because for THIS parser a comment
    // line's leading colon is always at index 0 — so the generic field
    // extraction (`line.slice(0, line.indexOf(':'))`) always computes
    // the same empty field name `''` that a real comment produces,
    // which matches neither 'event' nor 'data' either way. The two code
    // paths are extensionally identical for any input that merely
    // starts with ':'.
    //
    // What DOES distinguish "ignore the whole line" (correct, per the
    // WHATWG comment step, which runs before field/value extraction)
    // from "strip the leading ':' and parse what's left as a normal
    // field line" (a plausible alternate implementation of the same
    // intent) is a comment whose remainder — once that leading colon is
    // gone — happens to parse as a real field. ':data: sneaky' is
    // exactly that: stripping only the leading colon leaves 'data:
    // sneaky', whose field name IS 'data'. A correct comment-skipping
    // parser must never let this reach dataLines.
    const frames = parseAll(':data: sneaky\n\nevent: after\ndata: real\n\n')
    expect(frames).toEqual([{ event: 'after', data: 'real' }])
  })

  it('a comment line between two real frames does not disturb either', () => {
    const frames = parseAll(
      'event: a\ndata: 1\n\n: keepalive\n\nevent: b\ndata: 2\n\n',
    )
    expect(frames).toEqual([
      { event: 'a', data: '1' },
      { event: 'b', data: '2' },
    ])
  })

  it('produces no frame for event: with no data: at all (real SSE semantics, not a crash)', () => {
    // This is not a ShowMesh shortcut: a real text/event-stream parser
    // (the WHATWG "dispatch" algorithm) drops a frame with no data:
    // line, even if event: was set. See sse.ts's header comment. What
    // this test actually proves is that such a frame does not corrupt
    // parser state for the NEXT frame (the eventType buffer must be
    // reset even though nothing dispatched). The follow-up frame
    // deliberately sets no event: of its own, so a leaked eventType
    // buffer from the dropped "orphan" frame would surface here as
    // "orphan" instead of the correct default "message".
    const frames = parseAll('event: orphan\n\ndata: payload\n\n')
    expect(frames).toEqual([{ event: 'message', data: 'payload' }])
  })

  it('a data: line with an empty value still dispatches (data buffer becomes non-empty)', () => {
    const frames = parseAll('event: empty-data\ndata:\n\n')
    expect(frames).toEqual([{ event: 'empty-data', data: '' }])
  })

  it('defaults the event type to "message" when no event: field is present', () => {
    const frames = parseAll('data: no-event-name\n\n')
    expect(frames).toEqual([{ event: 'message', data: 'no-event-name' }])
  })

  it('parses multiple frames arriving in a single chunk, in order', () => {
    const frames = parseAll(
      'event: node.changed\ndata: {"n":1}\n\nevent: fpp.changed\ndata: {"f":2}\n\nevent: event.recorded\ndata: {"e":3}\n\n',
    )
    expect(frames).toEqual([
      { event: 'node.changed', data: '{"n":1}' },
      { event: 'fpp.changed', data: '{"f":2}' },
      { event: 'event.recorded', data: '{"e":3}' },
    ])
  })

  it('joins multiple data: lines with \\n, per standard SSE multi-line data', () => {
    const frames = parseAll('event: multi\ndata: line one\ndata: line two\n\n')
    expect(frames).toEqual([{ event: 'multi', data: 'line one\nline two' }])
  })

  it('parses a frame split across three separate chunk boundaries', () => {
    const parser = new SSEParser()
    const whole = 'event: node.changed\ndata: {"a":1}\n\n'
    const bytes = encoder.encode(whole)
    // Split at two arbitrary, deliberately awkward byte offsets that
    // land mid-field and mid-line, not on any natural boundary.
    const cut1 = 5
    const cut2 = 22
    const frames = [
      ...parser.push(bytes.slice(0, cut1)),
      ...parser.push(bytes.slice(cut1, cut2)),
      ...parser.push(bytes.slice(cut2)),
    ]
    expect(frames).toEqual([{ event: 'node.changed', data: '{"a":1}' }])
  })

  it('parses a frame split at a byte offset landing inside a multi-byte UTF-8 sequence', () => {
    const parser = new SSEParser()
    // U+1F386 FIREWORKS (a 4-byte UTF-8 sequence, F0 9F 8E 86) inside the
    // JSON string value, deliberately so the encoded byte stream has a
    // multi-byte character to split across chunks.
    const whole = 'event: event.recorded\ndata: {"summary":"show \u{1F386} start"}\n\n'
    const bytes = encoder.encode(whole)
    const fireworksStart = bytes.indexOf(0xf0) // first byte of the 4-byte sequence
    expect(fireworksStart).toBeGreaterThan(0)
    // Split right in the middle of the 4-byte sequence (after 2 of its 4 bytes).
    const cut = fireworksStart + 2

    const framesFromFirstChunk = parser.push(bytes.slice(0, cut))
    expect(framesFromFirstChunk).toEqual([]) // nothing complete yet
    const framesFromSecondChunk = parser.push(bytes.slice(cut))

    expect(framesFromSecondChunk).toEqual([
      { event: 'event.recorded', data: '{"summary":"show \u{1F386} start"}' },
    ])
  })

  it('waits for more data on a bare \\r at the very end of a chunk, rather than guessing it terminates the line', () => {
    // findLineEnd's own doc comment states the rule this pins: a `\r`
    // landing as the LAST buffered character is ambiguous — it might be
    // a genuine bare-\r line ending, or it might be the first half of a
    // `\r\n` pair whose `\n` just hasn't arrived yet — so it must wait
    // rather than guess. Guessing "it's a real line ending" is
    // observably wrong here: it would dispatch `event: node.changed`
    // immediately using only the `\r` half, then treat the arriving
    // `\n` as its OWN blank line in the next chunk. That spurious blank
    // line's dispatch check resets the parser's eventType buffer (see
    // consumeLine's blank-line branch) before the real data: line and
    // real blank line are even seen, so the eventually-dispatched frame
    // loses its event name and falls back to the "message" default.
    const parser = new SSEParser()
    const chunk1 = encoder.encode('event: node.changed\r')
    const chunk2 = encoder.encode('\ndata: {"a":1}\r\n\r\n')

    const framesFromFirstChunk = parser.push(chunk1)
    expect(framesFromFirstChunk).toEqual([]) // nothing may be dispatched from an ambiguous trailing \r

    const framesFromSecondChunk = parser.push(chunk2)
    expect(framesFromSecondChunk).toEqual([{ event: 'node.changed', data: '{"a":1}' }])
  })
})
