package agent

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// fseqNamingMedia returns a small FSEQ whose "mf" variable header names
// mediaPath, the way xLights records a sequence's song.
func fseqNamingMedia(t *testing.T, mediaPath string) []byte {
	t.Helper()
	base := fseqtest.Build(4, 2, 50)
	const fixedHeaderLen = 32

	entry := make([]byte, 4, 4+len(mediaPath)+1)
	copy(entry[2:4], "mf")
	entry = append(entry, mediaPath...)
	entry = append(entry, 0)
	binary.LittleEndian.PutUint16(entry[0:2], uint16(len(entry)))
	// One spare word keeps the entry strictly inside the header region.
	for len(entry)%4 != 0 || len(entry) < int(binary.LittleEndian.Uint16(entry[0:2]))+4 {
		entry = append(entry, 0)
	}

	out := append([]byte{}, base[:fixedHeaderLen]...)
	out = append(out, entry...)
	out = append(out, base[fixedHeaderLen:]...)
	binary.LittleEndian.PutUint16(out[4:6], uint16(fixedHeaderLen+len(entry)))
	return out
}

// fakeAssetCoordinator stands in for POST /api/v1/assets with the real
// identity rule: one current asset per (show, sequence, targetKind, target,
// mediaType), and a new upload replaces only that identity's current asset.
type fakeAssetCoordinator struct {
	mu      sync.Mutex
	nextID  int
	current map[string]string
}

func fakeAssetIdentity(req fppConnectRegisterRequest) string {
	f := req.fields
	return strings.Join([]string{f["show"], f["sequence"], f["targetKind"], f["target"], f["mediaType"]}, "|")
}

func (c *fakeAssetCoordinator) respond(t *testing.T) func(fppConnectRegisterRequest) (int, string) {
	return func(req fppConnectRegisterRequest) (int, string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.current == nil {
			c.current = map[string]string{}
		}
		c.nextID++
		id := fmt.Sprintf("asset-%d", c.nextID)
		c.current[fakeAssetIdentity(req)] = id
		return http.StatusOK, assetResponseBody(t, id, sha256Hex(req.fileBytes), false)
	}
}

func (c *fakeAssetCoordinator) currentAssets() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.current))
	for k, v := range c.current {
		out[k] = v
	}
	return out
}

func requestForSequence(t *testing.T, requests []fppConnectRegisterRequest, sequence string) fppConnectRegisterRequest {
	t.Helper()
	for _, req := range requests {
		if req.fields["sequence"] == sequence {
			return req
		}
	}
	t.Fatalf("no registration request for sequence %q among %d requests", sequence, len(requests))
	return fppConnectRegisterRequest{}
}

