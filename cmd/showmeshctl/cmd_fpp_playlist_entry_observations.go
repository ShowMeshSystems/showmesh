package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"text/tabwriter"
	"time"
)

// showmeshctl surface for TRACK-H-H2-SPEC.md §5/§5.1/§7's playlist-entry
// observation reads and recovery route. Declares its own wire types
// rather than importing the coordinator's v1 package, mirroring
// cmd_fpp_playlist_definition.go's identical shape.
//
// "playlist-entry-observations list" is the read half
// cmd_fpp.go's write-parity exemption comment for
// POST /integrations/fpp/playlist-entry-observations already named as
// owed "when Track H gives an operator a reason to look at it" — this
// seam is that reason. "playlist-entry-observations reconciliation" and
// "reset-observation-sequence" are H2's own two new routes (§5, §5.1).

type fppPlaylistEntryObservation struct {
	InstanceUUID                       string `json:"instanceUuid"`
	SchemaVersion                      int    `json:"schemaVersion"`
	Sequence                           int64  `json:"sequence"`
	PlaylistName                       string `json:"playlistName"`
	PlaylistHash                       string `json:"playlistHash"`
	Section                            string `json:"section"`
	Position                           *int   `json:"position"`
	EntryKey                           string `json:"entryKey"`
	SequenceFilename                   string `json:"sequenceFilename"`
	MediaFilename                      string `json:"mediaFilename"`
	Action                             string `json:"action"`
	Unavailable                        string `json:"unavailable"`
	ObservedAt                         string `json:"observedAt"`
	CoalescedSincePreviousAcknowledged int64  `json:"coalescedSincePreviousAcknowledged"`
	ReceivedAt                         string `json:"receivedAt"`

	// EndpointID is the configured fpp.endpoints id this
	// observation's instanceUuid resolves to, best-effort. Nil when no
	// single currently configured endpoint owns this uuid.
	EndpointID *string `json:"endpointId"`
}

type fppPlaylistEntryObservationsResponse struct {
	Observations []fppPlaylistEntryObservation `json:"observations"`
	ServerTime   time.Time                     `json:"serverTime"`
}

type fppPlaylistEntryReconciliationResponse struct {
	InstanceUUID string `json:"instanceUuid"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason"`

	ObservedPlaylistHash     string `json:"observedPlaylistHash"`
	ObservedEntryKey         string `json:"observedEntryKey"`
	ObservedSection          string `json:"observedSection"`
	ObservedPosition         *int   `json:"observedPosition"`
	ObservedSequenceFilename string `json:"observedSequenceFilename"`
	ObservedMediaFilename    string `json:"observedMediaFilename"`
	ObservedAction           string `json:"observedAction"`
	ObservedUnavailable      string `json:"observedUnavailable"`

	PlaylistID          string `json:"playlistId"`
	PlaylistRevision    int64  `json:"playlistRevision"`
	Show                string `json:"show"`
	BindingPlaylistHash string `json:"bindingPlaylistHash"`
	BindingPlaylistName string `json:"bindingPlaylistName"`

	EntryID     string `json:"entryId"`
	CueID       string `json:"cueId"`
	CueRevision int64  `json:"cueRevision"`

	DefinitionAvailable bool `json:"definitionAvailable"`

	ServerTime time.Time `json:"serverTime"`
}

func cmdFPPPlaylistEntryObservations(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFPPPlaylistEntryObservationsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFPPPlaylistEntryObservationsUsage(stdout)
		return exitOK
	case "list":
		return cmdFPPPlaylistEntryObservationsList(rest, stdout, stderr, clock)
	case "reconciliation":
		return cmdFPPPlaylistEntryReconciliation(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp playlist-entry-observations: unknown subcommand %q\n\n", sub)
		printFPPPlaylistEntryObservationsUsage(stderr)
		return exitUsage
	}
}

func printFPPPlaylistEntryObservationsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fpp playlist-entry-observations <subcommand> [flags]

Read FPP playlist-entry observations and their reconciliation against
configured show.playlist bindings (FPP-PLUGIN-COORDINATOR-CONTRACTS.md §1,
TRACK-H-H2-SPEC.md §5). The plugin posts these; this program only ever
reads them. Requires observation:read.

Subcommands:
  list                          the latest accepted observation for every
                                known instance
  reconciliation <instance-id>  what the coordinator currently makes of
                                one instance's latest accepted observation
                                (unbound, stale-import, unknown-entry,
                                evidence-mismatch, cross-show, resolved)

Run "showmeshctl fpp playlist-entry-observations <subcommand> --help" for
flags specific to one subcommand.
`)
}

func cmdFPPPlaylistEntryObservationsList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-entry-observations list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-entry-observations list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nThe latest accepted playlist-entry observation for every known FPP")
		_, _ = fmt.Fprintln(stderr, "instance (GET /api/v1/integrations/fpp/playlist-entry-observations).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-entry-observations list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-entry-observations list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistEntryObservationsResponse
	if err := c.getJSON(ctx, "/api/v1/integrations/fpp/playlist-entry-observations", nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-entry-observations list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-entry-observations list", err)
		}
		return exitOK
	}
	printFPPPlaylistEntryObservationsTable(stdout, resp)
	return exitOK
}

func cmdFPPPlaylistEntryReconciliation(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-entry-observations reconciliation", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-entry-observations reconciliation [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nWhat the coordinator currently makes of one instance's latest accepted")
		_, _ = fmt.Fprintln(stderr, "observation (GET /api/v1/integrations/fpp/playlist-entry-observations/")
		_, _ = fmt.Fprintln(stderr, "{instanceUuid}/reconciliation, TRACK-H-H2-SPEC.md §5). Read-only.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-entry-observations reconciliation", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-entry-observations reconciliation", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistEntryReconciliationResponse
	path := "/api/v1/integrations/fpp/playlist-entry-observations/" + url.PathEscape(instanceID) + "/reconciliation"
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-entry-observations reconciliation", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-entry-observations reconciliation", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Instance:  %s\n", resp.InstanceUUID)
	_, _ = fmt.Fprintf(stdout, "Outcome:   %s\n", resp.Outcome)
	_, _ = fmt.Fprintf(stdout, "Reason:    %s\n", resp.Reason)
	if resp.PlaylistID != "" {
		_, _ = fmt.Fprintf(stdout, "Playlist:  %s (revision %d)\n", resp.PlaylistID, resp.PlaylistRevision)
	}
	if resp.EntryID != "" {
		_, _ = fmt.Fprintf(stdout, "Entry:     %s\n", resp.EntryID)
	}
	if resp.CueID != "" {
		_, _ = fmt.Fprintf(stdout, "Cue:       %s (revision %d)\n", resp.CueID, resp.CueRevision)
	}
	_, _ = fmt.Fprintf(stdout, "Definition available: %t\n", resp.DefinitionAvailable)
	return exitOK
}

func printFPPPlaylistEntryObservationsTable(w io.Writer, resp fppPlaylistEntryObservationsResponse) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	// ENDPOINT is the fpp.endpoints id this instanceUuid
	// resolves to, best-effort, "-" when no single currently configured
	// endpoint owns this uuid (never yet observed there, or a duplicate).
	_, _ = fmt.Fprintln(tw, "INSTANCE\tENDPOINT\tPLAYLIST\tSECTION\tACTION\tUNAVAILABLE\tRECEIVED")
	for _, o := range resp.Observations {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			o.InstanceUUID, stringOrDash(o.EndpointID), o.PlaylistName, o.Section, o.Action, o.Unavailable, o.ReceivedAt)
	}
	_ = tw.Flush()
}
