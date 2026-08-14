// This file proves the fold-in guarantee decode.go and topics.go now
// document: internal/coordinator/collector/fpp.StatusSignals and
// .PortSignals are the ONLY decoders for the fppd_status/port_status
// document shapes, on both the REST and MQTT paths — the property
// decode.go's now-deleted STUB NOTICE said would need proving once
// fpp.StatusSignals/fpp.PortSignals existed. A second, independently
// maintained decoder reappearing here (by accident or by a future editor
// "just adding one field" to a local copy instead of to fpp/signals.go)
// is exactly the silent-drift risk this file guards against.
package fppmqtt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// realStatusCaptures is every real fppd_status document this repository has
// captured from the live fleet, drawn from BOTH packages' testdata/
// directories — including the fpp-ghost ghost payload (delivered retained
// only, over MQTT, in production; structurally the same document type
// either way, since neither decoder ever touches a clock). A capture
// living in only one package's testdata/ still proves agreement: the point
// is that fpp.StatusSignals is the one function that ever sees this
// document shape, regardless of which transport handed it over.
func realStatusCaptures(t *testing.T) map[string][]byte {
	t.Helper()
	return readCapturesOrFail(t, map[string]string{
		"fppmqtt/fpp-player_fppd_status.json":      filepath.Join("testdata", "fpp-player_fppd_status.json"),
		"fppmqtt/fpp-remote-a_fppd_status.json": filepath.Join("testdata", "fpp-remote-a_fppd_status.json"),
		"fppmqtt/fpp-ghost_fppd_status.json(ghost)": filepath.Join("testdata", "fpp-ghost_fppd_status.json"),
		"fpp/live_main_fppd_status.json":         filepath.Join("..", "fpp", "testdata", "live_main_fppd_status.json"),
		"fpp/live_remote01_fppd_status.json":     filepath.Join("..", "fpp", "testdata", "live_remote01_fppd_status.json"),
		"fpp/live_remote04_fppd_status.json":     filepath.Join("..", "fpp", "testdata", "live_remote04_fppd_status.json"),
	})
}

// realPortsCaptures is the port_status/ /api/fppd/ports mirror of
// realStatusCaptures, including the fpp-ghost ghost's port_status payload and
// fpp-remote-b's 48-element (32 smart-receiver) capture from both
// packages' testdata/.
func realPortsCaptures(t *testing.T) map[string][]byte {
	t.Helper()
	return readCapturesOrFail(t, map[string]string{
		"fppmqtt/fpp-ghost_port_status.json(ghost)": filepath.Join("testdata", "fpp-ghost_port_status.json"),
		"fppmqtt/fpp-remote-b_port_status.json": filepath.Join("testdata", "fpp-remote-b_port_status.json"),
		"fpp/live_main_fppd_ports.json":          filepath.Join("..", "fpp", "testdata", "live_main_fppd_ports.json"),
		"fpp/live_remote01_fppd_ports.json":      filepath.Join("..", "fpp", "testdata", "live_remote01_fppd_ports.json"),
		"fpp/live_remote04_fppd_ports.json":      filepath.Join("..", "fpp", "testdata", "live_remote04_fppd_ports.json"),
	})
}

func readCapturesOrFail(t *testing.T, names map[string]string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(names))
	for label, path := range names {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		out[label] = body
	}
	return out
}

// signalValueEqual compares two fpp.SignalValue for the facts that matter
// to this file's tests: Signal, Value, Unit, Absence, Reason. Neither
// SignalValue carries a clock (fpp.SignalValue's own doc comment: "no
// observation time attached") — that only appears once render.go turns a
// SignalValue into an [observation.Observation], which is exactly why this
// file compares SignalValue rather than Observation: it asks "did the two
// paths decode the document the same way", not "did they collect it at the
// same moment". The two paths are never expected to agree on the second
// question — contract section 4.2 requires a retained MQTT delivery's
// ObservedAt to be nil while REST's is always the poll time — so there is
// nothing to reconcile at this layer.
func signalValueEqual(a, b fpp.SignalValue) bool {
	return a.Signal == b.Signal && a.Value == b.Value && a.Unit == b.Unit &&
		a.Absence == b.Absence && a.Reason == b.Reason
}

