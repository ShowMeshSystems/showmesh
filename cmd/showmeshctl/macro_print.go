package main

import (
	"fmt"
	"io"
	"strings"
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
	case "resolume":
		_, _ = fmt.Fprintf(w, "  Action:      %s\n", t.Action)
		if len(t.Ref) > 0 {
			_, _ = fmt.Fprintf(w, "  Ref:         %v\n", t.Ref)
		}
	case "audio":
		_, _ = fmt.Fprintf(w, "  Node:        %s\n", strings.Join(t.AudioNodeIDs, ", "))
		_, _ = fmt.Fprintf(w, "  Session:     %s\n", t.AudioSessionID)
		_, _ = fmt.Fprintf(w, "  Action:      %s\n", t.AudioAction)
		if len(t.Params) > 0 {
			_, _ = fmt.Fprintf(w, "  Params:      %v\n", t.Params)
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
//
// A finished run whose Completed or Confirmed pointer is nil is its own,
// FOURTH case, deliberately never folded into "ABORTED": collapsing "the
// coordinator did not report this" into a definite negative would assert
// something absent evidence never claimed (this project's own recurring
// rule — absence of evidence is not evidence of absence). A prior version
// of this function computed `c := completed != nil && *completed`, which
// silently read a nil pointer as false and rendered a run the coordinator
// never actually reported as aborted as an outright ABORT, even though the
// exit-code sibling of this function (exitCodeForMacroRun) already treated
// nil specially and refused to guess. See exitCodeForMacroRun's own doc
// comment for the matching exit-code case.
func macroRunOutcomeGlyph(state string, completed, confirmed *bool, reason string) string {
	if state != "finished" {
		return "running"
	}
	var label string
	switch {
	case completed == nil || confirmed == nil:
		label = "OUTCOME NOT REPORTED"
	case *completed && *confirmed:
		return "confirmed"
	case *completed && !*confirmed:
		label = "COMPLETED, NOT CONFIRMED"
	case !*completed:
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
	_, _ = fmt.Fprintln(tw, "  #\tID\tSTATE\tOUTCOME\tOUTCOME STATE\tREASON")
	for _, st := range run.Steps {
		_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s\n", st.StepIndex, st.StepID, st.State, st.Outcome, st.OutcomeState, st.OutcomeReason)
	}
	_ = tw.Flush()

	// Each step's own command evidence (macroRunStepCommand: state/id/
	// reason, plus the dispatched fppCommandResult detail when retained)
	// is decoded off the wire and, until this fix, discarded before
	// reaching the operator — indistinguishable in the text renderer from
	// evidence that was never sent at all, which is exactly what this
	// API's "absent evidence is stated, never omitted" rule forbids. See
	// printMacroRunStepCommand.
	for _, st := range run.Steps {
		printMacroRunStepCommand(w, st)
	}
}

// printMacroRunStepCommand renders one step's macroRunStepCommand.State:
// "none" (an MQTT step, or an FPP step that never dispatched), "retained"
// (a dispatched FPP command whose row still exists), or "not_retained"
// (dispatched, but the commands row has since been pruned by retention —
// STEP-9-SPEC.md section 6.1: the step's own Outcome/OutcomeState/
// OutcomeReason above are unaffected by pruning; only this per-command
// detail can go missing). An operator reading only the steps table above
// cannot tell "retained" from "not_retained" from "none" — they all just
// look like a step that ran — so this is its own line per step rather than
// folded into the table.
func printMacroRunStepCommand(w io.Writer, st macroRunStep) {
	cmd := st.Command
	_, _ = fmt.Fprintf(w, "  step %d (%s) command: %s", st.StepIndex, st.StepID, macroRunStepCommandStateGlyph(cmd.State))
	if cmd.ID != nil {
		_, _ = fmt.Fprintf(w, " (id %s)", *cmd.ID)
	}
	if cmd.Reason != "" {
		_, _ = fmt.Fprintf(w, ": %s", cmd.Reason)
	}
	_, _ = fmt.Fprintln(w)
	if cmd.Detail != nil {
		d := cmd.Detail
		_, _ = fmt.Fprintf(w, "    dispatched %s on %s: outcome=%s state=%s reason=%s\n",
			d.Action, d.InstanceID, d.Outcome, d.OutcomeState, d.OutcomeReason)
	}
}

// macroRunStepCommandStateGlyph makes "not_retained" visually loud, the
// same convention stateGlyph/healthGlyph/severityGlyph use elsewhere in
// this package: it is the one state of the three that means "evidence
// existed and is now gone", and must not read the same as "none" (no
// evidence was ever expected) at a glance.
func macroRunStepCommandStateGlyph(state string) string {
	switch state {
	case "none":
		return "none"
	case "retained":
		return "retained"
	case "not_retained":
		return "NOT RETAINED"
	default:
		return "UNRECOGNIZED(" + state + ")"
	}
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
