package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

// run is main's testable core. Every subcommand handler takes explicit
// stdout/stderr and a clock function rather than reaching for os.Stdout,
// os.Stderr, or time.Now directly, so tests can capture output and inject
// a fixed clock — task spec §3 requires the clock-skew behaviour be tested
// "with a fixed clock", and that is only possible if the clock is a
// parameter, not a global.
func run(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printTopLevelUsage(stderr)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]

	switch cmd {
	case "-h", "-help", "--help", "help":
		printTopLevelUsage(stdout)
		return exitOK
	case "nodes":
		return cmdNodes(rest, stdout, stderr, clock)
	case "node":
		return cmdNode(rest, stdout, stderr, clock)
	case "fpp":
		return cmdFPP(rest, stdout, stderr, clock)
	case "events":
		return cmdEvents(rest, stdout, stderr, clock)
	case "snapshot":
		return cmdSnapshot(rest, stdout, stderr, clock)
	case "watch":
		return cmdWatch(rest, stdout, stderr, clock)
	case "session":
		return cmdSession(rest, stdout, stderr, clock)
	case "audit":
		return cmdAudit(rest, stdout, stderr, clock)
	case "config":
		return cmdConfig(rest, stdout, stderr, clock)
	case "discover":
		return cmdDiscover(rest, stdout, stderr, clock)
	case "declare":
		return cmdDeclare(rest, stdout, stderr, clock)
	case "undeclare":
		return cmdUndeclare(rest, stdout, stderr, clock)
	case "macro":
		return cmdMacro(rest, stdout, stderr, clock)
	case "run":
		return cmdRun(rest, stdout, stderr, clock)
	case "action":
		return cmdAction(rest, stdout, stderr, clock)
	case "show":
		return cmdShow(rest, stdout, stderr, clock)
	case "surface":
		return cmdSurface(rest, stdout, stderr, clock)
	case "resolume":
		return cmdResolume(rest, stdout, stderr, clock)
	case "render":
		return cmdRender(rest, stdout, stderr, clock)
	case "assets":
		return cmdAssets(rest, stdout, stderr, clock)
	case "version":
		return cmdVersion(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl: unknown command %q\n\n", cmd)
		printTopLevelUsage(stderr)
		return exitUsage
	}
}

