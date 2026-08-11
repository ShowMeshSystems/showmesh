# RES-011: Pixel-Current Diagnostics

[Observability](../architecture/OBSERVABILITY.md#8-pixel-current-diagnostics) · [Tracker](README.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: planned (bench) · Risk: critical · Verification: L1 — source verified 2026-08-10

## Decision to make

Define safe controller polling, baseline capture, diagnostic patterns, deviation classification, and readiness-gating rules for per-port pixel current.

## Questions

- Which current and voltage APIs are available on the deployed controllers (Kulp K16A-B, K16, K16-Pro, and any future eFuse-class boards, all running FPP)?
- What polling cadence is safe during idle, diagnostics, and live playback?
- How accurate and repeatable are readings across brightness, voltage, temperature, controller load, and pixel type?
- Which known-load patterns best isolate missing branches, power injection faults, shorts, and unstable connections?
- Should baselines be absolute, proportional to configured pixels, learned, or a combination?
- What retry, hysteresis, and minimum sample count prevent noisy classifications?
- Which conditions warn, inhibit show start, or require immediate output shutdown?

## Acceptance criteria

- Polling does not measurably degrade controller output or network behavior.
- Baseline variance is characterized across representative nights and temperatures.
- Known test faults are detected with recorded sensitivity and false-positive rates.
- Every diagnostic restores outputs to a defined safe state after success, timeout, coordinator loss, or controller loss.
- Excess-current handling is reviewed as a safety function rather than only an alert.

## Test matrix and method

Record controller model, firmware, receiver topology, voltage, pixel type/count, injection, cable length, ambient temperature, brightness, and pattern. Repeat clean baselines, then introduce controlled disconnected sections, missing injection, blown fuse equivalents, intermittent connections, voltage sag, and safe simulated overcurrent conditions. Preserve raw readings and physical verification.

## Evidence and findings

Desk research 2026-08-10 (vendor pages, FPP source read, release notes, forums; no hardware). Confidence tags: [doc] official/vendor doc, [src] FPP source read, [anec] forum/community. A 10–15 percent tolerance remains only a candidate starting point.

### Hardware finding: both primary deployed boards measure current; the standby board does not

- **Deployed primary controllers — K16-Max and K16A-B (eFuse variant) — both have per-string eFuses with current monitoring.** The K16A-B's eFuse capability was **confirmed by direct operator inspection of the deployed board, 2026-08-10** (quality: operator input per OBSERVABILITY §4.1). Note the vendor-doc discrepancy: the published [K16A-B manual](https://kulplights.com/manuals/K16A-B-Manual.pdf) describes a blade-fused revision, so K16A-B exists in fused and eFuse revisions — **record the exact board revision during bench work** and do not trust vendor pages alone for this model line. [operator + doc]
- **K16-Pro (on standby, not built out) is blade-fused only** per vendor pages and its absence from Kulp's [eFuses category](https://kulplights.com/product-category/controllers/efuses/); if it ever enters service it would advertise `telemetry.board-sensors` without `telemetry.port-current` — or more likely be replaced by an eFuse-class board at that point. [doc — by omission; verify if deployed]
- Kulp's eFuse-class line ([K16-Max](https://kulplights.com/product/k16-max/), K32-Max, K8-Max, K8-PB/K8-Pi, K4-PB, K2 family) provides per-string current monitoring and alerts. [doc]
- Consequence: **the OBSERVABILITY §8 known-load readiness workflow is viable on the entire active fleet.** The capability model still expresses current telemetry as optional (ADR-002) for mixed fleets and the standby board.

### FPP telemetry surface (applies when hardware supports it)

- `GET /api/fppd/ports` — per-port `name`, `enabled`, `status` (false = eFuse tripped), `ma`, `pixelCount`, layout hints; returns `[]` on boards without sensors. `GET /api/fppd/ports/pixelCount` runs a current-based pixel count test. [src: `src/OutputMonitor.cpp`]
- MQTT `{prefix}/falcon/player/{host}/port_status` at a configurable interval (added FPP 8.0: [release notes](https://github.com/FalconChristmas/fpp/releases/tag/8.0)); `fppd_status` carries `sensors[]` (temperature/voltage — present on **all** deployed Kulp boards); warnings topic carries eFuse trips (warning ID 16). [doc/src: [MQTT.md](https://github.com/FalconChristmas/fpp/blob/master/docs/MQTT.md)]
- eFuse trip is interrupt-driven: port force-disabled, optional auto-retry (`eFuseRetryCount`/`eFuseRetryInterval`), and a **command preset hook `EFUSE_TRIGGERED`** — event push, not polling. FPP commands `Set Port Status`/`Outputs On/Off` give the desired-state side (ADR-003 pairing). [src]
- eFuse support and the Current Monitor UI landed in FPP 7.0; per-port reset in 7.5. [doc]

### Live probe of the deployed boards (2026-08-11, L1 for shape, NOT evidence that current telemetry works)

Read-only `GET /api/fppd/ports` against both deployed Kulp boards, off-season, display de-energized, no outputs enabled. Versions and hosts per [reference installation](../reference-installation.md).

- **The response is a heterogeneous JSON array, not a uniform one.** Two distinct entry shapes arrive in the same list, and a decoder declaring one struct for all elements is wrong:
  - real ports: `{bank, col, enabled, ma, name, row, status}` — 16 on each board;
  - smart-receiver positions: `{col, name, row, smartReceivers: true}` — **no `ma`, `status`, `enabled`, or `bank` key at all**. 16 of these on the K16A-B (32 entries total), 32 on the K16-Max (48 entries total).
- This is the same class of hazard Step 3 recorded for `/api/fppd/status` (numeric-looking fields arriving as JSON strings): a Go struct that assumes every element carries `ma` will either fail to unmarshal or silently read a zero-valued current for positions that have no current reading at all. The second outcome is worse, because zero milliamps is a plausible reading rather than an obviously wrong one, and it would be indistinguishable from a dark port.
- **`pixelCount` was absent from every entry on both boards.** The L1 source-derived claim above lists it as a field of this endpoint. Both observations can hold: the operator confirms the FPP pixel-count operation (`GET /api/fppd/ports/pixelCount`) has **never been run** on these hosts, so the field is plausibly populated only once a count exists. Recorded as an open question rather than as a contradiction, and **not** resolved by this probe. Do not model `pixelCount` as always-present.
- **Every `ma` read `0`, as an integer**, on all 32 real ports across both boards. With nothing running and no outputs enabled this is the expected reading and it is **not evidence that current telemetry functions**. It confirms the field's presence and type, nothing more. Raising RES-011 above L1 requires readings from an energized display with known load, per the acceptance criteria above.
- `status` was `true` and `enabled` was `false` on every real port, consistent with a configured-but-idle board.

**What this does and does not license.** It licenses building the collector against the real schema, including the heterogeneous-array handling, rather than against the documented one. It licenses nothing about eFuse behaviour, trip reporting, current accuracy, or per-branch blind spots.

### Smart receivers

- FPP implements the **Falcon V5 smart-receiver query protocol** (PRU-assisted): sub-receivers A–F per port report `ma`, `pixelCount`, enabled/tripped upstream over the differential pair, surfaced in the same `/api/fppd/ports` payload; remote fuse reset supported. Falcon V4-protocol receivers are send-only (no telemetry). [src: `src/non-gpl/FalconV5Support/`]
- **Deployed receivers are older pre-V5 units with no current/fuse telemetry (operator-confirmed 2026-08-10); planned replacement with V5-protocol receivers. Kulp boards support the V5 receiver protocol (operator statement; bench-verify sub-receiver `ma` entries appear under FPP 9.x when V5 receivers arrive).** Until then, ports behind those receivers are current-telemetry **blind spots** and must report as such: the controller-level eFuse reading covers the whole port, but per-sub-receiver breakdown is unavailable, and health for those branches is `unknown`-biased rather than assumed healthy (ADR-011). Baselines captured pre-upgrade go `stale` when receivers are swapped. [operator]

### Falcon controller comparison (not deployed)

- Falcon F16v4/v5 show per-port current in their web UI but the `/API` JSON dialect is undocumented ("nothing publicly available" — [forum](https://falconchristmas.com/forum/index.php?topic=16952.0)); xLights `Falcon.cpp` is the de facto reference. Normalizing on **FPP's port schema** is the right contract; Falcon would need a bespoke adapter. [anec/src]

### Safe polling

- No official guidance exists. FPP itself moved UI status from polling to push, and MQTT publish intervals are deliberately configurable — push-at-low-Hz is the intended pattern. Pixel output runs on PRUs (HTTP load cannot directly jitter the wire), but the single Cortex-A8 on BBB-class boards is shared. Hypothesis for bench: MQTT `port_status` at 5–10 s, REST ≤1 Hz, back off during playback, rely on interrupts/warnings for trips. [src/anec]

### Fallback and corroborating measurement

- Community practice: INA226/INA3221 shunt sensors on ESP32 via ESPHome/Tasmota/ESPEasy publishing V/A over MQTT — realistic granularity is per-PSU/bank (50–100 A shunts), not per-port; AC-side smart plugs catch dead supplies. These map cleanly onto ADR-008 as independent power-sensor capability providers. [anec]
- **Deployed: ESPHome-flashed Emporia Vue provides per-circuit AC power over MQTT (operator-confirmed 2026-08-10).** This enables an **AC power-envelope check** covering receiver blind spots: baseline expected circuit draw per lifecycle state (and optionally per sequence/known-load diagnostic pattern), then flag "circuit draw below envelope during content that should be lit — possible fault on a non-monitored branch." Constraints per ADR-011: this is *derived, circuit-granularity* evidence confounded by PSU efficiency, power factor, brightness, and content — it yields **warning-level, low-confidence** observations that narrow a fault to a circuit, never a critical alert or root-cause claim on its own. Strongest during the known-load diagnostic (§8.1), where expected draw is controlled. Envelope baselines are versioned like current baselines and go `stale` on load changes. [operator]

## Additional acceptance criterion

- The capability model must express current telemetry as optional (`telemetry.port-current`, `telemetry.board-sensors`, `protection.efuse`) so mixed fleets (K16A-B beside K16-Max) report honestly per ADR-002/ADR-011.

## Decision, fallback, and revalidation

Decision pending bench work on the actual boards. Key open bench items: confirm `/api/fppd/ports` returns live `ma` on both the K16-Max and the eFuse K16A-B (and record both board revisions); `ma` accuracy vs clamp meter; REST/MQTT polling cost on BBB under 16×800px@40fps playback; whether deployed smart receivers speak the V5 query protocol; forced eFuse-trip behavior with retry 0 and >0. Fallback is read-only display of raw telemetry and manual diagnostic interpretation; for any non-eFuse board entering service, per-bank INA-class sensor nodes over MQTT. Revalidate after controller firmware, topology, pixel count/type, power distribution, or baseline procedure changes.
