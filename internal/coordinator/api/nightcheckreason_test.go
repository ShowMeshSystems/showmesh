package api

import (
	"strings"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// nightCheckText is every message a check shows, one per line.
func nightCheckText(c nightReadinessCheck) string {
	return strings.Join(c.messages(), "\n")
}

// nightBlockedText is every blocked safety check on a refusal, one per line.
func nightBlockedText(p v1.Problem) string {
	lines := make([]string, 0, len(p.BlockedChecks))
	for _, c := range p.BlockedChecks {
		lines = append(lines, c.Name+" "+c.Reason)
	}
	return strings.Join(lines, "\n")
}

func TestNightCheckMessagesAreOneItemEachAndAHealthyCheckSaysNothing(t *testing.T) {
	healthy := nightReadinessCheck{name: "audio:alignment:a", health: nightHealthHealthy(), reason: "left over"}
	if got := healthy.messages(); len(got) != 0 {
		t.Fatalf("a healthy check carries %q, want no message", got)
	}
	several := nightReadinessCheck{name: "announcement-assets", health: nightHealthFailed(), reasons: []string{"One.", "Two."}}
	wire := nightEncodeChecks([]nightReadinessCheck{several})[0]
	if len(wire.Reasons) != 2 || wire.Reasons[0] != "One." || wire.Reasons[1] != "Two." {
		t.Fatalf("reasons = %q, want one item per message, never joined", wire.Reasons)
	}
	if wire.StateLabel != "Failed" {
		t.Fatalf("stateLabel = %q, want Failed", wire.StateLabel)
	}
}

// A result saved before checks carried a list still reads as one message.
func TestNightStoredCheckReadsTheSingleReasonSavedBeforeTheList(t *testing.T) {
	got := nightStoredCheck{Name: "resting:asset-duration", State: "failed", Reason: "The file is missing."}.wire()
	if len(got.Reasons) != 1 || got.Reasons[0] != "The file is missing." || got.Label != "Resting sequence length" {
		t.Fatalf("stored check = %+v, want its one reason and its label", got)
	}
}

func TestNightOperatorReasonShowsOnlyTheFirstMessage(t *testing.T) {
	for stored, want := range map[string]string{
		"FPP refused the command.": "FPP refused the command.",
		"FPP refused the command." + nightReasonNoteSeparator + "The note.": "FPP refused the command.",
		nightBedBookmarkNotePrefix + `{"known":true}`:                       "",
		`{"aligned":true}`: "",
		"":                 "",
	} {
		if got := nightOperatorReason(stored); got != want {
			t.Errorf("nightOperatorReason(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestNightInterlockRefusalListsEachBlockedCheckAndKeepsTheDetailOneLine(t *testing.T) {
	p := nightInterlockRefusal("start-night", []nightWithheldInterlock{
		{Rule: config.NightInterlockRule{Name: "gate"}, Reason: "The gate is open."},
		{Rule: config.NightInterlockRule{Name: "cooldown"}, Reason: ""},
	})
	if p.Detail != "2 safety checks blocked Start night. Fix them, or retry with an override." {
		t.Fatalf("detail = %q", p.Detail)
	}
	if len(p.BlockedChecks) != 2 || p.BlockedChecks[0] != (v1.NightBlockedCheck{Name: "gate", Reason: "The gate is open."}) ||
		p.BlockedChecks[1].Reason != "This safety check did not pass." {
		t.Fatalf("blockedChecks = %+v, want one item per rule", p.BlockedChecks)
	}
}
