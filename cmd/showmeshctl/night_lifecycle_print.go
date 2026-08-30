package main

import (
	"fmt"
	"io"
)

// This file renders types_night_lifecycle.go's wire types as text,
// following night_print.go's own established conventions one file over.

func printNightSessionStateDetail(w io.Writer, s nightSessionStateWire) {
	_, _ = fmt.Fprintf(w, "State:       %s\n", s.State)
	if s.ID == "" {
		_, _ = fmt.Fprintf(w, "Session:     (no session has ever been created)\n")
		return
	}
	_, _ = fmt.Fprintf(w, "Session ID:  %s\n", s.ID)
	_, _ = fmt.Fprintf(w, "Config:      %s (revision %d)\n", s.ConfigObjectID, s.ConfigRevision)
	_, _ = fmt.Fprintf(w, "In state since: %s\n", s.StateEnteredAt)
	_, _ = fmt.Fprintf(w, "Cycle:       %d\n", s.Cycle)

	if s.Degraded {
		_, _ = fmt.Fprintf(w, "\nDEGRADED:    %s\n", s.DegradedReason)
	}
	if s.AttributionDegraded {
		_, _ = fmt.Fprintf(w, "ATTRIBUTION DEGRADED: this command applied, but its audit entry could not be written\n")
	}

	_, _ = fmt.Fprintf(w, "\nFinal show requested: %v", s.FinalShowRequested)
	if s.FinalShowRequestedAt != nil {
		_, _ = fmt.Fprintf(w, " (at %s)", *s.FinalShowRequestedAt)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "Admission closed:     %v", s.AdmissionClosed)
	if s.AdmissionClosedAt != nil {
		_, _ = fmt.Fprintf(w, " (at %s)", *s.AdmissionClosedAt)
	}
	_, _ = fmt.Fprintln(w)
	if s.ShutdownIntent != "" {
		_, _ = fmt.Fprintf(w, "Shutdown intent:      %s\n", s.ShutdownIntent)
	}

	if s.ArmedShowID != "" {
		_, _ = fmt.Fprintf(w, "\nArmed show:  %s (committed=%v)\n", s.ArmedShowID, s.ShowCommitted)
	}

	_, _ = fmt.Fprintf(w, "\nReadiness:   %s", s.Readiness.State)
	if s.Readiness.Outcome != "" {
		_, _ = fmt.Fprintf(w, " (outcome=%s sameEpoch=%v fresh=%v)", s.Readiness.Outcome, s.Readiness.SameEpoch, s.Readiness.Fresh)
	}
	_, _ = fmt.Fprintln(w)
	if s.Readiness.Reason != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", s.Readiness.Reason)
	}
	for _, c := range s.Readiness.Checks {
		_, _ = fmt.Fprintf(w, "  - %s: %s", c.Name, c.State)
		if c.Reason != "" {
			_, _ = fmt.Fprintf(w, " (%s)", c.Reason)
		}
		_, _ = fmt.Fprintln(w)
	}

	_, _ = fmt.Fprintf(w, "\nPower phase: %s (%s)\n", s.PowerPhase.State, s.PowerPhase.Reason)
	_, _ = fmt.Fprintf(w, "Transition:  %s (%s)\n", s.Transition.State, s.Transition.Reason)

	if s.Cues.State != "recorded" {
		_, _ = fmt.Fprintf(w, "\nCues:        %s (%s)\n", s.Cues.State, s.Cues.Reason)
	} else if len(s.Cues.Cues) == 0 {
		_, _ = fmt.Fprintf(w, "\nCues:        none configured\n")
	} else {
		_, _ = fmt.Fprintf(w, "\nCues:\n")
		for _, cue := range s.Cues.Cues {
			_, _ = fmt.Fprintf(w, "  - [%s] %s (role=%s action=%s): %s", cue.Phase, cue.Name, cue.Role, cue.Action, cue.State)
			if cue.Outcome != "" {
				_, _ = fmt.Fprintf(w, " outcome=%s", cue.Outcome)
			}
			if cue.ActionRevision != nil {
				_, _ = fmt.Fprintf(w, " rev=%d", *cue.ActionRevision)
			}
			_, _ = fmt.Fprintln(w)
			if cue.Reason != "" {
				_, _ = fmt.Fprintf(w, "      %s\n", cue.Reason)
			}
			if cue.DispatchedAt != nil {
				_, _ = fmt.Fprintf(w, "      dispatched: %s\n", *cue.DispatchedAt)
			}
			if cue.ResolvedAt != nil {
				_, _ = fmt.Fprintf(w, "      resolved:   %s\n", *cue.ResolvedAt)
			}
		}
	}

	if s.BackgroundAudio.State != "recorded" {
		_, _ = fmt.Fprintf(w, "\nBackground audio: %s (%s)\n", s.BackgroundAudio.State, s.BackgroundAudio.Reason)
		return
	}

	// Printed once, ahead of the step sections below (not nested under
	// either one, and not conditioned on either having steps): the pinned
	// ceiling describes the SESSION, not one sequence's own step log, and
	// is only ever present while running (see the OpenAPI description of
	// pinnedMaxGainDb) - found by review: an earlier version printed it
	// indented under the background section specifically, right after a
	// "not configured" line, and never printed at all when only
	// announcement steps existed.
	if s.BackgroundAudio.PinnedMaxGainDb != nil {
		_, _ = fmt.Fprintf(w, "\nPinned max gain: %.1f dB\n", *s.BackgroundAudio.PinnedMaxGainDb)
	} else if s.BackgroundAudio.Reason != "" {
		_, _ = fmt.Fprintf(w, "\nPinned max gain: none (%s)\n", s.BackgroundAudio.Reason)
	}

	// The two audio sequences print under their own headings. An
	// announcement's clear and start arrive in the same step list as the
	// bed's own steps, and a failure in one says something quite
	// different from a failure in the other: a refused announcement clear
	// means a previous announcement may still be playing and still
	// holding the bed ducked.
	background := nightAudioStepsForSequence(s.BackgroundAudio.Steps, "background")
	announcement := nightAudioStepsForSequence(s.BackgroundAudio.Steps, "announcement")

	if len(background) == 0 {
		_, _ = fmt.Fprintf(w, "\nBackground audio: not configured, or never started this cycle\n")
	} else {
		_, _ = fmt.Fprintf(w, "\nBackground audio:\n")
		printNightAudioSteps(w, background)
	}
	if len(announcement) > 0 {
		_, _ = fmt.Fprintf(w, "\nAnnouncement sessions:\n")
		printNightAudioSteps(w, announcement)
	}
}