// TestFPPConnectRegisterSongBecomesAudioOfItsSequence uploads a song and
// then the sequence whose FSEQ names it, as xLights does, and proves the
// song registers show-wide as "<sequence>-audio" while the FSEQ asset stays
// current under its own identity.
func TestFPPConnectRegisterSongBecomesAudioOfItsSequence(t *testing.T) {
	held, _ := newTestHeldStore(t)
	coordinator := &fakeAssetCoordinator{}
	fakeSrv, requests := newFPPConnectRegisterFake(t, coordinator.respond(t))
	defer fakeSrv.Close()

	reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
	reg.initialBackoff, reg.maxBackoff = fppConnectTestBackoff, fppConnectTestMaxBackoff

	songBytes := []byte("song-bytes")
	uploadAndBind(t, held, "music", "Dracula (Remaster).mp3", songBytes)
	waiting := waitForRegistrationState(t, held, "music", "Dracula (Remaster).mp3", fppConnectRegistrationPending)
	if waiting.RegistrationReason != "Waiting for the sequence this song belongs to." {
		t.Fatalf("RegistrationReason = %q while the sequence has not arrived", waiting.RegistrationReason)
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("registration requests = %d before the sequence arrived, want 0", got)
	}

	uploadAndBind(t, held, "sequences", "Tame Impala Dracula.fseq", fseqNamingMedia(t, `C:\Shows\Audio\Dracula (Remaster).mp3`))

	sequence := waitForRegistrationState(t, held, "sequences", "Tame Impala Dracula.fseq", fppConnectRegistrationRegistered)
	song := waitForRegistrationState(t, held, "music", "Dracula (Remaster).mp3", fppConnectRegistrationRegistered)

	if song.PairedSequence != "Tame Impala Dracula.fseq" || song.LogicalSequence != "tame-impala-dracula-audio" {
		t.Fatalf("song record = paired %q, sequence id %q; want the uploaded sequence and tame-impala-dracula-audio", song.PairedSequence, song.LogicalSequence)
	}
	if want := `Registered as "tame-impala-dracula-audio".`; song.RegistrationReason != want {
		t.Fatalf("song RegistrationReason = %q, want %q", song.RegistrationReason, want)
	}

	songReq := requestForSequence(t, requests(), "tame-impala-dracula-audio")
	wantOrder := []string{"show", "sequence", "mediaType", "targetKind"}
	if !equalStringSlices(songReq.fieldOrder, wantOrder) {
		t.Fatalf("song field order = %v, want %v (a show-wide asset sends no target)", songReq.fieldOrder, wantOrder)
	}
	if songReq.fields["mediaType"] != "audio" || songReq.fields["targetKind"] != "show" || songReq.fields["show"] != "halloween-2026" {
		t.Fatalf("song fields = %v, want audio, show-wide, under halloween-2026", songReq.fields)
	}
	if songReq.fileName != "Dracula (Remaster).mp3" || string(songReq.fileBytes) != string(songBytes) {
		t.Fatalf("song file part = %q (%d bytes), want the held MP3", songReq.fileName, len(songReq.fileBytes))
	}

	sequenceReq := requestForSequence(t, requests(), "tame-impala-dracula")
	if sequenceReq.fields["mediaType"] != "fseq" || sequenceReq.fields["targetKind"] != "node" || sequenceReq.fields["target"] != "node-1" {
		t.Fatalf("sequence fields = %v, want fseq targeted at node-1", sequenceReq.fields)
	}

	current := coordinator.currentAssets()
	if len(current) != 2 {
		t.Fatalf("current assets = %v, want the FSEQ and the audio under two identities", current)
	}
	if got := current[fakeAssetIdentity(sequenceReq)]; got != sequence.RegistrationAssetID {
		t.Fatalf("current FSEQ asset = %q after the audio registered, want %q untouched", got, sequence.RegistrationAssetID)
	}
	if got := current[fakeAssetIdentity(songReq)]; got != song.RegistrationAssetID || got == sequence.RegistrationAssetID {
		t.Fatalf("current audio asset = %q, want the song's own %q", got, song.RegistrationAssetID)
	}

	// xLights posts the whole playlist again on every later upload.
	held.BindShow("Halloween", "halloween-2026", []string{"Tame Impala Dracula.fseq", "Dracula (Remaster).mp3"}, nil, time.Now())
	fppConnectWaitPastBackoff()
	if again, _ := findHeldRecord(t, held, "music", "Dracula (Remaster).mp3"); again.RegistrationState != fppConnectRegistrationRegistered {
		t.Fatalf("song RegistrationState = %q after a repeated playlist post, want it left registered", again.RegistrationState)
	}
	if got := len(requests()); got != 2 {
		t.Fatalf("registration requests = %d after a repeated playlist post, want still 2", got)
	}
}

// TestFPPConnectRegisterSongPairedByPlaylistEntry proves the pairing a
// playlist entry carries names the audio asset, whether the entry arrives
// before the song or after it.
func TestFPPConnectRegisterSongPairedByPlaylistEntry(t *testing.T) {
	playlist := []byte(`{"mainPlaylist":[{"type":"both","sequenceName":"Opening Number.fseq","mediaName":"track01.mp3"}]}`)
	view := fakeFPPConnectView{
		enabled: true, showNames: []string{"Halloween"},
		shows: []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}},
	}

	for _, playlistFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("playlistFirst=%v", playlistFirst), func(t *testing.T) {
			held, _ := newTestHeldStore(t)
			coordinator := &fakeAssetCoordinator{}
			fakeSrv, requests := newFPPConnectRegisterFake(t, coordinator.respond(t))
			defer fakeSrv.Close()
			newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

			srv := startFPPConnectTestServer(t, view, "node-1", held)
			defer srv.Close()

			upload := func() {
				data := []byte("song-bytes")
				if resp, body := patchChunk(t, srv, "music", "track01.mp3", 0, int64(len(data)), data); resp.StatusCode != http.StatusOK {
					t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
				}
			}
			post := func() {
				if resp, body := postPlaylist(t, srv, "Halloween", playlist); resp.StatusCode != http.StatusOK {
					t.Fatalf("playlist post: status = %d, body=%s", resp.StatusCode, body)
				}
			}
			if playlistFirst {
				post()
				upload()
			} else {
				upload()
				post()
			}

			song := waitForRegistrationState(t, held, "music", "track01.mp3", fppConnectRegistrationRegistered)
			if song.PairedSequence != "Opening Number.fseq" {
				t.Fatalf("PairedSequence = %q, want the playlist entry's sequence", song.PairedSequence)
			}
			if req := requestForSequence(t, requests(), "opening-number-audio"); req.fields["mediaType"] != "audio" {
				t.Fatalf("fields = %v, want an audio registration", req.fields)
			}
		})
	}
}

