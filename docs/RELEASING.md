# Releasing ShowMesh Core

[Documentation index](README.md)

This document is the cut procedure for ShowMesh Core: the coordinator, the
operator UI, and the node agent. Its versioning scheme also sets the season
and suffix rules the FPP plugin and ShowMesh Companion follow; their cut
procedures live in their own repositories.

## The versioning scheme

1. **One version for the triad.** The coordinator, operator UI, and node agent
   ship as a single version, cut from a single commit. They are never
   versioned independently of each other.
2. **Format.** `MAJOR.MINOR.PATCH`, semver, written bare in the root `VERSION`
   file, with an optional prerelease suffix (rule 6). The git tag is `v`
   followed by the `VERSION` content, for example `v0.1.0` or
   `v1.0.0-beta.2`.
3. **MAJOR is the show season.** ShowMesh ships one supported line per holiday
   season, and the major number names the season: `0.x` is the 2026 season,
   the first and unofficial one; `1.x` is the 2027 season; and so on. A new
   major is a new line, installed fresh, with no compatibility or migration
   promise from the previous season's line.
4. **MINOR is a feature release.** A MINOR is cut when enough features have
   accumulated to be worth a release. A MINOR bump may change or remove
   behavior, within the compatibility rule below.
5. **PATCH is a fix on a running line.** A PATCH release carries fixes only and
   is what gets deployed to a fleet mid-season without taking new features.
6. **A prerelease suffix is allowed on any version.** The suffix is `-alpha.N`,
   `-beta.N`, or `-rc.N`, numbered from 1, and says how far that version is
   from release:
   - `alpha`: features are still landing and anything may break.
   - `beta`: the feature set is settled and is being tested on the rehearsal
     rig.
   - `rc`: a candidate for the release; only fixes land from here.
7. **The next season's line starts in January.** After the Christmas run,
   `main` starts cutting the next major as prereleases: `1.0.0-alpha.N` from
   January, then `-beta.N` and `-rc.N` through the summer. The final
   `1.0.0` ships in August, ready for the season. Work on a new feature
   within a season may use the same suffixes, for example `1.1.0-beta.1`.
8. **`VERSION` changes only in the pull request that cuts a release.** Merged
   features accumulate under `## Unreleased` in `CHANGELOG.md` until the cut.
   `VERSION` on `main` always names the last release cut from it.
9. **When to cut.** The `0.x` line has no fixed schedule: a MINOR is cut when
   features have accumulated, and a PATCH whenever a fix is needed. From
   January the target is weekly: if `## Unreleased` holds anything, cut a
   prerelease that week; if it is empty, skip the week. The cadence for a
   released `1.x` line is decided before `1.0.0`.
10. **A fix for an older line is cut from a release branch.** Once `main` has
    moved past a line, a fix for that line lands on a `release/<MAJOR>.<MINOR>`
    branch (for example `release/0.4`), is cut and tagged from there, and is
    carried forward to `main` if it applies there. Never the other way round.
11. **The FPP plugin and the companion version on their own.** The FPP plugin
    lives in `ShowMeshSystems/fpp-showmesh` with its own root `VERSION` file;
    ShowMesh Companion lives in `ShowMeshSystems/showmesh-companion` and
    carries its version in `src-tauri/tauri.conf.json`. Both follow the season
    rule for MAJOR and the suffix rule above, so every component from the same
    season shares a major number, but MINOR and PATCH move independently.
12. **`0.x` is the experimental season.** It makes no compatibility promise
    between versions: a fix may need the plugin and the coordinator upgraded
    together. Compatibility within a major starts at `1.0.0`. From `1.0.0` on,
    every PATCH and MINOR works with every other version in the same MAJOR:
    any `1.x.x` FPP plugin works with any `1.x.x` coordinator, and the same
    for `2.x.x` and later. Between the coordinator and the node agent this is
    the expected practice rather than a guarantee; a MINOR that has to break
    it says so in its changelog and upgrade notes. The intended way to run a
    season is to stay on one MAJOR, take PATCH releases, and take a MINOR only
    for a feature it brings. A prerelease (rule 6) carries no compatibility
    promise.
