package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

func getSystemStatus(t *testing.T, view fppConnectView) (fppConnectSystemStatusResponse, map[string]json.RawMessage) {
	t.Helper()
	srv := startFPPConnectTestServer(t, view, "node-1", nil)
	resp, body := getBody(t, srv.URL+fppConnectPathSystemStatus)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "Falcon Player") {
		t.Fatalf("body claims a Falcon Player identity: %s", body)
	}
	var got fppConnectSystemStatusResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw: %v; body=%s", err, body)
	}
	return got, raw
}

func TestFPPConnectSystemStatusPlayingSequence(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, multiSync: multisync.Snapshot{
		State:      multisync.StatePlaying,
		Filename:   "Carol.fseq",
		FileType:   multisync.SyncFileTypeSequence,
		PositionMS: 83_900,
	}}
	got, _ := getSystemStatus(t, view)

	if got.Status != 1 || got.StatusName != "playing" {
		t.Fatalf("status/status_name = %d/%q, want 1/playing", got.Status, got.StatusName)
	}
	if got.Mode != 8 || got.ModeName != "remote" {
		t.Fatalf("mode/mode_name = %d/%q, want 8/remote", got.Mode, got.ModeName)
	}
	if got.CurrentSequence != "Carol.fseq" || got.SequenceFilename != "Carol.fseq" {
		t.Fatalf("current_sequence/sequence_filename = %q/%q, want Carol.fseq", got.CurrentSequence, got.SequenceFilename)
	}
	if got.CurrentSong != "" || got.MediaFilename != "" {
		t.Fatalf("current_song/media_filename = %q/%q, want empty for a sequence", got.CurrentSong, got.MediaFilename)
	}
	if got.SecondsPlayed != "83" || got.SecondsElapsed != "83" || got.TimeElapsed != "01:23" {
		t.Fatalf("seconds_played/seconds_elapsed/time_elapsed = %q/%q/%q, want 83/83/01:23",
			got.SecondsPlayed, got.SecondsElapsed, got.TimeElapsed)
	}
	if got.UUID != fppConnectNodeUUID("node-1").String() {
		t.Fatalf("uuid = %q, want the node's derived uuid", got.UUID)
	}
}

func TestFPPConnectSystemStatusPlayingMedia(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, multiSync: multisync.Snapshot{
		State:      multisync.StateUnsynchronized,
		Filename:   "Carol.mp3",
		FileType:   multisync.SyncFileTypeMedia,
		PositionMS: 5_000,
	}}
	got, _ := getSystemStatus(t, view)

	if got.StatusName != "playing" {
		t.Fatalf("status_name = %q, want playing while the timeline still free-runs", got.StatusName)
	}
	if got.CurrentSong != "Carol.mp3" || got.MediaFilename != "Carol.mp3" {
		t.Fatalf("current_song/media_filename = %q/%q, want Carol.mp3", got.CurrentSong, got.MediaFilename)
	}
	if got.CurrentSequence != "" || got.SequenceFilename != "" {
		t.Fatalf("current_sequence/sequence_filename = %q/%q, want empty for media", got.CurrentSequence, got.SequenceFilename)
	}
}

func TestFPPConnectSystemStatusIdleStates(t *testing.T) {
	for _, state := range []multisync.State{
		"", multisync.StateUnknown, multisync.StateOpened, multisync.StateStopping, multisync.StateStopped,
	} {
		t.Run("state="+string(state), func(t *testing.T) {
			view := fakeFPPConnectView{enabled: true, multiSync: multisync.Snapshot{
				State:      state,
				Filename:   "Carol.fseq",
				PositionMS: 83_900,
			}}
			got, raw := getSystemStatus(t, view)

			if got.Status != 0 || got.StatusName != "idle" {
				t.Fatalf("status/status_name = %d/%q, want 0/idle", got.Status, got.StatusName)
			}
			if got.SequenceFilename != "" || got.CurrentSequence != "" || got.MediaFilename != "" || got.CurrentSong != "" {
				t.Fatalf("an idle node names a file: %+v", got)
			}
			if got.SecondsPlayed != "0" || got.TimeElapsed != "00:00" {
				t.Fatalf("seconds_played/time_elapsed = %q/%q, want 0/00:00", got.SecondsPlayed, got.TimeElapsed)
			}
			// FPP's page compares these against "" and would draw "undefined"
			// for a missing member, so they are served even when empty.
			for _, key := range []string{"sequence_filename", "media_filename", "current_sequence", "current_song", "time_elapsed"} {
				if _, ok := raw[key]; !ok {
					t.Errorf("member %q is missing", key)
				}
			}
		})
	}
}