// TestFPPConnectRegisterSongWithNoSequenceUsesItsOwnName proves a song
// uploaded alone registers under a name from its own file name once the
// wait for a sequence is over, and that its record says so.
func TestFPPConnectRegisterSongWithNoSequenceUsesItsOwnName(t *testing.T) {
	held, _ := newTestHeldStore(t)
	coordinator := &fakeAssetCoordinator{}
	fakeSrv, requests := newFPPConnectRegisterFake(t, coordinator.respond(t))
	defer fakeSrv.Close()

	reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
	reg.pairingGrace = 0

	uploadAndBind(t, held, "music", "Walk-In Music.mp3", []byte("song-bytes"))

	song := waitForRegistrationState(t, held, "music", "Walk-In Music.mp3", fppConnectRegistrationRegistered)
	if want := `No sequence came with this song. Registered as "walk-in-music-audio".`; song.RegistrationReason != want {
		t.Fatalf("RegistrationReason = %q, want %q", song.RegistrationReason, want)
	}
	if song.PairedSequence != "" {
		t.Fatalf("PairedSequence = %q, want empty for a song uploaded alone", song.PairedSequence)
	}
	req := requestForSequence(t, requests(), "walk-in-music-audio")
	if req.fields["mediaType"] != "audio" || req.fields["targetKind"] != "show" {
		t.Fatalf("fields = %v, want a show-wide audio registration", req.fields)
	}
}

// TestFPPConnectRegisterSongSharingItsSequenceNameIsPaired proves a song
// named like a held sequence pairs with it when the FSEQ names no media.
func TestFPPConnectRegisterSongSharingItsSequenceNameIsPaired(t *testing.T) {
	held, _ := newTestHeldStore(t)
	coordinator := &fakeAssetCoordinator{}
	fakeSrv, _ := newFPPConnectRegisterFake(t, coordinator.respond(t))
	defer fakeSrv.Close()
	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	uploadAndBind(t, held, "sequences", "Finale.fseq", fseqtest.Build(4, 2, 50))
	uploadAndBind(t, held, "music", "finale.mp3", []byte("song-bytes"))

	song := waitForRegistrationState(t, held, "music", "finale.mp3", fppConnectRegistrationRegistered)
	if song.PairedSequence != "Finale.fseq" || song.LogicalSequence != "finale-audio" {
		t.Fatalf("song record = paired %q, sequence id %q; want Finale.fseq and finale-audio", song.PairedSequence, song.LogicalSequence)
	}
	if len(coordinator.currentAssets()) != 2 {
		t.Fatalf("current assets = %v, want the FSEQ and the audio both current", coordinator.currentAssets())
	}
}

// TestFPPConnectRegisterRefusedSongStaysHeld proves a coordinator refusal
// lands on the song's record verbatim and leaves the file on disk.
func TestFPPConnectRegisterRefusedSongStaysHeld(t *testing.T) {
	held, _ := newTestHeldStore(t)
	const detail = "The file is not audio this system can play. Upload an MP3 or WAV."
	fakeSrv, _ := newFPPConnectRegisterFake(t, func(fppConnectRegisterRequest) (int, string) {
		return http.StatusBadRequest, problemBody(t, "https://showmesh.dev/problems/invalid-parameter", detail)
	})
	defer fakeSrv.Close()

	reg, _, _ := newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")
	reg.pairingGrace = 0

	uploadAndBind(t, held, "music", "Broken.mp3", []byte("not-audio"))

	failed := waitForRegistrationState(t, held, "music", "Broken.mp3", fppConnectRegistrationFailed)
	if failed.RegistrationReason != detail {
		t.Fatalf("RegistrationReason = %q, want the coordinator's own %q", failed.RegistrationReason, detail)
	}
	if _, err := os.Stat(held.HeldFilePath("music", "Broken.mp3")); err != nil {
		t.Fatalf("held file after a refusal: %v, want it still on disk", err)
	}
}

func TestFPPConnectSequenceIDFor(t *testing.T) {
	long := strings.Repeat("a", 70)
	cases := []struct {
		dir, name, paired, want string
	}{
		{"sequences", "Tame Impala Dracula.fseq", "", "tame-impala-dracula"},
		{"videos", "Clip.mp4", "", "clip"},
		{"music", "Dracula.mp3", "Tame Impala Dracula.fseq", "tame-impala-dracula-audio"},
		{"music", "Dracula.mp3", "", "dracula-audio"},
		{"music", "???.mp3", "", ""},
		{"music", "x.mp3", long + ".fseq", strings.Repeat("a", 58) + "-audio"},
	}
	for _, tc := range cases {
		got := fppConnectSequenceIDFor(tc.dir, tc.name, tc.paired)
		if got != tc.want {
			t.Errorf("fppConnectSequenceIDFor(%q, %q, %q) = %q, want %q", tc.dir, tc.name, tc.paired, got, tc.want)
		}
		if len(got) > 64 {
			t.Errorf("fppConnectSequenceIDFor(%q, %q, %q) is %d characters, over the 64 the assets API accepts", tc.dir, tc.name, tc.paired, len(got))
		}
	}
}
