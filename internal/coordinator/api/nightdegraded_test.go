package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/copycheck"
)

func nightDegradedExampleArgs(fact string) []any {
	var args []any
	for _, verb := range regexp.MustCompile(`%[sd]`).FindAllString(fact, -1) {
		if verb == "%d" {
			args = append(args, 3)
		} else {
			args = append(args, "player-01")
		}
	}
	return args
}

// Every degraded reason states the recovery step exactly once and meets the
// copy standard.
func TestEveryNightDegradedReasonStatesTheRecoveryStepOnce(t *testing.T) {
	for _, fact := range nightDegradedFacts {
		reason := nightDegradedReason(fact, nightDegradedExampleArgs(fact)...)
		if got := strings.Count(reason, nightRecoverAction); got != 1 {
			t.Errorf("%q states the recovery step %d times, want 1", reason, got)
		}
		if strings.Contains(fact, "End session") || strings.Contains(fact, "Prepare site") {
			t.Errorf("fact %q carries its own recovery step", fact)
		}
		if v := copycheck.Check(reason); len(v) != 0 {
			t.Errorf("%q breaks the copy standard: %+v", reason, v)
		}
	}
}

// The list above is every reason the package can produce: each fact constant
// is in it, and no degrade site writes a reason any other way.
func TestNightDegradedFactsListIsCompleteAndEverySiteUsesIt(t *testing.T) {
	files, err := filepath.Glob("night*.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := strings.Join(nightDegradedFacts, "\n")
	factConst := regexp.MustCompile(`(?m)^\t(nightDegraded\w+Fact)\s+= "([^"]+)"`)
	degradeCall := regexp.MustCompile(`h\.nightDegradeSession\(ctx, now, rec, (\w+)`)
	assignment := regexp.MustCompile(`DegradedReason = (\w+)`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, m := range factConst.FindAllStringSubmatch(src, -1) {
			if !strings.Contains(listed, m[2]) {
				t.Errorf("%s: %s is not in nightDegradedFacts", file, m[1])
			}
		}
		if file != "nightdegraded.go" && strings.Contains(src, "nightRecoverAction") {
			t.Errorf("%s adds the recovery step itself; use nightDegradedReason", file)
		}
		for _, re := range []*regexp.Regexp{degradeCall, assignment} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				if m[1] != "nightDegradedReason" && m[1] != "nightDegradedDetail" && m[1] != "reason" {
					t.Errorf("%s: a degraded reason is written as %q, want nightDegradedReason", file, m[0])
				}
			}
		}
	}
}

func TestNightDegradedDetailReplacesAReasonStoredInTheOlderForm(t *testing.T) {
	fixed := nightDegradedReason(nightDegradedNoReasonFact)
	for stored, want := range map[string]string{
		"": fixed,
		"resting playback stopped before its expected end (a different item is now playing than expected)": fixed,
		nightDegradedReason(nightDegradedStartFailedFact, "Resting", "player-01"):                          "Playlist Resting could not be started on FPP player-01. Run End session, then Prepare site.",
	} {
		if got := nightDegradedDetail(stored); got != want {
			t.Errorf("nightDegradedDetail(%q) = %q, want %q", stored, got, want)
		}
	}
}
