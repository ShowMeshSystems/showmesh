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
second run and diffing checksums after); `preflight.sh`'s build-host mode
(no arguments) runs every check it claims to and reports the informational
NDI line; `preflight.sh --runtime-only`, the mode `install.sh` actually
takes on every real install, is separately exercised as an unprivileged
user with a normal login PATH (`/usr/local/bin:/usr/bin:/bin`, no
`/usr/sbin`) and asserted to pass when the runtime library is present and
to fail, naming it, when it genuinely is not; and the installed
systemd unit is syntactically valid, every directive name in it included
(`systemd-analyze verify --recursive-errors=one`, which unlike a bare
`systemd-analyze verify` exits non-zero on an unknown directive rather
than only warning; the bench re-runs that same check against a
deliberately typo'd copy of the unit and fails if it is accepted); the unit's restart policy is `Restart=on-failure` (not `always`, which would relaunch the agent after a clean `systemctl stop`); and the upgrade path (install.sh re-run over an existing binary) enforces the same `SHOWMESH_NODE_ID` check the fresh-install path does, refusing to call `systemctl restart` when `agent.env` has never been edited.

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

The image picks its Go toolchain from BuildKit's `TARGETARCH`, so the
same `docker build` is correct on an amd64 host and an arm64 host without
extra flags. Pass `--build-arg GOARCH=amd64` (or `arm64`) only to
cross-build deliberately.

## Layout

```
bench/node-install/
  Dockerfile               Debian 13 + the build/runtime package set +
                           pinned Go 1.25.0 (matched to TARGETARCH) +
                           systemd-analyze
  run_install_proof.sh     Builds the agent, runs install.sh twice
                           (idempotency + sentinel check), preflight.sh in
                           both modes, systemd-analyze verify, and negative
                           checks proving that verification can fail.
                           Step 6 invokes ./preflight.sh with NO
                           arguments, the BUILD-host branch. Step 6b
                           invokes ./preflight.sh --runtime-only, the
                           branch install.sh itself always takes on a real
                           node, as an unprivileged user with a normal
                           login PATH, once with the runtime library
                           present (must pass) and once with it genuinely
                           absent (must fail, naming it).
```
