# RES-020: MultiSync-Triggered Cue Start and PCM Delivery

Status: planned (rehearsal rig measurements of the current path L2 2026-09-17; FPP start lifecycle L1 2026-09-18; every measurement of the new path L0)
Risk: critical
Depends on: RES-002, RES-019

## 1. Why this record exists

[ADR-051](../decisions/ADR-051-cue-audio-starts-on-the-multisync-start-packet.md)
moves a Cue's audio start from a coordinator-chosen instant to the MultiSync
START packet on every node, and moves show audio delivery to PCM. Both rest on
numbers that have not been measured on the new path. This record holds the
numbers that exist, the ones the rig run must produce, and the threshold the
result is judged against.

## 2. What is known

### 2.1 The current path is 10 to 14 seconds late (L2, rehearsal rig, 2026-09-17)

From the rehearsal coordinator's command store (build `bedfix-cfbd1cb`, FPP 10
in the bench container, showmesh-node-01 and pi-audio-01 as targets):

| Cue | FPP entry evidence | Probe done on the slowest node | cue.activate created | Scheduled audio start | Gap |
| -- | -- | -- | -- | -- | -- |
| wake-up | 22:46:26.98 | 22:46:29.49 | 22:46:31.18 | 22:46:37.43 | 10.5 s |
| kpop-audio | 22:49:31.70 | 22:49:35.50 | 22:49:36.38 | 22:49:45.89 | 14.2 s |

The gap is measured from the FPP entry observation, not from the cue.activate
record. The probe round (apply, prepare, and clear of the media on every node)
runs before the activation is dispatched, then `audiosched.Select` adds the
configured 2000 ms delivery bound, up to twice the slowest probe time, the
1000 ms margin, and preroll. Both activations carried `positionMs` 0 or 1, the
FPP playlist slot index, so the audio started from the beginning of the file.

### 2.2 FPP sends OPEN and START back to back (L1, FPP 10 source, 2026-09-18)

Read in the bench container at `/opt/fpp/src`:
`PlaylistEntrySequence::StartPlaying` calls `PreparePlay` (which calls
`Sequence::OpenSequenceFile`, sending the OPEN packet) and then
`Sequence::StartSequence` (sending START). `Playlist.cpp` does not prepare the
next entry ahead. A remote memory-maps the sequence on OPEN and starts on
START (`MultiSync::OpenSyncedSequence`, `StartSyncedSequence`). The plugin's
`query_next` callback runs synchronously at the moment FPP picks the next item,
milliseconds before OPEN.

### 2.3 Media prepare cost today (L2, rehearsal rig, 2026-09-16 and 2026-09-17)

ADR-049 decision 6 records about 1.4 s on the program plus LTC node and 2.4 s
on the Raspberry Pi 3B+ for a show MP3. The probe rounds above took 1.5 s and
2.5 s (wake-up) and 2.2 s and 3.7 s (kpop-audio) on the same two nodes,
including MQTT round trips. The Pi logs `item media duration unknown` for
these files, so no scheduled item boundary is possible.

### 2.4 Rate lock between nodes (L2, RES-019)

Two nodes rate-locked to the shared PTP clock hold within 0.5 ms
(RES-019, 2026-09-11). A start that lands on the same instant on both stays
together.

### 2.5 First rig runs of the new path (L2, rehearsal rig, 2026-09-18)

Bench FPP 10 on the coordinator host, `MultiSyncExtraRemotes` naming node-01
only, so node-01 exercises the packet path and the Pi exercises the fallback.
Playlist `halloween-quick-check` (wake-up, then kpop-audio), both nodes on the
same build, WAV renditions in place, prepare-ahead staging the second Cue.
Times are the node's media clock from its persisted session decisions and the
coordinator's command store; no audible-sample measurement was taken (run 1
of section 3 is still open).

| Build | Cue | node-01 START arrival to start | node-01 path | Pi start after FPP evidence | Pi activation time |
| -- | -- | -- | -- | -- | -- |
| d8e5c30 | wake-up (cold) | 850 ms | refused in past, late start | 5.4 s | 3.0 s |
| d8e5c30 | kpop-audio (staged) | 337 ms | refused in past, late start | 4.1 s | 2.4 s |
| 6fea752 | wake-up (cold) | 537 ms | refused in past, late start | 4.4 s (cold cache), 3.3 s (warm) | 3.0 s, 1.7 s |
| 6fea752 | kpop-audio (staged) | 100 ms lead honored | scheduled start | 4.1 s (cold cache), 1.8 s (warm) | 2.5 s, 0.16 s |

