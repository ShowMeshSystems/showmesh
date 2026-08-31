# ADR-048: A Signed FPP Fallback Program Preserves a Running Show

Status: Accepted (owner, 2026-08-30)
Date: 2026-08-30

## Context

FPP remains the calendar scheduler and the authority for an FPP-backed
Playlist's entry selection and progression.  ShowMesh normally observes that
progression, resolves the active entry to a show-scoped Cue, and dispatches the
Cue's already-authorized outputs.  That path deliberately gives the FPP plugin
observation authority, not general execution authority.

That leaves a real degraded-operation gap.  A coordinator failure during a
scheduled show must not turn a previously synchronized, safe show into a
silent or uncontrolled one merely because the component that normally resolves
the Cue is unavailable.  The answer must not create a second coordinator in
FPP, permit arbitrary commands from an FPP host, or let an old show regain
authority after it was superseded.

ADR-043 already establishes the correct identity for an FPP playlist entry.
The plugin produces a deterministic entry key from FPP instance, playlist
name, complete playlist hash, section, and position.  It does not produce a
durable UUID embedded in an FPP playlist item.  The coordinator resolves that
entry key to a Cue UUID and revision while healthy.

## Decision

### 1. The coordinator publishes a signed, per-FPP fallback program

For every active FPP-backed show, the coordinator builds a fallback program for
each participating FPP host.  The program is signed with the existing
coordinator signing authority.  It is a complete replacement, not a patch:
the plugin verifies it before atomically replacing its last-known-good copy.

The program contains only the active show's pre-resolved, target-specific
work:

- its package id, revision, expiry, active-show generation, and catalog
  revision;
- the FPP identity and playlist revisions it applies to;
- each deterministic playlist-entry key and its resolved Cue UUID and Cue
  revision;
- the named node targets and the exact output activation each target may
  perform;
- the fallback start, rest/hold, and local shutdown rules; and
- the recovery rule: return to normal coordination only at the next normal
  scheduled-show boundary.

The coordinator rebuilds and distributes this program whenever an active-show
authorization, Cue, FPP binding, target assignment, output action, fallback
rule, or relevant catalog revision changes.  While healthy it also reconciles
the program periodically and retries an unacknowledged delivery.  It never
creates, refreshes, or relaxes a fallback program during an outage.

The plugin reports the package id, revision, verification result, installed
time, and age.  A missing, stale, mismatched, or unacknowledged package is a
readiness failure before showtime.

### 2. FPP executes only a previously authorized entry match

When the plugin has confirmed loss of normal coordinator control under the
installed program's configured failure threshold, it may take over only at a
safe FPP playback boundary.  It obtains the current deterministic entry key
from the existing native playlist callback, looks up that key in the installed
program, and invokes the exact activation named there.

This is the same match that the coordinator normally uses, moved into a
pre-authorized local map for the outage.  FPP does not create a Cue identifier,
infer a Cue from a filename, choose a replacement Cue, advance a Playlist, or
run a command that the package did not name.

The program defines three bounded states:

1. **Normal:** the coordinator resolves and dispatches Cue activations; the
   plugin observes and keeps the package current.
2. **Fallback:** after confirmed loss, the plugin follows the installed map at
   safe entry boundaries until its configured cutoff.
3. **Resting:** at the cutoff it performs only the package's declared
   rest/hold and local-shutdown behavior.  It does not extend the program or
   improvise a new lifecycle action.

The exact outage detector and its threshold are configuration with a measured
default, not a silent hard-coded timeout.  The initial implementation must
test both false-positive protection and a coordinator outage.

### 3. Node agents accept a narrow fallback activation ingress

The agent gains a dedicated fallback-activation ingress within its existing
process.  It is not a general listener for FPP commands and does not expose the
public coordinator API.

An accepted request must carry the signed fallback-program identity, package
revision, FPP identity, deterministic entry key, Cue UUID and revision,
active-show generation, catalog revision, target node identity, and a unique
execution id.  The agent accepts it only when all of the following hold:

