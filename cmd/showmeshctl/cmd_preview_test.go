package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type previewReq struct {
	method, path, escapedPath, ifMatch, body string
}

const previewTilesJSON = `{"serverTime":"2026-10-15T12:00:00Z","gateway":{"state":"reachable","message":"","observedAt":"2026-10-15T12:00:00Z"},
"tiles":[
{"key":"ndi:NODE-01 (matrix-main)","kind":"ndi","label":"node-01 · matrix-main","nodeId":"node-01","surfaceId":"matrix-main","state":"idle","message":"","hidden":false,"observedAt":"2026-10-15T12:00:00Z"},
{"key":"camera:driveway","kind":"camera","label":"Driveway","cameraId":"driveway","state":"error","message":"The camera did not answer. Check its address and that it is on.","hidden":true,"observedAt":"2026-10-15T12:00:00Z"},
{"key":"ndi:ARENA (Composition)","kind":"ndi","label":"ARENA (Composition)","state":"live","message":"","hidden":false,"observedAt":"2026-10-15T12:00:00Z"}]}`

const previewLayoutJSON = `{"serverTime":"2026-10-15T12:00:00Z","kind":"dashboard.layout","revision":4,
"payload":{"previews":{"order":["camera:driveway"],"hidden":["camera:driveway"]}},
"updatedAt":"2026-10-15T11:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`

// The extra token and url fields stand for a coordinator leaking a secret by mistake; no command may print them.
const previewGatewayJSON = `{"serverTime":"2026-10-15T12:00:00Z","kind":"preview.gateway","revision":1,"payload":{"url":"http://gw:8095","tokenSet":true,"token":"LEAKED-TOKEN"},"updatedAt":"2026-10-15T12:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`

const previewCameraJSON = `{"serverTime":"2026-10-15T12:00:00Z","kind":"preview.camera","id":"driveway","revision":1,"payload":{"name":"Driveway","enabled":true,"urlSet":true,"urlDisplay":"rtsp://192.168.1.20:37379","url":"rtsp://LEAKED-URL/stream"},"updatedAt":"2026-10-15T12:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`

const previewNDIJSON = `{"serverTime":"2026-10-15T12:00:00Z","kind":"preview.ndi","id":"far-barn","revision":2,"payload":{"label":"Far barn","ndiName":"BARN-02 (Composition)","address":"10.0.20.15:5961","enabled":true},"updatedAt":"2026-10-15T12:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api"}`

