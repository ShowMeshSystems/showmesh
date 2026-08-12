package fpp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file tests signals.go's decode surface (StatusSignals, PortSignals,
// SystemInfoSignals) directly against the real fleet captures in
// testdata/live_*.json — main (FPP-Main, Pi 3 B+, player mode, FPP 9.4,
// ports []), remote01 (FPP-remote-01, K16A-B, remote mode, master build,
// 32 port elements), and remote04 (FPP-remote-04, K16-Max, remote mode,
// FPP 9.4, 48 port elements, no "warnings" key) — captured read-only from
// the operator's live fleet on 2026-08-11. Contract section 7: nothing
// here may claim more than that — see each test's own doc comment for
// exactly what it does and does not establish.

// findSignalValue returns the SignalValue for sig, failing the test if it
// is not present exactly once.
func findSignalValue(t *testing.T, sigs []SignalValue, sig observation.SignalID) SignalValue {
	t.Helper()
	var found []SignalValue
	for _, sv := range sigs {
		if sv.Signal == sig {
			found = append(found, sv)
		}
	}
	if len(found) != 1 {
		t.Fatalf("signal %q appeared %d times in decode result, want exactly 1", sig, len(found))
	}
	return found[0]
}

// --- StatusSignals against every real capture ------------------------------

// TestStatusSignalsRealCapturesDecodeWithZeroCollectionFailed is the
// contract section 3.6 acceptance test: every one of the three real
// fppd_status captures must decode with no signal reporting
// collection_failed. A capture that produced one would mean this
// package's decoder is wrong about a document a real FPP actually sent —
// "if a real captured document produces a decode failure, the decoder is
// wrong, not the document."
func TestStatusSignalsRealCapturesDecodeWithZeroCollectionFailed(t *testing.T) {
	for _, name := range []string{
		"live_main_fppd_status.json",
		"live_remote01_fppd_status.json",
		"live_remote04_fppd_status.json",
	} {
		t.Run(name, func(t *testing.T) {
			sigs, err := StatusSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("StatusSignals(%s) error = %v", name, err)
			}
			for _, sv := range sigs {
				if sv.Absence == observation.StateCollectionFailed {
					t.Errorf("signal %q: Absence = collection_failed (reason %q), want no collection_failed signals from a real capture", sv.Signal, sv.Reason)
				}
			}
		})
	}
}

// TestPortSignalsRealCapturesDecodeWithZeroCollectionFailed is the ports
// half of the same acceptance test: main's empty array, remote01's 32
// elements, and remote04's 48 elements must all decode with no
// collection_failed signal (including no fpp.ports.decode_failed — every
// element's "name" is present and unique on all three captures).
func TestPortSignalsRealCapturesDecodeWithZeroCollectionFailed(t *testing.T) {
	for _, name := range []string{
		"live_main_fppd_ports.json",
		"live_remote01_fppd_ports.json",
		"live_remote04_fppd_ports.json",
	} {
		t.Run(name, func(t *testing.T) {
			sigs, err := PortSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("PortSignals(%s) error = %v", name, err)
			}
			for _, sv := range sigs {
				if sv.Absence == observation.StateCollectionFailed {
					t.Errorf("signal %q: Absence = collection_failed (reason %q), want no collection_failed signals from a real capture", sv.Signal, sv.Reason)
				}
			}
		})
	}
}

// TestSystemInfoSignalsRealCapturesDecodeWithZeroCollectionFailed is the
// system/info half.
func TestSystemInfoSignalsRealCapturesDecodeWithZeroCollectionFailed(t *testing.T) {
	for _, name := range []string{
		"live_main_system_info.json",
		"live_remote01_system_info.json",
		"live_remote04_system_info.json",
	} {
		t.Run(name, func(t *testing.T) {
			sigs, err := SystemInfoSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("SystemInfoSignals(%s) error = %v", name, err)
			}
			for _, sv := range sigs {
				if sv.Absence == observation.StateCollectionFailed {
					t.Errorf("signal %q: Absence = collection_failed (reason %q), want no collection_failed signals from a real capture", sv.Signal, sv.Reason)
				}
			}
		})
	}
}

// --- Mode-dependent absence (contract section 3.3) -------------------------

