package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// This file is the machine check Task D spec section 8 requires: a
// hand-written OpenAPI document drifts from the code it describes unless
// something actually compares them. It drives every endpoint through a
// real [API] and validates the real response body against
// api/openapi.yaml's own schema for that endpoint, in both directions —
// the document lying about a field the server does not send, and the
// server sending something the document does not describe, both fail a
// test here (every object schema in api/openapi.yaml sets
// additionalProperties: false and lists every key as required, so either
// kind of drift is a validation error, not a silent pass).
//
// The validator is github.com/santhosh-tekuri/jsonschema/v6, a pure-Go
// JSON Schema (draft 2020-12) implementation — OpenAPI 3.1's Schema
// Object is JSON Schema 2020-12 verbatim, so this document's
// "components.schemas" entries are valid JSON Schema documents with no
// translation step needed. Chosen over a heavier OpenAPI-specific
// validator (e.g. kin-openapi, libopenapi) specifically to keep this
// dependency small and unambiguously CGo-free: ADR-012 forbids any new
// CGo dependency in the coordinator's build, and this package's own gate
// (see the report) checks that explicitly. yaml.v3 is already in this
// module's dependency graph (pulled in transitively via
// internal/agent's test dependencies) and is used here only to decode
// the document into a generic Go value before handing it to jsonschema.

// loadOpenAPIDocument parses api/openapi.yaml (relative to the repository
// root; this test's working directory is this package's own directory, so
// it walks up three levels) into the `any` shape jsonschema.Compiler wants:
// YAML decoded generically, re-encoded as JSON, then decoded again through
// jsonschema.UnmarshalJSON so integers and floats are distinguished the
// way the "integer" vs "number" JSON Schema types require — a plain
// encoding/json.Unmarshal into `any` collapses every JSON number to
// float64, which would make every "type: integer" check in the document
// either always-fail or silently unenforced depending on how it were
// worked around.
func loadOpenAPIDocument(t *testing.T) any {
	t.Helper()
	path := filepath.Join("..", "..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var generic any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("parsing %s as YAML: %v", path, err)
	}

	jsonBytes, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("re-encoding %s as JSON: %v", path, err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		t.Fatalf("decoding %s for jsonschema: %v", path, err)
	}
	return doc
}

// newOpenAPICompiler builds a [jsonschema.Compiler] with a STRICT overlay of
// api/openapi.yaml (see [strictSchemaOverlay]) registered as a resource
// under a fixed synthetic URL, so "#/components/schemas/X" fragments
// resolve against it.
//
// This is deliberately not the raw document [loadOpenAPIDocument] returns.
// Orchestrator correction (Step 3 review finding 2.1): the PUBLISHED
// api/openapi.yaml sets no "additionalProperties: false" anywhere — a
// closed schema there would reject the coordinator's very next additive
// field, contradicting this document's own info.description ("a client
// MUST ignore fields it does not recognize") and ADR-020 decision 8
// ("within a major version the contract is additive-only"). But an Opus
// review confirmed by mutation that closed schemas are exactly what makes
// this file's tests catch drift in the direction of an undeclared field
// (see TestOpenAPIValidatorRejectsAnUndeclaredField below), so every test
// in this file that exists to catch real drift compiles against the
// strict overlay, not the published document directly — strictness lives
// here, in the test binary, never in the shipped contract.
func newOpenAPICompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat() // actually validate "format: date-time", not just annotate.
	strict := strictSchemaOverlay(loadOpenAPIDocument(t))
	if err := c.AddResource(openAPIDocumentURL, strict); err != nil {
		t.Fatalf("registering the strict overlay of api/openapi.yaml as a jsonschema resource: %v", err)
	}
	return c
}

