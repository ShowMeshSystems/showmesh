package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// This file is showmeshctl's Resolume action surface: "resolume action
// <verb> [args]" over POST /api/v1/resolume/actions, and "resolume action
// list" over GET /api/v1/resolume/actions. Every wire type below is this
// program's own transcription of the contract, never a shared struct with
// the coordinator's internal types — see resolumeActionRequest's own doc
// comment for why.

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

// minResolumeActionClientTimeout is every "resolume action <verb>"
// subcommand's minimum request budget, since the coordinator can hold a
// dispatched action open for its own write deadline before answering.
// Reconciled against resolumeActionHTTPWriteDeadline (55s,
// internal/coordinator/api/resolumeaction.go) by
// TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget there and
// TestMinResolumeActionClientTimeoutExceedsServerDefault here — two
// independent literals, since this program cannot import that package.
const minResolumeActionClientTimeout = 80 * time.Second

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
	reportResolumeActionWarnings(stderr, cmdLabel, resp.Result)

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForResolumeActionResult(resp.Result)
	}
	return reportResolumeActionResult(stdout, cmdLabel, resp.Result)
}

// reportResolumeActionWarnings writes result's Replay/AttributionDegraded
// warnings to stderr — shared by both --output modes, since both facts
// are operator-facing and stderr is not the JSON stream printed to
// stdout.
func reportResolumeActionWarnings(stderr io.Writer, cmdLabel string, result resolumeActionResult) {
	if result.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", cmdLabel, result.ID)
	}
	if result.AttributionDegraded {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: WARNING: the coordinator's audit write "+
			"failed for this command; it proceeded anyway (ADR-024 decision 11's safety class) with degraded "+
			"attribution recorded only to its own stderr\n", cmdLabel)
	}
}

// reportResolumeActionResult prints result's outcome honestly to stdout
// and returns the exit code it maps to — the Resolume-action sibling of
// reportFPPCommandResult (cmd_fpp_command.go), widened to this
// vocabulary's five outcomes instead of two. exitOK is returned only for a
// genuinely "confirmed" outcome, never for any of the other four.
func reportResolumeActionResult(stdout io.Writer, cmdLabel string, result resolumeActionResult) int {
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
		return exitActionRefused
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
		return exitActionRefused
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
		return cmdResolumeLaunchClip(rest, stdout, stderr, clock)
	case "clear-layer":
		return cmdResolumeSingleNameAction(rest, stdout, stderr, clock, "resolume action clear-layer", "clearLayer",
			"layer", "<layer name>", "Clear (disconnect) a layer's active clip by name.")
	case "launch-column":
		return cmdResolumeLaunchColumn(rest, stdout, stderr, clock)
	case "select-deck":
		return cmdResolumeSingleNameAction(rest, stdout, stderr, clock, "resolume action select-deck", "selectDeck",
			"deck", "<deck name>", "Select a deck by name.")
	case "blackout":
		return cmdResolumeBlackout(rest, stdout, stderr, clock)
	case "set-layer-bypass":
		return cmdResolumeSetLayerBoolParam(rest, stdout, stderr, clock, "resolume action set-layer-bypass",
			"setLayerBypass", "bypassed", "Set a layer's bypass state by name.")
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
coordinator supports. Every reference below is a NAME (ADR-037) — the
coordinator resolves it against the stored composition; no Resolume object
id ever appears on this command line. Every dispatch subcommand requires
the resolume:action scope, mints a fresh idempotency key per invocation,
and reports the coordinator's outcome honestly — a request that completes
an HTTP round trip is not the same as the action having taken effect; see
the exit code table in "showmeshctl help".

Subcommands:
  list                                          show the action vocabulary
                                                 this coordinator supports
  launch-clip --deck <deck> [--layer <layer>] <clip name>
                                                 launch a deck clip (write)
  launch-clip --persistent [--layer <layer>] <clip name>
                                                 launch a persistent clip (write)
  clear-layer <layer name>                      clear (disconnect) a layer (write)
  launch-column --deck <deck> <column name>     launch (connect) a column (write)
  select-deck <deck name>                       select a deck (write)
  blackout                                      disconnect every tracked layer (write)
  set-layer-bypass <layer name> <true|false>    set a layer's bypass (write)
  set-layer-master <layer name> <value>         set a layer's master to a
                                                 continuous value (write)

--layer disambiguates a clip name that occurs more than once; --deck and
--persistent are mutually exclusive and exactly one is required for
launch-clip.

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

// cmdResolumeSingleNameAction implements every subcommand whose entire
// payload is one named reference under one params key: clear-layer
// ("layer"), select-deck ("deck"). launch-clip and launch-column carry more
// than one param and have their own functions below.
func cmdResolumeSingleNameAction(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, wireAction, paramName, argName, help string) int {
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
	name := rest[0]
	if name == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "%s must not be empty", argName))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, wireAction, map[string]any{paramName: name})
}

