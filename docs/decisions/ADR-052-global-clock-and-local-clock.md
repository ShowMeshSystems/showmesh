# ADR-052: Global Clock and Local Clock

Status: Accepted (owner, 2026-09-18)
Date: 2026-09-18

## Context

[ADR-018](ADR-018-program-and-ltc-share-a-clock-domain.md) requires program
audio and LTC to leave through one clock domain, so timecode cannot drift
against the music. The `audio.node` object expresses that as two required
free-text fields the operator types, `clockDomain` and
`clockDomainProvenance`, and the node echoes them back as
`node.audio.clock.domain` and `node.audio.clock.provenance`.

On the rehearsal rig on 2026-09-18 the owner, an audio engineer, could not
tell what either field meant. The name is also wrong for the trade: a clock
domain is the reference a set of devices is locked to (a PTP, Dante or AES67
domain), and the field holds the name of one interface's sample clock. It is
required on a node that emits no LTC, where the question does not exist, and
nothing in the system acts on its value. Since
[ADR-046](ADR-046-rate-lock-to-a-shared-clock-is-not-chasing.md) a node's
output is rate-locked to PTP, which is the thing an engineer would call the
clock domain, and it is reported elsewhere under another name.

## Decision

1. **Two names.** The *global clock* is the reference every node is locked
   to: PTP, with its domain and grandmaster, or none. The *local clock* is
   the sample clock of the interface a node's program audio leaves through.
   Operator-visible text, the API description and new identifiers use these
   two names. "Clock domain" is not used for the local clock anywhere.
2. **The local clock is derived, not declared.** It is the interface named
   by the node's program route. This works on a node with no LTC. The
   operator types nothing.
3. **Optional override.** An operator may name the local clock explicitly
   for an arrangement the node cannot see, such as two interfaces sharing
   external word clock. Absent means derived.
4. **Drift warning replaces the declaration.** When a node's LTC route names
   a different interface than its program route and no override says they
   share a clock, readiness warns that timecode will drift against the
   music. This is how ADR-018's rule is now enforced; its rule is unchanged.
5. **`clockDomainProvenance` is removed**, and `clockDomain` with it. A
   stored object that still carries them is accepted and the fields are
   ignored and dropped on the next write, so no installation breaks.
6. **One sync status line per node**, in the manner of a broadcast audio
   node's sync page: whether the local clock is locked, what it follows,
   the offset and the rate adjustment. Example: `Sync: Locked. Follows PTP
   000fd4fffe06553a:0, offset 0.2 µs, rate +0.00 ppm`. The word is
   "follows". A node with no global clock reads `Sync: Free-running on the
   local clock`. Values the node does not measure are omitted, never
   invented.

## Consequences

- `api/openapi.yaml`, `showmeshctl`, the node's audio report and the
  operator UI change together; the two old signals are replaced by a local
  clock signal and the sync status signals.
- The readiness condition for LTC on a second interface is new.
- ADR-018 stands. Only the way its rule is expressed changes.

## Alternatives

- Keep the declared fields and add descriptions. Rejected: the value is
  derivable, and a required field nothing reads is a cost with no benefit.
- Derive with no override. Rejected: shared external word clock is real
  outside this installation, and the node cannot detect it.

## Related

ADR-018, ADR-045, ADR-046, RES-019.
