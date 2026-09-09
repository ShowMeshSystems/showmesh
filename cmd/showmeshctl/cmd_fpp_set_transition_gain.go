package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// cmdFPPSetTransitionGain implements "showmeshctl fpp
// set-transition-gain <instance-id> <percent>": POST
// /api/v1/fpp/{instanceId}/brightness/transition-gain, behind
// fpp:command. Writes the brightness transition gain, a 0-100 multiplier
// that composes with FPP's own scheduled ceiling as
// round(ceiling * gain / 100). ShowMesh owns the gain and never the
// ceiling, so this can never overwrite a limit FPP's own schedule set.
//
// Unlike every other "fpp <verb>" this does NOT dispatch an FPP command
// and does not go through cmd_fpp_command.go's dispatch core: the gain has
// exactly one writer by contract, and it is not reachable as an FPP
// Action. It also needs no confirmation wait, so it keeps the ordinary
// --timeout rather than that core's own larger floor - the response
// already carries the applied state.
//
// An out-of-range value is refused here, before dispatch, and by the
// coordinator, and by the plugin. Three refusals and no clamping anywhere,
// so a mistyped value stays visible instead of silently becoming a
// brightness nobody asked for.
func cmdFPPSetTransitionGain(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp set-transition-gain", stderr)
	var fadeSeconds int
	var requestID string
	fs.IntVar(&fadeSeconds, "fade-seconds", 0, "fade duration in seconds, 0-86400; 0 applies immediately")
	fs.StringVar(&requestID, "request-id", "", "idempotency key; minted fresh per invocation when omitted")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp set-transition-gain [flags] <instance-id> <percent>")
		_, _ = fmt.Fprintln(stderr, "\nWrite one FPP host's brightness transition gain (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/brightness/transition-gain, behind the fpp:command")
		_, _ = fmt.Fprintln(stderr, "scope). <percent> is 0-100 and multiplies FPP's own scheduled brightness")
		_, _ = fmt.Fprintln(stderr, "ceiling: effective output = round(ceiling * gain / 100). This never writes")
		_, _ = fmt.Fprintln(stderr, "the ceiling, so it cannot overwrite a limit FPP's own schedule set.")
		_, _ = fmt.Fprintln(stderr, "\nPrints the applied state the FPP host reported - the fade's start and")
		_, _ = fmt.Fprintln(stderr, "target gain, its duration, the host's ceiling, and the effective output -")
		_, _ = fmt.Fprintln(stderr, "never a bare \"ok\". A repeated --request-id reports applied=false with the")
		_, _ = fmt.Fprintln(stderr, "gain unchanged: that is the idempotency key working, and it exits 0.")
		_, _ = fmt.Fprintln(stderr, "\nAn out-of-range percent or fade is refused, never clamped.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp set-transition-gain", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, percentArg := rest[0], rest[1]

	percent, err := strconv.Atoi(percentArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-transition-gain: invalid percent %q: must be an integer 0-100\n", percentArg)
		return exitUsage
	}
	if percent < 0 || percent > 100 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-transition-gain: percent %d out of range: must be 0-100\n", percent)
		return exitUsage
	}
	if fadeSeconds < 0 || fadeSeconds > 86400 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-transition-gain: --fade-seconds %d out of range: must be 0-86400\n", fadeSeconds)
		return exitUsage
	}

	if requestID == "" {
		key, keyErr := newIdempotencyKey()
		if keyErr != nil {
			return reportError(stderr, "fpp set-transition-gain", keyErr)
		}
		requestID = key
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp set-transition-gain", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/brightness/transition-gain"
	var resp fppTransitionGainResponse
	if err := c.postJSON(ctx, path,
		fppTransitionGainRequest{TargetPercent: percent, FadeSeconds: fadeSeconds, RequestID: requestID}, &resp); err != nil {
		return reportError(stderr, "fpp set-transition-gain", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp set-transition-gain", err)
		}
		return exitOK
	}
	printFPPTransitionGainResult(stdout, resp.TransitionGain)
	return exitOK
}

// printFPPTransitionGainResult prints the composed result the FPP host
// reported, never a bare success. applied=false gets its own leading word
// rather than a failure: nothing changed because this requestId had
// already been applied, and the numbers that follow are the gain as it
// stands.
func printFPPTransitionGainResult(stdout io.Writer, r fppTransitionGainResult) {
	lead := "applied"
	if !r.Applied {
		lead = "unchanged (this requestId was already applied)"
	}
	_, _ = fmt.Fprintf(stdout, "%s: %s gain %d -> %d over %ds; ceiling %d, effective output %d (request %s)\n",
		lead, r.InstanceID, r.GainStart, r.GainTarget, r.FadeSeconds, r.Ceiling, r.EffectiveOutput, r.RequestID)
}
