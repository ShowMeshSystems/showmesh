package api

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// nightRecoverAction is the one recovery step every degraded reason ends in.
const nightRecoverAction = "Run End session, then Prepare site."

// The facts a degraded reason can state. Each is one sentence, and
// nightDegradedReason adds the recovery step.
const (
	nightDegradedNoReasonFact        = "The night stopped advancing."
	nightDegradedTimingLostFact      = "The next show start was lost and cannot be worked out again."
	nightDegradedTimingRetriesFact   = "The next show start could not be worked out after %d tries."
	nightDegradedRestingStoppedFact  = "FPP stopped the resting sequence early and nothing is playing."
	nightDegradedRestingAtStartFact  = "FPP stopped the resting sequence as the show was about to start."
	nightDegradedStartTimeLostFact   = "The show start time was lost on the way to the show."
	nightDegradedShowNoReportFact    = "FPP %s has no current report, so the end of the show was not confirmed."
	nightDegradedShowNotIdleFact     = "FPP %s has not reported idle since the show should have ended."
	nightDegradedShowPlaylistFact    = "FPP %s has not reported an empty playlist, so the end of the show was not confirmed."
	nightDegradedStartUnconfirmFact  = "FPP %s did not report playlist %s playing within a minute."
	nightDegradedStartFailedFact     = "Playlist %s could not be started on FPP %s."
	nightDegradedNoFPPToStopFact     = "This night names no FPP to stop, so it cannot fade out."
	nightDegradedRestartMidFact      = "The coordinator restarted mid-change and cannot tell what is safe to resume."
	nightDegradedRestartShowFact     = "The coordinator restarted and cannot tell if the show is still playing."
	nightDegradedRestartRestingFact  = "The coordinator restarted and cannot tell if the resting sequence is still playing."
	nightDegradedRestartEndNightFact = "The coordinator restarted and cannot tell if the end of night playlist is still playing."
	nightDegradedRestartMismatchFact = "The coordinator restarted and FPP is not playing what the night expects."
)

// nightDegradedFacts is every fact above plus the shared stop fact.
var nightDegradedFacts = []string{
	nightDegradedNoReasonFact, nightDegradedTimingLostFact, nightDegradedTimingRetriesFact,
	nightDegradedRestingStoppedFact, nightDegradedRestingAtStartFact, nightDegradedStartTimeLostFact,
	nightDegradedShowNoReportFact, nightDegradedShowNotIdleFact, nightDegradedShowPlaylistFact,
	nightDegradedStartUnconfirmFact, nightDegradedStartFailedFact, nightDegradedNoFPPToStopFact,
	nightDegradedRestartMidFact, nightDegradedRestartShowFact, nightDegradedRestartRestingFact,
	nightDegradedRestartEndNightFact, nightDegradedRestartMismatchFact, nightStopNotConfirmedFact,
}

// nightDegradedReason is a degraded reason: one fact, then the recovery step.
func nightDegradedReason(fact string, args ...any) string {
	return fmt.Sprintf(fact, args...) + " " + nightRecoverAction
}

var nightDegradedOldFormLogged sync.Map

// nightDegradedDetail is what a degraded session tells an operator. A reason
// stored before reasons carried their recovery step is logged once and
// replaced by the fixed line.
func nightDegradedDetail(stored string) string {
	if strings.HasSuffix(stored, nightRecoverAction) {
		return stored
	}
	if stored != "" {
		if _, logged := nightDegradedOldFormLogged.LoadOrStore(stored, true); !logged {
			slog.Default().Warn("api: night session carries a degraded reason in an older form", "reason", stored)
		}
	}
	return nightDegradedReason(nightDegradedNoReasonFact)
}

// One wording each for a stop that FPP did not confirm or did not get.
const (
	nightStopNotConfirmedFact = "FPP %s was told to stop but has not reported that it is idle."
	nightStopNotReachedFact   = "The stop did not reach FPP %s."
	nightStopByHandAction     = "Stop it by hand if it is still playing."
	nightTryingAgain          = "Trying again."
)

// nightCueStrandedReason is the one wording for a cue a restart left
// without a recorded result.
const nightCueStrandedReason = "The coordinator restarted while this cue was running. " + nightRecoverAction

// nightReadinessNotRunReason is the one wording for readiness that has not
// run since the site was prepared.
const nightReadinessNotRunReason = "Readiness has not run since the site was prepared. Run readiness."
