// Package fpp is the FPP REST collector: it polls one configured FPP
// instance's HTTP API and produces [observation.Observation] values for it.
// It implements internal/coordinator/collector.Collector — one *Collector
// per configured FPP instance, each with its own identity, backoff state,
// and (by design; see Collector's doc comment) serialized poll calls.
//
// This is the first ShowMesh collector that observes something outside the
// coordinator's own control-plane traffic: everything before Step 3 watched
// nodes and the broker, both of which ShowMesh itself put the evidence into.
// FPP is a real, independently-running system with its own defaults,
// inconsistent field encodings, and at least one endpoint whose obvious
// reading is wrong. Getting the honesty right here — reporting exactly what
// was observed, never inventing a value for what could not be — is what the
// rest of Step 3's API and stream inherit.
//
// # Read-only, GET only
//
// Per ADR-001 (FPP is the authoritative scheduler) and the Step 3 contract
// section 2 ("read-only means read-only"), this package issues HTTP GET
// requests and nothing else. It has no method that could send FPP a
// command. It also opens no MultiSync socket: pkg/multisync stays where it
// is, and ADR-013's UDP 32320 sharing hazard plus ADR-012's deferred
// host-networking question both keep coordinator-side multicast listening
// out of scope (contract section 5.5). This collector reads FPP's own REST
// report of its MultiSync behavior instead of trying to observe the wire
// protocol directly.
//
// # The MultiSync trap this package exists to avoid
//
// MultiSyncEnabled defaults to off in FPP. With it off, fppd plays
// sequences normally and emits zero MultiSync packets — indistinguishable,
// from the wire, from a network fault. Session recon notes recorded the
// obvious-looking way to read this setting, GET
// /api/settings/MultiSyncEnabled, and it is wrong: verified against a real
// FPP 9.5.3 and 10.0 (see testdata and fpp_test.go's
// TestMultiSyncEnabledNeverReadsTheSettingsEndpoint), that endpoint returns
// the setting's SCHEMA, not a decoded value, and on a daemon that has never
// had the setting explicitly written, the response carries no "value" key
// at all. www/api/controllers/settings.php's GetSetting() still only sets
// $sInfo['value'] when isset($settings[$settingName]) at the 10.0 tag
// (confirmed against refs/tags/10.0^{}, commit 370e62ed7) — the same
// schema-not-value shape, unchanged. A Go struct with a bool "value" field
// decodes that body without error and yields false — correct today by
// coincidence (the default is also false) and silently wrong the moment
// MultiSync is turned on, with a test written against it passing the whole
// time it is wrong. See
// testdata/settings_multisyncenabled_schema_no_value.json and
// testdata/settings_multisyncenabled_schema_with_value.json: the latter,
// captured from the same daemon after the setting had been explicitly PUT
// at least once, shows the key can also appear later, as a JSON STRING
// ("0"/"1", not a bool) — a second, differently-shaped trap for any code
// that assumes the first capture's absence of the key is the only failure
// mode.
//
// This package never requests /api/settings/MultiSyncEnabled at all.
// fpp.multisync.enabled is read exclusively from the top-level "multisync"
// boolean in /api/fppd/status, which reports what the running daemon is
// actually doing rather than what is merely configured — the two disagree
// until fppd restarts, because the setting carries "restart": 2. See
// multiSyncEnabledSignal's doc comment.
//
// # Field-shape hazards
//
// A live capture against a real FPP 9.4 daemon found that
// /api/fppd/status encodes some numeric-looking fields as JSON numbers
// (mode, status, volume, uptimeSeconds) and others as JSON strings
// (seconds_played, seconds_remaining, repeat_mode,
// current_playlist.count/.index). decode.go's
// rawDoc and its field extractors decode each field independently and
// tolerate either encoding for a numeric field, so one inconsistently-typed
// field degrades only its own signal to StateCollectionFailed rather than
// failing the whole document (and, with it, every other signal from a
// perfectly reachable FPP).
//
// Re-verified against FPP 10.0 source and worse there: src/playlist/
// Playlist.cpp:2322 (idle) writes "repeat_mode" as a JSON string ("0") and
// :2345 (playing) writes it as a JSON number (m_repeat) — the same field
// flips JSON type by playback state on one running daemon, confirmed
// against refs/tags/10.0^{}, commit 370e62ed7. See decode.go's
// numberField doc comment.
//
// # Verification status
//
// Endpoint shapes and the field-typing hazards above are verified against a
// real FPP 9.5.3 and 10.0 (bench/fpp-multisync, container
// showmesh-bench-fpp-master, FPP_GIT_REF selecting either build) as of
// 2026-08-10 (9.5.3) and 2026-08-23 (10.0, upstream tag 10.0, commit
// 370e62ed7) — see each testdata file and fpp_test.go / integration_test.go
// for which claims were checked against the live daemon versus against a
// captured body, and testdata/v10-bench/README.md for the 10.0 captures.
// Step 5 (2026-08-11) added a read-only REST probe of the operator's live
// fleet — fpp-player, fpp-remote-a, fpp-remote-b (see
// docs/reference-installation.md): fpp-player and fpp-remote-b on FPP 9.4,
// fpp-remote-a on a 9.x master build (9.x-master-822-g56515e4d) — captured
// to testdata/live_*.json; those captures are what signals.go's StatusSignals,
// PortSignals, and SystemInfoSignals are written and tested against.
// Neither the bench captures nor the live fleet probe raises RES-002's L2
// promotion (protocol semantics) at all — RES-002 covers the MultiSync wire
// protocol, and this package speaks REST, never that wire — and neither
// says anything about hardware, drift, or live-show behavior; L1 there is
// untouched. Per Step 5 contract section 7: every "ma" (pixel current)
// reading on the live fleet was 0 with the display de-energized, which
// confirms the field's shape and type and proves NOTHING about whether
// current telemetry works — no comment, log line, test name, or document
// in this package may claim otherwise.
//
// The 10.0 bench's own /api/fppd/ports capture is an empty array — that
// container has no channel outputs configured, the same as an unconfigured
// 9.5.3 host — so it is evidence for the endpoint's shape and reachability
// only, never for the port-key-omission behavior signals.go's
// portAbsentReason and portCurrentMASignal now model. That omission (no
// "status"/"enabled"/"ma" key when a port has no eFuse/enable pin/current
// sensor) is verified from FPP 10.0 source only — OutputMonitor::appendTo,
// src/OutputMonitor.cpp, confirmed against refs/tags/10.0^{}, commit
// 370e62ed7 — and has never been observed on a running FPP 10 daemon by
// this package. See v10_signals_test.go's TestPortSignalsV10SourceDerivedKeyOmission
// and testdata/fpp10_ports_source_derived_not_captured.json for the fixture
// this is tested against and its source-derived, not-a-capture label.
//
// # The "warnings" key: absent-when-empty, verified against FPP's own source
//
// fpp-remote-b's live /api/fppd/status capture (testdata/live_remote04_fppd_status.json)
// has no "warnings" key at all, while fpp-player's and fpp-remote-a's both
// carry a populated array. Step 5 contract section 3.4 asks this to be
// checked against FPP's own source rather than assumed: httpAPI.cpp's
// status handler builds the field with
//
//	for (auto& warn : WarningHolder::GetWarnings()) {
//	    result["warnings"].append(warn.message());
//	    result["warningInfo"].append(warn);
//	}
//
// — a jsoncpp Json::Value's operator[] is only ever invoked inside this
// loop, so when GetWarnings() is empty, "warnings" and "warningInfo" are
// never touched and the key is never created; jsoncpp does not implicitly
// materialize a key that was never accessed. Confirmed by reading the
// source directly, not by inference from the captures alone: FalconChristmas/fpp,
// branch "v9.4" (the exact branch this fleet's "branch" field names — see
// live_main_fppd_status.json/live_remote04_fppd_status.json), commit
// 7e3c6acb02386e65855f420aa21cde518453be38, src/httpAPI.cpp lines 181-182
// (fetched via the GitHub API 2026-08-11; permalink:
// https://github.com/FalconChristmas/fpp/blob/7e3c6acb02386e65855f420aa21cde518453be38/src/httpAPI.cpp#L181-L182).
// This is an L1 citation (source-verified, not merely bench-observed): FPP
// omits "warnings" from /api/fppd/status when there are no active
// warnings, rather than publishing an empty array. signals.go's
// warningsSignals models REST absence as StateUnsupported regardless — see
// its doc comment for why the source-verified answer does not change the
// modeling choice contract section 3.4 specifies.
//
// Re-verified against FPP 10.0 source: src/httpAPI.cpp lines 203-204
// (confirmed against refs/tags/10.0^{}, commit 370e62ed7) still only
// appends to result["warnings"] inside the same GetWarnings() loop — the
// absent-when-empty behavior above is unchanged at 10.0.
//
// # Command responses: an unconditional 200 on FPP 9.5.3 and 10.0
//
// src/commands/PlaylistCommands.cpp's StartPlaylistCommand,
// TogglePlaylistCommand, StartPlaylistAtCommand, and
// StartPlaylistAtRandomCommand each return Command::Result("Playlist
// Starting") unconditionally — there is no branch that returns a
// different result or a non-200 status when the named playlist does not
// exist or the daemon otherwise fails to start it. Re-confirmed at the
// 10.0 tag (refs/tags/10.0^{}, commit 370e62ed7): identical shape, same
// four call sites. Any caller of this plugin's command layer that treats
// HTTP 200 plus this response body as proof playback started is wrong on
// both supported versions; see internal/coordinator/fppcommand for how
// this package's own command client treats that response.
package fpp
