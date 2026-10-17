package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/preview"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const (
	previewCameraSecretURL = "rtsp://viewer:cam-pass-9@192.168.1.20:37379/driveway"
	previewGatewayToken    = "gw-token-4b1f"
)

type fakePreviewService struct {
	overview  preview.Overview
	offerErr  error
	snapErr   error
	notified  int
	lastKey   string
	lastOffer string
}

func (f *fakePreviewService) Overview(context.Context) (preview.Overview, error) {
	return f.overview, nil
}

func (f *fakePreviewService) Offer(_ context.Context, key string, offer []byte) ([]byte, error) {
	f.lastKey, f.lastOffer = key, string(offer)
	if f.offerErr != nil {
		return nil, f.offerErr
	}
	return []byte("v=0 answer"), nil
}

func (f *fakePreviewService) Snapshot(_ context.Context, key string) ([]byte, error) {
	f.lastKey = key
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	return []byte("\xff\xd8jpeg"), nil
}

func (f *fakePreviewService) Notify() { f.notified++ }

func previewTestAPI(t *testing.T, svc *fakePreviewService, opts ...func(*Options)) (api *API, st *store.Store, admin, viewer string) {
	t.Helper()
	idSvc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin = mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "admin-1", identity.RoleAdmin).ID)
	viewer = mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "viewer-1", identity.RoleViewer).ID)
	deps := configTestDeps(idSvc, st)
	if svc != nil {
		deps.Previews = svc
	}
	o := Options{Clock: fixedClock(testNow), Logger: testLogger()}
	for _, f := range opts {
		f(&o)
	}
	return New(deps, o), st, admin, viewer
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func send(t *testing.T, api *API, method, path, body, token string) (*http.Response, []byte) {
	t.Helper()
	return doRawRequest(t, api.Handler, newJSONRequest(t, method, path, body, bearer(token)))
}

func TestPreviewCameraNeverReturnsItsURL(t *testing.T) {
	svc := &fakePreviewService{}
	api, st, admin, viewer := previewTestAPI(t, svc)
	path := "/api/v1/config/preview.camera/driveway"

	if resp, _ := send(t, api, http.MethodPut, path, `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, viewer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT status = %d, want 403", resp.StatusCode)
	}
	resp, body := send(t, api, http.MethodPut, path, `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	p := m["payload"].(map[string]any)
	if p["name"] != "Driveway" || p["enabled"] != true || p["urlSet"] != true || p["urlDisplay"] != "rtsp://192.168.1.20:37379" {
		t.Fatalf("payload = %v", p)
	}
	if svc.notified != 1 {
		t.Fatalf("service notified %d times, want 1", svc.notified)
	}

	v, ok, err := st.GetCredential(context.Background(), config.PreviewCameraConfigKind, "driveway", config.PreviewCameraURLCredentialField)
	if err != nil || !ok || v != previewCameraSecretURL {
		t.Fatalf("stored credential = %q, %v, %v", v, ok, err)
	}

	for _, target := range []string{path, path + "/revisions", "/api/v1/config/preview.camera", "/api/v1/config/preview.camera/driveway/revisions"} {
		resp, got := doRequest(t, api.Handler, http.MethodGet, target, bearer(admin))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, resp.StatusCode, got)
		}
		for _, secret := range []string{"cam-pass-9", "/driveway\"", "viewer:", "rtsp://viewer"} {
			if strings.Contains(string(got), secret) {
				t.Fatalf("GET %s leaked %q: %s", target, secret, got)
			}
		}
	}
	rev, err := st.GetConfigRevision(context.Background(), config.PreviewCameraConfigKind, "driveway", 1)
	if err != nil || strings.Contains(rev.PayloadJSON, "cam-pass-9") {
		t.Fatalf("stored revision payload %q leaks the URL (%v)", rev.PayloadJSON, err)
	}
}

