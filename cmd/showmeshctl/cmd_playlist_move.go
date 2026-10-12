package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// "playlist move-to-newer-fpp" previews, and with --confirm performs, moving
// an FPP-runner playlist to the newest captured copy of its FPP playlist.
// The coordinator computes what is kept, moved and dropped; this command
// only prints that answer and, on --confirm, sends its proposed payload to
// the ordinary playlist write with If-Match on the revision it named.

type playlistMoveSlot struct {
	Section  string `json:"section"`
	Position int    `json:"position"`
}

type playlistMoveDefinition struct {
	Hash       string    `json:"hash"`
	ReceivedAt time.Time `json:"receivedAt"`
	EntryCount int       `json:"entryCount"`
}

type playlistMoveEntry struct {
	EntryID           string            `json:"entryId"`
	Cue               string            `json:"cue"`
	Filename          string            `json:"filename"`
	Outcome           string            `json:"outcome"`
	MatchedBy         string            `json:"matchedBy"`
	From              playlistMoveSlot  `json:"from"`
	To                *playlistMoveSlot `json:"to"`
	DuplicateFilename bool              `json:"duplicateFilename"`
	PreviousSequence  string            `json:"previousSequence"`
	NewSequence       string            `json:"newSequence"`
	NeedsCheck        bool              `json:"needsCheck"`
	Summary           string            `json:"summary"`
}

type playlistMoveNewEntry struct {
	Section           string `json:"section"`
	Position          int    `json:"position"`
	Name              string `json:"name"`
	DuplicateFilename bool   `json:"duplicateFilename"`
	Summary           string `json:"summary"`
}

// playlistMovePreviewResponse mirrors v1.ShowPlaylistMovePreviewResponse.
type playlistMovePreviewResponse struct {
	ServerTime     time.Time               `json:"serverTime"`
	PlaylistID     string                  `json:"playlistId"`
	Revision       int64                   `json:"revision"`
	NewerAvailable bool                    `json:"newerAvailable"`
	CanConfirm     bool                    `json:"canConfirm"`
	Summary        string                  `json:"summary"`
	Current        *playlistMoveDefinition `json:"current"`
	Newest         *playlistMoveDefinition `json:"newest"`
	Entries        []playlistMoveEntry     `json:"entries"`
	NewEntries     []playlistMoveNewEntry  `json:"newEntries"`
	Proposed       *configShowPlaylist     `json:"proposed"`
}

func cmdPlaylistMove(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist move-to-newer-fpp", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "save the move shown by the preview; without it nothing is written")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist move-to-newer-fpp [flags] <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow what happens to this playlist's cues if it follows FPP's newest copy of")
		_, _ = fmt.Fprintln(stderr, "its playlist (GET /api/v1/config/show.playlist/{id}/definition-move-preview).")
		_, _ = fmt.Fprintln(stderr, "Each cue is matched by its saved sequence name, or by the sequence its position")
		_, _ = fmt.Fprintln(stderr, "held when none was saved, or by position when that is not known. Nothing is")
		_, _ = fmt.Fprintln(stderr, "written unless --confirm is given; --confirm then saves the previewed move")
		_, _ = fmt.Fprintln(stderr, "(PUT /api/v1/config/show.playlist/{id}) and is refused if the")
		_, _ = fmt.Fprintln(stderr, "playlist changed after the preview. Preview needs show:macro:run or")
		_, _ = fmt.Fprintln(stderr, "config:write; --confirm needs config:write.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist move-to-newer-fpp", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist move-to-newer-fpp", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	apiPath := "/api/v1/config/show.playlist/" + url.PathEscape(id)
	var preview playlistMovePreviewResponse
	if err := c.getJSON(ctx, apiPath+"/definition-move-preview", nil, &preview); err != nil {
		return reportError(stderr, "playlist move-to-newer-fpp", err)
	}
	printClockSkew(stderr, preview.ServerTime, clock())

	printPreview := func() int {
		if g.output == outputJSON {
			if err := printJSON(stdout, preview); err != nil {
				return reportError(stderr, "playlist move-to-newer-fpp", err)
			}
			return exitOK
		}
		printPlaylistMovePreview(stdout, preview)
		return exitOK
	}

	if !confirm {
		code := printPreview()
		if code == exitOK && g.output != outputJSON && preview.CanConfirm {
			_, _ = fmt.Fprintf(stdout, "\nNothing was changed. To save this, run the same command with --confirm.\n")
		}
		return code
	}

	if !preview.NewerAvailable {
		return printPreview()
	}
	if !preview.CanConfirm || preview.Proposed == nil {
		_ = printPreview()
		return exitAPIError
	}

	var resp showPlaylistConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatchHeaderValue(preview.Revision), preview.Proposed, &resp); err != nil {
		return reportError(stderr, "playlist move-to-newer-fpp", err)
	}
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "playlist move-to-newer-fpp", err)
		}
		return exitOK
	}
	printPlaylistMovePreview(stdout, preview)
	_, _ = fmt.Fprintf(stdout, "\nSaved. Playlist %s now follows FPP's newest playlist.\n", resp.ID)
	return exitOK
}

func printPlaylistMovePreview(w io.Writer, p playlistMovePreviewResponse) {
	_, _ = fmt.Fprintln(w, p.Summary)
	if !p.NewerAvailable {
		return
	}
	if p.Newest != nil {
		_, _ = fmt.Fprintf(w, "ShowMesh received FPP's changed playlist at %s.\n", p.Newest.ReceivedAt.Format(time.RFC3339))
	}
	_, _ = fmt.Fprintln(w, "\nSaved cues:")
	for _, e := range p.Entries {
		name := e.Filename
		if name == "" && e.MatchedBy == "previousSequence" {
			name = e.PreviousSequence
		}
		if name == "" {
			name = "(no sequence name saved)"
		}
		marker := ""
		if e.NeedsCheck {
			marker = "  CHECK"
		}
		_, _ = fmt.Fprintf(w, "  %-8s %s, cue %s%s\n    %s\n", e.Outcome, name, e.Cue, marker, e.Summary)
	}
	if len(p.NewEntries) > 0 {
		_, _ = fmt.Fprintln(w, "\nNew in FPP's playlist, with no cue:")
		for _, e := range p.NewEntries {
			_, _ = fmt.Fprintf(w, "  %s position %d: %s\n    %s\n", e.Section, e.Position, e.Name, e.Summary)
		}
	}
}
