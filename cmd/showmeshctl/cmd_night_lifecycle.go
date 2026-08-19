package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// The night-session LIFECYCLE surface: "night status", the seven ADR-038
// commands, and end-session — distinct from cmd_night.go's config-kind
// verbs one file over. No UI exists for this surface; the CLI ships with
// the API (ADR-039).
//
// Every write is POST /api/v1/night/commands/{command}, behind
// night:command, answering 202. A refusal maps to exitNightNotReady (26),
// exitNightStateRejected (27), or exitNightAmbiguous (28).

func cmdNightLifecycleStatus(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl night status", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl night status [flags]")
		_, _ = fmt.Fprintln(stderr, "\nPrint the current night session (GET /api/v1/night/session). Open read,")
		_, _ = fmt.Fprintln(stderr, "no credential required unless this coordinator has closed reads.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "night status", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "night status", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nightSessionLifecycleResponse
	if err := c.getJSON(ctx, "/api/v1/night/session", nil, &resp); err != nil {
		return reportError(stderr, "night status", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "night status", err)
		}
		return exitOK
	}
	printNightSessionStateDetail(stdout, resp.Session)
	return exitOK
}

// nightLifecycleCommand is the shared body of every "night <verb>" write
// subcommand below: dispatch, print, and map a refusal onto its own exit
// code — one function so the eight thin wrappers cannot drift from each
// other's request/response/error handling.
// minNightReadinessClientTimeout overrides --timeout's global default
// (10s) for "run-readiness" only: the server's own worst case is two
// sequential FPP playlist reads at up to 5s each (nightPlaylistReadTimeout,
// internal/coordinator/api/nightasset.go) plus lighter work, already at or
// past 10s, so the client must not abort before the server can answer.
// 15s is that budget plus headroom — the same reconciliation
// minFPPCommandClientTimeout uses one file over (cmd_fpp_command.go),
// including its caveat: a hand-copied literal, not derived, so it can only
// drift stale, never silently disagree with an import.
const minNightReadinessClientTimeout = 15 * time.Second

func nightLifecycleCommand(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, label, command string) int {
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, label, err)
	}
	timeout := g.timeout
	if command == "run-readiness" && timeout < minNightReadinessClientTimeout {
		timeout = minNightReadinessClientTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var resp nightCommandResponseWire
	if err := c.postJSON(ctx, "/api/v1/night/commands/"+command, map[string]any{}, &resp); err != nil {
		return reportError(stderr, label, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, label, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "%s: %s\n", command, resp.Command.Outcome)
	printNightSessionStateDetail(stdout, resp.Session)
	return exitOK
}

func cmdNightPrepareSite(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night prepare-site", "prepare-site",
		"Open a new preparation epoch (POST /api/v1/night/commands/prepare-site,\nrequires night:command). Idempotent within the same preparation or\nactive session; rejected during finalization or fade-out.")
}

func cmdNightReadiness(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night readiness", "run-readiness",
		"Run readiness for the current preparation epoch (POST\n/api/v1/night/commands/run-readiness). Rejected when no preparation\nepoch is open.\n\nThis build checks FPP reachability for the session's own referenced FPP\ninstances (\"fpp:<id>:reachable\"), the pinned resting FSEQ asset's own\nparseable non-zero duration (\"resting:asset-duration\"), the resting\nplaylist's idle-read shape (\"resting:playlist-shape:<playlist>\"), and the\nshow playlist's presence (\"show:playlist-present:<playlist>\") — never\naudio readiness or any interlock. \"resting:asset-exact-variant:<playlist>\"\nis PERMANENTLY \"not_verifiable\": FPP exposes no content hash, so this\ncannot confirm the live host is running the pinned asset's exact bytes —\nstated rather than defaulted to a pass, but excluded from \"outcome\" (it\nstays listed), so \"ready\" is still reachable once every checkable check\npasses. Neither that nor a plain \"unknown\" outcome blocks start-night by\nitself; only a missing or stale readiness result does. Read every check\nname and reason before trusting this as a complete pre-flight.")
}

func cmdNightPreshow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night preshow", "start-preshow",
		"Enter the configured pre-show presentation (POST\n/api/v1/night/commands/start-preshow). Requires the current preparation\nepoch.")
}

func cmdNightStart(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night start", "start-night",
		"Authorize the night session and begin the first transition (POST\n/api/v1/night/commands/start-night). Requires a completed readiness\nresult from the SAME preparation epoch, within the coordinator's\nconfigured maximum age.")
}

func cmdNightFinalShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night final-show", "request-final-show",
		"Close admission after one final complete show (POST\n/api/v1/night/commands/request-final-show). Never starts a second show\nafter the final playlist.")
}

func cmdNightFadeOut(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night fade-out", "fade-out-night",
		"Fade the active non-live presentation to stopped (POST\n/api/v1/night/commands/fade-out-night). Closes admission immediately and\ncancels any armed, uncommitted next show; never refused for want of an\naudit write.")
}

func cmdNightPowerDown(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night power-down", "power-down-presentation",
		"Close the session after playback and the fade have stopped (POST\n/api/v1/night/commands/power-down-presentation). Implies fade-out-night\nif it has not already occurred; with no power configuration this reaches\n\"stopped\" without error.")
}

func cmdNightEndSession(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night end-session", "end-session",
		"PROVISIONAL operator recovery action (POST /api/v1/night/commands/\nend-session): abandons the current session, reaches \"stopped\", and\nlaunches nothing. The only command that runs against a DEGRADED session\n(the seven ADR-038 commands all refuse). Does not clear the degraded\nrecord; recover with \"night prepare-site\" afterward.")
}

// runSimpleNightLifecycleCommand is the flag-parsing wrapper every
// no-argument "night <verb>" write subcommand above shares.
func runSimpleNightLifecycleCommand(args []string, stdout, stderr io.Writer, clock func() time.Time, label, command, help string) int {
	fs, g := newFlagSet("showmeshctl "+label, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s [flags]\n\n%s\n", label, help)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, label, err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	return nightLifecycleCommand(stdout, stderr, clock, g, label, command)
}
