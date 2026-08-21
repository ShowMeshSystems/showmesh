package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is the ingestion half of test/fixtures/fpp: it reads
// test/fixtures/fpp/ingestion.json from disk and drives the real
// handlePostFPPPlaylistEntryObservation handler through this package's
// own store/identity test scaffolding (fppobservations_test.go), the
// same scaffolding every other handler test in this package uses. It
// lives here rather than under test/fixtures/fpp because reaching that
// scaffolding from outside this package is not possible: it is
// unexported test-only plumbing (newFPPObservationTestSetup,
// mustCreatePrincipal, doRawRequest, ...), and duplicating a second copy
// of it under test/fixtures/fpp would let the two copies drift.
// See test/fixtures/fpp/README.md for the file's schema and how the
// plugin repository is expected to read the fixture data (it has no use
// for this file, which is Go-only and never touches the C++ side).

func ingestionFixturePath(t *testing.T) string {
	t.Helper()
	// This file lives at internal/coordinator/api/; the fixture lives at
	// test/fixtures/fpp/ from the repository root.
	return filepath.Join("..", "..", "..", "test", "fixtures", "fpp", "ingestion.json")
}

type ingestionFixture struct {
	Description string          `json:"description"`
	Cases       []ingestionCase `json:"cases"`
}

type ingestionCase struct {
	Name                string         `json:"name"`
	Description         string         `json:"description"`
	Scope               string         `json:"scope"`
	Body                map[string]any `json:"body,omitempty"`
	RawBody             *string        `json:"rawBody,omitempty"`
	RawBodyOverride     *string        `json:"rawBodyOverride,omitempty"`
	OversizedBodyBytes  *int           `json:"oversizedBodyBytes,omitempty"`
	ExpectedStatus      int            `json:"expectedStatus"`
	ExpectedProblemType string         `json:"expectedProblemType,omitempty"`
	ExpectedAccepted    *bool          `json:"expectedAccepted,omitempty"`
	ExpectedReplay      *bool          `json:"expectedReplay,omitempty"`
	PriorCases          []string       `json:"priorCases,omitempty"`
}

