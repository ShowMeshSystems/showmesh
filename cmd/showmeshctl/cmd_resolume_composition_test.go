package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testResolumeCompositionUploadResponse is shaped EXACTLY like the
// coordinator's real wire type (v1.ResolumeCompositionUploadResponse and
// v1.ResolumeCompositionSummary, internal/coordinator/api/v1/types.go) —
// review finding A's own fix: an earlier version of this fixture included
// only the fields the CLI's OWN (then-wrong) types happened to decode,
// which is exactly how the mismatch stayed invisible. sizeBytes,
// columnCount and decks[].closed are real server fields this fixture
// previously omitted entirely.
const testResolumeCompositionUploadResponse = `{
	"revision": 3,
	"activatedAt": "2026-08-14T19:47:37.605065Z",
	"composition": {
		"name": "Christmas 25",
		"sourceFilename": "Christmas 25.avc",
		"contentHash": "sha256:9f86d0",
		"sizeBytes": 407344,
		"writtenBy": {"product": "Resolume Arena", "major": 7, "minor": 23, "micro": 2, "revision": 51094},
		"canvas": {"width": 3000, "height": 1440},
		"decks": [{"id": "1733100600915", "name": "Main", "closed": false, "clipCount": 26}],
		"layerCount": 18,
		"layerGroupCount": 3,
		"columnCount": 14,
		"clipCount": 36,
		"persistentClipCount": 4
	}
}`

// TestCmdResolumeCompositionUploadSendsRealMultipartBody proves `upload`
// issues a genuine multipart/form-data POST, with the file part named
// "file" and the uploaded file's exact bytes intact — asserted against a
// real httptest.Server parsing the multipart body with the standard
// library's own mime/multipart reader, not a stub that only inspects
// what this program claims to have sent.
func TestCmdResolumeCompositionUploadSendsRealMultipartBody(t *testing.T) {
	const fileContents = "<CompositionFile><Name value=\"Christmas 25\"/></CompositionFile>"

	var gotMethod, gotPath, gotFilename, gotContentType string
	var gotBytes []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("server: ParseMultipartForm: %v", err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("server: FormFile(\"file\"): %v", err)
		}
		defer func() { _ = file.Close() }()
		gotFilename = header.Filename
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(file); err != nil {
			t.Fatalf("server: reading uploaded file: %v", err)
		}
		gotBytes = buf.Bytes()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testResolumeCompositionUploadResponse)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "Christmas 25.avc")
	if err := os.WriteFile(file, []byte(fileContents), 0o600); err != nil {
		t.Fatalf("write composition file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", ts.URL, "--token", "t", file}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/config/resolume/composition" {
		t.Errorf("path = %q, want /api/v1/config/resolume/composition", gotPath)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want a multipart/form-data prefix", gotContentType)
	}
	if gotFilename != "Christmas 25.avc" {
		t.Errorf("uploaded filename = %q, want %q", gotFilename, "Christmas 25.avc")
	}
	if string(gotBytes) != fileContents {
		t.Errorf("uploaded bytes = %q, want %q (the file's exact contents)", gotBytes, fileContents)
	}
}

// TestCmdResolumeCompositionUploadRendersParsedSummary proves `upload`
// prints what the coordinator reports it parsed — composition name, the
// Arena version that wrote it, and deck names — per ADR-032 decision 7:
// the result is what was found, never a bare success indicator.
func TestCmdResolumeCompositionUploadRendersParsedSummary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testResolumeCompositionUploadResponse)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "Christmas 25.avc")
	if err := os.WriteFile(file, []byte("<x/>"), 0o600); err != nil {
		t.Fatalf("write composition file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", ts.URL, "--token", "t", file}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Christmas 25") {
		t.Errorf("stdout = %q, want the composition name", out)
	}
	if !strings.Contains(out, "Resolume Arena") || !strings.Contains(out, "7.23.2") {
		t.Errorf("stdout = %q, want the Arena version that wrote the file", out)
	}
	if !strings.Contains(out, "Main") {
		t.Errorf("stdout = %q, want the deck name", out)
	}
	if !strings.Contains(out, "26") {
		t.Errorf("stdout = %q, want the deck's clip count", out)
	}
}

