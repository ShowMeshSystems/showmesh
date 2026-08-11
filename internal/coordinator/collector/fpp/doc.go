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
// FPP 9.5.3 (see testdata and fpp_test.go's
// TestMultiSyncEnabledNeverReadsTheSettingsEndpoint), that endpoint returns
// the setting's SCHEMA, not a decoded value, and on a daemon that has never
// had the setting explicitly written, the response carries no "value" key
// at all. A Go struct with a bool "value" field decodes that body without
// error and yields false — correct today by coincidence (the default is
// also false) and silently wrong the moment MultiSync is turned on, with a
// test written against it passing the whole time it is wrong. See
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
// A live capture against a real FPP 9.5.3 daemon found that
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
// # Verification status
//
// Endpoint shapes and the field-typing hazards above are verified against a
// real FPP 9.5.3 (bench/fpp-multisync, container showmesh-bench-fpp-master)
// as of 2026-08-10 — see each testdata file and fpp_test.go /
// integration_test.go for which claims were checked against the live
// daemon versus against a captured body. This raises RES-002's L2 promotion
// (protocol semantics) not at all — RES-002 covers the MultiSync wire
// protocol, and this package speaks REST, never that wire — and says
// nothing about hardware, drift, or live-show behavior; L1 there is
// untouched.
package fpp
