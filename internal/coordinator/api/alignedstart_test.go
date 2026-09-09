package api

import (
	"encoding/json"
	"strings"
	"testing"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestReadinessFromEvidencePreservesANanosecondReading is the defect this
// whole path exists to prevent: a UnixNano-scale reading that goes through
// a float64 comes back with different digits, and a start instant built on
// it lands at the wrong time.
func TestReadinessFromEvidencePreservesANanosecondReading(t *testing.T) {
	const exact = "1789012345678901234"
	got := readinessFromEvidence("node-a", true, map[string]any{
		pkgaudio.ResultMediaClockValid: true,
		pkgaudio.ResultMediaClockNowNs: json.Number(exact),
	})
	if !got.MediaClockValid {
		t.Fatalf("MediaClockValid = false, want true")
	}
	if got.MediaClockNowNs != 1789012345678901234 {
		t.Errorf("MediaClockNowNs = %d, want 1789012345678901234: the reading was rounded", got.MediaClockNowNs)
	}
}

// TestEvidenceInt64RefusesAnAlreadyRoundedFloat proves a float64 that has
// lost precision is refused rather than accepted at its rounded value. A
// number this large arriving as a float64 means something upstream already
// rounded it, and using it would bake that error into the start instant.
func TestEvidenceInt64RefusesAnAlreadyRoundedFloat(t *testing.T) {
	if _, ok := evidenceInt64(float64(1.789012345678901234e18)); ok {
		t.Error("evidenceInt64 accepted an oversized float64; it has already been rounded")
	}
	if v, ok := evidenceInt64(float64(120)); !ok || v != 120 {
		t.Errorf("evidenceInt64(120.0) = %d,%v, want 120,true", v, ok)
	}
	if _, ok := evidenceInt64(float64(1.5)); ok {
		t.Error("evidenceInt64 accepted a non-integral float64")
	}
	if _, ok := evidenceInt64("120"); ok {
		t.Error("evidenceInt64 accepted a string")
	}
}

// TestReadinessFromEvidenceNeverInventsAClock covers the rule that a
// missing validity flag is not a valid clock.
func TestReadinessFromEvidenceNeverInventsAClock(t *testing.T) {
	noEvidence := readinessFromEvidence("node-a", true, nil)
	if noEvidence.MediaClockValid {
		t.Error("a node that reported no evidence at all was read as having a valid clock")
	}
	if noEvidence.MediaClockReason == "" {
		t.Error("absent evidence produced no reason")
	}

	noFlag := readinessFromEvidence("node-a", true, map[string]any{
		pkgaudio.ResultMediaClockNowNs: json.Number("123"),
	})
	if noFlag.MediaClockValid {
		t.Error("a reading with no validity flag was read as valid; a missing flag is not consent")
	}

	// Valid true but no readable reading must not fall through as a
	// zero instant.
	badReading := readinessFromEvidence("node-a", true, map[string]any{
		pkgaudio.ResultMediaClockValid: true,
		pkgaudio.ResultMediaClockNowNs: "not a number",
	})
	if badReading.MediaClockValid {
		t.Error("a valid flag with an unreadable reading was accepted")
	}
	if !strings.Contains(badReading.MediaClockReason, "no readable reading") {
		t.Errorf("reason = %q, want it to state the reading was unreadable", badReading.MediaClockReason)
	}
}

// TestReadinessFromEvidenceTreatsAnUnknownErrorBoundAsUnknown pins the
// rule that unknown is not zero.
func TestReadinessFromEvidenceTreatsAnUnknownErrorBoundAsUnknown(t *testing.T) {
	unknown := readinessFromEvidence("node-a", true, map[string]any{
		pkgaudio.ResultMediaClockValid:           true,
		pkgaudio.ResultMediaClockNowNs:           json.Number("5"),
		pkgaudio.ResultMediaClockErrorBoundKnown: false,
		pkgaudio.ResultMediaClockErrorBoundNs:    json.Number("999"),
	})
	if unknown.ErrorBoundKnown || unknown.ErrorBoundNs != 0 {
		t.Errorf("ErrorBound known=%v ns=%d, want false/0: a bound reported as unknown must not be used",
			unknown.ErrorBoundKnown, unknown.ErrorBoundNs)
	}

	known := readinessFromEvidence("node-a", true, map[string]any{
		pkgaudio.ResultMediaClockValid:           true,
		pkgaudio.ResultMediaClockNowNs:           json.Number("5"),
		pkgaudio.ResultMediaClockErrorBoundKnown: true,
		pkgaudio.ResultMediaClockErrorBoundNs:    json.Number("250000"),
	})
	if !known.ErrorBoundKnown || known.ErrorBoundNs != 250000 {
		t.Errorf("ErrorBound known=%v ns=%d, want true/250000", known.ErrorBoundKnown, known.ErrorBoundNs)
	}
}

// TestReadinessFromEvidenceDistinguishesNoPrerollFromZero covers the same
// absent-is-not-zero rule for preroll.
func TestReadinessFromEvidenceDistinguishesNoPrerollFromZero(t *testing.T) {
	absent := readinessFromEvidence("node-a", false, map[string]any{})
	if absent.PrerollKnown {
		t.Error("a node reporting no preroll was read as reporting one")
	}
	reported := readinessFromEvidence("node-a", false, map[string]any{pkgaudio.ResultPrerollMs: json.Number("0")})
	if !reported.PrerollKnown || reported.PrerollMs != 0 {
		t.Errorf("preroll known=%v ms=%d, want true/0: a reported zero is evidence", reported.PrerollKnown, reported.PrerollMs)
	}
}

func TestDecodeAlignedAudioStartRequestBodyRejectsUnknownFields(t *testing.T) {
	_, err := decodeAlignedAudioStartRequestBody(strings.NewReader(`{"revision":1,"nodeIds":["a"],"params":{}}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("err = %v, want an unknown-field refusal", err)
	}
	req, err := decodeAlignedAudioStartRequestBody(strings.NewReader(`{"revision":7,"nodeIds":["a","b"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Revision != 7 || len(req.NodeIDs) != 2 {
		t.Errorf("decoded = %+v, want revision 7 and two nodes", req)
	}
}
