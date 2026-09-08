package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// showmeshctl surface for the "show" configuration kind and the
// show.active singleton: list, get, full-replacement set, revisions, plus
// "show active"/"show activate". Declares its own wire types rather than
// importing internal/coordinator/api/v1 (the import-graph test forbids
// it), matching types_macro.go's precedent one kind over.
//
// "show set" always sends both --name and --notes, unlike "declare"
// (cmd_discovery.go)'s partial-update shape: DecodeShowPayload has no
// per-field carry-forward, so omitting an unset --notes would silently
// erase it.

// configShow mirrors v1.ConfigShow: the "show" configuration kind's
// decoded payload.
type configShow struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
}

// showConfigResponse is the body of GET and PUT /config/show/{id}. See
// [showActionConfigResponse]'s doc comment (types_macro.go) for why the
// creator fields stay nullable.
type showConfigResponse struct {
	ServerTime             time.Time  `json:"serverTime"`
	Kind                   string     `json:"kind"`
	ID                     string     `json:"id"`
	Revision               int64      `json:"revision"`
	Payload                configShow `json:"payload"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	CreatedByPrincipalID   *string    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string    `json:"createdByPrincipalName"`
	Source                 string     `json:"source"`
}

// configShowActive mirrors v1.ConfigShowActive: the show.active
// singleton's decoded payload.
type configShowActive struct {
	Show string `json:"show"`
}

// showActiveConfigResponse is the body of GET and PUT /config/show.active.
type showActiveConfigResponse struct {
	ServerTime             time.Time        `json:"serverTime"`
	Kind                   string           `json:"kind"`
	ID                     string           `json:"id"`
	Revision               int64            `json:"revision"`
	Payload                configShowActive `json:"payload"`
	UpdatedAt              time.Time        `json:"updatedAt"`
	CreatedByPrincipalID   *string          `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string          `json:"createdByPrincipalName"`
	Source                 string           `json:"source"`
}

func cmdShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printShowUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printShowUsage(stdout)
		return exitOK
	case "list":
		return cmdShowList(rest, stdout, stderr, clock)
	case "get":
		return cmdShowGet(rest, stdout, stderr, clock)
	case "set":
		return cmdShowSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdShowRevisions(rest, stdout, stderr, clock)
	case "active":
		return cmdShowActive(rest, stdout, stderr, clock)
	case "activate":
		return cmdShowActivate(rest, stdout, stderr, clock)
	case "mode":
		return cmdShowMode(rest, stdout, stderr, clock)
	case "delete":
		return cmdShowDelete(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl show: unknown subcommand %q\n\n", sub)
		printShowUsage(stderr)
		return exitUsage
	}
}

func printShowUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl show <subcommand> [flags]

Read or write the coordinator's "show" configuration objects (Track E,
ADR-027: a Show is a namespace, not a container) and the show.active
singleton pointer. Reads require show:macro:run OR config:write, matching
"action"/"macro"; writes require config:write.

Subcommands:
  list             enumerate show objects (id, name, revision)
  get <id>         show one show's full definition
  set <id>         write a new show revision (write, full replacement)
  revisions <id>   list revision history, newest first
  active           print the currently active show (404 if none has ever
                   been activated)
  activate <id>    make <id> the active show (write, full replacement of
                   the show.active singleton; audited like any other
                   configuration write)
  mode             print the installation-wide operating mode (ADR-033);
                   "mode set <program|show>" writes it and "mode
                   revisions" lists its history. The mode is a different
                   thing from the active show: it says whether the
                   installation is being programmed or is running a show,
                   and it is readable with observation:read so an operator
                   can always see which mode they are in
  delete --confirm <id>
                   tombstone this show (write); revision history stays
                   readable via "revisions". Refused with a conflict while
                   this id is the currently active show ("show active");
                   change the active show first. Never cascades: any
                   show.surface/show.action/show.macro/show.cue/
                   show.playlist/night.session still naming this show id
                   is left in place

Run "showmeshctl show <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdShowList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show objects (GET /api/v1/config/show).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show", nil, &resp); err != nil {
		return reportError(stderr, "show list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdShowGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show get [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one show's full definition (GET /api/v1/config/show/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "show get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show get", err)
		}
		return exitOK
	}
	printShowDetail(stdout, resp)
	return exitOK
}

func cmdShowSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show set", stderr)
	var name, notes string
	fs.StringVar(&name, "name", "", "the show's name (required)")
	fs.StringVar(&notes, "notes", "", "the show's notes")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show set [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show revision (PUT /api/v1/config/show/{id}). Requires")
		_, _ = fmt.Fprintln(stderr, "config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT, never a read-modify-write: --name and --notes")
		_, _ = fmt.Fprintln(stderr, "are sent on every call regardless of whether either flag is given, and an")
		_, _ = fmt.Fprintln(stderr, "omitted --notes becomes empty on the coordinator, never \"left as it was\".")
		_, _ = fmt.Fprintln(stderr, "This command never reads the current value first (except for If-Match, below).")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "show changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if name == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl show set: --name is required")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	apiPath := "/api/v1/config/show/" + url.PathEscape(id)
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r showConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "show set", err)
	}

	body := configShow{Name: name, Notes: notes}
	var resp showConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "show set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show set", err)
		}
		return exitOK
	}
	printShowDetail(stdout, resp)
	return exitOK
}

func cmdShowRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show revisions [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nList show revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show/{id}/revisions). Metadata only, no payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "show revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func cmdShowActive(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show active", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show active [flags]")
		_, _ = fmt.Fprintln(stderr, "\nPrint the currently active show (GET /api/v1/config/show.active).")
		_, _ = fmt.Fprintln(stderr, "404 if nothing has ever been activated.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show active", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show active", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showActiveConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.active", nil, &resp); err != nil {
		return reportError(stderr, "show active", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show active", err)
		}
		return exitOK
	}
	printShowActiveDetail(stdout, resp)
	return exitOK
}

func cmdShowActivate(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show activate", stderr)
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show activate [flags] <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nMake <show-id> the active show (PUT /api/v1/config/show.active).")
		_, _ = fmt.Fprintln(stderr, "Requires config:write, admin only. Audited and revisioned like any")
		_, _ = fmt.Fprintln(stderr, "other configuration write, so a history of which show was active when")
		_, _ = fmt.Fprintln(stderr, "is preserved.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "active show pointer changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show activate", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show activate", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const showActiveAPIPath = "/api/v1/config/show.active"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r showActiveConfigResponse
		if err := c.getJSON(ctx, showActiveAPIPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "show activate", err)
	}

	body := configShowActive{Show: id}
	var resp showActiveConfigResponse
	if err := c.putJSON(ctx, showActiveAPIPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "show activate", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "show activate", err)
		}
		return exitOK
	}
	printShowActiveDetail(stdout, resp)
	return exitOK
}

func printShowDetail(w io.Writer, resp showConfigResponse) {
	_, _ = fmt.Fprintf(w, "Show ID:   %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Name:      %s\n", resp.Payload.Name)
	if resp.Payload.Notes != "" {
		_, _ = fmt.Fprintf(w, "Notes:     %s\n", resp.Payload.Notes)
	}
	_, _ = fmt.Fprintf(w, "Revision:  %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:   %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by: %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by: (no principal recorded)\n")
	}
}

// cmdShowDelete mirrors cmdUndeclare's own shape (cmd_discovery.go) and
// cmdSurfaceDelete's (cmd_surface.go) one kind over: --confirm is required
// and checked locally before any request is sent. A tombstone, not a hard
// delete: revision history stays readable through "show revisions"
// afterward. The coordinator refuses with a conflict while this id is the
// currently active show; this command does not special-case that, since
// reportError already maps a 409 to exitConflict generically.
func cmdShowDelete(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl show delete", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms deletion of this show")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl show delete --confirm <show-id>")
		_, _ = fmt.Fprintln(stderr, "\nDelete a show object (DELETE /api/v1/config/show/{id}). This is a")
		_, _ = fmt.Fprintln(stderr, "tombstone, not a hard delete: the object's revision history still reads")
		_, _ = fmt.Fprintln(stderr, "through \"show revisions\" afterward. Refused if this show is currently")
		_, _ = fmt.Fprintln(stderr, "active (\"show active\"); change the active show first. Requires")
		_, _ = fmt.Fprintln(stderr, "config:write and --confirm.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "show delete", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl show delete: refusing to delete "+id+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "show delete", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configObjectDeleteRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/config/show/"+url.PathEscape(id), body, nil); err != nil {
		return reportError(stderr, "show delete", err)
	}

	_, _ = fmt.Fprintf(stdout, "show %s deleted\n", id)
	return exitOK
}

func printShowActiveDetail(w io.Writer, resp showActiveConfigResponse) {
	_, _ = fmt.Fprintf(w, "Active show: %s\n", resp.Payload.Show)
	_, _ = fmt.Fprintf(w, "Revision:    %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:     %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:  %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:  (no principal recorded)\n")
	}
}