func TestPreviewCameraPartialUpdateKeepsTheURL(t *testing.T) {
	api, st, admin, _ := previewTestAPI(t, nil)
	path := "/api/v1/config/preview.camera/driveway"
	send(t, api, http.MethodPut, path, `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, admin)

	resp, body := send(t, api, http.MethodPut, path, `{"enabled":false}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	p := decodeMap(t, body)["payload"].(map[string]any)
	if p["enabled"] != false || p["name"] != "Driveway" || p["urlSet"] != true || p["urlDisplay"] != "rtsp://192.168.1.20:37379" {
		t.Fatalf("payload = %v", p)
	}
	if v, _, _ := st.GetCredential(context.Background(), config.PreviewCameraConfigKind, "driveway", config.PreviewCameraURLCredentialField); v != previewCameraSecretURL {
		t.Fatalf("credential after partial update = %q", v)
	}
}

func TestPreviewCameraRefusesBadWrites(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	cases := []struct{ name, path, body string }{
		{"http scheme", "/api/v1/config/preview.camera/c1", `{"name":"C","url":"http://192.168.1.20/video"}`},
		{"no url on create", "/api/v1/config/preview.camera/c1", `{"name":"C"}`},
		{"no name on create", "/api/v1/config/preview.camera/c1", `{"url":"rtsp://192.168.1.20/a"}`},
		{"empty url", "/api/v1/config/preview.camera/c1", `{"name":"C","url":""}`},
		{"null name", "/api/v1/config/preview.camera/c1", `{"name":null,"url":"rtsp://192.168.1.20/a"}`},
		{"unknown field", "/api/v1/config/preview.camera/c1", `{"name":"C","url":"rtsp://192.168.1.20/a","token":"x"}`},
		{"space in url", "/api/v1/config/preview.camera/c1", `{"name":"C","url":"rtsp://u:p@cam:554/a b"}`},
		{"tab in url", "/api/v1/config/preview.camera/c1", "{\"name\":\"C\",\"url\":\"rtsp://cam:554/a\\tb\"}"},
		{"fragment in url", "/api/v1/config/preview.camera/c1", `{"name":"C","url":"rtsp://cam:554/s#x"}`},
		{"bad id", "/api/v1/config/preview.camera/Bad_Id", `{"name":"C","url":"rtsp://192.168.1.20/a"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := send(t, api, http.MethodPut, tc.path, tc.body, admin)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
			}
		})
	}
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.camera/c1", bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a refused write created the camera: GET status = %d", resp.StatusCode)
	}
}

func TestPreviewCameraDeleteRemovesTheStoredURL(t *testing.T) {
	svc := &fakePreviewService{}
	api, st, admin, _ := previewTestAPI(t, svc)
	path := "/api/v1/config/preview.camera/driveway"
	send(t, api, http.MethodPut, path, `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, admin)

	resp, body := send(t, api, http.MethodDelete, path, `{"confirm":true}`, admin)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d: %s", resp.StatusCode, body)
	}
	if _, ok, _ := st.GetCredential(context.Background(), config.PreviewCameraConfigKind, "driveway", config.PreviewCameraURLCredentialField); ok {
		t.Fatal("the stream URL is still stored after the camera was deleted")
	}
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, path, bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", resp.StatusCode)
	}
	if resp, _ := send(t, api, http.MethodDelete, path, `{"confirm":true}`, admin); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", resp.StatusCode)
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.camera", bearer(admin))
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "driveway") {
		t.Fatalf("list after delete = %d: %s", resp.StatusCode, body)
	}
}

