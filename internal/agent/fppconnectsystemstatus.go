package agent

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/showmeshsystems/showmesh/internal/version"
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

	AdvancedView fppConnectAdvancedView `json:"advancedView"`
}

// fppConnectAdvancedView is this node's system info plus the members only an
// FPP player's MultiSync page reads. OSVersion carries the agent's version
// because the page draws it beside the advertised FPP version.
type fppConnectAdvancedView struct {
	fppConnectSystemInfoResponse
	OSVersion   string                 `json:"OSVersion"`
	Utilization *fppConnectUtilization `json:"Utilization,omitempty"`
}

func (s *fppConnectServer) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	status := fppConnectSystemStatusResponse{
		UUID:       s.uuid,
		Mode:       int(multisync.PingModeRemote),
		ModeName:   fppConnectStatusModeName,
		Status:     fppConnectStatusIdle,
		StatusName: fppConnectStatusNameIdle,
		AdvancedView: fppConnectAdvancedView{
			fppConnectSystemInfoResponse: s.systemInfo(),
			OSVersion:                    fppConnectPlatform + " agent " + version.Version,
			Utilization:                  s.host.utilization(),
		},
	}
	drawnMS, drawing := s.view.DrawnSequenceMS()
	fppConnectFillPlayback(&status, s.view.MultiSyncSnapshot(), drawnMS, drawing)
	fppConnectWriteJSON(w, http.StatusOK, status)
}

// fppConnectStillPlaying reports whether this node is playing the timeline's
// file right now. With sync arriving it is. Once sync has gone silent only a
// surface still drawing that sequence, short of the sequence's end, counts.
func fppConnectStillPlaying(snap multisync.Snapshot, drawnMS int64, drawing bool) bool {
	switch snap.State {
	case multisync.StatePlaying:
		return true
	case multisync.StateUnsynchronized:
		return drawing && snap.PositionMS < drawnMS
	default:
		return false
	}
}

// fppConnectFillPlayback fills the playback members, which read as idle with
// no filename, exactly as an FPP remote with nothing open does, unless
// fppConnectStillPlaying holds.
func fppConnectFillPlayback(status *fppConnectSystemStatusResponse, snap multisync.Snapshot, drawnMS int64, drawing bool) {
	seconds := int64(0)
	if fppConnectStillPlaying(snap, drawnMS, drawing) {
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
