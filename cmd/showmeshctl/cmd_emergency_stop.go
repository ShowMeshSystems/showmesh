package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// This file is showmeshctl's emergency-stop surface:
// "emergency-stop stop", "emergency-stop stop-power-down", and
// "emergency-stop hard-stop arm"/"emergency-stop hard-stop fire".
//
// THE NO-CHAINING RULE: "hard-stop" has no bare form and no flag that
// arms and fires in one invocation. It has exactly two subcommands, arm
// and fire, and nothing calls one from the other. That is the whole
// deliberate-intent gate on this CLI: the coordinator's own arm/fire
// contract (internal/coordinator/api/emergencystop.go) is what makes a
// retry or a redelivered command safe, but a convenience flag here that
// silently chained arm then fire would make this CLI's own gate
// ornamental while still looking present. Do not add one, however often
// asked. See that file's own doc comment for the same rule stated from
// the server side.
//
// Every result's exit code is driven by StopOutcomes ALONE. A follow-up
// action's own outcome is printed, but never changes the exit code, so a
// worklight that failed to turn on can never be misread as "the stop did
// not happen" (this build's own degrade-safely rule).

func cmdEmergencyStop(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printEmergencyStopUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printEmergencyStopUsage(stdout)
		return exitOK
	case "stop":
		return cmdEmergencyStopLevel(rest, stdout, stderr, clock, "showmeshctl emergency-stop stop", "/api/v1/emergency-stop/stop")
	case "stop-power-down":
		return cmdEmergencyStopLevel(rest, stdout, stderr, clock, "showmeshctl emergency-stop stop-power-down", "/api/v1/emergency-stop/stop-power-down")
	case "hard-stop":
		return cmdEmergencyStopHardStop(rest, stdout, stderr, clock)
	case "config":
		return cmdEmergencyStopConfig(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl emergency-stop: unknown subcommand %q\n\n", sub)
		printEmergencyStopUsage(stderr)
		return exitUsage
	}
}

func printEmergencyStopUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl emergency-stop <subcommand> [flags]