// TestFoldInStatusTopicAgreesWithFPPStatusSignals is this package's
// cross-path agreement test for the fppd_status document: decodeStatusTopic
// (the MQTT topic's decoder) must produce EXACTLY fpp.StatusSignals' own
// output, minus exactly statusTopicExcludedSignals — never more, never
// less, and never a different Value/Unit/Absence/Reason for a signal both
// sides keep. A second, independently-written decoder reappearing here
// would show up as either a signal present on one side and not the other,
// or the same Signal carrying two different Values — this test would have
// failed against this package's original statusSignals STUB the moment it
// diverged from fpp.StatusSignals on SignalPositionSeconds/
// SignalPositionRemaining/SignalUptimeSeconds's type (int64 vs the REST
// decoder's float64 — see decode_test.go's
// TestStatusSignalsPlayerModeValues for where that divergence was found
// and corrected during fold-in).
func TestFoldInStatusTopicAgreesWithFPPStatusSignals(t *testing.T) {
	for label, body := range realStatusCaptures(t) {
		t.Run(label, func(t *testing.T) {
			restSignals, err := fpp.StatusSignals(body)
			if err != nil {
				t.Fatalf("fpp.StatusSignals() error = %v", err)
			}
			mqttSignals, err := decodeStatusTopic(body)
			if err != nil {
				t.Fatalf("decodeStatusTopic() error = %v", err)
			}

			restBySignal := make(map[observation.SignalID]fpp.SignalValue, len(restSignals))
			for _, sv := range restSignals {
				restBySignal[sv.Signal] = sv
			}
			mqttBySignal := make(map[observation.SignalID]fpp.SignalValue, len(mqttSignals))
			for _, sv := range mqttSignals {
				if _, dup := mqttBySignal[sv.Signal]; dup {
					t.Fatalf("decodeStatusTopic produced signal %q more than once", sv.Signal)
				}
				mqttBySignal[sv.Signal] = sv
			}

			// Every REST-side signal must appear, identically, on the MQTT
			// side — UNLESS it is one of the documented exclusions, in
			// which case it must be entirely absent from the MQTT side.
			for sig, restSV := range restBySignal {
				mqttSV, present := mqttBySignal[sig]
				if statusTopicExcludedSignals[sig] {
					if present {
						t.Errorf("signal %q is in statusTopicExcludedSignals but decodeStatusTopic still emitted it (%+v)", sig, mqttSV)
					}
					continue
				}
				if !present {
					t.Errorf("fpp.StatusSignals produced %q but decodeStatusTopic dropped it, and it is not in statusTopicExcludedSignals", sig)
					continue
				}
				if !signalValueEqual(restSV, mqttSV) {
					t.Errorf("signal %q disagrees between paths:\n  fpp.StatusSignals()  = %+v\n  decodeStatusTopic()  = %+v", sig, restSV, mqttSV)
				}
			}

			// The reverse: the MQTT side must never invent a signal
			// fpp.StatusSignals did not produce for the same document —
			// that would be exactly a second, diverging decoder.
			for sig, mqttSV := range mqttBySignal {
				if _, present := restBySignal[sig]; !present {
					t.Errorf("decodeStatusTopic produced %q, which fpp.StatusSignals never produced for the same document (%+v)", sig, mqttSV)
				}
			}
		})
	}
}

// TestFoldInPortTopicAgreesWithFPPPortSignals is the port_status mirror of
// TestFoldInStatusTopicAgreesWithFPPStatusSignals. There is no exclusion
// list on this side (topics.go wires "port_status" straight to
// fpp.PortSignals, with no filter), so the two outputs must be identical,
// element for element, in order.
func TestFoldInPortTopicAgreesWithFPPPortSignals(t *testing.T) {
	for label, body := range realPortsCaptures(t) {
		t.Run(label, func(t *testing.T) {
			direct, err := fpp.PortSignals(body)
			if err != nil {
				t.Fatalf("fpp.PortSignals() error = %v", err)
			}
			viaTopic, err := topicSpecs["port_status"].decode(body)
			if err != nil {
				t.Fatalf(`topicSpecs["port_status"].decode() error = %v`, err)
			}
			if len(direct) != len(viaTopic) {
				t.Fatalf(`fpp.PortSignals produced %d signals, topicSpecs["port_status"].decode produced %d, want equal`, len(direct), len(viaTopic))
			}
			for i := range direct {
				if !signalValueEqual(direct[i], viaTopic[i]) {
					t.Errorf("signal at index %d disagrees:\n  fpp.PortSignals()                    = %+v\n  topicSpecs[\"port_status\"].decode()  = %+v", i, direct[i], viaTopic[i])
				}
			}
		})
	}
}

