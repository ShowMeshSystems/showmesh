package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These tests cover the RESOLVED participation state the coordinator
// reports for one node or one instance (node.showParticipation,
// fppInstance.showParticipation, resolumeInstance.showParticipation), not
// the selection an operator records on a show object. The states an
// operator actually hits at a rig are the ones where a host is NOT being
// checked, so those are the ones covered here; "participating" is the
// case nobody needs help reading.

const nodeParticipationFixture = `{"serverTime":"2026-09-08T00:00:00Z","node":{"nodeId":"node-01",
	"capabilities":[],"controlPlane":{"state":"online","reason":null},
	"evidence":{
	  "hello":{"signal":"node.hello","value":null,"unit":null,"state":"not_collected","reason":"none","observedAt":null,"collectedAt":"2026-09-08T00:00:00Z","source":"s","quality":"direct"},
	  "lastWill":{"signal":"node.lastWill","value":null,"unit":null,"state":"not_collected","reason":"none","observedAt":null,"collectedAt":"2026-09-08T00:00:00Z","source":"s","quality":"direct"},
	  "heartbeat":{"signal":"node.heartbeat","value":null,"unit":null,"state":"not_collected","reason":"none","observedAt":null,"collectedAt":"2026-09-08T00:00:00Z","source":"s","quality":"direct"}
	},"render":[],"audio":[],"fppConnect":[],%s}}`

func renderNodeWithParticipation(t *testing.T, participationJSON string) string {
	t.Helper()
	body := fmt.Sprintf(nodeParticipationFixture, participationJSON)
	n, serverTime, err := decodeSingleNode([]byte(body))
	if err != nil {
		t.Fatalf("decodeSingleNode: %v", err)
	}
	var out bytes.Buffer
	printNodeDetail(&out, n, serverTime)
	return out.String()
}

