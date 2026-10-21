# ADR-069: Power down does not wait forever for FPP, and can be forced

Status: Accepted (owner, 2026-10-21)
Date: 2026-10-21
Amends: [RESTING-MODE.md](../architecture/RESTING-MODE.md) sections 4.6 and
4.7, which held a power down until ShowMesh observed playback stopped, and
[ADR-065](ADR-065-site-actions-and-shutdown-sequences.md)'s power down, which
starts only once the night reaches `stopped`.

## Context

- A power down reaches `stopped`, and its shutdown sequences start, only when
  FPP reports idle with no playlist after the shutdown stop.
- When FPP never confirms the stop, the night degrades, resends the stop
  every 90 seconds, and never reaches `stopped`. The site stays powered all
  night.
- A degraded night that does reach `stopped` runs nothing: the night loop
  skipped every degraded state except fading out.
- An operator ending the session while a power down was pending got
  `stopped` with no power phase, so neither the sequences nor the end
  session actions ran.

## Decision

1. A power down in fading out waits for FPP's idle report for the confirm
   deadline (90 seconds). When it passes, the night reaches `stopped`
   anyway, records that FPP did not confirm the stop, and starts the power
   down sequences. The night is not degraded for it.
2. While that record stands, the stopped tick sends the FPP stop again on
   the same 90 second cadence until FPP reports idle, for this session only.
3. A night that only fades out keeps the earlier behavior: it degrades at the
   deadline and keeps fading out.
4. `power-down-presentation` takes an optional `force: true`. A forced power
   down does not wait for a running show or the fade, reaches `stopped` at
   once with the stop recorded as unconfirmed, starts the sequences, and
   sends the stop as in 2. Its interlock is still evaluated and can still
   refuse it; force never overrides a rule. The audit entry records force.
   Every other command refuses `force` with `400`.
5. A degraded night no longer blocks the power down. Once `stopped` with a
   power down intent, it runs its power down sequences, the stop resend, and
   then its end session actions, and nothing that starts a show or
   presentation.
6. The operator's End Session on a night whose power down was requested but
   has no power phase resolves the power phase, so the sequences run and
   then the end session actions. Emergency stop paths are unchanged.

## Consequences

- Power can come down while FPP may still be playing. The operator sees the
  unconfirmed stop on the night's boundary reason until FPP reports idle.
- Emergency stop level 2 forces a power down into fading out, so it now
  reaches `stopped` after 90 seconds without FPP's idle report too.
- `showmeshctl night power-down --force` sends the forced power down.

## Alternatives

- Keep waiting for FPP: rejected, a silent FPP leaves the site powered all
  night.
- Let force skip interlocks: rejected, an interlock guards the power down
  itself, and overrides already have their own audited path.
