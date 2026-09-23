# showmesh-install

The one-command installer from
[ADR-055](../../docs/decisions/ADR-055-one-command-install-and-node-enrollment.md).
This file is for contributors. Operator documentation lives in
`ShowMeshSystems/showmesh-docs`.

## What the release ships

`make package-installer INSTALLER_VERSION=<version>` writes to `dist/installer/`:

| File | Contents |
| --- | --- |
| `get-showmesh.sh` | The bootstrap, with the version written in. |
| `showmesh-installer_<version>.tar.gz` | `showmesh-install`, `lib/`, the avahi service template, the coordinator Compose bundle under `coordinator/`, `install-ptp-audio.sh`, `ptp-audio/` and `ndi-plugin/` under `node/`, `showmeshctl` for linux amd64 and arm64 under `bin/`, and `BUILD-INFO`. |
| `showmesh-installer_<version>_SHA256SUMS` | Checksums of the two files above. |

The release must publish one file named `SHA256SUMS` that lists the installer
bundle and both node agent tarballs. The bootstrap checks the bundle against it
and the installer checks the node agent tarball against it.

## How a run flows

```sh
curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v<version>/get-showmesh.sh | sudo bash -s -- [options]
```

1. `get-showmesh.sh` downloads `SHA256SUMS` and the bundle for its version,
   refuses a checksum mismatch, unpacks the bundle to
   `/opt/showmesh/installer/<version>/`, links `/usr/local/bin/showmesh-install`
   to it, and runs it with every argument passed through. Its stdin is the
   curl pipe, so it hands the installer `/dev/tty` when there is one and
   `/dev/null` otherwise.
2. `showmesh-install` refuses anything but Debian 13 or newer on amd64 or
   arm64, then asks for the role unless `--role` was given or a previous run
   recorded one in `/etc/showmesh/installer.env`.

| Role | What it does | Finishes when |
| --- | --- | --- |
| `coordinator` | Installs `docker.io`, `docker-cli` and `docker-compose` from Debian (trixie ships Compose 2.26), places the bundle in `/opt/showmesh/coordinator`, runs `generate-credentials.sh` for the built-in broker or records an external one, writes `.env`, starts the published images, creates the first administrator with the coordinator's `bootstrap` subcommand (or `create-admin` when bootstrap was already claimed), issues it a token, installs `showmeshctl` with a wrapper that reads `/etc/showmesh/showmeshctl.env`, and writes `/etc/avahi/services/showmesh.service`. | `/healthz` answers, the administrator token reads `/api/v1/nodes`, and the broker accepts the coordinator's login. |
| `render`, `audio` | Installs the runtime packages, downloads and checks the node agent tarball, runs `preflight.sh --runtime-only`, redeems the enrollment code, writes the returned values into `/etc/showmesh/agent.env` (every other line kept), and runs the package's `install.sh`. A render node installs the packaged NDI plugin and offers the NDI runtime step. An audio node offers `install-ptp-audio.sh`. | `GET /api/v1/nodes/<nodeId>` with the node's token shows `controlPlane.state` `online` with a `startedAt` other than the one seen before the agent started. |
| `coordinator-node` | The coordinator role, then `showmeshctl node enroll <node-id>` against itself, then the node role with that code. | Both of the above. |

A second run upgrades in place. It keeps `.env`, broker logins, the
administrator token, `agent.env` and node state, and it skips enrollment
unless `--reenroll` is given.

## Unattended runs

`--yes`, or no terminal, means the installer never asks. Anything it would
have asked for must come from an option, and a missing one stops the run with
the option to add. `--help` lists every option. Beyond ADR-055's list, these
exist so every question has an unattended answer: `--address`,
`--admin-name`, `--admin-password-file`, `--node-role`, `--node-id`,
`--ptp-interface`, `--ptp-domain`, `--ptp-role`, `--audio-card` and
`--agent-package` (a node agent tarball on disk, for a build that has no
release).

## NDI

The installer never downloads the NDI runtime. `--ndi <file>` accepts the SDK
download (`Install_NDI_SDK_v6_Linux.tar.gz`), the installer script inside it,
or an already unpacked SDK directory. It runs the SDK's own installer with the
terminal attached so the operator reads and answers the licence, copies
`libndi.so.6` for this architecture into `/usr/local/lib`, runs `ldconfig`,
and requires `gst-inspect-1.0 ndisink` to resolve. When the node agent package
carries no `gstreamer/libgstndi.so`, the plugin is built with
`node/ndi-plugin/build-ndi-plugin.sh <output-dir>` at that point, not before.

## Party mode

`--party` adds the banner, colour and animation of `lib/party.sh` to the
opening and success screens only. It never decorates a question, a refusal or
an error, and without `--party` nothing in that file runs. The hidden modes
need `--party` as well.

## Testing

`shellcheck -x deploy/install/showmesh-install deploy/install/get-showmesh.sh deploy/install/lib/*.sh`

`bench/node-install/run_installer_bench.sh` proves the render node and audio
node roles unattended against a fake enrollment server, including a wrong code,
an expired code and an in-place upgrade. `bench/node-install/run_coordinator_bench.sh`
then runs the coordinator role for real inside a privileged Debian 13
container with its own dockerd. See `bench/node-install/README.md`.
