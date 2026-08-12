package fpp

import (
	"encoding/json"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file covers Step 5 review finding 2: encoding/json treats a JSON
// null as a no-op returning no error for almost any Go destination type
// (a string, bool, or float64 field is simply left at its zero value),
// which every extractor in decode.go used to implicitly treat as "the
// field decoded successfully as its zero value" rather than "the field
// could not be trusted." Reproduced live:
//
//	{"name":"Port 1","smartReceivers":[],"ma":null}  ->  fpp.port.port_1.current_ma = 0, unit milliamps, NO absence
//	"powerBad": null   -> false, contributing HealthHealthy
//	"warnings": null   -> count 0, summary "", fabricating "no warnings"
//
// The realistic trigger is the broker: any publisher on
// falcon/player/<host>/port_status or .../fppd_status can put that shape
// on the wire, and fppmqtt decodes it through these exact same functions
// (contract section 4.3: "the decoding logic is the same. Do not
// duplicate it.").
//
// Before trusting any test below, the specific extractor it names was
// reverted to its pre-fix form (no isJSONNull check) and confirmed to
// make that test fail — see this package's Step 5 review-fix report for
// the full list of what was actually broken and re-fixed during
// verification.

// --- decode.go's raw field extractors, directly -----------------------------

func TestStringFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.stringField("k"); err == nil {
		t.Fatalf("stringField(null) = (%q, nil), want a non-nil error naming the field", v)
	}
}

func TestBoolFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.boolField("k"); err == nil {
		t.Fatalf("boolField(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

func TestNumberFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.numberField("k"); err == nil {
		t.Fatalf("numberField(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

func TestIntFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.intField("k"); err == nil {
		t.Fatalf("intField(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

func TestBoolFromNumberOrBoolRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.boolFromNumberOrBool("k"); err == nil {
		t.Fatalf("boolFromNumberOrBool(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

func TestObjectFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.objectField("k"); err == nil {
		t.Fatalf("objectField(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

func TestArrayFieldRejectsExplicitNull(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	if v, err := doc.arrayField("k"); err == nil {
		t.Fatalf("arrayField(null) = (%v, nil), want a non-nil error naming the field", v)
	}
}

// A JSON null must be distinguishable from the key being genuinely absent:
// isFieldAbsent (Step 5 review finding 3's mechanism) must report false for
// an explicit null, since the key WAS present — a null value is a decode
// failure, not an absence.
func TestExplicitNullIsNotFieldAbsent(t *testing.T) {
	doc := rawDoc{"k": json.RawMessage(`null`)}
	_, err := doc.stringField("k")
	if err == nil {
		t.Fatalf("test setup broken: stringField(null) returned nil error")
	}
	if isFieldAbsent(err) {
		t.Errorf("isFieldAbsent(stringField(null)'s error) = true, want false — the key was present, just null")
	}

	_, absentErr := doc.stringField("missing")
	if !isFieldAbsent(absentErr) {
		t.Errorf("isFieldAbsent(stringField(missing key)'s error) = false, want true")
	}
}

// --- Reproduced end to end, from a real capture mutated to carry null ------

// TestPortCurrentMANeverFabricatesFromExplicitNull is the exact §3.2
// reproduction: an output port element whose "ma" is present but null must
// never decode to a measured 0 mA (or any value at all) — it must become
// collection_failed, distinguishable from both a measured reading and from
// the smart-receiver "ma" was never even present" Unsupported case.
func TestPortCurrentMANeverFabricatesFromExplicitNull(t *testing.T) {
	body := mutatePortsElementField(t, loadTestdata(t, "live_remote01_fppd_ports.json"), 0, "ma", nil)

	sigs, err := PortSignals(body)
	if err != nil {
		t.Fatalf("PortSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, "fpp.port.port_1.current_ma")
	if got.Value != nil {
		t.Fatalf(`fpp.port.port_1.current_ma Value = %#v (%T), want nil — a present-but-null "ma" must never fabricate a measured current`, got.Value, got.Value)
	}
	if got.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.port.port_1.current_ma Absence = %q, want collection_failed (the key WAS present, just null — distinct from the smart-receiver 'never had an ma key at all' case)", got.Absence)
	}
	if got.Reason == "" {
		t.Errorf("fpp.port.port_1.current_ma Reason is empty, want an explanation naming the field")
	}
}

// TestPowerBadNeverFabricatesFalseFromExplicitNull is the health-relevant
// reproduction named in the review: "powerBad": null must never decode to
// false (which, per §5.3, would silently contribute a healthy verdict) —
// it must become collection_failed.
func TestPowerBadNeverFabricatesFalseFromExplicitNull(t *testing.T) {
	body := mutateJSONField(t, loadTestdata(t, "live_main_fppd_status.json"), "powerBad", nil)

	sigs, err := StatusSignals(body)
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, SignalPowerBad)
	if got.Value != nil {
		t.Fatalf("fpp.power.bad Value = %#v, want nil — a null powerBad must never fabricate false", got.Value)
	}
	if got.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.power.bad Absence = %q, want collection_failed", got.Absence)
	}
}

// TestWarningsNullNeverFabricatesEmptyList is the third named reproduction:
// "warnings": null must never decode to count 0 / summary "" (which reads
// identically to "FPP positively reported zero warnings") — it must become
// collection_failed, distinct from both the populated-array case and the
// key-absent-entirely Unsupported case (contract section 3.4).
func TestWarningsNullNeverFabricatesEmptyList(t *testing.T) {
	// live_main_fppd_status.json carries a populated "warnings" array (see
	// TestWarningsPresentArrayIsMeasured), so mutating its value to null
	// here exercises the "present but null" branch specifically, distinct
	// from live_remote04's "absent entirely" capture.
	body := mutateJSONField(t, loadTestdata(t, "live_main_fppd_status.json"), "warnings", nil)

	sigs, err := StatusSignals(body)
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	for _, sig := range []observation.SignalID{SignalWarningsCount, SignalWarningsSummary} {
		got := findSignalValue(t, sigs, sig)
		if got.Value != nil {
			t.Errorf("signal %q Value = %#v, want nil — a null \"warnings\" must never fabricate an empty-list measurement", sig, got.Value)
		}
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed (distinct from the key-absent Unsupported case)", sig, got.Absence)
		}
	}
}

// TestSchedulerEnabledNullNeverFabricatesFalse covers boolFromNumberOrBool
// through the mode-governed path: scheduler.enabled arriving as an
// explicit null on a player-mode host (where scheduler IS expected) must
// become collection_failed, never a fabricated false.
func TestSchedulerEnabledNullNeverFabricatesFalse(t *testing.T) {
	body := mutateNestedJSONField(t, loadTestdata(t, "live_main_fppd_status.json"), "scheduler", "enabled", nil)

	sigs, err := StatusSignals(body)
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, SignalSchedulerEnabled)
	if got.Value != nil {
		t.Fatalf("fpp.scheduler.enabled Value = %#v, want nil", got.Value)
	}
	if got.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.scheduler.enabled Absence = %q, want collection_failed (present but null, not mode-explained)", got.Absence)
	}
}

// mutateNestedJSONField returns a copy of body with doc[outerKey][innerKey]
// replaced by newValue, failing the test if either key is not present.
// Mirrors mutateJSONField (fpp_test.go) one level deeper, for fields under
// an object like "scheduler".
func mutateNestedJSONField(t *testing.T, body []byte, outerKey, innerKey string, newValue any) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("mutateNestedJSONField: base body is not a JSON object: %v", err)
	}
	outerRaw, ok := doc[outerKey]
	if !ok {
		t.Fatalf("mutateNestedJSONField: outer key %q not present in base body", outerKey)
	}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(outerRaw, &outer); err != nil {
		t.Fatalf("mutateNestedJSONField: outer key %q is not a JSON object: %v", outerKey, err)
	}
	if _, ok := outer[innerKey]; !ok {
		t.Fatalf("mutateNestedJSONField: inner key %q not present under %q", innerKey, outerKey)
	}
	raw, err := json.Marshal(newValue)
	if err != nil {
		t.Fatalf("mutateNestedJSONField: marshal newValue: %v", err)
	}
	outer[innerKey] = raw
	outerOut, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("mutateNestedJSONField: marshal mutated outer object: %v", err)
	}
	doc[outerKey] = outerOut
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("mutateNestedJSONField: marshal mutated document: %v", err)
	}
	return out
}

// --- Step 5 review finding 3: mode-explained absence vs. decode failure ----

// TestPresentButMalformedModeFieldIsCollectionFailedNotUnsupported is the
// exact reproduction named in the review: injecting a present-but-malformed
// "repeat_mode" into a real remote-mode capture must yield
// collection_failed with the decode error, never "unsupported" with the
// mode-explanation reason — FPP DID report the field; it just did not
// decode as an integer or numeric string.
//
// Before trusting this test, modeAbsenceReason was reverted to ignore
// fieldErr (its pre-fix form, checking only modeName/modeErr) and confirmed
// to make this test fail with Absence == unsupported and Reason == "host
// is in remote mode; FPP does not report a repeat mode" — exactly the
// false-reason bug the review found; see the Step 5 review-fix report.
func TestPresentButMalformedModeFieldIsCollectionFailedNotUnsupported(t *testing.T) {
	// "repeat_mode" is genuinely absent from this real remote-mode capture
	// (confirmed: TestGenuinelyAbsentModeFieldStillReportsUnsupported below
	// exercises that as-is), so mutateJSONField (which requires the key to
	// already exist) cannot be used here — the whole point of this test is
	// the key being INJECTED as present-but-malformed, which is a
	// different document shape than any real capture, per this package's
	// convention of deriving mutations from a real capture wherever the
	// hazard under test can actually appear in one.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(loadTestdata(t, "live_remote01_fppd_status.json"), &doc); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	doc["repeat_mode"] = json.RawMessage(`"not-a-number"`)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	sigs, statusErr := StatusSignals(body)
	if statusErr != nil {
		t.Fatalf("StatusSignals() error = %v", statusErr)
	}
	got := findSignalValue(t, sigs, SignalPlaylistRepeatMode)
	if got.Absence != observation.StateCollectionFailed {
		t.Fatalf(`fpp.playlist.repeat_mode Absence = %q (reason %q), want collection_failed: the field WAS present (just undecodable), so the mode explanation ("host is in remote mode...") must not apply`,
			got.Absence, got.Reason)
	}
	if contains(got.Reason, "does not report") {
		t.Errorf("fpp.playlist.repeat_mode Reason = %q, want the actual decode error, not the mode-explained-absence wording", got.Reason)
	}
}

// TestPresentButMalformedNestedModeFieldIsCollectionFailed covers the same
// bug through a nested mode-governed path: current_playlist.index present
// but malformed on a remote-mode host (where current_playlist is not
// expected at all) must still be collection_failed, not unsupported —
// proving the fix holds for modeGovernedNestedInt, not only the top-level
// case above.
func TestPresentButMalformedNestedModeFieldIsCollectionFailed(t *testing.T) {
	// live_remote01_fppd_status.json has no "current_playlist" object at
	// all (remote mode) — inject one with a malformed "index" so the
	// field is genuinely present-but-undecodable, not absent.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(loadTestdata(t, "live_remote01_fppd_status.json"), &doc); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	doc["current_playlist"] = json.RawMessage(`{"index":"not-a-number"}`)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}

	sigs, decErr := StatusSignals(body)
	if decErr != nil {
		t.Fatalf("StatusSignals() error = %v", decErr)
	}
	got := findSignalValue(t, sigs, SignalPlaylistIndex)
	if got.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.playlist.index Absence = %q, want collection_failed (current_playlist WAS present with a malformed index, not genuinely absent)", got.Absence)
	}
}

// TestGenuinelyAbsentModeFieldStillReportsUnsupported is the non-regression
// check: the ordinary, correct case (the field is truly absent because the
// host is in the "wrong" mode) must still report Unsupported with the
// mode-naming reason — the fix must narrow the bug, not remove the
// feature.
func TestGenuinelyAbsentModeFieldStillReportsUnsupported(t *testing.T) {
	sigs, err := StatusSignals(loadTestdata(t, "live_remote01_fppd_status.json"))
	if err != nil {
		t.Fatalf("StatusSignals() error = %v", err)
	}
	got := findSignalValue(t, sigs, SignalPlaylistRepeatMode)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("fpp.playlist.repeat_mode Absence = %q, want unsupported (genuinely absent on this real remote-mode capture)", got.Absence)
	}
	if !contains(got.Reason, "remote") {
		t.Errorf("fpp.playlist.repeat_mode Reason = %q, want it to name the remote mode", got.Reason)
	}
}