// printTopLevelUsage documents every subcommand, every global flag, and
// the exit code table (task spec §3: "Document them in --help. A script
// wrapping this tool should not have to grep stderr."), so `showmeshctl
// help` alone is enough to use this tool without reading source.
func printTopLevelUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `showmeshctl is the non-UI client for a ShowMesh coordinator's public
control API. Reads and the audit log are gated by an authenticated
principal's scopes rather than by one shared secret; see --token below
and "showmeshctl session --help".

Step 7 gave this program its first writes; Step 8 gave it seven more, all
under "fpp". "config set", "discover", "declare" and "undeclare" change
this coordinator's own configuration and inventory and need the
config:write scope. Every "fpp <verb>" subcommand dispatches one of eight
primitive FPP commands and needs fpp:command; none of them ever reports
success on an HTTP 200 alone — evidence that observed state actually
moved is required before this program calls anything confirmed.

Step 9 gave this program "macro", "run" and "action": reading show.macro
and show.action definitions, and submitting a macro run. "macro run" needs
show:macro:run; every other new subcommand is a read (show:macro:run OR
config:write). A macro run is accepted asynchronously (202): "macro run"
and "run show" never wait for a run to finish unless given --follow, and
--follow itself times out on IDLE silence, never on total duration — see
"showmeshctl macro run --help".

Usage:
  showmeshctl <command> [flags] [args]

Commands:
  nodes                    list the node inventory
  node <id>                show one node in detail
  fpp [id]                 list configured FPP instances (or show one, if id given)
  fpp stop-playlist <id>              dispatch FPP's Stop Now and confirm by evidence (write)
  fpp start-playlist <id> <name>      dispatch FPP's Start Playlist and confirm by evidence (write)
  fpp stop-playlist-gracefully <id>   dispatch FPP's Stop Gracefully and confirm by evidence (write)
  fpp pause-playlist <id>             dispatch FPP's Pause Playlist and confirm by evidence (write)
  fpp resume-playlist <id>            dispatch FPP's Resume Playlist and confirm by evidence (write)
  fpp next-playlist-item <id>         dispatch FPP's Next Playlist Item and confirm by evidence (write)
  fpp prev-playlist-item <id>         dispatch FPP's Prev Playlist Item and confirm by evidence (write)
  fpp set-volume <id> <volume>        dispatch FPP's Volume Set and confirm by evidence (write)
  events                   show event history
  snapshot                 show the authoritative snapshot
  watch                    fetch the snapshot, then stream live changes
  session                  show the current principal, role, and effective scopes
  audit                    show the audit log (requires the audit:read scope)
  config                   read or write the fpp.endpoints configuration
                           ("config set" is a write, requires config:write)
  discover                 run discovery and print proposals (write)
  declare <id>             promote a node to declared, or update its label/notes (write)
  undeclare <id>           remove a node's declaration, requires --confirm (write)
  macro list                          enumerate show.macro objects
  macro show <id>                     show one macro's full definition
  macro run <id> [--follow]           submit a macro run (write, 202 accepted; asynchronous unless --follow)
  run show <runId> [--follow]         show one macro run, including every step's outcome
  run list [--macro <id>] [--state]   list macro runs, most recent first
  action list                         enumerate show.action objects
  action show <id>                    show one action's full definition
  show list                           enumerate show objects
  show get <id>                       show one show's full definition
  show set <id>                       write a new show revision (write, full replacement)
  show revisions <id>                 list show revision history, newest first
  show active                         print the currently active show (404 if none set)
  show activate <id>                  make <id> the active show (write, full replacement)
  surface list [--show <id>]          enumerate show.surface objects, optionally by show
  surface get <id>                    show one surface's full definition
  surface set <id>                    write a new surface revision (write, full replacement)
  surface revisions <id>              list surface revision history, newest first
  resolume composition upload <path>   parse and store a Resolume composition file (write)
  resolume composition show            show the stored composition (requires config:write)
  resolume action list                 show the Resolume action vocabulary this coordinator supports
  resolume action launch-clip <id>            launch (connect) a clip and confirm by evidence (write)
  resolume action clear-layer <id>            clear (disconnect) a layer and confirm by evidence (write)
  resolume action launch-column <id>          launch (connect) a column and confirm by evidence (write)
  resolume action select-deck <id>            select a deck and confirm by evidence (write)
  resolume action blackout                    disconnect every tracked layer and confirm by evidence (write)
  resolume action set-layer-bypass <id> <bool>   set a layer's bypass and confirm by evidence (write)
  resolume action set-layer-master <id> <value>  set a layer's master (continuous value) and confirm by evidence (write)
  assets list [--show <id>] [--node <id>] [--sequence <id>]
                           enumerate asset metadata
  assets get <assetId>    show one asset's full metadata
  assets upload            stream a file into the asset store (write, requires
                           asset:write; --show --sequence --media-type
                           --target-kind [--target] --file)
  assets fetch <assetId> --out <path>
                           download one asset's bytes, verifying the content
                           hash before the file lands at --out
  assets manifest [--node <id>] [--require-ready]
                           what each node should hold for the active show,
                           versus what it actually holds (Track E seam E5)
  resolume status [id]                 show the configured Resolume instance's health, loaded
                                        composition, and every resolume.* observation
  render settings get                  show the active render.settings configuration (Track B,
                                        ADR-039; never 404s — reports the built-in default)
  render settings set                  write a new render.settings revision (write, full
                                        replacement, requires config:write)
  render settings revisions            list render.settings revision history, newest first
  version                  show this CLI's and the coordinator's version and API negotiation
  help                     show this help

Global flags (place before any positional arguments):
  --server <url>    coordinator base URL (default http://localhost:8080,
                     or $SHOWMESH_SERVER)
  --token <token>   API bearer token: a token minted for a
                     principal by an admin (see "showmesh-coordinator
                     issue-token" on the coordinator host), sent as
                     "Authorization: Bearer <token>" and NEVER placed in a
                     URL or query string — the coordinator rejects any
                     request whose query string carries the token prefix
                     with 400 credential-in-url. Prefer $SHOWMESH_CTL_TOKEN
                     over this flag: a value passed on the command line is
                     visible to anyone on the same host who can read the
                     process table (ps, /proc), while an environment
                     variable is not. This is deliberately NOT
                     $SHOWMESH_API_TOKEN, an older shared-secret variable
                     this coordinator retired: a coordinator that still
                     sees that variable set refuses to start, so this CLI
                     uses a distinct name rather than colliding with a
                     variable an operator may still have exported for an
                     unrelated reason. Kind does not restrict credential
                     form: a human principal may mint a token and use it
                     here exactly as a machine principal would, and every
                     action taken this way is attributed to that human in
                     the audit log, not to a robot.
  --output text|json
                     output format (default text). json re-serializes this
                     CLI's OWN decoded structs, not the coordinator's raw
                     response bytes: the decoder tolerates unknown fields
                     by design (a newer coordinator will not break this
                     CLI), but that also means a field this build does not
                     know about is silently absent from --output json, not
                     merely unrendered as it would be in the text tables.
  --timeout <dur>   request timeout, e.g. 10s (default 10s; ignored by watch,
                     which is long-lived by design). Every "fpp <verb>"
                     write subcommand RAISES this to its own, larger
                     minimum (currently 35s) when given a smaller value:
                     the coordinator holds a dispatched command's response
                     open for its own confirmation deadline before
                     answering, so a shorter client budget could only ever
                     abort a healthy, still-working conversation and
                     report it as a transport failure. A too-small
                     --timeout on one of those subcommands prints a note
                     to stderr naming both values rather than silently
                     waiting longer than requested; see
                     "showmeshctl fpp <verb> --help". Every "macro"/"run"/
                     "action" subcommand RAISES --timeout to its OWN,
                     smaller minimum (currently 5s) on the same terms —
                     see "showmeshctl macro run --help" for why that floor
                     is much smaller than the fpp one (a macro run is
                     accepted asynchronously; nothing on this surface
                     holds a response open the way a dispatched fpp
                     command's confirmation wait does). --idle-timeout on
                     "macro run --follow"/"run show --follow" is a
                     SEPARATE, unrelated setting: it bounds how long that
                     follow loop goes without any update, never the
                     request budget for one HTTP call.

Run "showmeshctl <command> --help" for flags specific to one command.

Exit codes:
  0  success
  1  usage error (bad flags, bad arguments, unknown command)
  2  coordinator unreachable (connection refused, DNS failure, timeout)
  3  unauthorized (401: no valid credential was presented)
  4  API version incompatible (the coordinator does not serve a version
     this CLI supports)
  5  not found (404 from the coordinator)
  6  the coordinator returned some other error (see stderr for the
     RFC 9457 problem detail), OR (Step 9) "macro run"/"run show" read
     back a FINISHED run that did not report whether it completed or
     confirmed at all — this program never guesses at an outcome the
     coordinator's own response left out, and never reports it as either
     a success or as exit 9/12's specific, named failures
  7  forbidden (403: authenticated, but missing a required scope — see
     stderr for the scope name; distinct from 3, which means no credential
     authenticated at all)
  8  rate limited (429: the login concurrency bound was exceeded — see
     stderr for how long to wait before retrying)
  9  command unconfirmed. Two different sources share this one code on
     purpose: any "fpp <verb>" write subcommand (the request itself
     succeeded and the coordinator answered honestly that the command's
     effect was not confirmed by evidence), and (Step 9) "macro run"/
     "run show" reading back a FINISHED run whose completed=true but
     confirmed=false — every step dispatched and none aborted, but at
     least one produced no confirming evidence (an MQTT step that
     declares no expected response reports this on every correct run, by
     design). Never conflated with exit 6, which means the request itself
     failed; distinct from exit 15 below, which is a run that did NOT
     complete
  10 conflict (409: the request was valid, but this coordinator's current
     state makes it unsafe or meaningless to act on right now — a
     different playlist is playing and ifBusy=refuse, the evidence needed
     to decide that is not current, an idempotency key was reused against
     a different action/target/params, or (Step 9) a macro run's
     idempotency key was reused against a different macro/revision, or the
     same macro already has a run in flight; see stderr for the specific
     reason and remedy. Distinct from exit 6: the coordinator is healthy
     and answered correctly, it declined on purpose)
  11 action unconfirmable (a "resolume action <verb>" subcommand only:
     the action was dispatched, but its own effect could not be told
     apart from its state before dispatch, so the coordinator cannot say
     whether it did anything — never conflated with exit 0, which means
     the effect WAS observed, or exit 9, which means a deadline expired
     with no evidence either way)
  12 action failed (a "resolume action <verb>" subcommand only: dispatch
     was attempted and the attempt itself failed — distinct from exit 6,
     which means this program's own request to the coordinator failed;
     here the coordinator answered normally and reported, honestly, that
     its own attempt to reach Resolume did not work)
  13 action refused (a "resolume action <verb>" subcommand only: the
     coordinator answered "refused" — no HTTP request ever reached
     Resolume, see stderr for why. Distinct from exit 10: this is not an
     idempotency-key conflict, and minting a fresh key will not help)
  14 still watching (Step 9: "macro run --follow"/"run show --follow"
     stopped watching because its --idle-timeout elapsed with no run
     update at all — never because a total duration was exceeded. This is
     NOT a reported failure: the request itself succeeded and the run may
     still be in progress, or may already have finished: check with
     "showmeshctl run show <runId>". It is still a NON-ZERO exit, though:
     a shell "&&" chain, or a script running under "set -e", treats this
     exactly like any other failure and stops there — a caller that wants
     to keep going after "still watching" must check for 14 explicitly
     rather than relying on "&&"/"set -e" alone)
  15 macro run aborted (Step 9: a macro run reached its terminal state
     with completed=false — a step failed and the remainder was not
     dispatched, or a step's target was removed mid-run; see stderr/the
     run's own "reason" for which step and why. Distinct from exit 9,
     which this program still uses for a run that completed but did not
     confirm — "completed" and "confirmed" are two separate facts and
     this CLI's exit codes keep them separate too. A FINISHED run whose
     coordinator response did not even report completed/confirmed at all
     is neither of these: see exit 6)
  20 assets not ready ("assets manifest --require-ready" only: at least one
     node is not_ready — a fresh, complete inventory report is MISSING a
     named asset. "I checked, and it is missing.")
  21 assets unknown ("assets manifest --require-ready" only: at least one
     node is unknown, and none is not_ready — no report has ever arrived,
     the last one is stale, it said complete:false, or no active show is
     configured. "I cannot tell." Deliberately distinct from exit 20: a
     script that collapses "checked and missing" into "cannot tell", or the
     reverse, will either start a show it should not or block one it
     should not)
  22 render unavailable ("render status" only: this node has never
     published a render report at all — distinct from a node that HAS
     reported and is simply stale/unknown/failed, which prints normally
     and exits 0)
  23 render pipeline down ("render apply"/"render clear"/"render restart"
     only: the confirmation wait ended with DIRECT evidence the surface's
     pipeline is in its "failed" state — distinct from exit 9, which
     covers every other unconfirmed case, including a deadline that
     simply elapsed with no evidence either way)
`)
}

