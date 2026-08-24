package fpp

import (
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the regression proof for the FPP 9/10 support strategy
// this package settled on: observe the version, never branch on it, and
// keep the same decoders for both. See doc.go's "Verification status"
// section for the bench provenance of every testdata/v10-bench/*.json file
// this test drives through the package's UNCHANGED StatusSignals,
// PortSignals, and SystemInfoSignals — none of those functions, or
// anything they call, was touched to make these pass.

// TestV10BenchCapturesDecodeWithZeroCollectionFailed is the FPP 10 half of
// TestStatusSignalsRealCapturesDecodeWithZeroCollectionFailed /
// TestPortSignalsRealCapturesDecodeWithZeroCollectionFailed /
// TestSystemInfoSignalsRealCapturesDecodeWithZeroCollectionFailed: every
// real FPP 10.0 bench capture (bench/fpp-multisync, FPP_GIT_REF=10.0,
// upstream tag 10.0, commit 370e62ed7 — see testdata/v10-bench/README.md)
// must decode with no signal reporting collection_failed. A capture that
// produced one would mean this package's 9.5.3-derived decoder is wrong
// about a document a real FPP 10 daemon actually sent.
func TestV10BenchCapturesDecodeWithZeroCollectionFailed(t *testing.T) {
	assertNoCollectionFailed := func(t *testing.T, sigs []SignalValue) {
		t.Helper()
		for _, sv := range sigs {
			if sv.Absence == observation.StateCollectionFailed {
				t.Errorf("signal %q: Absence = collection_failed (reason %q), want no collection_failed signals from a real capture", sv.Signal, sv.Reason)
			}
		}
	}

	t.Run("fppd_status_idle.json", func(t *testing.T) {
		sigs, err := StatusSignals(loadTestdata(t, "v10-bench/fppd_status_idle.json"))
		if err != nil {
			t.Fatalf("StatusSignals() error = %v", err)
		}
		assertNoCollectionFailed(t, sigs)
	})
	t.Run("fppd_status_playing.json", func(t *testing.T) {
		sigs, err := StatusSignals(loadTestdata(t, "v10-bench/fppd_status_playing.json"))
		if err != nil {
			t.Fatalf("StatusSignals() error = %v", err)
		}
		assertNoCollectionFailed(t, sigs)
	})
	t.Run("fppd_ports.json", func(t *testing.T) {
		sigs, err := PortSignals(loadTestdata(t, "v10-bench/fppd_ports.json"))
		if err != nil {
			t.Fatalf("PortSignals() error = %v", err)
		}
		assertNoCollectionFailed(t, sigs)
	})
	t.Run("system_info.json", func(t *testing.T) {
		sigs, err := SystemInfoSignals(loadTestdata(t, "v10-bench/system_info.json"))
		if err != nil {
			t.Fatalf("SystemInfoSignals() error = %v", err)
		}
		assertNoCollectionFailed(t, sigs)
	})
}

// TestStatusSignalsV10RepeatModeTypeFlip is the load-bearing regression for
// decode.go's numberField tolerant-number decode, driven by two real FPP
// 10.0 captures rather than a synthetic body: fppd_status_idle.json encodes
// "repeat_mode" as the JSON string "0" and fppd_status_playing.json encodes
// it as the JSON number 0 — the exact type flip Playlist.cpp:2322 (idle)
// versus :2345 (playing) produces (confirmed against refs/tags/10.0^{},
// commit 370e62ed7). Both must decode to the same measured signal, never
// collection_failed.
func TestStatusSignalsV10RepeatModeTypeFlip(t *testing.T) {
	for _, name := range []string{"v10-bench/fppd_status_idle.json", "v10-bench/fppd_status_playing.json"} {
		t.Run(name, func(t *testing.T) {
			sigs, err := StatusSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("StatusSignals(%s) error = %v", name, err)
			}
			got := findSignalValue(t, sigs, SignalPlaylistRepeatMode)
			if got.Absence != "" {
				t.Fatalf("%s: fpp.playlist.repeat_mode Absence = %q, want a value", name, got.Absence)
			}
			if got.Value != int64(0) {
				t.Errorf("%s: fpp.playlist.repeat_mode = %v, want 0 regardless of whether FPP encoded it as a JSON string or a JSON number", name, got.Value)
			}
		})
	}
}

