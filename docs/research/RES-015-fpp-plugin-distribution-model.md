# RES-015: FPP Plugin Repository and Distribution Model

[Architecture](../architecture/ARCHITECTURE.md) · [ADR-013](../decisions/ADR-013-no-fpp-control-port-sharing.md) · [Tracker](README.md)

Status: unresearched · Risk: medium · Verification: L0 — assumption

## Decision to make

Define how the FPP plugin is packaged, versioned, and installed: whether it lives in its own repository (required by FPP's plugin manager, which git-clones a listed repo URL and expects a fixed layout at its root — `pluginInfo.json`, install/uninstall scripts — so this half of the question is close to settled already), and whether its installer pulls a built `showmesh-agent` artifact from the main repo's releases or something else. This record exists to verify that plan against FPP's actual plugin manager behavior rather than adopt it on assumption, and to work out the parts not yet decided: release/versioning scheme, target-arch matrix, checksum verification, upgrade/rollback behavior, and how the plugin's callback hooks are scoped against ADR-013's "use FPP's plugin callback boundary, not a second socket" rule.

## Questions

- What does FPP's plugin manager actually require in a plugin repo's structure and `pluginInfo.json` today, and does a thin installer-only repo (no agent source) satisfy it?
- Can `install.sh` fetch a pinned binary release from a separate (main ShowMesh) GitHub repo, or does FPP's plugin install flow assume everything needed ships inside the plugin repo itself (offline installs, air-gapped shows, review requirements for the official FPP-Plugins listing)?
- What target architectures does the installer need to support (Pi 3/4/5, x86 FPP installs), and does the main repo's existing arm64 static-build pipeline (ADR-012) cover them without change?
- How does the plugin declare/pin a compatible agent version, and what happens on a coordinator/agent protocol bump if a user's installed plugin is behind?
- What does uninstall need to clean up (service files, binaries, any FPP config the plugin touched), and does FPP's plugin manager call a defined uninstall hook reliably?
- Which FPP plugin callback hooks are available (e.g., start/stop/config-change events) and are they sufficient for whatever the plugin needs to observe without opening a second UDP 32320 listener, per ADR-013?
- Does the official FPP-Plugins registry impose review, licensing, or structural requirements (e.g., no binary blobs committed to the repo) that constrain the "download a release artifact" approach?

## Acceptance criteria

- A plugin repo installs cleanly on a real FPP instance (bench Pi, per RES-002's existing rig) via FPP's own plugin manager UI, not a hand-run script.
- The installed agent binary matches the pinned release checksum and runs as a supervised service that survives an FPP reboot.
- Uninstall removes the service and binary with no leftover process or stale MQTT LWT registration.
- A protocol-incompatible agent/coordinator pairing fails loudly (version-mismatch error surfaced to the operator) rather than silently misbehaving.
- No second listener on UDP 32320 is introduced; any FPP-side observation goes through a documented plugin callback hook.

## Test matrix and method

Bench-only until there is a first cut of `showmesh-agent` to distribute (this record is gated on Step 6, GStreamer pipeline supervision, existing at minimum). Test against the same containerized `fppd` used for RES-002 first, then the physical bench Pi. Exercise: fresh install, upgrade from an older pinned release, uninstall, install with no network reachable to the release host, and install where the coordinator is unreachable at agent-start time (should not block FPP or the plugin install itself, consistent with constraint 6).

## Evidence and findings

None. This record is a work queue, not a conclusion.

## Decision, fallback, and revalidation

Working assumption carried into `BUILD-PLAN.md` until source-verified: separate thin plugin repo, `install.sh` fetches a pinned `showmesh-agent` release from the main repo rather than building or vendoring agent source in the plugin repo. Fallback if FPP's plugin listing process or its plugin-manager mechanics don't tolerate an external-artifact fetch: vendor a prebuilt binary directly in the plugin repo per tagged release, accepting the larger repo and the extra release-sync step that implies.

Revalidate before Step 6 ships anything install-facing, and again if FPP's plugin manager or the FPP-Plugins registry's requirements change.