13. **The first ShowMesh Core pre-release is `0.1.0`.**
14. **The release version is not the public API version.** `/api/v1` is
    versioned and moves independently of the `VERSION` file. Do not read a
    `VERSION` bump as an API change, and do not read an API version as a
    release number. This distinction is the reason this scheme exists:
    conflating the two is the mistake it prevents.

## What a pre-release cut produces

Pushing a `v<VERSION>` tag runs `.github/workflows/release.yml`, which
verifies the tag agrees with `VERSION` and `CHANGELOG.md` (see "Cutting a
pre-release" below), then publishes, with no manual step:

- The coordinator, operator UI and preview gateway images on GHCR, tagged
  with the bare version and the full commit SHA (no `latest` tag):
  `ghcr.io/showmeshsystems/showmesh-coordinator`,
  `ghcr.io/showmeshsystems/showmesh-ui` and
  `ghcr.io/showmeshsystems/showmesh-preview-gateway`. The gateway image
  carries the same NDI plugin build as the node agent tarball and never
  contains the NDI runtime. The coordinator image is built with
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

The build itself is `.github/workflows/build.yml`, which the edge build below
also runs on every merge to `main`, so a release tag runs build steps that
have already succeeded on its commit's edge build.

## Edge builds

Every merge to `main` that changes more than documentation runs
`.github/workflows/edge.yml`, which publishes a development build separate
from any release:

- Binaries report the `git describe` version, for example `0.4.0-3-ga1b2c3d`:
  three commits after `v0.4.0`. The coordinator's version check reads this as
  a build made after `0.4.0`, and a node and coordinator from the same edge
  build match exactly.
- Coordinator, operator UI and preview gateway images tagged with that version, with
  `edge-<short sha>`, and with `edge`, which always names the newest one.
- The node agent tarballs, the installer, and `SHA256SUMS` on a single GitHub
  prerelease named `edge`, replaced on every build. Its installer downloads
  from that release, so this installs or upgrades a machine to the newest
  edge build:

  ```sh
  curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/edge/get-showmesh.sh | sudo bash
  ```

  An installer kept from an older edge build stops working once a newer one
  replaces it; run the command above again instead.
- A newer merge cancels an edge build still running for an older one, so a
  long session of merges builds the newest commit rather than every commit.
- `.github/workflows/edge-cleanup.yml` runs weekly and deletes edge images
  beyond the newest 20 of each package. It deletes only image versions tagged
  `edge-<short sha>`, never a release image.

The `edge` prerelease never shows as a newer release in the coordinator,
because its tag is not a version number.

A feature built as several pull requests that do not work on their own lands
on a `dev/<feature>` integration branch first, and reaches `main` as one pull
request when it works. Edge builds then never carry a half-built feature.

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
   and push the tag. A fix for an older line (rule 10) is merged to and
   tagged on its `release/<MAJOR>.<MINOR>` branch instead.

Steps 2 through 6 are manual today: there is no automation that bumps
`VERSION`, edits `CHANGELOG.md`, or creates the tag. Step 6, the tag push,
is what triggers `.github/workflows/release.yml` and produces everything
described in "What a pre-release cut produces" above; the release workflow
enforces step 4's agreement itself and fails the run before publishing
anything if the tag, `VERSION`, and `CHANGELOG.md` disagree.

## Planned, not built

These are decided in direction and not implemented. Each is its own piece of
work.

### A release channel setting in the coordinator

An operator will choose which releases the coordinator's "newer release"
notice offers: stable only, stable and prereleases, or development builds as
well. This needs the release workflow to publish an unsuffixed version as a
normal GitHub release; today every cut is published as a GitHub prerelease.

### To decide before `1.0.0`

- **Show Ready.** Whether a release can be qualified for running a show, and
  what evidence qualifies it. [RES-009](research/RES-009-failure-mode-testing.md)
  is expected to become that bar. No `0.x` release is ever Show Ready.
- **The `1.x` release cadence**, from what the weekly prereleases show.
- **Release preparation help:** a workflow or agent that reviews changes since
  the last release, proposes the version, and prepares `VERSION`,
  `CHANGELOG.md`, and the release pull request. Publishing stays manual.