// TestCmdResolumeCompositionUploadMissingFileFailsBeforeRequest is the
// task's own flagged "vacuous if written carelessly" case: a missing path
// must fail as a usage error, and — this is the part a careless test
// would not check — must never let a single byte reach the network. The
// handler below fails the test outright if it is ever invoked at all.
func TestCmdResolumeCompositionUploadMissingFileFailsBeforeRequest(t *testing.T) {
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", ts.URL, "--token", "t", "/no/such/file.avc"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
	if reached {
		t.Error("the server handler was reached; a missing file must fail before any request is sent")
	}
}

// TestCmdResolumeCompositionUploadDirectoryFailsBeforeRequest is the
// unreadable-path half of the same rule, using a directory (unreadable AS
// A FILE on every platform this test needs to run on, unlike a
// permission-denied file, which root ignores) so the test is not flaky
// under a sandboxed or root test runner.
func TestCmdResolumeCompositionUploadDirectoryFailsBeforeRequest(t *testing.T) {
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", ts.URL, "--token", "t", dir}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d); stderr=%s", code, exitUsage, stderr.String())
	}
	if reached {
		t.Error("the server handler was reached; a directory path must fail before any request is sent")
	}
}

// TestCmdResolumeCompositionShowNothingStored proves `show` renders a 404
// (no composition uploaded yet) as the plain, expected state the wire
// contract says it is — a one-line remedy, not a scary failure message —
// while still exiting exitNotFound, matching `config get`'s identical
// unset-configuration case so a caller branching on $? sees the same
// signal from both configuration surfaces.
func TestCmdResolumeCompositionShowNothingStored(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no Resolume composition has been stored yet"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "show", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound (%d) — matching \"config get\"'s own unset-configuration case; stderr=%s", code, exitNotFound, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "no Resolume composition") && !strings.Contains(out, "uploaded yet") {
		t.Errorf("stdout = %q, want a plain statement that nothing is stored", out)
	}
	if !strings.Contains(out, "upload") {
		t.Errorf("stdout = %q, want it to say how to fix it (the upload command)", out)
	}
}

// testResolumeCompositionResponse is shaped EXACTLY like the coordinator's
// real wire type (v1.ResolumeCompositionResponse,
// internal/coordinator/api/v1/types.go) — review finding A's own fix.
// layerGroups is {id, index}, layers is {id, index, layerGroupIndex}, and
// columns is {id, deckId, index}: none of the three carry a "name" on the
// wire at all (there is no name for a layer group or a column anywhere in
// the .avc format, and a layer's own real name does not exist in the file
// either — only its index and group membership do, per
// pkg/resolumecomp.Layer's own doc comment). An earlier version of this
// fixture invented a "name" field for all three, which is exactly the
// defect this fixture exists to catch: it silently agreed with the CLI's
// own wrong types instead of the server's real contract.
//
// layer-2 has NO layerGroupIndex (the composition-has-no-groups-for-this-
// layer case, omitted rather than sent as null) and clip-2/clip-p1 omit
// width/height/transportTypeIndex individually, exercising review finding
// B's per-field omission (a clip with no such param at all) independent of
// whether the composition has groups at all.
const testResolumeCompositionResponse = `{
	"revision": 3,
	"activatedAt": "2026-08-14T19:47:37.605065Z",
	"composition": {
		"name": "Christmas 25",
		"sourceFilename": "Christmas 25.avc",
		"contentHash": "sha256:9f86d0",
		"sizeBytes": 407344,
		"writtenBy": {"product": "Resolume Arena", "major": 7, "minor": 23, "micro": 2, "revision": 51094},
		"canvas": {"width": 3000, "height": 1440},
		"decks": [{"id": "deck-a", "name": "Main", "closed": false, "clipCount": 2}, {"id": "deck-b", "name": "Halloween", "closed": true, "clipCount": 1}],
		"layerCount": 2,
		"layerGroupCount": 1,
		"columnCount": 2,
		"clipCount": 3,
		"persistentClipCount": 1
	},
	"decks": [{"id": "deck-a", "name": "Main", "closed": false, "clipCount": 2}, {"id": "deck-b", "name": "Halloween", "closed": true, "clipCount": 1}],
	"layerGroups": [{"id": "lg-1", "index": 0}],
	"layers": [{"id": "layer-1", "index": 0, "layerGroupIndex": 0}, {"id": "layer-2", "index": 1}],
	"columns": [{"id": "col-1", "deckId": "deck-a", "index": 1}, {"id": "col-2", "deckId": "deck-b", "index": 1}],
	"clips": [
		{"id": "clip-1", "deckId": "deck-a", "layerIndex": 1, "columnIndex": 1, "name": "Snow", "transportTypeIndex": 0, "sourcePath": "/media/snow.mp4", "width": 1920, "height": 1080},
		{"id": "clip-2", "deckId": "deck-a", "layerIndex": 1, "columnIndex": 2, "name": "Wreath", "sourcePath": "/media/wreath.mp4"},
		{"id": "clip-3", "deckId": "deck-b", "layerIndex": 1, "columnIndex": 1, "name": "Pumpkin", "transportTypeIndex": 0, "sourcePath": "/media/pumpkin.mp4", "width": 1920, "height": 1080}
	],
	"persistentClips": [
		{"id": "clip-p1", "layerIndex": 2, "columnIndex": 1, "name": "Countdown", "sourcePath": "/media/countdown.mp4"}
	]
}`