// TestSystemInfoSignalsDecodeMajorMinorVersion verifies
// fpp.major_version/fpp.minor_version against both supported FPP lines:
// the 9.5.3 live fleet capture and the 10.0 bench capture. These are
// observe-only signals — see fpp.go's doc comment on the constants for why
// nothing in this collector branches on the value.
func TestSystemInfoSignalsDecodeMajorMinorVersion(t *testing.T) {
	tests := []struct {
		file  string
		major int64
		minor int64
	}{
		{"live_main_system_info.json", 9, 4},
		{"v10-bench/system_info.json", 10, 0},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			sigs, err := SystemInfoSignals(loadTestdata(t, tc.file))
			if err != nil {
				t.Fatalf("SystemInfoSignals(%s) error = %v", tc.file, err)
			}
			major := findSignalValue(t, sigs, SignalMajorVersion)
			if major.Absence != "" || major.Value != tc.major {
				t.Errorf("%s: fpp.major_version = value %v absence %q, want value %d absence \"\"", tc.file, major.Value, major.Absence, tc.major)
			}
			minor := findSignalValue(t, sigs, SignalMinorVersion)
			if minor.Absence != "" || minor.Value != tc.minor {
				t.Errorf("%s: fpp.minor_version = value %v absence %q, want value %d absence \"\"", tc.file, minor.Value, minor.Absence, tc.minor)
			}
		})
	}
}

// --- Port-key omission: source-derived only, NOT a bench capture ----------
//
// testdata/v10-bench/fppd_ports.json is a real FPP 10.0 capture, but its
// bench container has no channel outputs configured, so the response is an
// empty array `[]` — the same shape an unconfigured 9.5.3 host produces
// (see testdata/v10-bench/README.md). It proves the endpoint is reachable
// and shaped as expected; it proves NOTHING about the omitted-key behavior
// this test exists to check, because it has no port elements at all.
//
// testdata/fpp10_ports_source_derived_not_captured.json is NOT a capture
// from any running FPP. It is hand-built to encode the shape
// OutputMonitor::appendTo (src/OutputMonitor.cpp, confirmed against
// refs/tags/10.0^{}, commit 370e62ed7) produces: "enabled" is written only
// when the port has an enable pin, "status" only when it has an eFuse, and
// "ma" only when it has a current sensor. Port 1 has all three; Port 2 has
// only an enable pin (the K8-Pro/K16-Pro "local outputs" case the source's
// own comment names — enabled but no eFuse to read, no current sensor);
// Port 3 has none of the three, the full omission case. This has never
// been observed on a running FPP 10 daemon by this package — see doc.go's
// "Verification status" section.
func TestPortSignalsV10SourceDerivedKeyOmission(t *testing.T) {
	sigs, err := PortSignals(loadTestdata(t, "fpp10_ports_source_derived_not_captured.json"))
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}

	assertUnsupported := func(t *testing.T, sig observation.SignalID, wantReason string) {
		t.Helper()
		sv := findSignalValue(t, sigs, sig)
		if sv.Absence != observation.StateUnsupported {
			t.Errorf("%s: Absence = %q, want unsupported", sig, sv.Absence)
		}
		if sv.Reason != wantReason {
			t.Errorf("%s: Reason = %q, want %q", sig, sv.Reason, wantReason)
		}
	}
	assertMeasured := func(t *testing.T, sig observation.SignalID, want any) {
		t.Helper()
		sv := findSignalValue(t, sigs, sig)
		if sv.Absence != "" {
			t.Fatalf("%s: Absence = %q, want a value", sig, sv.Absence)
		}
		if sv.Value != want {
			t.Errorf("%s = %v, want %v", sig, sv.Value, want)
		}
	}

	// Port 1: enable pin, eFuse, and current sensor all configured — every
	// key present as an ordinary value, unaffected by the FPP 10 change.
	assertMeasured(t, "fpp.port.port_1.enabled", true)
	assertMeasured(t, "fpp.port.port_1.status", true)
	assertMeasured(t, "fpp.port.port_1.current_ma", float64(0))

	// Port 2: enable pin only (the K8-Pro/K16-Pro local-outputs case) —
	// "enabled" is an ordinary value, "status" and "ma" are Unsupported.
	assertMeasured(t, "fpp.port.port_2.enabled", true)
	assertUnsupported(t, "fpp.port.port_2.status", "this port has no eFuse")
	assertUnsupported(t, "fpp.port.port_2.current_ma", "this port has no current sensor")

	// Port 3: no enable pin, no eFuse, no current sensor — every one of
	// the three keys is Unsupported, naming what is actually absent, never
	// collection_failed and never a fabricated value.
	assertUnsupported(t, "fpp.port.port_3.enabled", "this port has no enable pin")
	assertUnsupported(t, "fpp.port.port_3.status", "this port has no eFuse")
	assertUnsupported(t, "fpp.port.port_3.current_ma", "this port has no current sensor")

	for _, sv := range sigs {
		if sv.Absence == observation.StateCollectionFailed {
			t.Errorf("signal %q: Absence = collection_failed (reason %q), want no collection_failed from this fixture", sv.Signal, sv.Reason)
		}
	}
}
