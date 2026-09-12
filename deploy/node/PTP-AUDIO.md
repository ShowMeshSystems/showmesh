# PTP-disciplined PipeWire audio

This is the install path that makes a ShowMesh audio node's sound card
follow a PTP clock instead of free-running on its own crystal: `ptp4l`
disciplines a PHC (or, absent one, the system clock), and a PipeWire
graph driven from that clock rate-matches the ALSA sink to it. Separate
from `deploy/node/README.md`, which installs the ShowMesh agent itself;
run both on a node that plays synchronized audio.

RES-019 sections 4.5, 5.1, 5.3, and 7.2 (Track I, seam I4, candidate A,
ruled by the owner 2026-09-11). [Track I](../../docs/build/TRACK-I-clock-and-sync.md)
is the build record this seam belongs to.

## Why ptp4l runs as its own systemd service, not inside the agent

The repository already has a ShowMesh-managed PTP provider
(`internal/agent/clock`'s `ManagedProvider`): given the choice, it writes
its own `ptp4l` config and supervises the process itself. This install
path deliberately does **not** use that provider. It installs `ptp4l` as
an independent systemd service instead, and expects the agent's
`node.clock` to be configured as `provider=external`, observing that
service's read-only socket.

The reason is durability, not preference: `PipeWire is a system service
independent of the ShowMesh agent's own lifecycle. If the agent owned
`ptp4l`, an agent crash or restart would take the PHC discipline down
with it, and PipeWire's `clock.device` would lose its sync source for as
long as the agent was down -- turning an agent-level fault into an audio
clock fault, which is exactly the kind of coupling the project's
degrade-safely property forbids. With `ptp4l` running independently, the
PHC stays disciplined and the PipeWire graph stays clocked from it
whether or not the agent process is running at all.

**This means exactly one thing about agent configuration**: never set
this node's `node.clock` to `provider=managed`. RES-019 section 5.3 is
explicit that exactly one component owns `ptp4l` on an interface, and
after running this install path, that component is
`ptp4l-showmesh.service`. Configure the agent instead with:

```sh
showmeshctl node-clock set <node-id> \
  --provider external \
  --interface <interface> \
  --domain <domain>
```

(`--external-uds-address` defaults to `/var/run/ptp/ptp4lro`, which is
exactly the socket `ptp4l-showmesh.service` publishes; only pass it if
you changed the service's config by hand.) See `cmd/showmeshctl/cmd_nodeclock.go`
for every flag.

## What `install-ptp-audio.sh` does

```sh
sudo deploy/node/install-ptp-audio.sh <interface> <domain> [follower|grandmaster|auto] [audio-card-match]
```

`audio-card-match` (default `M4`) is a substring/glob fragment of the
sound card's ALSA device name. It is not cosmetic: it is the only thing
standing between this script and a hardcoded assumption that every
ShowMesh audio node uses an MOTU M4. Point it at a different card's name
fragment to reuse this script unmodified for a different interface.

Idempotent: every file it writes is fully derived from its arguments, so
re-running it (same or different arguments) simply regenerates them.
Unlike `deploy/node/install.sh`'s `agent.env`, nothing here is
operator-edited state that a re-run must avoid touching.

1. **Installs packages**: `linuxptp`, `pipewire`, `pipewire-audio`,
   `wireplumber`, `gstreamer1.0-pipewire` (Debian 13 package names,
   confirmed against the real `apt` archive, not assumed).
2. **Finds the PHC**, reading `/sys/class/net/<iface>/device/ptp/` directly
   rather than depending on `ethtool` (not installed on the reference
   node). If a `ptpN` entry exists, `ptp4l` uses hardware timestamping
   against `/dev/ptpN`. If none does -- the Raspberry Pi 3B+'s LAN7515
   USB NIC has no PHC at all, RES-019 section 4.6 -- `ptp4l` uses software
   timestamping instead, which disciplines `CLOCK_REALTIME`. Either way
   the node ends up PTP-disciplined; only the clock source differs, and
   the script says plainly which one applies.
3. **Makes this node the only writer of its own system clock when it has
   no PHC.** Software timestamping disciplines `CLOCK_REALTIME` directly
   (there is no PHC to discipline instead); an NTP client left running on
   the same node disciplines the same clock, and neither daemon knows the
   other exists. Measured on a real Raspberry Pi 3B+: `systemd-timesyncd`
   and `ptp4l` fought over `CLOCK_REALTIME` at the same time. On a PHC-less
   node, the script checks `systemd-timesyncd`, `chrony`, and `ntp` for an
   active instance, stops and disables whichever it finds (`timedatectl
   set-ntp false` for `systemd-timesyncd`, `systemctl disable --now` for
   the others), and **fails loudly** if it cannot -- it will not leave two
   things disciplining one clock. A node **with** a PHC keeps its NTP
   client running: there, `ptp4l` only ever touches the PHC, and NTP keeps
   the system clock honest for step 4 below.
4. **Disciplines a `grandmaster`-role node's own PHC from its system
   clock**, when that node has one. A PHC free-runs from whatever it held
   at boot until something disciplines it; left alone, a `grandmaster`-role
   node's `ptp4l` announces that arbitrary epoch as the domain's time to
   every follower on the wire. Measured on `showmesh-node-01`: an
   undisciplined PHC read **17.7 seconds** behind the host's own
   NTP-disciplined system clock, and a PHC-less follower on the same
   domain slewed its own wall clock **54.7 seconds** away from real time
   following it -- the class of failure FPP removed `phc2sys` over. The
   script installs and enables `phc2sys-showmesh.service`
   (`phc2sys -s CLOCK_REALTIME -c /dev/ptpN -O 0`), ordered
   `Before=ptp4l-showmesh.service` so the correction is underway before
   `ptp4l` starts announcing this PHC's time (correcting an 18-second PHC
   error while a show is already running would be an 18-second clock step
   mid-show), and reports the PHC-to-system offset it found via `phc_ctl`
   *before* installing the unit, so an operator sees the size of the
   correction about to happen. `follower`-role nodes never get this unit:
   their PHC belongs to the domain, not to their own system clock. A stale
   unit from a previous run with a different role or interface is removed.
5. **Makes a `grandmaster`-role node announce an arbitrary (non-TAI)
   timescale**, so every node in the domain reads the same wall-clock
   number. `ptp4l`'s default `ptpTimescale 1` (PTP/TAI) means a
   PHC-less follower subtracts `currentUtcOffset` (37 seconds, as of this
   writing) from what it reads; measured on the real pair
   (`showmesh-node-01` grandmaster with a PHC kept at UTC by `phc2sys`, a
   Raspberry Pi 3B+ follower with none), that put the two nodes' wall
   clocks **exactly 36.7 seconds apart**, even with `ptp4l`'s own
   `master_offset` reading a healthy half a millisecond. `ptpTimescale` is
   **not** a valid `ptp4l.conf` option -- putting it there makes `ptp4l`
   refuse to start at all ("failed to parse configuration file"). It is a
   runtime-only `pmc SET GRANDMASTER_SETTINGS_NP` (`currentUtcOffset 37`,
   `ptpTimescale 0`, PTP/ARB), so the script installs a small script
   (`/usr/local/lib/showmesh/announce-grandmaster-timescale.sh`) and wires
   it as `ExecStartPost` on `ptp4l-showmesh.service`, retried for up to
   10 seconds after every (re)start (the management socket may not exist
   the instant `ptp4l` forks). **This makes the domain's time this
   grandmaster's own UTC, not a traceable TAI reference** -- an accepted
   tradeoff for a system whose actual requirement is every node agreeing
   on the same wall clock, not tracing that clock to a TAI standard.
   `follower`-role nodes never get this: they take whatever timescale the
   domain's real grandmaster announces.
6. **Gives `follower`-role nodes a `step_threshold`** (`1.0` second) in
   `ptp4l.conf`. The default, `0.0`, means `ptp4l` never steps a gross
   error after its first update -- only slews it, at the servo's 10%
   maximum rate. Measured on the real Raspberry Pi 3B+ follower: a
   37-second gap (from the timescale mismatch above, before it was fixed)
   took about six minutes to close this way, wrong the entire time, twice
   in one night. `1.0` corrects anything at or above a one-second error
   instantly and still slews anything smaller. A step is a discontinuity,
   so it must happen before a show starts, never during one -- this is
   why a follower node's own clock state is worth checking before
   showtime, not assumed to have already converged.
7. **Writes `/etc/showmesh/ptp4l.conf`** and a systemd unit
   (`ptp4l-showmesh.service`) that runs it, `clientOnly`/`priority1` set
   by the requested role:

   | Role | clientOnly | priority1 | Behavior |
   |---|---|---|---|
   | `follower` (default) | 1 | 255 | Never becomes master no matter what else is on the wire. The Day-0 show network case: the network already has a grandmaster. |
   | `grandmaster` | 0 | 248 | Runs full BMCA; alone on the wire, elects itself master. The dev-network case: one node, nothing else speaking PTP. 248 is worse than the 128 professional gear typically declares, so real gear introduced later still wins BMCA and demotes this node to follower automatically. |
   | `auto` | 0 | (unset, linuxptp default 128) | BMCA decides with no thumb on the scale either way. |

   `priority1 255` for `follower` is not optional decoration: a
   clientOnly port left at linuxptp's plain default (128) BMCA-outranks a
   `grandmaster`-role node's 248, and the port gets stuck logging "master
   state recommended in slave only mode" instead of ever reaching SLAVE
   -- measured in this repository's own `bench/ptp-node`, not assumed.

8. **Installs a udev rule** (`/etc/udev/rules.d/99-showmesh-ptp.rules`)
   granting the `showmesh` group read access to any `/dev/ptpN`: the
   agent's external clock provider reads the PHC directly, and PipeWire's
   node.driver opens it too. Both already run as the `showmesh` user (see
   the next step), so no separate group grant is needed for either.
9. **Creates the `showmesh` system user** (if `deploy/node/install.sh` has
   not already, so the two installers' order never matters) and installs
   `pipewire-showmesh.service` and `wireplumber-showmesh.service` to run
   as that SAME user, sharing one runtime directory (`/run/pipewire`) with
   each other and with the agent. Not a separate `pipewire` account: on a
   ShowMesh audio node, PipeWire and the agent are both ShowMesh's own
   components with no third party to isolate from, and MEASURED on
   node-01, a separate account bought nothing but a socket permission
   problem -- `RuntimeDirectoryMode=0700` makes `pipewire-showmesh.service`
   create `/run/pipewire/pipewire-0` owned by, and readable/writable only
   by, its own account, and `connect()` to a Unix socket needs the WRITE
   bit specifically, so any other account (including the agent's, under
   the old separate-user design) got `EACCES` before PipeWire's own
   access module was ever consulted. Sharing the account removes the
   problem outright rather than loosening the socket's mode. This is
   necessary, not cosmetic, for another reason too: Debian's `pipewire`
   and `wireplumber` packages ship only user-session units
   (`/usr/lib/systemd/user/{pipewire,wireplumber}.service`), which assume
   a logged-in desktop session. This node has none, and `systemctl --user`
   needs a lingering login this node is never going to have. A PipeWire
   that only exists inside a user session is a node that goes silent
   after every reboot. The agent's own systemd unit gets a drop-in
   (`/etc/systemd/system/showmesh-agent.service.d/10-showmesh-ptp-audio-runtime.conf`)
   setting `PIPEWIRE_RUNTIME_DIR`/`XDG_RUNTIME_DIR` to this same runtime
   directory, so its own `pw-dump` and `pipewiresink` calls look in the
   right place; restart `showmesh-agent.service` after running this
   script if it was already active.
10. **Installs the PipeWire clock config**
   (`/etc/pipewire/pipewire.conf.d/10-showmesh-ptp-clock.conf`): a
   `support.node.driver` named `showmesh-ptp-driver`, `priority.driver
   210000` (above the stock config's own highest entry, 190000, so it
   wins driver selection without needing to know every other driver's
   exact value), `node.group` set to the value in
   `deploy/node/ptp-audio/ptp-group.conf` (`showmesh-ptp` by default),
   clocked from `clock.device=/dev/ptpN` when a PHC was found or
   `clock.id=realtime` when it was not, and `default.clock.rate=48000` to
   match the sound card's native rate.

   **`node.group` is not optional decoration.** PipeWire/WirePlumber elect
   a driver *within* a node.group, not graph-wide: a sound card's own
   ALSA output node sits in its own per-device group by default
   (WirePlumber's `pro-audio-N`) and elects itself driver there,
   regardless of `priority.driver`, because the two nodes were never even
   compared. Proven wrong the hard way on real node hardware
   (`showmesh-node-01`): before this entry existed, `showmesh-ptp-driver`
   sat unused in a group of one while the card drove itself. Putting the
   driver and the card's ALSA output node in the same group is what makes
   `priority.driver` the comparison that actually happens.
11. **Installs a WirePlumber rate rule**
   (`/etc/wireplumber/wireplumber.conf.d/51-showmesh-alsa-rate.conf`,
   generated from `51-showmesh-alsa-rate.conf.template`) that:
   - pins the sound card (matched by `audio-card-match`, default `M4`)
     onto WirePlumber's `pro-audio` profile and disables
     `api.acp.auto-profile`. **Required, not optional**: WirePlumber's
     default UCM profile for a multichannel USB interface splits it into
     plain stereo sinks, and the four-channel node this rate lock needs
     does not exist under that profile. A `wpctl set-profile` applied by
     hand does not survive a WirePlumber restart; only a persisted rule
     does. This is not M4-specific -- any card WirePlumber's UCM logic
     splits by profile has the identical problem -- but the match stays
     per-card (via `audio-card-match`) rather than one pattern meant to
     catch every card on the host, so this script never force-changes a
     card's profile it was not told to touch.
   - pins that card's ALSA output node to 48 kHz, puts it in the same
     `node.group` as `showmesh-ptp-driver`, and disables its suspend
     timeout so it never goes idle-silent between cues.
   - deliberately leaves the card's **capture (input)** node out of the
     group: this seam's proven use case is playback, and grouping the
     input node would only add an unused follower with no consumer
     benefiting from it. If a capture use case needs PTP-locked timing
     later, evaluate it on its own rather than assuming this group
     applies.

   **Unverified against real hardware by this repository's own bench**:
   `bench/ptp-node` has no ALSA card, so this file is syntax-checked
   (WirePlumber starts cleanly with it present) but its match rules and
   profile pin have never matched a real card there. `bench/ptp-node`
   does prove the underlying `node.group` election mechanism generically,
   with a synthetic node (see that bench's own README); the group name,
   the profile pin, and the M4-specific match were proven separately, by
   hand, on real node hardware (`showmesh-node-01`) before being encoded
   here -- see "What this repository's own verification proved" below for
   exactly what that covered and what still needs a re-run of this
   script.
12. **Enables and starts everything**, or -- on a host with no systemd PID
   1 (a plain container) -- installs every file and says exactly what it
   could not do and why, the same pattern `deploy/node/install.sh` uses.
13. **Prints a summary**: installed package versions, the PTP role/mode/
   PHC it configured, the live port state read back from `pmc` (SLAVE
   with a grandmaster, MASTER if this node is currently the domain's
   active clock, or an honest "not yet available" if `ptp4l` is still
   settling), and which clock the PipeWire graph is configured for.

## What `verify-ptp-audio.sh` checks

```sh
deploy/node/verify-ptp-audio.sh [--play <seconds>] [--alsa-match <pattern>]
```

Read-only; never installs, starts, stops, or reconfigures anything. In
one pass:

Exit status distinguishes three outcomes, not two: `0` if every check ran
and passed, `1` if every check ran but at least one found a real negative,
and `2` if at least one check could not run its own instrument at all (a
missing tool, a crashed parser) and so produced no answer either way.
`2` is not a stronger `1`: it means part of this report is unreliable, and
an operator should fix whatever kept that check from running before
trusting a `1` or a `0` next to it. This mattered in practice: the
driver-election check used to hand a real node's `pw-dump` output to
`python3` as a command-line argument, which fails with "Argument list too
long" once a real sound card is in the graph (a container's smaller graph
never hit this); the failed check then read as a specific, confident,
*wrong* answer -- "no PipeWire node named showmesh-ptp-driver found" --
when the driver was in fact present and driving the card. `pw-dump`'s
output is now always fed to its parser
(`deploy/node/ptp-audio/driver-election.py`) on stdin, and any check whose
own tool is missing or fails to run is reported as unable to run, not as
a negative result.

1. **Is `ptp4l` locked, and to whom** -- `pmc GET PORT_DATA_SET` for port
   state, `pmc GET TIME_STATUS_NP` for `gmIdentity` and `master_offset`.
   Reports honestly whether this node is itself the domain's grandmaster
   (`MASTER`) or following one (`SLAVE`), rather than assuming the latter.
2. **Does the ALSA node's driver election actually land on the PTP
   driver** -- `pw-dump`, reading each node's `node.driver-id` (the id of
   the node ACTUALLY driving it) rather than a node's own `node.driver`
   flag (which only says a node is *capable* of driving, not who drives
   it -- this repository's earlier version of this check read exactly
   that flag and reported a clean pass on real node hardware while the
   card was in fact driving its own group). Pass unless at least one node
   matching `--alsa-match` (default `alsa_output`) has a `node.driver-id`
   equal to `showmesh-ptp-driver`'s own id.
3. **ALSA sink xrun count** -- best-effort, via `pw-top -b`'s `ERR`
   column (not exposed through `pw-dump`). Informational only: `pw-top`'s
   exact column layout is not a guaranteed contract, so a parse failure
   here is reported and does not fail the run.
4. **The load-bearing reading**: runs a short `pipewiresink` pipeline
   with `GST_DEBUG=audiobasesink:6` and counts skew/slaving lines. With
   the rate lock actually working there should be **none** -- `alsasink`
   is no longer in the signal path at all, `pipewiresink` is -- so any
   line here means the pipeline under test is not actually going through
   this seam (RES-019 section 7.1 explains why `alsasink`'s own slaving
   was never going to be good enough on its own).

Pass `--play 0` to skip step 4 (faster, but it is the check that actually
proves the rate lock is doing anything -- steps 1-3 only prove the pieces
exist and are correctly wired to each other, not that the card is
correcting its rate).

## What a correct driver election looks like, and what a broken one looks like

`pw-top` on the real node, once `node.group` and the pro-audio profile
pin are both in place, shows the driver with the card and the engine as
its followers:

```
R   30  showmesh-ptp-driver                                    (driver, QUANT 1024, RATE 48000)
R   46  + alsa_output...pro-output-0    S32LE 4 48000          (follower)
R   53  + ptpharness                    F32LE 4 48000          (follower)
```

When `node.group` is missing or names don't line up, the card instead
drives itself, and `showmesh-ptp-driver` shows up alone with no
followers -- the exact failure this install path used to produce
silently, and the reason `verify-ptp-audio.sh`'s driver-election check
now reads `node.driver-id` instead of a node's own `node.driver` flag
(see above).

## What this repository's own verification proved, and what it did not

`bench/ptp-node` (see its own README) proved, on two Debian 13
containers with no PHC: real package installation, correct PHC/no-PHC
detection and the `clock.id=realtime` fallback, all three roles producing
the documented `clientOnly`/`priority1` values, a real `ptp4l` reaching
MASTER (grandmaster role, alone on the wire and with a follower present)
and SLAVE (follower role, with `pmc`-reported grandmaster identity and
offset), a real PipeWire electing the PTP driver, a `pipewiresink`
pipeline playing without error, and the `node.group` driver-election
mechanism itself (a synthetic sink node placed in `showmesh-ptp-driver`'s
group ends up with `node.driver-id` pointing at it).

It did **not** prove, and this repository does not claim from its own
bench: hardware timestamping against a real PHC, an actual ALSA sink
being rate-matched (no `/dev/snd` in a container), WirePlumber's own ALSA
monitor putting a real card's node into this group or onto the
pro-audio profile, any of this surviving a real systemd boot, or the
`audiobasesink:6` skew-slaving absence against a real `alsasink` (there
is none in the bench's own pipeline to have slaved in the first place).

The `node.group` entry, the `device.profile = "pro-audio"` /
`api.acp.auto-profile = false` pin, and the driver election they produce
together **were** proven separately, by hand, on real node hardware
(`showmesh-node-01`) before being encoded into this script: applying
those same settings by hand there produced the `pw-top` reading shown
above. What has not yet been verified is this *script*, as changed here,
against that same real hardware -- that re-run, and a fresh `pw-top`
capture against it, is the next step and is not this session's own
evidence.

## Undoing it

```sh
sudo systemctl disable --now ptp4l-showmesh.service pipewire-showmesh.service wireplumber-showmesh.service
sudo systemctl disable --now phc2sys-showmesh.service   # only present on a grandmaster-role node that had a PHC
sudo rm -f /etc/systemd/system/ptp4l-showmesh.service \
           /etc/systemd/system/pipewire-showmesh.service \
           /etc/systemd/system/wireplumber-showmesh.service \
           /etc/systemd/system/phc2sys-showmesh.service
sudo rm -f /usr/local/lib/showmesh/announce-grandmaster-timescale.sh   # only present on a grandmaster-role node
sudo rm -f /etc/systemd/system/showmesh-agent.service.d/10-showmesh-ptp-audio-runtime.conf
sudo systemctl daemon-reload
sudo rm -f /etc/showmesh/ptp4l.conf
sudo rm -f /etc/udev/rules.d/99-showmesh-ptp.rules
sudo udevadm control --reload-rules
sudo rm -rf /etc/pipewire/pipewire.conf.d/10-showmesh-ptp-clock.conf \
            /etc/wireplumber/wireplumber.conf.d/51-showmesh-alsa-rate.conf
sudo apt-get remove linuxptp pipewire pipewire-audio wireplumber gstreamer1.0-pipewire
```

The `showmesh` user and group are left in place: PipeWire runs as the
same account as the agent (no separate account to remove), and
`deploy/node/install.sh` depends on the account existing regardless.
Restart `showmesh-agent.service` if it was active, so it stops carrying
the now-removed `PIPEWIRE_RUNTIME_DIR`/`XDG_RUNTIME_DIR` drop-in's
environment forward from its own process state.

If the agent's `node.clock` was set to `provider=external` for this
node, revert it (or remove the node's clock config entirely) before or
alongside this teardown -- otherwise the agent keeps polling a socket
that no longer exists and reports `unsynchronized`.

If `install-ptp-audio.sh` disabled an NTP client on this node (only on a
PHC-less node -- see step 3 above), put it back once `ptp4l` is gone,
or this node's system clock is disciplined by nothing at all:

```sh
sudo timedatectl set-ntp true                 # systemd-timesyncd
sudo systemctl enable --now chrony            # or: chronyd, ntp
```
