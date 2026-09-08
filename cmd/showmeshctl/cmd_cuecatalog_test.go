package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestCmdCueCatalogDeployUsageNamesTheEnforcedScope guards against the
// help text drifting from the route's actual gate: the deploy route is
// behind identity.ScopeCueCatalogDeploy ("cuecatalog:deploy", admin only),
// deliberately distinct from asset:write (api/openapi.yaml, cuecatalog
// operations; internal/coordinator/identity/types.go's ScopeCueCatalogDeploy
// doc comment).
func TestCmdCueCatalogDeployUsageNamesTheEnforcedScope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCueCatalog([]string{"deploy", "-h"}, &stdout, &stderr, func() time.Time { return time.Now() })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (flag.ErrHelp); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cuecatalog:deploy") {
		t.Errorf("usage output does not name cuecatalog:deploy:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "asset:write") {
		t.Errorf("usage output wrongly claims asset:write:\n%s", stderr.String())
	}
}

// cueCatalogGetResponseBody is a GET .../cue-catalog body carrying all
// three acknowledgement projection fields api/openapi.yaml's
// CueCatalogResponse schema declares (acknowledgedStatus required,
// acknowledgedRevision/acknowledgedAt present since the node has
// acknowledged something).
const cueCatalogGetResponseBody = `{
	"serverTime":"2026-08-16T21:00:00Z",
	"node":"node-1",
	"configured":true,
	"show":"halloween-2026",
	"generation":3,
	"revision":"rev-9",
	"entries":[],
	"acknowledgedStatus":"catalog-stale",
	"acknowledgedRevision":"rev-8",
	"acknowledgedAt":"2026-08-16T20:00:00Z"
}`

// TestCmdCueCatalogGetReportsAcknowledgement reproduces the defect: a
// node's held acknowledgement (acknowledgedStatus/acknowledgedRevision/
// acknowledgedAt) must survive both output modes. Against the struct
// before this fix, cueCatalogResponse declares none of these three
// fields, so json.Unmarshal silently drops them and -output json
// re-marshals a body missing three fields api/openapi.yaml marks
// present on the wire — this test fails on that code with the table
// output missing the acknowledgement line and the JSON output missing
// all three keys.
func TestCmdCueCatalogGetReportsAcknowledgement(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, cueCatalogGetResponseBody)
	}))
	defer ts.Close()

	t.Run("table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"catalog-stale", "rev-8", "2026-08-16T20:00:00Z"} {
			if !strings.Contains(out, want) {
				t.Errorf("table stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "-output", "json", "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		for _, key := range []string{`"acknowledgedStatus"`, `"acknowledgedRevision"`, `"acknowledgedAt"`} {
			if !strings.Contains(out, key) {
				t.Errorf("-output json = %q, want it to contain key %s", out, key)
			}
		}
		for _, val := range []string{"catalog-stale", "rev-8", "2026-08-16T20:00:00Z"} {
			if !strings.Contains(out, val) {
				t.Errorf("-output json = %q, want it to contain value %q", out, val)
			}
		}
	})
}

// cueCatalogGetRenderResponseBody carries a render output with both
// sequence and filename populated (a real, resolved asset): the entry
// shape TestCmdCueCatalogGetReportsRenderFilename decodes.
const cueCatalogGetRenderResponseBody = `{
	"serverTime":"2026-08-16T21:00:00Z",
	"node":"node-1",
	"configured":true,
	"show":"halloween-2026",
	"generation":3,
	"revision":"rev-9",
	"entries":[
		{
			"cueId":"wake-up",
			"cueRevision":1,
			"outputs":{
				"render":{"sequence":"wake-up-mh-test","filename":"wake-up-mh-test.fseq","assetHashes":["sha256:abc"]}
			}
		}
	],
	"acknowledgedStatus":"catalog-unacknowledged"
}`

// TestCmdCueCatalogGetReportsRenderFilename proves render.filename
// survives BOTH output modes, not merely the text line. Against the
// struct before this fix, cueCatalogRenderOutputRecord declared no
// Filename field at all, so json.Unmarshal silently dropped it on decode
// and -output json (which re-marshals the CLI's own decoded struct,
// never the raw response body) printed a render object with no filename
// key at all, exactly where an operator reaching for the whole truth
// would look for it.
func TestCmdCueCatalogGetReportsRenderFilename(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, cueCatalogGetRenderResponseBody)
	}))
	defer ts.Close()

	t.Run("table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "wake-up-mh-test.fseq") {
			t.Errorf("table stdout = %q, want it to contain the render filename", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "-output", "json", "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, `"filename"`) {
			t.Errorf("-output json = %q, want it to contain key \"filename\"", out)
		}
		if !strings.Contains(out, "wake-up-mh-test.fseq") {
			t.Errorf("-output json = %q, want it to contain the render filename value", out)
		}
	})
}

