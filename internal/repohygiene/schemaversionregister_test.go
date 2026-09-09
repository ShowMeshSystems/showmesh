package repohygiene

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// schemaVersionRegisterFloor is the lowest schema version the "## Schema
// versions" table in docs/build/IDENTIFIER-REGISTER.md is expected to carry
// a row for. v1 through v5 shipped before the register existed and are
// deliberately unregistered, so a missing-row check below this floor is not
// a defect.
const schemaVersionRegisterFloor = 6

// schemaVersionRow is one parsed row of the "## Schema versions" table:
// either an explicit "vN" version or the closing open-ended "vN+" row that
// claims every number above the highest shipped as free.
type schemaVersionRow struct {
	version   int
	openEnded bool
	status    string
}

// schemaVersionHeadingPattern finds a level-2 Markdown heading, used to cut
// the "## Schema versions" section out of the register without sweeping the
// file's other tables. Two other tables in the same document contain range
// rows shaped exactly like the open-ended row this test cares about (the
// showmeshctl exit-code table's documented "16 to 19 unallocated free", and
// "RES-020+ unallocated free"); a whole-file sweep would flag both as false
// positives.
var schemaVersionHeadingPattern = regexp.MustCompile(`(?m)^## .+$`)

// extractSchemaVersionSection returns the text of the "## Schema versions"
// section only: from its own heading up to (not including) the next "## "
// heading, or end of file if it is last.
func extractSchemaVersionSection(registerText string) (string, error) {
	const heading = "## Schema versions"
	start := strings.Index(registerText, heading)
	if start == -1 {
		return "", fmt.Errorf("no %q heading found", heading)
	}
	rest := registerText[start+len(heading):]
	end := len(rest)
	if loc := schemaVersionHeadingPattern.FindStringIndex(rest); loc != nil {
		end = loc[0]
	}
	return rest[:end], nil
}

// schemaVersionRowPattern matches one Markdown table row whose first cell is
// a version number, capturing the version digits, an optional trailing "+"
// marking the open-ended row, and the status cell. It requires a lowercase
// "v" immediately followed by a digit, so it does not match the table's own
// "| Version | Status | ... |" header row.
var schemaVersionRowPattern = regexp.MustCompile(`(?m)^\|\s*v(\d+)(\+)?\s*\|\s*([^|]*?)\s*\|`)

// parseSchemaVersionRows parses every version row out of the given table
// text (normally the output of extractSchemaVersionSection). It returns an
// error if it finds none, so an empty parse fails loudly instead of being
// mistaken for a table with nothing wrong in it.
func parseSchemaVersionRows(tableText string) ([]schemaVersionRow, error) {
	matches := schemaVersionRowPattern.FindAllStringSubmatch(tableText, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("found no version rows; the table heading, row format, or section bound may have changed")
	}
	rows := make([]schemaVersionRow, 0, len(matches))
	for _, m := range matches {
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("row %q: %w", m[0], err)
		}
		rows = append(rows, schemaVersionRow{
			version:   version,
			openEnded: m[2] == "+",
			status:    m[3],
		})
	}
	return rows, nil
}

// migrationVersionPattern matches one {version: N, ...} entry in
// internal/coordinator/store/migrations.go.
var migrationVersionPattern = regexp.MustCompile(`\{version:\s*(\d+),`)

