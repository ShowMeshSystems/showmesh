# Releasing ShowMesh Core

[Documentation index](README.md)

This document is the cut procedure for ShowMesh Core: the coordinator, the
operator UI, and the node agent. It does not cover the FPP plugin, which lives
and versions in its own repository, `ShowMeshSystems/fpp-showmesh`.

## The versioning scheme

1. **One version for the triad.** The coordinator, operator UI, and node agent
   ship as a single version, cut from a single commit. They are never
   versioned independently of each other.
2. **Format.** `MAJOR.MINOR.PATCH`, semver, written bare in the root `VERSION`
   file. The git tag is `v` followed by the `VERSION` content, for example
   `v0.1.0`.
3. **MAJOR is the show season.** ShowMesh ships one supported line per holiday
   season, and the major number names the season: `0.x` is the 2026 season,
   the first and unofficial one; `1.x` is the 2027 season; and so on. A new
   major is a new line, installed fresh, with no compatibility or migration
   promise from the previous season's line.
4. **MINOR is a feature drop within the season.** Each planned set of features
   for the season (for example the pre-release set, the Halloween set, the
   Christmas set) raises MINOR. A MINOR bump may change or remove behavior.
5. **PATCH is a fix on a running line.** A PATCH release carries fixes only and
   is what gets deployed to a fleet mid-season without taking new features.
6. **The `-rc.N` suffix is allowed only on a season's opening `MAJOR.0.0`.** It
   means the new season's line exists and can be installed, but has not yet
   run a show on the rehearsal rig. Work on the next season starts while the
   current one is still running, so `1.0.0-rc.1` may exist during the 2026
   Christmas run for anyone who wants to try it. Once the line has run a show,
   the suffix comes off. No other version carries a suffix.
7. **A fix for the running season lands on the running line first.** While
   `0.3.x` is what the fleet runs, a fix ships as `0.3.1` and is carried
   forward to `1.0.0-rc.N` if it applies there. Never the other way round.
8. **The FPP plugin versions on its own.** It lives in
   `ShowMeshSystems/fpp-showmesh` with its own root `VERSION` file (currently
   `0.1.5`). It follows the same season rule for MAJOR, so a plugin and a
   coordinator from the same season share a major number, but MINOR and PATCH
   move independently.
9. **The first ShowMesh Core pre-release is `0.1.0`.**
10. **The release version is not the public API version.** `/api/v1` is
    versioned and moves independently of the `VERSION` file. Do not read a
    `VERSION` bump as an API change, and do not read an API version as a
    release number. This distinction is the reason this scheme exists:
    conflating the two is the mistake it prevents.

## What a pre-release cut produces

Pushing a `v<VERSION>` tag runs `.github/workflows/release.yml`, which
verifies the tag agrees with `VERSION` and `CHANGELOG.md` (see "Cutting a
pre-release" below), then publishes, with no manual step:

- The coordinator and operator UI images on GHCR, tagged with the bare
  version and the full commit SHA (no `latest` tag):
  `ghcr.io/showmeshsystems/showmesh-coordinator` and
  `ghcr.io/showmeshsystems/showmesh-ui`. The coordinator image is built with
  the release version, commit, and build date as its `-ldflags`, so a
  deployed coordinator's `GET /version` reports the real cut instead of
  `version=dev commit=none`.
- The node agent, packaged for amd64 and arm64, as GitHub release assets
  (`showmesh-node-agent_<VERSION>_linux_<amd64|arm64>.tar.gz`), the
  one-command installer (`showmesh-installer_<VERSION>.tar.gz` and
  `get-showmesh.sh`), and one `SHA256SUMS` covering all four. There is no
  armv7 asset yet: the cgo build fails on that target with a 32-bit C
  portability defect, a 64-bit-only constant in
  `internal/agent/audio/ltcgen/ltcgen_cgo.go` overflowing `size_t`. That is a
  source defect rather than release plumbing, and it is not fixed here.
- A GitHub pre-release entry whose body is the matching `CHANGELOG.md`
  section for that version.

An early tester who is given a pre-release today can either pull the
published images and the node agent asset for their platform, or build from
source at the tagged commit using the instructions in
[`CONTRIBUTING.md`](../CONTRIBUTING.md) (`make build`); both paths remain
supported. `deploy/docker-compose.yml` still builds from source by default;
run it against published images instead with `deploy/docker-compose.published.yml`
(see [`deploy/README.md`](../deploy/README.md)). `VERSION` in an unversioned
local build stays `dev`; a build that wants the real version string passes
`VERSION=` on the `make` command line explicitly, since the Makefile's
`VERSION ?= dev` default is not overridden by this procedure.

`workflow_dispatch` runs the same pipeline as a proof run on a branch, before
any tag exists: it builds and packages everything and publishes the two
images under the non-release tag `0.0.0-dispatch.<short sha>`, but creates no
GitHub release and uploads no release asset.

## Cutting a pre-release

Steps, in order:

1. Decide the new version number per the scheme above.
2. Bump the root `VERSION` file to that number (bare, one line, trailing
   newline, nothing else).
3. Move the `## Unreleased` section's content in `CHANGELOG.md` into a new
   dated section titled `## <VERSION> - <YYYY-MM-DD>`, using the date of the
   cut. Leave `## Unreleased` at the top, now empty, for the next round of
   changes.
4. Verify the two files agree: the git tag will equal `v` plus the `VERSION`
   file content, and the top released section of `CHANGELOG.md` (the first
   `## ` heading below `Unreleased`) must equal the `VERSION` file content
   exactly. A mismatch between `VERSION` and the top `CHANGELOG.md` section is
   a defect in the cut, not a stylistic difference.
5. Get the bump merged to `main` through the normal pull request process in
   [`CONTRIBUTING.md`](../CONTRIBUTING.md).
6. Tag the resulting commit on `main` as `v<VERSION>` (for example `v0.1.0`)
   and push the tag.

Steps 2 through 6 are manual today: there is no automation that bumps
`VERSION`, edits `CHANGELOG.md`, or creates the tag. Step 6, the tag push,
is what triggers `.github/workflows/release.yml` and produces everything
described in "What a pre-release cut produces" above; the release workflow
enforces step 4's agreement itself and fails the run before publishing
anything if the tag, `VERSION`, and `CHANGELOG.md` disagree.
