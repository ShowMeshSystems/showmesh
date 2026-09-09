package store

import (
	"fmt"
	"strings"
	"testing"
)

// checkMigrationVersionOrder returns one message per version number that
// repeats an earlier entry or fails to increase on the entry before it.
// Either shape is the same defect: migrate() stamps the maximum version and
// returns early once the stored user_version has reached it, so an entry at
// or below a number some store already recorded never runs, never errors,
// and leaves the change it carries unmade. Each message names the offending
// version and its index so a red gate identifies the entry on its own.
func checkMigrationVersionOrder(versions []int) []string {
	var violations []string
	firstIndexOf := map[int]int{}
	previous := 0
	for i, v := range versions {
		if seen, ok := firstIndexOf[v]; ok {
			violations = append(violations, fmt.Sprintf(
				"version %d at index %d repeats the entry at index %d: migrate() stamps the maximum version and short-circuits on current == target, so the later entry can never run against a store already stamped at %d. Renumber it above the slice's maximum.",
				v, i, seen, v))
		} else {
			firstIndexOf[v] = i
		}
		if i > 0 && v <= previous {
			violations = append(violations, fmt.Sprintf(
				"version %d at index %d does not increase on version %d at index %d: an entry numbered at or below one that already shipped never runs against a store stamped at the higher number, and nothing errors. Renumber it above the slice's maximum.",
				v, i, previous, i-1))
		}
		previous = v
	}
	return violations
}

func migrationVersionsFromSlice() []int {
	versions := make([]int, 0, len(migrations))
	for _, m := range migrations {
		versions = append(versions, m.version)
	}
	return versions
}

// TestMigrationVersionsIncreaseStrictly reads the real migrations slice as a
// Go value rather than parsing migrations.go as text, so no reformatting of
// the struct literal can make it stop seeing entries and report clean.
func TestMigrationVersionsIncreaseStrictly(t *testing.T) {
	versions := migrationVersionsFromSlice()
	if len(versions) == 0 {
		t.Fatal("read zero entries from the migrations slice; an empty read must fail loudly, not read as a clean history")
	}
	if violations := checkMigrationVersionOrder(versions); len(violations) > 0 {
		t.Errorf("the migrations slice has %d version numbering issue(s):\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestCheckMigrationVersionOrderAcceptsGaps(t *testing.T) {
	if got := checkMigrationVersionOrder([]int{1, 2, 3, 10, 14}); len(got) != 0 {
		t.Errorf("expected no violations for increasing versions with gaps, got: %v", got)
	}
}

func TestCheckMigrationVersionOrderFlagsRepeat(t *testing.T) {
	got := checkMigrationVersionOrder([]int{1, 2, 3, 2, 4})
	if !anyMigrationViolationContains(got, "version 2 at index 3 repeats the entry at index 1") {
		t.Errorf("expected a violation naming the repeated version 2 and its index, got: %v", got)
	}
}

func TestCheckMigrationVersionOrderFlagsDecrease(t *testing.T) {
	got := checkMigrationVersionOrder([]int{1, 5, 4})
	if !anyMigrationViolationContains(got, "version 4 at index 2 does not increase on version 5 at index 1") {
		t.Errorf("expected a violation naming the decreasing version 4 and its index, got: %v", got)
	}
	if anyMigrationViolationContains(got, "repeats") {
		t.Errorf("a decrease that is not a repeat must not be reported as one, got: %v", got)
	}
}

func TestCheckMigrationVersionOrderFlagsAdjacentRepeatBothWays(t *testing.T) {
	got := checkMigrationVersionOrder([]int{1, 2, 2})
	if len(got) != 2 {
		t.Errorf("expected an adjacent repeat to be reported as both a repeat and a non-increase, got: %v", got)
	}
}

func anyMigrationViolationContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
