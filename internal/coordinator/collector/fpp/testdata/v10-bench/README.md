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

These started as a raw capture, not decoder test fixtures, taken while
adding FPP 10 bench support so a later step would have a real v10 capture
to start from. SM-210 (`v10_signals_test.go`, package `fpp`) is that later
step: `system_info.json`, `fppd_status_idle.json`, `fppd_status_playing.json`,
and `fppd_ports.json` are now driven through this package's UNCHANGED
`StatusSignals`, `PortSignals`, and `SystemInfoSignals` decoders and
asserted to decode with zero `collection_failed` signals — the regression
proof that this package's "observe the version, never branch on it, support
9.5.3 and 10.0 with the same decoders" strategy actually holds against a
real FPP 10.0 daemon, not just against 9.5.3.

`fppd_ports.json` is the one file this decode test cannot use to prove
anything about FPP 10's port-key omission (see below): it is `[]`, since
this bench container has no channel outputs configured. Nothing in this
directory was hand-edited to test that behavior — see
`../fpp10_ports_source_derived_not_captured.json` and
`v10_signals_test.go`'s `TestPortSignalsV10SourceDerivedKeyOmission` for
the separate, explicitly-labeled source-derived fixture that covers it
instead. `multisync_systems.json` is not yet wired into a test.

## What these captures confirm

`majorVersion: 10`, `minorVersion: 0`, `"Version"/"version": "10.0"`, and
`LocalGitVersion: "370e62ed7"` in `system_info.json` confirm this really is
FPP 10.0 at the pinned commit, not a mislabeled 9.5 build.
