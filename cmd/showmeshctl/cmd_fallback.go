package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"
)

// showmeshctl surface for whether an FPP player is running the show from
// its fallback program (ADR-048 decision 4). Declares its own wire types
// rather than importing the coordinator's v1 package, as every command
// here does.

type fallbackPlayerState struct {
	State                      string `json:"state"`
	Since                      string `json:"since"`
	PlaylistName               string `json:"playlistName,omitempty"`
	PackageID                  string `json:"packageId,omitempty"`
	PackageRevision            string `json:"packageRevision,omitempty"`
	CutoffAt                   string `json:"cutoffAt,omitempty"`
	ReportedAt                 string `json:"reportedAt"`
	PluginReporting            bool   `json:"pluginReporting"`
	Held                       bool   `json:"held"`
	HoldReason                 string `json:"holdReason,omitempty"`
	AcknowledgementWaitSeconds *int64 `json:"acknowledgementWaitSeconds,omitempty"`
	Message                    string `json:"message,omitempty"`
}

type fallbackProgramListEntry struct {
	FPPInstanceUUID string               `json:"fppInstanceUuid"`
	PackageID       string               `json:"packageId"`
	Revision        string               `json:"revision"`
	Show            string               `json:"show"`
	Generation      int64                `json:"generation"`
	ExpiresAt       string               `json:"expiresAt"`
	CompiledAt      string               `json:"compiledAt"`
	PlayerState     *fallbackPlayerState `json:"playerState,omitempty"`
}

type fallbackProgramListResponse struct {
	ServerTime time.Time                  `json:"serverTime"`
	Programs   []fallbackProgramListEntry `json:"programs"`
}

func cmdFallback(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFallbackUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFallbackUsage(stdout)
		return exitOK
	case "list":
		return cmdFallbackList(rest, stdout, stderr, clock)
	case "show":
		return cmdFallbackShow(rest, stdout, stderr, clock)
	case "clear":
		return cmdFallbackClear(rest, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fallback: unknown subcommand %q\n\n", sub)
		printFallbackUsage(stderr)
		return exitUsage
	}
}

func printFallbackUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fallback <subcommand> [flags]

Each FPP player's fallback program, and whether the player's plugin is
running the show from it. While a plugin runs the show, the coordinator
starts no Cues for that player until the playlist ends.

Subcommands:
  list                          every FPP player with a published fallback
                                program, and its plugin's reported state
  show <fpp-instance-uuid>      one player in full
  clear --confirm <fpp-instance-uuid>
                                forget what the coordinator stored about one
                                player's state (write, requires fpp:command)

