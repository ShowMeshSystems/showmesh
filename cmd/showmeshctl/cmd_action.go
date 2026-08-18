package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// This file is "showmeshctl action list"/"action show"/"action put",
// against the show.action configuration kind. list/show fall out of the
// identical GET /config/{kind}[/{id}] shape "macro list"/"macro show"
// already use — see printShowConfigObjectsTable (macro_print.go), which
// both list commands share.
//
// "put" is integration-agnostic (it PUTs whatever valid show.action
// payload it is given, fpp, mqtt, or resolume) rather than growing
// per-integration flags: "action show --output json" prints exactly what
// "action put" accepts back, mirroring "config get"/"config set"'s own
// round-trip contract (cmd_config.go). Writing a show.macro definition is
// "macro put" (cmd_macro.go, Track G seam G-6), following this file's own
// shape.

func cmdAction(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printActionUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printActionUsage(stdout)
		return exitOK
	case "list":
		return cmdActionList(rest, stdout, stderr, clock)
	case "show":
		return cmdActionShow(rest, stdout, stderr, clock)
	case "put":
		return cmdActionPut(rest, stdout, stderr, clock)
	case "check":
		return cmdActionCheck(rest, stdout, stderr, clock)
	case "invoke":
		return cmdActionInvoke(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl action: unknown subcommand %q\n\n", sub)
		printActionUsage(stderr)
		return exitUsage
	}
}

func printActionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl action <subcommand> [flags]

Read or write the coordinator's show.action configuration objects: the
logical actions ("Projectors on", "Stop main show", a Resolume clip
launch) that a show.macro's steps reference. Reads require show:macro:run
OR config:write; put requires config:write (admin only).

Subcommands:
  list [--show <id>]
                enumerate action objects (id, label, show, revision),
                optionally narrowed to one show
  show <id>     show one action's full definition, including its target
  put <id>      write a new show.action configuration revision (reads a
                payload from --file, or from stdin if --file is not given)
  check [<id>] [--show <id>]
                re-resolve one action's (or, with no id, every action's)
                stored target against current integration state; exits
                29 if any checked binding is broken, 0 otherwise
  invoke <id>   invoke one stored action by id, outside of a macro run;
                requires show:action:invoke

"action put" accepts a full show.action JSON payload (show, label,
safetyClass, target) for any integration, including "resolume" — the
target names a Resolume action and a named reference (ADR-037: clip, deck,
layer, column, persistent, bypassed, master), never a Resolume object id.
Validated before activation: an invalid payload, or a reference that does
not resolve against the currently stored composition, is rejected and
appends no revision (ADR-009).

Writing a show.macro definition is "showmeshctl macro put" — see
"showmeshctl macro --help".

Run "showmeshctl action <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdActionList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to actions belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.action objects (GET /api/v1/config/show.action).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.action", query, &resp); err != nil {
		return reportError(stderr, "action list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdActionShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action show", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action show [flags] <action-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one action's full definition (GET /api/v1/config/show.action/{id}),")
		_, _ = fmt.Fprintln(stderr, "including its safetyClass and its fpp/mqtt target.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action show", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveMacroClientTimeout(g.timeout))
	defer cancel()

	var resp showActionConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.action/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "action show", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action show", err)
		}
		return exitOK
	}
	printShowActionDetail(stdout, resp)
	return exitOK
}

