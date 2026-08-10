# RES-002 capture procedure: `showmesh-multisync-probe`

[RES-002](../research/RES-002-fpp-multisync-compatibility.md) · [Build plan, Step 1](../build/BUILD-PLAN.md#step-1-pkgmultisync)

This is the operator procedure for running `showmesh-multisync-probe` against a real FPP player to collect the evidence RES-002's five open bench items ask for. It is written for the project owner running captures against his own reference installation.

The probe collects evidence. It does not decide whether RES-002 passes. That judgment, and the edit to RES-002 itself, is a separate step described at the end of this document.

## WARNING: do not run this on the FPP player host during a live show

**Do not run `showmesh-multisync-probe` on an FPP player host (the master, or any of its configured MultiSync remotes) while a show that depends on MultiSync unicast is running.** Run it from a separate machine on the same network segment instead, one that is not itself a sender or an intended recipient of the show's MultiSync traffic.

The reason is specific, not general caution. This listener's socket can be opened with `SO_REUSEPORT` (Linux) so it can coexist with another process already bound to UDP 32320 on the same host, for example so a capture can be taken without first stopping `fppd`. `SO_REUSEPORT` lets more than one socket bind the same port and share incoming traffic, and the kernel decides how to split it by hashing each datagram's source/destination address and port (a 4-tuple hash), not by copying it to every listener. Multicast and broadcast datagrams still fan out to every member and are unaffected by this. Unicast datagrams are not: each one goes to exactly one of the reuseport sockets, chosen by that hash, which is by design and not something either socket can override. An Opus review verified this directly on Linux: two `SO_REUSEPORT` sockets on port 32320 receiving 20 unicast MultiSync datagrams split them 20 to 0, all 20 landing on one socket, none on the other.

That matters here because a MultiSync unicast setup (the mode used specifically where multicast is blocked, for example a routed VLAN) addresses sync traffic directly to each participant's IP. If this probe runs with port sharing enabled on a host that is itself one of those addressees, whether that is the FPP master or one of its listed remotes, the kernel's hash can silently route some or all of that host's own incoming unicast MultiSync stream to the probe instead of to `fppd`, with nothing in either process's own output warning that it happened. That desyncs whatever that host was supposed to be playing, live, with no error on either side. CLAUDE.md's non-negotiable constraint 6 and ADR-001 require that ShowMesh never sits in the real-time timing or media path; this port-sharing behavior is the one place in this codebase where running the tool carelessly could put it there anyway.

The port-sharing socket option is off by default and should stay off for any capture taken on, or anywhere near, a host that a live show's MultiSync unicast traffic actually reaches. If a bench setup enables it deliberately, for example to run two probe instances at once for comparison, only do so against an idle FPP instance, never during a real show.

## Before you start: what this tool does to the network

By default `showmesh-multisync-probe` only listens. It joins the MultiSync multicast group, opens a UDP socket on port 32320 (or whatever `-listen` overrides it to), and reads. It transmits nothing and has zero effect on the show, and it does not by itself cause the port-sharing hazard above; that hazard is about *where* (which host) you run it, not something this procedure's default flags turn on or off.

The one exception is `-respond-discover`. If you pass it, the probe answers any discover ping it observes with a Ping packet of its own, addressed back to the sender. This is a real transmission onto the show network, done specifically so the probe appears as a device in FPP's own MultiSync UI (see RES-002's item 5). Leave it off unless you specifically want to check that UI behavior; every other capture in this procedure should run without it.

## What to run

From the repository root:

```
make build
./bin/showmesh-multisync-probe -duration 5m -out captures/res-002-<label>.jsonl
```

Useful flags:

- `-iface <name>`: join the multicast group on one specific interface instead of every suitable one. Use this on a multi-homed capture host (for example a laptop with both Wi-Fi and a wired connection to the show network) so you know exactly which interface is being exercised.
- `-listen <host:port>`: override the bind address. Leave this alone unless you have a specific reason; the default is the real FPP_CTRL_PORT (32320), which is what you want for a realistic capture.
- `-duration <duration>`: how long to capture, e.g. `5m`, `45m`. Omit it (or pass `0`) to run until you press Ctrl-C.
- `-step-ms <n>`: the step time (in milliseconds) of the sequence you are about to play, if you know it. This only affects the position the probe derives from `FrameNumber` on packets where `SecondsElapsed` is unusable, and the Timeline/drift numbers built on top of that; it does not affect the raw packet capture. 25ms (the default) is a common FPP sequence step time, not a guarantee for your show.
- `-quiet`: suppress the human-readable per-packet stdout line. The JSONL file is written either way; use this if you want a clean terminal during a long capture and plan to review the JSONL and the final summary instead.
- `-out <path>`: where the JSONL evidence file goes. If omitted, it defaults to `showmesh-multisync-capture-<UTC timestamp>.jsonl` in the current directory. Give each capture in this procedure a distinct, meaningful name (see the per-item runs below) so you are not left guessing which file is which afterward.

Stop a capture early with Ctrl-C (SIGINT) or SIGTERM. The probe prints its summary and exits cleanly either way; a duration that elapses naturally behaves the same way.

## The five captures

RES-002 lists five open bench items. Each needs its own capture, with a specific action taken on the FPP side while the probe is running. Run these as five separate invocations, not one combined capture, so each JSONL file and summary maps cleanly to one item; nothing stops you from also reviewing all five for the other items opportunistically (the summary always reports on all five from whatever the capture actually saw).

### 1. Cadence and jitter: a normal playlist run

```
./bin/showmesh-multisync-probe -duration 3m -out captures/res-002-item1-cadence.jsonl
```

Start a normal FPP playlist (sequence plus audio if your show uses it) shortly after starting the probe, and let it run for the capture window. Use a playlist entry that resembles your actual reference show's pixel and matrix load; an idle or lightly loaded FPP will not exercise the "under load" condition item 1 asks about, and the summary will say so if it cannot tell.

### 2. Lifecycle ordering, pause, and seek

```
./bin/showmesh-multisync-probe -duration 3m -out captures/res-002-item2-lifecycle.jsonl
```

Start the probe, then start a sequence from FPP, and while it plays:

- Pause it from the FPP UI, wait a few seconds, then resume.
- Seek to a different position in the sequence.
- Let it finish or stop it manually.

The probe's summary lists every OPEN/START/STOP event in order and checks whether OPEN preceded START per filename. It does not know which timestamp corresponds to your pause or your seek; note the wall-clock time you triggered each action (a phone timestamp or a note is enough) and cross-reference it against the JSONL's `wall_clock` field afterward.

### 3. STOP and BLANK at three different endings

Run this as three short captures back to back, one per ending:

```
./bin/showmesh-multisync-probe -duration 2m -out captures/res-002-item3-playlist-end.jsonl
```
Let a short playlist entry play to its natural end.

```
./bin/showmesh-multisync-probe -duration 2m -out captures/res-002-item3-manual-stop.jsonl
```
Start a sequence and stop it manually from the FPP UI partway through.

```
./bin/showmesh-multisync-probe -duration 2m -out captures/res-002-item3-fppd-shutdown.jsonl
```
Start a sequence, then stop the `fppd` service itself (not just the playlist) partway through, e.g. `systemctl stop fppd` on the FPP host, or the equivalent for your install.

Each summary lists every STOP and BLANK observed with timings. Compare the three files to see whether the three endings produce different packet patterns (in particular, whether the shutdown case produces no STOP at all, the "orphaned no-STOP" case RES-002 calls out).

### 4. Clock drift over a long run

```
./bin/showmesh-multisync-probe -duration 45m -out captures/res-002-item4-drift.jsonl -step-ms <your sequence's actual step time>
```

Play a single long sequence, or a playlist that loops the same sequence, for the full window; 30 to 60 minutes per RES-002. Getting `-step-ms` right matters more here than in the other captures: it is what the drift series is measured against. If you do not know your sequence's step time, xLights and FPP both show it in the sequence properties.

The summary's item 4 section reports the drift series (master position vs. this tool's uncorrected free-run estimate) for sequence and media separately, and will tell you plainly if the capture ran short of the 30 to 60 minute window.