// previewServer plays the coordinator for every preview command. layoutStatus 404 means no layout is stored.
func previewServer(t *testing.T, layoutStatus int, snapshotStatus int) (*httptest.Server, *[]previewReq) {
	t.Helper()
	var seen []previewReq
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, previewReq{r.Method, r.URL.Path, r.URL.EscapedPath(), r.Header.Get("If-Match"), string(body)})
		w.Header().Set("ShowMesh-API-Version", "1")
		problem := func(status int, detail string) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"https://showmesh.dev/problems/x","title":"x","status":` + http.StatusText(status)[:0] + itoa(status) + `,"detail":"` + detail + `","serverTime":"2026-10-15T12:00:00Z"}`))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/previews":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewTilesJSON))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/snapshot"):
			if snapshotStatus != http.StatusOK {
				problem(snapshotStatus, "The preview gateway cannot be reached. Check that it is running.")
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("\xff\xd8frame"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config/dashboard.layout":
			if layoutStatus != http.StatusOK {
				problem(layoutStatus, "No dashboard layout has been saved yet.")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewLayoutJSON))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/revisions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"serverTime":"2026-10-15T12:00:00Z","kind":"x","revisions":[{"revision":2,"createdAt":"2026-10-15T11:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"ada","source":"api","note":"","active":true},{"revision":1,"createdAt":"2026-10-14T11:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"source":"api","note":"","active":false}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config/preview.camera":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"serverTime":"2026-10-15T12:00:00Z","kind":"preview.camera","objects":[{"id":"driveway","label":"Driveway","show":"","currentRevision":3,"updatedAt":"2026-10-15T11:00:00Z"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config/preview.gateway":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewGatewayJSON))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config/preview.ndi":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"serverTime":"2026-10-15T12:00:00Z","kind":"preview.ndi","objects":[{"id":"far-barn","label":"Far barn","show":"","currentRevision":2,"updatedAt":"2026-10-15T11:00:00Z"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/config/preview.ndi/") && r.Method != http.MethodDelete:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewNDIJSON))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/config/preview.camera/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewCameraJSON))
		case r.Method == http.MethodGet:
			problem(http.StatusNotFound, "Not saved yet.")
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/config/dashboard.layout":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Replace(previewLayoutJSON, `"revision":4`, `"revision":5`, 1)))
		case r.URL.Path == "/api/v1/config/preview.gateway":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewGatewayJSON))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(previewCameraJSON))
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func runPreview(t *testing.T, ts *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append(append([]string{"preview"}, args...), "--server", ts.URL)
	code := run(full, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-15T12:00:00Z")))
	return code, stdout.String(), stderr.String()
}

func writeSecret(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreviewListPrintsGatewayAndTiles(t *testing.T) {
	ts, _ := previewServer(t, http.StatusOK, http.StatusOK)
	code, out, errOut := runPreview(t, ts, "list")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"gateway: reachable", "ndi:NODE-01 (matrix-main)", "node-01 · matrix-main", "camera:driveway", "yes", "The camera did not answer."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = runPreview(t, ts, "list", "--output", "json")
	var decoded previewsResponse
	if code != exitOK || json.Unmarshal([]byte(out), &decoded) != nil || len(decoded.Tiles) != 3 {
		t.Fatalf("json output (exit %d): %s", code, out)
	}
}

func TestPreviewSnapshotSavesTheFrameUnderTheEscapedKey(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	file := filepath.Join(t.TempDir(), "frame.jpg")
	code, out, errOut := runPreview(t, ts, "snapshot", "ndi:NODE-01 (matrix-main)", "-o", file)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(got), "\xff\xd8") || !strings.Contains(out, "saved") {
		t.Fatalf("file %q (%v), output %q", got, err, out)
	}
	if p := (*seen)[0].path; p != "/api/v1/previews/ndi:NODE-01 (matrix-main)/snapshot" {
		t.Fatalf("request path = %q", p)
	}

	if code, _, errOut := runPreview(t, ts, "snapshot", "camera:a/b c%d", "-o", file); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if p := (*seen)[1].escapedPath; p != "/api/v1/previews/camera:a%2Fb%20c%25d/snapshot" {
		t.Fatalf("escaped request path = %q: a key must be escaped once", p)
	}
}

func TestPreviewListKeepsTheNodeSurfaceAndCameraOfEachTile(t *testing.T) {
	ts, _ := previewServer(t, http.StatusOK, http.StatusOK)
	code, out, errOut := runPreview(t, ts, "list", "--output", "json")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var decoded struct {
		Tiles []map[string]any `json:"tiles"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Tiles[0]["nodeId"] != "node-01" || decoded.Tiles[0]["surfaceId"] != "matrix-main" || decoded.Tiles[1]["cameraId"] != "driveway" {
		t.Fatalf("tiles = %v", decoded.Tiles)
	}
}

