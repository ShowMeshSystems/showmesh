package agent

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// fppConnectPathSystemStatus is GET /api/system/status: an FPP player's
// MultiSync page polls it, through the player's own web server, to fill this
// node's row. Read only: nothing here accepts a control action.
const fppConnectPathSystemStatus = "/api/system/status"

const (
	fppConnectStatusIdle    = 0
	fppConnectStatusPlaying = 1

	fppConnectStatusNameIdle    = "idle"
	fppConnectStatusNamePlaying = "playing"

	// fppConnectStatusModeName is the mode this node pings with (ADR-044
	// decision 6), which is not the Mode /api/system/info serves to xLights.
	fppConnectStatusModeName = "remote"
)

// fppConnectSystemStatusResponse is GET /api/system/status's body. Member
// names, casing and the string-typed second counts are fppd's own
// (src/httpAPI.cpp); RES-002 lists which of them FPP's MultiSync page reads.
type fppConnectSystemStatusResponse struct {
	UUID       string `json:"uuid"`
	Mode       int    `json:"mode"`
	ModeName   string `json:"mode_name"`
	Status     int    `json:"status"`
	StatusName string `json:"status_name"`

	CurrentSequence  string `json:"current_sequence"`
	CurrentSong      string `json:"current_song"`
	SequenceFilename string `json:"sequence_filename"`
	MediaFilename    string `json:"media_filename"`
	SecondsPlayed    string `json:"seconds_played"`
	SecondsElapsed   string `json:"seconds_elapsed"`
	TimeElapsed      string `json:"time_elapsed"`

	AdvancedView fppConnectSystemInfoResponse `json:"advancedView"`
}

func (s *fppConnectServer) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	status := fppConnectSystemStatusResponse{
		UUID:         s.uuid,
		Mode:         int(multisync.PingModeRemote),
		ModeName:     fppConnectStatusModeName,
		Status:       fppConnectStatusIdle,
		StatusName:   fppConnectStatusNameIdle,
		AdvancedView: s.systemInfo(),
	}
	fppConnectFillPlayback(&status, s.view.MultiSyncSnapshot())
	fppConnectWriteJSON(w, http.StatusOK, status)
}

// fppConnectFillPlayback reports playing only while the timeline is running:
// playing, or unsynchronized, which is still free-running. Every other state,
// including a timeline that has never seen a packet, reads as idle with no
// filename, exactly as an FPP remote with nothing open does.
func fppConnectFillPlayback(status *fppConnectSystemStatusResponse, snap multisync.Snapshot) {
	seconds := int64(0)
	if snap.State == multisync.StatePlaying || snap.State == multisync.StateUnsynchronized {
		status.Status = fppConnectStatusPlaying
		status.StatusName = fppConnectStatusNamePlaying
		if snap.PositionMS > 0 {
			seconds = snap.PositionMS / 1000
		}
		if snap.FileType == multisync.SyncFileTypeMedia {
			status.MediaFilename = snap.Filename
			status.CurrentSong = snap.Filename
		} else {
			status.SequenceFilename = snap.Filename
			status.CurrentSequence = snap.Filename
		}
	}
	status.SecondsPlayed = strconv.FormatInt(seconds, 10)
	status.SecondsElapsed = status.SecondsPlayed
	status.TimeElapsed = fppConnectSecondsToTime(seconds)
}

// fppConnectSecondsToTime matches fppd's secondsToTime (src/common_mini.cpp),
// including its strict comparisons: exactly one hour reads "60:00".
func fppConnectSecondsToTime(seconds int64) string {
	const hour, day = int64(60 * 60), int64(24 * 60 * 60)
	out := ""
	if seconds > day {
		days := seconds / day
		unit := " days, "
		if days == 1 {
			unit = " day, "
		}
		out += strconv.FormatInt(days, 10) + unit
		seconds %= day
	}
	if seconds > hour {
		out += fmt.Sprintf("%02d:", seconds/hour)
		seconds %= hour
	}
	return out + fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