// TestFPPConnectSystemStatusFollowsRealTimeline drives the production view
// adapter over a real Timeline: nothing seen yet, then playing, then stopped.
func TestFPPConnectSystemStatusFollowsRealTimeline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	timeline := multisync.NewTimeline(func() time.Time { return now }, multisync.Config{})
	view := newFPPConnectStateView(newFPPConnectState(), newTestAssignmentStore(t)).withTimeline(timeline)

	if got, _ := getSystemStatus(t, view); got.StatusName != "idle" || got.SequenceFilename != "" {
		t.Fatalf("before any packet: status_name/sequence_filename = %q/%q, want idle and empty", got.StatusName, got.SequenceFilename)
	}

	timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence, Filename: "Carol.fseq"}, "192.0.2.1")
	timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionSync, FileType: multisync.SyncFileTypeSequence, SecondsElapsed: 12, Filename: "Carol.fseq"}, "192.0.2.1")
	got, _ := getSystemStatus(t, view)
	if got.StatusName != "playing" || got.SequenceFilename != "Carol.fseq" || got.SecondsElapsed != "12" || got.TimeElapsed != "00:12" {
		t.Fatalf("while playing: %+v", got)
	}

	timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionStop, FileType: multisync.SyncFileTypeSequence, Filename: "Carol.fseq"}, "192.0.2.1")
	now = now.Add(time.Minute)
	if got, _ := getSystemStatus(t, view); got.StatusName != "idle" || got.SequenceFilename != "" {
		t.Fatalf("after stop: status_name/sequence_filename = %q/%q, want idle and empty", got.StatusName, got.SequenceFilename)
	}
}

func TestFPPConnectStateViewWithoutTimelineIsNotPlaying(t *testing.T) {
	view := newFPPConnectStateView(newFPPConnectState(), newTestAssignmentStore(t))
	if got, _ := getSystemStatus(t, view); got.StatusName != "idle" {
		t.Fatalf("status_name = %q, want idle", got.StatusName)
	}
}

func TestFPPConnectSystemStatusAdvancedViewIsSystemInfo(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, channelRanges: "0-9"}
	got, raw := getSystemStatus(t, view)

	if got.AdvancedView.Platform != fppConnectPlatform || got.AdvancedView.HostName != "node-1" || got.AdvancedView.ChannelRanges != "0-9" {
		t.Fatalf("advancedView = %+v, want this node's system info", got.AdvancedView)
	}
	if want := "ShowMesh agent " + version.Version; got.AdvancedView.OSVersion != want {
		t.Fatalf("advancedView.OSVersion = %q, want %q", got.AdvancedView.OSVersion, want)
	}
	// With RemoteGitVersion present FPP's page draws a commit link to a page
	// the node does not serve.
	if strings.Contains(string(raw["advancedView"]), "RemoteGitVersion") {
		t.Fatalf("advancedView carries RemoteGitVersion: %s", raw["advancedView"])
	}
}

func TestFPPConnectSystemInfoCarriesAgentVersion(t *testing.T) {
	srv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", nil)
	_, body := getBody(t, srv.URL+"/api/system/info")
	var got fppConnectSystemInfoResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if want := "ShowMesh agent " + version.Version; got.OSVersion != want {
		t.Fatalf("OSVersion = %q, want %q", got.OSVersion, want)
	}
}

