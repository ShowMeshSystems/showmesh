# Installing the ShowMesh node agent

This is the install path for a media node (a Debian host attached to real
audio/video output) running the ShowMesh agent natively. It is separate
from `deploy/`, which is the coordinator's Docker Compose appliance bundle.
Node agents are never part of that bundle, per its own README.

## What the agent does, and does not, do here

The binary this flow installs is the **cgo build**
(`make build-agent-native`), the only build that actually plays audio: it
links go-gst (GStreamer) and libltc for in-pipeline LTC generation
(ADR-042). The plain `make build` agent is `CGO_ENABLED=0` and has no
audio engine at all: do not ship that one to a media node.

Even this build shells out to real GStreamer command-line tools
(`gst-launch-1.0`, `gst-discoverer-1.0`) rather than only linking the
library, so a correct install needs both the GStreamer runtime packages
and the C build toolchain (build-time only).

**NDI output is not part of this install.** The `ndisink` GStreamer element
comes from the gst-plugins-rs NDI plugin, which this repository does not
build, vendor, or ship: ADR-010 forbids redistributing the NDI runtime,
and building that plugin is a separately owned recipe (not this issue's
scope). `preflight.sh` reports whether `ndisink` currently resolves as
informational output only; a render node that needs NDI must build that
plugin by hand and point `GST_PLUGIN_PATH` at it in `agent.env` (see the
template's comment), then re-run preflight to confirm.

## Platform floor

**Debian 13 (trixie) or newer.** Measured, not assumed: the agent's cgo
build fails on Debian 12 because its GLib 2.74 is missing symbols
go-gst/go-glib need (2.80+), producing about twenty undefined C symbols
rather than a clear error. `preflight.sh` and `install.sh` both check
`/etc/os-release` and refuse plainly on anything older.

## What you need before you start

1. A Debian 13+ host with real audio/video hardware (or, for testing, a
   container, see this repo's `bench/node-install/`).
2. Root access on that host.
3. A running ShowMesh coordinator and broker somewhere reachable from this
   host (see the top-level `deploy/README.md`).
4. A node id you have chosen for this host (lowercase letters, digits,
   internal hyphens only; `coordinator`, `fpp`, and `healthcheck` are
   reserved).

## Steps

### 1. Get a broker credential for this node

From the machine running the coordinator's `deploy/` bundle (or wherever
`deploy/mosquitto/add-agent-credential.sh` can reach the broker's Mosquitto
config):

```sh
./mosquitto/add-agent-credential.sh <node-id>
```

This prints a password once. `<node-id>` here must be the exact value you
will set as `SHOWMESH_NODE_ID` below: the broker's ACL trusts the
username to equal the node's own id. Keep the password; you'll paste it
into this node's env file.

### 2. Build the native agent for this host

On the node itself (recommended, it guarantees the binary matches the
host's own libraries), or on a build host running the identical Debian
release:

```sh
apt-get update && apt-get install -y \
  build-essential pkg-config \
  libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev libltc-dev \
  # Go: install by hand from https://go.dev/dl/ and put it on PATH.
  # Debian 13's golang-go is older than the version go.mod requires.

git clone https://github.com/ShowMeshSystems/showmesh.git
cd showmesh
make build-agent-native
```

This produces `bin/showmesh-agent-native`. `make package-node-agent`
(see the repository root `Makefile`) instead builds a distributable
tarball containing this binary plus everything in this directory, see
that target's own comment for the reproducibility discipline it follows.
The tarball is named for the platform it was built on
(`..._linux_amd64.tar.gz`, `..._linux_arm64.tar.gz`): this is a cgo
build linking host C libraries, so it can only ever target its own
platform. To package for a Raspberry Pi class node, run that target on
an arm64 Debian 13 host or in an arm64 `debian:13` container.

### 3. Install the runtime packages

```sh
apt-get install -y \
  alsa-utils gstreamer1.0-tools \
  gstreamer1.0-plugins-base gstreamer1.0-plugins-good gstreamer1.0-plugins-bad \
  gstreamer1.0-plugins-base-apps gstreamer1.0-alsa libltc11
```

(`alsasink` ships in `gstreamer1.0-alsa`, not in
`gstreamer1.0-plugins-base-apps`, a common point of confusion; see
`docs/build/TRACK-C-audio-node.md`.)

### 4. Run the installer

```sh
sudo ./install.sh /path/to/showmesh-agent-native
```

This is idempotent. On a fresh host it:

- runs `preflight.sh --runtime-only` and refuses to continue if anything
  is missing (it names the exact apt package to install). It checks only
  what the agent needs to RUN, because it installs an already-built
  binary; run `./preflight.sh` with no arguments to also check the
  build-time toolchain on a host that will build the agent itself;
- creates the `showmesh` system user/group;
- creates `/etc/showmesh` (0755) and `/etc/showmesh/agent.env` (0600,
  root:root) from `agent.env.example`, **only if that file does not
  already exist**;
- creates `/var/lib/showmesh` (the state directory: `.render-state/`,
  `audio-sessions/`, and the asset payload files all live under here, at
  `/var/lib/showmesh/assets`), owned by `showmesh`;
- installs the binary to `/usr/local/bin/showmesh-agent-native`;
- installs and enables `showmesh-agent.service`.

Run again on an upgrade with a newer binary: it replaces the binary and
unit and restarts the service, and **never touches `/etc/showmesh/agent.env`
or anything already written under `/var/lib/showmesh`**. A node that
reboots or is upgraded with no coordinator reachable keeps its last
applied render assignments and audio sessions and resumes from them.

### 5. Configure and start

Edit `/etc/showmesh/agent.env`: set at minimum `SHOWMESH_NODE_ID` (the id
you provisioned in step 1), `SHOWMESH_MQTT_BROKER`,
`SHOWMESH_MQTT_USERNAME`, `SHOWMESH_MQTT_PASSWORD`. See the file itself
for every other variable this agent reads
(`internal/agent/config/config.go` is the source of truth if this ever
drifts).

```sh
sudo systemctl start showmesh-agent
sudo systemctl status showmesh-agent
sudo journalctl -u showmesh-agent -f
```

## Verifying the install worked

```sh
./preflight.sh                 # re-run any time; safe, read-only
systemctl status showmesh-agent
journalctl -u showmesh-agent -n 50
```

A healthy agent logs its hello publish and does not crash-loop. If it logs
`mqtt broker rejected connection: not authorized`, the credential in
`agent.env` does not match what `add-agent-credential.sh` provisioned.

## What this install flow does NOT verify

- Real audio output through a physical interface. `preflight.sh` checks
  that the ALSA tooling and elements exist, not that sound actually comes
  out of a real DAC. That is per-hardware commissioning, out of scope
  here.
- Real NDI output. `ndisink` element resolution is reported as
  informational only, and this repository does not build or verify that
  element, see "What the agent does" above.
- That the systemd unit actually boots correctly on real hardware. It has
  been checked for syntactic validity (`systemd-analyze verify`) and
  built/installed/preflighted inside a container, but a plain container
  does not run systemd as PID 1, so no container run in this repository's
  own verification claims the service actually started under systemd. See
  `bench/node-install/README.md` for exactly what that bench does and does
  not prove.