func TestPreviewReadVerbsShowTheSameThingsTheUIShowsAndNeverASecret(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	cases := []struct {
		args []string
		path string
		want []string
	}{
		{[]string{"gateway", "get"}, "/api/v1/config/preview.gateway", []string{"http://gw:8095", "tokenSet", "true"}},
		{[]string{"camera", "list"}, "/api/v1/config/preview.camera", []string{"driveway", "Driveway", "3"}},
		{[]string{"camera", "get", "driveway"}, "/api/v1/config/preview.camera/driveway", []string{"Driveway", "rtsp://192.168.1.20:37379"}},
		{[]string{"layout", "get"}, "/api/v1/config/dashboard.layout", []string{"camera:driveway", "order", "hidden", "4"}},
		{[]string{"gateway", "revisions"}, "/api/v1/config/preview.gateway/revisions", []string{"REVISION", "ada"}},
		{[]string{"camera", "revisions", "driveway"}, "/api/v1/config/preview.camera/driveway/revisions", []string{"REVISION", "ada"}},
		{[]string{"layout", "revisions"}, "/api/v1/config/dashboard.layout/revisions", []string{"REVISION", "ada"}},
	}
	for _, tc := range cases {
		before := len(*seen)
		for _, format := range [][]string{nil, {"--output", "json"}} {
			code, out, errOut := runPreview(t, ts, append(append([]string{}, tc.args...), format...)...)
			if code != exitOK {
				t.Fatalf("%v %v: exit %d: %s", tc.args, format, code, errOut)
			}
			if strings.Contains(out, "LEAKED") {
				t.Fatalf("%v %v printed a secret:\n%s", tc.args, format, out)
			}
			if format == nil {
				for _, w := range tc.want {
					if !strings.Contains(out, w) {
						t.Fatalf("%v output lacks %q:\n%s", tc.args, w, out)
					}
				}
			} else if !json.Valid([]byte(out)) {
				t.Fatalf("%v json output is not JSON: %s", tc.args, out)
			}
		}
		if got := (*seen)[before].path; got != tc.path || (*seen)[before].method != http.MethodGet {
			t.Fatalf("%v requested %s %s, want GET %s", tc.args, (*seen)[before].method, got, tc.path)
		}
	}
}

func TestPreviewSnapshotFailureWritesNoFileAndSaysWhy(t *testing.T) {
	ts, _ := previewServer(t, http.StatusOK, http.StatusServiceUnavailable)
	file := filepath.Join(t.TempDir(), "frame.jpg")
	code, _, errOut := runPreview(t, ts, "snapshot", "camera:driveway", "-o", file)
	if code == exitOK || !strings.Contains(errOut, "cannot be reached") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a failed snapshot left a file behind")
	}
	if code, _, _ := runPreview(t, ts, "snapshot", "camera:driveway"); code != exitUsage {
		t.Fatalf("missing -o exit = %d, want usage", code)
	}
}

func TestPreviewGatewaySetReadsTheTokenFromAFile(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	tokenFile := writeSecret(t, "gw-token-4b1f\n")
	code, out, errOut := runPreview(t, ts, "gateway", "set", "--url", "http://gw:8095", "--token-file", tokenFile)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	put := (*seen)[len(*seen)-1]
	if put.method != http.MethodPut || put.path != "/api/v1/config/preview.gateway" {
		t.Fatalf("last request = %+v", put)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(put.body), &body)
	if body["url"] != "http://gw:8095" || body["token"] != "gw-token-4b1f" {
		t.Fatalf("body = %s", put.body)
	}
	if strings.Contains(out, "gw-token-4b1f") || !strings.Contains(out, "tokenSet:") {
		t.Fatalf("output = %q", out)
	}
}

func TestPreviewGatewaySetRefusesASecretOnTheCommandLine(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	if code, _, _ := runPreview(t, ts, "gateway", "set", "--token", "x", "--url", "http://gw:8095", "--token-value", "y"); code != exitUsage {
		t.Fatalf("exit = %d, want usage", code)
	}
	if code, _, _ := runPreview(t, ts, "gateway", "set"); code != exitUsage {
		t.Fatalf("no flags exit = %d, want usage", code)
	}
	if code, _, _ := runPreview(t, ts, "gateway", "set", "--token-file", writeSecret(t, "a"), "--clear-token"); code != exitUsage {
		t.Fatalf("token-file with clear-token exit = %d, want usage", code)
	}
	if code, _, _ := runPreview(t, ts, "gateway", "set", "--token-file", writeSecret(t, "  \n")); code == exitOK {
		t.Fatal("an empty token file was accepted")
	}
	for _, r := range *seen {
		if r.method == http.MethodPut {
			t.Fatalf("a refused command still wrote: %+v", r)
		}
	}
}