// strictSchemaOverlay walks doc (as [loadOpenAPIDocument] returns it —
// nested map[string]any / []any / json.Number / string / bool / nil, per
// [jsonschema.UnmarshalJSON]) and, for every JSON Schema object anywhere
// in the document that declares "properties", injects
// "additionalProperties": false into that same map. It mutates doc in
// place and returns it; every caller (just [newOpenAPICompiler]) loads a
// fresh document on each call, so nothing else holds a reference this
// could corrupt.
//
// This is the test-only overlay finding 2.1 asked for, restricted to
// exactly the one keyword the published document had to give up:
// "required" is left completely untouched, exactly as api/openapi.yaml
// itself authors it. That is enough on its own: every schema in this
// document except Problem already lists every one of its properties as
// required (Problem's supportedVersions is the sole, deliberate
// exception — present only on an unsupported-api-version problem — and
// forcing it into "required" here would fail every OTHER problem
// response's correctly-absent supportedVersions, which is not drift and
// must not fail). Re-deriving "required" from "properties" here would
// silently overwrite that one deliberate exception; not touching it at
// all is what keeps this overlay strictly additive to what the document
// already declares.
//
// A schema with no "properties" key (Capability.attributes,
// Event.details — free-form maps the document deliberately leaves
// additionalProperties: true on) is left completely alone: this walk
// only closes a schema that has a known, enumerable shape to close it
// against.
func strictSchemaOverlay(doc any) any {
	switch v := doc.(type) {
	case map[string]any:
		if _, ok := v["properties"]; ok {
			v["additionalProperties"] = false
		}
		for key, val := range v {
			v[key] = strictSchemaOverlay(val)
		}
		return v
	case []any:
		for i, val := range v {
			v[i] = strictSchemaOverlay(val)
		}
		return v
	default:
		return v
	}
}

const openAPIDocumentURL = "mem://showmesh/openapi.yaml"

// compileSchema compiles the named component schema (e.g. "NodeResponse")
// from api/openapi.yaml.
func compileSchema(t *testing.T, c *jsonschema.Compiler, name string) *jsonschema.Schema {
	t.Helper()
	sch, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("compiling schema %s: %v", name, err)
	}
	return sch
}

// assertMatchesSchema validates body (a raw JSON response) against the
// named component schema, failing with the full validation error
// (jsonschema's errors are hierarchical and name the exact failing
// path/keyword) on mismatch.
func assertMatchesSchema(t *testing.T, c *jsonschema.Compiler, schemaName string, body []byte) {
	t.Helper()
	sch := compileSchema(t, c, schemaName)

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decoding response body for validation: %v\nbody: %s", err, body)
	}
	if err := sch.Validate(instance); err != nil {
		t.Errorf("response does not match api/openapi.yaml schema %q:\n%v\nbody: %s", schemaName, err, body)
	}
}

// TestOpenAPIDocumentIsWellFormed proves the document itself at least
// parses as YAML and every schema referenced by a path in this test file
// compiles — a cheap, fast sanity check that fails clearly (not with a Go
// panic three tests later) if the document is broken outright.
func TestOpenAPIDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ServiceDescriptor", "NodesResponse", "NodeResponse", "FPPResponse",
		"FPPInstanceResponse", "ObservationsResponse", "EventsResponse",
		"Snapshot", "Problem", "StreamStart", "StreamReset",
		"NodeChangedEvent", "FPPChangedEvent", "EventRecordedEvent",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPISchemasMatchRealResponses drives every endpoint through a
// real [API] (the same fixtures and dependencies handlers_test.go's
// golden tests use) and validates each actual response body against
// api/openapi.yaml's schema for it.
func TestOpenAPISchemasMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api := buildTestAPI(t)

	tests := []struct {
		method, target, schema string
	}{
		{"GET", "/api/v1/", "ServiceDescriptor"},
		{"GET", "/api/v1/nodes", "NodesResponse"},
		{"GET", "/api/v1/nodes/media-03", "NodeResponse"},
		{"GET", "/api/v1/fpp", "FPPResponse"},
		{"GET", "/api/v1/fpp/player-01", "FPPInstanceResponse"},
		{"GET", "/api/v1/observations", "ObservationsResponse"},
		{"GET", "/api/v1/events", "EventsResponse"},
		{"GET", "/api/v1/snapshot", "Snapshot"},
	}

	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, tt.method, tt.target, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			assertMatchesSchema(t, c, tt.schema, body)
		})
	}
}

// TestOpenAPIProblemSchemaMatchesEveryClass validates a real problem
// response from each of the four classes Step 3 produces against the
// shared Problem schema.
func TestOpenAPIProblemSchemaMatchesEveryClass(t *testing.T) {
	c := newOpenAPICompiler(t)
	api := buildTestAPI(t)
	tokenAPI := New(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{AuthToken: "s3cret", Clock: fixedClock(testNow), Logger: testLogger()})

	tests := []struct {
		name    string
		handler http.Handler
		method  string
		target  string
		headers map[string]string
	}{
		{"resource-not-found", api.Handler, "GET", "/api/v1/nodes/nonexistent", nil},
		{"invalid-parameter", api.Handler, "GET", "/api/v1/nodes/Not_Valid!", nil},
		{"unsupported-api-version", api.Handler, "GET", "/api/v2/nodes", nil},
		{"unauthorized", tokenAPI.Handler, "GET", "/api/v1/", nil},
		{"method-not-allowed", api.Handler, "POST", "/api/v1/nodes", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, body := doRequest(t, tt.handler, tt.method, tt.target, tt.headers)
			assertMatchesSchema(t, c, "Problem", body)
		})
	}
}

