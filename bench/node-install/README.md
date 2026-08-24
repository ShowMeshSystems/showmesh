# Node install bench (SM-43)

Bench scaffolding proving `deploy/node`'s install flow on a clean Debian 13
container. Not part of the ShowMesh product, not imported by `internal/` or
`pkg/`, exactly like `bench/audio-node` and `bench/fpp-multisync`. Not part
of `make check` or CI.

## What this proves, and what it cannot

**Proves**: on a freshly created Debian 13 container with only the packages
`deploy/node/README.md` names, the native (cgo) agent builds; `install.sh`
runs to completion and creates the expected user, files, and permissions;
running `install.sh` a second time is idempotent and does not touch
`/etc/showmesh/agent.env` or anything already written under
`/var/lib/showmesh` (proved by writing a sentinel into both before the
second run and diffing checksums after); `preflight.sh` runs every check it
claims to and reports the informational NDI line; and the installed
systemd unit is syntactically valid (`systemd-analyze verify`).

**Cannot prove, and does not claim to**: that the service actually starts
and stays running under systemd. A plain `docker run` container does not
run systemd as PID 1, there is no service manager to hand the unit to,
so nothing in this bench claims `systemctl start showmesh-agent` was
observed to succeed. It also proves nothing about real audio hardware,
real NDI output, or a real broker; every run here is offline, against no
network beyond what building the toolchain needs.

## Running it

```sh
docker build -t sm43-node-install:dev bench/node-install
docker run --rm --name sm43-node-install-run \
  -v "$(pwd)":/repo \
  sm43-node-install:dev \
  -c "bash /repo/bench/node-install/run_install_proof.sh"
```

The container mounts the repository read-write at `/repo` so the script
can `go build` against it; it installs into the container's own
filesystem, not the host's.

## Layout

```
bench/node-install/
  Dockerfile               Debian 13 + the build/runtime package set +
                           pinned Go 1.25.0 + systemd-analyze
  run_install_proof.sh     Builds the agent, runs install.sh twice
                           (idempotency + sentinel check), preflight.sh,
                           and systemd-analyze verify
```