// nightAudioStepsForSequence selects one sequence's steps. A step whose
// sequence is empty came from a coordinator older than the field and is
// treated as a background step, which is what every step was then.
func nightAudioStepsForSequence(steps []nightBackgroundAudioStepWire, sequence string) []nightBackgroundAudioStepWire {
	out := make([]nightBackgroundAudioStepWire, 0, len(steps))
	for _, step := range steps {
		got := step.Sequence
		if got == "" {
			got = "background"
		}
		if got == sequence {
			out = append(out, step)
		}
	}
	return out
}

func printNightAudioSteps(w io.Writer, steps []nightBackgroundAudioStepWire) {
	for _, step := range steps {
		_, _ = fmt.Fprintf(w, "  - [%s] %s (kind=%s rev=%d): %s", step.Phase, step.CueName, step.Kind, step.ActionRevision, step.State)
		if step.Outcome != "" {
			_, _ = fmt.Fprintf(w, " outcome=%s", step.Outcome)
		}
		_, _ = fmt.Fprintln(w)
		if step.Reason != "" {
			_, _ = fmt.Fprintf(w, "      %s\n", step.Reason)
		}
		if step.DispatchedAt != nil {
			_, _ = fmt.Fprintf(w, "      dispatched: %s\n", *step.DispatchedAt)
		}
		if step.ResolvedAt != nil {
			_, _ = fmt.Fprintf(w, "      resolved:   %s\n", *step.ResolvedAt)
		}
	}
}