// TestCmdResolumeCompositionShowJSONEmitsRawDocument proves `--json`
// prints the full stored document (revision, composition summary, and
// the id map's clips with their deckId) unmodified — the form another
// tool would consume. It also decodes stdout back into this program's own
// resolumeCompositionResponse and checks EVERY field of the id map
// (layerGroups.index, layers.index and layerGroupIndex, columns.deckId
// and index), not only the clip/deckId assertions this test had before —
// review finding A: those three types used to declare {id, name}, a
// field the server never sends, while dropping index/layerGroupIndex/
// deckId — the fields that actually carry the structural relations ADR-032
// decision 1 exists to store. Before this fix, testResolumeCompositionResponse
// (this test's own fixture) was built with a "name" field on all three,
// which is exactly how the mismatch between this program's types and the
// server's real contract stayed invisible: the fixture agreed with the
// wrong code instead of the real one. It is now built from the server's
// actual shape (see that constant's own doc comment), so decoding it with
// the CORRECT types below is the same check `showmeshctl`'s own
// independence rule (doc.go, importgraph_test.go) exists to get: an
// independent transcription is supposed to break on drift.
func TestCmdResolumeCompositionShowJSONEmitsRawDocument(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testResolumeCompositionResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "show", "--json", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var got resolumeCompositionResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout did not decode as resolumeCompositionResponse: %v; stdout=%s", err, stdout.String())
	}
	if got.Revision != 3 {
		t.Errorf("revision = %d, want 3", got.Revision)
	}
	if got.Composition.Name != "Christmas 25" {
		t.Errorf("composition.name = %q, want %q", got.Composition.Name, "Christmas 25")
	}
	if got.Composition.SizeBytes != 407344 {
		t.Errorf("composition.sizeBytes = %d, want 407344", got.Composition.SizeBytes)
	}
	if got.Composition.ColumnCount != 2 {
		t.Errorf("composition.columnCount = %d, want 2", got.Composition.ColumnCount)
	}
	if len(got.Composition.Decks) == 0 || !got.Composition.Decks[1].Closed {
		t.Errorf("composition.decks[1].closed = %v, want true (deck-b)", got.Composition.Decks)
	}
	if len(got.Clips) != 3 {
		t.Fatalf("len(clips) = %d, want 3", len(got.Clips))
	}
	if got.Clips[0].DeckID == nil || *got.Clips[0].DeckID != "deck-a" {
		t.Errorf("clips[0].deckId = %v, want \"deck-a\"", got.Clips[0].DeckID)
	}
	if len(got.PersistentClips) != 1 || got.PersistentClips[0].DeckID != nil {
		t.Errorf("persistentClips = %+v, want exactly one entry with a nil deckId", got.PersistentClips)
	}

	// The id map's structural relations: this is the part an earlier
	// version of resolumeLayerGroup/resolumeLayer/resolumeColumn (all
	// declared {id, name}) could never have decoded correctly, because
	// none of these fields existed on those old types at all.
	if len(got.LayerGroups) != 1 || got.LayerGroups[0].ID != "lg-1" || got.LayerGroups[0].Index != 0 {
		t.Errorf("layerGroups = %+v, want [{id:lg-1 index:0}]", got.LayerGroups)
	}
	if len(got.Layers) != 2 {
		t.Fatalf("len(layers) = %d, want 2", len(got.Layers))
	}
	if got.Layers[0].Index != 0 || got.Layers[0].LayerGroupIndex == nil || *got.Layers[0].LayerGroupIndex != 0 {
		t.Errorf("layers[0] = %+v, want index:0 layerGroupIndex:0", got.Layers[0])
	}
	if got.Layers[1].Index != 1 || got.Layers[1].LayerGroupIndex != nil {
		t.Errorf("layers[1] = %+v, want index:1 layerGroupIndex:nil (omitted on the wire)", got.Layers[1])
	}
	if len(got.Columns) != 2 {
		t.Fatalf("len(columns) = %d, want 2", len(got.Columns))
	}
	if got.Columns[0].DeckID != "deck-a" || got.Columns[0].Index != 1 {
		t.Errorf("columns[0] = %+v, want deckId:deck-a index:1", got.Columns[0])
	}
	if got.Columns[1].DeckID != "deck-b" || got.Columns[1].Index != 1 {
		t.Errorf("columns[1] = %+v, want deckId:deck-b index:1", got.Columns[1])
	}

	// Review finding B: an absent transportTypeIndex/width/height must
	// decode to nil, not a plausible-looking 0.
	if got.Clips[1].TransportTypeIndex != nil || got.Clips[1].Width != nil || got.Clips[1].Height != nil {
		t.Errorf("clips[1] (clip-2, no transportType/width/height in the fixture) = %+v, want all three nil", got.Clips[1])
	}
	if got.Clips[0].TransportTypeIndex == nil || *got.Clips[0].TransportTypeIndex != 0 {
		t.Errorf("clips[0].transportTypeIndex = %v, want a present 0 (measured, not absent)", got.Clips[0].TransportTypeIndex)
	}
}

