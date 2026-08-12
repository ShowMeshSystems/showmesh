package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// This file renders the wire types in types.go as either a human-readable
// text table (task spec §3: readable over SSH, safe to pipe, no colour as
// the only signal) or JSON (a re-serialization of this program's own
// decoded structs — see the report's note on why that is not the same
// thing as echoing the coordinator's raw bytes back).

// printJSON marshals v with a stable two-space indent so scripted
// consumers get predictable output.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printClockSkew writes contract-required freshness self-defense (task
// spec §3) to stderr, never stdout: it is diagnostic noise about the
// environment, not part of the data being requested, and must not
// contaminate `--output json | jq` or any other pipeline.
func printClockSkew(stderr io.Writer, serverTime, localNow time.Time) {
	if w := clockSkewWarning(serverTime, localNow); w != "" {
		_, _ = fmt.Fprintln(stderr, w)
	}
}

// evidenceColumn renders one evidence envelope (contract §6.3) as a single
// table cell: its state (never silently "fine" for anything but current —
// format.go's stateGlyph) plus its age against serverTime when an
// observation time exists at all.
func evidenceColumn(e evidence, serverTime time.Time) string {
	st := stateGlyph(e.State, e.Reason)
	if e.ObservedAt != nil {
		return fmt.Sprintf("%s (%s)", st, ageAgainst(e.ObservedAt, serverTime))
	}
	return st
}

// valueDisplay renders an observation's value for a table cell, "-" for
// nil (every absence state has a nil value, per contract §6.3).
func valueDisplay(v any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%v", v)
}

func printNodesTable(w io.Writer, resp nodesResponse) {
	if len(resp.Nodes) == 0 {
		_, _ = fmt.Fprintln(w, "(no nodes in inventory)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "NODE ID\tLABEL\tCONTROL PLANE\tHELLO\tHEARTBEAT\tLAST WILL")
	for _, n := range resp.Nodes {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			n.NodeID,
			stringOrDash(n.Label),
			controlPlaneColumn(n.ControlPlane),
			evidenceColumn(n.Evidence.Hello, resp.ServerTime),
			evidenceColumn(n.Evidence.Heartbeat, resp.ServerTime),
			evidenceColumn(n.Evidence.LastWill, resp.ServerTime),
		)
	}
	_ = tw.Flush()
}

func printNodeDetail(w io.Writer, n node, serverTime time.Time) {
	_, _ = fmt.Fprintf(w, "Node ID:       %s\n", n.NodeID)
	_, _ = fmt.Fprintf(w, "Label:         %s\n", stringOrDash(n.Label))
	_, _ = fmt.Fprintf(w, "Platform:      %s\n", stringOrDash(n.Platform))
	_, _ = fmt.Fprintf(w, "Agent version: %s\n", stringOrDash(n.AgentVersion))
	_, _ = fmt.Fprintf(w, "Boot ID:       %s\n", stringOrDash(n.BootID))
	_, _ = fmt.Fprintf(w, "Started at:    %s\n", timeOrDash(n.StartedAt))
	// FirstSeenAt/UpdatedAt are non-pointer time.Time (always present per
	// contract §6.10 — see types.go's doc comment), so these render
	// directly rather than through timeOrDash, which exists for the
	// genuinely optional timestamps above.
	_, _ = fmt.Fprintf(w, "First seen at: %s\n", n.FirstSeenAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "Updated at:    %s\n", n.UpdatedAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "%s\n", controlPlaneColumn(n.ControlPlane))
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "Capabilities:")
	if len(n.Capabilities) == 0 {
		_, _ = fmt.Fprintln(w, "  (none observed)")
	}
	for _, capa := range n.Capabilities {
		_, _ = fmt.Fprintf(w, "  %s v%d %v\n", capa.ID, capa.Version, capa.Attributes)
	}
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "Evidence:")
	printEvidenceRow(w, "hello", n.Evidence.Hello, serverTime)
	printEvidenceRow(w, "heartbeat", n.Evidence.Heartbeat, serverTime)
	printEvidenceRow(w, "lastWill", n.Evidence.LastWill, serverTime)
}

func printEvidenceRow(w io.Writer, label string, e evidence, serverTime time.Time) {
	observed := "null"
	if e.ObservedAt != nil {
		observed = fmt.Sprintf("%s (%s)", e.ObservedAt.Format(time.RFC3339), ageAgainst(e.ObservedAt, serverTime))
	}
	_, _ = fmt.Fprintf(w, "  %-9s signal=%-28s value=%-8s state=%-20s observedAt=%s collectedAt=%s source=%s quality=%s\n",
		label, e.Signal, valueDisplay(e.Value), stateGlyph(e.State, e.Reason),
		observed, e.CollectedAt.Format(time.RFC3339), e.Source, e.Quality)
}