func TestFPPConnectSystemStatusRefusesEveryOtherMethod(t *testing.T) {
	srv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", nil)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		req, err := http.NewRequest(method, srv.URL+fppConnectPathSystemStatus, strings.NewReader(`{"command":"stop"}`))
		if err != nil {
			t.Fatalf("building %s request: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", method, resp.StatusCode)
		}
	}
}

func TestFPPConnectSystemStatusIs404WhileDisabled(t *testing.T) {
	view := fakeFPPConnectView{enabled: false, multiSync: multisync.Snapshot{State: multisync.StatePlaying, Filename: "Carol.fseq"}}
	srv := startFPPConnectTestServer(t, view, "node-1", nil)
	resp, body := getBody(t, srv.URL+fppConnectPathSystemStatus)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while disabled; body=%s", resp.StatusCode, body)
	}
}

func TestFPPConnectSecondsToTimeMatchesFPP(t *testing.T) {
	for seconds, want := range map[int64]string{
		0:      "00:00",
		59:     "00:59",
		83:     "01:23",
		3600:   "60:00",
		3601:   "01:00:01",
		90_061: "1 day, 01:01:01",
	} {
		if got := fppConnectSecondsToTime(seconds); got != want {
			t.Errorf("fppConnectSecondsToTime(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func TestFPPConnectFormatUptimeMatchesFPP(t *testing.T) {
	for seconds, want := range map[int64]string{
		0:       "0:0",
		3_725:   "1:2",
		86_400:  "1 days 0:0",
		273_900: "3 days 4:5",
	} {
		if got := fppConnectFormatUptime(seconds); got != want {
			t.Errorf("fppConnectFormatUptime(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func writeProcFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestFPPConnectHostStatsReadsProc(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_700_000_000, 0)
	stats := newFPPConnectHostStats(root, func() time.Time { return now })

	writeProcFile(t, root, "stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 100 0 100 800 0 0 0 0 0 0\n")
	writeProcFile(t, root, "meminfo", "MemTotal:        8000000 kB\nMemFree:          100000 kB\nMemAvailable:    6000000 kB\n")
	writeProcFile(t, root, "uptime", "273900.52 1000000.00\n")

	first := stats.utilization()
	if first == nil {
		t.Fatal("utilization() = nil, want memory and uptime")
	}
	if first.CPU != nil {
		t.Fatalf("CPU = %v on the first read, want it omitted until a second read exists", *first.CPU)
	}
	if first.Memory == nil || *first.Memory != 25 {
		t.Fatalf("Memory = %v, want 25", first.Memory)
	}
	if first.Uptime != "3 days 4:5" {
		t.Fatalf("Uptime = %q, want %q", first.Uptime, "3 days 4:5")
	}

	writeProcFile(t, root, "stat", "cpu  150 0 150 900 0 0 0 0 0 0\n")
	if early := stats.utilization(); early.CPU != nil {
		t.Fatalf("CPU = %v inside the minimum window, want it still omitted", *early.CPU)
	}

	now = now.Add(2 * time.Second)
	second := stats.utilization()
	if second.CPU == nil || *second.CPU != 50 {
		t.Fatalf("CPU = %v, want 50", second.CPU)
	}
}

func TestFPPConnectHostStatsOmitsWhatItCannotMeasure(t *testing.T) {
	root := t.TempDir()
	stats := newFPPConnectHostStats(root, time.Now)
	if got := stats.utilization(); got != nil {
		t.Fatalf("utilization() = %+v with nothing readable, want nil", got)
	}

	writeProcFile(t, root, "stat", "garbage\n")
	writeProcFile(t, root, "meminfo", "MemTotal: 8000000 kB\n")
	writeProcFile(t, root, "uptime", "not-a-number\n")
	if got := stats.utilization(); got != nil {
		t.Fatalf("utilization() = %+v with unparseable files, want nil", got)
	}

	encoded, err := json.Marshal(fppConnectSystemInfoResponse{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "Utilization") {
		t.Fatalf("system info serves a Utilization member with nothing measured: %s", encoded)
	}
}