// TestRemoteModeAbsenceIsUnsupportedNotCollectionFailed is the contract
// section 3.3 bug-fix regression: on a real remote-mode capture, every
// signal whose source lives under current_playlist, scheduler, or
// top-level repeat_mode must be Unsupported, naming "remote" mode, never
// collection_failed. Before this step, the collector reported
// collection_failed for these on a remote host — nothing had failed.
func TestRemoteModeAbsenceIsUnsupportedNotCollectionFailed(t *testing.T) {
	for _, name := range []string{"live_remote01_fppd_status.json", "live_remote04_fppd_status.json"} {
		t.Run(name, func(t *testing.T) {
			sigs, err := StatusSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("StatusSignals(%s) error = %v", name, err)
			}

			playerOnly := []observation.SignalID{
				SignalPlaylistRepeatMode, SignalPlaylistIndex, SignalPlaylistCount,
				SignalPlaylistType, SignalSchedulerEnabled, SignalSchedulerStatus,
				SignalSchedulerNextPlaylist, SignalSchedulerNextStartTime,
			}
			for _, sig := range playerOnly {
				got := findSignalValue(t, sigs, sig)
				if got.Absence != observation.StateUnsupported {
					t.Errorf("%s: signal %q Absence = %q, want unsupported (this host is in remote mode)", name, sig, got.Absence)
					continue
				}
				if got.Reason == "" || !contains(got.Reason, "remote") {
					t.Errorf("%s: signal %q Reason = %q, want it to name the remote mode", name, sig, got.Reason)
				}
			}

			// The inverse must hold too: remote-only fields are real
			// values here, not unsupported, because this host IS in
			// remote mode.
			for _, sig := range []observation.SignalID{SignalMediaFilename, SignalPositionElapsedSeconds} {
				got := findSignalValue(t, sigs, sig)
				if got.Absence != "" {
					t.Errorf("%s: signal %q Absence = %q, want a value (this host IS in remote mode)", name, sig, got.Absence)
				}
			}
		})
	}
}

// TestPlayerModeRemoteOnlyFieldsAreUnsupported is the symmetric case: on
// FPP-Main (player mode), fpp.media.filename and
// fpp.position.elapsed.seconds are the ones that are absent, and their
// absence must be explained by mode too.
func TestPlayerModeRemoteOnlyFieldsAreUnsupported(t *testing.T) {
	sigs, err := StatusSignals(loadTestdata(t, "live_main_fppd_status.json"))
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	for _, sig := range []observation.SignalID{SignalMediaFilename, SignalPositionElapsedSeconds} {
		got := findSignalValue(t, sigs, sig)
		if got.Absence != observation.StateUnsupported {
			t.Errorf("signal %q Absence = %q, want unsupported (FPP-Main is in player mode)", sig, got.Absence)
			continue
		}
		if got.Reason == "" || !contains(got.Reason, "player") {
			t.Errorf("signal %q Reason = %q, want it to name the player mode", sig, got.Reason)
		}
	}

	// And the player-only fields are real values on FPP-Main.
	for _, sig := range []observation.SignalID{SignalSchedulerStatus, SignalSchedulerEnabled, SignalPlaylistRepeatMode} {
		got := findSignalValue(t, sigs, sig)
		if got.Absence != "" {
			t.Errorf("signal %q Absence = %q, want a value (FPP-Main IS in player mode)", sig, got.Absence)
		}
	}
}

// TestSchedulerNextPlaylistIsMeasuredSentenceNotInterpreted verifies
// contract section 3.3's specific instruction: FPP's "No playlist
// scheduled." string is reported as-is, a Measured value, never
// interpreted or rewritten by this package.
func TestSchedulerNextPlaylistIsMeasuredSentenceNotInterpreted(t *testing.T) {
	sigs, err := StatusSignals(loadTestdata(t, "live_main_fppd_status.json"))
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, SignalSchedulerNextPlaylist)
	if got.Absence != "" {
		t.Fatalf("signal %q Absence = %q, want a value", SignalSchedulerNextPlaylist, got.Absence)
	}
	if got.Value != "No playlist scheduled." {
		t.Errorf("signal %q value = %v, want the literal FPP sentence", SignalSchedulerNextPlaylist, got.Value)
	}
}

// --- warnings (contract section 3.4) ----------------------------------------