// parseMigrationVersions extracts every migration version migrations.go
// declares. It returns an error if it finds none, matching
// parseSchemaVersionRows: an empty parse must fail loudly rather than read
// as an empty, trivially satisfied comparison.
func parseMigrationVersions(migrationsSrc string) ([]int, error) {
	matches := migrationVersionPattern.FindAllStringSubmatch(migrationsSrc, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("found no {version: N, ...} entries; the migration struct literal shape may have changed")
	}
	versions := make([]int, 0, len(matches))
	for _, m := range matches {
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", m[0], err)
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// checkSchemaVersionRows compares parsed register rows against the versions
// migrations.go actually carries and returns one message per rule broken,
// each naming the offending version, the reason, and what to do about it.
// An empty result means the register and migrations.go agree.
func checkSchemaVersionRows(rows []schemaVersionRow, migrationVersions []int, floor int) []string {
	unique := map[int]bool{}
	for _, v := range migrationVersions {
		unique[v] = true
	}
	sortedMigrations := make([]int, 0, len(unique))
	for v := range unique {
		sortedMigrations = append(sortedMigrations, v)
	}
	sort.Ints(sortedMigrations)
	maxMigration := 0
	if len(sortedMigrations) > 0 {
		maxMigration = sortedMigrations[len(sortedMigrations)-1]
	}

	var violations []string
	rowSet := map[int]bool{}
	var openEnded *schemaVersionRow
	for i := range rows {
		r := rows[i]
		if r.openEnded {
			row := r
			openEnded = &row
			continue
		}
		rowSet[r.version] = true
		status := strings.ToLower(r.status)
		switch {
		case strings.HasPrefix(status, "reserved"):
			if r.version <= maxMigration {
				violations = append(violations, fmt.Sprintf(
					"v%d is reserved but migrations.go's highest version is v%d: migrate() targets the maximum version and returns early once the stored version reaches it, so a migration numbered v%d can never run. Release this reservation or renumber it above v%d.",
					r.version, maxMigration, r.version, maxMigration))
			}
		case strings.HasPrefix(status, "shipped"):
			if !unique[r.version] {
				violations = append(violations, fmt.Sprintf(
					"v%d is marked shipped in the register but migrations.go carries no {version: %d, ...} entry: write the migration, or correct the row if it was never actually shipped.",
					r.version, r.version))
			}
		}
	}

	for _, v := range sortedMigrations {
		if v >= floor && !rowSet[v] {
			violations = append(violations, fmt.Sprintf(
				"migrations.go carries version %d (at or above the register's floor of v%d) with no row in the \"## Schema versions\" table: add a row recording who introduced it, before the next reader mints it again as free.",
				v, floor))
		}
	}

	if openEnded == nil {
		violations = append(violations, "the \"## Schema versions\" table has no open-ended \"vN+\" row recording every version above the highest shipped as free.")
	} else {
		want := maxMigration + 1
		if openEnded.version != want {
			violations = append(violations, fmt.Sprintf(
				"the open-ended row reads v%d+ but migrations.go's highest version is v%d, so it should read v%d+: a lower bound at or below v%d advertises an already-shipped version as free, which is the exact defect that let a duplicate migration silently never run.",
				openEnded.version, maxMigration, want, maxMigration))
		}
	}

	return violations
}

func TestParseSchemaVersionRowsRejectsEmptyParse(t *testing.T) {
	_, err := parseSchemaVersionRows("| Version | Status | Introduced by |\n|---|---|---|\n")
	if err == nil {
		t.Fatal("expected an error for a table with no version rows, got nil")
	}
}

func TestParseMigrationVersionsRejectsEmptyParse(t *testing.T) {
	_, err := parseMigrationVersions("var migrations = []migration{}\n")
	if err == nil {
		t.Fatal("expected an error for source with no {version: N, ...} entries, got nil")
	}
}

func TestExtractSchemaVersionSectionIgnoresOtherTables(t *testing.T) {
	const doc = `## showmeshctl exit codes

| Code | Meaning |
|---|---|
| 16 to 19 | unallocated free |

16 to 19 are deliberately unallocated, see the prose below the table.

## Schema versions

| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v7+ | unallocated | free |

## Research record numbers

| RES-020+ | unallocated | free |
`
	section, err := extractSchemaVersionSection(doc)
	if err != nil {
		t.Fatalf("extractSchemaVersionSection: %v", err)
	}
	if strings.Contains(section, "showmeshctl") || strings.Contains(section, "16 to 19") {
		t.Errorf("section leaked content from the exit-code table:\n%s", section)
	}
	if strings.Contains(section, "RES-020") {
		t.Errorf("section leaked content from the research-record table:\n%s", section)
	}
	rows, err := parseSchemaVersionRows(section)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows (v6 and the v7+ range row), got %d: %+v", len(rows), rows)
	}
}

func TestCheckSchemaVersionRowsHealthyTable(t *testing.T) {
	const table = `| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v7 | shipped | seam 1 |
| v8 | shipped | seam 2 |
| v9+ | unallocated | free |
`
	rows, err := parseSchemaVersionRows(table)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	got := checkSchemaVersionRows(rows, []int{6, 7, 8}, 6)
	if len(got) != 0 {
		t.Errorf("expected no violations for a healthy table, got: %v", got)
	}
}

func TestCheckSchemaVersionRowsReservedBelowMaximum(t *testing.T) {
	const table = `| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v7 | reserved | held for later work |
| v8 | shipped | seam 2 |
| v9+ | unallocated | free |
`
	rows, err := parseSchemaVersionRows(table)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	got := checkSchemaVersionRows(rows, []int{6, 8}, 6)
	if !anyContains(got, "v7 is reserved") {
		t.Errorf("expected a violation naming v7 as a stale reservation, got: %v", got)
	}
}

func TestCheckSchemaVersionRowsShippedRowWithNoMigration(t *testing.T) {
	const table = `| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v7 | shipped | seam 1, never built |
| v8+ | unallocated | free |
`
	rows, err := parseSchemaVersionRows(table)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	got := checkSchemaVersionRows(rows, []int{6}, 6)
	if !anyContains(got, "v7 is marked shipped") {
		t.Errorf("expected a violation naming v7 as shipped with no migration, got: %v", got)
	}
}

func TestCheckSchemaVersionRowsMigrationWithNoRow(t *testing.T) {
	const table = `| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v8+ | unallocated | free |
`
	rows, err := parseSchemaVersionRows(table)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	got := checkSchemaVersionRows(rows, []int{6, 7}, 6)
	if !anyContains(got, "migrations.go carries version 7") {
		t.Errorf("expected a violation naming migration v7 as missing a row, got: %v", got)
	}
}

func TestCheckSchemaVersionRowsRangeRowLowerBoundTooLow(t *testing.T) {
	const table = `| Version | Status | Introduced by |
|---|---|---|
| v6 | shipped | seam 0 |
| v7 | shipped | seam 1 |
| v7+ | unallocated | free |
`
	rows, err := parseSchemaVersionRows(table)
	if err != nil {
		t.Fatalf("parseSchemaVersionRows: %v", err)
	}
	got := checkSchemaVersionRows(rows, []int{6, 7}, 6)
	if !anyContains(got, "the open-ended row reads v7+") {
		t.Errorf("expected a violation naming the v7+ range row as too low, got: %v", got)
	}
}

func anyContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestSchemaVersionRegisterMatchesMigrations checks
// docs/build/IDENTIFIER-REGISTER.md's "## Schema versions" table against
// internal/coordinator/store/migrations.go, the authority migrate() actually
// runs against. A row can claim a version is reserved or free while a
// migration with that number has already shipped; migrate() targets the
// maximum version across the slice and returns early once the stored
// version already equals it, so the collision never merge-conflicts and the
// duplicate migration silently never runs. Nothing errors, and the tables it
// would have created are never created. This test is the thing that catches
// that before a node hits it during an upgrade.
func TestSchemaVersionRegisterMatchesMigrations(t *testing.T) {
	root := repoRoot()

	registerPath := filepath.Join(root, "docs", "build", "IDENTIFIER-REGISTER.md")
	registerRaw, err := os.ReadFile(registerPath)
	if err != nil {
		t.Fatalf("reading %s: %v", registerPath, err)
	}
	section, err := extractSchemaVersionSection(string(registerRaw))
	if err != nil {
		t.Fatalf("%s: %v", registerPath, err)
	}
	rows, err := parseSchemaVersionRows(section)
	if err != nil {
		t.Fatalf("%s: %v", registerPath, err)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: parsed zero rows from the \"## Schema versions\" table; an empty parse must fail loudly, not read as a clean table", registerPath)
	}

	migrationsPath := filepath.Join(root, "internal", "coordinator", "store", "migrations.go")
	migrationsRaw, err := os.ReadFile(migrationsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", migrationsPath, err)
	}
	migrationVersions, err := parseMigrationVersions(string(migrationsRaw))
	if err != nil {
		t.Fatalf("%s: %v", migrationsPath, err)
	}
	if len(migrationVersions) == 0 {
		t.Fatalf("%s: parsed zero migration versions; an empty parse must fail loudly, not read as a clean comparison", migrationsPath)
	}

	violations := checkSchemaVersionRows(rows, migrationVersions, schemaVersionRegisterFloor)
	if len(violations) > 0 {
		t.Errorf("%s's \"## Schema versions\" table disagrees with %s (%d issue(s)):\n%s",
			registerPath, migrationsPath, len(violations), strings.Join(violations, "\n"))
	}
}