func TestPreviewCameraListShowsNamesOnly(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	send(t, api, http.MethodPut, "/api/v1/config/preview.camera/driveway", `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, admin)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.camera", bearer(admin))
	m := decodeMap(t, body)
	objs, _ := m["objects"].([]any)
	if resp.StatusCode != http.StatusOK || len(objs) != 1 || objs[0].(map[string]any)["label"] != "Driveway" {
		t.Fatalf("list = %d: %s", resp.StatusCode, body)
	}
}

func TestPreviewGatewayConfigNeverReturnsItsToken(t *testing.T) {
	svc := &fakePreviewService{}
	api, st, admin, viewer := previewTestAPI(t, svc)
	path := "/api/v1/config/preview.gateway"

	if resp, _ := doRequest(t, api.Handler, http.MethodGet, path, bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET before any write = %d, want 404", resp.StatusCode)
	}
	if resp, _ := send(t, api, http.MethodPut, path, `{"url":"http://192.168.130.176:8095"}`, viewer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT = %d, want 403", resp.StatusCode)
	}
	resp, body := send(t, api, http.MethodPut, path, `{"url":"http://192.168.130.176:8095","token":"`+previewGatewayToken+`"}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	if p := decodeMap(t, body)["payload"].(map[string]any); p["url"] != "http://192.168.130.176:8095" || p["tokenSet"] != true {
		t.Fatalf("payload = %v", p)
	}
	if v, _, _ := st.GetCredential(context.Background(), config.PreviewGatewayConfigKind, "default", "token"); v != previewGatewayToken {
		t.Fatalf("stored token = %q", v)
	}
	for _, target := range []string{path, path + "/revisions"} {
		_, got := doRequest(t, api.Handler, http.MethodGet, target, bearer(admin))
		if strings.Contains(string(got), previewGatewayToken) {
			t.Fatalf("GET %s leaked the token: %s", target, got)
		}
	}

	resp, body = send(t, api, http.MethodPut, path, `{"url":"http://192.168.130.177:8095"}`, admin)
	if p := decodeMap(t, body)["payload"].(map[string]any); resp.StatusCode != http.StatusOK || p["tokenSet"] != true {
		t.Fatalf("a PUT without a token must keep it: %d %s", resp.StatusCode, body)
	}
	resp, body = send(t, api, http.MethodPut, path, `{"token":null}`, admin)
	if p := decodeMap(t, body)["payload"].(map[string]any); resp.StatusCode != http.StatusOK || p["tokenSet"] != false || p["url"] != "http://192.168.130.177:8095" {
		t.Fatalf("clearing the token: %d %s", resp.StatusCode, body)
	}
	if _, ok, _ := st.GetCredential(context.Background(), config.PreviewGatewayConfigKind, "default", "token"); ok {
		t.Fatal("the token is still stored after it was cleared")
	}
	if resp, body = send(t, api, http.MethodPut, path, `{"url":""}`, admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("an empty url must turn previews off: %d %s", resp.StatusCode, body)
	}
	if svc.notified < 4 {
		t.Fatalf("service notified %d times, want one per write", svc.notified)
	}
}

func TestPreviewGatewayConfigRefusesBadWrites(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	for name, body := range map[string]string{
		"not a url":    `{"url":"gateway"}`,
		"wrong scheme": `{"url":"rtsp://192.168.1.2:8095"}`,
		"has a login":  `{"url":"http://u:p@192.168.1.2:8095"}`,
		"null url":     `{"url":null}`,
		"unknown":      `{"address":"http://x"}`,
		"null body":    `null`,
	} {
		t.Run(name, func(t *testing.T) {
			if resp, got := send(t, api, http.MethodPut, "/api/v1/config/preview.gateway", body, admin); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, got)
			}
		})
	}
}