// TestWarningsAbsentKeyIsUnsupported verifies the live evidence at the
// center of contract section 3.4: FPP-remote-04's real /api/fppd/status
// capture has no "warnings" key at all (confirmed against FPP's own
// source — see this package's doc comment), and this package must report
// that as Unsupported, never a fabricated zero count and never
// collection_failed.
func TestWarningsAbsentKeyIsUnsupported(t *testing.T) {
	body := loadTestdata(t, "live_remote04_fppd_status.json")
	if bytesContains(body, `"warnings"`) {
		t.Fatalf("test fixture unexpectedly contains a \"warnings\" key; this test assumes FPP-remote-04's real capture omits it entirely")
	}

	sigs, err := StatusSignals(body)
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	for _, sig := range []observation.SignalID{SignalWarningsCount, SignalWarningsSummary} {
		got := findSignalValue(t, sigs, sig)
		if got.Absence != observation.StateUnsupported {
			t.Errorf("signal %q Absence = %q, want unsupported", sig, got.Absence)
		}
		if got.Reason == "" {
			t.Errorf("signal %q Reason is empty, want an explanation", sig)
		}
	}
}

// TestWarningsPresentArrayIsMeasured verifies the other half: FPP-Main and
// FPP-remote-01 both carry a populated "warnings" array in their real
// captures, and this package must report the count and a joined summary
// as real, current values.
func TestWarningsPresentArrayIsMeasured(t *testing.T) {
	tests := []struct {
		file      string
		wantCount int64
	}{
		{"live_main_fppd_status.json", 1},
		{"live_remote01_fppd_status.json", 3},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			sigs, err := StatusSignals(loadTestdata(t, tt.file))
			if err != nil {
				t.Fatalf("StatusSignals() error = %v", err)
			}
			count := findSignalValue(t, sigs, SignalWarningsCount)
			if count.Absence != "" {
				t.Fatalf("fpp.warnings.count Absence = %q, want a value", count.Absence)
			}
			if count.Value != tt.wantCount {
				t.Errorf("fpp.warnings.count = %v, want %v", count.Value, tt.wantCount)
			}
			summary := findSignalValue(t, sigs, SignalWarningsSummary)
			if summary.Absence != "" {
				t.Errorf("fpp.warnings.summary Absence = %q, want a value", summary.Absence)
			}
			if summary.Value == "" {
				t.Errorf("fpp.warnings.summary is empty, want the joined warning text")
			}
		})
	}
}

// --- ports: the three absences (contract section 3.2) ----------------------

// TestPortsEmptyArrayIsMeasuredZeroNotAbsence verifies FPP-Main's real
// /api/fppd/ports capture ("[]", a Pi with no pixel output cape): count
// and blind_count must both be Measured 0, and there must be no per-port
// signals and no fpp.ports.decode_failed signal at all — zero ports is a
// true statement, not something to explain away with a synthesized
// absence.
func TestPortsEmptyArrayIsMeasuredZeroNotAbsence(t *testing.T) {
	body := loadTestdata(t, "live_main_fppd_ports.json")
	sigs, err := PortSignals(body)
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}

	count := findSignalValue(t, sigs, SignalPortsCount)
	if count.Absence != "" || count.Value != int64(0) {
		t.Errorf("fpp.ports.count = value %v absence %q, want value 0 absence \"\"", count.Value, count.Absence)
	}
	blind := findSignalValue(t, sigs, SignalPortsBlindCount)
	if blind.Absence != "" || blind.Value != int64(0) {
		t.Errorf("fpp.ports.blind_count = value %v absence %q, want value 0 absence \"\"", blind.Value, blind.Absence)
	}

	for _, sv := range sigs {
		if sv.Signal == SignalPortsDecodeFailed {
			t.Errorf("unexpected fpp.ports.decode_failed signal for a genuinely empty ports array: reason %q", sv.Reason)
		}
		if sv.Signal != SignalPortsCount && sv.Signal != SignalPortsBlindCount {
			t.Errorf("unexpected per-port signal %q from an empty ports array", sv.Signal)
		}
	}
	if len(sigs) != 2 {
		t.Errorf("PortSignals(empty array) returned %d signals, want exactly 2 (count, blind_count)", len(sigs))
	}
}

