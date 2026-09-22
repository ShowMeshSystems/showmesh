package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// cmdFPPSetBrightnessCeiling implements "showmeshctl fpp
// set-brightness-ceiling <instance-id> <0-100>": POST
// /api/v1/fpp/{instanceId}/brightness/ceiling, behind fpp:command.
//
// This writes the CEILING, the limit FPP's own output is held under. The
// transition gain is a separate value with a separate command and is
// never written from here.
//
// An out-of-range value is refused here, before dispatch, and again by
// the coordinator. No clamping anywhere, so a mistyped value stays
// visible instead of silently becoming a brightness nobody asked for.
func cmdFPPSetBrightnessCeiling(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp set-brightness-ceiling", stderr)
	var requestID string
	fs.StringVar(&requestID, "request-id", "", "idempotency key; minted fresh per invocation when omitted")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp set-brightness-ceiling [flags] <instance-id> <ceiling>")
		_, _ = fmt.Fprintln(stderr, "\nWrite one FPP host's brightness ceiling (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/brightness/ceiling, behind the fpp:command scope).")
		_, _ = fmt.Fprintln(stderr, "<ceiling> is 0-100 and is the limit the host's output is held under.")
		_, _ = fmt.Fprintln(stderr, "\nPrints the ceiling the plugin reported back, when it reported one in")
		_, _ = fmt.Fprintln(stderr, "time. A ceiling that was not read back is not a failure: the command")
		_, _ = fmt.Fprintln(stderr, "reached FPP and the value simply was not confirmed yet.")
		_, _ = fmt.Fprintln(stderr, "\nAn out-of-range ceiling is refused, never clamped.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, ceilingArg := rest[0], rest[1]

	ceiling, err := strconv.Atoi(ceilingArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-brightness-ceiling: invalid ceiling %q: must be an integer 0-100\n", ceilingArg)
		return exitUsage
	}
	if ceiling < 0 || ceiling > 100 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-brightness-ceiling: ceiling %d out of range: must be 0-100\n", ceiling)
		return exitUsage
	}

	if requestID == "" {
		key, keyErr := newIdempotencyKey()
		if keyErr != nil {
			return reportError(stderr, "fpp set-brightness-ceiling", keyErr)
		}
		requestID = key
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/brightness/ceiling"
	var resp fppBrightnessCeilingResponse
	if err := c.postJSON(ctx, path, fppBrightnessCeilingRequest{Ceiling: ceiling, RequestID: requestID}, &resp); err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp set-brightness-ceiling", err)
		}
		return exitOK
	}
	if resp.Ceiling != nil {
		_, _ = fmt.Fprintf(stdout, "confirmed: %s is holding its output at a ceiling of %d (request %s)\n",
			instanceID, *resp.Ceiling, resp.Command.IdempotencyKey)
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "unconfirmed: %s accepted a ceiling of %d and has not reported it back yet: %s (request %s)\n",
		instanceID, ceiling, resp.Command.OutcomeReason, resp.Command.IdempotencyKey)
	return exitCommandUnconfirmed
}