func TestDashboardLayoutConfig(t *testing.T) {
	api, _, admin, viewer := previewTestAPI(t, nil)
	path := "/api/v1/config/dashboard.layout"
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, path, bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET before any write = %d, want 404", resp.StatusCode)
	}
	if resp, _ := send(t, api, http.MethodPut, path, `{"previews":{"order":["camera:a"]}}`, viewer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT = %d, want 403", resp.StatusCode)
	}
	resp, body := send(t, api, http.MethodPut, path, `{"previews":{"order":["ndi:NODE-01 (matrix-main)","camera:driveway"],"hidden":["ndi:ARENA (Composition)"]}}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	resp, body = send(t, api, http.MethodPut, path, `{"previews":{"hidden":[]}}`, admin)
	p := decodeMap(t, body)["payload"].(map[string]any)["previews"].(map[string]any)
	if resp.StatusCode != http.StatusOK || len(p["order"].([]any)) != 2 || len(p["hidden"].([]any)) != 0 {
		t.Fatalf("partial PUT kept the wrong lists: %d %s", resp.StatusCode, body)
	}
	for name, bad := range map[string]string{
		"duplicate":  `{"previews":{"order":["camera:a","camera:a"]}}`,
		"empty key":  `{"previews":{"order":[""]}}`,
		"null list":  `{"previews":{"order":null}}`,
		"not a list": `{"previews":{"order":"camera:a"}}`,
		"array":      `{"previews":[]}`,
		"unknown":    `{"previews":{"pinned":[]}}`,
		"top":        `{"tiles":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if resp, got := send(t, api, http.MethodPut, path, bad, admin); resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, got)
			}
		})
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, path+"/revisions", bearer(admin))
	if resp.StatusCode != http.StatusOK || len(decodeMap(t, body)["revisions"].([]any)) != 2 {
		t.Fatalf("revisions = %d: %s", resp.StatusCode, body)
	}
}

func TestPreviewConfigWritesRefuseAStaleRevision(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	path := "/api/v1/config/dashboard.layout"
	send(t, api, http.MethodPut, path, `{"previews":{"order":["camera:a"]}}`, admin)
	req := newJSONRequest(t, http.MethodPut, path, `{"previews":{"order":["camera:b"]}}`, map[string]string{"Authorization": "Bearer " + admin, "If-Match": `"7"`})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, body)
	}
}

func TestPreviewsListsWhatTheServiceReports(t *testing.T) {
	svc := &fakePreviewService{overview: preview.Overview{
		Gateway: preview.GatewayStatus{State: preview.GatewayReachable, ObservedAt: testNow},
		Tiles: []preview.Tile{
			{Key: "ndi:NODE-01 (matrix-main)", Kind: "ndi", Label: "node-01 · matrix-main", NodeID: "node-01", SurfaceID: "matrix-main", State: "idle", ObservedAt: testNow},
			{Key: "camera:driveway", Kind: "camera", Label: "Driveway", CameraID: "driveway", State: "live", Hidden: true, ObservedAt: testNow},
		},
	}}
	api, _, admin, _ := previewTestAPI(t, svc)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews", bearer(admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	tiles := m["tiles"].([]any)
	first, second := tiles[0].(map[string]any), tiles[1].(map[string]any)
	if m["gateway"].(map[string]any)["state"] != "reachable" || first["nodeId"] != "node-01" || first["surfaceId"] != "matrix-main" {
		t.Fatalf("body = %s", body)
	}
	if _, has := first["cameraId"]; has {
		t.Fatalf("an NDI tile carries cameraId: %v", first)
	}
	if _, has := second["nodeId"]; has || second["cameraId"] != "driveway" || second["hidden"] != true {
		t.Fatalf("camera tile = %v", second)
	}
}