// TestNodeDetailRendersParticipationStatesAndReasons decodes each state
// off the wire and asserts the node detail view prints the state AND the
// coordinator's reason. The reason is the load-bearing half: "no show is
// active" and "could not be determined" send an operator to different
// actions, and the state name alone does not say which.
func TestNodeDetailRendersParticipationStatesAndReasons(t *testing.T) {
	cases := []struct {
		name           string
		participation  string
		wantState      string
		wantShow       string
		wantReasonLine string
	}{
		{
			name:           "participating",
			participation:  `"showParticipation":{"state":"participating","show":"halloween-2026","reason":null}`,
			wantState:      "State:  participating",
			wantShow:       "Show:   halloween-2026",
			wantReasonLine: "Reason: (none reported)",
		},
		{
			name:           "not participating",
			participation:  `"showParticipation":{"state":"not_participating","show":"halloween-2026","reason":null}`,
			wantState:      "State:  NOT-PARTICIPATING",
			wantShow:       "Show:   halloween-2026",
			wantReasonLine: "Reason: (none reported)",
		},
		{
			name:           "no show active",
			participation:  `"showParticipation":{"state":"not_configured","show":"","reason":"no show is currently active"}`,
			wantState:      "State:  NOT-CONFIGURED",
			wantShow:       "Show:   -",
			wantReasonLine: "Reason: no show is currently active",
		},
		{
			name:           "could not be determined",
			participation:  `"showParticipation":{"state":"unknown","show":"halloween-2026","reason":"could not resolve cue catalog: store closed"}`,
			wantState:      "State:  UNKNOWN",
			wantShow:       "Show:   halloween-2026",
			wantReasonLine: "Reason: could not resolve cue catalog: store closed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderNodeWithParticipation(t, tc.participation)
			if !strings.Contains(got, "Show participation:") {
				t.Fatalf("node detail printed no participation block:\n%s", got)
			}
			for _, want := range []string{tc.wantState, tc.wantShow, tc.wantReasonLine} {
				if !strings.Contains(got, want) {
					t.Errorf("node detail is missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// TestNodeDetailParticipationAbsentIsNotAnAnswer: a coordinator that
// predates the field sends nothing, which decodes to State "". That must
// read as "not reported", never as a resolved "no".
func TestNodeDetailParticipationAbsentIsNotAnAnswer(t *testing.T) {
	got := renderNodeWithParticipation(t, `"label":null`)
	if !strings.Contains(got, "(not reported by this coordinator, which predates this field)") {
		t.Errorf("absent participation did not render the not-reported line:\n%s", got)
	}
	if strings.Contains(got, "NOT-PARTICIPATING") {
		t.Errorf("absent participation rendered as a resolved negative answer:\n%s", got)
	}
}

const fppParticipationFixture = `{"serverTime":"2026-09-08T00:00:00Z","instance":{"instanceId":"fpp-01",
	"endpoint":"http://fpp-01","health":"healthy","observations":[],"lastPollAt":null,"lastPollError":null,
	"instanceUuid":null,"instanceUuidFirstObservedAt":null,"instanceUuidChange":null,
	"duplicateInstanceUuidEndpointIds":[],%s}}`

func renderFPPWithParticipation(t *testing.T, participationJSON string) string {
	t.Helper()
	body := fmt.Sprintf(fppParticipationFixture, participationJSON)
	inst, serverTime, err := decodeSingleFPPInstance([]byte(body))
	if err != nil {
		t.Fatalf("decodeSingleFPPInstance: %v", err)
	}
	var out bytes.Buffer
	printFPPTable(&out, fppResponse{ServerTime: serverTime, Instances: []fppInstance{inst}})
	return out.String()
}

// TestFPPInstanceSelectionUnrecordedIsNotNotParticipating is the
// load-bearing assertion for the state the instance form has and the node
// form does not. An active show with no selection recorded still has every
// configured instance checked; rendering that as NOT-PARTICIPATING would
// tell an operator the opposite of what is true.
func TestFPPInstanceSelectionUnrecordedIsNotNotParticipating(t *testing.T) {
	got := renderFPPWithParticipation(t,
		`"showParticipation":{"state":"selection_unrecorded","show":"halloween-2026",`+
			`"reason":"the active show has no instance participation selection recorded; `+
			`every configured instance is treated as taking part until one is recorded"}`)

	if !strings.Contains(got, "SELECTION-UNRECORDED") {
		t.Errorf("selection_unrecorded did not render its own state:\n%s", got)
	}
	if strings.Contains(got, "NOT-PARTICIPATING") {
		t.Errorf("selection_unrecorded was rendered as NOT-PARTICIPATING, which reverses its meaning:\n%s", got)
	}
	if !strings.Contains(got, "every configured instance is treated as taking part until one is recorded") {
		t.Errorf("selection_unrecorded did not print the reason that carries its consequence:\n%s", got)
	}
}

// TestFPPInstanceNotParticipatingRendersReason covers the other confusing
// case: this instance was explicitly left out of tonight's selection.
func TestFPPInstanceNotParticipatingRendersReason(t *testing.T) {
	got := renderFPPWithParticipation(t,
		`"showParticipation":{"state":"not_participating","show":"halloween-2026","reason":null}`)
	for _, want := range []string{
		"fpp-01 show participation:",
		"State:  NOT-PARTICIPATING",
		"Show:   halloween-2026",
		"Reason: (none reported)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fpp instance render is missing %q:\n%s", want, got)
		}
	}
}

// TestFPPTableCarriesParticipationColumn: the summary table an operator
// skims has to say which hosts are in tonight, not only the per-instance
// block below it.
func TestFPPTableCarriesParticipationColumn(t *testing.T) {
	got := renderFPPWithParticipation(t,
		`"showParticipation":{"state":"not_configured","show":"","reason":"no show is currently active"}`)
	header := firstLine(got)
	if !strings.Contains(header, "PARTICIPATION") {
		t.Errorf("fpp table header has no participation column: %q", header)
	}
	if !strings.Contains(got, "Reason: no show is currently active") {
		t.Errorf("fpp instance block did not print the reason:\n%s", got)
	}
}

// TestResolumeInstanceRendersParticipationReason covers the third
// structure carrying the field, including its own summary column.
func TestResolumeInstanceRendersParticipationReason(t *testing.T) {
	inst := resolumeInstance{
		InstanceID: "arena-01",
		Health:     "healthy",
		ShowParticipation: instanceShowParticipation{
			State:  "unknown",
			Show:   "halloween-2026",
			Reason: strPtr("could not read the active show's participation selection: store closed"),
		},
	}
	var out bytes.Buffer
	printResolumeInstancesTable(&out, resolumeInstancesResponse{
		ServerTime: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC),
		Instances:  []resolumeInstance{inst},
	})
	got := out.String()
	if !strings.Contains(firstLine(got), "PARTICIPATION") {
		t.Errorf("resolume table header has no participation column:\n%s", got)
	}
	for _, want := range []string{
		"arena-01 show participation:",
		"State:  UNKNOWN",
		"Reason: could not read the active show's participation selection: store closed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resolume instance render is missing %q:\n%s", want, got)
		}
	}
}

// TestParticipationStateGlyphKeepsEveryStateDistinct: five states with
// five different remedies must not collapse onto one another, and a state
// added after this build must render loudly rather than as an answer.
func TestParticipationStateGlyphKeepsEveryStateDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, st := range []string{
		participationParticipating,
		participationNotParticipating,
		participationSelectionUnrecorded,
		participationNotConfigured,
		participationUnknown,
		"",
		"invented_later",
	} {
		glyph := participationStateGlyph(st)
		if prev, dup := seen[glyph]; dup {
			t.Errorf("states %q and %q both render as %q", prev, st, glyph)
		}
		seen[glyph] = st
	}
	if got := participationStateGlyph("invented_later"); got != "UNRECOGNIZED-STATE(invented_later)" {
		t.Errorf("unrecognized state rendered as %q", got)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
