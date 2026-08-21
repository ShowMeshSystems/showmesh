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
}
