/**
 * The seam that turns "elapsed time" into an injectable dependency for
 * the store's stream idle-deadline (D2) and the client's request
 * timeout (D2), rather than each calling the global `setTimeout`
 * directly. Production code always uses SYSTEM_CLOCK, so this changes
 * no production behavior: `now()` still delegates to `Date.now()` and
 * `setTimeout`/`clearTimeout` still delegate to the real globals.
 *
 * Why this exists: store.test.ts's two D2 idle-timeout tests, plus
 * client.ts's request-timeout test, used to race a real `setTimeout`
 * deadline in the production code against real bytes arriving over a
 * real socket, with both sides' magnitudes (a 15ms keepalive interval
 * against a 40ms idle deadline) close enough that scheduling jitter
 * under machine load could flip the outcome — reproduced directly: 1
 * failure in 10 full-suite runs under CPU load, always this pairing.
 * Injecting the clock lets a test supply `test-support/fake-clock.ts`'s
 * FakeClock, whose virtual time only advances when the test explicitly
 * tells it to. That removes real-time races from the *decision* the
 * production code makes, while the bytes it decides about still cross
 * a real socket to a real node:http server — the transport stays real;
 * only the clock driving the deadline comparison becomes controllable.
 */
export interface TimerHandle {
  readonly __timerHandleBrand?: never
}

export interface Clock {
  now(): number
  setTimeout(callback: () => void, ms: number): TimerHandle
  clearTimeout(handle: TimerHandle): void
}

export const SYSTEM_CLOCK: Clock = {
  now: () => Date.now(),
  setTimeout: (callback, ms) => setTimeout(callback, ms) as unknown as TimerHandle,
  clearTimeout: (handle) => clearTimeout(handle as unknown as ReturnType<typeof globalThis.setTimeout>),
}
