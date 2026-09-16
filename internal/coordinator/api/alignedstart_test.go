package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestNodeHoldsMediaClockUsesDeclaredRoleNotLTCRoute proves the fix: a
// node carrying "program+ltc" with NO ltcRoute at all still holds the
// media clock (the shape the swapped-holder failure needed and
// ValidateAudioNodePlacement will never let a real Pi author, since it
// lacks a discrete LTC-capable route -- but nodeHoldsMediaClock itself
// must not require one), and a node carrying "program" with an ltcRoute
// coincidentally present does NOT -- role decides this alone.
func TestNodeHoldsMediaClockUsesDeclaredRoleNotLTCRoute(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	holderNoLTC, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:          "usb-interface",
		ProgramChannels:       []int{1, 2},
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "program+ltc role, ltc route not yet declared",
		Role:                  config.AudioNodeRoleProgramLTC,
	})
	if err != nil {
		t.Fatalf("encode holder-no-ltc payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.AudioNodeConfigKind, "holder-no-ltc", holderNoLTC)

	holds, err := api.h.nodeHoldsMediaClock(ctx, "holder-no-ltc")
	if err != nil {
		t.Fatalf("nodeHoldsMediaClock: %v", err)
	}
	if !holds {
		t.Error("holds = false, want true: role program+ltc holds the clock with no ltcRoute at all")
	}

	nonHolderWithLTC, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "role program, ltcRoute set anyway",
		Role:                  config.AudioNodeRoleProgram,
	})
	if err != nil {
		t.Fatalf("encode non-holder-with-ltc payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.AudioNodeConfigKind, "non-holder-with-ltc", nonHolderWithLTC)

	holds, err = api.h.nodeHoldsMediaClock(ctx, "non-holder-with-ltc")
	if err != nil {
		t.Fatalf("nodeHoldsMediaClock: %v", err)
	}
	if holds {
		t.Error("holds = true, want false: role program never holds the clock, whatever ltcRoute says")
	}
}

// TestNodeHoldsMediaClockDefaultRoleIsProgramLTC proves a stored payload
// with no "role" key at all -- a pre-ADR-045 object, or any payload
// literal built without setting Role -- resolves to the default role
// "program+ltc", the identical default [config.DecodeAudioNodePayload]
// applies on every fresh write.
func TestNodeHoldsMediaClockDefaultRoleIsProgramLTC(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
		// Role deliberately left unset.
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.AudioNodeConfigKind, "no-role-node", raw)

	holds, err := api.h.nodeHoldsMediaClock(ctx, "no-role-node")
	if err != nil {
		t.Fatalf("nodeHoldsMediaClock: %v", err)
	}
	if !holds {
		t.Error("holds = false, want true: an absent role key must default to program+ltc")
	}
}

// TestProgramLTCNodeWithoutLTCRouteIsSelectedAsClockHolderByAudiosched
// proves the fix end to end through the aligned-start path's own two real
// production functions together: [handlers.nodeHoldsMediaClock] (this
// file) decides who holds the role, and [audiosched.Select]
// (internal/coordinator/audiosched/select.go) is still the one that
// actually picks the start instant from that holder's reading. A
// program+ltc node with no ltcRoute must be the ClockNodeID audiosched
// selects, exactly as it must be for the real installation's node-01
// before any ltcRoute discovery has run.
func TestProgramLTCNodeWithoutLTCRouteIsSelectedAsClockHolderByAudiosched(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	ctx := context.Background()

	holder, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:          "usb-interface",
		ProgramChannels:       []int{1, 2},
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "program+ltc role, ltc route not yet declared",
		Role:                  config.AudioNodeRoleProgramLTC,
	})
	if err != nil {
		t.Fatalf("encode holder payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.AudioNodeConfigKind, "holder-no-ltc", holder)

	second, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute:          "scarlett",
		ProgramChannels:       []int{1, 2},
		ClockDomain:           "scarlett-domain",
		ClockDomainProvenance: "two-channel interface, program only",
		Role:                  config.AudioNodeRoleProgram,
	})
	if err != nil {
		t.Fatalf("encode second-node payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.AudioNodeConfigKind, "second-node", second)

	var readiness []audiosched.Readiness
	for _, nodeID := range []string{"holder-no-ltc", "second-node"} {
		holds, err := api.h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			t.Fatalf("nodeHoldsMediaClock(%s): %v", nodeID, err)
		}
		r := audiosched.Readiness{NodeID: nodeID, HoldsMediaClock: holds}
		if holds {
			r.MediaClockValid = true
			r.MediaClockNowNs = 1_000_000_000
		} else {
			r.MediaClockValid = true
			r.MediaClockNowNs = 2_000_000_000
		}
		readiness = append(readiness, r)
	}

	sel, err := audiosched.Select(readiness, 0, 0)
	if err != nil {
		t.Fatalf("audiosched.Select: %v", err)
	}
	if sel.ClockNodeID != "holder-no-ltc" {
		t.Errorf("ClockNodeID = %q, want %q: the program+ltc role holds the clock even with no ltcRoute declared",
			sel.ClockNodeID, "holder-no-ltc")
	}
	if sel.ScheduledAtNs != 1_000_000_000 {
		t.Errorf("ScheduledAtNs = %d, want the holder's own reading (1e9), not the other node's (2e9)", sel.ScheduledAtNs)
	}
}

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
