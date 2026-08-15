package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// This file is Track D seam D-3/B's showmeshctl surface: "resolume action
// <verb> [args]" over POST /api/v1/resolume/actions, and "resolume action
// list" over GET /api/v1/resolume/actions. Per this program's own
// independence rule (doc.go, importgraph_test.go), every wire type below
// is this file's own transcription of the contract, not a shared struct
// with the coordinator's internal types — see resolumeActionRequest's own
// doc comment.
//
// ADR-030: this coverage is not deferred to a later Operator UI seam. The
// CLI is the "the show is broken and the UI is down" path, which makes it
// a fully tested emergency path rather than contract hygiene, so every one
// of the seven actions gets its own subcommand here, shaped exactly like
// the "fpp <verb>" write subcommands (cmd_fpp_command.go): one shared
// dispatch core (dispatchResolumeAction below), a fresh idempotency key
// minted per invocation, and an outcome reported honestly — never a bare
// 200 read as success (ADR-003).

// resolumeActionResult mirrors v1.ResolumeActionResult field for field —
// see that type's doc comment in
// internal/coordinator/api/v1/resolumeaction.go for what each field means;
// this is this program's own independent transcription of it.
type resolumeActionResult struct {
	ID                  string         `json:"id"`
	IdempotencyKey      string         `json:"idempotencyKey"`
	Action              string         `json:"action"`
	Params              map[string]any `json:"params"`
	Replay              bool           `json:"replay"`
	Outcome             string         `json:"outcome"`
	OutcomeReason       string         `json:"outcomeReason"`
	AttributionDegraded bool           `json:"attributionDegraded"`
	DispatchedAt        *time.Time     `json:"dispatchedAt"`
	ResolvedAt          *time.Time     `json:"resolvedAt"`
}

// resolumeActionResponse is the body of a successful POST
// /api/v1/resolume/actions — mirroring fppCommandResponse's identical
// shape (types.go).
type resolumeActionResponse struct {
	ServerTime time.Time            `json:"serverTime"`
	Result     resolumeActionResult `json:"result"`
}