// cueCatalogGetEmptyRenderFilenameResponseBody carries a render output
// whose sequence is populated but whose filename is empty: the coordinator
// resolves this exact shape whenever no asset has been uploaded for that
// sequence (assetsync.resolveAssetFor). This is the one state that made
// the underlying defect hard to notice: the sequence alone reads as
// healthy.
const cueCatalogGetEmptyRenderFilenameResponseBody = `{
	"serverTime":"2026-08-16T21:00:00Z",
	"node":"node-1",
	"configured":true,
	"show":"halloween-2026",
	"generation":3,
	"revision":"rev-9",
	"entries":[
		{
			"cueId":"wake-up",
			"cueRevision":1,
			"outputs":{
				"render":{"sequence":"wake-up-mh-test","filename":"","assetHashes":[]}
			}
		}
	],
	"acknowledgedStatus":"catalog-unacknowledged"
}`

// TestCmdCueCatalogGetReportsEmptyRenderFilenameDistinctlyFromSequence
// proves an empty filename alongside a populated sequence renders
// distinctly: the table line shows a dash rather than a blank or an
// omitted field, and -output json still carries the "filename" key (an
// empty string, never dropped) rather than silently agreeing with the
// healthy-looking sequence.
func TestCmdCueCatalogGetReportsEmptyRenderFilenameDistinctlyFromSequence(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, cueCatalogGetEmptyRenderFilenameResponseBody)
	}))
	defer ts.Close()

	t.Run("table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "sequence=wake-up-mh-test filename=- assets=0") {
			t.Errorf("table stdout = %q, want the render line to show filename=- distinctly from the populated sequence", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cmdCueCatalog([]string{"get", "--server", ts.URL, "-output", "json", "node-1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if code != exitOK {
			t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, `"filename": ""`) {
			t.Errorf("-output json = %q, want key \"filename\" present with an empty value, never dropped", out)
		}
	})
}

// TestCueCatalogResponseMatchesOpenAPIAcknowledgement pins
// cueCatalogResponse's three acknowledgement fields to
// api/openapi.yaml's CueCatalogResponse schema so the two cannot drift
// apart again: acknowledgedStatus must stay the one required field of
// the three (string, no null variant), and acknowledgedRevision /
// acknowledgedAt must stay optional (absent, never null, when the node
// has never acknowledged a catalog).
func TestCueCatalogResponseMatchesOpenAPIAcknowledgement(t *testing.T) {
	path := filepath.Join("..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	// Components.Schemas decodes each schema as a raw yaml.Node first (not
	// a typed struct): other schemas in this document use a type/format
	// shape this test does not model (e.g. oneOf lists), and a fully
	// typed decode of the whole components.schemas map fails on those
	// unrelated entries before it ever reaches CueCatalogResponse.
	var doc struct {
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s as YAML: %v", path, err)
	}

	node, ok := doc.Components.Schemas["CueCatalogResponse"]
	if !ok {
		t.Fatalf("%s: components.schemas.CueCatalogResponse not found", path)
	}
	var schema struct {
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Type   string `yaml:"type"`
			Format string `yaml:"format"`
		} `yaml:"properties"`
	}
	if err := node.Decode(&schema); err != nil {
		t.Fatalf("%s: decoding components.schemas.CueCatalogResponse: %v", path, err)
	}

	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}

	// jsonTags maps cueCatalogResponse's own Go field json name to whether
	// its struct tag carries omitempty, so this test checks the CLI's
	// actual wire type, not a hand-copied description of it.
	jsonTags := map[string]bool{}
	rt := reflect.TypeOf(cueCatalogResponse{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		omitempty := false
		for _, p := range parts[1:] {
			if p == "omitempty" {
				omitempty = true
			}
		}
		jsonTags[name] = omitempty
	}

	for _, field := range []struct {
		name       string
		wantType   string
		wantFormat string
		required   bool
	}{
		{"acknowledgedStatus", "string", "", true},
		{"acknowledgedRevision", "string", "", false},
		{"acknowledgedAt", "string", "date-time", false},
	} {
		prop, ok := schema.Properties[field.name]
		if !ok {
			t.Errorf("CueCatalogResponse.properties missing %q", field.name)
			continue
		}
		if prop.Type != field.wantType {
			t.Errorf("CueCatalogResponse.properties.%s.type = %q, want %q", field.name, prop.Type, field.wantType)
		}
		if prop.Format != field.wantFormat {
			t.Errorf("CueCatalogResponse.properties.%s.format = %q, want %q", field.name, prop.Format, field.wantFormat)
		}
		if required[field.name] != field.required {
			t.Errorf("CueCatalogResponse required[%s] = %v, want %v", field.name, required[field.name], field.required)
		}

		omitempty, ok := jsonTags[field.name]
		if !ok {
			t.Errorf("cueCatalogResponse has no field with json tag %q", field.name)
			continue
		}
		// required in the contract means the CLI's own field must never
		// be dropped by json.Marshal, i.e. no omitempty.
		if wantOmitempty := !field.required; omitempty != wantOmitempty {
			t.Errorf("cueCatalogResponse field with json tag %q: omitempty = %v, want %v (contract required = %v)",
				field.name, omitempty, wantOmitempty, field.required)
		}
	}
}
