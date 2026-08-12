package fppmqtt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	return body
}

func findSignalValue(t *testing.T, values []fpp.SignalValue, sig observation.SignalID) fpp.SignalValue {
	t.Helper()
	var found []fpp.SignalValue
	for _, v := range values {
		if v.Signal == sig {
			found = append(found, v)
		}
	}
	if len(found) != 1 {
		t.Fatalf("signal %q appeared %d times, want exactly 1 (all: %+v)", sig, len(found), values)
	}
	return found[0]
}

// --- decodeStatusTopic (fpp.StatusSignals, filtered) against real captures -

// TestStatusSignalsNoCollectionFailedOnRealCaptures is this package's
// version of the Step 3 rule fpp_test.go established: "if a real captured
// document produces a decode failure, the decoder is wrong, not the
// document." All three real fppd_status captures (player-mode FPP-Main and
// FPP-01, remote-mode FPP-remote-01) must decode with every field either
// measured or a mode-explained Unsupported — never collection_failed.
func TestStatusSignalsNoCollectionFailedOnRealCaptures(t *testing.T) {
	for _, name := range []string{
		"FPP-Main_fppd_status.json",
		"FPP-remote-01_fppd_status.json",
		"FPP-01_fppd_status.json",
	} {
		t.Run(name, func(t *testing.T) {
			values, err := decodeStatusTopic(readTestdata(t, name))
			if err != nil {
				t.Fatalf("decodeStatusTopic() error = %v, want nil", err)
			}
			for _, v := range values {
				if v.Absence == observation.StateCollectionFailed {
					t.Errorf("signal %q = collection_failed (reason %q), want a real capture to never produce this", v.Signal, v.Reason)
				}
			}
		})
	}
}

// TestStatusSignalsStayWithinStatusStaticSignals proves topics.go's
// statusStaticSignals list (used for the "topic never delivered a message"
// and "connection down" fallback paths) actually covers every non-sensor
// signal decodeStatusTopic can produce, across all three real mode shapes.
// Referenced by topics.go's doc comment.
func TestStatusSignalsStayWithinStatusStaticSignals(t *testing.T) {
	known := make(map[observation.SignalID]bool, len(statusStaticSignals))
	for _, sig := range statusStaticSignals {
		known[sig] = true
	}

	for _, name := range []string{
		"FPP-Main_fppd_status.json",
		"FPP-remote-01_fppd_status.json",
		"FPP-01_fppd_status.json",
	} {
		values, err := decodeStatusTopic(readTestdata(t, name))
		if err != nil {
			t.Fatalf("%s: decodeStatusTopic() error = %v", name, err)
		}
		for _, v := range values {
			if isSensorSignal(v.Signal) {
				continue
			}
			if !known[v.Signal] {
				t.Errorf("%s: decodeStatusTopic produced %q, which is not in statusStaticSignals", name, v.Signal)
			}
		}
	}
}

func isSensorSignal(sig observation.SignalID) bool {
	s := string(sig)
	return len(s) > len("fpp.sensor.") && s[:len("fpp.sensor.")] == "fpp.sensor."
}

// TestStatusSignalsPlayerModeValues spot-checks specific values against
// testdata/FPP-Main_fppd_status.json (player mode, idle, nothing
// scheduled) rather than only checking "no failure" — a decoder that
// silently produced the WRONG value for every field would still pass the
// no-collection-failed test above.
func TestStatusSignalsPlayerModeValues(t *testing.T) {
	values, err := decodeStatusTopic(readTestdata(t, "FPP-Main_fppd_status.json"))
	if err != nil {
		t.Fatalf("decodeStatusTopic() error = %v", err)
	}

	cases := []struct {
		sig  observation.SignalID
		want any
	}{
		{SignalMode, "player"},
		{SignalPlaylistName, ""}, // current_playlist.playlist == ""
		{SignalSequenceName, ""},
		// fpp.StatusSignals decodes seconds_played via its generic
		// numberSignalValue (float64), not an integer-only field — see
		// fpp/signals.go's StatusSignals. This is the canonical decoder's
		// actual type, which fppmqtt's now-deleted independent copy got
		// wrong (int64): exactly the kind of silent drift fold-in exists
		// to close.
		{SignalPositionSeconds, float64(0)},
		{SignalMultiSyncEnabled, true},
		{SignalSchedulerStatus, "idle"},
		{SignalFppdState, "running"},
		{SignalPowerBad, false},
		{SignalHostName, "FPP-Main"},
		{SignalMQTTConfigured, true},
		{SignalMQTTConnected, true},
		// scheduler.enabled arrives as the NUMBER 1 (contract section
		// 3.1); this is the field boolFromNumberOrBool exists for.
		{SignalSchedulerEnabled, true},
		// scheduler.nextPlaylist.playlistName is the human sentence "No
		// playlist scheduled." — a Measured string value, per contract
		// section 3.3, not interpreted by the collector.
		{SignalSchedulerNextPlaylist, "No playlist scheduled."},
		{SignalSchedulerNextStartTime, int64(0)},
	}
	for _, tc := range cases {
		got := findSignalValue(t, values, tc.sig)
		if got.Absence != "" {
			t.Errorf("signal %q: Absence = %q (reason %q), want a measured value %v", tc.sig, got.Absence, got.Reason, tc.want)
			continue
		}
		if got.Value != tc.want {
			t.Errorf("signal %q = %#v, want %#v", tc.sig, got.Value, tc.want)
		}
	}

	// Player-mode host: the remote-only fields must be Unsupported, never
	// silently absent and never collection_failed.
	for _, sig := range []observation.SignalID{SignalMediaFilename, SignalPositionElapsedSeconds} {
		got := findSignalValue(t, values, sig)
		if got.Absence != observation.StateUnsupported {
			t.Errorf("signal %q on a player-mode host: Absence = %q, want %q", sig, got.Absence, observation.StateUnsupported)
		}
	}
}