// --- one signal, one topic: enforced against real Poll output --------------

// TestPollNeverDuplicatesAnExcludedSignalAcrossTopics is the dynamic
// counterpart to TestNoSignalIDAppearsInMoreThanOneTopicSpec
// (decode_test.go), which only checks topicSpecs' table shape. This test
// instead delivers a REAL fppd_status payload — which, undecoded, carries
// every one of statusTopicExcludedSignals' seven signals — on the
// fppd_status topic AND a real payload on each of those signals' own
// dedicated topics, for the SAME instance, then Polls once and asserts
// each of the seven signals appears EXACTLY ONCE in the actual output.
//
// This is the scenario the exclusion filter exists for: without
// decodeStatusTopic's filtering (topics.go), fpp.StatusSignals' own
// fpp.version/fpp.branch/fpp.status/fpp.playlist.repeat_mode/
// fpp.playlist.index/fpp.warnings.count/fpp.warnings.summary would collide
// with the values these dedicated topics already deliver, producing two
// observations for one (resource, signal, source) in a single Poll call —
// undefined for the store's schema v4 primary key (topics.go's doc
// comment). Before trusting this test, statusTopicExcludedSignals was
// emptied out (simulating the filter being silently dropped, e.g. by a
// future edit to decode.go/topics.go) and every one of the seven
// assertions below was confirmed to fail with "appeared 2 times, want
// exactly 1" — see this package's report for that verification; the
// mutation was reverted immediately after.
func TestPollNeverDuplicatesAnExcludedSignalAcrossTopics(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "fpp-player"}, &now)
	c.setConnected(true, "")

	// fppd_status: a real capture, which by itself carries version, branch,
	// status, playlist.repeat_mode, playlist.index, and (via its own
	// "warnings" field or lack thereof) has warnings-derived signals too.
	deliver(c, "falcon/player/fpp-player/fppd_status", readTestdata(t, "fpp-player_fppd_status.json"), false)

	// Each excluded signal's own dedicated topic, delivered independently —
	// exactly as a real FPP publishes them, per topics.go's table.
	deliver(c, "falcon/player/fpp-player/version", []byte("v9.5.3"), false)
	deliver(c, "falcon/player/fpp-player/branch", []byte("master"), false)
	deliver(c, "falcon/player/fpp-player/status", []byte("idle"), false)
	deliver(c, "falcon/player/fpp-player/playlist/repeat/status", []byte("0"), false)
	deliver(c, "falcon/player/fpp-player/playlist/position/status", []byte("2"), false)
	deliver(c, "falcon/player/fpp-player/warnings", []byte(`[{"id":1,"message":"A Log Level is set to Debug"}]`), false)

	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{
		SignalVersion, SignalBranch, SignalStatus,
		SignalPlaylistRepeatMode, SignalPlaylistIndex,
		SignalWarningsCount, SignalWarningsSummary,
	} {
		findObservation(t, obs, sig) // fails the test unless sig appears exactly once
	}

	// Spot-check the values actually came from the dedicated topics, not
	// from fppd_status's overlapping fields (which this same real capture
	// carries different values for, e.g. its own "version"/"branch"), so a
	// future change that flips which side wins would also be caught here.
	version := findObservation(t, obs, SignalVersion)
	if version.Value != "v9.5.3" {
		t.Errorf("fpp.version = %#v, want %q (from the dedicated topic, not fppd_status)", version.Value, "v9.5.3")
	}
	repeatMode := findObservation(t, obs, SignalPlaylistRepeatMode)
	if repeatMode.Value != int64(0) {
		t.Errorf("fpp.playlist.repeat_mode = %#v, want 0", repeatMode.Value)
	}
}