func TestPreviewsWithoutAGatewayIsEmptyAndNotConfigured(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews", bearer(admin))
	m := decodeMap(t, body)
	if resp.StatusCode != http.StatusOK || m["gateway"].(map[string]any)["state"] != "not_configured" || len(m["tiles"].([]any)) != 0 {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/previews/camera:driveway/webrtc"},
		{http.MethodGet, "/api/v1/previews/camera:driveway/snapshot"},
	} {
		req := newJSONRequest(t, tc.method, tc.path, "v=0", bearer(admin))
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusServiceUnavailable || decodeMap(t, body)["type"] != ProblemTypePreviewGatewayUnavailable {
			t.Fatalf("%s %s = %d: %s", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}

func TestPreviewRoutesMapServiceErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown key", preview.ErrUnknownKey, http.StatusNotFound},
		{"gateway unreachable", &preview.UnavailableError{Message: "The preview gateway cannot be reached. Check that it is running."}, http.StatusServiceUnavailable},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakePreviewService{offerErr: tc.err, snapErr: tc.err}
			api, _, admin, _ := previewTestAPI(t, svc)
			resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/previews/camera:x/webrtc", "v=0", bearer(admin)))
			if resp.StatusCode != tc.want {
				t.Fatalf("offer status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
			resp, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews/camera:x/snapshot", bearer(admin))
			if resp.StatusCode != tc.want {
				t.Fatalf("snapshot status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusServiceUnavailable && !strings.Contains(string(body), "cannot be reached") {
				t.Fatalf("the operator's message is missing: %s", body)
			}
		})
	}
}

func TestPreviewOfferAndSnapshotPassTheKeyAndBodyThrough(t *testing.T) {
	svc := &fakePreviewService{}
	api, _, admin, _ := previewTestAPI(t, svc)
	key := "ndi:NODE-01 (matrix-main)"
	escaped := "ndi:NODE-01%20%28matrix-main%29"

	req := newJSONRequest(t, http.MethodPost, "/api/v1/previews/"+escaped+"/webrtc", "v=0 offer", map[string]string{"Authorization": "Bearer " + admin, "Content-Type": "application/sdp"})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK || string(body) != "v=0 answer" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/sdp") {
		t.Fatalf("offer = %d %q (%s)", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	if svc.lastKey != key || svc.lastOffer != "v=0 offer" {
		t.Fatalf("service saw key %q offer %q", svc.lastKey, svc.lastOffer)
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews/"+escaped+"/snapshot", bearer(admin))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(string(body), "\xff\xd8") || svc.lastKey != key {
		t.Fatalf("snapshot = %d %q key %q", resp.StatusCode, resp.Header.Get("Content-Type"), svc.lastKey)
	}
	if resp, _ := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/previews/camera:x/webrtc", "", bearer(admin))); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty offer = %d, want 400", resp.StatusCode)
	}
}

func TestOpenAPIPreviewResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc := &fakePreviewService{overview: preview.Overview{
		Gateway: preview.GatewayStatus{State: preview.GatewayReachable, ObservedAt: testNow},
		Tiles: []preview.Tile{
			{Key: "ndi:NODE-01 (matrix-main)", Kind: "ndi", Label: "node-01 · matrix-main", NodeID: "node-01", SurfaceID: "matrix-main", State: "idle", ObservedAt: testNow},
			{Key: "camera:driveway", Kind: "camera", Label: "Driveway", CameraID: "driveway", State: "error", Message: "The camera did not answer. Check its address and that it is on.", ObservedAt: testNow},
		},
	}}
	api, _, admin, _ := previewTestAPI(t, svc)

	put := func(path, schemaIn, body, schemaOut string) {
		t.Helper()
		assertMatchesSchema(t, c, schemaIn, []byte(body))
		resp, got := send(t, api, http.MethodPut, path, body, admin)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s = %d: %s", path, resp.StatusCode, got)
		}
		assertMatchesSchema(t, c, schemaOut, got)
		_, got = doRequest(t, api.Handler, http.MethodGet, path, bearer(admin))
		assertMatchesSchema(t, c, schemaOut, got)
		_, got = doRequest(t, api.Handler, http.MethodGet, path+"/revisions", bearer(admin))
		assertMatchesSchema(t, c, "ConfigRevisionsResponse", got)
	}
	put("/api/v1/config/preview.gateway", "ConfigPreviewGatewayPutRequest", `{"url":"http://192.168.130.176:8095","token":"t"}`, "PreviewGatewayConfigResponse")
	put("/api/v1/config/preview.camera/driveway", "ConfigPreviewCameraPutRequest", `{"name":"Driveway","url":"`+previewCameraSecretURL+`"}`, "PreviewCameraConfigResponse")
	put("/api/v1/config/dashboard.layout", "ConfigDashboardLayoutPutRequest", `{"previews":{"order":["camera:driveway"],"hidden":[]}}`, "DashboardLayoutConfigResponse")

	_, got := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.camera", bearer(admin))
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", got)
	_, got = doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews", bearer(admin))
	assertMatchesSchema(t, c, "PreviewsResponse", got)

	empty, _, adminEmpty, _ := previewTestAPI(t, nil)
	_, got = doRequest(t, empty.Handler, http.MethodGet, "/api/v1/previews", bearer(adminEmpty))
	assertMatchesSchema(t, c, "PreviewsResponse", got)
	resp, got := doRawRequest(t, empty.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/previews/camera:x/webrtc", "v=0", bearer(adminEmpty)))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	assertMatchesSchema(t, c, "Problem", got)
}

