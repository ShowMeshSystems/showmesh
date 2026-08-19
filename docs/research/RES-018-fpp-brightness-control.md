# RES-018: FPP Brightness Control and Plugin Runtime

[Architecture](../architecture/RESTING-MODE.md) · [Distribution research](RES-015-fpp-plugin-distribution-model.md) · [Track F](../build/TRACK-F-resting-mode.md) · [Tracker](README.md)

Status: planned · Risk: **high** · Verification: **L1 source evidence** for the FPP plugin mechanisms and **L2 bench evidence** for the absence of a stock FPP brightness command; no ShowMesh brightness component has been built or installed on an FPP host

Decision date: 2026-08-18. This record answers the design question raised by SM-49. It does not claim that the selected design, the third repository, release artifacts, or real-host installation exist yet.

Linear tracking: SM-63 owns the runtime/repository build; SM-14 owns the first real-host install and release-publication gate. SM-49 is complete because the owner decision is made.

## 1. Finding

Stock FPP does not expose the compositional brightness seam required by [RESTING-MODE §7.3](../architecture/RESTING-MODE.md#73-brightness-composition). The bench FPP 9.5.3 command vocabulary contains no brightness command. Its brightness-shaped API replaces the complete output-processor configuration, and the running status has no separately observable scheduled ceiling.

`FalconChristmas/fpp-brightness` proves that the correct per-frame extension point exists: `modifyChannelData` can multiply rendered channel data without rewriting the output-processor configuration. The upstream plugin does not itself solve ShowMesh's requirement because it owns one scalar. HTTP, MQTT, FPP commands, and MultiSync all overwrite that same value; there is no independent ceiling and transition gain.

The selected design is therefore a ShowMesh-owned FPP component with two independently owned values:

```text
effective output = round(ceiling * transition_gain / 100)
```

- `ceiling` is 0–100 and is written by the FPP schedule/operator command path. The plugin will register a parameterized FPP Action named `ShowMesh: Set Brightness Ceiling` with `targetPercent` (integer 0–100) and `fadeSeconds` (integer 0–86400), so ordinary FPP scheduler entries and manual presets can set it. `fadeSeconds: 0` applies immediately; a positive value linearly interpolates the ceiling to the target per output frame.
- `transition_gain` is 0–100 and is written by the ShowMesh night-session controller.
- Both default to 100. A ceiling change during a fade takes effect immediately; a later gain of 100 reveals the current ceiling, never a cached earlier ceiling.
- A new ceiling action received during an active ceiling fade starts from the currently interpolated ceiling, not the previous start or target, so it introduces no discontinuity. Ceiling fades and ShowMesh transition-gain fades may run concurrently and compose on every frame.
- The component applies only to configured channel ranges. Overlap and out-of-bounds ranges are rejected.
- MultiSync carries the complete versioned brightness and active-fade state, not relative adjustments. A late-joining node can converge on the current fade position and target; stale or incompatible payloads are rejected rather than guessed at.
- Fade state and target are persisted before activation. After restart the component reconstructs the current fade position from persisted timing; if that timing cannot be trusted, it chooses the darker of the last applied ceiling and target rather than jumping brighter.
- The transition gain is not exposed as an FPP Action; only the coordinator-facing night-session contract may write it. MQTT setters, relative brighten/dim commands, and hard-coded fade-up/fade-down commands are out of scope. They would add writers or hide ownership.

**Reference-installation fact, confirmed by the owner 2026-08-18:** the deployed 10 PM and 11 PM scheduler entries invoke commands from `FalconChristmas/fpp-brightness` and use timed fades rather than jumps. Migration retargets those entries to `ShowMesh: Set Brightness Ceiling` with the same target percentages and fade durations in seconds. The old plugin is removed only after the new action, MultiSync propagation, fade behavior, and effective output are verified on the participating hosts.

## 2. Supported FPP versions

The build targets **FPP 9.4 through 9.x and FPP 10.x**. FPP 8 is not supported.

As checked on 2026-08-18, the latest official FPP 10 release is [`10.0-beta`](https://github.com/FalconChristmas/fpp/releases/tag/10.0-beta), released 2026-07-24; no official 10.0 RC release is published. The beta changes the HTTP implementation from libhttpserver to Drogon and introduces a revamped Plugin Manager, so one C++ binary cannot be assumed compatible across majors.

The runtime will have one shared brightness engine and two narrow FPP adapters:

- FPP 9 adapter: the 9.x plugin/lifecycle and libhttpserver surface.
- FPP 10 adapter: the 10.x plugin/lifecycle and Drogon surface.

FPP 10 beta is an implementation target from the first build. Each RC and final release will be added to the compatibility matrix and rebuilt against its installed headers; a version label alone is not evidence of ABI compatibility.

## 3. Repository decision

The approved target is three repositories with one-way release dependencies:

| Repository | Responsibility |
|---|---|
| `showmesh` | Coordinator, API/OpenAPI contract, UI/CLI, collectors, night-session integration, and the coordinator-facing brightness contract. |
| `fpp-showmesh` | Thin FPP Plugin Manager repository: `pluginInfo.json`, command descriptions, lifecycle scripts, version pins, and committed artifact hashes. |
| `showmesh-fpp-plugin` | FPP runtime source: the Go macro helper, C++ brightness engine and version adapters, tests, and release artifacts. |

Current reality is intentionally different while extraction is pending: the Go helper remains under `showmesh/cmd/showmesh-fpp-plugin`, `fpp-showmesh` exists only as a local sibling used for packaging work, and `showmesh-fpp-plugin` has not been created. No document should describe the target split as already implemented.

## 4. Install and release contract

After the SM-14 gate passes, `showmesh-fpp-plugin` will publish one versioned release containing:

- statically linked Go helper archives for `linux/amd64`, `linux/arm64`, and `linux/arm/v7`;
- one architecture-independent C++ source bundle containing the shared engine and both FPP adapters;
- a release manifest suitable for producing the packaging repository's lock file.

`fpp-showmesh` commits `artifacts.lock.json` with the exact release version, filenames, and SHA-256 digest for every downloadable artifact. Installation downloads only those exact files, verifies them against the committed hashes, and fails closed on a mismatch. A checksum manifest fetched from the same mutable release location is not the trust anchor.

The Go helper is installed prebuilt. The C++ adapter is selected from the installed FPP major and compiled locally against that host's installed FPP headers and libraries. Installation stages the new files, validates the binary and compiled component, and atomically activates them; a failed download, verification, compile, or activation leaves the previous working version in place. Uninstall reverses every file and service created outside the plugin directory.

This local C++ build is the highest-risk implementation assumption. It must be measured on the slowest supported ARMv7 host for compiler availability, headers, memory, build time, and rollback behavior before official listing or a readiness claim.

## 5. Go client seam and future SDK

No shared ShowMesh Go SDK is built now. As part of SM-63, the runtime repository will define a narrow consumer-side `CoordinatorClient` for command handling:

- run a macro, with any buffered prior failures included in the request;
- read a macro definition for the local cache/status behavior.

The existing handwritten HTTP client will move behind that interface without behavioral changes. Local persistence, cached macro data, status rendering, refusal/outage classification, and failure-buffer policy remain plugin-owned.

Prior failures are not a separate client call. They remain part of the run request so the buffer is cleared only after a successful 2xx response, preserving the current transactional behavior.

A future versioned SDK should be integrated through a thin adapter in `showmesh-fpp-plugin`. It cannot directly implement an interface whose method signatures use plugin-local named request/result types: Go requires identical named types, and a `package main` consumer is not importable. Creating a shared contract package now would be the shared dependency this decision intentionally defers. The adapter preserves the seam without forcing that dependency.

The C++ brightness component will remain independent of this Go interface and any future SDK. It will use FPP-native APIs plus its own narrow coordinator-facing brightness contract.

## 6. Build sequence

1. Extract the current Go runtime and its release workflow from `showmesh` into `showmesh-fpp-plugin`, replacing its `internal/version` dependency and keeping behavior unchanged.
2. Add the consumer-side `CoordinatorClient` and fake-driven command tests; retain HTTP-level regression tests for request, status-code, and failure-buffer behavior.
3. Implement the shared two-value brightness engine, range validation, persistence, and versioned full-state synchronization without FPP headers.
4. Add isolated FPP 9 and FPP 10 adapters and compile each against pinned upstream/bench headers in CI.
5. Produce local/private candidate runtime assets, generate `artifacts.lock.json`, and update `fpp-showmesh` to verify, stage, compile, activate, roll back, upgrade, and uninstall them.
6. Wire the coordinator's ceiling observation and transition-gain commands through the public contract and Track F action runner.
7. Run the packaging linter, container/bench matrix, then the first real-host install under SM-14 using the local/private candidate assets. Publish the versioned release only after that gate passes, unless the owner explicitly changes the standing rule.

## 7. Acceptance evidence required

- FPP 9.4/9.x and the current FPP 10 release each install, compile, load, upgrade, reboot, and uninstall cleanly.
- amd64, arm64, and armv7 select and verify the correct Go artifact without relying on `uname -m` alone.
- A corrupt binary or source bundle, missing compiler/header, compile error, incompatible adapter, and interrupted upgrade all preserve the previous working installation.
- Ceiling and gain are independently readable and writable; either can change while the other remains stable.
- `ShowMesh: Set Brightness Ceiling` appears in the FPP Action/Command UI on both supported majors, accepts `targetPercent` 0–100 and `fadeSeconds` 0–86400, rejects missing/malformed/out-of-range input, changes only the ceiling, and can be invoked by an ordinary FPP scheduler entry.
- A 100→75 command with a positive fade duration produces monotonic per-frame values and reaches exactly 75 at the deadline; zero seconds applies 75 immediately.
- Replacing an in-progress fade begins at its current interpolated value without a jump. Restart and late-join tests converge on the same target without ever failing brighter.
- The decisive composition case passes: ceiling 60, fade gain downward, ceiling changes to 40 during the fade, then gain returns to 100 and effective output is 40.
- Configured excluded ranges are byte-identical before and after a fade.
- MultiSync convergence survives duplicate, delayed, stale, and version-incompatible brightness payloads without applying a relative change twice.
- Coordinator outage, `401`, and `403` retain the Go helper's existing local status and prior-failure behavior.
- The installed component survives reboot and does not bind a second UDP 32320 listener.

Until these run on the intended hosts, Track F readiness rejects any configuration that requires compositional FPP brightness. The design decision removes an architecture unknown; it does not remove the verification gate.

## 8. Open deployment facts

- Record the current 10 PM/11 PM percentages and the participating hosts before migrating the confirmed `fpp-brightness` scheduler entries.
- Inventory the installed `FalconChristmas/fpp-brightness` versions so removal and rollback are explicit.
- Record channel ranges that must be included or excluded on each host.
- Measure local C++ compilation on the slowest ARMv7 target and confirm the available FPP development headers for both supported majors.
- Decide and record licensing for the new runtime repository and any derivative use of upstream plugin code.
- Revalidate FPP 10 compatibility at each RC/final release and whenever FPP changes its plugin ABI, HTTP framework, or Plugin Manager lifecycle.
