package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// showmeshctl surface for the "media.playlist" configuration kind
// (internal/coordinator/config/mediaplaylist.go): list (optional --show
// filter), get, full-replacement set, revisions, delete. Declares its own
// wire types and follows cmd_playlist.go's exact shape one kind over.
//
// "media-playlist set" is a full replacement. --items-json carries the
// whole "items" array as raw JSON, mirroring cmd_playlist.go's
// --entries-json: an item's own shape is a fixed (kind, show, sequence,
// target) tuple, but treating it as one JSON value avoids a bespoke
// presence-tracking scheme for an array of objects. crossfadeMs, fadeOutMs,
// and fadeInMs are optional integers with a real "was this given" question
// (0 is a valid value none of them can default to unnoticed), so each gets
// its own fs.Func presence flag, matching registerIfMatchFlags' own
// pattern (revision.go) rather than a zero-value sentinel.

// configMediaPlaylist mirrors v1.ConfigMediaPlaylist.
type configMediaPlaylist struct {
	Label          string          `json:"label"`
	Show           string          `json:"show"`
	Items          json.RawMessage `json:"items"`
	Repeat         string          `json:"repeat,omitempty"`
	Resume         string          `json:"resume"`
	ItemTransition string          `json:"itemTransition"`
	CrossfadeMs    *int            `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64         `json:"maxGainDb"`
	FadeOutMs      *int            `json:"fadeOutMs,omitempty"`
	FadeInMs       *int            `json:"fadeInMs,omitempty"`
}

// mediaPlaylistConfigResponse is the body of GET and PUT
// /config/media.playlist/{id}.
type mediaPlaylistConfigResponse struct {
	ServerTime             time.Time           `json:"serverTime"`
	Kind                   string              `json:"kind"`
	ID                     string              `json:"id"`
	Revision               int64               `json:"revision"`
	Payload                configMediaPlaylist `json:"payload"`
	UpdatedAt              time.Time           `json:"updatedAt"`
	CreatedByPrincipalID   *string             `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string             `json:"createdByPrincipalName"`
	Source                 string              `json:"source"`
}

func cmdMediaPlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printMediaPlaylistUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printMediaPlaylistUsage(stdout)
		return exitOK
	case "list":
		return cmdMediaPlaylistList(rest, stdout, stderr, clock)
	case "get":
		return cmdMediaPlaylistGet(rest, stdout, stderr, clock)
	case "set":
		return cmdMediaPlaylistSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdMediaPlaylistRevisions(rest, stdout, stderr, clock)
	case "delete":
		return cmdMediaPlaylistDelete(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl media-playlist: unknown subcommand %q\n\n", sub)
		printMediaPlaylistUsage(stderr)
		return exitUsage
	}
}

func printMediaPlaylistUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl media-playlist <subcommand> [flags]

Read or write the coordinator's "media.playlist" configuration objects: an
operator-authored bed the audio engine plays (unlike show.playlist, a list
of cues a runner steps through, media.playlist is a list of things the
audio engine plays as a bed, and several may exist per show). Reads
require show:macro:run OR config:write; writes require config:write.

Subcommands:
  list [--show <id>]     enumerate media playlist objects, optionally
                         narrowed to one show
  get <id>               show one media playlist's full definition
  set <id>               write a new media playlist revision (write, full
                         replacement)
  revisions <id>         list revision history, newest first
  delete --confirm <id>  tombstone this media playlist (write); revision
                         history stays readable via "revisions"