// TestStatusSignalsRemoteModeValues is the remote-mode mirror of
// TestStatusSignalsPlayerModeValues, against
// testdata/FPP-remote-01_fppd_status.json, which has NO current_playlist
// or scheduler object at all (verified: absent wholesale, not
// present-but-empty).
func TestStatusSignalsRemoteModeValues(t *testing.T) {
	values, err := decodeStatusTopic(readTestdata(t, "FPP-remote-01_fppd_status.json"))
	if err != nil {
		t.Fatalf("decodeStatusTopic() error = %v", err)
	}

	cases := []struct {
		sig  observation.SignalID
		want any
	}{
		{SignalMode, "remote"},
		{SignalPlaylistName, ""}, // falls back to top-level "playlist"
		{SignalSequenceName, ""},
		{SignalMultiSyncEnabled, false},
		{SignalHostName, "FPP-remote-01"},
		{SignalMediaFilename, ""},
		{SignalPositionElapsedSeconds, int64(0)},
	}
	for _, tc := range cases {
		got := findSignalValue(t, values, tc.sig)
		if got.Absence != "" {
			t.Errorf("signal %q: Absence = %q (reason %q), want a measured value %v", tc.sig, got.Absence, got.Reason, tc.want)
			continue
		}
		if got.Value != tc.want {
			t.Errorf("signal %q = %#v, want %#v", tc.sig, got.Value, tc.want)
		}
	}

	// Remote-mode host: the player-only fields must all be Unsupported,
	// each naming the mode (contract section 3.3's exact requirement).
	for _, sig := range []observation.SignalID{
		SignalSchedulerStatus, SignalPlaylistCount, SignalPlaylistType,
		SignalSchedulerEnabled, SignalSchedulerNextPlaylist, SignalSchedulerNextStartTime,
	} {
		got := findSignalValue(t, values, sig)
		if got.Absence != observation.StateUnsupported {
			t.Errorf("signal %q on a remote-mode host: Absence = %q, want %q", sig, got.Absence, observation.StateUnsupported)
		}
		if got.Reason == "" {
			t.Errorf("signal %q: Unsupported with empty Reason", sig)
		}
	}
}

// TestStatusSignalsSensorKeysNormalized verifies contract section 3.5's
// exact normalization examples against testdata/FPP-remote-01_fppd_status.json
// ("VIN1: " -> "vin1") and testdata/FPP-Main_fppd_status.json ("CPU: " ->
// "cpu"), and that no unit is claimed on the value signal (only "type"
// carries "Temperature"/"Voltage" — never a guessed scale).
func TestStatusSignalsSensorKeysNormalized(t *testing.T) {
	values, err := decodeStatusTopic(readTestdata(t, "FPP-Main_fppd_status.json"))
	if err != nil {
		t.Fatalf("decodeStatusTopic() error = %v", err)
	}
	v := findSignalValue(t, values, sensorSignalValue("cpu"))
	if v.Unit != "" {
		t.Errorf("fpp.sensor.cpu.value Unit = %q, want empty (no guessed scale)", v.Unit)
	}
	typ := findSignalValue(t, values, sensorSignalType("cpu"))
	if typ.Value != "Temperature" {
		t.Errorf("fpp.sensor.cpu.type = %#v, want %q", typ.Value, "Temperature")
	}

	values, err = decodeStatusTopic(readTestdata(t, "FPP-remote-01_fppd_status.json"))
	if err != nil {
		t.Fatalf("decodeStatusTopic() error = %v", err)
	}
	_ = findSignalValue(t, values, sensorSignalValue("vin1"))
	vin1Type := findSignalValue(t, values, sensorSignalType("vin1"))
	if vin1Type.Value != "Voltage" {
		t.Errorf("fpp.sensor.vin1.type = %#v, want %q", vin1Type.Value, "Voltage")
	}
}

