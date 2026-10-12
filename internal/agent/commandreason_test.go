package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
)

func TestOperatorReasonIsOnePlainLine(t *testing.T) {
	uncovered := fmt.Errorf("render.surface.apply: starting frame writer: %w",
		&fseq.ErrChannelRangeNotCovered{RequestedStart: 0, RequestedCount: 300})
	cases := []struct {
		name, action string
		err          error
		want         string
	}{
		{"sequence does not cover the surface", "render.surface.apply", uncovered, reasonSequenceLacksChannels},
		{"missing file", "render.surface.apply", fmt.Errorf("opening: %w", fs.ErrNotExist), reasonFileMissing},
		{"timeout", "audio.session.start", fmt.Errorf("start: %w", context.DeadlineExceeded), reasonTimedOut},
		{"wrapped operator line wins", "asset.fetch",
			fmt.Errorf("outer: %w", withOperatorReason(errors.New("sha mismatch"), "The file arrived damaged.")), "The file arrived damaged."},
		{"unknown render failure", "render.surface.apply", errors.New("render.surface.apply: params.geometry is required"),
			"This node could not change what the surface shows. Check this node's log."},
		{"unknown audio failure", "audio.session.prepare", errors.New("params.sessionId is required"),
			"This node could not run the audio request. Check this node's log."},
		{"unknown action", "agent.echo", errors.New("boom"), "This node could not finish the request. Check this node's log."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := operatorReason(tc.action, tc.err)
			if got != tc.want {
				t.Errorf("operatorReason = %q, want %q", got, tc.want)
			}
			if len(got) > 90 || strings.Contains(got, tc.action) {
				t.Errorf("reason %q is not one plain line", got)
			}
		})
	}
}

func TestWithOperatorReasonKeepsTheTechnicalTextForTheLog(t *testing.T) {
	cause := errors.New("asset.remove: hash mismatch")
	err := withOperatorReason(cause, "The file changed, so it was kept.")
	if err.Error() != cause.Error() || !errors.Is(err, cause) {
		t.Errorf("Error() = %q, want the technical cause %q, still matchable", err.Error(), cause.Error())
	}
	if withOperatorReason(nil, "unused") != nil {
		t.Error("a nil error gained a reason")
	}
}

func TestCueAudioFailureCarriesOnlyOperatorSentences(t *testing.T) {
	plain := operatorReason("cue.activate", cueAudioFailure("intro", "did not start", "No audio engine is connected yet. Retry once one connects."))
	if plain != `Cue "intro": No audio engine is connected yet. Retry once one connects.` {
		t.Errorf("reason = %q, want the node's own sentence carried", plain)
	}
	raw := operatorReason("cue.activate", cueAudioFailure("intro", "did not start", "gst: internal data stream error"))
	if raw != `Cue "intro"'s audio did not start. Check this node's log.` {
		t.Errorf("reason = %q, want engine text kept out of the operator line", raw)
	}
}