// resolumeActionRequest is the body this program sends to POST
// /api/v1/resolume/actions. Deliberately NOT
// internal/coordinator/api/v1.ResolumeActionRequest: this program mints
// its own idempotency key independently (newIdempotencyKey,
// cmd_fpp_command.go) rather than sharing a decoder with the coordinator,
// for the identical reason fppCommandRequest (types.go) does — a future
// JSON tag rename on the server must break this program's build, not
// silently rename the field on both sides at once.
type resolumeActionRequest struct {
	Action         string         `json:"action"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Params         map[string]any `json:"params,omitempty"`
}

// resolumeActionDescriptor mirrors v1.ResolumeAction — used only by
// "resolume action list".
type resolumeActionDescriptor struct {
	Name                string                `json:"name"`
	Params              []resolumeActionParam `json:"params"`
	AuditExempt         bool                  `json:"auditExempt"`
	CoordinatorRequired bool                  `json:"coordinatorRequired"`
}

type resolumeActionParam struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
}

type resolumeActionsResponse struct {
	ServerTime time.Time                  `json:"serverTime"`
	Actions    []resolumeActionDescriptor `json:"actions"`
}

// minResolumeActionClientTimeout is this program's own minimum request
// budget for every "resolume action <verb>" write subcommand, mirroring
// minFPPCommandClientTimeout's identical reasoning (cmd_fpp_command.go): a
// budget smaller than what the coordinator's own confirmation wait can
// take can only ever abort a healthy, still-working conversation and
// report it as a transport failure.
//
// This MUST match internal/coordinator/api/resolumeaction.go's own
// resolumeActionMaxConfirmDeadline (30s) plus a 15s round-trip margin —
// this program does not import that package (importgraph_test.go forbids
// it), so this is a SECOND, independently chosen literal, reconciled
// against the server's value by
// TestResolumeActionMaxConfirmDeadlineFitsWithinCLIClientBudget in
// internal/coordinator/api/resolumeaction_test.go, which hardcodes this
// exact value and fails if the server's own deadline is ever raised past
// what this literal assumes. See that test's own doc comment for why two
// independently chosen literals, not one shared constant, is the correct
// shape here — the identical reconciliation minFPPCommandClientTimeout's
// own doc comment describes for its own server-side counterpart.
const minResolumeActionClientTimeout = 45 * time.Second

func effectiveResolumeActionTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minResolumeActionClientTimeout {
		return minResolumeActionClientTimeout
	}
	return flagTimeout
}

// dispatchResolumeAction is the request/response core shared by every
// "showmeshctl resolume action <verb>" write subcommand — the Resolume-
// action sibling of dispatchFPPCommand (cmd_fpp_command.go). params is nil
// for blackout, the one zero-parameter action: resolumeActionRequest.Params
// carries "params,omitempty", so a nil map encodes as an OMITTED "params"
// key on the wire, never "null" and never an explicit "{}".
func dispatchResolumeAction(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, cmdLabel, action string, params map[string]any) int {
	timeout := effectiveResolumeActionTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s "+
				"instead. The coordinator holds an unresolved action's response open for its own confirmation "+
				"deadline before answering, so a shorter client budget can only ever produce a false "+
				"transport-timeout failure for a healthy, still-working conversation — never a genuinely faster "+
				"answer.\n",
			cmdLabel, g.timeout, minResolumeActionClientTimeout, timeout)
	}
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp resolumeActionResponse
	reqErr := c.postJSON(ctx, "/api/v1/resolume/actions",
		resolumeActionRequest{Action: action, IdempotencyKey: key, Params: params}, &resp)
	if reqErr != nil {
		return reportError(stderr, cmdLabel, reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForResolumeActionResult(resp.Result)
	}
	return reportResolumeActionResult(stdout, stderr, cmdLabel, resp.Result)
}

// reportResolumeActionResult prints result's outcome honestly to stdout
// and returns the exit code it maps to — the Resolume-action sibling of
// reportFPPCommandResult (cmd_fpp_command.go), widened to this
// vocabulary's five outcomes instead of two. exitOK is returned only for a
// genuinely "confirmed" outcome, never for any of the other four.
func reportResolumeActionResult(stdout, stderr io.Writer, cmdLabel string, result resolumeActionResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", cmdLabel, result.ID)
	}
	if result.AttributionDegraded {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: WARNING: the coordinator's audit write "+
			"failed for this command; it proceeded anyway (ADR-024 decision 11's safety class) with degraded "+
			"attribution recorded only to its own stderr\n", cmdLabel)
	}

	switch result.Outcome {
	case "confirmed":
		_, _ = fmt.Fprintf(stdout, "confirmed: %s (command %s): %s\n", result.Action, result.ID, result.OutcomeReason)
		return exitOK
	case "unconfirmed":
		_, _ = fmt.Fprintf(stdout, "unconfirmed: %s: %s (command %s)\n", result.Action, result.OutcomeReason, result.ID)
		return exitCommandUnconfirmed
	case "unconfirmable":
		_, _ = fmt.Fprintf(stdout, "unconfirmable: %s: %s (command %s)\n", result.Action, result.OutcomeReason, result.ID)
		return exitActionUnconfirmable
	case "refused":
		_, _ = fmt.Fprintf(stdout, "refused: %s: %s (command %s)\n", result.Action, result.OutcomeReason, result.ID)
		return exitConflict
	case "failed":
		_, _ = fmt.Fprintf(stdout, "failed: %s: %s (command %s)\n", result.Action, result.OutcomeReason, result.ID)
		return exitActionFailed
	default:
		// Empty outcome: the one accepted race a REPLAY response can
		// return before the original request's own dispatch/confirmation
		// has finished — see fppCommandResult.Outcome's identical doc
		// comment (types.go).
		_, _ = fmt.Fprintf(stdout, "pending: %s: command %s has not yet resolved\n", result.Action, result.ID)
		return exitCommandUnconfirmed
	}
}

// exitCodeForResolumeActionResult maps a decoded result to this program's
// exit code convention, for --output json's caller (which prints the raw
// decoded response rather than reportResolumeActionResult's own text and
// so needs the same outcome -> exit code mapping applied separately).
func exitCodeForResolumeActionResult(result resolumeActionResult) int {
	switch result.Outcome {
	case "confirmed":
		return exitOK
	case "unconfirmable":
		return exitActionUnconfirmable
	case "refused":
		return exitConflict
	case "failed":
		return exitActionFailed
	default:
		return exitCommandUnconfirmed
	}
}

// cmdResolumeAction implements "showmeshctl resolume action <verb>
// [args]".
func cmdResolumeAction(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printResolumeActionUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printResolumeActionUsage(stdout)
		return exitOK
	case "list":
		return cmdResolumeActionList(rest, stdout, stderr, clock)
	case "launch-clip":
		return cmdResolumeSingleIDAction(rest, stdout, stderr, clock, "resolume action launch-clip", "launchClip",
			"<clip-id>", "Launch (connect) a clip by its stored ShowMesh reference.")
	case "clear-layer":
		return cmdResolumeSingleIDAction(rest, stdout, stderr, clock, "resolume action clear-layer", "clearLayer",
			"<layer-id>", "Clear (disconnect) a layer's active clip by its stored ShowMesh reference.")
	case "launch-column":
		return cmdResolumeSingleIDAction(rest, stdout, stderr, clock, "resolume action launch-column", "launchColumn",
			"<column-id>", "Launch (connect) a column by its stored ShowMesh reference.")
	case "select-deck":
		return cmdResolumeSingleIDAction(rest, stdout, stderr, clock, "resolume action select-deck", "selectDeck",
			"<deck-id>", "Select a deck by its stored ShowMesh reference.")
	case "blackout":
		return cmdResolumeBlackout(rest, stdout, stderr, clock)
	case "set-layer-bypass":
		return cmdResolumeSetLayerBoolParam(rest, stdout, stderr, clock, "resolume action set-layer-bypass",
			"setLayerBypass", "bypassed", "Set a layer's bypass state by its stored ShowMesh reference.")
	case "set-layer-master":
		return cmdResolumeSetLayerMaster(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl resolume action: unknown subcommand %q\n\n", sub)
		printResolumeActionUsage(stderr)
		return exitUsage
	}
}

func printResolumeActionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl resolume action <subcommand> [args] [flags]

Dispatch one of the seven Resolume actions, or list the vocabulary this
coordinator supports. Every dispatch subcommand requires the
resolume:action scope, mints a fresh idempotency key per invocation, and
reports the coordinator's outcome honestly — a request that completes an
HTTP round trip is not the same as the action having taken effect; see the
exit code table in "showmeshctl help".

Subcommands:
  list                              show the action vocabulary this
                                     coordinator supports, with each
                                     action's own parameters
  launch-clip <clip-id>              launch (connect) a clip (write)
  clear-layer <layer-id>             clear (disconnect) a layer (write)
  launch-column <column-id>          launch (connect) a column (write)
  select-deck <deck-id>               select a deck (write)
  blackout                           disconnect every tracked layer (write)
  set-layer-bypass <layer-id> <true|false>   set a layer's bypass (write)
  set-layer-master <layer-id> <value>        set a layer's master to a
                                              continuous value (write)

Run "showmeshctl resolume action <subcommand> --help" for flags specific
to one subcommand.
`)
}