### 5. Multicast and interfaces

```
./bin/showmesh-multisync-probe -duration 3m -out captures/res-002-item5-transport.jsonl
```

Run this once with FPP configured for its default (multicast) MultiSync mode, and, if you can reconfigure FPP's MultiSync settings, once more each for broadcast and unicast mode, each as its own capture (`-out captures/res-002-item5-broadcast.jsonl`, etc.). The summary reports which transports actually delivered a packet and the outcome of the multicast group join on every interface the probe considered. It cannot see IGMP snooping or querier behavior on your switch; if you want that evidence too, you will need switch-side capture or logs (port mirroring, switch CLI, or SNMP), which is outside this tool's scope.

To specifically check the discover-ping / MultiSync UI behavior mentioned in RES-002:

```
./bin/showmesh-multisync-probe -duration 2m -out captures/res-002-item5-discover.jsonl -respond-discover
```

Every other capture in this procedure, and every other flag combination in this document, is read-only: the probe only listens, and answers nothing. `-respond-discover` is the one exception, and the only thing in this whole procedure that transmits: with it enabled, the probe answers any discover ping it observes with a Ping packet addressed back to the sender, a real packet placed onto the show network. Without it (the default), FPP never learns this probe exists, no matter how long a capture runs.

While this capture runs, watch FPP's MultiSync systems list in its own UI (the page that lists known MultiSync devices). Once the probe answers a discover ping, expect a new entry to appear there for this probe's host: hostname and IP matching this machine, a non-zero version number, and a hardware/system type string identifying it as a ShowMesh device in FPP's generic "other systems" bucket rather than as another FPP instance (see RES-002's "Third-party interoperability" section for the reserved-ID etiquette this follows). If no entry appears, that is itself evidence worth recording, not a tool failure to work around: it means either no discover ping reached this host, the response did not reach FPP, or FPP's UI does not surface non-FPP MultiSync devices the way RES-002's source reading expected. This tool only reports whether it sent a response; confirming FPP actually displayed it requires looking at that UI yourself during the run.

## Where the output lands

Each run produces two things:

- The JSONL file at the path you gave `-out` (or the default timestamped name in the current directory): one record per received datagram, with wall-clock time, monotonic offset since capture start, source address, inferred transport, the full raw bytes as hex, every decoded field, any decode error, a drift sample where applicable, and both Timeline snapshots (sequence and media). This is the primary evidence; it is complete enough to re-derive the byte layout offline without repeating the capture.
- The summary report on stdout, organized by RES-002's five open items, printed once the capture ends (duration elapsed, or Ctrl-C/SIGTERM). Redirect it to a file if you want to keep it verbatim: add `2>captures/res-002-<label>.log` to also capture the startup banner and any warnings, or just copy the summary block from your terminal.

Nothing is deleted or overwritten by later runs unless you reuse the same `-out` path, so use distinct filenames per the per-item runs above.

## After the captures: what to do with the results

This procedure's job ends at "evidence collected." Turning that evidence into a verification result is a separate, deliberate step, and it is the project owner's call, not something this tool or a future automated run should do on its own:

1. Review each capture's summary against what you actually did on the FPP side (the pause/seek timestamps you noted, which ending you triggered, whether the drift capture ran the full 30 to 60 minutes, etc.).
2. Skim the JSONL for anything the summary's prose did not surface: an unexpected packet type, a decode error, a transport you did not expect to see traffic on.
3. If the evidence answers an open item to your satisfaction, attach the relevant capture file(s) (or a trimmed excerpt, if the full JSONL is unwieldy) to [RES-002](../research/RES-002-fpp-multisync-compatibility.md)'s evidence section, recording the FPP version, network mode, and topology the capture was taken under, per the research record conventions in `docs/research/README.md`.
4. Only once RES-002's open items are addressed by recorded evidence, move RES-002's status from `planned` to `testing` (or further, per the evidence ladder) yourself. Do not have an agent or this tool make that status change; CLAUDE.md and RES-002 both require research record status moves to be a deliberate, human-reviewed decision, not a side effect of running a capture.
