package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// defaultAuditLimit and maxAuditLimit mirror
// internal/coordinator/api/audit.go's own constants (which in turn mirror
// store.DefaultAuditPageSize / store.MaxAuditPageSize) — reproduced here,
// not imported (see doc.go/importgraph_test.go), so this CLI can compute
// what the coordinator will actually clamp a page to WITHOUT asking the
// server, purely to decide whether to print the "this page might not be
// the last one" note below.
const (
	defaultAuditLimit = 100
	maxAuditLimit     = 500
)

// auditOrderAsc/auditOrderDesc are GET /api/v1/audit's own `order` values.
const (
	auditOrderAsc  = "asc"
	auditOrderDesc = "desc"
)

// cmdAudit implements `showmeshctl audit` (GET /api/v1/audit, ADR-024
// decision 11), which requires the audit:read scope regardless of whether
// reads are otherwise open (only the admin role holds it).
//
// Pages in both directions, at parity with the endpoint: --order asc
// walks forward from --since (oldest first, the default), --order desc
// walks backward from --before (newest first), and --order desc with no
// --before opens on the most recent activity in one request. Every entry
// carries its id, so this command prints the exact next-page invocation
// instead of telling an operator it cannot compute one, and it uses the
// response's oldestRetainedId to say when a backward walk has genuinely
// reached the beginning of retained history rather than merely a short
// page.
func cmdAudit(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audit", stderr)
	var order, since, before, limit string
	fs.StringVar(&order, "order", "", "asc (oldest first, paged with --since) or desc (newest first, paged with --before); default asc")
	fs.StringVar(&since, "since", "", "with --order asc: return entries with an id greater than this (default: from the beginning of retained history)")
	fs.StringVar(&before, "before", "", "with --order desc: return entries with an id less than this (default: from the newest retained entry)")
	fs.StringVar(&limit, "limit", "", "maximum number of entries to return (default 100, coordinator clamps above 500)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audit [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the audit log (GET /api/v1/audit, ADR-024 decision 11).")
		_, _ = fmt.Fprintln(stderr, "Requires the audit:read scope (the admin role) regardless of whether")
		_, _ = fmt.Fprintln(stderr, "reads are otherwise open: this is not one of the four resources the")
		_, _ = fmt.Fprintln(stderr, "open-reads posture covers.")
		_, _ = fmt.Fprintln(stderr, "\nTo see what just happened, use --order desc: it returns the most")
		_, _ = fmt.Fprintln(stderr, "recent page directly. Every entry carries its id, so this command")
		_, _ = fmt.Fprintln(stderr, "prints the next page's exact invocation when more entries remain,")
		_, _ = fmt.Fprintln(stderr, "and says so when the log's beginning has been reached. An id is a")
		_, _ = fmt.Fprintln(stderr, "cursor, never a count: retention prunes from the oldest end and ids")
		_, _ = fmt.Fprintln(stderr, "are never reused.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audit", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	effectiveOrder := auditOrderAsc
	if order != "" {
		if order != auditOrderAsc && order != auditOrderDesc {
			return reportError(stderr, "audit", newCLIError(exitUsage, "invalid --order value %q: want %q or %q", order, auditOrderAsc, auditOrderDesc))
		}
		effectiveOrder = order
	}

	// The two cursors are one cursor read in two directions, so an
	// operator who names both, or names the one the chosen order does not
	// use, is told here rather than having one silently ignored (the
	// coordinator refuses the same combinations with a 400).
	if since != "" && before != "" {
		return reportError(stderr, "audit", newCLIError(exitUsage, "--since and --before are the two directions of one cursor and cannot be combined"))
	}
	if since != "" && effectiveOrder == auditOrderDesc {
		return reportError(stderr, "audit", newCLIError(exitUsage, "--since pages forward and is not valid with --order desc; use --before"))
	}
	if before != "" && effectiveOrder == auditOrderAsc {
		return reportError(stderr, "audit", newCLIError(exitUsage, "--before pages backward and requires --order desc"))
	}

	query := url.Values{}
	if order != "" {
		query.Set("order", order)
	}
	// Both cursors are int64 row ids server-side (store/audit.go); parsed
	// as signed here to match exactly what the coordinator itself accepts
	// (parseAuditQuery: strconv.ParseInt, base 10, 64 bits, rejecting
	// negative), rather than uint64 as cmdEvents does for seq.
	for _, c := range []struct {
		name  string
		value string
	}{{"since", since}, {"before", before}} {
		if c.value == "" {
			continue
		}
		if _, err := strconv.ParseInt(c.value, 10, 64); err != nil {
			return reportError(stderr, "audit", newCLIError(exitUsage, "invalid --%s value %q: %v", c.name, c.value, err))
		}
		query.Set(c.name, c.value)
	}
	requestedLimit := 0
	if limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil {
			return reportError(stderr, "audit", newCLIError(exitUsage, "invalid --limit value %q: %v", limit, err))
		}
		requestedLimit = n
		query.Set("limit", limit)
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audit", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp auditResponse
	if err := c.getJSON(ctx, "/api/v1/audit", query, &resp); err != nil {
		return reportError(stderr, "audit", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audit", err)
		}
		return exitOK
	}
	printAuditTable(stdout, resp)
	if note := auditPagingNote(resp, requestedLimit); note != "" {
		_, _ = fmt.Fprintln(stderr, note)
	}
	return exitOK
}

// effectiveAuditLimit mirrors the coordinator's own clamp
// (parseAuditQuery in internal/coordinator/api/audit.go) so this CLI can
// tell, without asking the server a second time, whether the page it just
// received is exactly as large as what was actually requested.
func effectiveAuditLimit(requested int) int {
	switch {
	case requested <= 0:
		return defaultAuditLimit
	case requested > maxAuditLimit:
		return maxAuditLimit
	default:
		return requested
	}
}

// auditPagingNote returns the stderr line describing where this page sits
// in the log, or "" when there is nothing honest to add. A descending page
// whose last entry is the oldest retained one has reached the beginning of
// retained history and says so; every other full page names the exact next
// invocation, computed from a real entry id rather than from a count.
func auditPagingNote(resp auditResponse, requestedLimit int) string {
	if len(resp.Entries) == 0 {
		return ""
	}
	last := resp.Entries[len(resp.Entries)-1]
	if resp.Order == auditOrderDesc && resp.OldestRetainedID != nil && last.ID <= *resp.OldestRetainedID {
		return "showmeshctl audit: this page ends at the oldest entry the coordinator still retains (id " +
			strconv.FormatInt(*resp.OldestRetainedID, 10) + "); there is no older history to page to."
	}
	if len(resp.Entries) < effectiveAuditLimit(requestedLimit) {
		return ""
	}
	if resp.Order == auditOrderDesc {
		return fmt.Sprintf("showmeshctl audit: more entries may exist before this page; continue with "+
			"'showmeshctl audit --order desc --before %d'.", last.ID)
	}
	return fmt.Sprintf("showmeshctl audit: more entries may exist after this page; continue with "+
		"'showmeshctl audit --since %d'.", last.ID)
}