func TestPreviewGatewayClearTokenSendsNull(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	if code, _, errOut := runPreview(t, ts, "gateway", "set", "--clear-token"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if put := (*seen)[len(*seen)-1]; put.body != `{"token":null}` {
		t.Fatalf("body = %s", put.body)
	}
}

func TestPreviewCameraAddReadsTheAddressFromAFileAndNeverPrintsIt(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	urlFile := writeSecret(t, "rtsp://viewer:cam-pass-9@192.168.1.20:37379/a\n")
	code, out, errOut := runPreview(t, ts, "camera", "add", "driveway", "--name", "Driveway", "--url-file", urlFile)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	put := (*seen)[len(*seen)-1]
	var body map[string]any
	_ = json.Unmarshal([]byte(put.body), &body)
	if put.path != "/api/v1/config/preview.camera/driveway" || body["name"] != "Driveway" || body["url"] != "rtsp://viewer:cam-pass-9@192.168.1.20:37379/a" {
		t.Fatalf("request = %+v", put)
	}
	if strings.Contains(out+errOut, "cam-pass-9") || !strings.Contains(out, "rtsp://192.168.1.20:37379") {
		t.Fatalf("output = %q", out)
	}
	code, _, _ = runPreview(t, ts, "camera", "add", "driveway", "--disabled")
	put = (*seen)[len(*seen)-1]
	if code != exitOK || put.body != `{"enabled":false}` {
		t.Fatalf("disable: exit %d body %s", code, put.body)
	}
	if code, _, _ := runPreview(t, ts, "camera", "add", "driveway"); code != exitUsage {
		t.Fatalf("no changes exit = %d, want usage", code)
	}
	if code, _, _ := runPreview(t, ts, "camera", "add", "--name", "x"); code != exitUsage {
		t.Fatalf("no id exit = %d, want usage", code)
	}
}

func TestPreviewCameraRemoveDeletesWithConfirmation(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	code, out, errOut := runPreview(t, ts, "camera", "remove", "driveway")
	if code != exitOK || !strings.Contains(out, "driveway removed") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
	del := (*seen)[len(*seen)-1]
	if del.method != http.MethodDelete || del.path != "/api/v1/config/preview.camera/driveway" || del.body != `{"confirm":true}` {
		t.Fatalf("request = %+v", del)
	}
}

func lastPut(t *testing.T, seen *[]previewReq) previewReq {
	t.Helper()
	for i := len(*seen) - 1; i >= 0; i-- {
		if (*seen)[i].method == http.MethodPut {
			return (*seen)[i]
		}
	}
	t.Fatal("no PUT was sent")
	return previewReq{}
}

func TestPreviewLayoutMoveSendsTheWholeOrderWithTheCurrentRevision(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	code, _, errOut := runPreview(t, ts, "layout", "move", "ndi:ARENA (Composition)", "--to", "0")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	put := lastPut(t, seen)
	want := `{"previews":{"order":["ndi:ARENA (Composition)","ndi:NODE-01 (matrix-main)","camera:driveway"]}}`
	if put.body != want || put.ifMatch != `"4"` {
		t.Fatalf("PUT = %+v, want body %s", put, want)
	}
	if code, _, _ := runPreview(t, ts, "layout", "move", "ndi:ARENA (Composition)", "--to", "3"); code == exitOK {
		t.Fatal("a position past the last tile was accepted")
	}
	if code, _, e := runPreview(t, ts, "layout", "move", "camera:nope", "--to", "0"); code == exitOK || !strings.Contains(e, "preview list") {
		t.Fatalf("unknown key: exit %d stderr %q", code, e)
	}
	if code, _, _ := runPreview(t, ts, "layout", "move", "camera:driveway"); code != exitUsage {
		t.Fatalf("missing --to exit = %d, want usage", code)
	}
}

func TestPreviewLayoutHideAndShowKeepTheOtherHiddenTiles(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	if code, _, e := runPreview(t, ts, "layout", "hide", "ndi:ARENA (Composition)"); code != exitOK {
		t.Fatalf("hide exit %d: %s", code, e)
	}
	if got := lastPut(t, seen).body; got != `{"previews":{"hidden":["camera:driveway","ndi:ARENA (Composition)"]}}` {
		t.Fatalf("hide body = %s", got)
	}
	if code, _, e := runPreview(t, ts, "layout", "show", "camera:driveway"); code != exitOK {
		t.Fatalf("show exit %d: %s", code, e)
	}
	if got := lastPut(t, seen).body; got != `{"previews":{"hidden":[]}}` {
		t.Fatalf("show body = %s", got)
	}
}

func TestPreviewLayoutWorksBeforeAnyLayoutIsSaved(t *testing.T) {
	ts, seen := previewServer(t, http.StatusNotFound, http.StatusOK)
	if code, _, e := runPreview(t, ts, "layout", "hide", "ndi:ARENA (Composition)"); code != exitOK {
		t.Fatalf("exit %d: %s", code, e)
	}
	put := lastPut(t, seen)
	if put.body != `{"previews":{"hidden":["ndi:ARENA (Composition)"]}}` || put.ifMatch != "" {
		t.Fatalf("PUT = %+v", put)
	}
}

func TestPreviewNDIAddSendsOnlyWhatIsPassed(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	code, out, errOut := runPreview(t, ts, "ndi", "add", "far-barn", "--ndi-name", "BARN-02 (Composition)", "--label", "Far barn", "--address", "10.0.20.15:5961")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	put := lastPut(t, seen)
	var body map[string]any
	_ = json.Unmarshal([]byte(put.body), &body)
	if put.path != "/api/v1/config/preview.ndi/far-barn" || body["ndiName"] != "BARN-02 (Composition)" || body["label"] != "Far barn" || body["address"] != "10.0.20.15:5961" || len(body) != 3 {
		t.Fatalf("request = %+v", put)
	}
	for _, want := range []string{"far-barn", "Far barn", "BARN-02 (Composition)", "10.0.20.15:5961"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if code, _, _ := runPreview(t, ts, "ndi", "add", "far-barn", "--address", ""); code != exitOK || lastPut(t, seen).body != `{"address":""}` {
		t.Fatalf("clearing the address: exit %d body %s", code, lastPut(t, seen).body)
	}
	if code, _, _ := runPreview(t, ts, "ndi", "add", "far-barn"); code != exitUsage {
		t.Fatalf("no changes exit = %d, want usage", code)
	}
}

func TestPreviewNDIListAndRemove(t *testing.T) {
	ts, seen := previewServer(t, http.StatusOK, http.StatusOK)
	code, out, errOut := runPreview(t, ts, "ndi", "list")
	if code != exitOK || !strings.Contains(out, "far-barn") || !strings.Contains(out, "Far barn") {
		t.Fatalf("list: exit %d out %q err %q", code, out, errOut)
	}
	code, out, errOut = runPreview(t, ts, "ndi", "remove", "far-barn")
	if code != exitOK || !strings.Contains(out, "far-barn removed") {
		t.Fatalf("remove: exit %d out %q err %q", code, out, errOut)
	}
	del := (*seen)[len(*seen)-1]
	if del.method != http.MethodDelete || del.path != "/api/v1/config/preview.ndi/far-barn" || del.body != `{"confirm":true}` {
		t.Fatalf("request = %+v", del)
	}
}
