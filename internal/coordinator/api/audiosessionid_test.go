package api

import (
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// Every session id this coordinator mints must match
// audioSessionIDPattern, or the coordinator can drive that session
// internally while no operator can ever address it through the API. The
// cue-activation ids carry a colon and failed exactly that way: a Cue's
// audio plays in cue-activation:show, which no operator could stop.
// This asserts against audioSessionIDPattern ITSELF, never a copied
// regex, so the two cannot drift apart.
func TestCoordinatorMintedAudioSessionIDsAreAddressable(t *testing.T) {
	for _, id := range []string{
		cueactivation.AudioSessionID,
		cueactivation.BackgroundSessionID,
		cueactivation.AnnouncementSessionID,
		cueactivation.PrepareStagingSessionID,
	} {
		if !audioSessionIDPattern.MatchString(id) {
			t.Errorf("session id %q does not match audioSessionIDPattern %s; no operator surface could ever address this session", id, audioSessionIDPattern.String())
		}
	}
}

// Widening the pattern for a colon must not admit anything that could
// escape a path segment, traverse a directory, or act as an MQTT
// wildcard.
func TestAudioSessionIDPatternStillRefusesUnsafeIdentifiers(t *testing.T) {
	for _, id := range []string{
		"",
		"..",
		"../etc/passwd",
		"a/b",
		"a\\b",
		"a b",
		"a+b",
		"a#b",
		"#",
		"+",
		":leading-colon",
		"a\nb",
		"a\x00b",
	} {
		if audioSessionIDPattern.MatchString(id) {
			t.Errorf("audioSessionIDPattern accepted %q, which is not a safe identifier", id)
		}
	}
}