// TestSmartReceiverPositionNeverReportsCurrent is this step's load-bearing
// test (contract section 3.2 and section 7): decode the real
// remote01/remote04 ports captures and assert that no observation for a
// smart-receiver key ever carries a non-nil current-mA Value. This proves
// the absence rule, not that pixel-current monitoring works — every "ma"
// this session has seen reads 0 with the display de-energized (contract
// section 7), which this test does not touch and does not claim.
func TestSmartReceiverPositionNeverReportsCurrent(t *testing.T) {
	for _, name := range []string{"live_remote01_fppd_ports.json", "live_remote04_fppd_ports.json"} {
		t.Run(name, func(t *testing.T) {
			sigs, err := PortSignals(loadTestdata(t, name))
			if err != nil {
				t.Fatalf("PortSignals(%s) error = %v", name, err)
			}

			sawSmartReceiver := false
			for _, sv := range sigs {
				if !hasSuffix(string(sv.Signal), ".kind") || sv.Value != "smart_receiver" {
					continue
				}
				sawSmartReceiver = true
				key := trimSuffix(string(sv.Signal), ".kind")
				currentSig := findSignalValue(t, sigs, observation.SignalID(key+".current_ma"))
				if currentSig.Value != nil {
					t.Errorf("%s: %s carries Value = %v (%T), want nil — a smart-receiver position must never report a current reading",
						name, currentSig.Signal, currentSig.Value, currentSig.Value)
				}
				if currentSig.Absence != observation.StateUnsupported {
					t.Errorf("%s: %s Absence = %q, want unsupported", name, currentSig.Signal, currentSig.Absence)
				}
			}
			if !sawSmartReceiver {
				t.Fatalf("test bug: %s produced no smart_receiver-kind port signals; this test cannot prove anything without at least one", name)
			}
		})
	}
}

// TestPortPixelCountAlwaysUnsupportedOnLiveCaptures verifies contract
// section 3.2's third absence rule against every real port element this
// session captured, output and smart-receiver alike: pixelCount is absent
// from all of them, and every fpp.port.<key>.pixel_count signal must be
// Unsupported with the exact reason contract section 3.2 specifies —
// never a fabricated 0.
func TestPortPixelCountAlwaysUnsupportedOnLiveCaptures(t *testing.T) {
	const wantReason = "pixelCount absent from this FPP's port document; the pixel-count operation has never been run on this host"

	for _, name := range []string{"live_remote01_fppd_ports.json", "live_remote04_fppd_ports.json"} {
		sigs, err := PortSignals(loadTestdata(t, name))
		if err != nil {
			t.Fatalf("PortSignals(%s) error = %v", name, err)
		}
		sawPixelCount := false
		for _, sv := range sigs {
			if !hasSuffix(string(sv.Signal), ".pixel_count") {
				continue
			}
			sawPixelCount = true
			if sv.Absence != observation.StateUnsupported {
				t.Errorf("%s: %s Absence = %q, want unsupported", name, sv.Signal, sv.Absence)
			}
			if sv.Reason != wantReason {
				t.Errorf("%s: %s Reason = %q, want %q", name, sv.Signal, sv.Reason, wantReason)
			}
		}
		if !sawPixelCount {
			t.Fatalf("test bug: %s produced no pixel_count signals", name)
		}
	}
}

// TestPortKeyDerivedFromNameNotIndex verifies contract section 3.1's key
// derivation rule against a real capture: "Port 1" normalizes to
// "port_1", and the resulting signal must actually be present.
func TestPortKeyDerivedFromNameNotIndex(t *testing.T) {
	sigs, err := PortSignals(loadTestdata(t, "live_remote01_fppd_ports.json"))
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, "fpp.port.port_1.kind")
	if got.Value != "output" {
		t.Errorf("fpp.port.port_1.kind = %v, want \"output\"", got.Value)
	}
}

// TestPortsOutputElementCurrentIsMeasuredNumber verifies the ordinary,
// non-absence path: an output element's "ma" (0 in every live capture,
// per contract section 7 — the display is de-energized) decodes as a
// Measured float64 with unit "milliamps", never an absence.
func TestPortsOutputElementCurrentIsMeasuredNumber(t *testing.T) {
	sigs, err := PortSignals(loadTestdata(t, "live_remote01_fppd_ports.json"))
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, "fpp.port.port_1.current_ma")
	if got.Absence != "" {
		t.Fatalf("fpp.port.port_1.current_ma Absence = %q, want a value", got.Absence)
	}
	if got.Value != float64(0) {
		t.Errorf("fpp.port.port_1.current_ma = %v, want 0 (the display was de-energized when this was captured)", got.Value)
	}
	if got.Unit != "milliamps" {
		t.Errorf("fpp.port.port_1.current_ma Unit = %q, want \"milliamps\"", got.Unit)
	}
}

