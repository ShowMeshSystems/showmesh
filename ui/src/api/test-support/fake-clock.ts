/**
 * A virtual `Clock` (see clock.ts) for store.test.ts's D2 timing
 * assertions. Time here only ever moves when a test calls `advance()`
 * — never on a real timer — so the production code's idle-deadline
 * comparison (store.ts's readWithIdleTimeout, client.ts's request
 * timeout) can no longer be raced by real scheduling jitter: a
 * deadline armed against this clock simply cannot fire on its own, no
 * matter how long the real process is starved of CPU, until the test
 * explicitly advances past it.
 *
 * `armed()` returns a promise that resolves the next time production
 * code calls `setTimeout` on this clock. store.ts's readWithIdleTimeout
 * arms a fresh deadline once per read attempt, so awaiting this after
 * writing a byte on the real test server — instead of a fixed real
 * `sleepMs` guess — is what lets a test know "the previous read
 * resolved and a new deadline now covers whatever comes next" without
 * assuming anything about how long that took in real wall-clock time.
 */
import type { Clock, TimerHandle } from '../clock'

interface PendingTimer {
  readonly id: number
  readonly deadline: number
  readonly callback: () => void
}

interface FakeTimerHandle extends TimerHandle {
  readonly id: number
}

export class FakeClock implements Clock {
  private virtualNow: number
  private pending: PendingTimer[] = []
  private nextId = 1
  private armWaiters: Array<() => void> = []

  constructor(start = 0) {
    this.virtualNow = start
  }

  now(): number {
    return this.virtualNow
  }

  setTimeout(callback: () => void, ms: number): TimerHandle {
    const handle: FakeTimerHandle = { id: this.nextId++ }
    this.pending.push({ id: handle.id, deadline: this.virtualNow + ms, callback })
    const waiters = this.armWaiters.splice(0)
    for (const resolve of waiters) resolve()
    return handle
  }

  clearTimeout(handle: TimerHandle): void {
    const id = (handle as FakeTimerHandle).id
    this.pending = this.pending.filter((t) => t.id !== id)
  }

  /** Resolves the next time production code arms a new timer on this clock. */
  armed(): Promise<void> {
    return new Promise((resolve) => this.armWaiters.push(resolve))
  }

  /**
   * Moves virtual time forward by `ms` and synchronously fires every
   * timer whose deadline that reaches, earliest-deadline first. A
   * callback firing here is typically a `reject(...)` inside a pending
   * `Promise.race` — that only settles the Promise; callers still need
   * to `await` (directly, or via `waitFor` polling model state) to let
   * the resulting rejection actually propagate through the store.
   */
  advance(ms: number): void {
    this.virtualNow += ms
    for (;;) {
      const due = this.pending
        .filter((t) => t.deadline <= this.virtualNow)
        .sort((a, b) => a.deadline - b.deadline)
      const next = due[0]
      if (next === undefined) return
      this.pending = this.pending.filter((t) => t.id !== next.id)
      next.callback()
    }
  }
}
