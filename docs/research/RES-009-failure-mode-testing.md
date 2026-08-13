# RES-009: Failure-Mode Testing

[Architecture](../architecture/ARCHITECTURE.md#11-failure-handling) · [Tracker](README.md)

Status: planned · Risk: critical · Verification: L0

## Decision to make

Define the failure contract for every show-critical component and establish the evidence required to declare a release show-ready.

## Failure record format

Each scenario must state the injected condition, detection signal and deadline, expected safe or degraded state, autonomous recovery deadline, operator notification, manual action, persistent evidence, and conditions that prevent automatic recovery.

## Minimum matrix

| Failure | Expected concern |
|---|---|
| Coordinator loss or restart | Current show continues; local state is not corrupted |
| Switch, VLAN, or control-network loss | Nodes expose loss and follow bounded local policy |
| Packet delay, loss, duplication, or reordering | Timing becomes degraded rather than falsely healthy |
| Node reboot or process crash | Assignment and recovery are visible and deterministic |
| Missing or corrupt media | Playback does not substitute the wrong asset |
| Decoder hang or frozen frames | Fault is detected without treating intentional stills as failures |
| NDI sender, receiver, or link loss | Output enters defined state and reconnect is measurable |
| User-installed NDI runtime absent at agent start | Node stays operational, NDI send is unavailable and not advertised as usable, other capabilities remain available, and the operator receives an actionable installation pointer ([RES-006](RES-006-linux-ndi-support.md)) |
| HDMI/capture or EDID loss | Signal loss is distinguished from black content |
| SMPTE loss, noise, or jump | Resolume and audio follow defined hold/recover policy |
| Audio device or Dante route loss | Program and LTC failure are independently visible |
| Resolume restart | Composition and timing reconcile safely |
| Conflicting or duplicate commands | Idempotency and revision rules prevent oscillation |
| Stale configuration or partial deployment | Mixed revisions are detected and contained |
| Disk exhaustion | Existing show assets remain protected where possible |
| Full power restoration | Services start in safe order without unintended playback |
| Change stream interrupted mid-show | Client re-fetches an authoritative snapshot; it never resumes from its local model |
| Subscriber stops reading the change stream | Producer is never blocked, buffers stay bounded, the client is told it lost events rather than dropping them silently |
| Coordinator restarted under a connected client | Evidence is not restamped; formerly fresh observations read stale, never current |
| Operator device clock skewed from the coordinator | Freshness is computed against server time, so skew is visible rather than silently misreported |
| Collected telemetry source disabled at the source | Reported as a positively observed configuration fact, never as a collection failure or a network fault |
| Operator locked out of the identity system | Show continues, reads remain observable, and a documented host-level recovery path works without a network route |
| Coordinator refuses a command with `401` or `403` | Treated as coordinator-unavailable-to-this-caller so local fallback fires, and surfaced as distinct from a network fault ([ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md) decision 7) |
| Session revoked against an open change stream | Connection is closed and the client surfaces an authentication state, rather than continuing to receive live show state |
| Scope list stale or unavailable at the client | Controls render `unknown`, never permitted |
| Agent rejected by the broker (CONNACK `0x87`) | Distinguishable from an unreachable broker, non-fatal, and the agent continues on its cached fallback subset |
| Broker credential rotated with a show running | Effect and blast radius are known: already-connected clients are not re-authenticated, and a broker restart drops the whole fleet at once |
| Audit store unavailable under disk exhaustion | Blackout, stop, and power-off still execute with degraded attribution; `config:write` and `principal:write` are refused |
| Database restored from backup under active sessions | The session generation counter invalidates coherently rather than resurrecting revoked sessions |
| Agent fallback cache fails signature verification | Cache is discarded and reported as evidence; the agent starts, connects, and runs with no local fallback, and never executes unverified content ([ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) decision 6) |
| Agent enrolled while the coordinator is unreachable | Defined degraded state with no pinned key and no local fallback, visible as such, resolving on the next successful enrollment, never blocking agent start |
| Signing key rotated with a show running | Add-then-retire holds; no node loses its cache mid-show, and a cache signed by a retired key is refused rather than accepted |
| Coordinator restored from a backup predating a key rotation | Retirement is a revocation and rolls back with the database, so the restore runbook's explicit key check catches it; the coordinator must not silently resume signing with a retired key |

The nine rows from ADR-024 and the four from [ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md) are unverified. Both records argue from requirements rather than evidence, and closing that gap is this record's work.

Two of those rows exist because one rule was applied deliberately rather than discovered during an incident: **a restore must preserve identity and must not resurrect revocations.** A signing key is identity and must survive a restore; a generation counter and a key retirement are revocations and must not roll back. Since a database cannot detect its own restore, neither guarantee can be automatic, so both depend on an explicit operator step whose absence is invisible until it matters. **Any future state added to the coordinator's database should be classified against that rule before it ships**, and this table is where the resulting failure case belongs.

## Acceptance criteria

Before show readiness, every critical scenario has an observed L4 result on the reference topology, a documented manual recovery path, and retained proof. No critical failure may be reported healthy merely because a process or host responds.

## Method

Build automated fault injection where safe and use controlled physical tests for device, cable, audio, capture, and power failures. Run faults during resting, pre-show, live, intermission, and post-show states. Repeat failures during recovery to identify unstable loops.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

This record remains open throughout development. Any new component, capability, transport, lifecycle state, or recovery mechanism must add corresponding scenarios. A release cannot inherit L4 status across material topology or timing changes without targeted revalidation.
