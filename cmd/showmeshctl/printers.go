package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// This file renders the wire types in types.go as either a human-readable
// text table (task spec §3: readable over SSH, safe to pipe, no colour as
// the only signal) or JSON (a re-serialization of this program's own
// decoded structs — see the report's note on why that is not the same
// thing as echoing the coordinator's raw bytes back).

// printJSON marshals v with a stable two-space indent so scripted
// consumers get predictable output. Used everywhere this program prints
// exactly one JSON value for one command invocation.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printJSONCompact marshals v as one line with no indentation (still
// newline-terminated, since json.Encoder.Encode always appends one) and is
// [printJSON]'s sibling for exactly one caller: macro_client.go's follow
// loop, which prints many JSON objects over time rather than one. A
// multi-line, indented object cannot be told apart from the next one by a
// line-oriented tool (line-diffing, "read one JSON value per line"), which
// is the exact capability a follow stream's own doc comments claim for
// it — see renderMacroRunProgress's doc comment for where that claim used
// to be false.
func printJSONCompact(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
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
	_, _ = fmt.Fprintln(tw, "NODE ID\tLABEL\tCONTROL PLANE\tDECLARATION\tHELLO\tHEARTBEAT\tLAST WILL")
	for _, n := range resp.Nodes {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			n.NodeID,
			stringOrDash(n.Label),
			controlPlaneColumn(n.ControlPlane),
			declarationColumn(n.Declaration),
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

	printDeclarationDetail(w, n.Declaration)
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
	_, _ = fmt.Fprintln(tw, "INSTANCE ID\tENDPOINT\tHEALTH\tLAST POLL\tLAST POLL ERROR\tINSTANCE UUID")
	for _, f := range resp.Instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			f.InstanceID, f.Endpoint, healthGlyph(f.Health), timeOrDash(f.LastPollAt), stringOrDash(f.LastPollError),
			stringOrDash(f.InstanceUUID))
	}
	_ = tw.Flush()

	// the changed-uuid and duplicate-uuid rules rendered as explicit, unmissable lines, never
	// folded silently into the table above, which has no room to explain
	// WHY a uuid changed or WHICH other endpoint shares it.
	for _, f := range resp.Instances {
		if f.InstanceUUIDChange != nil {
			_, _ = fmt.Fprintf(w, "CONFLICT: %s now reports instance uuid %s, previously %s (changed %s), "+
				"acknowledge with \"showmeshctl fpp acknowledge-instance-uuid-change %s\" once verified\n",
				f.InstanceID, stringOrDash(f.InstanceUUID), f.InstanceUUIDChange.PreviousUUID,
				f.InstanceUUIDChange.ChangedAt.Format(time.RFC3339), f.InstanceID)
		}
		if len(f.DuplicateInstanceUUIDEndpointIDs) > 0 {
			_, _ = fmt.Fprintf(w, "DUPLICATE: %s reports the same instance uuid (%s) as: %s\n",
				f.InstanceID, stringOrDash(f.InstanceUUID), strings.Join(f.DuplicateInstanceUUIDEndpointIDs, ", "))
		}
	}

	for _, f := range resp.Instances {
		_, _ = fmt.Fprintf(w, "\n%s observations:\n", f.InstanceID)
		printObservations(w, f.Observations, resp.ServerTime)
	}
}

