# Releasing ShowMesh Core

[Documentation index](README.md)

This document is the cut procedure for ShowMesh Core: the coordinator, the
operator UI, and the node agent. It does not cover the FPP plugin, which lives
and versions in its own repository, `ShowMeshSystems/fpp-showmesh`.

## The versioning scheme

1. **One version for the triad.** The coordinator, operator UI, and node agent
   ship as a single version, cut from a single commit. They are never
   versioned independently of each other.
2. **The FPP plugin versions on its own.** It lives in
   `ShowMeshSystems/fpp-showmesh`, is currently at `0.1.2`, and its number has
   no fixed relationship to this repository's version beyond both currently
   being on a `0.1.x` line.
3. **Format.** `MAJOR.MINOR.PATCH`, semver, written bare in the root `VERSION`
   file (the same shape as the plugin repository's root `VERSION` file, which
   contains `0.1.2`). The git tag is `v` followed by the `VERSION` content,
   for example `v0.1.0`. Pre-release suffixes use the semver form `-rc.N`.
4. **The first ShowMesh Core pre-release is `0.1.0`.**
5. **0.x means no compatibility promise between releases.** A `0.x` version
   bump may change or remove behavior without notice; nothing about the
   `VERSION` number implies migration support between two `0.x` releases.
6. **The release version is not the public API version.** `/api/v1` is versioned
   and moves independently of the `VERSION` file. Do not read a `VERSION` bump
   as an API change, and do not read an API version as a release number. This
   distinction is the reason this scheme exists: conflating the two is the
   mistake it prevents.

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
  (`showmesh-node-agent_<VERSION>_linux_<amd64|arm64>.tar.gz`, plus a
  `showmesh-node-agent_<VERSION>_SHA256SUMS` covering both). There is no
  armv7 asset yet: the cgo build fails on that target with a 32-bit C
  portability defect unrelated to release plumbing (a 64-bit-only constant
  in `internal/agent/audio/ltcgen/ltcgen_cgo.go` overflowing `size_t`),
  tracked as a source fix separate from this workflow.
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
