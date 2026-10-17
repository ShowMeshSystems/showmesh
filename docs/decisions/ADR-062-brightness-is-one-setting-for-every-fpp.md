# ADR-062: Brightness is one setting for every FPP

Status: Accepted (owner, 2026-10-17)
Date: 2026-10-17
Supersedes: [RES-018](../research/RES-018-fpp-brightness-control.md) section 1's
rule that the ceiling is owned by each FPP's own scheduler and operator command
path, and that the coordinator writes no ceiling. Amends
[ADR-038](ADR-038-fpp-authorizes-night-sessions.md)'s brightness consequence
and the night-session fade targeting in
[FPP plugin coordinator contracts](../build/FPP-PLUGIN-COORDINATOR-CONTRACTS.md)
section 2.

## Context

A display driven by an FPP player and its MultiSync remotes has one brightness.
Each remote drives its own pixels, so a ceiling applied on the player alone
leaves the remotes at full brightness.

The plugin was designed to carry its full brightness state between FPP hosts
over MultiSync. On two containerized FPP 10.0 hosts (one player, one remote)
with plugin 0.3.0, nothing was carried in either direction:

- The plugin called `PluginManager::multiSyncData`, which in FPP only hands the
  payload to plugins on the same host. The network send is
  `MultiSync::SendPluginData`.
- FPP in remote mode forces MultiSync off and never opens its send socket, so
  `SendPluginData` on a remote refuses. Turning on multicast or listing the
  player as a destination on the remote did not change that.
- The plugin only sent from `modifyChannelData`, which FPP calls per output
  frame. A change made while nothing plays went out at the next frame of
  playback.

With the send call corrected in a scratch build, a ceiling set on the playing
player reached the remote. A ceiling set on the remote could not reach anything.
Peer sync over MultiSync can therefore carry a change from the player to its
remotes and never from a remote.

The owner ruled that brightness is a global setting with no reason to be set
one FPP at a time.

## Decision

1. **The display has one brightness ceiling.** The coordinator holds it as
   desired state, persisted across restarts, with the time it was set and who
   set it. There is no per-FPP ceiling write in the API, the UI or
   `showmeshctl`.
2. **Every FPP in `fpp.endpoints` follows it.** The coordinator compares each
   FPP's reported ceiling with the desired one and writes the desired ceiling to
   any FPP that differs, through the plugin's existing ceiling command, until the
   FPP reports it. A fade carries its end time: an FPP that joins a fade late is
   sent only the remaining duration, so every FPP reaches the target together.
3. **A change on any FPP becomes the display's ceiling.** The plugin page
   fader, the `ShowMesh: Set Brightness Ceiling` FPP action and FPP scheduler
   entries stay where they are. The plugin's brightness read reports when the
   ceiling last changed and to what target. When an FPP reports a ceiling change
   the coordinator did not make, newer than the desired ceiling, the coordinator
   adopts it as the desired ceiling and writes it to every other FPP. The
   coordinator discovers the change from its existing five-second brightness
   poll. The plugin gains no new connection to the coordinator and no broker
   credential.
4. **Only a deliberate change counts.** A plugin restart that settles at the
   safe ceiling, and a restore from the plugin's own persisted state, do not
   change the reported ceiling change time. A restarted FPP is brought back to
   the display's ceiling rather than imposing its own.
5. **Times from different hosts are compared on the coordinator's clock.** The
   plugin's read already carries its own clock (`updatedAtMillis`). The
   coordinator corrects an FPP's change time by the difference between that clock
   and its own receipt time before comparing it with the desired ceiling. When
   two FPPs report changes in the same poll, the later one wins.
6. **MultiSync peer sync is kept as the coordinator-loss path.** The plugin
   sends its full state with `MultiSync::SendPluginData` from its worker thread
   whenever the state changes, playing or idle, and only where FPP can send
   (MultiSync enabled). While the coordinator is down a change on the player
   still reaches its remotes. A change on a remote stays on that remote until
   the coordinator returns and spreads it under decision 3.
7. **Night fades and lighting-cue dimming write the transition gain to every
   FPP in `fpp.endpoints`**, not only the resting and show FPP. The manual
   transition-gain write in the API and `showmeshctl` does the same.
8. **What stays per FPP:** channel ranges and excluded ranges, because each
   FPP drives a different channel layout, and pairing. The weather gate and
   emergency stop already reach every FPP and are unchanged.

## Consequences

- RES-018's single-owner rule for the ceiling no longer holds: the FPP
  scheduler, every plugin page, and the coordinator are all writers of one
  value, ordered newest first.
- An FPP whose plugin predates the change-time field is written to but never
  adopted from, because the coordinator cannot tell a deliberate change on it
  from a restart.
- A change on one FPP reaches the others within one poll interval plus the
  write, about five seconds, not per frame.
- Ordering depends on the coordinator's skew correction, which is only as good
  as the poll's round trip. Two changes on different FPPs closer together than
  that may resolve in either order.
- Nothing here has run on real FPP hardware. The container bench covers FPP's
  MultiSync send and receive; real multicast between hosts on the show LAN is
  unverified.
