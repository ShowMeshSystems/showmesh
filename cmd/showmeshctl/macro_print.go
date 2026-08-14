package main

import (
	"fmt"
	"io"
)

// This file renders types_macro.go's wire types as text tables, following
// printers.go's own established conventions (a tabwriter for lists, a
// labeled block for one detail view, no colour as the only signal — task
// spec §3, carried into this step).

func printShowConfigObjectsTable(w io.Writer, resp showConfigObjectsListResponse) {
	if len(resp.Objects) == 0 {
		_, _ = fmt.Fprintf(w, "(no %s objects)\n", resp.Kind)
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tLABEL\tSHOW\tREVISION\tUPDATED")
	for _, o := range resp.Objects {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", o.ID, o.Label, o.Show, o.CurrentRevision, o.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	_ = tw.Flush()
}

func printShowActionDetail(w io.Writer, resp showActionConfigResponse) {
	_, _ = fmt.Fprintf(w, "Action ID:    %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:         %s\n", resp.Payload.Show)
	_, _ = fmt.Fprintf(w, "Label:        %s\n", resp.Payload.Label)
	if resp.Payload.Description != "" {
		_, _ = fmt.Fprintf(w, "Description:  %s\n", resp.Payload.Description)
	}
	_, _ = fmt.Fprintf(w, "Safety class: %s\n", resp.Payload.SafetyClass)
	_, _ = fmt.Fprintf(w, "Revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:      %s\n", resp.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:   %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:   (no principal recorded)\n")
	}
	_, _ = fmt.Fprintf(w, "\nTarget:\n")
	t := resp.Payload.Target
	_, _ = fmt.Fprintf(w, "  Integration: %s\n", t.Integration)
	switch t.Integration {
	case "fpp":
		_, _ = fmt.Fprintf(w, "  Instance:    %s\n", t.InstanceID)
		_, _ = fmt.Fprintf(w, "  Primitive:   %s\n", t.Primitive)
		if len(t.Params) > 0 {
			_, _ = fmt.Fprintf(w, "  Params:      %v\n", t.Params)
		}
	case "mqtt":
		_, _ = fmt.Fprintf(w, "  Broker:      %s\n", t.Broker)
		if t.Publish != nil {
			_, _ = fmt.Fprintf(w, "  Publish:     topic=%s qos=%d retain=%v payload=%q\n",
				t.Publish.Topic, t.Publish.QoS, t.Publish.Retain, t.Publish.Payload)
		}
		if t.Expect != nil {
			if t.Expect.Kind == "none" {
				_, _ = fmt.Fprintf(w, "  Expect:      none (this step is structurally unconfirmable)\n")
			} else {
				valueStr := "-"
				if t.Expect.Value != nil {
					valueStr = *t.Expect.Value
				}
				_, _ = fmt.Fprintf(w, "  Expect:      kind=%s topic=%s value=%s deadline=%ds\n",
					t.Expect.Kind, t.Expect.Topic, valueStr, t.Expect.DeadlineSeconds)
			}
		}
	default:
		_, _ = fmt.Fprintf(w, "  (unrecognized integration %q)\n", t.Integration)
	}
}

func printShowMacroDetail(w io.Writer, resp showMacroConfigResponse) {
	_, _ = fmt.Fprintf(w, "Macro ID:    %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:        %s\n", resp.Payload.Show)
	_, _ = fmt.Fprintf(w, "Label:       %s\n", resp.Payload.Label)
	if resp.Payload.Description != "" {
		_, _ = fmt.Fprintf(w, "Description: %s\n", resp.Payload.Description)
	}
	_, _ = fmt.Fprintf(w, "Revision:    %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:     %s\n", resp.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:  %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:  (no principal recorded)\n")
	}
	_, _ = fmt.Fprintf(w, "\nSteps (%d):\n", len(resp.Payload.Steps))
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  #\tID\tACTION\tON FAILURE\tON UNCONFIRMED\tLOCAL FALLBACK")
	for i, st := range resp.Payload.Steps {
		_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s (%s)\n",
			i, st.ID, st.Action, st.OnFailure, st.OnUnconfirmed, st.LocalFallback.Class, st.LocalFallback.Reason)
	}
	_ = tw.Flush()
}

func printMacroRunsTable(w io.Writer, resp macroRunsListResponse) {
	if len(resp.Runs) == 0 {
		_, _ = fmt.Fprintln(w, "(no macro runs)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "RUN ID\tMACRO\tSTATE\tOUTCOME\tTRIGGER\tISSUER\tCREATED")
	for _, r := range resp.Runs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.MacroObjectID, r.State, macroRunOutcomeGlyph(r.State, r.Completed, r.Confirmed, r.Reason),
			r.Trigger, r.IssuerPrincipalName, r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	_ = tw.Flush()
}

// macroRunOutcomeGlyph renders a run's own state plus its two STEP-9-SPEC.md
// section 2.3 booleans as one column: never a single green-tick-shaped
// word for every case, per section 2.3's own requirement that "a run that
// completed without confirmation must not render the same as" a run that
// confirmed. Mirrors format.go's stateGlyph/scopesStateGlyph convention of
// making the non-clean case visually loud rather than a bare lowercase
// word blending into the column.
func macroRunOutcomeGlyph(state string, completed, confirmed *bool, reason string) string {
	if state != "finished" {
		return "running"
	}
	c := completed != nil && *completed
	k := confirmed != nil && *confirmed
	var label string
	switch {
	case c && k:
		return "confirmed"
	case c && !k:
		label = "COMPLETED, NOT CONFIRMED"
	case !c:
		label = "ABORTED"
	}
	if reason != "" {
		return fmt.Sprintf("%s (%s)", label, reason)
	}
	return label
}

func printMacroRunDetail(w io.Writer, run macroRun) {
	_, _ = fmt.Fprintf(w, "Run ID:       %s\n", run.ID)
	_, _ = fmt.Fprintf(w, "Macro:        %s (revision %d)\n", run.MacroObjectID, run.MacroRevision)
	_, _ = fmt.Fprintf(w, "Show:         %s\n", run.Show)
	_, _ = fmt.Fprintf(w, "Trigger:      %s\n", run.Trigger)
	_, _ = fmt.Fprintf(w, "Issuer:       %s\n", run.IssuerPrincipalName)
	_, _ = fmt.Fprintf(w, "Created:      %s\n", run.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if run.FinishedAt != nil {
		_, _ = fmt.Fprintf(w, "Finished:     %s\n", run.FinishedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	_, _ = fmt.Fprintf(w, "State:        %s\n", run.State)
	_, _ = fmt.Fprintf(w, "Outcome:      %s\n", macroRunOutcomeGlyph(run.State, run.Completed, run.Confirmed, run.Reason))
	if run.AttributionDegraded {
		_, _ = fmt.Fprintln(w, "WARNING:      this run's audit attribution is degraded (the audit store was unwritable for at least one exempt step)")
	}
	_, _ = fmt.Fprintf(w, "\nSteps (%d):\n", len(run.Steps))
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  #\tID\tSTATE\tOUTCOME\tREASON")
	for _, st := range run.Steps {
		_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", st.StepIndex, st.StepID, st.State, st.Outcome, st.OutcomeReason)
	}
	_ = tw.Flush()
}

// printMacroRunProgressLine is followMacroRun's own text-mode renderer: one
// compact line per poll, cheap enough to print on every successful poll
// without cluttering the terminal the way a full printMacroRunDetail
// redraw on every 2-second tick would.
func printMacroRunProgressLine(w io.Writer, run macroRun) {
	dispatched, resolved := 0, 0
	for _, st := range run.Steps {
		if st.State != "pending" && st.State != "skipped" {
			dispatched++
		}
		if st.DispatchedAt != nil && st.ResolvedAt != nil {
			resolved++
		}
	}
	_, _ = fmt.Fprintf(w, "run %s: state=%s steps resolved=%d/%d outcome=%s\n",
		run.ID, run.State, resolved, len(run.Steps), macroRunOutcomeGlyph(run.State, run.Completed, run.Confirmed, run.Reason))
}
