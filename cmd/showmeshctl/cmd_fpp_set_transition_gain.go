package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"
)

// cmdFPPSetTransitionGain implements "showmeshctl fpp set-transition-gain
// <percent>": POST /api/v1/fpp/brightness/transition-gain, writing the
// brightness transition gain to every FPP. The gain is a 0-100 multiplier
// over the ceiling, round(ceiling * gain / 100), and is never the ceiling.
//
// Unlike every other "fpp <verb>" this does NOT dispatch an FPP command:
// the gain has exactly one writer by contract, and it is not reachable as
// an FPP Action. An out-of-range value is refused here, by the coordinator
// and by the plugin, never clamped.
func cmdFPPSetTransitionGain(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp set-transition-gain", stderr)
	var fadeSeconds int
	var requestID string
	fs.IntVar(&fadeSeconds, "fade-seconds", 0, "fade duration in seconds, 0-86400; 0 applies immediately")
	fs.StringVar(&requestID, "request-id", "", "idempotency key; minted fresh per invocation when omitted")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp set-transition-gain [flags] <percent>")
		_, _ = fmt.Fprintln(stderr, "\nWrite the brightness transition gain to every FPP (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/brightness/transition-gain, behind the fpp:command scope).")
		_, _ = fmt.Fprintln(stderr, "<percent> is 0-100 and multiplies the brightness ceiling:")
		_, _ = fmt.Fprintln(stderr, "effective output = round(ceiling * gain / 100). It never writes the ceiling.")
		_, _ = fmt.Fprintln(stderr, "\nPrints each FPP's applied state. A repeated --request-id reports")
		_, _ = fmt.Fprintln(stderr, "\"unchanged\" on an FPP that already took it, and exits 0. Exits 12 when")
		_, _ = fmt.Fprintln(stderr, "any FPP did not take the gain.")
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
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	percentArg := rest[0]

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

	var resp fppTransitionGainResponse
	if err := c.postJSON(ctx, "/api/v1/fpp/brightness/transition-gain",
		fppTransitionGainRequest{TargetPercent: percent, FadeSeconds: fadeSeconds, RequestID: requestID}, &resp); err != nil {
		return reportError(stderr, "fpp set-transition-gain", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	failed := false
	for _, p := range resp.Players {
		failed = failed || p.Outcome == "failed"
	}
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp set-transition-gain", err)
		}
	} else {
		for _, p := range resp.Players {
			printFPPTransitionGainResult(stdout, p)
		}
	}
	// Any FPP that did not take the gain is an attempt that failed; the others took it.
	if failed {
		return exitActionFailed
	}
	return exitOK
}

// printFPPTransitionGainResult prints one FPP's applied state, never a bare
// success. A repeat of an applied requestId reads "unchanged".
func printFPPTransitionGainResult(stdout io.Writer, r fppTransitionGainResult) {
	switch {
	case r.Outcome == "failed":
		_, _ = fmt.Fprintf(stdout, "failed: %s (request %s)\n", r.Reason, r.RequestID)
		return
	case r.GainStart == nil || r.GainTarget == nil || r.FadeSeconds == nil || r.Ceiling == nil || r.EffectiveOutput == nil:
		_, _ = fmt.Fprintf(stdout, "%s: %s (request %s)\n", r.Outcome, r.InstanceID, r.RequestID)
		return
	}
	lead := "applied"
	if r.Outcome == "alreadyApplied" {
		lead = "unchanged (this requestId was already applied)"
	}
	_, _ = fmt.Fprintf(stdout, "%s: %s gain %d -> %d over %ds; ceiling %d, effective output %d (request %s)\n",
		lead, r.InstanceID, *r.GainStart, *r.GainTarget, *r.FadeSeconds, *r.Ceiling, *r.EffectiveOutput, r.RequestID)
}
