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

// This file is Track F seam F1's own showmeshctl surface (ADR-039's parity
// rule: a capability with no CLI verb is the defect that made Resolume
// unconnectable; the night.session config kind gets full coverage in the
// same seam that adds its API, not a later cleanup pass): "night list",
// "night get", "night set" (from --file or stdin, full replacement),
// "night revisions", "night revision" (one past revision's full payload),
// "night active" (get the pointer), "night activate" (set it), and
// "night deactivate" (clear it back to unset — ADR-039 rule 4's
// zero-to-one-and-back-to-zero transition, and the reason a plain
// "activate ''" is not offered: an empty positional argument is easy to
// type by accident, so clearing gets its own explicit verb).

func cmdNight(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printNightUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printNightUsage(stdout)
		return exitOK
	case "list":
		return cmdNightList(rest, stdout, stderr, clock)
	case "get":
		return cmdNightGet(rest, stdout, stderr, clock)
	case "set":
		return cmdNightSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdNightRevisions(rest, stdout, stderr, clock)
	case "revision":
		return cmdNightRevision(rest, stdout, stderr, clock)
	case "active":
		return cmdNightActive(rest, stdout, stderr, clock)
	case "activate":
		return cmdNightActivate(rest, stdout, stderr, clock)
	case "deactivate":
		return cmdNightDeactivate(rest, stdout, stderr, clock)
	case "delete":
		return cmdNightDelete(rest, stdout, stderr, clock)
	case "status":
		return cmdNightLifecycleStatus(rest, stdout, stderr, clock)
	case "prepare-site":
		return cmdNightPrepareSite(rest, stdout, stderr, clock)
	case "readiness":
		return cmdNightReadiness(rest, stdout, stderr, clock)
	case "preshow":
		return cmdNightPreshow(rest, stdout, stderr, clock)
	case "start":
		return cmdNightStart(rest, stdout, stderr, clock)
	case "final-show":
		return cmdNightFinalShow(rest, stdout, stderr, clock)
	case "fade-out":
		return cmdNightFadeOut(rest, stdout, stderr, clock)
	case "power-down":
		return cmdNightPowerDown(rest, stdout, stderr, clock)
	case "end-session":
		return cmdNightEndSession(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl night: unknown subcommand %q\n\n", sub)
		printNightUsage(stderr)
		return exitUsage
	}
}

func printNightUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl night <subcommand> [flags]

Read or write the coordinator's "night.session" configuration objects
(RESTING-MODE.md, ADR-038, ADR-039) and the night.session.active singleton
pointer. Reads require show:macro:run OR config:write, matching
"show"/"macro"/"action"; writes require config:write.

FPP alone authorizes and schedules a night session (ADR-038): no
night.session field may carry a wall-clock time, date, weekday, timezone,
or cron expression, and a write carrying one is rejected.

Subcommands:
  list              enumerate night.session objects (id, label, show, revision)
  get <id>          show one night session's full definition
  set <id>          write a new night.session revision (write, full
                     replacement; reads a payload from --file, or from
                     stdin if --file is not given)
  revisions <id>    list revision history, newest first (metadata only)
  revision <id> <n> show one past revision's full payload (immutable,
                     ADR-009)
  active            print the currently active night session (404 if none
                     has ever been activated)
  activate <id>     make <id> the active night session (write, full
                     replacement of the night.session.active singleton)
  deactivate        clear the active night session back to unset (write;
                     the zero-to-one-and-back-to-zero transition ADR-039
                     rule 4 requires)
  delete --confirm <id>
                     tombstone this session (write); revision history
                     stays readable via "revisions"/"revision". Refused
                     with a conflict while this id is the active night
                     session ("night active"); deactivate it first

Lifecycle (RESTING-MODE.md, ADR-038 — the closed state machine and its
seven commands; reads open, writes require night:command):
  status            print the current night session's lifecycle state
  prepare-site      open a new preparation epoch
  readiness         run readiness for the current preparation epoch
  preshow           enter the configured pre-show presentation
  start             authorize the night and begin the first transition
  final-show        close admission after one final complete show
  fade-out          fade the active non-live presentation to stopped
  power-down        close the session after playback and the fade stop
  end-session       PROVISIONAL operator recovery: abandon the current
                     session, reach stopped, launch nothing (the only
                     command that runs against a degraded session)

Run "showmeshctl night <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdNightList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate night.session objects (GET /api/v1/config/night.session).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/night.session", nil, &resp); err != nil {
		return reportError(stderr, "night list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdNightGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night get [flags] <session-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one night session's full definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nightSessionConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/night.session/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "night get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night get", err)
		}
		return exitOK
	}
	printNightSessionDetail(stdout, resp)
	return exitOK
}

func cmdNightSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON night.session payload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night set [flags] <session-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new night.session configuration revision (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session/{id}, requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "This is a FULL REPLACEMENT: an omitted optional block (e.g.")
		_, _ = fmt.Fprintln(stderr, "backgroundAudio) is left out entirely, never wiped by an absent key on")
		_, _ = fmt.Fprintln(stderr, "top of a required one. Validated before activation: a calendar field, a")
		_, _ = fmt.Fprintln(stderr, "hand-entered rest duration, a siteControl/interlocks block, a dangling or")
		_, _ = fmt.Fprintln(stderr, "cross-show asset/action reference, or a negative duration field")
		_, _ = fmt.Fprintln(stderr, "(blackoutHoldMs, blackoutAfterShowMs, fadeDurationMs, crossfadeMs) is")
		_, _ = fmt.Fprintln(stderr, "rejected and appends no revision (ADR-009). A cue's own offsetMs is NOT")
		_, _ = fmt.Fprintln(stderr, "bounded here: checking it against the resting FSEQ's actual length needs")
		_, _ = fmt.Fprintln(stderr, "a live FPP read and is readiness work, not this write-time check.")
		_, _ = fmt.Fprintln(stderr, "Accepts either a bare payload, or the full object \"night get --output json\" prints.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "night set", newCLIError(exitUsage, "%v", err))
	}
	raw, err = unwrapConfigGetResponse(raw)
	if err != nil {
		return reportError(stderr, "night set", newCLIError(exitUsage, "%v", err))
	}
	if !json.Valid(raw) {
		return reportError(stderr, "night set", newCLIError(exitUsage, "payload must be valid JSON"))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nightSessionConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/night.session/"+url.PathEscape(id), json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "night set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night set", err)
		}
		return exitOK
	}
	printNightSessionDetail(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl night set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

// cmdNightDelete mirrors cmdShowDelete's own shape (cmd_show.go):
// --confirm is required and checked locally before any request is sent. A
// tombstone, not a hard delete: revision history stays readable through
// "night revisions"/"night revision" afterward. The coordinator refuses
// with a conflict while this id is the active night session; this command
// does not special-case that, since reportError already maps a 409 to
// exitConflict generically.
func cmdNightDelete(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night delete", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms deletion of this night session")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night delete --confirm <session-id>")
		_, _ = fmt.Fprintln(stderr, "\nDelete a night.session object (DELETE /api/v1/config/night.session/{id}).")
		_, _ = fmt.Fprintln(stderr, "A tombstone, not a hard delete: revision history stays readable via")
		_, _ = fmt.Fprintln(stderr, "\"night revisions\"/\"night revision\" afterward. Refused if this session is")
		_, _ = fmt.Fprintln(stderr, "currently active (\"night active\"); deactivate it first. Requires")
		_, _ = fmt.Fprintln(stderr, "config:write and --confirm.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night delete", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl night delete: refusing to delete "+id+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night delete", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configObjectDeleteRequest{Confirm: true}
	if err := c.deleteJSON(ctx, "/api/v1/config/night.session/"+url.PathEscape(id), body, nil); err != nil {
		return reportError(stderr, "night delete", err)
	}

	_, _ = fmt.Fprintf(stdout, "night session %s deleted\n", id)
	return exitOK
}

func cmdNightRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night revisions [flags] <session-id>")
		_, _ = fmt.Fprintln(stderr, "\nList night.session revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session/{id}/revisions). Metadata only, no payload;")
		_, _ = fmt.Fprintln(stderr, "use \"night revision <id> <n>\" for one revision's full payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/night.session/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "night revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func cmdNightRevision(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night revision", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night revision [flags] <session-id> <revision>")
		_, _ = fmt.Fprintln(stderr, "\nShow one past revision's full payload (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session/{id}/revisions/{revision}). Revisions are")
		_, _ = fmt.Fprintln(stderr, "immutable (ADR-009); this may not be the currently active one.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night revision", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	revNum, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil || revNum <= 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl night revision: <revision> must be a positive integer")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night revision", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nightSessionConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/night.session/"+url.PathEscape(id)+"/revisions/"+strconv.FormatInt(revNum, 10), nil, &resp); err != nil {
		return reportError(stderr, "night revision", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night revision", err)
		}
		return exitOK
	}
	printNightSessionDetail(stdout, resp)
	return exitOK
}

func cmdNightActive(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night active", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night active [flags]")
		_, _ = fmt.Fprintln(stderr, "\nPrint the currently active night session (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session.active). 404 if nothing has ever been")
		_, _ = fmt.Fprintln(stderr, "activated.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night active", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night active", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nightSessionActiveConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/night.session.active", nil, &resp); err != nil {
		return reportError(stderr, "night active", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night active", err)
		}
		return exitOK
	}
	printNightSessionActiveDetail(stdout, resp)
	return exitOK
}

func putNightSessionActive(g *globalFlags, stdout, stderr io.Writer, clock func() time.Time, cmdName, session string) int {
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdName, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := nightSessionActive{Session: session}
	var resp nightSessionActiveConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/night.session.active", body, &resp); err != nil {
		return reportError(stderr, cmdName, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdName, err)
		}
		return exitOK
	}
	printNightSessionActiveDetail(stdout, resp)
	return exitOK
}

func cmdNightActivate(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night activate", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night activate [flags] <session-id>")
		_, _ = fmt.Fprintln(stderr, "\nMake <session-id> the active night session (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session.active). Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "Audited and revisioned like any other configuration write.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night activate", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	return putNightSessionActive(g, stdout, stderr, clock, "night activate", rest[0])
}

func cmdNightDeactivate(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night deactivate", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night deactivate [flags]")
		_, _ = fmt.Fprintln(stderr, "\nClear the active night session back to unset (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/night.session.active with an empty session). Requires")
		_, _ = fmt.Fprintln(stderr, "config:write, admin only. This is the \"back to zero\" half of ADR-039")
		_, _ = fmt.Fprintln(stderr, "rule 4's zero-to-one-and-back-to-zero transition.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night deactivate", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	return putNightSessionActive(g, stdout, stderr, clock, "night deactivate", "")
}