// cmdActionPut implements "showmeshctl action put"
// (PUT /api/v1/config/show.action/{id}). The payload is read from --file,
// or from stdin when --file is not given (readConfigPayload, cmd_config.go)
// and sent to the coordinator verbatim as json.RawMessage: this command
// does not decode or re-encode the operator's own payload, matching
// "config set"'s identical "the server validates, this program does not
// duplicate that check client-side" posture — the one thing checked here
// is that it is syntactically valid JSON at all, so a malformed file fails
// fast with a clear message rather than an opaque 400 from the server.
func cmdActionPut(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action put", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON show.action payload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action put [flags] <action-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.action configuration revision")
		_, _ = fmt.Fprintln(stderr, "(PUT /api/v1/config/show.action/{id}, requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "The payload is a full show.action object: show, label, safetyClass, and")
		_, _ = fmt.Fprintln(stderr, "target. Validated before activation: an invalid payload, or a reference")
		_, _ = fmt.Fprintln(stderr, "that does not resolve, is rejected and appends no revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action put", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "action put", newCLIError(exitUsage, "%v", err))
	}
	if !json.Valid(raw) {
		return reportError(stderr, "action put", newCLIError(exitUsage, "payload must be valid JSON"))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action put", err)
	}
	// A local SQLite write with no confirmation wait, exactly like
	// "config set" (cmd_config.go) — g.timeout unmodified, not
	// effectiveMacroClientTimeout's floor, which exists for the run/macro
	// surface's own different contract (no response held open on this
	// route either, so borrowing that floor would size this budget from a
	// rationale that does not apply here).
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showActionConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/show.action/"+url.PathEscape(id), json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "action put", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action put", err)
		}
		return exitOK
	}
	printShowActionDetail(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl action put: revision %d is now active.\n", resp.Revision)
	return exitOK
}

// cmdActionCheck implements "showmeshctl action check [<id>] [--show <id>]"
// (Track E seam E7-2): GET /api/v1/actions/{id}/binding with one id, or
// GET /api/v1/actions/bindings otherwise. Exits exitActionBindingBroken
// (29) when any checked binding is "broken"; "unknown" never exits 29 —
// it means the check could not be performed, not that anything is
// broken.
func cmdActionCheck(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action check", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the check to actions belonging to this show id (only with no <id>)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action check [<id>] [--show <id>] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nRe-resolve one action's (or every action's) stored target against")
		_, _ = fmt.Fprintln(stderr, "current integration state. A read: dispatches nothing, no credential")
		_, _ = fmt.Fprintln(stderr, "required. Exits 29 if any checked binding is broken; \"unknown\" never")
		_, _ = fmt.Fprintln(stderr, "exits 29.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action check", err)
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "action check", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var bindings []actionBinding
	var serverTime time.Time
	if len(rest) == 1 {
		if show != "" {
			_, _ = fmt.Fprintln(stderr, "showmeshctl action check: --show is ignored when an action id is given")
		}
		var resp actionBindingResponse
		if err := c.getJSON(ctx, "/api/v1/actions/"+url.PathEscape(rest[0])+"/binding", nil, &resp); err != nil {
			return reportError(stderr, "action check", err)
		}
		serverTime = resp.ServerTime
		bindings = []actionBinding{resp.Binding}
	} else {
		var query url.Values
		if show != "" {
			query = url.Values{"show": {show}}
		}
		var resp actionBindingsResponse
		if err := c.getJSON(ctx, "/api/v1/actions/bindings", query, &resp); err != nil {
			return reportError(stderr, "action check", err)
		}
		serverTime = resp.ServerTime
		bindings = resp.Bindings
	}
	printClockSkew(stderr, serverTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, bindings); err != nil {
			return reportError(stderr, "action check", err)
		}
		return exitCodeForActionBindings(bindings)
	}
	printActionBindings(stdout, bindings)
	return exitCodeForActionBindings(bindings)
}

// exitCodeForActionBindings reports exitActionBindingBroken when any
// binding is "broken", exitOK otherwise. "unknown" is never a reason to
// exit 29.
func exitCodeForActionBindings(bindings []actionBinding) int {
	for _, b := range bindings {
		if b.State == "broken" {
			return exitActionBindingBroken
		}
	}
	return exitOK
}

func printActionBindings(w io.Writer, bindings []actionBinding) {
	if len(bindings) == 0 {
		_, _ = fmt.Fprintln(w, "(no actions to check)")
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ID\tSHOW\tSTATE\tREASON")
	for _, b := range bindings {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ActionID, b.Show, b.State, b.Reason)
	}
	_ = tw.Flush()
}

// minActionInvokeClientTimeout is "action invoke"'s minimum request
// budget, mirroring minResolumeActionClientTimeout's identical reasoning:
// the coordinator's own write deadline (actionInvokeHTTPWriteDeadline,
// 150s, internal/coordinator/api/actioninvoke.go) can hold the response
// open that long. Reconciled against it by
// TestMinActionInvokeClientTimeoutExceedsServerDeadline.
const minActionInvokeClientTimeout = 170 * time.Second

func effectiveActionInvokeTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minActionInvokeClientTimeout {
		return minActionInvokeClientTimeout
	}
	return flagTimeout
}

// cmdActionInvoke implements "showmeshctl action invoke <id>" (Track E
// seam E7-1): POST /api/v1/actions/{id}/invocations, requiring
// show:action:invoke. Exits 0 confirmed, 9 unconfirmed, 11 unconfirmable,
// 12 failed, 13 refused — reusing the exact codes "resolume action
// <verb>" already uses; the ADR-020 outcome vocabulary does not fork per
// surface.
func cmdActionInvoke(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl action invoke", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl action invoke [flags] <action-id>")
		_, _ = fmt.Fprintln(stderr, "\nInvoke one stored show.action by id, outside of any macro run")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/actions/{id}/invocations, requires show:action:invoke).")
		_, _ = fmt.Fprintln(stderr, "The action's own stored target supplies every parameter; this command")
		_, _ = fmt.Fprintln(stderr, "passes none.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "action invoke", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	timeout := effectiveActionInvokeTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl action invoke: --timeout %s is below this command's own minimum request budget of %s; "+
				"using %s instead.\n", g.timeout, minActionInvokeClientTimeout, timeout)
	}
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "action invoke", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, "action invoke", err)
	}

	var resp actionInvocationResponse
	if err := c.postJSON(ctx, "/api/v1/actions/"+url.PathEscape(id)+"/invocations",
		actionInvocationRequest{IdempotencyKey: key}, &resp); err != nil {
		return reportError(stderr, "action invoke", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if resp.Result.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl action invoke: this idempotency key was already used; "+
			"returning the ORIGINAL invocation's result (id %s), nothing was dispatched\n", resp.Result.ID)
	}
	if resp.Result.AttributionDegraded {
		_, _ = fmt.Fprintf(stderr, "showmeshctl action invoke: WARNING: the coordinator's audit write failed "+
			"for this invocation; it proceeded anyway with degraded attribution recorded only to its own stderr\n")
	}

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "action invoke", err)
		}
		return exitCodeForActionInvocation(resp.Result)
	}
	return reportActionInvocationResult(stdout, resp.Result)
}

// reportActionInvocationResult prints result's outcome, leading with the
// outcome word and the reason, and returns the matching exit code.
// "unconfirmable" is printed with its own distinct word, never merged
// into "confirmed" — an operator who cannot tell the two apart at a
// glance stops reading them (ADR-029 decision 4).
func reportActionInvocationResult(stdout io.Writer, result actionInvocationResult) int {
	label := result.Label
	if label == "" {
		label = result.ActionID
	}
	switch result.Outcome {
	case "confirmed":
		_, _ = fmt.Fprintf(stdout, "confirmed: %s (invocation %s): %s\n", label, result.ID, result.OutcomeReason)
		return exitOK
	case "unconfirmed":
		_, _ = fmt.Fprintf(stdout, "unconfirmed: %s: %s (invocation %s)\n", label, result.OutcomeReason, result.ID)
		return exitCommandUnconfirmed
	case "unconfirmable":
		_, _ = fmt.Fprintf(stdout, "unconfirmable: %s: %s (invocation %s)\n", label, result.OutcomeReason, result.ID)
		return exitActionUnconfirmable
	case "refused":
		_, _ = fmt.Fprintf(stdout, "refused: %s: %s (invocation %s)\n", label, result.OutcomeReason, result.ID)
		return exitActionRefused
	case "failed":
		_, _ = fmt.Fprintf(stdout, "failed: %s: %s (invocation %s)\n", label, result.OutcomeReason, result.ID)
		return exitActionFailed
	default:
		_, _ = fmt.Fprintf(stdout, "pending: %s: invocation %s has not yet resolved\n", label, result.ID)
		return exitCommandUnconfirmed
	}
}

func exitCodeForActionInvocation(result actionInvocationResult) int {
	switch result.Outcome {
	case "confirmed":
		return exitOK
	case "unconfirmable":
		return exitActionUnconfirmable
	case "refused":
		return exitActionRefused
	case "failed":
		return exitActionFailed
	default:
		return exitCommandUnconfirmed
	}
}
