# PTP audio node bench

Bench scaffolding proving `deploy/node/install-ptp-audio.sh` and
`deploy/node/verify-ptp-audio.sh` on Debian 13 containers with no PHC and
no systemd as PID 1. Not part of the ShowMesh product, not imported by
`internal/` or `pkg/`, exactly like `bench/audio-node` and
`bench/node-install`. Not part of `make check` or CI.

RES-019 sections 4.5, 5.1, 5.3, and 7.2 (Track I, seam I4, candidate A).

## What this proves, and what it cannot

**Proves**, with two real containers on their own docker network, neither
with a PHC:

- `install-ptp-audio.sh` installs linuxptp, pipewire, pipewire-audio,
  wireplumber, and gstreamer1.0-pipewire (real Debian 13 package names,
  confirmed against the actual `apt` archive, not assumed);
- it correctly detects the absence of a PHC and falls back to software
  timestamping, and points PipeWire's node.driver at `clock.id=realtime`
  instead of a PHC device;
- the three roles (`follower`, `grandmaster`, `auto`) produce the
  `clientOnly`/`priority1` combination documented in the script's own
  usage comment;
- a real `ptp4l`, started from the exact config the script generates,
  reaches **MASTER** on the grandmaster-role container (alone on the wire
  until the follower starts, and still MASTER once it has) and **SLAVE**
  on the follower-role container, with `pmc` reporting the follower's
  `gmIdentity` and `master_offset` against that same grandmaster;
- a real PipeWire, started from the exact config the script generates,
  elects the `showmesh-ptp-driver` node (`support.node.driver`, priority
  210000, `clock.id=realtime`) as a driver in the graph;
- a `pipewiresink` GStreamer pipeline negotiates against that graph and
  plays without error, into a synthetic `support.null-audio-sink` node
  this bench creates only because the container has no real ALSA card for
  WirePlumber's monitor to find (`51-showmesh-alsa-rate.conf`'s pinned
  48 kHz rate rule and pro-audio profile pin are installed but never
  exercised: nothing in this bench creates an ALSA device for them to
  match);
- **the node.group driver-election mechanism itself**: that synthetic sink
  node is created with `node.group` set to the same
  `deploy/node/ptp-audio/ptp-group.conf` value `install-ptp-audio.sh`
  writes into `showmesh-ptp-driver`'s own config, and `pw-dump` confirms
  the driver (not the sink) ends up with `node.driver=true` while sharing
  that group -- the exact reading `verify-ptp-audio.sh` now checks against
  a real ALSA node, and the same mechanism that was silently broken on
  real node hardware before this node.group entry existed (a sound card's
  ALSA node sat in its own per-device group and drove itself, regardless
  of `showmesh-ptp-driver`'s `priority.driver`);
- the exact `driver-election.py` parser `verify-ptp-audio.sh` uses, fed
  pw-dump output padded past this host's own `ARG_MAX` on stdin, still
  finds the driver -- and that the same oversized payload does fail past
  `ARG_MAX` when passed as an argument/environment variable instead,
  confirming the fix (a real node's `pw-dump` output can exceed `ARG_MAX`
  outright; a container's own small graph never does, so this bench cannot
  otherwise exercise the failure this fixed);
- **the two negative cases of clock ownership this bench can reach without
  real hardware**: `phc2sys-showmesh.service` is never written on this
  bench's PHC-less containers, for either role (only a `grandmaster`-role
  node with a real PHC gets it, which this bench cannot produce), and the
  NTP-detection branch runs and reports honestly that no NTP client is
  active here (this bench never installs or runs one, so it cannot prove
  the branch actually stops a real NTP client -- the orchestrator proved
  that by hand on the Raspberry Pi 3B+, see `PTP-AUDIO.md`).

**Cannot prove, and does not claim to**:

- hardware timestamping, or `clock.device` against a real `/dev/ptpN` --
  no container has a PHC, so every run here falls back to
  `clock.id=realtime` and this is the only path exercised;
- a real ALSA sink actually being rate-matched to the graph driver, or
  WirePlumber's own ALSA monitor actually putting a real sound card's node
  into `showmesh-ptp-driver`'s group or onto the pro-audio profile -- no
  `/dev/snd` in a container, so `51-showmesh-alsa-rate.conf`'s match
  rules, profile pin, and rate pin are syntax-checked (WirePlumber starts
  cleanly with it present) but never exercised against a real card;