// testResolumeCompositionPersistentClipNoDeckIDResponse is a minimal,
// hand-built stored-composition body whose ONLY clip is persistent and
// therefore, per ADR-032 decision 6, carries no "deckId" key on the wire
// at all — "PersistentClips ... live outside any deck and resolve
// regardless of selection". It also carries one top-level key
// ("futureField") this program's own resolumeCompositionResponse does not
// model, so a test against it can distinguish "genuinely passed the
// server's bytes through unmodified" from "happened to decode and
// re-encode to something that looks similar" — a decode-then-re-marshal
// implementation drops an unknown field silently (this program's own
// documented, deliberate behavior for its OTHER --output json commands;
// see main.go's --output json flag help), which this fixture is built to
// catch.
const testResolumeCompositionPersistentClipNoDeckIDResponse = `{"revision":7,"activatedAt":"2026-08-14T00:00:00Z","futureField":"unmodeled-by-this-cli-build","composition":{"name":"X","sourceFilename":"x.avc","contentHash":"sha256:abc","writtenBy":{"product":"Arena","major":7,"minor":23,"micro":2,"revision":1},"canvas":{"width":100,"height":100},"decks":[],"layerCount":0,"layerGroupCount":0,"clipCount":0,"persistentClipCount":1},"decks":[],"layerGroups":[],"layers":[],"columns":[],"clips":[],"persistentClips":[{"id":"clip-p1","layerIndex":0,"columnIndex":0,"name":"Countdown","transportTypeIndex":0,"sourcePath":"/media/countdown.mp4","width":10,"height":10}]}`

// TestCmdResolumeCompositionShowJSONOmitsKeyTheServerOmitted is this
// task's own named deliverable: a key the server never sent (here,
// "deckId" on the composition's one clip, a persistent one) must not
// appear anywhere in the CLI's JSON output, under EITHER spelling of the
// JSON flag. Before this fix, decoding into resolumeClip's
// `DeckID *string` field and re-marshaling it (printJSON) turned that
// absence into an invented `"deckId":null` — a key the coordinator never
// sent, which is exactly what this test would have caught: "printJSON
// re-serializes this CLI's OWN decoded structs, not the coordinator's raw
// response bytes" (main.go's own --output json documentation) is the
// right behavior for the rest of this program, and the wrong one for a
// subcommand whose own help text calls its output "the raw document".
func TestCmdResolumeCompositionShowJSONOmitsKeyTheServerOmitted(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"json flag", []string{"composition", "show", "--json"}},
		{"output json flag", []string{"composition", "show", "--output", "json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, testResolumeCompositionPersistentClipNoDeckIDResponse)
			}))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdResolume(append(tc.args, "--server", ts.URL), &stdout, &stderr, time.Now)
			if code != exitOK {
				t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
			}

			out := stdout.String()
			if strings.Contains(out, "deckId") {
				t.Errorf("stdout contains %q, but the server never sent that key for this clip (it is persistent); stdout=%s", "deckId", out)
			}
		})
	}
}

