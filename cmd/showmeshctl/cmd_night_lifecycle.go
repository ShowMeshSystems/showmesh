package main

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	raw, err := c.getJSONKeepingRaw(ctx, "/api/v1/night/session", nil, &resp)
	if err != nil {
		return reportError(stderr, "night status", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSONBody(stdout, raw); err != nil {
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

// minNightPrepareSiteStopClientTimeout is the budget for prepare-site with
// --stop-fpp-playback: the coordinator waits one FPP confirmation deadline
// for the stop, after the announcement reset pass prepare-site also runs
// (30s), so --timeout's 10s default would abort before an unconfirmed stop
// could be reported. Derived from minFPPCommandClientTimeout.
const minNightPrepareSiteStopClientTimeout = minFPPCommandClientTimeout + 30*time.Second

func nightLifecycleCommand(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, label, command string) int {
	return nightLifecycleCommandWithBody(stdout, stderr, clock, g, label, command, map[string]any{})
}

// nightLifecycleCommandWithBody is [nightLifecycleCommand] with an
// explicit request body, for [runGatedNightLifecycleCommand]'s own
// --override support (Track F seam F6).
func nightLifecycleCommandWithBody(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, label, command string, body map[string]any) int {
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, label, err)
	}
	timeout := g.timeout
	if command == "run-readiness" && timeout < minNightReadinessClientTimeout {
		timeout = minNightReadinessClientTimeout
	}
	if command == "prepare-site" && body["stopFppPlayback"] == true && timeout < minNightPrepareSiteStopClientTimeout {
		timeout = minNightPrepareSiteStopClientTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var resp nightCommandResponseWire
	if err := c.postJSON(ctx, "/api/v1/night/commands/"+command, body, &resp); err != nil {
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
	if resp.Command.Reason != "" {
		_, _ = fmt.Fprintln(stdout, resp.Command.Reason)
	}
	printNightSessionStateDetail(stdout, resp.Session)
	return exitOK
}

// runGatedNightLifecycleCommand is [runSimpleNightLifecycleCommand] plus
// a repeatable --override RULE=REASON flag (Track F seam F6), for every
// "night <verb>" command whose own phase a configured "block" interlock
// can withhold: prepare-site, run-readiness, start-preshow, start-night,
// fade-out-night, and power-down-presentation. request-final-show and
// end-session consult no interlock and keep using
// [runSimpleNightLifecycleCommand].
func runGatedNightLifecycleCommand(args []string, stdout, stderr io.Writer, clock func() time.Time, label, command, help string) int {
	return runGatedNightLifecycleCommandStopping(args, stdout, stderr, clock, label, command, help, false)
}

// runGatedNightLifecycleCommandStopping adds --stop-fpp-playback, which
// only prepare-site honors.
func runGatedNightLifecycleCommandStopping(args []string, stdout, stderr io.Writer, clock func() time.Time, label, command, help string, offerStopFPP bool) int {
	fs, g := newFlagSet("showmeshctl "+label, stderr)
	var stopFPP *bool
	if offerStopFPP {
		stopFPP = fs.Bool("stop-fpp-playback", false, "also stop whatever the night's FPP players are playing")
	}
	var overrides []nightCommandOverrideWire
	fs.Var(nightOverrideFlag{overrides: &overrides}, "override",
		"override one blocking safety rule: RULE=REASON (repeatable, needs night:override)")
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
	body := map[string]any{}
	if len(overrides) > 0 {
		body["interlockOverrides"] = overrides
	}
	if stopFPP != nil && *stopFPP {
		body["stopFppPlayback"] = true
	}
	return nightLifecycleCommandWithBody(stdout, stderr, clock, g, label, command, body)
}

func cmdNightPrepareSite(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommandStopping(args, stdout, stderr, clock, "night prepare-site", "prepare-site",
		"Start preparing the site for tonight. Safe to repeat.\nFPP keeps playing unless --stop-fpp-playback is given.\nA blocking safety rule can refuse this; see --override.", true)
}

func cmdNightReadiness(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night readiness", "run-readiness",
		"Check that the site is ready for tonight and list every check.\nRefused until the site is being prepared. Read each check before starting.\nA blocking safety rule can refuse this; see --override.")
}

func cmdNightPreshow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night preshow", "start-preshow",
		"Start the pre-show. Refused until the site is being prepared.\nA blocking safety rule can refuse this; see --override.")
}

// nightOverrideFlag is flag.Value for a repeatable "--override
// rule=reason" flag (Track F seam F6): each occurrence names one
// configured "block" interlock rule to override, plus the required
// reason ADR-024/RESTING-MODE.md §10.1 puts in the audit entry. Honored
// only against a rule that declares overridePolicy "authorized-operator"
// and only by a caller holding night:override; see
// internal/coordinator/api/nightinterlock.go's own doc comment for the
// full authorization chain this flag's contents feed into.
type nightOverrideFlag struct {
	overrides *[]nightCommandOverrideWire
}

func (f nightOverrideFlag) String() string { return "" }

func (f nightOverrideFlag) Set(s string) error {
	rule, reason, ok := strings.Cut(s, "=")
	if !ok || rule == "" || reason == "" {
		return fmt.Errorf("--override must be RULE=REASON, got %q", s)
	}
	*f.overrides = append(*f.overrides, nightCommandOverrideWire{Rule: rule, Reason: reason})
	return nil
}

func cmdNightStart(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night start", "start-night",
		"Start the night. Needs a readiness check from this preparation.\nAn old check is run again first, and a failed one refuses the start.\nA blocking safety rule can refuse this; see --override.")
}

func cmdNightFinalShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night final-show", "request-final-show",
		"Play one more complete show, then admit no more.\nWorks while the night is degraded.")
}

func cmdNightFadeOut(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night fade-out", "fade-out-night",
		"Fade the presentation out and stop FPP. During a show the fade is deferred\nuntil that show ends. The night reads \"fading-out\", then \"stopped\" once\nFPP is seen idle.\nWorks while the night is degraded.\nA blocking safety rule can refuse this; see --override.")
}

func cmdNightPowerDown(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night power-down", "power-down-presentation",
		"Fade out if needed, stop FPP, then run the configured power-off step.\nWorks while the night is degraded.\nA blocking safety rule can refuse this; see --override.")
}

func cmdNightEndSession(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night end-session", "end-session",
		"Abandons the current night without stopping FPP or powering anything off.\nUse fade-out-night or power-down-presentation first when either works.\nRun \"night prepare-site\" afterward to recover a degraded night.\n\nThese still work while the night is degraded:\n  request-final-show        admit no more after one more show\n  fade-out-night            fade out and stop FPP\n  power-down-presentation   fade out, stop FPP, then power off\n  end-session               abandon the night\nThe first three end the night in order. Only end-session abandons it.")
}

func cmdNightResumeShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night resume-show", "resume-show",
		"After Stop holds the night, start the show playlist from its first song.\nRefused during a weather delay, and before or after the night's shows.")
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