// globalFlags are the flags every subcommand accepts, per task spec §2.
type globalFlags struct {
	server  string
	token   string
	output  string
	timeout time.Duration
}

// outputText and outputJSON are the only values --output accepts.
const (
	outputText = "text"
	outputJSON = "json"
)

// newFlagSet builds a flag.FlagSet pre-registered with the global flags,
// shared by every subcommand so --server/--token/--output/--timeout behave
// identically everywhere. flag.ContinueOnError (rather than the default
// flag.ExitOnError) is required for this to be testable at all: the
// default calls os.Exit from inside Parse, which a unit test cannot
// observe or recover from.
func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *globalFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	g := &globalFlags{}
	fs.StringVar(&g.server, "server", envOr("SHOWMESH_SERVER", "http://localhost:8080"), "coordinator base URL")
	// SHOWMESH_CTL_TOKEN, deliberately not SHOWMESH_API_TOKEN: that name is
	// the ADR-021 shared secret ADR-024 decision 2 retired, and a
	// coordinator that still sees it set refuses to start by design. An
	// operator who exports SHOWMESH_API_TOKEN to use this CLI would
	// otherwise be unable to start a coordinator from the same shell, and
	// the resulting refusal names a migration that has nothing to do with
	// what they set. This flag intentionally does NOT fall back to the old
	// name — a fallback would recreate the exact collision this rename
	// exists to remove.
	fs.StringVar(&g.token, "token", os.Getenv("SHOWMESH_CTL_TOKEN"), "API bearer token")
	fs.StringVar(&g.output, "output", outputText, "output format: text|json")
	fs.DurationVar(&g.timeout, "timeout", 10*time.Second, "request timeout")
	return fs, g
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// flagParseExit turns a non-nil flag.FlagSet.Parse error into this
// program's exit code convention: -h/--help is success (flag.ErrHelp),
// anything else is a usage error. Parse itself has already written its
// message to stderr via fs.SetOutput, so callers do not print anything
// more. Every call site is of the form:
//
//	if err := fs.Parse(args); err != nil {
//	    return flagParseExit(err)
//	}
func flagParseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

// validateOutput rejects an --output value other than text/json as a
// usage error, before any request is attempted.
func validateOutput(g *globalFlags) error {
	if g.output != outputText && g.output != outputJSON {
		return newCLIError(exitUsage, "invalid --output value %q: must be text or json", g.output)
	}
	return nil
}

// newRequestClient builds the client used by every one-shot (non-watch)
// subcommand: an http.Client whose Timeout is the --timeout flag, so a
// hung coordinator cannot hang the CLI indefinitely.
func newRequestClient(g *globalFlags) (*client, error) {
	return newClient(g.server, g.token, &http.Client{Timeout: g.timeout})
}

// reportError writes err to stderr in a form appropriate to its type and
// returns the exit code to use. A *cliError already carries the right
// code; anything else (a bug in this program, not a modeled failure) is
// reported as exitAPIError so it is at least visible and non-zero.
func reportError(stderr io.Writer, cmdName string, err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: %s\n", cmdName, ce.Error())
		return ce.code
	}
	_, _ = fmt.Fprintf(stderr, "showmeshctl %s: %v\n", cmdName, err)
	return exitAPIError
}