// --- fpp.PortSignals against real captures ------------------------------

// TestPortSignalsEmptyArrayIsMeasuredZero covers testdata/FPP-01_port_status.json
// ("[]"): contract section 3.2, "an empty ports array is a measured fact,
// not an absence."
func TestPortSignalsEmptyArrayIsMeasuredZero(t *testing.T) {
	values, err := fpp.PortSignals(readTestdata(t, "FPP-01_port_status.json"))
	if err != nil {
		t.Fatalf("fpp.PortSignals() error = %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("fpp.PortSignals(empty array) = %+v, want exactly count+blind_count", values)
	}
	count := findSignalValue(t, values, SignalPortsCount)
	if count.Value != int64(0) || count.Absence != "" {
		t.Errorf("fpp.ports.count = %+v, want measured 0", count)
	}
	blind := findSignalValue(t, values, SignalPortsBlindCount)
	if blind.Value != int64(0) || blind.Absence != "" {
		t.Errorf("fpp.ports.blind_count = %+v, want measured 0", blind)
	}
}

// TestSmartReceiverPositionNeverReportsCurrent decodes the real
// FPP-remote-04 capture (48 elements, 16 output + 32 smart-receiver) and
// proves NO observation for a smart-receiver key ever carries a non-nil
// Value for current_ma — the exact test contract section 3.2 asks for by
// name, and the strongest form of "a missing ma is never 0."
//
// Before trusting this test, the behavior it names was broken (current_ma
// fell through to the same handling as an output port, decoding the
// smart-receiver's absent "ma" key as a failure rather than an explicit
// Unsupported) and confirmed to make this test fail; see the Step 5 Seam B
// report for that verification.
func TestSmartReceiverPositionNeverReportsCurrent(t *testing.T) {
	values, err := fpp.PortSignals(readTestdata(t, "FPP-remote-04_port_status.json"))
	if err != nil {
		t.Fatalf("fpp.PortSignals() error = %v", err)
	}

	smartReceiverCount := 0
	for _, v := range values {
		sig := string(v.Signal)
		if len(sig) < len(".current_ma") || sig[len(sig)-len(".current_ma"):] != ".current_ma" {
			continue
		}
		// Every current_ma signal produced for a smart-receiver-shaped key
		// (port_17 through port_48 on this capture) must carry Value ==
		// nil. Distinguish by checking the paired .kind signal.
		key := sig[len("fpp.port.") : len(sig)-len(".current_ma")]
		kind := findSignalValue(t, values, portSignalKind(key))
		if kind.Value != "smart_receiver" {
			continue
		}
		smartReceiverCount++
		if v.Value != nil {
			t.Errorf("port %q (smart receiver): current_ma Value = %#v, want nil (never a measured current)", key, v.Value)
		}
		if v.Absence != observation.StateUnsupported {
			t.Errorf("port %q (smart receiver): current_ma Absence = %q, want %q", key, v.Absence, observation.StateUnsupported)
		}
	}
	if smartReceiverCount != 32 {
		t.Fatalf("found %d smart-receiver ports, want 32 (per the reference capture's documented shape)", smartReceiverCount)
	}
}

// TestPortSignalsOutputPortsMeasured spot-checks that the 16 real output
// ports (bank Ports 1-4/5-8/17-20/21-24) on FPP-remote-04 decode as
// measured values, not absences — the mirror image of the smart-receiver
// test above.
func TestPortSignalsOutputPortsMeasured(t *testing.T) {
	values, err := fpp.PortSignals(readTestdata(t, "FPP-remote-04_port_status.json"))
	if err != nil {
		t.Fatalf("fpp.PortSignals() error = %v", err)
	}

	kind := findSignalValue(t, values, portSignalKind("port_1"))
	if kind.Value != "output" {
		t.Errorf("fpp.port.port_1.kind = %#v, want %q", kind.Value, "output")
	}
	ma := findSignalValue(t, values, portSignalCurrentMA("port_1"))
	if ma.Absence != "" {
		t.Errorf("fpp.port.port_1.current_ma: Absence = %q, want measured", ma.Absence)
	}
	if ma.Value != float64(0) {
		t.Errorf("fpp.port.port_1.current_ma = %#v, want 0 (de-energized display — see contract section 7)", ma.Value)
	}
	if ma.Unit != "milliamps" {
		t.Errorf("fpp.port.port_1.current_ma Unit = %q, want %q", ma.Unit, "milliamps")
	}
	bank := findSignalValue(t, values, portSignalBank("port_1"))
	if bank.Value != "Ports 1-4" {
		t.Errorf("fpp.port.port_1.bank = %#v, want %q", bank.Value, "Ports 1-4")
	}
	enabled := findSignalValue(t, values, portSignalEnabled("port_1"))
	if enabled.Absence != "" || enabled.Value != false {
		t.Errorf("fpp.port.port_1.enabled = %+v, want measured false (per this capture)", enabled)
	}
	status := findSignalValue(t, values, portSignalStatus("port_1"))
	if status.Absence != "" || status.Value != true {
		t.Errorf("fpp.port.port_1.status = %+v, want measured true (per this capture)", status)
	}
	pixelCount := findSignalValue(t, values, portSignalPixelCount("port_1"))
	if pixelCount.Absence != observation.StateUnsupported {
		t.Errorf("fpp.port.port_1.pixel_count: Absence = %q, want %q (pixelCount absent from this capture)", pixelCount.Absence, observation.StateUnsupported)
	}

	count := findSignalValue(t, values, SignalPortsCount)
	if count.Value != int64(48) {
		t.Errorf("fpp.ports.count = %#v, want 48", count.Value)
	}
	blind := findSignalValue(t, values, SignalPortsBlindCount)
	if blind.Value != int64(32) {
		t.Errorf("fpp.ports.blind_count = %#v, want 32 (the smart-receiver positions)", blind.Value)
	}
}

// TestPortSignalsDuplicateKeyEmitsOneDecodeFailedSignal proves contract
// section 3.1's "if two elements normalize to the same key ... emit no
// port signals for it and emit one fpp.ports.decode_failed observation
// naming the problem" — and that colliding elements never silently invent
// a key by falling back to the array index.
func TestPortSignalsDuplicateKeyEmitsOneDecodeFailedSignal(t *testing.T) {
	body := []byte(`[
		{"name":"Port 1","bank":"Ports 1-4","col":1,"row":1,"enabled":true,"status":true,"ma":0},
		{"name":"Port  1","bank":"Ports 1-4","col":2,"row":1,"enabled":true,"status":true,"ma":0}
	]`)
	values, err := fpp.PortSignals(body)
	if err != nil {
		t.Fatalf("fpp.PortSignals() error = %v", err)
	}

	failures := 0
	for _, v := range values {
		if v.Signal == SignalPortsDecodeFailed {
			failures++
			if v.Reason == "" {
				t.Errorf("fpp.ports.decode_failed: empty Reason, want it to name the problem")
			}
		}
		if v.Signal == portSignalKind("port_1") {
			t.Errorf("a colliding key must never receive port signals, but found %q", v.Signal)
		}
	}
	if failures != 1 {
		t.Errorf("fpp.ports.decode_failed appeared %d times, want exactly 1 (one observation, not one per colliding element)", failures)
	}
}

// TestPortSignalsMissingNameEmitsDecodeFailed covers the other §3.1
// collision case: an element with no usable "name" at all.
func TestPortSignalsMissingNameEmitsDecodeFailed(t *testing.T) {
	body := []byte(`[{"bank":"Ports 1-4","col":1,"row":1,"enabled":true,"status":true,"ma":0}]`)
	values, err := fpp.PortSignals(body)
	if err != nil {
		t.Fatalf("fpp.PortSignals() error = %v", err)
	}
	found := false
	for _, v := range values {
		if v.Signal == SignalPortsDecodeFailed {
			found = true
		}
	}
	if !found {
		t.Errorf("fpp.PortSignals(no name field) did not produce fpp.ports.decode_failed")
	}
}

// --- warningsSignals -----------------------------------------------------

// TestWarningsSignalsObjectShape covers the real MQTT "warnings" topic
// shape captured from FPP-Main: an array of {"id":<int>,"message":<string>}
// objects — structurally warningInfo, not the plain-string "warnings"
// array /api/fppd/status carries under the same name (see decode.go's
// warningsSignals doc comment).
func TestWarningsSignalsObjectShape(t *testing.T) {
	values, err := warningsSignals(readTestdata(t, "FPP-Main_warnings.json"))
	if err != nil {
		t.Fatalf("warningsSignals() error = %v", err)
	}
	count := findSignalValue(t, values, SignalWarningsCount)
	if count.Value != int64(1) {
		t.Errorf("fpp.warnings.count = %#v, want 1", count.Value)
	}
	summary := findSignalValue(t, values, SignalWarningsSummary)
	if summary.Value != "A Log Level is set to Debug" {
		t.Errorf("fpp.warnings.summary = %#v, want %q", summary.Value, "A Log Level is set to Debug")
	}
}

// TestWarningsSignalsEmptyArray covers testdata/FPP-remote-04_warnings.json
// ("[]"): zero warnings is a measured fact (count 0), same discipline as
// an empty ports array.
func TestWarningsSignalsEmptyArray(t *testing.T) {
	values, err := warningsSignals(readTestdata(t, "FPP-remote-04_warnings.json"))
	if err != nil {
		t.Fatalf("warningsSignals() error = %v", err)
	}
	count := findSignalValue(t, values, SignalWarningsCount)
	if count.Value != int64(0) {
		t.Errorf("fpp.warnings.count = %#v, want 0", count.Value)
	}
	summary := findSignalValue(t, values, SignalWarningsSummary)
	if summary.Value != "" {
		t.Errorf("fpp.warnings.summary = %#v, want empty string", summary.Value)
	}
}

// --- single-value raw-text topics ----------------------------------------

// TestSingleValueTopicsAreRawTextNotJSON verifies decode.go's finding that
// version/branch/status/ready/playlist-repeat/playlist-position all
// publish PLAIN TEXT, not a JSON-quoted string — testdata/FPP-Main_status.json
// is exactly the four bytes "idle", which json.Unmarshal into a string
// would reject outright.
func TestSingleValueTopicsAreRawTextNotJSON(t *testing.T) {
	values, err := rawTextStringSignal(SignalStatus)(readTestdata(t, "FPP-Main_status.json"))
	if err != nil {
		t.Fatalf("rawTextStringSignal(status)() error = %v", err)
	}
	got := findSignalValue(t, values, SignalStatus)
	if got.Value != "idle" {
		t.Errorf("fpp.status = %#v, want %q", got.Value, "idle")
	}

	readyValues, err := readySignal(readTestdata(t, "FPP-Main_ready.json"))
	if err != nil {
		t.Fatalf("readySignal() error = %v", err)
	}
	readyGot := findSignalValue(t, readyValues, SignalReady)
	if readyGot.Value != true {
		t.Errorf("fpp.ready = %#v, want true (payload was \"1\")", readyGot.Value)
	}

	repeatValues, err := rawTextIntSignal(SignalPlaylistRepeatMode)(readTestdata(t, "FPP-Main_playlist_repeat_status.json"))
	if err != nil {
		t.Fatalf("rawTextIntSignal(repeat)() error = %v", err)
	}
	repeatGot := findSignalValue(t, repeatValues, SignalPlaylistRepeatMode)
	if repeatGot.Value != int64(0) {
		t.Errorf("fpp.playlist.repeat_mode = %#v, want 0", repeatGot.Value)
	}
}

func TestReadySignalRejectsUnexpectedValue(t *testing.T) {
	if _, err := readySignal([]byte("maybe")); err == nil {
		t.Errorf("readySignal(%q) error = nil, want an error for a non-0/1 value", "maybe")
	}
}

// --- normalizeKey ---------------------------------------------------------

func TestNormalizeKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Port 1", "port_1"},
		{"Port 48", "port_48"},
		{"CPU: ", "cpu"},
		{"VIN1: ", "vin1"},
		{"K16-Max: ", "k16_max"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := normalizeKey(tc.in); got != tc.want {
			t.Errorf("normalizeKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- structural invariant: one signal, one topic ---------------------------

// TestNoSignalIDAppearsInMoreThanOneTopicSpec is topics.go's own
// referenced invariant test: no static signal ID may be declared by more
// than one topicSpec, because two topics producing the same (resource,
// signal, source) within a single Poll call is undefined for the store
// this collector feeds (see topics.go's doc comment).
func TestNoSignalIDAppearsInMoreThanOneTopicSpec(t *testing.T) {
	owner := make(map[observation.SignalID]string)
	for suffix, spec := range topicSpecs {
		for _, sig := range spec.staticSignals {
			if prev, dup := owner[sig]; dup {
				t.Errorf("signal %q is declared by both topic %q and topic %q", sig, prev, suffix)
			}
			owner[sig] = suffix
		}
	}
}
