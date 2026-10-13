package api

import (
	"strconv"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// nightNoFileRecordReason is the one wording for a coordinator that keeps
// no record of the files on its nodes.
const nightNoFileRecordReason = "This coordinator keeps no record of the files on its nodes, so this could not be checked."

// nightAudioAbilityWords names what a node must support to play background
// audio, in the words the night definition uses.
var nightAudioAbilityWords = map[capability.ID]string{
	"audio.playback.background": "background audio",
	"audio.playback.playlist":   "playlists",
	"audio.playback.gain":       "volume changes",
	"audio.playback.loop":       "repeat",
}

// messages is every sentence a check has to say, one per item. A passing
// check says nothing.
func (c nightReadinessCheck) messages() []string {
	if c.health == nightHealthHealthy() {
		return []string{}
	}
	if len(c.reasons) > 0 {
		return c.reasons
	}
	if c.reason != "" {
		return []string{c.reason}
	}
	return []string{}
}

// nightNames writes names the way a sentence lists them: A, B and C.
func nightNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// nightUnique drops a message that repeats an earlier one, keeping order.
func nightUnique(messages []string) []string {
	seen := make(map[string]bool, len(messages))
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		if !seen[message] {
			seen[message] = true
			out = append(out, message)
		}
	}
	return out
}

// One wording each for conditions several checks report.
const (
	nightActiveShowUnreadReason = "ShowMesh could not read which show is active. Run readiness again."
	nightPlaylistUnreadReason   = "ShowMesh could not read playlist %s from FPP. Check that FPP is on, then run readiness again."
	nightPlaylistEmptyReason    = "Playlist %s has no items. Add a sequence to it in FPP."
	nightNoFPPForPlaylistReason = "No FPP is set up to read playlist %s from. Check the night definition."
)

// nightSecondsWords writes a millisecond length in seconds, as a person
// reads it.
func nightSecondsWords(ms int64) string {
	if ms == 1000 {
		return "1 second"
	}
	if ms%1000 == 0 {
		return strconv.FormatInt(ms/1000, 10) + " seconds"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " seconds"
}

// nightNoSessionReason is the one wording for a section with no night
// session behind it.
const nightNoSessionReason = "No night session has been prepared. Run Prepare site."

// nightStoredCheck reads a saved check. Reason is the single message a
// check carried before it carried a list.
type nightStoredCheck struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Reason  string   `json:"reason"`
	Reasons []string `json:"reasons"`
}

func (c nightStoredCheck) wire() v1.NightReadinessCheck {
	check := nightReadinessCheck{name: c.Name, health: nightCheckState(c.State), reason: c.Reason, reasons: c.Reasons}
	return v1.NightReadinessCheck{
		Name: c.Name, Label: nightCheckLabel(c.Name), State: c.State,
		StateLabel: nightCheckStateLabel(check.health), Reasons: check.messages(),
	}
}

// One wording each for the show start readouts.
const (
	nightNoShowStartReason      = "No show start is scheduled right now."
	nightShowStartSetReason     = "The next show start is scheduled."
	nightShowStartUnknownReason = "The next show start is not known."
	nightRecoverAction          = "Run End session, then Prepare site."
)

const (
	nightNoDefinitionReason      = "No night definition is active. Activate one, then run Prepare site."
	nightAudioRecordUnreadReason = "The background audio record could not be read. Reload the page."
)

const nightCueNotSentReason = "This cue was not sent. Check the coordinator log."

// nightReasonNoteSeparator divides a stored step reason from the notes this
// controller keeps after it. Only the first part is shown.
const nightReasonNoteSeparator = "\n"

// nightOperatorReason is the one message an operator reads from a stored
// step reason. A part this controller wrote for its own use is not shown.
func nightOperatorReason(stored string) string {
	message, _, _ := strings.Cut(stored, nightReasonNoteSeparator)
	if strings.HasPrefix(message, "{") || strings.HasPrefix(message, nightBedBookmarkNotePrefix) {
		return ""
	}
	return message
}

const (
	nightBedTimecodeNotReadyReason = "The timecode node was not ready in time, so the speakers start on their own."
	nightBedNoSharedStartReason    = "No shared start time could be picked, so the speakers start on their own."
)
