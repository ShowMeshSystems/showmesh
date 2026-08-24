# RES-002 bench: FPP in Docker driving showmesh-multisync-probe

This is bench scaffolding for
[RES-002](../../docs/research/RES-002-fpp-multisync-compatibility.md), not
part of the ShowMesh product. It runs a real `fppd` in a container as a
controlled, repeatable MultiSync master, alongside `showmesh-multisync-probe`
from this repo, so captures can be taken without touching the live show rig
mid-programming. The actual capture runbook — what to click on the FPP side
for each of RES-002's open items, and how to turn a capture into a
verification result — is
[docs/bench/RES-002-capture-procedure.md](../../docs/bench/RES-002-capture-procedure.md).
This README is scoped to standing the bench up; read the capture procedure
before running the captures themselves.

## Purpose, and its limits

Read this before running anything, and re-read it before drawing any
conclusion from a capture taken here.

This bench **can** produce real evidence for RES-002's open items 1
(cadence/jitter — though not specifically "under load": there is no pixel
output hardware here, see below), 2 (lifecycle ordering, pause, seek), 3
(STOP/BLANK at playlist end vs. manual stop vs. `fppd` shutdown), and the
transport/version-stability portion of item 5 (does multicast/broadcast/
unicast actually deliver, is behavior consistent across the FPP versions
this bench builds).

This bench **cannot** close item 4 (clock-drift accumulation over a 30–60
minute show): drift is a property of the reference Pi's crystal and OS
scheduling, which a container on a development machine does not reproduce.
It also cannot close the IGMP-snooping half of item 5: that requires
evidence from the reference show's actual switch, which neither network mode
below provides (see "Two network modes" for exactly what Mode B does and
does not get you closer to). Do not let a capture taken here stand in for
either of those; the capture procedure's summary output says so as well.

## An architectural point worth preserving

Running `fppd` in its own container is not merely convenient — it satisfies
[ADR-013](../../docs/decisions/ADR-013-no-fpp-control-port-sharing.md)
structurally, not through operator discipline. `probe` never shares UDP
32320 with `fppd` because they are in different network namespaces; there is
no bind conflict to avoid because there is nothing to share. Port sharing in
`showmesh-multisync-probe` stays off (it is off by default) for every
capture this bench takes.

## Layout

```
bench/fpp-multisync/
  docker-compose.yml          Mode A (default): fpp-master, probe, fpp-remote (profile)
  docker-compose.macvlan.yml  Mode B overlay: redefines the network as external macvlan
  probe.Dockerfile            Bench-only build for showmesh-multisync-probe
  .env.example                Copy to .env and adjust
  captures/                   Default landing spot for captures; JSONL/log output is git-ignored
```

## Prerequisites

- Docker with Compose v2 (`docker compose`, not `docker-compose`).
- Network access to `github.com` at build time: `fpp-master` and
  `fpp-remote` build FPP directly from a git URL context (see "Versions"
  below), so there is no local FPP checkout to maintain.
- For Mode B only: a Linux Docker host with a spare parent interface for
  macvlan, and enough access to that network to create the macvlan network
  and know its subnet/gateway.

## Two network modes, and why there are two

Docker's `-p 32320:32320/udp`, which the upstream FPP `dockerBuild.sh`
comment documents as its normal run command, **forwards unicast to a host
port and does not carry multicast.** Using it here would misrepresent what
this bench tests, so `docker-compose.yml` does not publish 32320 at all —
MultiSync traffic stays on the `bench` network between containers.