- its local catalog and active-show authorization exactly match the request;
- the referenced program is signed by the coordinator and is the installed,
  current program for that FPP host;
- the entry key maps to that exact Cue and target in the signed program;
- the request is from the enrolled FPP fallback executor for that host;
- the package is within its validity window; and
- the execution id has not already been processed.

The FPP executor authenticates itself with a per-host credential or public key
issued during enrollment.  The node persists its replay fence.  The listener
is target-restricted, rate-limited, and reports every refusal.  It never
accepts an arbitrary Cue, action, macro, payload, or command name.

The agent uses the same Cue activation validation and output application path
as normal coordinator dispatch after this ingress has established that the
fallback authorization is valid.  The fallback path therefore changes who
delivers a pre-authorized activation during an outage, not what a Cue means or
what an output may do.

### 4. Recovery deliberately waits for the next scheduled-show boundary

Coordinator recovery does not seize a running fallback show mid-entry or
mid-program.  The coordinator may observe, diagnose, and publish a new package
once healthy, but the plugin remains the fallback executor until the next
normal scheduled-show boundary declared in the package.  At that boundary,
FPP's normal scheduling authority resumes and the coordinator resumes normal
Cue resolution only after its catalog and package acknowledgements are current.

This avoids two authorities attempting to advance or reapply the same Cue.

### 5. Witness quorum is deliberately deferred

Multiple FPP witnesses may later narrow who is allowed to declare the
coordinator unavailable.  That is not part of the first fallback program.  The
initial implementation has one FPP host executing only its own signed program
and uses conservative failure detection.  A future quorum must be a separate
decision with its own failure and partition evidence; it must not be smuggled
into the first build as informal peer coordination.

## Consequences

- A coordinator outage can preserve only a pre-published, active-show Cue path.
  Unknown entries, changed playlists, expired packages, missing target
  catalogs, and ambiguous bindings hold or rest visibly rather than guessing.
- FPP retains schedule and primary-playlist authority in both normal and
  fallback operation.  ShowMesh does not acquire calendar authority.
- The plugin gains bounded local execution but never arbitrary system-control
  authority.
- Agents gain a separately authenticated inbound route.  ADR-044 remains true
  for its xLights route; this record adds a different, stricter route rather
  than widening the xLights compatibility endpoint.
- The normal and fallback paths share Cue identity, authorization checks, and
  output application.  They cannot silently drift into different cue semantics.
- Real FPP, node, network-partition, restart, cutoff, and recovery evidence is
  required before the feature can be considered operational.

## Alternatives considered

### Put a Cue UUID in every FPP playlist item

Rejected.  FPP does not yet prove that a custom item field survives every
editor, import, API, and upgrade path.  The existing deterministic entry key
already distinguishes duplicate filenames safely.  The signed program maps it
to the Cue UUID that ShowMesh owns.

### Let the FPP plugin send arbitrary commands to agents

Rejected.  It would turn an outage mechanism into a second control plane and
make the FPP host a broad command authority.  The agent accepts only an exact
entry-key-to-Cue activation that a valid coordinator-signed program grants.

### Let the recovered coordinator take over immediately

Rejected.  A mid-program handoff risks double activation, contradicting
progression, and an unsafe output transition.  The next scheduled-show
boundary is explicit, observable, and owned by FPP.

### Require a multi-host witness quorum before any first build

Rejected for now.  It introduces partition and peer-authentication behavior
that is independent of proving one host can safely preserve a pre-authorized
show.  It remains future work, not an implicit omission.

## Related decisions and work

- ADR-004: reduced local fallback establishes the need for a bounded local
  path.
- ADR-024: the new ingress is an authorization boundary.
- ADR-025: signed fallback material and enrollment-pinned verification are the
  trust foundation.
- ADR-038: FPP still authorizes normal night sessions; this record only covers
  coordinator loss during an already scheduled show.
- ADR-043: deterministic FPP entry identity and Cue authorization are retained.
- ADR-044: the xLights listener remains a distinct compatibility endpoint.
- [Track J](../build/TRACK-J-fpp-fallback.md) defines the implementation order
  and verification gates.