// TestMethodNotAllowedHasAllowHeaderAndCorrectType proves finding 2.8's
// fix end to end through a real [API], not just that the body matches
// Problem's schema (TestOpenAPIProblemSchemaMatchesEveryClass above
// already does that): a POST to a route this API only serves as GET must
// come back 405, with a non-empty Allow header naming GET, and a
// problem+json body whose type is exactly [ProblemTypeMethodNotAllowed]
// — not merely "some Problem-shaped body", which the schema check alone
// would already accept even if a caller returned the wrong problem class
// entirely.
func TestMethodNotAllowedHasAllowHeaderAndCorrectType(t *testing.T) {
	api := buildTestAPI(t)

	resp, body := doRequest(t, api.Handler, "POST", "/api/v1/nodes", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body: %s", resp.StatusCode, body)
	}
	if allow := resp.Header.Get("Allow"); allow == "" {
		t.Fatalf("Allow header is empty; a 405 without it does not tell a client what it may do instead")
	} else if !strings.Contains(allow, "GET") {
		t.Fatalf("Allow header = %q, want it to name GET", allow)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json — net/http's own default 405 body must not leak through", ct)
	}

	var p struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decoding 405 body: %v\nbody: %s", err, body)
	}
	if p.Type != ProblemTypeMethodNotAllowed {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeMethodNotAllowed)
	}
}

// TestOpenAPIValidatorRejectsAMismatch proves the validator is not
// decorative: a response missing a field the schema requires must fail.
// Without a test like this, a validator that always reports success would
// pass every test above for the wrong reason.
func TestOpenAPIValidatorRejectsAMismatch(t *testing.T) {
	c := newOpenAPICompiler(t)
	sch := compileSchema(t, c, "NodeResponse")

	// A NodeResponse missing serverTime entirely — exactly the shape of
	// defect the orchestrator's correction (section 6.2's serverTime
	// requirement) was about.
	bad := []byte(`{"node":{"nodeId":"x","label":null,"platform":null,"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","capabilities":[],"controlPlane":{"state":"unknown","reason":"x"},"evidence":{"hello":{},"lastWill":{},"heartbeat":{}}}}`)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("decoding deliberately-bad instance: %v", err)
	}
	if err := sch.Validate(instance); err == nil {
		t.Fatalf("validator accepted a NodeResponse missing serverTime; the schema/validator pairing is not catching real drift")
	}
}

// TestOpenAPIValidatorRejectsAnUndeclaredField proves the OTHER direction
// finding 2.1's fix must not lose. api/openapi.yaml on disk no longer sets
// additionalProperties: false anywhere (finding 2.1: a closed published
// schema would reject the coordinator's very next additive field, which
// ADR-020 decision 8 makes a normal, permitted event). That means the
// PUBLISHED document, compiled on its own, would happily accept a
// response carrying a field it never declared — this test proves that
// [newOpenAPICompiler]'s strict overlay (additionalProperties: false,
// injected only for this test binary — see [strictSchemaOverlay]) still
// catches it, which is the specific strength the Step 3 review confirmed
// by mutation and asked to be preserved rather than quietly dropped
// along with the published strictness.
func TestOpenAPIValidatorRejectsAnUndeclaredField(t *testing.T) {
	c := newOpenAPICompiler(t)
	sch := compileSchema(t, c, "NodeResponse")

	// Every field NodeResponse/Node actually declare, plus one this
	// schema does not: "unexpectedField".
	extra := []byte(nodeResponseJSONWithExtraField)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(extra))
	if err != nil {
		t.Fatalf("decoding deliberately-extra instance: %v", err)
	}
	if err := sch.Validate(instance); err == nil {
		t.Fatalf("validator accepted a NodeResponse with an undeclared field; the strict test-only overlay (finding 2.1) is not being applied — check that newOpenAPICompiler still calls strictSchemaOverlay")
	}
}

