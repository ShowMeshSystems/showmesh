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
| `showmesh-installer_<version>.tar.gz` | `showmesh-install`, `lib/`, the avahi service template, the coordinator Compose bundle under `coordinator/`, `install-ptp-audio.sh`, `verify-ptp-audio.sh`, `ptp-audio/` and `ndi-plugin/` under `node/`, `showmeshctl` for linux amd64 and arm64 under `bin/`, and `BUILD-INFO`. |
| `showmesh-installer_<version>_SHA256SUMS` | Checksums of the two files above, for the local self-check only. |

The release workflow publishes the bundle and the bootstrap with one file named
`SHA256SUMS` that lists both of them and both node agent tarballs. The bootstrap
checks the bundle against it and the installer checks the node agent tarball
against it.

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

Before an upgrade changes anything, the installer asks the coordinator whether
a night session is running and refuses if one is (or if it cannot be asked),
unless `--force` is given. It then stops the coordinator only while it copies its
database, `.env` and broker files to `/var/backups/showmesh/<time>-<old
version>/` with the restore steps in `RESTORE.txt`, and keeps the newest five
backups. If the copy fails, nothing is upgraded. A coordinator upgrade ends by
printing the one-line upgrade command for each node that is not yet on the new
version. An enrolled node's upgrade asks the same night question when it can,
and only warns when it cannot.

## Unattended runs

`--yes`, or no terminal, means the installer never asks. Anything it would
have asked for must come from an option, and a missing one stops the run with
the option to add. `--help` lists every option. Beyond ADR-055's list, these
exist so every question has an unattended answer: `--address`,
`--admin-name`, `--admin-password-file`, `--node-role`, `--node-id`,
`--ptp-interface`, `--ptp-domain`, `--ptp-role`, `--audio-card` and
`--agent-package` (a node agent tarball on disk, for a build that has no
release).

## Firewall

`lib/firewall.sh` runs after the role finishes and detects an active ufw, a
running firewalld, or a raw nftables ruleset whose input chain hooks input
with a drop policy (Debian 13 ships none of these by default).

| Detected | What the installer does |
| --- | --- |
| ufw active | Opens exactly the ports the installed role needs with `ufw allow`, each carrying a comment naming ShowMesh. Re-running adds nothing twice; ufw already skips a rule it already has. |
| firewalld running | Defines a permanent `showmesh` service listing those ports, adds it to every active zone that has an interface, and opens the same ports in the running zone. It never reloads firewalld, so runtime-only rules survive. |
| Raw nftables, input chain policy drop | Prints the exact `accept` rules to add and the file nftables loads them from (`/etc/nftables.conf` on Debian). An accept rule in a separate table cannot override a drop in the owner's own chain, so nothing is changed. |
| No firewall active | Prints one line and changes nothing. |

The ports: every node role needs tcp 80 (or `SHOWMESH_FPPCONNECT_LISTEN_ADDR`'s
port), udp 32320 (or `SHOWMESH_MULTISYNC_LISTEN_ADDR`'s port, FPP MultiSync),
udp 319 and 320 (PTP), udp 5004 and 9875 (AES67 RTP and SAP), and udp 5353
(mDNS). A render node also needs tcp and udp 5959-5999 (NDI). A coordinator
needs tcp 8080 (or `SHOWMESH_HTTP_PORT`, the API), tcp 8081 (or
`SHOWMESH_UI_PORT`, the operator UI), and, with the built-in broker, tcp 1883
(or `MOSQUITTO_PORT`). Docker publishes the coordinator's ports itself, which
bypasses a ufw or nftables host's input chain, but a firewalld zone can still
block a published port, so the coordinator's ports are opened on every
backend regardless.

`--firewall` opts in to installing a ShowMesh firewall on a host with none
active: an nftables table named `showmesh_host` in
`/etc/nftables.d/showmesh-host.nft`, included from `/etc/nftables.conf`
(backed up once before the first edit), with a drop policy on the input
chain, established/related and loopback traffic, Docker's bridge interfaces,
ICMP, IGMP, mDNS and SSH (every port `sshd -T` reports, or 22) allowed, and the role's own ports on top. It never
runs `nft flush ruleset`, so Docker's own tables survive. The table is checked with `nft -c` before `nftables.conf` is touched, and when `nftables.conf` already declares tables that are not loaded, the installer warns and does not enable the nftables service over them. `--no-firewall`
skips firewall setup entirely.

## NDI

The installer never downloads the NDI runtime. `--ndi <file>` accepts the SDK
download (`Install_NDI_SDK_v6_Linux.tar.gz`), the installer script inside it,
or an already unpacked SDK directory. It runs the SDK's own installer with the
terminal attached so the operator reads and answers the licence, copies
`libndi.so.6` for this architecture into `/usr/local/lib`, runs `ldconfig`,
and requires `gst-inspect-1.0 ndisink` to resolve. When the node agent package
carries no `gstreamer/libgstndi.so`, the plugin is built with
`node/ndi-plugin/build-ndi-plugin.sh <output-dir>` at that point, not before.

## Output and the install log

`lib/common.sh` draws every install: the static banner, `▸` step headers,
`✔` and `⚠` check lines, and a spinner beside a step that takes time
(ADR-055 decision 9, amended 2026-09-28). Output that is not a terminal,
`NO_COLOR` and `TERM=dumb` each get the same lines as plain text, so a captured
install reads as it always did and the benches assert on that text. Refusals
and errors are never coloured.

`log_open` appends a header to `/var/log/showmesh-install.log` (mode 0600,
root) with the date, version, role and arguments, hiding the values of `--code`
and `--broker-password`. `SHOWMESH_INSTALL_LOG` moves that file, which is how a
test reads one run's log without root's `/var/log`.

`run_step LABEL FILE CMD...` is how a step runs a command: it captures the
output to `FILE`, appends it to the log and animates the spinner. `CMD` runs in
the installer's own shell and in the foreground with stdin from `/dev/null`, in
both layouts, so a function can set variables and Ctrl-C stops `CMD` itself.
`step_warnings FILE` then shows any `WARNING:` line the step printed, which a
successful step otherwise leaves only in the log. `log_open` keeps no log when
the log path is a symbolic link.
`fail_with FILE FACT FIX` stops with the fact, then the last lines that command
printed, then the fix and the log path. Three commands print a secret of their
own and so are recorded in the log as withheld rather than captured:
`generate-credentials.sh`, the coordinator's `bootstrap` and `create-admin`, and
`showmeshctl node enroll`. `http_request` logs the method, URL and status of a
request that did not succeed, never the answer, because a redeem answer carries
the node's broker password and API token.

## Party mode

`--party` adds the animated opening and success screens of `lib/party.sh`. It
never decorates a question, a refusal or an error, and without `--party`
nothing in that file runs. The hidden modes need `--party` as well.

## Testing

`shellcheck -x deploy/install/showmesh-install deploy/install/get-showmesh.sh deploy/install/lib/*.sh`

`bench/node-install/run_installer_bench.sh` proves the render node and audio
node roles unattended against a fake enrollment server, including a wrong code,
an expired code and an in-place upgrade. `bench/node-install/run_coordinator_bench.sh`
then runs the coordinator role for real inside a privileged Debian 13
container with its own dockerd. `bench/node-install/run_firewall_bench.sh` proves
`lib/firewall.sh` against ufw, firewalld, a raw nftables ruleset with a drop
policy, no firewall, `--firewall` and `--no-firewall`. See
`bench/node-install/README.md`.
