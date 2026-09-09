package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// showmeshctl surface for the "show.playlist" configuration kind
// (TRACK-H-H1-SPEC.md section 3): list (optional --show filter), get,
// full-replacement set, revisions. Declares its own wire types, matching
// cmd_cue.go's reasoning.
//
// "playlist set" is a full replacement. --fpp-json/--showmesh-audio-json
// carry their nested objects as raw JSON (mirroring cmd_cue.go's
// --outputs-json), and --entries-json carries the whole "entries" array,
// since an entry's own shape already varies by runner the same way
// outputs' members do.

// configShowPlaylist mirrors v1.ConfigShowPlaylist.
type configShowPlaylist struct {
	Show           string          `json:"show"`
	Name           string          `json:"name"`
	Runner         string          `json:"runner"`
	MismatchPolicy string          `json:"mismatchPolicy,omitempty"`
	SafeCueRef     string          `json:"safeCueRef,omitempty"`
	FPP            json.RawMessage `json:"fpp,omitempty"`
	ShowmeshAudio  json.RawMessage `json:"showmeshAudio,omitempty"`
	Entries        json.RawMessage `json:"entries"`
}

// showPlaylistConfigResponse is the body of GET and PUT
// /config/show.playlist/{id}.
type showPlaylistConfigResponse struct {
	ServerTime             time.Time          `json:"serverTime"`
	Kind                   string             `json:"kind"`
	ID                     string             `json:"id"`
	Revision               int64              `json:"revision"`
	Payload                configShowPlaylist `json:"payload"`
	UpdatedAt              time.Time          `json:"updatedAt"`
	CreatedByPrincipalID   *string            `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string            `json:"createdByPrincipalName"`
	Source                 string             `json:"source"`
}

func cmdPlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPlaylistUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printPlaylistUsage(stdout)
		return exitOK
	case "list":
		return cmdPlaylistList(rest, stdout, stderr, clock)
	case "get":
		return cmdPlaylistGet(rest, stdout, stderr, clock)
	case "set":
		return cmdPlaylistSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdPlaylistRevisions(rest, stdout, stderr, clock)
	case "delete":
		return cmdPlaylistDelete(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl playlist: unknown subcommand %q\n\n", sub)
		printPlaylistUsage(stderr)
		return exitUsage
	}
}

func printPlaylistUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl playlist <subcommand> [flags]

Read or write the coordinator's "show.playlist" configuration objects
(Track H, ADR-043: a Playlist is the show-scoped ordered program of Cues,
bound to a runner). Reads require show:macro:run OR config:write; writes
require config:write.

Subcommands:
  list [--show <id>]     enumerate playlist objects, optionally narrowed to
                         one show
  get <id>               show one playlist's full definition
  set <id>               write a new playlist revision (write, full
                         replacement)
  revisions <id>         list revision history, newest first
  delete --confirm <id>  tombstone this playlist (write); revision history
                         stays readable via "revisions"

Run "showmeshctl playlist <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdPlaylistList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to playlists belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.playlist objects (GET /api/v1/config/show.playlist).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.playlist", query, &resp); err != nil {
		return reportError(stderr, "playlist list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "playlist list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdPlaylistGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist get [flags] <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one playlist's full definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.playlist/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showPlaylistConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.playlist/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "playlist get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "playlist get", err)
		}
		return exitOK
	}
	printPlaylistDetail(stdout, resp)
	return exitOK
}

func cmdPlaylistSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist set", stderr)
	var (
		show, name, runner, mismatchPolicy, safeCueRef string
		fppJSON, showmeshAudioJSON, entriesJSON        string
	)
	fs.StringVar(&show, "show", "", "the show this playlist belongs to (required)")
	fs.StringVar(&name, "name", "", "the playlist's operator-facing name (required)")
	fs.StringVar(&runner, "runner", "", "runner: fpp|showmesh-audio (required)")
	fs.StringVar(&mismatchPolicy, "mismatch-policy", "", "mismatchPolicy: hold|blackAndSilence|safeCue (fpp runner only; default hold)")
	fs.StringVar(&safeCueRef, "safe-cue-ref", "", "safeCueRef, a same-show cue id (required when --mismatch-policy=safeCue)")
	fs.StringVar(&fppJSON, "fpp-json", "", "the playlist's \"fpp\" binding object, as raw JSON (required when --runner=fpp)")
	fs.StringVar(&showmeshAudioJSON, "showmesh-audio-json", "", "the playlist's \"showmeshAudio\" object, as raw JSON (showmesh-audio runner only)")
	fs.StringVar(&entriesJSON, "entries-json", "", "the playlist's \"entries\" array, as raw JSON (required)")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist set [flags] <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.playlist revision (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.playlist/{id}). Requires config:write.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: this command never reads the playlist's")
		_, _ = fmt.Fprintln(stderr, "current definition first. --entries-json is the whole entries array, e.g.:")
		_, _ = fmt.Fprintln(stderr, `  '[{"id":"e1","cue":"thriller","fpp":{"section":"mainPlaylist","position":0}}]'`)
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read of this playlist), refusing with")
		_, _ = fmt.Fprintln(stderr, "a 409 if it changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist set", err)
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
	if name == "" {
		missing = append(missing, "--name")
	}
	if runner == "" {
		missing = append(missing, "--runner")
	}
	if entriesJSON == "" {
		missing = append(missing, "--entries-json")
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl playlist set: missing required flag(s): %v\n", missing)
		return exitUsage
	}
	if !json.Valid([]byte(entriesJSON)) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl playlist set: --entries-json is not valid JSON")
		return exitUsage
	}
	if fppJSON != "" && !json.Valid([]byte(fppJSON)) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl playlist set: --fpp-json is not valid JSON")
		return exitUsage
	}
	if showmeshAudioJSON != "" && !json.Valid([]byte(showmeshAudioJSON)) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl playlist set: --showmesh-audio-json is not valid JSON")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configShowPlaylist{
		Show: show, Name: name, Runner: runner,
		MismatchPolicy: mismatchPolicy, SafeCueRef: safeCueRef,
		Entries: json.RawMessage(entriesJSON),
	}
	if fppJSON != "" {
		body.FPP = json.RawMessage(fppJSON)
	}
	if showmeshAudioJSON != "" {
		body.ShowmeshAudio = json.RawMessage(showmeshAudioJSON)
	}

	apiPath := "/api/v1/config/show.playlist/" + url.PathEscape(id)
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r showPlaylistConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "playlist set", err)
	}

	var resp showPlaylistConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "playlist set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "playlist set", err)
		}
		return exitOK
	}
	printPlaylistDetail(stdout, resp)
	return exitOK
}

func cmdPlaylistRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist revisions [flags] <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nList show.playlist revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.playlist/{id}/revisions). Metadata only, no payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.playlist/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "playlist revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "playlist revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// cmdPlaylistDelete mirrors cmdCueDelete's own shape (cmd_cue.go):
// --confirm is required and checked locally before any request is sent. A
// tombstone, not a hard delete: revision history stays readable through
// "playlist revisions" afterward.
func cmdPlaylistDelete(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl playlist delete", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms deletion of this playlist")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl playlist delete --confirm <playlist-id>")
		_, _ = fmt.Fprintln(stderr, "\nDelete a show.playlist object (DELETE /api/v1/config/show.playlist/{id}).")
		_, _ = fmt.Fprintln(stderr, "A tombstone, not a hard delete: the object's revision history still")
		_, _ = fmt.Fprintln(stderr, "reads through \"playlist revisions\" afterward. Requires config:write and")
		_, _ = fmt.Fprintln(stderr, "--confirm.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "playlist delete", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl playlist delete: refusing to delete "+id+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "playlist delete", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configObjectDeleteRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/config/show.playlist/"+url.PathEscape(id), body, nil); err != nil {
		return reportError(stderr, "playlist delete", err)
	}

	_, _ = fmt.Fprintf(stdout, "playlist %s deleted\n", id)
	return exitOK
}

func printPlaylistDetail(w io.Writer, resp showPlaylistConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Playlist ID:  %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:         %s\n", p.Show)
	_, _ = fmt.Fprintf(w, "Name:         %s\n", p.Name)
	_, _ = fmt.Fprintf(w, "Runner:       %s\n", p.Runner)
	if p.MismatchPolicy != "" {
		_, _ = fmt.Fprintf(w, "Mismatch:     %s\n", p.MismatchPolicy)
	}
	if p.SafeCueRef != "" {
		_, _ = fmt.Fprintf(w, "Safe cue:     %s\n", p.SafeCueRef)
	}
	if len(p.FPP) > 0 {
		_, _ = fmt.Fprintf(w, "FPP binding:  %s\n", string(p.FPP))
	}
	if len(p.ShowmeshAudio) > 0 {
		_, _ = fmt.Fprintf(w, "Showmesh audio: %s\n", string(p.ShowmeshAudio))
	}
	_, _ = fmt.Fprintf(w, "Entries:      %s\n", string(p.Entries))
	_, _ = fmt.Fprintf(w, "Revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:      %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:   %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:   (no principal recorded)\n")
	}
}