Run "showmeshctl media-playlist <subcommand> --help" for flags specific to
one subcommand.
`)
}

func cmdMediaPlaylistList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl media-playlist list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to media playlists belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl media-playlist list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate media.playlist objects (GET /api/v1/config/media.playlist).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "media-playlist list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "media-playlist list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/media.playlist", query, &resp); err != nil {
		return reportError(stderr, "media-playlist list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "media-playlist list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdMediaPlaylistGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl media-playlist get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl media-playlist get [flags] <media-playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one media playlist's full definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/media.playlist/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "media-playlist get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "media-playlist get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp mediaPlaylistConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/media.playlist/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "media-playlist get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "media-playlist get", err)
		}
		return exitOK
	}
	printMediaPlaylistDetail(stdout, resp)
	return exitOK
}

func cmdMediaPlaylistSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl media-playlist set", stderr)
	var (
		show, label, repeat, resume, itemTransition, itemsJSON string
		crossfadeMs, fadeOutMs, fadeInMs                       int
		crossfadeSet, fadeOutSet, fadeInSet, maxGainSet        bool
		maxGainDb                                              float64
	)
	fs.StringVar(&show, "show", "", "the show this media playlist belongs to (required)")
	fs.StringVar(&label, "label", "", "the media playlist's operator-facing label (required)")
	fs.StringVar(&itemsJSON, "items-json", "", "the media playlist's \"items\" array, as raw JSON (required)")
	fs.StringVar(&repeat, "repeat", "", "repeat: none|item|playlist (default none)")
	fs.StringVar(&resume, "resume", "", "resume: resume|restart (required)")
	fs.StringVar(&itemTransition, "item-transition", "", "itemTransition: sequential|gapless|crossfade (required)")
	fs.Func("crossfade-ms", "crossfadeMs, milliseconds (required iff --item-transition=crossfade)", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			return fmt.Errorf("must be a non-negative integer, got %q", s)
		}
		crossfadeMs, crossfadeSet = v, true
		return nil
	})
	fs.Func("max-gain-db", "maxGainDb, the bed's own ceiling in dB; must be <= 0 (required)", func(s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("must be a number, got %q", s)
		}
		maxGainDb, maxGainSet = v, true
		return nil
	})
	fs.Func("fade-out-ms", "fadeOutMs, milliseconds (must be configured together with --fade-in-ms, or both omitted)", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			return fmt.Errorf("must be a positive integer, got %q", s)
		}
		fadeOutMs, fadeOutSet = v, true
		return nil
	})
	fs.Func("fade-in-ms", "fadeInMs, milliseconds (must be configured together with --fade-out-ms, or both omitted)", func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			return fmt.Errorf("must be a positive integer, got %q", s)
		}
		fadeInMs, fadeInSet = v, true
		return nil
	})
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl media-playlist set [flags] <media-playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new media.playlist revision (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/media.playlist/{id}). Requires config:write.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: this command never reads the media")
		_, _ = fmt.Fprintln(stderr, "playlist's current definition first. --items-json is the whole items")
		_, _ = fmt.Fprintln(stderr, "array, e.g.:")
		_, _ = fmt.Fprintln(stderr, `  '[{"kind":"asset","show":"halloween-2026","sequence":"seq1","target":"node1"}]'`)
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read of this media playlist),")
		_, _ = fmt.Fprintln(stderr, "refusing with a 409 if it changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "media-playlist set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	var missing []string
	if show == "" {
		missing = append(missing, "--show")
	}
	if label == "" {
		missing = append(missing, "--label")
	}
	if itemsJSON == "" {
		missing = append(missing, "--items-json")
	}
	if resume == "" {
		missing = append(missing, "--resume")
	}
	if itemTransition == "" {
		missing = append(missing, "--item-transition")
	}
	if !maxGainSet {
		missing = append(missing, "--max-gain-db")
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl media-playlist set: missing required flag(s): %v\n", missing)
		return exitUsage
	}
	if !json.Valid([]byte(itemsJSON)) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl media-playlist set: --items-json is not valid JSON")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "media-playlist set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configMediaPlaylist{
		Label: label, Show: show, Items: json.RawMessage(itemsJSON),
		Repeat: repeat, Resume: resume, ItemTransition: itemTransition, MaxGainDb: maxGainDb,
	}
	if crossfadeSet {
		body.CrossfadeMs = &crossfadeMs
	}
	if fadeOutSet {
		body.FadeOutMs = &fadeOutMs
	}
	if fadeInSet {
		body.FadeInMs = &fadeInMs
	}

	apiPath := "/api/v1/config/media.playlist/" + url.PathEscape(id)
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r mediaPlaylistConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "media-playlist set", err)
	}

	var resp mediaPlaylistConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "media-playlist set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "media-playlist set", err)
		}
		return exitOK
	}
	printMediaPlaylistDetail(stdout, resp)
	return exitOK
}

func cmdMediaPlaylistRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl media-playlist revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl media-playlist revisions [flags] <media-playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nList media.playlist revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/media.playlist/{id}/revisions). Metadata only, no payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "media-playlist revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "media-playlist revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/media.playlist/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "media-playlist revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "media-playlist revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// cmdMediaPlaylistDelete mirrors cmdPlaylistDelete's own shape
// (cmd_playlist.go): --confirm is required and checked locally before any
// request is sent. A tombstone, not a hard delete: revision history stays
// readable through "media-playlist revisions" afterward.
func cmdMediaPlaylistDelete(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl media-playlist delete", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms deletion of this media playlist")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl media-playlist delete --confirm <media-playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nDelete a media.playlist object (DELETE /api/v1/config/media.playlist/{id}).")
		_, _ = fmt.Fprintln(stderr, "A tombstone, not a hard delete: the object's revision history still")
		_, _ = fmt.Fprintln(stderr, "reads through \"media-playlist revisions\" afterward. Requires config:write")
		_, _ = fmt.Fprintln(stderr, "and --confirm.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "media-playlist delete", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl media-playlist delete: refusing to delete "+id+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "media-playlist delete", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configObjectDeleteRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/config/media.playlist/"+url.PathEscape(id), body, nil); err != nil {
		return reportError(stderr, "media-playlist delete", err)
	}

	_, _ = fmt.Fprintf(stdout, "media playlist %s deleted\n", id)
	return exitOK
}

func printMediaPlaylistDetail(w io.Writer, resp mediaPlaylistConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Media playlist ID: %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:               %s\n", p.Show)
	_, _ = fmt.Fprintf(w, "Label:              %s\n", p.Label)
	_, _ = fmt.Fprintf(w, "Items:              %s\n", string(p.Items))
	_, _ = fmt.Fprintf(w, "Repeat:             %s\n", p.Repeat)
	_, _ = fmt.Fprintf(w, "Resume:             %s\n", p.Resume)
	_, _ = fmt.Fprintf(w, "Item transition:    %s\n", p.ItemTransition)
	if p.CrossfadeMs != nil {
		_, _ = fmt.Fprintf(w, "Crossfade ms:       %d\n", *p.CrossfadeMs)
	}
	_, _ = fmt.Fprintf(w, "Max gain dB:        %g\n", p.MaxGainDb)
	if p.FadeOutMs != nil {
		_, _ = fmt.Fprintf(w, "Fade out ms:        %d\n", *p.FadeOutMs)
	}
	if p.FadeInMs != nil {
		_, _ = fmt.Fprintf(w, "Fade in ms:         %d\n", *p.FadeInMs)
	}
	_, _ = fmt.Fprintf(w, "Revision:           %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:            %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:         %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:         (no principal recorded)\n")
	}
}