What the two builds changed:

- On d8e5c30 every scheduled start ran a synchronous clock poll (four `pmc`
  subprocesses, about 415 ms on node-01) before comparing the instant, so the
  100 ms lead was always in the past by the time it was checked. 6fea752 uses
  the tracker's last polled status when it is younger than 30 s. With that, a
  staged Cue starts at its packet's arrival plus the configured lead.
- Every `cue.activate` hashed the whole media file before starting (66 MB WAV,
  about 2.3 s on the Pi 3B+). 6fea752 caches the verified hash by size and
  modification time, so the Pi's staged activation fell to 160 ms. The cache
  is in-process: the first activation of each file after an agent restart
  still pays the hash.
- The cold first Cue is bounded by the OPEN prepare: about 520 ms on node-01
  for the 35 MB WAV, because FPP 10 sends OPEN and START back to back and
  nothing stages the first Cue of a playlist. The late-start fallback now
  fires within 2 ms of the refusal instead of after the poll.

Open from these runs: staging the first Cue of the next scheduled playlist
(would remove the cold prepare); the coordinator's 1.5 s wait reads the
periodic session report, whose `start.trigger` field persists from the
previous Cue, so the wait can return early on stale evidence and the node's
own `cue.activate` result fields are what the persisted record now relies on;
node-01's `audio.node` binding has `outputLatency.method: unmeasured`, so no
latency compensation was applied in these runs.

## 3. What the rig run must measure

Every run uses the bound show playlist with both audio nodes, the coordinator,
node-01 and the Pi on the same build, and FPP 10 with MultiSync enabled and
both nodes in its systems list. Record the build commit, the FPP version, and
the playlist's step time with every number.

1. **Armed start.** Play the playlist through. For every Cue after the first,
   per node: START packet arrival time, first audible sample time (alignment
   run tooling, `audio_alignment_runs`), the lead the node applied, and the
   node-to-node offset.
2. **Cold start.** Start one show sequence by hand from FPP's page. Per node:
   OPEN arrival, prepare duration, START arrival, first audible sample, and
   the late-by value the node reported.
3. **Loop.** Let the playlist loop twice. No refusals in the coordinator log,
   and the armed-start numbers do not grow from pass to pass.
4. **Fallback.** Disable MultiSync on the player for one Cue. The audio still
   plays, the activation reads unaligned with the no-packet reason, and the
   gap from entry observation to audible is recorded.
5. **Prepare cost.** For the same file as MP3 and as the 48 kHz WAV rendition,
   the node's own prepare evidence on the Pi and on node-01.

### 3.1 Threshold

An armed Cue is audible within one sequence frame (the playlist's step time,
50 ms at 20 fps) of the lights on both nodes, and the two nodes are within
RES-019's 0.5 ms of each other. A cold Cue is audible within the measured WAV
prepare time plus one frame. The fixed lead in `audio.settings` is set from
run 1 so the armed result meets the threshold with margin, and recorded here.

## 4. Open questions

- Q1: What is the START packet arrival to first audible sample on each node
  with a prepared WAV? (run 1)
- Q2: How long does a Pi 3B+ take to prepare a 48 kHz WAV rendition from
  OPEN? (runs 2 and 5)
- Q3: Does a second prepared branch on the Pi, held while the bed plays, cost
  memory or glitches? (run 1, with the bed running)
- Q4: Does FPP 10's unicast-to-discovered-remotes default reach both nodes on
  the show LAN without `MultiSyncExtraRemotes`? (run 1, with the setting
  cleared)
- Q5: With a containerized FPP player on the host network, do the packets
  reach the nodes? (only if the show player becomes a container)

## 5. Decision, fallback, and revalidation

Direction: ADR-051. Fallback: the coordinator's chosen instant (ADR-049
decisions 3 and 6) after the 1.5 s window. Revalidate on every FPP major
release and whenever a node's audio interface or clock provider changes.