- that any of this survives a real systemd. This bench manually starts
  `ptp4l`, `pipewire`, and `wireplumber` as plain background processes
  from the exact config `install-ptp-audio.sh` writes, because a plain
  `docker run` container does not run systemd as PID 1 (`bench/node-install`
  makes the identical point about `deploy/node/install.sh`'s unit).
  `verify-ptp-audio.sh` itself is not run by this bench for the same
  reason: every one of its checks starts with `systemctl is-active`.
- the `GST_DEBUG=audiobasesink:6` skew-slaving check `verify-ptp-audio.sh`
  runs on a real node: `alsasink` is never in this bench's own pipeline
  (there is no ALSA sink to put it in front of), so there is nothing here
  that could produce a slaving line either way;
- `phc2sys-showmesh.service` actually disciplining a real PHC from a real
  system clock, or the NTP-detection branch actually stopping a real,
  running NTP client -- no PHC and no NTP client in this bench's own
  containers. Both were proven by hand on real hardware
  (`showmesh-node-01` and a Raspberry Pi 3B+, see `PTP-AUDIO.md`); this
  bench proves only that the role/PHC-conditional generation logic takes
  the correct negative branch and that the NTP-detection code path runs
  without error and reports honestly.

## What this bench actually found

Building and running this bench surfaced three real bugs that inspection
alone did not, all fixed in the scripts this bench exercises:

1. A `follower`-role node left `priority1` at ptp4l's plain default (128)
   beat a `grandmaster`-role node's 248 on BMCA's comparison, so the
   follower's own clock looked preferable and it never left LISTENING
   ("master state recommended in slave only mode: defaultDS.priority1
   probably misconfigured"). Fixed by giving `follower` role `priority1
   255`, the worst legal value.
2. `pmc`'s request domain defaults to 0; a ptp4l running a non-zero
   `domainNumber` silently drops a request in the wrong domain -- no
   error, nothing. Every `pmc` call in `install-ptp-audio.sh`,
   `verify-ptp-audio.sh`, and this bench now passes `-d <domain>`,
   sourced from the installed config, never assumed to be 0.
3. `pmc`'s field/value output lines are tab-indented, so a naive
   whitespace-split `awk` field count is off by one (`$2` is the field
   *name*, not its value). Every extraction now reads `$NF` instead.
4. PipeWire's `context.objects` needs `factory = spa-node-factory` with
   `args.factory.name = support.node.driver`, not
   `factory = support.node.driver` directly -- the indirection stock
   `pipewire.conf`'s own Dummy-Driver/Freewheel-Driver entries use, which
   a plain read of the file's comments does not make obvious.

Two further bugs were found on real node hardware (`showmesh-node-01`),
not by this bench (no ALSA card and no WirePlumber ALSA monitor activity
here), and are covered by this bench only at the mechanism level:

5. Driver election happens **within a node.group**, not graph-wide. A
   sound card's own ALSA output node sits in its own per-device group by
   default (WirePlumber's `pro-audio-N`) and elects itself driver there
   regardless of `showmesh-ptp-driver`'s `priority.driver`, which is never
   even considered because the two nodes were never in the same group.
   Fixed by giving `showmesh-ptp-driver` and the card's ALSA output node
   the same `node.group` (`deploy/node/ptp-audio/ptp-group.conf`). This
   bench's step 6 proves the mechanism generically with a synthetic sink
   node in that group; it cannot prove WirePlumber's own ALSA monitor puts
   a *real* card's node into it, which needs the real node.
6. WirePlumber's default UCM profile for a multichannel USB interface
   splits it into plain stereo sinks; the four-channel node this seam
   needs does not exist under that profile, and a `wpctl set-profile`
   applied by hand does not survive a WirePlumber restart. Fixed by a
   persisted `device.profile = "pro-audio"` / `api.acp.auto-profile =
   false` rule in `51-showmesh-alsa-rate.conf.template`. Unverified by
   this bench (no ALSA hardware).

## Running it

```sh
./bench/ptp-node/run_proof.sh
```

Run from the repository root. Builds the bench image, starts a
`showmesh-bench-ptp-gm` and a `showmesh-bench-ptp-follower` container on
their own docker network (`--cap-add=SYS_TIME`, needed for `ptp4l` to
actually step/slew `CLOCK_REALTIME` in software-timestamping mode), runs
`in-container-proof.sh` in each, and reports pass/fail. Cleans up its
containers and network on exit.

## Layout

```
bench/ptp-node/
  Dockerfile             Debian 13 + what the bench harness needs beyond
                         what install-ptp-audio.sh itself installs
                         (Python for pw-dump parsing, gstreamer1.0-tools
                         for the playback check, sudo to run pipewire/
                         wireplumber as the unprivileged service account
                         install-ptp-audio.sh creates)
  in-container-proof.sh  Runs inside one container: install, then
                         manually starts ptp4l/pipewire/wireplumber
                         (no systemd PID 1) and checks lock state,
                         driver election, and playback
  run_proof.sh           Host-side: builds the image, starts the two
                         containers, runs in-container-proof.sh in each
```