// TestCmdResolumeCompositionShowJSONIsVerbatimServerBytes proves the
// stronger property TestCmdResolumeCompositionShowJSONOmitsKeyTheServerOmitted
// implies but does not directly assert: stdout is the coordinator's exact
// response body, not merely a re-encoding that happens to omit "deckId".
// The fixture's "futureField" (unmodeled by resolumeCompositionResponse)
// is the tripwire: a decode-then-re-marshal path drops it silently, while
// a genuine byte pass-through cannot.
func TestCmdResolumeCompositionShowJSONIsVerbatimServerBytes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testResolumeCompositionPersistentClipNoDeckIDResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "show", "--json", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	want := testResolumeCompositionPersistentClipNoDeckIDResponse + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q,\nwant the server's exact response body (plus a trailing newline) = %q", stdout.String(), want)
	}
	if !strings.Contains(stdout.String(), `"futureField":"unmodeled-by-this-cli-build"`) {
		t.Errorf("stdout does not contain the server's unmodeled \"futureField\" key; a re-marshal through this program's own structs would silently drop it, want a verbatim pass-through: stdout=%s", stdout.String())
	}
}

// TestCmdResolumeCompositionUploadJSONIsVerbatimServerBytes is the
// upload-side half of the same check (the task's own "check whether
// --output json on the other new subcommand has the same problem"):
// stdout must be the coordinator's exact response bytes, including a
// top-level field (again "futureField") resolumeCompositionUploadResponse
// does not model, which a decode-then-re-marshal implementation would
// silently drop.
func TestCmdResolumeCompositionUploadJSONIsVerbatimServerBytes(t *testing.T) {
	const respBody = `{"revision":9,"activatedAt":"2026-08-14T00:00:00Z","futureField":"unmodeled-by-this-cli-build","composition":{"name":"X","sourceFilename":"x.avc","contentHash":"sha256:abc","writtenBy":{"product":"Arena","major":7,"minor":23,"micro":2,"revision":1},"canvas":{"width":100,"height":100},"decks":[],"layerCount":0,"layerGroupCount":0,"clipCount":0,"persistentClipCount":0}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, respBody)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "Christmas 25.avc")
	if err := os.WriteFile(file, []byte("<x/>"), 0o600); err != nil {
		t.Fatalf("write composition file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--output", "json", "--server", ts.URL, "--token", "t", file}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	want := respBody + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q,\nwant the server's exact response body (plus a trailing newline) = %q", stdout.String(), want)
	}
	if !strings.Contains(stdout.String(), `"futureField":"unmodeled-by-this-cli-build"`) {
		t.Errorf("stdout does not contain the server's unmodeled \"futureField\" key; a re-marshal through this program's own structs would silently drop it, want a verbatim pass-through: stdout=%s", stdout.String())
	}
}

// TestCmdResolumeCompositionUploadForbiddenNamesScope proves a 403 is
// distinguished from a transport failure and names the missing scope —
// CLAUDE.md's own recorded inversion ("a 403 is a successful conversation
// with a healthy coordinator, not an outage") checked at this program's
// newest write.
func TestCmdResolumeCompositionUploadForbiddenNamesScope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/forbidden","title":"Forbidden","status":403,"detail":"this action requires the config:write scope"}`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "Christmas 25.avc")
	if err := os.WriteFile(file, []byte("<x/>"), 0o600); err != nil {
		t.Fatalf("write composition file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", ts.URL, file}, &stdout, &stderr, time.Now)
	if code != exitForbidden {
		t.Fatalf("exit code = %d, want exitForbidden (%d) — distinct from exitUnreachable (%d); stderr=%s",
			code, exitForbidden, exitUnreachable, stderr.String())
	}
	if !strings.Contains(stderr.String(), "config:write") {
		t.Errorf("stderr = %q, want it to name the missing scope", stderr.String())
	}
}

