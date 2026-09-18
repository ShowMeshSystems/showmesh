package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
