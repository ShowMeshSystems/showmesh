package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// cmdFPPSetBrightnessExcludedRanges implements "showmeshctl fpp
// set-brightness-excluded-ranges <instance-id> <ranges>": PUT
// /api/v1/fpp/{instanceId}/brightness/excluded-ranges, behind fpp:command.
// It replaces the whole list and never writes the ceiling or the gain.
func cmdFPPSetBrightnessExcludedRanges(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "fpp set-brightness-excluded-ranges"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var requestID string
	fs.StringVar(&requestID, "request-id", "", "idempotency key; minted fresh per invocation when omitted")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl "+name+" [flags] <instance-id> <ranges>")
		_, _ = fmt.Fprintln(stderr, "\nReplace the channels one FPP host's brightness ceiling never changes (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/brightness/excluded-ranges, behind fpp:command).")
		_, _ = fmt.Fprintln(stderr, "<ranges> is start-count pairs, for example \"1-512, 1025-512\": 512 channels")
		_, _ = fmt.Fprintln(stderr, "from channel 1 and 512 from channel 1025. Use \"none\" to clear the list.")
		_, _ = fmt.Fprintln(stderr, "\nPrints the list the plugin reported back, when it reported one in time.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	ranges, refusal := parseChannelRanges(rest[1])
	if refusal != "" {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: %s\n", name, refusal)
		return exitUsage
	}

	if requestID == "" {
		key, keyErr := newIdempotencyKey()
		if keyErr != nil {
			return reportError(stderr, name, keyErr)
		}
		requestID = key
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/brightness/excluded-ranges"
	var resp fppBrightnessExcludedRangesResponse
	if err := c.putJSON(ctx, path, "", fppBrightnessExcludedRangesRequest{Ranges: ranges, RequestID: requestID}, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	if resp.ExcludedRanges != nil {
		_, _ = fmt.Fprintf(stdout, "confirmed: %s leaves these channels alone: %s (request %s)\n",
			instanceID, formatChannelRanges(*resp.ExcludedRanges), resp.Command.IdempotencyKey)
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "unconfirmed: %s (request %s)\n", resp.Command.OutcomeReason, resp.Command.IdempotencyKey)
	return exitCommandUnconfirmed
}

// parseChannelRanges reads "start-count, start-count" or "none". The
// second result is a one-line refusal, "" when the text was usable.
func parseChannelRanges(text string) ([]fppChannelRange, string) {
	ranges := []fppChannelRange{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.EqualFold(trimmed, "none") {
		return ranges, ""
	}
	for _, part := range strings.Split(trimmed, ",") {
		part = strings.TrimSpace(part)
		startText, countText, found := strings.Cut(part, "-")
		start, startErr := strconv.Atoi(strings.TrimSpace(startText))
		count, countErr := strconv.Atoi(strings.TrimSpace(countText))
		if !found || startErr != nil || countErr != nil || start < 1 || count < 1 {
			return nil, fmt.Sprintf("%q is not a range. Enter start-count pairs, like 1-512, 1025-512.", part)
		}
		ranges = append(ranges, fppChannelRange{StartChannel: start, ChannelCount: count})
	}
	return ranges, ""
}

// formatChannelRanges is parseChannelRanges's inverse.
func formatChannelRanges(ranges []fppChannelRange) string {
	if len(ranges) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, fmt.Sprintf("%d-%d", r.StartChannel, r.ChannelCount))
	}
	return strings.Join(parts, ", ")
}