**Mode A ("contained"), the default.** Every service sits on one
user-defined Docker bridge (`docker-compose.yml`'s `bench` network).
Multicast between containers on the same Linux bridge works without
touching the host's physical network, so this mode runs anywhere, including
a laptop running Docker Desktop. This is the mode this bench was actually
built and validated against (see "How this bench was validated" below) and
the one you can run yourself without any site-specific setup.

**Mode B ("wire").** Overlay `docker-compose.macvlan.yml` to put every
container on its own MAC on the real L2, reaching an actual switch:

```
docker compose -f docker-compose.yml -f docker-compose.macvlan.yml up
```

macvlan needs a parent interface, subnet, and gateway specific to your
network, so the network is declared `external: true` rather than baked into
this repo. Create it first — see the full command and its caveats in
`docker-compose.macvlan.yml`. **Mode B is not meaningfully testable on macOS
Docker Desktop**, since Docker Desktop runs inside a VM and macvlan cannot
reach the real host network from there; it is the mode intended for the
project owner's Linux Docker host.

**Even Mode B is not RES-002 open item 5's reference switch.** If the Docker
host is itself a VM (the owner's is, on ESXi), the hypervisor's own vSwitch
does its own IGMP snooping between the VM and the physical network. Mode B
gets traffic past Docker's bridge onto a real L2, but that L2 is still not
the reference show switch. Only a capture taken on, or against, that
physical switch closes item 5's IGMP question.

## Versions

FalconChristmas/fpp does not publish tagged images beyond `latest`, and its
`dockerBuild.sh` takes an `FPPBRANCH` build argument, so pinned versions are
built locally from source via a git-URL Docker build context. Confirmed
directly against the upstream repository
(`git ls-remote https://github.com/FalconChristmas/fpp.git 'refs/tags/<tag>^{}'`):

| Bench target | Branch (moves) | Tag this bench pins to | Confirmed upstream commit |
|---|---|---|---|
| 9.5, current stable (default) | `v9.5` | `9.5.3` | `7979a4bb0bb9068fea71f3b447e273d5c0ea01e3` |
| 10.0, current stable | `v10.0` (release, not `-beta`) | `10.0` | `370e62ed7e8c8318da6ee5b01312b8b75082d952` |

`docker-compose.yml` pins `FPP_GIT_REF` to an explicit **tag**, not the
floating branch name, for the same reason
`deploy/docker-compose.yml` pins Mosquitto to an exact patch version rather
than a floating minor tag: a tag pin means a rebuild months from now
reproduces the same FPP. Default is `9.5.3`. Both targets are first-class:
the same `docker-compose.yml`, `.env.example`, and `FPP_GIT_REF` variable
select either, side by side. There is no separate v10 compose file and no
version-branching in this bench's own code — the source build already
takes any upstream tag, so supporting FPP 10 here is a matter of building
with a different `FPP_GIT_REF`, not new scaffolding.

To build the 10.0 target instead of the 9.5.3 default, set in `.env`:

```
FPP_GIT_REF=10.0
```

then `docker compose build fpp-master`. Building from source pulls FPP's own
prebuilt package repo for most dependencies rather than compiling
everything locally, so a build takes on the order of a couple of minutes on
a development machine, not the much longer full-recompile time you might
expect — confirmed while building this bench (see below), and reconfirmed
for the 10.0 tag while adding FPP 10 support.

**Running a 9.5.3 bench and a 10.0 bench side by side.** `docker-compose.yml`
gives every host port a single instance's worth of identity
(`FPP_MASTER_HTTP_PORT`), so two versions cannot run from the same `.env`
at the same time (they would both try to bind the same host port).
Container and volume names are namespaced by the compose project
automatically (`docker-compose.yml` does not pin `container_name`, for
exactly this reason), so they never collide across projects. To have both
up at once, run this bench twice with distinct `.env` files (or
`COMPOSE_PROJECT_NAME`/port overrides) — e.g. a `.env` with
`FPP_GIT_REF=9.5.3` and default ports for one, and a second `.env` with
`FPP_GIT_REF=10.0`, a different `FPP_MASTER_HTTP_PORT`, and a different
`COMPOSE_PROJECT_NAME` for the other:

```
docker compose --env-file .env -p showmesh-fpp95 up -d --build fpp-master
docker compose --env-file .env.v10 -p showmesh-fpp10 up -d --build fpp-master
```

Ordinarily, though, only one bench target is needed at a time: switch
`FPP_GIT_REF` in `.env` and rebuild, the same way you would switch any other
bench setting.

**What differs between the two targets, as built here.** The 9.5.3 image's
Debian base and web UI are the "How this bench was validated" baseline
below. The 10.0 build was confirmed (see "FPP 10 verification" below) to
also build clean via the same `Docker/Dockerfile`/`dockerBuild.sh` path and
answer the same REST endpoints (`/api/system/info`,
`/api/fppd/status`, `/api/fppd/ports`, `/api/fppd/multiSyncSystems`); this
README has not exhaustively diffed every response shape between the two —
that is SM-210's job, not this bench's.

## CI's prebuilt test fixture

CI does not source-build fpp-master: GitHub-hosted runners have no
persistent Docker layer cache, so every run would pay the full build.
Instead `.github/workflows/test-integration-fpp.yml` pulls a prebuilt image
from `ghcr.io/showmeshsystems/showmesh-fpp-test` via
`docker-compose.prebuilt.yml`, an override that layers on top of
`docker-compose.yml` and replaces `fpp-master`'s `build:` with a
digest-pinned `image:`.

- Upstream FPP version: `9.5.3`
- Upstream commit the build context is pinned to:
  `7979a4bb0bb9068fea71f3b447e273d5c0ea01e3`
- Current CI tag: `9.5.3-build1`
- Current CI digest:
  `sha256:94c38cd2168ae9d5da820a678360a55837685915ffc4deb1a283604e5f01d1ff`

The commit pin fixes the `Docker/Dockerfile` and the source tree copied
into the image. It does not fully pin what gets installed: upstream's
Dockerfile runs `SD/FPP_Install.sh --branch ${FPPBRANCH}` without
`--skip-clone`, so the install step clones the ref rather than the commit.
The digest, not the commit, is what makes CI reproducible.

Local development is unaffected and still source-builds by default:

```
docker compose -f docker-compose.yml up -d --build fpp-master
```

Prebuilt mode locally, if you have access to the package. CI runs the same
two commands but the second inside a container carrying GStreamer 1.26 and
libltc, which `test/integration`'s cgo audio engine needs to compile:

```
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml pull fpp-master
SHOWMESH_FPP_TEST_PREBUILT=1 make test-integration-fpp
```

Prebuilt mode **recreates** the bench `fpp-master` container from the
pinned image, replacing a source-built container if one is running, and
then asserts that the running container really is that image. Do not run
it on a machine whose bench container you want to keep.

**Why a digest, not just the tag.** Rebuilding the same upstream git tag
does not reproduce the same image: the build installs packages from
mutable Debian and FPP apt repositories, so identical tag does not mean
identical contents. Only a `sha256` image digest is immutable.

**Publishing a new fixture** (a new `build` number or a new FPP version):
run the `Build FPP test image` workflow (`workflow_dispatch`) with
`fpp_ref`, `fpp_commit`, and `image_tag` inputs; it verifies `fpp_commit`
actually resolves from `fpp_ref` upstream before building. Take the
resulting digest from the job's summary. The workflow's inputs default to
the current 9.5.3 fixture, so a careless dispatch (accepting every default)
republishes 9.5.3, never silently starts publishing something else as CI's
fixture.

**Publishing a 10.0 fixture** works the same way, with explicit inputs
(defaults are never enough on their own for this — they exist so a
no-input dispatch stays 9.5.3):

```
fpp_ref:    10.0
fpp_commit: 370e62ed7e8c8318da6ee5b01312b8b75082d952
image_tag:  10.0-build1
```

`image_tag` convention: `<fpp_ref>-build<N>`, `N` incrementing per
re-publish of the same `fpp_ref` (`9.5.3-build1`, `10.0-build1`,
`10.0-build2`, ...) — the same pattern the 9.5.3 fixture already uses.

**Pointing CI at a new image** is a separate, deliberate step: publishing
does not move CI to it. Replace the digest in `docker-compose.prebuilt.yml`'s
`image: ${SHOWMESH_FPP_TEST_IMAGE:-...}` default with the
`ghcr.io/showmeshsystems/showmesh-fpp-test@sha256:...` reference the publish
job's summary prints. Until a v10 fixture is deliberately pointed at, CI's
default fixture and `docker-compose.prebuilt.yml`'s default digest both stay
9.5.3.

**Using a published v10 fixture locally without touching CI's default**:
`docker-compose.prebuilt.yml`'s `image:` already reads
`SHOWMESH_FPP_TEST_IMAGE` before falling back to the pinned 9.5.3 digest, so
point a local run at a published v10 digest by exporting it rather than
editing the file:

```
export SHOWMESH_FPP_TEST_IMAGE=ghcr.io/showmeshsystems/showmesh-fpp-test@sha256:<v10 digest>
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml pull fpp-master
docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml up -d fpp-master
```

**Fork pull requests cannot run the FPP integration workflow.** A pull
request from a fork gets a `GITHUB_TOKEN` scoped to the fork, which cannot
read a private package owned by `ShowMeshSystems`, so the pull step fails
with an explicit message rather than falling back to a source build.

**Licensing.** Upstream FPP's `LICENSE` describes a mix: LGPL v2.1 for the
core, GPL v2 for channel outputs and general code, and CC-BY-ND for
`src/non-gpl/**`. The fixture is unmodified upstream FPP built at the
recorded commit, carries that source tree under `/opt/fpp` in the image,
and its OCI labels record the source repository and revision.

## Bringing the bench up

```
cd bench/fpp-multisync
cp .env.example .env   # adjust ports/version if needed
docker compose up -d --build fpp-master
```

This starts only `fpp-master`; `probe` is normally run per-capture (see
"Running a capture"), and `fpp-remote` is off by default (see below). Once
up, the web UI is at `http://localhost:8090/` (or whatever
`FPP_MASTER_HTTP_PORT` you set).

## Configuring fpp-master as a MultiSync master with a sequence to play

This is manual, done through the web UI, and it is the step most likely to
stall someone — in particular one setting below defaults to **off** and
silently suppresses every MultiSync packet with no error anywhere if you
miss it.

1. **Open the web UI** at `http://localhost:8090/`. A fresh container may
   show FPP's first-run setup wizard (timezone, hostname, etc.); click
   through it, or Skip.

2. **Enable MultiSync sending.** Go to **Settings** and find the setting
   named **"Send MultiSync Packets"** (internal name `MultiSyncEnabled`).
   **This defaults to off.** With it off, `fppd` runs and plays sequences
   completely normally, but the multisync-sending code path
   (`MultiSync::isMultiSyncEnabled()`) gates every `SendSeqOpenPacket`,
   `SendSeqSyncStartPacket`, and the periodic `SendSeqSyncPacket` call — with
   it off, `fppd` never constructs or sends a single MultiSync packet, and
   nothing in its logs says so at default log verbosity. This was the one
   thing that made this bench appear not to work at all while it was being
   built (see "How this bench was validated"). Turn it on, save, and let FPP
   restart `fppd` when it prompts you to (this setting requires a restart to
   take effect).

   Leaving the transport settings (Multicast/Broadcast/Unicast) at their
   defaults is fine on the **9.5.3** target: with nothing else configured,
   FPP 9.x defaults to multicast, matching RES-002's documented 9.x default.

   **This is not true on the 10.0 target.** A fresh FPP 10 install ships
   with `MultiSyncUnicast` defaulting to on and `MultiSyncMulticast`
   carrying no default at all (upstream `www/settings.json` at the `10.0`
   tag, recorded in RES-002), and FPP 10's automatic unicast targeting only
   ever selects other FPP instances in remote mode (`supportsUnicast` in
   `src/MultiSync.cpp`) — never this bench's `probe`. Left at its shipped
   defaults, an FPP 10 `fpp-master` sends nothing this probe will ever
   receive, on any transport, and neither side logs an error. **This is not
   a bench defect**; it is the exact configuration RES-002 exists to
   document, and it is what the
   [FPP 10 default-transport capture](captures/sm209/FPP10-DEFAULT-TRANSPORT.md)
   shows the probe correctly reporting as expected FPP 10 behavior rather
   than as a fault. To make a 10.0 `fpp-master` actually reach `probe`, add
   the probe's address to `MultiSyncRemotes` (or `MultiSyncExtraRemotes`)
   under **Settings → MultiSync** — this applies live on FPP 10 with no
   `fppd` restart — or enable `MultiSyncBroadcast`/`MultiSyncMulticast`
   explicitly.

3. **Get a sequence onto the master.** Under **Content Setup → Sequences**,
   upload a `.fseq` file — one of your own from xLights is the realistic
   choice, since RES-002 item 1's "under load" condition specifically wants
   a playlist entry that resembles the reference show's actual pixel and
   matrix load. This bench does not ship one; there is no pixel hardware
   here to exercise regardless, so "under load" cannot be answered by this
   bench no matter what you upload (see "Purpose, and its limits").

4. **Build a playlist and play it.** Under **Content Setup → Playlists**,
   create a playlist, add the sequence as an entry, save, then start it from
   the **Status/Control** page (or the playlist page's own Play button).
   FPP's pause, seek, and stop controls on that page are what RES-002 open
   item 2 needs exercised — trigger them while a capture is running and note
   the wall-clock time, per the capture procedure.

### Quick smoke test without any real content

FPP exposes a **developer-only** endpoint that starts a single sequence file
directly, without a playlist, and is explicitly documented upstream as "only
intended for testing":

```
GET /api/sequence/{SequenceName}/start/{startSecond}
```

This is genuinely useful for a first smoke test that the bench itself is
wired up correctly, before you have real content loaded, or before you want
to click through the UI at all. It still requires step 2 above
(`MultiSyncEnabled` on) and a `.fseq` file already present under
`/home/fpp/media/sequences` inside the container (`docker cp` one in, or
upload via Content Setup). For example, once a file named `test.fseq` is in
place:

```
curl "http://localhost:8090/api/sequence/test.fseq/start/0"
```

This is how this bench's own build was validated end-to-end (see below); it
is not a substitute for the playlist-based path above for anything beyond a
connectivity check, since RES-002's items assume real playlist behavior.

## Running a capture

Once `fpp-master` is configured and playing, run the probe against it. Each
of RES-002's open items still needs its own invocation with its own
`-out` path and its own manual action on `fpp-master`'s web UI while it
runs — this bench does not change that, it only changes how you invoke the
probe binary. Full detail (exactly what to click, per item, and what to do
with the result) is in
[docs/bench/RES-002-capture-procedure.md](../../docs/bench/RES-002-capture-procedure.md);
this table only translates that procedure's `./bin/showmesh-multisync-probe`
invocations into `docker compose run` ones:

| RES-002 item | This bench | Example |
|---|---|---|
| 1: cadence/jitter | Yes, minus "under load" (no pixel hardware here) | `docker compose run --rm probe -duration 3m -out /captures/res-002-item1-cadence.jsonl` |
| 2: lifecycle, pause, seek | Yes | `docker compose run --rm probe -duration 3m -out /captures/res-002-item2-lifecycle.jsonl` |
| 3: STOP/BLANK at 3 endings | Yes (playlist end; manual stop; `docker compose exec fpp-master pkill fppd` for the shutdown case) | `docker compose run --rm probe -duration 2m -out /captures/res-002-item3-<ending>.jsonl` |
| 4: clock drift | **No** — see "Purpose, and its limits" | — |
| 5: transport/version half | Yes | `docker compose run --rm probe -duration 3m -out /captures/res-002-item5-transport.jsonl` |
| 5: IGMP half | **No** — see "Purpose, and its limits" | — |

`docker compose run --rm probe` attaches to the `bench` network like `up`
would, runs to completion (or until you Ctrl-C), and is removed afterward
(`--rm`) since each invocation is a one-shot capture, not a long-lived
service. Pass `-respond-discover` (as its own flag after the others) only
for the item-5 discover-ping check, per the capture procedure's warning
about what that flag transmits.

## fpp-remote (optional second FPP)

```
docker compose --profile remote up -d fpp-remote
```

A second FPP instance, same pinned version as `fpp-master` (same build
inputs, so Docker's layer cache makes the second build effectively free),
intended to be configured as a MultiSync remote so you can compare what a
real FPP remote does against what the ShowMesh timeline does with the same
traffic — a stronger check than reading this bench's own probe output alone.
Configuring it as a remote is done through its own web UI
(`http://localhost:8091/` by default) the same way any FPP remote is
configured; this bench does not automate that step, and the exact menu
wording differs somewhat between the 9.5 and 10.0 UI, which this README has
not separately verified for the remote-mode path.

## Where output lands

Captures land in `${CAPTURES_DIR:-./captures}` on the host (a bind mount,
not a named volume, specifically so they survive `docker compose down` and
are immediately reachable as plain files — see
`docs/bench/RES-002-capture-procedure.md` for the JSONL schema and the
summary report each run prints).

## Tearing down

```
docker compose down            # stops and removes containers + the bench network
docker compose down -v         # also removes fpp-master-media / fpp-remote-media volumes
```

Captures under `captures/` are host files and are never touched by either
command.

## How this bench was validated when built

Built and validated 2026-08-10 against Mode A only, on macOS Docker Desktop
(macvlan is not meaningfully testable there — see "Two network modes"). This
section records what was actually run, so nobody has to re-derive it; it is
evidence that this bench's own plumbing works, **not** RES-002 verification
— see `docs/bench/RES-002-capture-procedure.md` for what turns a capture
into that.

- **FPP image pulls/builds and `fppd` comes up:** confirmed against both the
  published `falconchristmas/fpp:latest` image and a from-source build of
  this bench's own `Docker/Dockerfile` against the `v9.5` branch
  (`FPP 9.5.3-14-g422ed1ae8`). The from-source build took roughly two
  minutes end-to-end on the build machine, most of it FPP's own prebuilt
  package repo, not a full local recompile.
- **Web UI / REST API reachable:** confirmed (`GET /api/fppd/status`
  answered correctly from a second container on the same bridge network).
- **`fppd` emits MultiSync traffic with zero pixel output hardware
  configured — the load-bearing question this whole bench depends on:**
  confirmed, decisively. A hand-built minimal FSEQ v2 file (no real show
  content, no channel outputs configured at all) started via the developer
  `/api/sequence/{name}/start/{startSecond}` endpoint produced
  `SendSeqOpenPacket` → `SendSeqSyncStartPacket` → periodic
  `SendSeqSyncPacket` calls at exactly RES-002's documented cadence (every 4
  frames for the first 32, then every 10), independently confirmed both via
  `fppd`'s own debug-level log output and via an actual received, decoded
  packet capture (below). Sync genuinely derives from playlist/sequence
  position, not from output hardware.
- **The probe, in a second container, receives and decodes those packets:**
  confirmed. `showmesh-multisync-probe`, built from `probe.Dockerfile` and
  run in its own container on the same `bench` network, captured and
  correctly decoded a full OPEN → START → SYNC (repeating) sequence from
  `fpp-master`, both over multicast and over broadcast, with 0 malformed or
  undecodable packets.

Two things worth recording precisely because they cost real time to
diagnose and will otherwise cost the same time again:

- **`MultiSyncEnabled` defaults to off** and silently suppresses all
  MultiSync sending with no logged error at default verbosity — see step 2
  of "Configuring fpp-master" above. Most of the debugging time during this
  bench's construction went into this single setting.
- **Multicast delivery between sibling containers on this bridge network was
  observed to be unreliable** on this macOS Docker Desktop host: a
  hand-crafted multicast test packet from a third container, and `fppd`'s
  own real multicast sync traffic, both failed to reach a freshly-started
  probe in more than one attempt, despite the probe correctly joining the
  multicast group. Enabling FPP's broadcast transport alongside multicast
  (`MultiSyncBroadcast`, in addition to the default `MultiSyncMulticast`)
  produced a fully reliable capture. If a capture on this bench comes back
  empty on Mode A, try enabling broadcast on `fpp-master` before concluding
  anything about `fppd` itself — this looks like a property of Docker
  Desktop's virtualized bridge network, not of FPP or of this bench's
  wiring, but it was not tracked down further than that. It was not
  observed against Mode B, which was not testable on this machine.

## FPP 10 verification

Confirmed while adding FPP 10 as a first-class bench target: `FPP_GIT_REF=10.0`
source-builds and runs a real `fppd` through this same `docker-compose.yml`,
no separate compose file or code path needed.

- **Build:** `docker compose -f docker-compose.yml build fpp-master` with
  `FPP_GIT_REF=10.0` completed successfully, producing an image from
  upstream commit `370e62ed7e8c8318da6ee5b01312b8b75082d952`.
- **One caveat hit during the build, worth recording so it does not cost
  the same time again:** the first attempt failed with
  `fatal: No url found for submodule path 'external/rpi_ws281x' in
  .gitmodules`. This is not an upstream FPP 10 defect — `external/rpi_ws281x`
  is a real submodule on the 9.5 line but was removed from `.gitmodules` by
  the 10.0 tag, and Docker's buildx git-source cache had a stale shared
  mirror left over from an earlier 9.5.3 build on this machine that still
  carried that submodule's registration. `docker buildx prune` (clearing the
  stale `source.git.checkout` cache) resolved it; a machine that has never
  built this repo's 9.5.3 target would not hit this at all.
- **`fppd` comes up and answers real REST endpoints:** confirmed via
  `curl http://localhost:<port>/api/system/info`,
  `/api/fppd/status` (idle and with a playlist started),
  `/api/fppd/ports`, and `/api/fppd/multiSyncSystems`. Every response
  reports `majorVersion: 10`, `minorVersion: 0`, `version: "10.0"`, and
  `system/info`'s `LocalGitVersion: "370e62ed7"` — a real FPP 10.0 build at
  the pinned commit, not a mislabeled 9.5 image. Raw response bodies are
  captured at
  `internal/coordinator/collector/fpp/testdata/v10-bench/` (see that
  directory's own README) for later steps of the FPP 10 migration that need
  a v10 REST capture to work from.
- **The 9.5.3 default still builds and starts unchanged**, confirmed the
  same session: `docker compose -f docker-compose.yml build fpp-master`
  with no `FPP_GIT_REF` override (the `9.5.3` default) and
  `docker compose -f docker-compose.yml up -d fpp-master` came up and
  answered `/api/fppd/status` exactly as before.
- **Not verified in this pass:** running a 9.5.3 and a 10.0 bench
  simultaneously (see "Running a 9.5.3 bench and a 10.0 bench side by side"
  above for the mechanism — separate `.env`/project name/ports — which was
  not itself exercised); the MultiSync packet-cadence and lifecycle
  evidence "How this bench was validated" recorded for 9.5.3 above was not
  independently re-run against 10.0; and Mode B (macvlan) was not tested
  for either version, consistent with the rest of this README.