func TestPreviewRoutesRequireObservationReadEvenWhenReadsAreOpen(t *testing.T) {
	api, _, _, _ := previewTestAPI(t, &fakePreviewService{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/previews"},
		{http.MethodPost, "/api/v1/previews/camera:x/webrtc"},
		{http.MethodGet, "/api/v1/previews/camera:x/snapshot"},
	} {
		resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, tc.method, tc.path, "v=0", nil))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a credential = %d, want 401: %s", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}

func TestPreviewRoutesRefusePrincipalsWithoutObservationRead(t *testing.T) {
	idSvc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	scheduler := mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "sched-1", identity.RoleScheduler).ID)
	viewer := mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "viewer-1", identity.RoleViewer).ID)
	deps := configTestDeps(idSvc, st)
	deps.Previews = &fakePreviewService{}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/previews"},
		{http.MethodPost, "/api/v1/previews/camera:x/webrtc"},
		{http.MethodGet, "/api/v1/previews/camera:x/snapshot"},
	} {
		if resp, body := send(t, api, tc.method, tc.path, "v=0", scheduler); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("scheduler %s %s = %d, want 403: %s", tc.method, tc.path, resp.StatusCode, body)
		}
		if resp, body := send(t, api, tc.method, tc.path, "v=0", viewer); resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer %s %s = %d, want 200: %s", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}

