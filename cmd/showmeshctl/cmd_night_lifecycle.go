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
	fs, g := newFlagSet("showmeshctl "+label, stderr)
	var overrides []nightCommandOverrideWire
	fs.Var(nightOverrideFlag{overrides: &overrides}, "override",
		"override one withholding interlock rule: RULE=REASON (repeatable; requires night:override and that rule's own overridePolicy: authorized-operator)")
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
	return nightLifecycleCommandWithBody(stdout, stderr, clock, g, label, command, body)
}

func cmdNightPrepareSite(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night prepare-site", "prepare-site",
		"Open a new preparation epoch (POST /api/v1/night/commands/prepare-site,\nrequires night:command). Idempotent within the same preparation or\nactive session; rejected during finalization or fade-out.\n\nA configured \"block\" interlock for phase prepare-site is dispatched LIVE,\nat the instant this command runs, and can refuse it (409) unless covered\nby --override.")
}

func cmdNightReadiness(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night readiness", "run-readiness",
		"Run readiness for the current preparation epoch (POST\n/api/v1/night/commands/run-readiness). Rejected when no preparation\nepoch is open.\n\nThis build checks FPP reachability for the session's own referenced FPP\ninstances (\"fpp:<id>:reachable\"), the pinned resting FSEQ asset's own\nparseable non-zero duration (\"resting:asset-duration\"), the resting\nplaylist's idle-read shape (\"resting:playlist-shape:<playlist>\"), the\nshow playlist's presence (\"show:playlist-present:<playlist>\"), and every\nconfigured interlock's own current outcome (\"interlock:<phase>:<name>\"),\ndispatched live at run-readiness time. \"resting:asset-exact-variant:<playlist>\"\nis PERMANENTLY \"not_verifiable\": FPP exposes no content hash, so this\ncannot confirm the live host is running the pinned asset's exact bytes,\nstated rather than defaulted to a pass, but excluded from \"outcome\" (it\nstays listed), so \"ready\" is still reachable once every checkable check\npasses. Neither that nor a plain \"unknown\" outcome blocks start-night by\nitself; only a missing or stale readiness result does. Read every check\nname and reason before trusting this as a complete pre-flight.\n\nA configured \"block\" interlock for phase run-readiness is dispatched\nLIVE, at the instant this command runs, and can refuse the command itself\n(409, nothing computed or stored) unless covered by --override; a rule\ndeclared for any OTHER phase is still evaluated and shown above, never\nwithholding this command.")
}

func cmdNightPreshow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night preshow", "start-preshow",
		"Enter the configured pre-show presentation (POST\n/api/v1/night/commands/start-preshow). Requires the current preparation\nepoch.\n\nA configured \"block\" interlock for phase start-preshow is gated against\nthe most recent readiness result (at most as fresh as the last\nrun-readiness call, never a live dispatch) and can refuse this command\n(409) unless covered by --override.")
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
		"Authorize the night session and begin the first transition (POST\n/api/v1/night/commands/start-night). Requires a completed readiness\nresult from the SAME preparation epoch, within the coordinator's\nconfigured maximum age.\n\nA configured \"block\" interlock for phase start-night is gated against\nthat same readiness result (never a live dispatch here) and can refuse\nthis command (409) unless covered by --override.")
}

func cmdNightFinalShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night final-show", "request-final-show",
		"Close admission after one final complete show (POST\n/api/v1/night/commands/request-final-show). Never starts a second show\nafter the final playlist.\n\nAccepted against a DEGRADED session: it is directional, and refusing it\nwould leave the operator less able to end the night than if this\ncoordinator were switched off. See \"night end-session\" for the full list\nof four.")
}

func cmdNightFadeOut(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night fade-out", "fade-out-night",
		"Fade the active non-live presentation out (POST\n/api/v1/night/commands/fade-out-night). Closes admission immediately and\ncancels any armed, uncommitted next show; never refused for want of an\naudit write, and never gated on this session's own degraded flag.\n\nThe session enters \"fading-out\", not \"stopped\": it runs the configured\nfade cues, issues a real stop to FPP, and reports \"stopped\" only once it\nhas observed FPP idle with no playlist after that stop. An unconfirmed\nstop degrades the session rather than claiming success. During a live or\nalready-committed show the fade is deferred until that show finishes, and\nthe end-of-night resting playlist is then not started.\n\nA configured \"block\" interlock for phase fade-out-night is dispatched\nLIVE, at the instant this command runs, and CAN refuse this exact\ncommand (409) unless covered by --override; a rule with an unavailable\nsource and onUnavailable: block still refuses this way, but every such\nrule now requires overridePolicy: authorized-operator (a plain\noverridePolicy: none is refused at PUT /config/night.session/{id} time),\nso an authorized override always exists. \"night end-session\" also\nconsults no interlock and always reaches \"stopped\", but only fade-out-night\nand power-down-presentation actually issue FPP's own stop.\n\nAccepted against a DEGRADED session: see \"night end-session\" for the full\nlist of four.")
}

func cmdNightPowerDown(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runGatedNightLifecycleCommand(args, stdout, stderr, clock, "night power-down", "power-down-presentation",
		"Close the session after playback and the fade have stopped (POST\n/api/v1/night/commands/power-down-presentation). Implies fade-out-night\nif it has not already occurred, so the session enters \"fading-out\" and\nreaches \"stopped\" only on observed idle evidence. With no power\nconfiguration the optional power phase records \"not_configured\" at that\npoint, without error; with siteControl.presentationPowerOff configured but\nnot yet automatically dispatched by this build, it records\n\"configured_not_dispatched\" instead.\n\nA configured \"block\" interlock for phase power-down-presentation is\ndispatched LIVE, at the instant this command runs, and CAN refuse this\nexact command (409) unless covered by --override, the identical\nguarantee \"night fade-out\" describes for its own phase. \"night\nend-session\" also consults no interlock and always reaches \"stopped\",\nbut only fade-out-night and power-down-presentation actually issue\nFPP's own stop.\n\nAccepted against a DEGRADED session: see \"night end-session\" for the full\nlist of four.")
}

func cmdNightEndSession(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	return runSimpleNightLifecycleCommand(args, stdout, stderr, clock, "night end-session", "end-session",
		"PROVISIONAL operator recovery action (POST /api/v1/night/commands/\nend-session): abandons the current session, reaches \"stopped\", and\nlaunches nothing. Does not clear the degraded record; recover with\n\"night prepare-site\" afterward.\n\nFour commands are accepted against a DEGRADED session:\n\n  request-final-show        close admission after one more show\n  fade-out-night            fade the presentation out and stop FPP\n  power-down-presentation   the above, plus any configured power phase\n  end-session               abandon the session outright\n\nThe first three are directional safety and shutdown actions and are never\nrefused for want of this coordinator's own evidence. end-session is the\nonly one that abandons the session rather than ending the night through\nit. Every other lifecycle command refuses while degraded.\n\nend-session declares no interlock phase and consults no gate at all: it\nis unconditionally the way to reach \"stopped\" even when a configured\n\"block\" interlock on fade-out-night or power-down-presentation has no\noverride path (overridePolicy: none) and an unavailable source. It never\nissues an FPP stop or resolves the power phase, so use fade-out-night or\npower-down-presentation first whenever either one is reachable.")
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
