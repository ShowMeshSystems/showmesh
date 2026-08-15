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
	case "resolume":
		return cmdResolume(rest, stdout, stderr, clock)
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
control API (ADR-014). Since ADR-024, reads and the audit log are gated
by an authenticated principal's scopes rather than by one shared secret,
see --token below and "showmeshctl session --help".

Step 7 gave this program its first writes; Step 8 gave it seven more, all
under "fpp". "config set", "discover", "declare" and "undeclare" change
this coordinator's own configuration and inventory and need the
config:write scope. Every "fpp <verb>" subcommand dispatches one of
docs/bench/fpp-command-vocabulary.md section 4's eight primitive FPP
commands (ADR-001) and needs fpp:command; none of them ever reports
success on an HTTP 200 alone, because ADR-003 requires evidence that
observed state actually moved.

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
  resolume composition upload <path>   parse and store a Resolume composition file (write)
  resolume composition show            show the stored composition (requires config:write)
  version                  show this CLI's and the coordinator's version and API negotiation
  help                     show this help

Global flags (place before any positional arguments):
  --server <url>    coordinator base URL (default http://localhost:8080,
                     or $SHOWMESH_SERVER)
  --token <token>   API bearer token (ADR-024): a token minted for a
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
                     $SHOWMESH_API_TOKEN, the ADR-021 shared secret ADR-024
                     retired: a coordinator that still sees that variable
                     set refuses to start, so this CLI uses a distinct
                     name rather than colliding with a variable an
                     operator may still have exported for an unrelated
                     reason. Kind does not restrict credential form: a
                     human principal may mint a token and use it here
                     exactly as a machine principal would, and every
                     action taken this way is attributed to that human in
                     the audit log, not to a robot.
  --output text|json
                     output format (default text). json re-serializes this
                     CLI's OWN decoded structs, not the coordinator's raw
                     response bytes: the decoder tolerates unknown fields
                     per contract section 6.2 (a newer coordinator will not
                     break this CLI), but that also means a field this
                     build does not know about is silently absent from
                     --output json, not merely unrendered as it would be in
                     the text tables.
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
                     "showmeshctl fpp <verb> --help".

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
     RFC 9457 problem detail)
  7  forbidden (403: authenticated, but missing a required scope — see
     stderr for the scope name; distinct from 3, which means no credential
     authenticated at all)
  8  rate limited (429: the login concurrency bound was exceeded — see
     stderr for how long to wait before retrying)
  9  command unconfirmed (any "fpp <verb>" write subcommand, ADR-003: the
     request itself succeeded and the coordinator answered honestly that
     the command's effect was not confirmed by evidence — never conflated
     with exit 6, which means the request itself failed)
  10 conflict (409: the request was valid, but this coordinator's current
     state makes it unsafe or meaningless to act on right now — a
     different playlist is playing and ifBusy=refuse, the evidence needed
     to decide that is not current, or an idempotency key was reused
     against a different action/target/params; see stderr for the
     specific reason and remedy. Distinct from exit 6: the coordinator is
     healthy and answered correctly, it declined on purpose)
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