func TestOperatorArrangesTheGridButCannotChangeGatewayOrCameras(t *testing.T) {
	idSvc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "op-1", identity.RoleOperator).ID)
	viewer := mustIssueToken(t, idSvc, mustCreatePrincipal(t, idSvc, "viewer-1", identity.RoleViewer).ID)
	api := New(configTestDeps(idSvc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	layout := "/api/v1/config/dashboard.layout"

	if resp, body := send(t, api, http.MethodPut, layout, `{"previews":{"order":["camera:a"]}}`, operator); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator PUT layout = %d: %s", resp.StatusCode, body)
	}
	if resp, body := send(t, api, http.MethodGet, layout, "", operator); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator GET layout = %d: %s", resp.StatusCode, body)
	}
	if resp, _ := send(t, api, http.MethodGet, layout, "", viewer); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET layout = %d, want 200", resp.StatusCode)
	}
	if resp, body := send(t, api, http.MethodPut, layout, `{"previews":{"order":[]}}`, viewer); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "dashboard:write") {
		t.Fatalf("viewer PUT layout = %d: %s", resp.StatusCode, body)
	}
	for _, path := range []string{"/api/v1/config/preview.gateway", "/api/v1/config/preview.camera/c1", "/api/v1/config/preview.ndi/n1"} {
		if resp, _ := send(t, api, http.MethodPut, path, `{}`, operator); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("operator PUT %s = %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestPreviewCameraRevisionsOfAnUnknownIdIsAnEmptyListLikeOtherKinds(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, admin, _ := previewTestAPI(t, nil)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.camera/never-saved/revisions", bearer(admin))
	if resp.StatusCode != http.StatusOK || len(decodeMap(t, body)["revisions"].([]any)) != 0 {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", body)
}

func TestPreviewsReportsAConnectingGateway(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc := &fakePreviewService{overview: preview.Overview{
		Gateway: preview.GatewayStatus{State: preview.GatewayConnecting, Message: "The preview gateway has not answered yet. Wait a few seconds and reload.", ObservedAt: testNow},
		Tiles:   []preview.Tile{},
	}}
	api, _, admin, _ := previewTestAPI(t, svc)
	resp, body := doRequest(t, api.Handler, http.MethodGet, "/api/v1/previews", bearer(admin))
	if resp.StatusCode != http.StatusOK || decodeMap(t, body)["gateway"].(map[string]any)["state"] != "connecting" {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "PreviewsResponse", body)
}

func TestPreviewNDISourceRoundTripsAndMatchesTheSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc := &fakePreviewService{}
	api, _, admin, viewer := previewTestAPI(t, svc)
	path := "/api/v1/config/preview.ndi/far-barn"
	if resp, _ := send(t, api, http.MethodPut, path, `{"ndiName":"BARN-02 (Composition)"}`, viewer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT = %d, want 403", resp.StatusCode)
	}
	resp, body := send(t, api, http.MethodPut, path, `{"label":"Far barn","ndiName":"BARN-02 (Composition)","address":"10.0.20.15:5961"}`, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "PreviewNDIConfigResponse", body)
	if svc.notified == 0 {
		t.Fatal("a saved NDI source did not wake the preview service")
	}
	p := decodeMap(t, body)["payload"].(map[string]any)
	if p["label"] != "Far barn" || p["ndiName"] != "BARN-02 (Composition)" || p["address"] != "10.0.20.15:5961" || p["enabled"] != true {
		t.Fatalf("payload = %v", p)
	}

	resp, body = send(t, api, http.MethodPut, path, `{"enabled":false,"address":""}`, admin)
	p = decodeMap(t, body)["payload"].(map[string]any)
	if resp.StatusCode != http.StatusOK || p["enabled"] != false || p["address"] != "" || p["ndiName"] != "BARN-02 (Composition)" {
		t.Fatalf("partial update = %d: %v", resp.StatusCode, p)
	}

	resp, body = doRequest(t, api.Handler, http.MethodGet, path, bearer(admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "PreviewNDIConfigResponse", body)
	resp, body = doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.ndi", bearer(admin))
	objs, _ := decodeMap(t, body)["objects"].([]any)
	if resp.StatusCode != http.StatusOK || len(objs) != 1 || objs[0].(map[string]any)["label"] != "Far barn" {
		t.Fatalf("list = %d: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", body)

	if resp, body := send(t, api, http.MethodDelete, path, `{"confirm":true}`, admin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", resp.StatusCode, body)
	}
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, path, bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", resp.StatusCode)
	}
	resp, body = doRequest(t, api.Handler, http.MethodGet, path+"/revisions", bearer(admin))
	if resp.StatusCode != http.StatusOK || len(decodeMap(t, body)["revisions"].([]any)) != 2 {
		t.Fatalf("revisions = %d: %s", resp.StatusCode, body)
	}
}

func TestPreviewNDISourceRefusesBadWrites(t *testing.T) {
	api, _, admin, _ := previewTestAPI(t, nil)
	cases := []struct{ name, path, body string }{
		{"no NDI name on create", "/api/v1/config/preview.ndi/s1", `{"label":"S"}`},
		{"blank NDI name", "/api/v1/config/preview.ndi/s1", `{"ndiName":"  "}`},
		{"quote in NDI name", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A\" ! filesink"}`},
		{"address without port", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","address":"10.0.20.15"}`},
		{"address out of range", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","address":"10.0.20.15:70000"}`},
		{"null address", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","address":null}`},
		{"enabled not boolean", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","enabled":"yes"}`},
		{"unknown field", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","url":"x"}`},
		{"long label", "/api/v1/config/preview.ndi/s1", `{"ndiName":"A (x)","label":"` + strings.Repeat("x", 81) + `"}`},
		{"bad id", "/api/v1/config/preview.ndi/Bad_Id", `{"ndiName":"A (x)"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := send(t, api, http.MethodPut, tc.path, tc.body, admin)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
			}
		})
	}
	if resp, _ := doRequest(t, api.Handler, http.MethodGet, "/api/v1/config/preview.ndi/s1", bearer(admin)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a refused write created the source: GET status = %d", resp.StatusCode)
	}
}