// TestCmdResolumeCompositionUploadUnreachableIsDistinctFromForbidden pins
// the OTHER half of the same distinction: an actually-unreachable
// coordinator must exit exitUnreachable, never exitForbidden, so a script
// branching on $? cannot confuse "no credential accepted, coordinator is
// fine" with "coordinator is not answering at all".
func TestCmdResolumeCompositionUploadUnreachableIsDistinctFromForbidden(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Christmas 25.avc")
	if err := os.WriteFile(file, []byte("<x/>"), 0o600); err != nil {
		t.Fatalf("write composition file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload", "--server", "http://127.0.0.1:1", file}, &stdout, &stderr, time.Now)
	if code != exitUnreachable {
		t.Errorf("exit code = %d, want exitUnreachable (%d); stderr=%s", code, exitUnreachable, stderr.String())
	}
}

// TestCmdResolumeCompositionShowGroupsClipsByDeck is decision 6's own
// test: every clip must appear under its own deck's header (Main before
// its two clips, Halloween before its one clip), and a persistent clip
// must appear under a clearly separate "no deck" section rather than
// folded into either deck or printed as a bare id anywhere.
func TestCmdResolumeCompositionShowGroupsClipsByDeck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, testResolumeCompositionResponse)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "show", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()

	mainIdx := strings.Index(out, "Main (deck-a)")
	halloweenIdx := strings.Index(out, "Halloween (deck-b)")
	snowIdx := strings.Index(out, "clip-1")
	wreathIdx := strings.Index(out, "clip-2")
	pumpkinIdx := strings.Index(out, "clip-3")
	persistentIdx := strings.Index(out, "persistent clips")
	countdownIdx := strings.Index(out, "clip-p1")

	for name, idx := range map[string]int{
		"deck Main header": mainIdx, "deck Halloween header": halloweenIdx,
		"clip-1": snowIdx, "clip-2": wreathIdx, "clip-3": pumpkinIdx,
		"persistent clips section": persistentIdx, "clip-p1": countdownIdx,
	} {
		if idx == -1 {
			t.Fatalf("stdout does not contain %q; full output:\n%s", name, out)
		}
	}

	if !(mainIdx < snowIdx && snowIdx < halloweenIdx) {
		t.Errorf("clip-1 (deck-a) must appear after the Main deck header and before the Halloween deck header; stdout=%s", out)
	}
	if !(mainIdx < wreathIdx && wreathIdx < halloweenIdx) {
		t.Errorf("clip-2 (deck-a) must appear after the Main deck header and before the Halloween deck header; stdout=%s", out)
	}
	if !(halloweenIdx < pumpkinIdx) {
		t.Errorf("clip-3 (deck-b) must appear after the Halloween deck header; stdout=%s", out)
	}
	if !(persistentIdx < countdownIdx) {
		t.Errorf("clip-p1 (persistent) must appear after the persistent clips section header; stdout=%s", out)
	}
	// The load-bearing negative check: a persistent clip must never
	// appear ABOVE the persistent section, which would mean it got
	// silently folded into a deck's clip list.
	if countdownIdx < pumpkinIdx {
		t.Errorf("clip-p1 (persistent) appeared before clip-3 (a deck's clip); persistent clips must never be mixed into a deck's own list; stdout=%s", out)
	}
}

// TestTopLevelHelpDoesNotClaimResolumeCompositionShowIsOpenRead is review
// finding E: the top-level command list (main.go's printTopLevelUsage)
// used to describe "resolume composition show" as "(open read)", while the
// subcommand's own usage text (printResolumeCompositionUsage, this file)
// correctly says every subcommand "requires the config:write scope (admin
// only)". An operator skimming the front page of `showmeshctl help` would
// be told the opposite of what the gate actually does. This test locks in
// that the front page and the subcommand agree.
func TestTopLevelHelpDoesNotClaimResolumeCompositionShowIsOpenRead(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "resolume composition show") && strings.Contains(out, "(open read)") {
		t.Errorf("stdout claims \"resolume composition show\" is an open read, but it requires config:write; stdout=%s", out)
	}
	if !strings.Contains(out, "resolume composition show") {
		t.Errorf("stdout does not mention \"resolume composition show\" at all; stdout=%s", out)
	}
}

func TestCmdResolumeCompositionUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdResolumeCompositionNoSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolume(nil, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdResolumeCompositionHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "config:write") {
		t.Errorf("stdout = %q, want the help text to name the scope upload requires", stdout.String())
	}
}

func TestCmdResolumeCompositionUploadRequiresPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "upload"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdResolumeCompositionShowRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdResolume([]string{"composition", "show", "extra"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}