// TestStrictSchemaOverlayLeavesPublishedDocumentPermissive is the other
// half of finding 2.1's proof: the PUBLISHED document — what
// [loadOpenAPIDocument] returns with no overlay applied — must accept
// exactly the same undeclared-field instance
// [TestOpenAPIValidatorRejectsAnUndeclaredField] proves the strict
// overlay rejects. If this test ever fails, api/openapi.yaml has grown a
// closed schema again (a hand-edit re-adding additionalProperties: false,
// most likely), which would break every real client the moment this
// coordinator adds a field — exactly the defect finding 2.1 was raised
// against.
func TestStrictSchemaOverlayLeavesPublishedDocumentPermissive(t *testing.T) {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(openAPIDocumentURL, loadOpenAPIDocument(t)); err != nil {
		t.Fatalf("registering the unmodified api/openapi.yaml as a jsonschema resource: %v", err)
	}
	sch, err := c.Compile(openAPIDocumentURL + "#/components/schemas/NodeResponse")
	if err != nil {
		t.Fatalf("compiling schema NodeResponse: %v", err)
	}

	extra := []byte(nodeResponseJSONWithExtraField)
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(extra))
	if err != nil {
		t.Fatalf("decoding deliberately-extra instance: %v", err)
	}
	if err := sch.Validate(instance); err != nil {
		t.Fatalf("the published api/openapi.yaml document rejected a response with an additional field it does not declare: %v\n"+
			"this document must stay additive-only-compatible (ADR-020 decision 8) — the closed-schema check belongs only in the strict test overlay, see strictSchemaOverlay", err)
	}
}

// validEvidenceJSON is a minimal, but fully schema-valid, Evidence
// envelope literal — every property Evidence's schema requires, with
// values chosen only to satisfy types (e.g. collectedAt null pairs with
// state "not_collected" the way mapping.go's collectedAtForWire actually
// produces it, but this JSON is hand-built, not server output; nothing
// here claims to be a real response).
const validEvidenceJSON = `{"signal":"node.hello","value":null,"unit":null,"state":"not_collected","reason":"no hello observed yet","observedAt":null,"collectedAt":null,"source":"mqtt-inventory","quality":"direct","validForSeconds":null}`

// nodeResponseJSONWithExtraField is a NodeResponse carrying every field
// its own schema (NodeResponse -> Node -> NodeEvidence -> Evidence)
// declares, with valid values throughout, plus exactly one field none of
// those schemas declare: "unexpectedField" at the top level. Shared
// between [TestOpenAPIValidatorRejectsAnUndeclaredField] and
// [TestStrictSchemaOverlayLeavesPublishedDocumentPermissive], which
// validate this same instance against the strict overlay and the
// published document respectively and must see opposite outcomes.
var nodeResponseJSONWithExtraField = `{"serverTime":"2026-01-01T00:00:00Z","node":{"nodeId":"x","label":null,"platform":null,"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","capabilities":[],"controlPlane":{"state":"unknown","reason":"x"},"evidence":{"hello":` +
	validEvidenceJSON + `,"lastWill":` + validEvidenceJSON + `,"heartbeat":` + validEvidenceJSON + `}},"unexpectedField":"surprise"}`

// TestOpenAPIStreamEventSchemasMatchRealFrames validates one real
// stream.start frame's JSON payload — obtained the same way
// stream_test.go does, over a real SSE connection — against the
// StreamStart schema, so the stream's documented shapes are checked
// against something a real connection actually sent, not merely
// hand-typed examples.
func TestOpenAPIStreamEventSchemasMatchRealFrames(t *testing.T) {
	c := newOpenAPICompiler(t)

	nodes := &fakeNodeLister{}
	testAPI := newStreamTestAPI(Dependencies{
		Nodes: nodes, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "stream.start" {
		t.Fatalf("event = %q, want stream.start", event)
	}
	assertMatchesSchema(t, c, "StreamStart", []byte(data))

	nodes.setViews([]inventory.NodeView{onlineNodeFixture(t)})
	testAPI.Hub.Notify()

	event, data = readEventWithTimeout(t, r, 5*time.Second)
	if event != "node.changed" {
		t.Fatalf("event = %q, want node.changed", event)
	}
	assertMatchesSchema(t, c, "NodeChangedEvent", []byte(data))
}
