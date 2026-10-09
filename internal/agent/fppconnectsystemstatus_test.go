package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
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
		State:      multisync.StatePlaying,
		Filename:   "Carol.mp3",
		FileType:   multisync.SyncFileTypeMedia,
		PositionMS: 5_000,
	}}
	got, _ := getSystemStatus(t, view)

	if got.StatusName != "playing" {
		t.Fatalf("status_name = %q, want playing", got.StatusName)
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

// agedTimelineStatus drives the production view adapter over a real Timeline
// on a stepped clock: START and a SYNC at 12 s, then whatever the test does.
type agedTimelineStatus struct {
	t        *testing.T
	now      time.Time
	timeline *multisync.Timeline
	view     fppConnectStateView
}

func newAgedTimelineStatus(t *testing.T, surfaces func() []pipeline.Snapshot) *agedTimelineStatus {
	t.Helper()
	a := &agedTimelineStatus{t: t, now: time.Unix(1_700_000_000, 0)}
	a.timeline = multisync.NewTimeline(func() time.Time { return a.now }, multisync.Config{})
	a.view = newFPPConnectStateView(newFPPConnectState(), newTestAssignmentStore(t)).withPlayback(a.timeline, surfaces)
	return a
}

func (a *agedTimelineStatus) sync(action multisync.SyncAction, seconds float32) {
	a.timeline.Observe(multisync.SyncPacket{Action: action, FileType: multisync.SyncFileTypeSequence, SecondsElapsed: seconds, Filename: "Carol.fseq"}, "192.0.2.1")
}

func (a *agedTimelineStatus) want(when, statusName, filename, elapsed string) {
	a.t.Helper()
	got, _ := getSystemStatus(a.t, a.view)
	if got.StatusName != statusName || got.SequenceFilename != filename || got.CurrentSequence != filename || got.TimeElapsed != elapsed {
		a.t.Fatalf("%s: status_name/sequence_filename/current_sequence/time_elapsed = %q/%q/%q/%q, want %q/%q/%q/%q",
			when, got.StatusName, got.SequenceFilename, got.CurrentSequence, got.TimeElapsed, statusName, filename, filename, elapsed)
	}
}

func drawingSurface(durationMS int64) func() []pipeline.Snapshot {
	return func() []pipeline.Snapshot {
		return []pipeline.Snapshot{
			{SurfaceID: "idle-surface", Drawing: pipeline.DrawingIdle},
			{SurfaceID: "matrix", Drawing: pipeline.DrawingContent, ContentDurationMS: &durationMS},
		}
	}
}

// A node with no surface drawing the sequence cannot know it is still
// playing once sync goes silent, so it stops saying so.
func TestFPPConnectSystemStatusSilentSyncWithNothingDrawnReadsIdle(t *testing.T) {
	a := newAgedTimelineStatus(t, func() []pipeline.Snapshot { return nil })
	a.want("before any packet", "idle", "", "00:00")

	a.sync(multisync.SyncActionStart, 0)
	a.sync(multisync.SyncActionSync, 12)
	a.want("fresh sync", "playing", "Carol.fseq", "00:12")

	a.now = a.now.Add(10 * time.Second)
	a.want("10 s of silence", "idle", "", "00:00")

	a.now = a.now.Add(6 * time.Hour)
	a.want("hours of silence", "idle", "", "00:00")

	a.sync(multisync.SyncActionSync, 40)
	a.want("sync resumed", "playing", "Carol.fseq", "00:40")

	a.sync(multisync.SyncActionStop, 0)
	a.now = a.now.Add(time.Minute)
	a.want("after stop", "idle", "", "00:00")
}

// A surface keeps drawing the sequence through silence until the sequence
// ends, so the row follows it that far and no further.
func TestFPPConnectSystemStatusSilentSyncFollowsTheDrawnSequenceToItsEnd(t *testing.T) {
	a := newAgedTimelineStatus(t, drawingSurface(180_000))

	a.sync(multisync.SyncActionStart, 0)
	a.sync(multisync.SyncActionSync, 12)
	a.want("fresh sync", "playing", "Carol.fseq", "00:12")

	a.now = a.now.Add(10 * time.Second)
	a.want("10 s of silence", "playing", "Carol.fseq", "00:22")

	a.now = a.now.Add(157 * time.Second)
	a.want("one second before the sequence ends", "playing", "Carol.fseq", "02:59")

	a.now = a.now.Add(time.Second)
	a.want("at the sequence's end", "idle", "", "00:00")

	a.now = a.now.Add(72 * time.Hour)
	a.want("days of silence", "idle", "", "00:00")

	a.sync(multisync.SyncActionSync, 40)
	a.want("sync resumed", "playing", "Carol.fseq", "00:40")
}

func TestFPPConnectStateViewDrawnSequence(t *testing.T) {
	short, long := int64(60_000), int64(180_000)
	base := newFPPConnectStateView(newFPPConnectState(), newTestAssignmentStore(t))

	if _, ok := base.DrawnSequenceMS(); ok {
		t.Fatal("a view built without surfaces reports a drawn sequence")
	}
	view := base.withPlayback(nil, func() []pipeline.Snapshot {
		return []pipeline.Snapshot{
			{Drawing: pipeline.DrawingContent, ContentDurationMS: &short},
			{Drawing: pipeline.DrawingContent, ContentDurationMS: &long},
			{Drawing: pipeline.DrawingStale},
			{Drawing: pipeline.DrawingBlackout, ContentDurationMS: &long},
		}
	})
	if got, ok := view.DrawnSequenceMS(); !ok || got != long {
		t.Fatalf("DrawnSequenceMS() = %d, %v, want %d, true", got, ok, long)
	}
	none := base.withPlayback(nil, func() []pipeline.Snapshot {
		return []pipeline.Snapshot{{Drawing: pipeline.DrawingIdle}}
	})
	if _, ok := none.DrawnSequenceMS(); ok {
		t.Fatal("a view whose surfaces all draw idle output reports a drawn sequence")
	}
	if got, _ := getSystemStatus(t, base); got.StatusName != "idle" {
		t.Fatalf("status_name = %q for a view with no timeline, want idle", got.StatusName)
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

// TestFPPConnectSystemInfoMemberSetIsPinned keeps the document xLights reads
// exactly as it was before the status route existed.
func TestFPPConnectSystemInfoMemberSetIsPinned(t *testing.T) {
	view := fakeFPPConnectView{enabled: true, channelRanges: "0-9"}
	srv := startFPPConnectTestServer(t, view, "node-1", nil)
	_, body := getBody(t, srv.URL+"/api/system/info")

	want := `{"uuid":"` + fppConnectNodeUUID("node-1").String() + `","HostName":"node-1","Version":"9.5.0","majorVersion":9,"minorVersion":5,"Mode":"player","typeId":127,"channelRanges":"0-9","Platform":"ShowMesh","Variant":"ShowMesh"}` + "\n"
	if string(body) != want {
		t.Fatalf("GET /api/system/info body changed:\n got %s\nwant %s", body, want)
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

	encoded, err := json.Marshal(fppConnectAdvancedView{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "Utilization") {
		t.Fatalf("advancedView serves a Utilization member with nothing measured: %s", encoded)
	}
}