// cmdResolumeActionList implements "showmeshctl resolume action list"
// (GET /api/v1/resolume/actions). Never gated by any scope on the
// coordinator side — see handleListResolumeActions' own doc comment
// (internal/coordinator/api/resolumeaction.go).
func cmdResolumeActionList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume action list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume action list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the Resolume action vocabulary this coordinator supports.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume action list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume action list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp resolumeActionsResponse
	if err := c.getJSON(ctx, "/api/v1/resolume/actions", nil, &resp); err != nil {
		return reportError(stderr, "resolume action list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume action list", err)
		}
		return exitOK
	}

	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintln(tw, "NAME\tPARAMS\tAUDIT EXEMPT\tCOORDINATOR REQUIRED")
	for _, a := range resp.Actions {
		names := ""
		for i, p := range a.Params {
			if i > 0 {
				names += ", "
			}
			names += fmt.Sprintf("%s (%s)", p.Name, p.Kind)
		}
		if names == "" {
			names = "(none)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\n", a.Name, names, a.AuditExempt, a.CoordinatorRequired)
	}
	_ = tw.Flush()
	return exitOK
}

// cmdResolumeSingleIDAction implements every subcommand whose entire
// payload is one ShowMesh object reference ("id"): launch-clip,
// clear-layer, launch-column, select-deck.
func cmdResolumeSingleIDAction(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, wireAction, argName, help string) int {
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s %s [flags]\n\n%s\nRequires resolume:action.\n", cmdLabel, argName, help)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if id == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "%s must not be empty", argName))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, wireAction, map[string]any{"id": id})
}

