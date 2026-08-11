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
control API (ADR-014). It is read-only: there is no write or command
subcommand, matching the API it talks to.

Usage:
  showmeshctl <command> [flags] [args]

Commands:
  nodes             list the node inventory
  node <id>         show one node in detail
  fpp [id]          list configured FPP instances (or show one, if id given)
  events            show event history
  snapshot          show the authoritative snapshot
  watch             fetch the snapshot, then stream live changes
  version           show this CLI's and the coordinator's version and API negotiation
  help              show this help

Global flags (place before any positional arguments):
  --server <url>    coordinator base URL (default http://localhost:8080,
                     or $SHOWMESH_SERVER)
  --token <token>   API bearer token. Prefer $SHOWMESH_API_TOKEN over this
                     flag: a value passed on the command line is visible to
                     anyone on the same host who can read the process table
                     (ps, /proc), while an environment variable is not.
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
                     which is long-lived by design)

Run "showmeshctl <command> --help" for flags specific to one command.

Exit codes:
  0  success
  1  usage error (bad flags, bad arguments, unknown command)
  2  coordinator unreachable (connection refused, DNS failure, timeout)
  3  unauthorized (401 from the coordinator)
  4  API version incompatible (the coordinator does not serve a version
     this CLI supports)
  5  not found (404 from the coordinator)
  6  the coordinator returned some other error (see stderr for the
     RFC 9457 problem detail)
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
	fs.StringVar(&g.token, "token", os.Getenv("SHOWMESH_API_TOKEN"), "API bearer token")
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
