package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

func cmdEvents(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl events", stderr)
	var since, limit string
	fs.StringVar(&since, "since", "", "only include events with seq greater than N")
	fs.StringVar(&limit, "limit", "", "maximum number of events to return")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl events [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow event history (GET /api/v1/events).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "events", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	query := url.Values{}
	if since != "" {
		// seq is uint64 on the wire (contract §6.10 / v1.Event.Seq); parse
		// as unsigned so a value only valid as a uint64 (theoretical today,
		// but the type is the type) is not rejected as a usage error before
		// it ever reaches the coordinator.
		if _, err := strconv.ParseUint(since, 10, 64); err != nil {
			return reportError(stderr, "events", newCLIError(exitUsage, "invalid --since value %q: %v", since, err))
		}
		query.Set("since", since)
	}
	if limit != "" {
		if _, err := strconv.Atoi(limit); err != nil {
			return reportError(stderr, "events", newCLIError(exitUsage, "invalid --limit value %q: %v", limit, err))
		}
		query.Set("limit", limit)
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "events", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp eventsResponse
	if err := c.getJSON(ctx, "/api/v1/events", query, &resp); err != nil {
		return reportError(stderr, "events", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "events", err)
		}
		return exitOK
	}
	printEventsTable(stdout, resp)
	return exitOK
}