Run "showmeshctl fallback <subcommand> --help" for flags specific to one
subcommand.
`)
}

func fetchFallbackPrograms(g *globalFlags, clock func() time.Time, stderr io.Writer) (fallbackProgramListResponse, error) {
	c, err := newRequestClient(g)
	if err != nil {
		return fallbackProgramListResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp fallbackProgramListResponse
	if err := c.getJSON(ctx, "/api/v1/fallback-programs", nil, &resp); err != nil {
		return fallbackProgramListResponse{}, err
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	return resp, nil
}

func cmdFallbackList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fallback list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fallback list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEvery FPP player with a published fallback program, and whether its")
		_, _ = fmt.Fprintln(stderr, "plugin is running the show from it (GET /api/v1/fallback-programs).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fallback list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	resp, err := fetchFallbackPrograms(g, clock, stderr)
	if err != nil {
		return reportError(stderr, "fallback list", err)
	}
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fallback list", err)
		}
		return exitOK
	}
	printFallbackTable(stdout, resp)
	return exitOK
}

// fallbackStateWord is the STATE column: the reported state, or why there
// is none.
func fallbackStateWord(s *fallbackPlayerState) string {
	if s == nil {
		return "not reported"
	}
	return s.State
}

func fallbackHeldWord(s *fallbackPlayerState) string {
	switch {
	case s == nil || !s.Held:
		return "no"
	case s.HoldReason != "":
		return "yes (" + s.HoldReason + ")"
	}
	return "yes"
}

func fallbackPluginWord(s *fallbackPlayerState) string {
	switch {
	case s == nil:
		return "-"
	case s.PluginReporting:
		return "reporting"
	}
	return "not reporting"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printFallbackTable(w io.Writer, resp fallbackProgramListResponse) {
	if len(resp.Programs) == 0 {
		_, _ = fmt.Fprintln(w, "No fallback program is published for any FPP player.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "FPP PLAYER\tSHOW\tSTATE\tHELD\tPLAYLIST\tPROGRAM ENDS\tPLUGIN")
	for _, p := range resp.Programs {
		playlist, ends := "", p.ExpiresAt
		if p.PlayerState != nil {
			playlist = p.PlayerState.PlaylistName
			if p.PlayerState.CutoffAt != "" {
				ends = p.PlayerState.CutoffAt
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.FPPInstanceUUID, p.Show,
			fallbackStateWord(p.PlayerState), fallbackHeldWord(p.PlayerState), dashIfEmpty(playlist), ends, fallbackPluginWord(p.PlayerState))
	}
	_ = tw.Flush()
	for _, p := range resp.Programs {
		if p.PlayerState != nil && p.PlayerState.Message != "" {
			_, _ = fmt.Fprintf(w, "\n%s: %s\n", p.FPPInstanceUUID, p.PlayerState.Message)
		}
	}
}

func cmdFallbackShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fallback show", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fallback show [flags] <fpp-instance-uuid>")
		_, _ = fmt.Fprintln(stderr, "\nOne FPP player's fallback program and its plugin's reported state, read")
		_, _ = fmt.Fprintln(stderr, "from GET /api/v1/fallback-programs.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fallback show", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	resp, err := fetchFallbackPrograms(g, clock, stderr)
	if err != nil {
		return reportError(stderr, "fallback show", err)
	}
	for _, p := range resp.Programs {
		if p.FPPInstanceUUID != rest[0] {
			continue
		}
		if g.output == outputJSON {
			if err := printJSON(stdout, p); err != nil {
				return reportError(stderr, "fallback show", err)
			}
			return exitOK
		}
		printFallbackDetail(stdout, p)
		return exitOK
	}
	_, _ = fmt.Fprintf(stderr, "showmeshctl fallback show: no fallback program is published for FPP player %s\n", rest[0])
	return exitNotFound
}

func printFallbackDetail(w io.Writer, p fallbackProgramListEntry) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FPP player:\t%s\n", p.FPPInstanceUUID)
	_, _ = fmt.Fprintf(tw, "Show:\t%s\n", p.Show)
	_, _ = fmt.Fprintf(tw, "Published program:\t%s (revision %s)\n", p.PackageID, p.Revision)
	_, _ = fmt.Fprintf(tw, "Published program ends:\t%s\n", p.ExpiresAt)
	s := p.PlayerState
	if s == nil {
		_, _ = fmt.Fprintf(tw, "Plugin state:\tnot reported\n")
		_ = tw.Flush()
		_, _ = fmt.Fprintln(w, "\nThis player's plugin has never reported a fallback state, so the coordinator never holds this player.")
		return
	}
	_, _ = fmt.Fprintf(tw, "Plugin state:\t%s since %s\n", s.State, s.Since)
	_, _ = fmt.Fprintf(tw, "Last report:\t%s (%s)\n", s.ReportedAt, fallbackPluginWord(s))
	if s.PlaylistName != "" {
		_, _ = fmt.Fprintf(tw, "Playlist:\t%s\n", s.PlaylistName)
	}
	if s.PackageID != "" {
		_, _ = fmt.Fprintf(tw, "Program in use:\t%s (revision %s)\n", s.PackageID, s.PackageRevision)
	}
	if s.CutoffAt != "" {
		_, _ = fmt.Fprintf(tw, "Program in use ends:\t%s\n", s.CutoffAt)
	}
	_, _ = fmt.Fprintf(tw, "Held by coordinator:\t%s\n", fallbackHeldWord(s))
	if s.AcknowledgementWaitSeconds != nil {
		_, _ = fmt.Fprintf(tw, "Waiting for confirmation:\t%s seconds\n", strconv.FormatInt(*s.AcknowledgementWaitSeconds, 10))
	}
	_ = tw.Flush()
	if s.Message != "" {
		_, _ = fmt.Fprintf(w, "\n%s\n", s.Message)
	}
}

// cmdFallbackClear implements "showmeshctl fallback clear --confirm
// <fpp-instance-uuid>": DELETE /api/v1/fallback-programs/{id}/fallback-state.
// --confirm is this program's own guard, as on "fpp reset-observation-sequence".
func cmdFallbackClear(args []string, stdout, stderr io.Writer) int {
	fs, g := newFlagSet("showmeshctl fallback clear", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms forgetting this player's stored fallback state")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fallback clear --confirm <fpp-instance-uuid>")
		_, _ = fmt.Fprintln(stderr, "\nForget what the coordinator stored about one FPP player's fallback state")
		_, _ = fmt.Fprintln(stderr, "(DELETE /api/v1/fallback-programs/{fppInstanceId}/fallback-state). Requires")
		_, _ = fmt.Fprintln(stderr, "fpp:command. Use it when the coordinator keeps holding a player whose plugin")
		_, _ = fmt.Fprintln(stderr, "will not release it. A plugin that is still running the show reports again")
		_, _ = fmt.Fprintln(stderr, "within 10 seconds and is held again, so this does not take a show from it.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fallback clear", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceUUID := rest[0]
	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fallback clear: refusing to clear the stored fallback state for "+instanceUUID+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fallback clear", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	path := "/api/v1/fallback-programs/" + url.PathEscape(instanceUUID) + "/fallback-state"
	if err := c.deleteJSON(ctx, path, nil, nil); err != nil {
		return reportError(stderr, "fallback clear", err)
	}
	_, _ = fmt.Fprintf(stdout, "fallback state cleared for %s\n", instanceUUID)
	return exitOK
}
