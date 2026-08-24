# FPP 10.0 bench captures

Raw REST response bodies captured directly from a real `fppd` built and run
from `bench/fpp-multisync` at `FPP_GIT_REF=10.0` (upstream commit
`370e62ed7e8c8318da6ee5b01312b8b75082d952`, confirmed against
`refs/tags/10.0^{}`), taken while building FPP 10 bench support so the
migration's later steps have a real v10 capture to start from.

Unlike `../live_*.json` (captured from physical fleet hardware and requiring
identity substitution, see `../README.md`), these came from a disposable
bench container with no operator identity to protect: `HostName`,
`host_name`, `uuid`, and IP address here are whatever this bench container
happened to have, not fleet hardware, and are left verbatim.

## Files

- `system_info.json` — `GET /api/system/info`, idle.
- `fppd_status_idle.json` — `GET /api/fppd/status`, no playlist running.
- `fppd_status_playing.json` — `GET /api/fppd/status`, a single-pause-item
  playlist (`showmesh-test`, the same fixture playlist
  `scripts/test-integration-fpp.sh` seeds for the 9.5.3 bench) started via
  `GET /api/playlist/showmesh-test/start`.
- `fppd_ports.json` — `GET /api/fppd/ports`, no channel outputs configured
  (`[]`, same shape as an unconfigured 9.5.3 host).
- `multisync_systems.json` — `GET /api/fppd/multiSyncSystems`, one local
  entry (this bench container itself).

## What this directory is, and is not

This is a raw capture, not decoder test fixtures. No existing decoder,
struct, or test in this package was changed to consume these files — that
is a separate piece of work (comparing these captures against the 9.5.3
shapes in `../live_*.json` and `../*multisync*.json` and updating the
collector accordingly). Treat these as the starting evidence for that work,
not as already-wired-in fixtures.

## What these captures confirm

`majorVersion: 10`, `minorVersion: 0`, `"Version"/"version": "10.0"`, and
`LocalGitVersion: "370e62ed7"` in `system_info.json` confirm this really is
FPP 10.0 at the pinned commit, not a mislabeled 9.5 build.
