package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// showmeshctl surface for TRACK-H-H2-SPEC.md §6/§7's playlist readiness
// read route: "readiness nobody can see is not readiness." Declares its
// own wire type rather than importing the coordinator's v1 package,
// mirroring cmd_fpp_playlist_definition.go's identical shape.

type fppPlaylistReadinessResponse struct {
	PlaylistID       string    `json:"playlistId"`
	Ready            bool      `json:"ready"`
	FailingCondition string    `json:"failingCondition"`
	Reason           string    `json:"reason"`
	Warning          string    `json:"warning"`
	ServerTime       time.Time `json:"serverTime"`
}

func cmdFPPPlaylistReadiness(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp playlist-readiness", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp playlist-readiness [flags] <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nWhether one FPP-backed Playlist is ready (GET /api/v1/integrations/fpp/")
		_, _ = fmt.Fprintln(stderr, "playlists/{playlistId}/readiness, TRACK-H-H2-SPEC.md §6). Read-only.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp playlist-readiness", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	playlistID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp playlist-readiness", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPlaylistReadinessResponse
	path := "/api/v1/integrations/fpp/playlists/" + url.PathEscape(playlistID) + "/readiness"
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
		return reportError(stderr, "fpp playlist-readiness", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp playlist-readiness", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Playlist: %s\n", resp.PlaylistID)
	_, _ = fmt.Fprintf(stdout, "Ready:    %t\n", resp.Ready)
	if resp.FailingCondition != "" {
		_, _ = fmt.Fprintf(stdout, "Failing:  %s\n", resp.FailingCondition)
	}
	if resp.Reason != "" {
		_, _ = fmt.Fprintf(stdout, "Reason:   %s\n", resp.Reason)
	}
	if resp.Warning != "" {
		_, _ = fmt.Fprintf(stdout, "Warning:  %s\n", resp.Warning)
	}
	return exitOK
}