// TestPortsDecodeFailedNamesDuplicateKey verifies the "two elements
// normalize to the same key" branch of contract section 3.1: derived from
// the real remote-01 capture with element 1's name mutated to collide
// with element 0's, per the Step 5 spec's "derive it from a capture ...
// rather than authoring a new body from scratch."
func TestPortsDecodeFailedNamesDuplicateKey(t *testing.T) {
	body := mutatePortsElementField(t, loadTestdata(t, "live_remote01_fppd_ports.json"), 1, "name", "Port 1")

	sigs, err := PortSignals(body)
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}

	failed := findSignalValue(t, sigs, SignalPortsDecodeFailed)
	if failed.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.ports.decode_failed Absence = %q, want collection_failed", failed.Absence)
	}
	if failed.Reason == "" {
		t.Errorf("fpp.ports.decode_failed Reason is empty, want the problem named")
	}

	// Contract section 3.1: "no port signals for it" — the colliding key
	// must receive NO port signals at all, not even from the element that
	// would otherwise "win" by appearing first. Before trusting this,
	// PortSignals used a single-pass decode that emitted element 0's
	// signals immediately, before element 1's collision with it was even
	// detected; that version made this assertion fail with exactly 1 (see
	// PortSignals' own doc comment for the two-pass fix and the git history
	// of this test for the confirmation).
	kindSignals := 0
	for _, sv := range sigs {
		if sv.Signal == "fpp.port.port_1.kind" {
			kindSignals++
		}
	}
	if kindSignals != 0 {
		t.Errorf("fpp.port.port_1.kind appeared %d times, want exactly 0 (both colliding elements must be dropped, including the one that appeared first)", kindSignals)
	}
}

// --- sensors (contract section 3.5) -----------------------------------------

// TestSensorKeyNormalizationFromRealLabel verifies contract section 3.5's
// worked example against the real remote-04 capture, whose sensors
// include a label literally "K16-Max: " (trailing colon and space,
// embedded hyphen): it must normalize to "k16_max".
func TestSensorKeyNormalizationFromRealLabel(t *testing.T) {
	sigs, err := StatusSignals(loadTestdata(t, "live_remote04_fppd_status.json"))
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, "fpp.sensor.k16_max.type")
	if got.Value != "Temperature" {
		t.Errorf("fpp.sensor.k16_max.type = %v, want \"Temperature\"", got.Value)
	}
}

// TestSensorValueNeverCarriesAUnit verifies contract section 3.5's "do not
// claim a unit" rule: fpp.sensor.<key>.value must have an empty Unit even
// for a Voltage-typed sensor (remote-01's VIN1..VIN4), where a unit would
// be tempting to hard-code.
func TestSensorValueNeverCarriesAUnit(t *testing.T) {
	sigs, err := StatusSignals(loadTestdata(t, "live_remote01_fppd_status.json"))
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	value := findSignalValue(t, sigs, "fpp.sensor.vin1.value")
	if value.Unit != "" {
		t.Errorf("fpp.sensor.vin1.value Unit = %q, want empty — FPP does not state a voltage/temperature unit and this package must not guess one", value.Unit)
	}
	typ := findSignalValue(t, sigs, "fpp.sensor.vin1.type")
	if typ.Value != "Voltage" {
		t.Errorf("fpp.sensor.vin1.type = %v, want \"Voltage\"", typ.Value)
	}
}

// --- system/info -------------------------------------------------------

// TestSystemInfoRealCapturesValues spot-checks a handful of platform
// signals against known values from the real captures — enough to catch a
// field-name typo, not a claim that every field is exhaustively checked
// elsewhere (TestSystemInfoSignalsRealCapturesDecodeWithZeroCollectionFailed
// already covers "nothing fails to decode").
func TestSystemInfoRealCapturesValues(t *testing.T) {
	sigs, err := SystemInfoSignals(loadTestdata(t, "live_remote04_system_info.json"))
	if err != nil {
		t.Fatalf("SystemInfoSignals() error = %v", err)
	}

	platform := findSignalValue(t, sigs, SignalPlatform)
	if platform.Value != "BeagleBone 64" {
		t.Errorf("fpp.platform = %v, want \"BeagleBone 64\"", platform.Value)
	}
	osVersion := findSignalValue(t, sigs, SignalOSVersion)
	if osVersion.Value != "v2025-11" {
		t.Errorf("fpp.os.version = %v, want \"v2025-11\"", osVersion.Value)
	}
	diskFree := findSignalValue(t, sigs, SignalDiskMediaFree)
	if diskFree.Absence != "" {
		t.Fatalf("fpp.disk.media.free_bytes Absence = %q, want a value", diskFree.Absence)
	}
	if diskFree.Value != int64(115468398592) {
		t.Errorf("fpp.disk.media.free_bytes = %v, want 115468398592", diskFree.Value)
	}
	if diskFree.Unit != "bytes" {
		t.Errorf("fpp.disk.media.free_bytes Unit = %q, want \"bytes\"", diskFree.Unit)
	}
}