// cmdResolumeLaunchClip implements launch-clip: a clip name, scoped to
// either a named deck or --persistent — never both, never neither (ADR-037
// TRACK-D-SEAM-B-NAMES-SPEC.md §2.1) — with an optional --layer to
// disambiguate a clip name that occurs more than once. --deck and
// --persistent are registered on the SAME flag.FlagSet newFlagSet returns,
// alongside the global flags, per this seam's own convention for
// subcommand-scoped flags.
func cmdResolumeLaunchClip(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "resolume action launch-clip"
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	var deck, layer string
	var persistent bool
	fs.StringVar(&deck, "deck", "", "the deck this clip lives on (required unless --persistent)")
	fs.StringVar(&layer, "layer", "", "disambiguate a clip name that occurs more than once")
	fs.BoolVar(&persistent, "persistent", false, "the clip is a persistent clip (lives outside any deck)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s --deck <deck> [--layer <layer>] <clip name>\n"+
			"       showmeshctl %s --persistent [--layer <layer>] <clip name>\n\n"+
			"Launch (connect) a clip by name. Exactly one of --deck or --persistent\n"+
			"is required. Requires resolume:action.\n", cmdLabel, cmdLabel)
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
	clip := rest[0]
	if clip == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "clip name must not be empty"))
	}
	if persistent && deck != "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "--deck and --persistent must not both be given"))
	}
	if !persistent && deck == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "either --deck or --persistent is required"))
	}

	params := map[string]any{"clip": clip}
	if persistent {
		params["persistent"] = true
	} else {
		params["deck"] = deck
	}
	if layer != "" {
		params["layer"] = layer
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, "launchClip", params)
}

// cmdResolumeLaunchColumn implements launch-column: a column name, always
// scoped to a required --deck (§2's table: "deck" is required, never
// conditional, for launchColumn).
func cmdResolumeLaunchColumn(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "resolume action launch-column"
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	var deck string
	fs.StringVar(&deck, "deck", "", "the deck this column lives on (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s --deck <deck> <column name> [flags]\n\n"+
			"Launch (connect) a column by name. Requires resolume:action.\n", cmdLabel)
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
	column := rest[0]
	if column == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "column name must not be empty"))
	}
	if deck == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "--deck is required"))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, "launchColumn", map[string]any{"column": column, "deck": deck})
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

// cmdResolumeSetLayerBoolParam implements set-layer-bypass — a layer name
// and one named boolean parameter. setLayerMaster is a continuous number
// end to end, not a boolean, so it has its own subcommand below rather
// than sharing this shape.
func cmdResolumeSetLayerBoolParam(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, wireAction, boolParamName, help string) int {
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s <layer name> <true|false> [flags]\n\n%s\nRequires resolume:action.\n", cmdLabel, help)
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
	layer := rest[0]
	if layer == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "layer name must not be empty"))
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
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, wireAction, map[string]any{"layer": layer, boolParamName: value})
}

// cmdResolumeSetLayerMaster implements set-layer-master — a layer name and
// one continuous numeric value: a layer master that can only be 0 or 1 is
// not a master. The coordinator validates the value against Arena's own
// declared range for this specific layer's master parameter (read fresh at
// dispatch time); this program does not duplicate that check client-side.
func cmdResolumeSetLayerMaster(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "resolume action set-layer-master"
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s <layer name> <value> [flags]\n\n"+
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
	layer := rest[0]
	if layer == "" {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "layer name must not be empty"))
	}
	value, err := strconv.ParseFloat(rest[1], 64)
	if err != nil {
		return reportError(stderr, cmdLabel, newCLIError(exitUsage, "value must be a number, not %q", rest[1]))
	}
	return dispatchResolumeAction(stdout, stderr, clock, g, cmdLabel, "setLayerMaster", map[string]any{"layer": layer, "master": value})
}
