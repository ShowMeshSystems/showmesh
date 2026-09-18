package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestPostAssetUploadRefusesUndecodableAudio proves an "audio" mediaType
// upload is checked against its own content before anything is registered
// (owner ruling 2026-09-18, PCM show audio): a file this coordinator
// cannot decode at all is refused synchronously, naming the supported
// formats, and no asset row is created for it.
func TestPostAssetUploadRefusesUndecodableAudio(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	fields := validAssetFields()
	fields["mediaType"] = "audio"
	resp, body := doAssetUpload(t, api.Handler, fields, "a.mp3", []byte("not actually an mp3 file"), auth)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem body: %v\nbody: %s", err, body)
	}
	for _, want := range []string{"WAV", "MP3", "FLAC", "Ogg Vorbis"} {
		if !strings.Contains(problem.Detail, want) {
			t.Errorf("problem detail %q does not name supported format %q", problem.Detail, want)
		}
	}

	recs, err := st.ListAssets(context.Background(), store.AssetFilter{ShowID: "halloween-2026"})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("assets registered = %+v, want none: a refused upload must register nothing", recs)
	}
}

// TestPostAssetUploadAcceptsValidWAVAndResponseCarriesNoRenditionYet
// proves a decodable audio upload succeeds, is stored as the operator's
// original bytes unchanged, and its response carries a nil rendition until
// the background transcode service (not wired in this test) builds one.
func TestPostAssetUploadAcceptsValidWAVAndResponseCarriesNoRenditionYet(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	wav := minimalTestWAV(7)
	fields := validAssetFields()
	fields["mediaType"] = "audio"
	resp, body := doAssetUpload(t, api.Handler, fields, "bed.wav", wav, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var decoded v1AssetResponseForTest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, body)
	}
	if decoded.Asset.ContentHash != contentHashOf(wav) {
		t.Errorf("ContentHash = %q, want the original upload's own hash %q", decoded.Asset.ContentHash, contentHashOf(wav))
	}
	if decoded.Asset.Rendition != nil {
		t.Errorf("Rendition = %+v, want nil: no background service is wired into this test", decoded.Asset.Rendition)
	}
}

// TestGetAssetCarriesReadyRenditionState proves GET /assets/{id} attaches
// an audio asset's rendition state once one has been recorded.
func TestGetAssetCarriesReadyRenditionState(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	wav := minimalTestWAV(9)
	fields := validAssetFields()
	fields["mediaType"] = "audio"
	_, uploadBody := doAssetUpload(t, api.Handler, fields, "bed.wav", wav, auth)
	var uploaded v1AssetResponseForTest
	if err := json.Unmarshal(uploadBody, &uploaded); err != nil {
		t.Fatalf("decode upload response: %v\nbody: %s", err, uploadBody)
	}

	if err := st.SetAudioRenditionReady(context.Background(), uploaded.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: "sha256:rendition", SizeBytes: 4096, DurationMillis: 1234, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("seed ready rendition: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+uploaded.Asset.ID, nil)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var got v1AssetResponseForTest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode get response: %v\nbody: %s", err, body)
	}
	if got.Asset.Rendition == nil {
		t.Fatal("Rendition = nil, want the ready rendition just seeded")
	}
	if got.Asset.Rendition.Status != "ready" || got.Asset.Rendition.Format != "wav48k16s" || got.Asset.Rendition.DurationMillis != 1234 {
		t.Errorf("Rendition = %+v, want status ready, format wav48k16s, durationMillis 1234", got.Asset.Rendition)
	}
}

// TestGetAssetRenditionContentServesRenditionBytes proves the rendition
// content route serves the RENDITION's own bytes, distinct from the
// original upload: the served body hashes to the rendition's own content
// hash and matches its own recorded size, exactly what a node's asset.fetch
// verifies against once assetsync substitutes a ready rendition into an
// expected-set entry.
func TestGetAssetRenditionContentServesRenditionBytes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	original := minimalTestWAV(11)
	fields := validAssetFields()
	fields["mediaType"] = "audio"
	_, uploadBody := doAssetUpload(t, api.Handler, fields, "bed.wav", original, auth)
	var uploaded v1AssetResponseForTest
	if err := json.Unmarshal(uploadBody, &uploaded); err != nil {
		t.Fatalf("decode upload response: %v\nbody: %s", err, uploadBody)
	}

	renditionBytes := []byte("stand-in 48kHz/16-bit/stereo WAV bytes, distinct from the original upload")
	blob, err := deps.AssetBackend.Put(context.Background(), bytes.NewReader(renditionBytes), int64(len(renditionBytes)))
	if err != nil {
		t.Fatalf("store rendition blob: %v", err)
	}
	if err := st.SetAudioRenditionReady(context.Background(), uploaded.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: blob.ContentHash, SizeBytes: blob.SizeBytes, DurationMillis: 999, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("seed ready rendition: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+uploaded.Asset.ID+"/rendition/content", nil)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !bytes.Equal(body, renditionBytes) {
		t.Fatalf("served bytes = %q, want the rendition's own bytes %q", body, renditionBytes)
	}
	if gotHash := contentHashOf(body); gotHash != blob.ContentHash {
		t.Errorf("served bytes hash to %q, want the rendition's own content hash %q", gotHash, blob.ContentHash)
	}
	if int64(len(body)) != blob.SizeBytes {
		t.Errorf("served %d bytes, want the rendition's own recorded size %d", len(body), blob.SizeBytes)
	}
	if got, want := resp.Header.Get("ETag"), `"`+blob.ContentHash+`"`; got != want {
		t.Errorf("ETag = %q, want %q", got, want)
	}
	if gotHash := contentHashOf(body); gotHash == uploaded.Asset.ContentHash {
		t.Error("served bytes hash to the ORIGINAL upload's content hash, want the rendition's own")
	}
}

// TestGetAssetRenditionContentNotFoundWithoutAReadyRendition proves the
// rendition route refuses with 404 when the asset has never had a
// rendition queued, is still rendering, or last failed, rather than
// serving the original upload's bytes under this route.
func TestGetAssetRenditionContentNotFoundWithoutAReadyRendition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store, hash string)
	}{
		{"never queued", func(t *testing.T, st *store.Store, hash string) {}},
		{"still rendering", func(t *testing.T, st *store.Store, hash string) {
			if err := st.SetAudioRenditionRendering(context.Background(), hash); err != nil {
				t.Fatalf("SetAudioRenditionRendering: %v", err)
			}
		}},
		{"failed", func(t *testing.T, st *store.Store, hash string) {
			if err := st.SetAudioRenditionFailed(context.Background(), hash, "decode error"); err != nil {
				t.Fatalf("SetAudioRenditionFailed: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, st, auth := assetsAdminAPI(t)

			wav := minimalTestWAV(13)
			fields := validAssetFields()
			fields["mediaType"] = "audio"
			_, uploadBody := doAssetUpload(t, api.Handler, fields, "bed.wav", wav, auth)
			var uploaded v1AssetResponseForTest
			if err := json.Unmarshal(uploadBody, &uploaded); err != nil {
				t.Fatalf("decode upload response: %v\nbody: %s", err, uploadBody)
			}
			tc.setup(t, st, uploaded.Asset.ContentHash)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+uploaded.Asset.ID+"/rendition/content", nil)
			for k, v := range auth {
				req.Header.Set(k, v)
			}
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
			}
		})
	}
}