Stop playout immediately, at one of three levels, each with its own
optional follow-up actions (configured with "showmeshctl config set
show.emergencystop", requires config:write). Every subcommand requires
show:emergencystop:invoke.

  stop              Stop playout immediately.
  stop-power-down   Stop playout immediately, then force the active night
                     session's own standard graceful shutdown to start now.
  hard-stop arm     Arm the hard stop: mints a single-use token, valid a
                     short time, with no effect on the show by itself.
  hard-stop fire    Fire the hard stop using --arm-token from "hard-stop arm":
                     stop playout immediately, abandon the active night
                     session straight to stopped with no wait, and run
                     hard-stop's own follow-ups. There is no single
                     command that arms and fires. See this file's own
                     doc comment for why.
  config get|set|revisions
                     Read or write each level's own optional follow-up
                     action list (config:write). See
                     cmd_emergency_stop_config.go.

Every level's own exit code reflects the STOP alone: 0 confirmed, 9
unconfirmed, 12 failed, 13 refused, taken as the worst outcome across
every configured FPP instance. A follow-up action's own outcome is always
printed but never changes the exit code.
`)
}

// emergencyStopInstanceOutcome mirrors
// v1.EmergencyStopInstanceOutcome field for field. This program's own
// independent transcription, never a shared struct with the coordinator.
type emergencyStopInstanceOutcome struct {
	InstanceID    string  `json:"instanceId"`
	Outcome       string  `json:"outcome"`
	OutcomeReason string  `json:"outcomeReason"`
	DispatchedAt  *string `json:"dispatchedAt"`
	Replay        bool    `json:"replay"`
}

type emergencyStopFollowUpResult struct {
	ActionID      string `json:"actionId"`
	Label         string `json:"label"`
	Outcome       string `json:"outcome"`
	OutcomeReason string `json:"outcomeReason"`
}

type emergencyStopNightSessionOutcome struct {
	Present   bool   `json:"present"`
	SessionID string `json:"sessionId"`
	Outcome   string `json:"outcome"`
	Error     string `json:"error"`
}

type emergencyStopResult struct {
	Level                 string                            `json:"level"`
	IdempotencyKey        string                            `json:"idempotencyKey"`
	StopOutcomes          []emergencyStopInstanceOutcome    `json:"stopOutcomes"`
	NoInstancesConfigured bool                              `json:"noInstancesConfigured"`
	NightSession          *emergencyStopNightSessionOutcome `json:"nightSession"`
	FollowUps             []emergencyStopFollowUpResult     `json:"followUps"`
	FollowUpConfigError   string                            `json:"followUpConfigError"`
}

type emergencyStopResponse struct {
	ServerTime time.Time           `json:"serverTime"`
	Result     emergencyStopResult `json:"result"`
}

type emergencyStopArmResponse struct {
	ServerTime time.Time `json:"serverTime"`
	ArmToken   string    `json:"armToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// cmdEmergencyStopLevel is "stop" and "stop-power-down"'s shared body:
// both send {idempotencyKey} and report the identical result shape.
func cmdEmergencyStopLevel(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, apiPath string) int {
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp emergencyStopResponse
	if err := c.postJSON(ctx, apiPath, map[string]string{"idempotencyKey": key}, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForEmergencyStopResult(resp.Result)
	}
	return reportEmergencyStopResult(stdout, cmdLabel, resp.Result)
}

func cmdEmergencyStopHardStop(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printEmergencyStopHardStopUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printEmergencyStopHardStopUsage(stdout)
		return exitOK
	case "arm":
		return cmdEmergencyStopHardStopArm(rest, stdout, stderr, clock)
	case "fire":
		return cmdEmergencyStopHardStopFire(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl emergency-stop hard-stop: unknown subcommand %q\n\n", sub)
		printEmergencyStopHardStopUsage(stderr)
		return exitUsage
	}
}

func printEmergencyStopHardStopUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl emergency-stop hard-stop <arm|fire> [flags]

Two separate subcommands, deliberately never chained by one command. See
cmd_emergency_stop.go's own doc comment. Run "arm", then "fire --arm-token
<token>" using the token "arm" printed, before it expires.
`)
}

func cmdEmergencyStopHardStopArm(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl emergency-stop hard-stop arm"
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp emergencyStopArmResponse
	if err := c.postJSON(ctx, "/api/v1/emergency-stop/hard-stop/arm", map[string]string{"idempotencyKey": key}, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "armed: token %s, expires %s\n", resp.ArmToken, resp.ExpiresAt.Format(time.RFC3339))
	_, _ = fmt.Fprintln(stderr, "showmeshctl emergency-stop hard-stop arm: run \"hard-stop fire --arm-token "+resp.ArmToken+"\" before it expires to actually fire")
	return exitOK
}

func cmdEmergencyStopHardStopFire(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl emergency-stop hard-stop fire"
	fs, g := newFlagSet(cmdLabel, stderr)
	// --arm-token, deliberately NOT --token: newFlagSet already registers
	// --token as the global bearer credential (ADR-024 decision 2), and
	// this is a different secret with a different lifetime. Reusing the
	// name would either collide (flag.FlagSet panics on a redefinition) or
	// silently overload one flag with two unrelated meanings.
	var armToken string
	fs.StringVar(&armToken, "arm-token", "", "the arm token printed by \"hard-stop arm\" (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s --arm-token <token> [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}
	if armToken == "" {
		_, _ = fmt.Fprintln(stderr, cmdLabel+": --arm-token is required (from \"hard-stop arm\")")
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp emergencyStopResponse
	if err := c.postJSON(ctx, "/api/v1/emergency-stop/hard-stop/fire", map[string]string{"idempotencyKey": key, "armToken": armToken}, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForEmergencyStopResult(resp.Result)
	}
	return reportEmergencyStopResult(stdout, cmdLabel, resp.Result)
}

// reportEmergencyStopResult prints every stop instance, the night-session
// outcome if present, and every follow-up, then returns the exit code
// StopOutcomes alone determines (exitCodeForEmergencyStopResult).
func reportEmergencyStopResult(stdout io.Writer, cmdLabel string, result emergencyStopResult) int {
	// result.NoInstancesConfigured is the ONLY honest "nothing to stop"
	// signal, never inferred from an empty StopOutcomes array, which a
	// failure to read the configured instance list also leaves non-empty
	// (one "failed" entry), never empty.
	if result.NoInstancesConfigured {
		_, _ = fmt.Fprintf(stdout, "%s: no FPP instances are configured; nothing to stop\n", result.Level)
	}
	for _, o := range result.StopOutcomes {
		_, _ = fmt.Fprintf(stdout, "%s: %s: %s\n", o.Outcome, o.InstanceID, o.OutcomeReason)
	}
	if result.NightSession != nil {
		switch {
		case result.NightSession.Error != "":
			_, _ = fmt.Fprintf(stdout, "night session: DEGRADED (the stop still proceeded): %s\n", result.NightSession.Error)
		case result.NightSession.Present:
			_, _ = fmt.Fprintf(stdout, "night session %s: %s\n", result.NightSession.SessionID, result.NightSession.Outcome)
		default:
			_, _ = fmt.Fprintln(stdout, "night session: none active")
		}
	}
	if result.FollowUpConfigError != "" {
		_, _ = fmt.Fprintf(stdout, "follow-up configuration: DEGRADED (the stop still proceeded, no follow-ups were attempted): %s\n", result.FollowUpConfigError)
	}
	for _, f := range result.FollowUps {
		label := f.Label
		if label == "" {
			label = f.ActionID
		}
		outcome := f.Outcome
		if outcome == "" {
			outcome = "unresolved"
		}
		_, _ = fmt.Fprintf(stdout, "follow-up (best-effort, does not affect exit code): %s: %s: %s\n", outcome, label, f.OutcomeReason)
	}
	return exitCodeForEmergencyStopResult(result)
}

// emergencyStopOutcomeSeverity ranks one instance outcome word so
// exitCodeForEmergencyStopResult can take the WORST across every
// configured instance: failed outranks refused outranks unconfirmed
// outranks confirmed. This is a severity rank, deliberately NOT the raw
// exit code integer: exitActionFailed (12) and exitActionRefused (13)
// do not happen to sort in severity order as exit codes, so comparing the
// exit codes directly would silently invert this ranking.
func emergencyStopOutcomeSeverity(outcome string) int {
	switch outcome {
	case "confirmed":
		return 0
	case "unconfirmed", "":
		return 1
	case "refused":
		return 2
	case "failed":
		return 3
	default:
		return 1
	}
}

// exitCodeForEmergencyStopResult is driven by StopOutcomes ALONE, taking
// the worst outcome across every configured instance. FollowUps never
// participate. See this file's own doc comment for why.
func exitCodeForEmergencyStopResult(result emergencyStopResult) int {
	worst := "confirmed"
	for _, o := range result.StopOutcomes {
		if emergencyStopOutcomeSeverity(o.Outcome) > emergencyStopOutcomeSeverity(worst) {
			worst = o.Outcome
		}
	}
	switch worst {
	case "confirmed":
		return exitOK
	case "failed":
		return exitActionFailed
	case "refused":
		return exitActionRefused
	default: // "unconfirmed", or empty on a mid-flight replay race
		return exitCommandUnconfirmed
	}
}