// printResolumeInstancesTable renders GET /resolume/instances and the
// resolume.changed stream event's instance, mirroring printFPPTable's
// shape: a one-line-per-instance summary (id, health, composition)
// followed by each instance's full observation table.
func printResolumeInstancesTable(w io.Writer, resp resolumeInstancesResponse) {
	if len(resp.Instances) == 0 {
		_, _ = fmt.Fprintln(w, "(no Resolume instance configured)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "INSTANCE ID\tHEALTH\tCOMPOSITION")
	for _, ri := range resp.Instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", ri.InstanceID, healthGlyph(ri.Health), resolumeCompositionSummaryColumn(ri.Composition))
	}
	_ = tw.Flush()

	for _, ri := range resp.Instances {
		_, _ = fmt.Fprintf(w, "\n%s observations:\n", ri.InstanceID)
		printObservations(w, ri.Observations, resp.ServerTime)
	}
}

// resolumeCompositionSummaryColumn renders ResolumeInstance.composition as
// a single table cell: "-" when nothing has ever been uploaded, or the
// loaded show's name once it has. Run `resolume composition show` for
// revision/upload provenance (that route is gated; this one is not).
func resolumeCompositionSummaryColumn(c *resolumeInstanceComposition) string {
	if c == nil {
		return "-"
	}
	return c.Name
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

// printSessionDetail renders GET /api/v1/session (ADR-024 decisions 5, 9,
// 12). "not authenticated" is printed as a plain, readable fact rather
// than an error banner: SessionResponse's own contract makes that a
// success response, and this function must not editorialize it into
// looking like something went wrong.
func printSessionDetail(w io.Writer, s sessionResponse) {
	if !s.Authenticated {
		_, _ = fmt.Fprintln(w, "Authenticated: no")
		if s.BootstrapRequired {
			_, _ = fmt.Fprintln(w, "Bootstrap:     REQUIRED — this coordinator holds zero principals (ADR-024 decision 9)")
		}
		_, _ = fmt.Fprintln(w, "\nNo credential authenticated this request. Set --token or $SHOWMESH_CTL_TOKEN")
		_, _ = fmt.Fprintln(w, "to an API token minted for a principal by an admin.")
		return
	}

	_, _ = fmt.Fprintln(w, "Authenticated: yes")
	if s.Principal != nil {
		_, _ = fmt.Fprintf(w, "Principal:     %s (id %s, kind %s)\n", s.Principal.Name, s.Principal.ID, s.Principal.Kind)
		_, _ = fmt.Fprintf(w, "Role:          %s\n", s.Principal.Role)
	}
	if s.CredentialForm != nil {
		_, _ = fmt.Fprintf(w, "Credential:    %s\n", *s.CredentialForm)
	}
	if s.Session != nil {
		label := s.Session.DeviceLabel
		if label == "" {
			label = "-"
		}
		_, _ = fmt.Fprintf(w, "Session:       id=%s device=%s\n", s.Session.ID, label)
	}
	_, _ = fmt.Fprintf(w, "Scopes state:  %s\n", scopesStateGlyph(s.ScopesState))
	if len(s.Scopes) == 0 {
		_, _ = fmt.Fprintln(w, "Scopes:        (none)")
	} else {
		for i, sc := range s.Scopes {
			prefix := "Scopes:        "
			if i > 0 {
				prefix = "               "
			}
			_, _ = fmt.Fprintf(w, "%s%s\n", prefix, sc)
		}
	}
	if s.BootstrapRequired {
		_, _ = fmt.Fprintln(w, "Bootstrap:     REQUIRED — this coordinator holds zero principals (ADR-024 decision 9)")
	}
}

// printAuditTable renders GET /api/v1/audit (ADR-024 decision 11).
func printAuditTable(w io.Writer, resp auditResponse) {
	if len(resp.Entries) == 0 {
		_, _ = fmt.Fprintln(w, "(no audit entries)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "TIMESTAMP\tKIND\tPRINCIPAL\tFORM\tACTION\tTARGET\tOUTCOME\tOUTCOME STATE")
	for _, e := range resp.Entries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s (%s)\t%s\t%s\t%s\t%s\t%s\n",
			e.Timestamp.Format(time.RFC3339), auditKindGlyph(e.Kind),
			emptyOrDash(e.PrincipalName), emptyOrDash(e.PrincipalID), emptyOrDash(e.Form),
			e.Action, e.Target, emptyOrDash(e.Outcome), emptyOrDash(e.OutcomeState))
	}
	_ = tw.Flush()
}

// printFPPEndpointsConfig renders GET/PUT /api/v1/config/fpp.endpoints
// (Step 7 seam A, RES-008 D1). RestartRequiredReason is always rendered,
// never silently dropped; the loud RESTART REQUIRED label is driven by the
// wire boolean rather than assumed, so this stays correct if a future
// server ever sets it true again.
func printFPPEndpointsConfig(w io.Writer, resp fppEndpointsConfigResponse) {
	_, _ = fmt.Fprintf(w, "kind:      %s\n", resp.Kind)
	_, _ = fmt.Fprintf(w, "revision:  %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "source:    %s\n", resp.Source)
	_, _ = fmt.Fprintf(w, "createdBy: %s\n", stringOrDash(resp.CreatedByPrincipalName))
	_, _ = fmt.Fprintf(w, "updatedAt: %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.RestartRequired {
		_, _ = fmt.Fprintf(w, "\nRESTART REQUIRED: %s\n\n", resp.RestartRequiredReason)
	} else {
		// The reason is a full sentence from the server; a label in front
		// of it would just restate its opening clause.
		_, _ = fmt.Fprintf(w, "\n%s\n\n", resp.RestartRequiredReason)
	}

	if len(resp.Payload.Endpoints) == 0 {
		_, _ = fmt.Fprintln(w, "(no FPP endpoints configured)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tURL")
	for _, e := range resp.Payload.Endpoints {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", e.ID, e.URL)
	}
	_ = tw.Flush()
}

// printResolumeInstancesConfig renders GET/PUT
// /api/v1/config/resolume.instances (Track G seam G-2, ADR-039), mirroring
// printFPPEndpointsConfig's identical shape.
func printResolumeInstancesConfig(w io.Writer, resp resolumeInstancesConfigResponse) {
	_, _ = fmt.Fprintf(w, "kind:      %s\n", resp.Kind)
	_, _ = fmt.Fprintf(w, "revision:  %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "source:    %s\n", resp.Source)
	_, _ = fmt.Fprintf(w, "createdBy: %s\n", stringOrDash(resp.CreatedByPrincipalName))
	_, _ = fmt.Fprintf(w, "updatedAt: %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.RestartRequired {
		_, _ = fmt.Fprintf(w, "\nRESTART REQUIRED: %s\n\n", resp.RestartRequiredReason)
	} else {
		_, _ = fmt.Fprintf(w, "\n%s\n\n", resp.RestartRequiredReason)
	}

	if len(resp.Payload.Instances) == 0 {
		_, _ = fmt.Fprintln(w, "(no Resolume instance configured)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tURL")
	for _, e := range resp.Payload.Instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", e.ID, e.URL)
	}
	_ = tw.Flush()
}

// printFPPMQTTConfig renders GET/PUT /api/v1/config/fpp.mqtt (Track G seam
// G-3, ADR-039), mirroring printFPPEndpointsConfig's shape. The password
// itself never appears on the wire (decision 7); only "set"/"not set".
func printFPPMQTTConfig(w io.Writer, resp fppMQTTConfigResponse) {
	_, _ = fmt.Fprintf(w, "kind:      %s\n", resp.Kind)
	_, _ = fmt.Fprintf(w, "revision:  %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "source:    %s\n", resp.Source)
	_, _ = fmt.Fprintf(w, "createdBy: %s\n", stringOrDash(resp.CreatedByPrincipalName))
	_, _ = fmt.Fprintf(w, "updatedAt: %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.RestartRequired {
		_, _ = fmt.Fprintf(w, "\nRESTART REQUIRED: %s\n\n", resp.RestartRequiredReason)
	} else {
		_, _ = fmt.Fprintf(w, "\n%s\n\n", resp.RestartRequiredReason)
	}

	if resp.Payload.BrokerURL == "" {
		_, _ = fmt.Fprintln(w, "(no FPP MQTT broker configured)")
		return
	}
	_, _ = fmt.Fprintf(w, "brokerURL:   %s\n", resp.Payload.BrokerURL)
	_, _ = fmt.Fprintf(w, "username:    %s\n", resp.Payload.Username)
	_, _ = fmt.Fprintf(w, "topicPrefix: %s\n", resp.Payload.TopicPrefix)
	_, _ = fmt.Fprintf(w, "password:    %s\n", fppMQTTPasswordLabel(resp.Payload.PasswordSet))

	if len(resp.Payload.Hosts) == 0 {
		_, _ = fmt.Fprintln(w, "(no hosts configured)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "INSTANCE ID\tHOSTNAME")
	for id, name := range resp.Payload.Hosts {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", id, name)
	}
	_ = tw.Flush()
}

func fppMQTTPasswordLabel(set bool) string {
	if set {
		return "set"
	}
	return "not set"
}

// printAssetsSettingsConfig renders GET/PUT
// /api/v1/config/assets.settings (Track G seam G-4, ADR-039), mirroring
// printResolumeInstancesConfig's identical shape.
func printAssetsSettingsConfig(w io.Writer, resp assetsSettingsConfigResponse) {
	_, _ = fmt.Fprintf(w, "kind:      %s\n", resp.Kind)
	_, _ = fmt.Fprintf(w, "revision:  %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "source:    %s\n", resp.Source)
	_, _ = fmt.Fprintf(w, "createdBy: %s\n", stringOrDash(resp.CreatedByPrincipalName))
	_, _ = fmt.Fprintf(w, "updatedAt: %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.RestartRequired {
		_, _ = fmt.Fprintf(w, "\nRESTART REQUIRED: %s\n\n", resp.RestartRequiredReason)
	} else {
		_, _ = fmt.Fprintf(w, "\n%s\n\n", resp.RestartRequiredReason)
	}

	contentBaseURL := resp.Payload.ContentBaseURL
	if contentBaseURL == "" {
		contentBaseURL = "(none — asset sync disabled)"
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintf(tw, "contentBaseUrl:\t%s\n", contentBaseURL)
	_, _ = fmt.Fprintf(tw, "maxUploadBytes:\t%d\n", resp.Payload.MaxUploadBytes)
	_, _ = fmt.Fprintf(tw, "syncInterval:\t%s\n", time.Duration(resp.Payload.SyncIntervalSeconds*float64(time.Second)).String())
	_, _ = fmt.Fprintf(tw, "inventoryInterval:\t%s\n", time.Duration(resp.Payload.InventoryIntervalSeconds*float64(time.Second)).String())
	_ = tw.Flush()
}

// printConfigRevisionsTable renders GET
// /api/v1/config/fpp.endpoints/revisions, newest first.
func printConfigRevisionsTable(w io.Writer, resp configRevisionsResponse) {
	if len(resp.Revisions) == 0 {
		_, _ = fmt.Fprintln(w, "(no revisions)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "REVISION\tACTIVE\tCREATED AT\tCREATED BY\tSOURCE\tNOTE")
	for _, r := range resp.Revisions {
		active := ""
		if r.Active {
			active = "*"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			r.Revision, active, r.CreatedAt.Format(time.RFC3339),
			stringOrDash(r.CreatedByPrincipalName), r.Source, emptyOrDash(r.Note))
	}
	_ = tw.Flush()
}

// printDeclarationDetail renders node.declaration (RES-008 D2/D6,
// BUILD-PLAN Step 7 seam B) for `showmeshctl node`'s detail view.
func printDeclarationDetail(w io.Writer, d nodeDeclaration) {
	_, _ = fmt.Fprintln(w, "Declaration:")
	if !d.Declared {
		_, _ = fmt.Fprintln(w, "  (not declared — an observed node nobody has promoted; see `showmeshctl discover`)")
		return
	}
	_, _ = fmt.Fprintf(w, "  Label:            %s\n", stringOrDash(d.Label))
	_, _ = fmt.Fprintf(w, "  Notes:            %s\n", stringOrDash(d.Notes))
	_, _ = fmt.Fprintf(w, "  Declared at:      %s\n", timeOrDash(d.DeclaredAt))
	_, _ = fmt.Fprintf(w, "  Declared by:      %s (%s)\n", stringOrDash(d.DeclaredByPrincipalName), stringOrDash(d.DeclaredByPrincipalID))
	_, _ = fmt.Fprintf(w, "  Discovery state:  %s\n", d.DiscoveryState)
	if d.DiscoveryReason != nil {
		_, _ = fmt.Fprintf(w, "  Discovery reason: %s\n", *d.DiscoveryReason)
	}
	_, _ = fmt.Fprintf(w, "  Last discovery run: %s (%s)\n", stringOrDash(d.LastDiscoveryRunID), timeOrDash(d.LastDiscoveredAt))
	// DEFECT 8: printed ONLY when discoveryState is "not_seen", from its
	// own separately named fields — never folded into "Last discovery
	// run" above, which reports this declaration's OWN last-seen
	// bookkeeping and must never be overwritten by the run that failed to
	// see it.
	if d.NotSeenAsOfRunID != nil {
		_, _ = fmt.Fprintf(w, "  Not seen as of run: %s (finished %s)\n", *d.NotSeenAsOfRunID, timeOrDash(d.NotSeenAsOfRunFinishedAt))
	}
}

// printDiscoveryRunResult renders the body of POST /api/v1/discovery/runs
// (BUILD-PLAN Step 7 seam B B1). The honest consequence B1 requires stated
// rather than implied: a discovery run reads what this coordinator already
// observes and cannot find equipment that has never talked to ShowMesh.
func printDiscoveryRunResult(w io.Writer, resp discoveryRunResponse) {
	_, _ = fmt.Fprintf(w, "Discovery run %s\n", resp.Run.ID)
	_, _ = fmt.Fprintf(w, "  Started at:  %s\n", resp.Run.StartedAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "  Finished at: %s\n", timeOrDash(resp.Run.FinishedAt))
	completeStr := "yes"
	if !resp.Run.Complete {
		completeStr = "NO"
	}
	_, _ = fmt.Fprintf(w, "  Complete:    %s\n", completeStr)
	if resp.Run.Reason != nil {
		_, _ = fmt.Fprintf(w, "  Reason:      %s\n", *resp.Run.Reason)
	}
	_, _ = fmt.Fprintf(w, "  Found:       %d\n", resp.Run.FoundCount)
	_, _ = fmt.Fprintf(w, "  Initiated by: %s (%s)\n\n", resp.Run.InitiatedByPrincipalName, resp.Run.InitiatedByPrincipalID)

	if len(resp.Proposals) == 0 {
		_, _ = fmt.Fprintln(w, "No undeclared entities observed. This run performed no active probing — it cannot find equipment that has never talked to ShowMesh; see `showmeshctl discover --help`.")
		return
	}
	_, _ = fmt.Fprintln(w, "Proposals (observed but not declared — promote with `showmeshctl declare <id>`):")
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  NODE ID\tSOURCE")
	for _, p := range resp.Proposals {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", p.NodeID, p.Source)
	}
	_ = tw.Flush()
}

// printNodeDeclarationResult renders the body of
// POST /api/v1/nodes/{nodeId}/declaration.
func printNodeDeclarationResult(w io.Writer, resp nodeDeclarationResponse) {
	printDeclarationDetail(w, resp.Declaration)
}

func printSnapshotDetail(w io.Writer, s snapshot) {
	_, _ = fmt.Fprintf(w, "serverTime:     %s\n", s.ServerTime.Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "latestEventSeq: %d\n\n", s.LatestEventSeq)

	_, _ = fmt.Fprintln(w, "Nodes:")
	printNodesTable(w, nodesResponse{ServerTime: s.ServerTime, Nodes: s.Nodes})

	_, _ = fmt.Fprintln(w, "\nFPP instances:")
	printFPPTable(w, fppResponse{ServerTime: s.ServerTime, Instances: s.FPP.Instances})

	_, _ = fmt.Fprintln(w, "\nResolume instances:")
	printResolumeInstancesTable(w, resolumeInstancesResponse{ServerTime: s.ServerTime, Instances: s.Resolume})

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

// printPrincipalsTable renders GET /api/v1/principals (Track G seam G-5).
func printPrincipalsTable(w io.Writer, resp principalsResponse) {
	if len(resp.Principals) == 0 {
		_, _ = fmt.Fprintln(w, "(no principals)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tKIND\tROLE\tDISABLED\tHAS PASSWORD\tRESERVED\tCREATED AT")
	for _, p := range resp.Principals {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\t%v\t%v\t%s\n",
			p.ID, p.Name, p.Kind, p.Role, p.Disabled, p.HasPassword, p.Reserved, p.CreatedAt.Format(time.RFC3339))
	}
	_ = tw.Flush()
}

// printPrincipalDetail renders one principalObject -- POST/GET/PUT .../role,
// POST .../enable, .../disable, and .../password all share this shape.
func printPrincipalDetail(w io.Writer, p principalObject) {
	_, _ = fmt.Fprintf(w, "ID:           %s\n", p.ID)
	_, _ = fmt.Fprintf(w, "Name:         %s\n", p.Name)
	_, _ = fmt.Fprintf(w, "Kind:         %s\n", p.Kind)
	_, _ = fmt.Fprintf(w, "Role:         %s\n", p.Role)
	_, _ = fmt.Fprintf(w, "Disabled:     %v\n", p.Disabled)
	_, _ = fmt.Fprintf(w, "Has password: %v\n", p.HasPassword)
	_, _ = fmt.Fprintf(w, "Reserved:     %v\n", p.Reserved)
	_, _ = fmt.Fprintf(w, "Created at:   %s\n", p.CreatedAt.Format(time.RFC3339))
}

// printTokensTable renders GET /api/v1/principals/{id}/tokens (Track G
// seam G-5). Never prints a digest or a raw value -- tokenObject carries
// neither.
func printTokensTable(w io.Writer, resp tokensResponse) {
	if len(resp.Tokens) == 0 {
		_, _ = fmt.Fprintln(w, "(no tokens)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tHINT\tLABEL\tCREATED AT\tEXPIRES\tLAST USED")
	for _, t := range resp.Tokens {
		label := t.Label
		if label == "" {
			label = "-"
		}
		expires := "never"
		if t.ExpiresAt != nil {
			expires = t.ExpiresAt.Format(time.RFC3339)
		}
		lastUsed := "never"
		if t.LastUsedAt != nil {
			lastUsed = t.LastUsedAt.Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Hint, label, t.CreatedAt.Format(time.RFC3339), expires, lastUsed)
	}
	_ = tw.Flush()
}