// --- End-to-end Poll against every real capture, including ports/system-info ---

// TestPollEndToEndAgainstEachRealHost drives the full Collector.Poll (not
// just the pure decode functions) with all four endpoints served from one
// host's real captures, for all three hosts, and checks the four
// endpoint-independent aggregate facts: fpp.reachable is true, and every
// signal source is "fpp-rest". This is the integration point between
// signals.go's pure decoding and fpp.go's Poll/toObservation stamping.
func TestPollEndToEndAgainstEachRealHost(t *testing.T) {
	hosts := []struct {
		name   string
		status string
		ports  string
		info   string
	}{
		{"main", "live_main_fppd_status.json", "live_main_fppd_ports.json", "live_main_system_info.json"},
		{"remote01", "live_remote01_fppd_status.json", "live_remote01_fppd_ports.json", "live_remote01_system_info.json"},
		{"remote04", "live_remote04_fppd_status.json", "live_remote04_fppd_ports.json", "live_remote04_system_info.json"},
	}

	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			srv := newFPPServer()
			srv.serveBody("/api/fppd/status", loadTestdata(t, h.status))
			srv.serveBody("/api/fppd/ports", loadTestdata(t, h.ports))
			srv.serveBody("/api/system/info", loadTestdata(t, h.info))
			srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
			ts := srv.start(t)

			now := time.Now()
			c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
			obs, _ := c.Poll(t.Context())

			reachable := findSignal(t, obs, SignalReachable)
			if reachable.Value != true {
				t.Fatalf("fpp.reachable = %v, want true", reachable.Value)
			}

			for _, o := range obs {
				if o.Source != sourceName {
					t.Errorf("signal %q Source = %q, want %q", o.Signal, o.Source, sourceName)
				}
			}

			// Every static signal in AllSignals (minus SignalReachable,
			// already checked, and SignalMultiSyncSystems, served from an
			// unrelated fixture on purpose) must appear exactly once.
			for _, sig := range AllSignals {
				if sig == SignalReachable || sig == SignalMultiSyncSystems || sig == SignalPortsDecodeFailed {
					// SignalPortsDecodeFailed is conditional — only
					// emitted when PortSignals actually finds a problem
					// (see PortSignals), which none of these real,
					// well-formed captures have.
					continue
				}
				findSignal(t, obs, sig) // fails the test if not exactly 1
			}

			srv.assertOnlyGET(t)
			srv.assertExactPathSet(t, "/api/fppd/status", "/api/fppd/multiSyncSystems", "/api/fppd/ports", "/api/system/info")
		})
	}
}

// --- helpers -----------------------------------------------------------

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func trimSuffix(s, suffix string) string {
	if hasSuffix(s, suffix) {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func bytesContains(body []byte, substr string) bool {
	return contains(string(body), substr)
}

// mutatePortsElementField returns a copy of a /api/fppd/ports body (a
// top-level JSON array) with element index's key field replaced by
// newValue. Mirrors mutateJSONField's role for the object-shaped status
// bodies, adapted for the array shape /api/fppd/ports actually has.
func mutatePortsElementField(t *testing.T, body []byte, index int, key string, newValue any) []byte {
	t.Helper()
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("mutatePortsElementField: base body is not a JSON array of objects: %v", err)
	}
	if index < 0 || index >= len(arr) {
		t.Fatalf("mutatePortsElementField: index %d out of range (array has %d elements)", index, len(arr))
	}
	raw, err := json.Marshal(newValue)
	if err != nil {
		t.Fatalf("mutatePortsElementField: marshal newValue: %v", err)
	}
	arr[index][key] = raw
	out, err := json.Marshal(arr)
	if err != nil {
		t.Fatalf("mutatePortsElementField: marshal mutated array: %v", err)
	}
	return out
}