// healthGlyph renders fppInstance.Health (ADR-011's five Health values)
// distinctly from "healthy" for everything else, for the same reason
// stateGlyph does for evidence states.
func healthGlyph(h string) string {
	switch h {
	case "healthy":
		return "healthy"
	case "unknown":
		return "UNKNOWN"
	case "degraded":
		return "DEGRADED"
	case "failed":
		return "FAILED"
	case "suppressed":
		return "SUPPRESSED"
	default:
		return "UNRECOGNIZED(" + h + ")"
	}
}

func printFPPTable(w io.Writer, resp fppResponse) {
	if len(resp.Instances) == 0 {
		_, _ = fmt.Fprintln(w, "(no FPP instances configured)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "INSTANCE ID\tENDPOINT\tHEALTH\tLAST POLL\tLAST POLL ERROR")
	for _, f := range resp.Instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			f.InstanceID, f.Endpoint, healthGlyph(f.Health), timeOrDash(f.LastPollAt), stringOrDash(f.LastPollError))
	}
	_ = tw.Flush()

	for _, f := range resp.Instances {
		_, _ = fmt.Fprintf(w, "\n%s observations:\n", f.InstanceID)
		printObservations(w, f.Observations, resp.ServerTime)
	}
}

// printFPPObservationsChangedLine renders one fpp.observations.changed
// frame (ADR-023) for --output text: which instance, which signals moved
// (with their new value/state, exactly like printObservations' columns),
// and which signals stopped existing entirely. Only produced on a
// connection opened with --deltas — see cmdWatch.
func printFPPObservationsChangedLine(w io.Writer, ev streamFPPObservationsChanged) {
	_, _ = fmt.Fprintf(w, "[fpp.observations.changed] %s\n", ev.InstanceID)
	if len(ev.Changed) > 0 {
		_, _ = fmt.Fprintln(w, "  changed:")
		printObservations(w, ev.Changed, ev.ServerTime)
	}
	if len(ev.Removed) > 0 {
		_, _ = fmt.Fprintf(w, "  removed: %v\n", ev.Removed)
	}
}

func printObservations(w io.Writer, obs []evidence, serverTime time.Time) {
	if len(obs) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  SIGNAL\tVALUE\tSTATE\tAGE")
	for _, o := range obs {
		age := "-"
		if o.ObservedAt != nil {
			age = ageAgainst(o.ObservedAt, serverTime)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", o.Signal, valueDisplay(o.Value), stateGlyph(o.State, o.Reason), age)
	}
	_ = tw.Flush()
}

// severityGlyph renders event.severity (OBSERVABILITY §11.2's three
// values) with warning/critical made visually louder than informational,
// again without relying on colour.
func severityGlyph(sev string) string {
	switch sev {
	case "informational":
		return "info"
	case "warning":
		return "WARNING"
	case "critical":
		return "CRITICAL"
	default:
		return "UNRECOGNIZED(" + sev + ")"
	}
}

func printEventsTable(w io.Writer, resp eventsResponse) {
	if resp.Gap {
		oldest := "unknown"
		if resp.OldestRetainedSeq != nil {
			oldest = fmt.Sprintf("%d", *resp.OldestRetainedSeq)
		}
		_, _ = fmt.Fprintf(w, "GAP: events older than seq %s have been pruned from history and are gone; this page is not a complete history for the range requested.\n", oldest)
	}
	if len(resp.Events) == 0 {
		_, _ = fmt.Fprintln(w, "(no events)")
		_, _ = fmt.Fprintf(w, "latestSeq: %d\n", resp.LatestSeq)
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "SEQ\tRECORDED AT\tOCCURRED AT\tSEVERITY\tCATEGORY\tRESOURCE\tSUMMARY")
	for _, e := range resp.Events {
		occurred := "unknown age (e.g. learned from a retained delivery)"
		if e.OccurredAt != nil {
			occurred = e.OccurredAt.Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s:%s\t%s\n",
			e.Seq, e.RecordedAt.Format(time.RFC3339), occurred, severityGlyph(e.Severity),
			e.Category, e.Resource.Kind, e.Resource.ID, e.Summary)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "latestSeq: %d\n", resp.LatestSeq)
}

func printSnapshotDetail(w io.Writer, s snapshot) {
	_, _ = fmt.Fprintf(w, "serverTime:     %s\n", s.ServerTime.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "latestEventSeq: %d\n\n", s.LatestEventSeq)

	_, _ = fmt.Fprintln(w, "Nodes:")
	printNodesTable(w, nodesResponse{ServerTime: s.ServerTime, Nodes: s.Nodes})

	_, _ = fmt.Fprintln(w, "\nFPP instances:")
	printFPPTable(w, fppResponse{ServerTime: s.ServerTime, Instances: s.FPP.Instances})

	_, _ = fmt.Fprintln(w, "\nCollectors:")
	if len(s.Collectors) == 0 {
		_, _ = fmt.Fprintln(w, "  (none configured)")
	}
	for _, c := range s.Collectors {
		_, _ = fmt.Fprintf(w, "  %s: %s", c.ID, c.State)
		if c.Reason != nil && *c.Reason != "" {
			_, _ = fmt.Fprintf(w, " (%s)", *c.Reason)
		}
		_, _ = fmt.Fprintln(w)
	}
}
