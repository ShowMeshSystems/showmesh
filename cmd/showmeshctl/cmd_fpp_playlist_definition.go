package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"text/tabwriter"
	"time"
)

// showmeshctl surface for the read half of
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md §3's playlist definition
// publication (TRACK-H-H2-SPEC.md §4 step 2, §7): "the definition POST
// is the plugin's route, not an operator capability ... the READ half ...
// is an ordinary read this CLI should grow a verb for when Track H gives
// an operator a reason to look at it. This seam is that reason." Mirrors
// cmd_playlist.go's own reasoning and shape: declares its own wire types
// rather than importing the coordinator's v1 package.

type fppPlaylistDefinitionMetadata struct {
	InstanceUUID string    `json:"instanceUuid"`
	PlaylistName string    `json:"playlistName"`
	PlaylistHash string    `json:"playlistHash"`
	CapturedAt   time.Time `json:"capturedAt"`
	ReceivedAt   time.Time `json:"receivedAt"`
	EntryCount   int       `json:"entryCount"`
	Referenced   bool      `json:"referenced"`
}

type fppPlaylistDefinitionsListResponse struct {
	Definitions []fppPlaylistDefinitionMetadata `json:"definitions"`
	ServerTime  time.Time                       `json:"serverTime"`
}

type fppPlaylistDefinitionResponse struct {
	InstanceUUID string          `json:"instanceUuid"`
	PlaylistName string          `json:"playlistName"`
	PlaylistHash string          `json:"playlistHash"`
	Definition   json.RawMessage `json:"definition"`
	CapturedAt   time.Time       `json:"capturedAt"`
	ReceivedAt   time.Time       `json:"receivedAt"`
	ServerTime   time.Time       `json:"serverTime"`
}

type fppPlaylistDefinitionEntry struct {
	Section      string `json:"section"`
	Position     int    `json:"position"`
	Type         string `json:"type"`
	SequenceName string `json:"sequenceName"`
	MediaName    string `json:"mediaName"`
}

type fppPlaylistDefinitionEntriesResponse struct {
	InstanceUUID string                       `json:"instanceUuid"`
	PlaylistHash string                       `json:"playlistHash"`
	Entries      []fppPlaylistDefinitionEntry `json:"entries"`
	ServerTime   time.Time                    `json:"serverTime"`
}

func cmdFPPPlaylistDefinitions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFPPPlaylistDefinitionsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFPPPlaylistDefinitionsUsage(stdout)
		return exitOK
	case "list":
		return cmdFPPPlaylistDefinitionsList(rest, stdout, stderr, clock)
	case "get":
		return cmdFPPPlaylistDefinitionGet(rest, stdout, stderr, clock)
	case "entries":
		return cmdFPPPlaylistDefinitionEntries(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp playlist-definitions: unknown subcommand %q\n\n", sub)
		printFPPPlaylistDefinitionsUsage(stderr)
		return exitUsage
	}
}

func printFPPPlaylistDefinitionsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fpp playlist-definitions <subcommand> [flags]

Read stored FPP playlist definitions (FPP-PLUGIN-COORDINATOR-CONTRACTS.md
§3): the plugin posts these, this program only ever reads them. Requires
observation:read.

Subcommands:
  list                              metadata for every stored definition,
                                    newest received first
  get <instance-id> <playlist-hash>  the stored definition itself
  entries <instance-id> <playlist-hash>
                                    parsed leadIn/mainPlaylist/leadOut
                                    entries (TRACK-H-H2-SPEC.md §4 step 2)

Run "showmeshctl fpp playlist-definitions <subcommand> --help" for flags
specific to one subcommand.
`)
}

func cmdFPPPlaylistDefinitionsList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-definitions list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-definitions list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nMetadata for every stored playlist definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/integrations/fpp/playlist-definitions), newest received first.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-definitions list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-definitions list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistDefinitionsListResponse
	if err := c.getJSON(ctx, "/api/v1/integrations/fpp/playlist-definitions", nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-definitions list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-definitions list", err)
		}
		return exitOK
	}
	printFPPPlaylistDefinitionsTable(stdout, resp)
	return exitOK
}

func cmdFPPPlaylistDefinitionGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-definitions get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-definitions get [flags] <instance-id> <playlist-hash>")
		_, _ = fmt.Fprintln(stderr, "\nShow one stored playlist definition (GET /api/v1/integrations/fpp/")
		_, _ = fmt.Fprintln(stderr, "playlist-definitions/{instanceUuid}/{playlistHash}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-definitions get", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, playlistHash := rest[0], rest[1]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-definitions get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistDefinitionResponse
	path := "/api/v1/integrations/fpp/playlist-definitions/" + url.PathEscape(instanceID) + "/" + url.PathEscape(playlistHash)
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-definitions get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-definitions get", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Instance:     %s\n", resp.InstanceUUID)
	_, _ = fmt.Fprintf(stdout, "Playlist:     %s\n", resp.PlaylistName)
	_, _ = fmt.Fprintf(stdout, "Hash:         %s\n", resp.PlaylistHash)
	_, _ = fmt.Fprintf(stdout, "Captured:     %s\n", resp.CapturedAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(stdout, "Received:     %s\n", resp.ReceivedAt.Format(time.RFC3339))
	_, _ = fmt.Fprintf(stdout, "Definition:   %s\n", string(resp.Definition))
	return exitOK
}

func cmdFPPPlaylistDefinitionEntries(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-definitions entries", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-definitions entries [flags] <instance-id> <playlist-hash>")
		_, _ = fmt.Fprintln(stderr, "\nParsed leadIn/mainPlaylist/leadOut entries for one stored playlist")
		_, _ = fmt.Fprintln(stderr, "definition (GET /api/v1/integrations/fpp/playlist-definitions/")
		_, _ = fmt.Fprintln(stderr, "{instanceUuid}/{playlistHash}/entries). Import evidence only; selects nothing.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-definitions entries", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, playlistHash := rest[0], rest[1]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-definitions entries", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistDefinitionEntriesResponse
	path := "/api/v1/integrations/fpp/playlist-definitions/" + url.PathEscape(instanceID) + "/" + url.PathEscape(playlistHash) + "/entries"
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-definitions entries", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-definitions entries", err)
		}
		return exitOK
	}
	printFPPPlaylistDefinitionEntriesTable(stdout, resp)
	return exitOK
}

func printFPPPlaylistDefinitionsTable(w io.Writer, resp fppPlaylistDefinitionsListResponse) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "INSTANCE\tPLAYLIST\tHASH\tENTRIES\tREFERENCED\tRECEIVED")
	for _, d := range resp.Definitions {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%t\t%s\n",
			d.InstanceUUID, d.PlaylistName, d.PlaylistHash, d.EntryCount, d.Referenced, d.ReceivedAt.Format(time.RFC3339))
	}
	_ = tw.Flush()
}

func printFPPPlaylistDefinitionEntriesTable(w io.Writer, resp fppPlaylistDefinitionEntriesResponse) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SECTION\tPOSITION\tTYPE\tSEQUENCE\tMEDIA")
	for _, e := range resp.Entries {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", e.Section, e.Position, e.Type, e.SequenceName, e.MediaName)
	}
	_ = tw.Flush()
}