// cmdResolumeBlackout implements "showmeshctl resolume action blackout"
// (no parameters).
func cmdResolumeBlackout(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume action blackout", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume action blackout [flags]")
		_, _ = fmt.Fprintln(stderr, "\nDisconnect every tracked layer. Requires resolume:action. Exempt from")
		_, _ = fmt.Fprintln(stderr, "ADR-024 decision 11's fail-closed audit rule: still dispatches even if")
		_, _ = fmt.Fprintln(stderr, "this coordinator's audit store is currently failing.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume action blackout", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, "resolume action blackout", "blackout", nil)
}

// cmdResolumeSetLayerBoolParam implements set-layer-bypass — a layer id and
// one named boolean parameter. setLayerMaster used to share this shape too
// (a boolean "master" mapped to Arena's own 0.0/1.0 range endpoints), but
// defect 3 (2026-08-15) made master a continuous number end to end; see
// cmdResolumeSetLayerMaster below for its own subcommand.
func cmdResolumeSetLayerBoolParam(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, wireAction, boolParamName, help string) int {
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s <layer-id> <true|false> [flags]\n\n%s\nRequires resolume:action.\n", cmdLabel, help)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if id == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "layer-id must not be empty"))
	}
	var value bool
	switch rest[1] {
	case "true":
		value = true
	case "false":
		value = false
	default:
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "value must be %q or %q, not %q", "true", "false", rest[1]))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, wireAction, map[string]any{"id": id, boolParamName: value})
}

// cmdResolumeSetLayerMaster implements set-layer-master — a layer id and
// one continuous numeric value, per defect 3 (2026-08-15): a layer master
// that can only be 0 or 1 is not a master, so this is its own subcommand
// rather than sharing cmdResolumeSetLayerBoolParam's boolean shape. The
// coordinator validates the value against Arena's own declared range for
// this specific layer's master parameter (read fresh at dispatch time);
// this program does not duplicate that check client-side.
func cmdResolumeSetLayerMaster(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "resolume action set-layer-master"
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s <layer-id> <value> [flags]\n\n"+
			"Set a layer's master to a continuous value. The coordinator validates\n"+
			"<value> against Arena's own declared range for this layer and refuses\n"+
			"an out-of-range request rather than clamping it silently.\nRequires resolume:action.\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	if id == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "layer-id must not be empty"))
	}
	value, err := strconv.ParseFloat(rest[1], 64)
	if err != nil {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "value must be a number, not %q", rest[1]))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, "setLayerMaster", map[string]any{"id": id, "master": value})
}