func loadIngestionFixture(t *testing.T) ingestionFixture {
	t.Helper()
	raw, err := os.ReadFile(ingestionFixturePath(t))
	if err != nil {
		t.Fatalf("read ingestion.json: %v", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var fixture ingestionFixture
	if err := dec.Decode(&fixture); err != nil {
		t.Fatalf("decode ingestion.json: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("ingestion.json has no cases")
	}
	return fixture
}

// ingestionCaseBody renders tc's wire body as raw bytes: rawBodyOverride
// wins when present (the case's content is not valid JSON, contract
// step 4's malformed-body refusal), then rawBody (valid JSON whose exact
// member order matters, for example a replay case that must not be
// re-marshaled: json.Marshal(map[string]any) sorts keys and would
// silently defeat that case), then oversizedBodyBytes (synthesized
// padding, since a literal 16 KiB JSON value would bloat the fixture
// file), then the ordinary JSON-encoded body object.
func ingestionCaseBody(t *testing.T, tc ingestionCase) string {
	t.Helper()
	if tc.RawBodyOverride != nil {
		return *tc.RawBodyOverride
	}
	if tc.RawBody != nil {
		return *tc.RawBody
	}
	if tc.OversizedBodyBytes != nil {
		return synthesizeOversizedBody(*tc.OversizedBodyBytes)
	}
	raw, err := json.Marshal(tc.Body)
	if err != nil {
		t.Fatalf("case %q: marshal body: %v", tc.Name, err)
	}
	return string(raw)
}

// synthesizeOversizedBody builds a syntactically valid observation body
// padded with filler characters in playlistName until the encoded body
// is at least n bytes, matching TestFPPObservationOversizedBodyRefused413's
// own construction in fppobservations_test.go.
func synthesizeOversizedBody(n int) string {
	prefix := `{"schemaVersion":1,"instanceUuid":"instance-oversized","action":"playing","sequence":1,` +
		`"observedAtMillis":1700000000000,"coalescedSincePreviousAcknowledged":0,"playlistName":"`
	suffix := `"}`
	fillerLen := n - len(prefix) - len(suffix)
	if fillerLen < 0 {
		fillerLen = 0
	}
	return prefix + strings.Repeat("a", fillerLen) + suffix
}

// ingestionTokens mints one bearer token per distinct scope value seen in
// a single case (a case's priorCases share the same underlying store, so
// each scope's principal is created at most once, not once per POST):
// "fpp:observe" -> RoleScheduler (holds fpp:observe), "show:macro:run" ->
// RoleOperator (holds show:macro:run but deliberately not fpp:observe,
// contract section 1.1's "an operator credential must not be able to
// forge plugin evidence"), "none" -> no credential at all.
type ingestionTokens struct {
	t       *testing.T
	setup   *fppObservationTestSetup
	byScope map[string]string
}

func newIngestionTokens(t *testing.T, setup *fppObservationTestSetup) *ingestionTokens {
	return &ingestionTokens{t: t, setup: setup, byScope: map[string]string{}}
}

func (it *ingestionTokens) forScope(scope string) string {
	it.t.Helper()
	if scope == "none" {
		return ""
	}
	if tok, ok := it.byScope[scope]; ok {
		return tok
	}
	var role identity.Role
	switch scope {
	case "fpp:observe":
		role = identity.RoleScheduler
	case "show:macro:run":
		role = identity.RoleOperator
	default:
		it.t.Fatalf("unrecognized fixture scope %q", scope)
	}
	p := mustCreatePrincipal(it.t, it.setup.svc, "fixture-"+strings.ReplaceAll(scope, ":", "-"), role)
	tok := mustIssueToken(it.t, it.setup.svc, p.ID)
	it.byScope[scope] = tok
	return tok
}

// TestIngestionFixtures drives handlePostFPPPlaylistEntryObservation
// through every case in test/fixtures/fpp/ingestion.json: this is the
// "fixtures test both producer and consumer" acceptance criterion, since
// it exercises the real HTTP handler rather than a stub.
func TestIngestionFixtures(t *testing.T) {
	fixture := loadIngestionFixture(t)
	byName := make(map[string]ingestionCase, len(fixture.Cases))
	for _, tc := range fixture.Cases {
		if _, dup := byName[tc.Name]; dup {
			t.Fatalf("duplicate case name %q in ingestion.json", tc.Name)
		}
		byName[tc.Name] = tc
	}

	for _, tc := range fixture.Cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			setup := newFPPObservationTestSetup(t, fixedClock(testNow))
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			tokens := newIngestionTokens(t, setup)
			token := tokens.forScope(tc.Scope)

			for _, priorName := range tc.PriorCases {
				prior, ok := byName[priorName]
				if !ok {
					t.Fatalf("case %q: priorCases references unknown case %q", tc.Name, priorName)
				}
				priorToken := tokens.forScope(prior.Scope)
				body := ingestionCaseBody(t, prior)
				resp, m := mustPostObservation(t, api, body, priorToken)
				if resp.StatusCode != prior.ExpectedStatus {
					t.Fatalf("case %q: priorCase %q: status = %d, want %d; body: %v",
						tc.Name, priorName, resp.StatusCode, prior.ExpectedStatus, m)
				}
			}

			body := ingestionCaseBody(t, tc)
			resp, m := mustPostObservation(t, api, body, token)

			if resp.StatusCode != tc.ExpectedStatus {
				t.Fatalf("case %q: status = %d, want %d; body: %v", tc.Name, resp.StatusCode, tc.ExpectedStatus, m)
			}

			if tc.ExpectedProblemType != "" {
				wantType := ProblemBaseURI + tc.ExpectedProblemType
				if got := fmt.Sprint(m["type"]); got != wantType {
					t.Errorf("case %q: problem type = %q, want %q", tc.Name, got, wantType)
				}
			}
			if tc.ExpectedAccepted != nil {
				accepted, _ := m["accepted"].(bool)
				if accepted != *tc.ExpectedAccepted {
					t.Errorf("case %q: accepted = %v, want %v", tc.Name, accepted, *tc.ExpectedAccepted)
				}
			}
			if tc.ExpectedReplay != nil {
				replay, _ := m["replay"].(bool)
				if replay != *tc.ExpectedReplay {
					t.Errorf("case %q: replay = %v, want %v", tc.Name, replay, *tc.ExpectedReplay)
				}
			}
			if tc.ExpectedStatus != http.StatusOK && (tc.ExpectedAccepted != nil || tc.ExpectedReplay != nil) {
				t.Errorf("case %q: a non-200 case should not also assert accepted/replay", tc.Name)
			}
		})
	}
}
