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

// cmdAudit implements `showmeshctl audit` (GET /api/v1/audit, ADR-024
// decision 11), which requires the audit:read scope regardless of whether
// reads are otherwise open (only the admin role holds it).
//
// Known, reported contract limitation this command works around rather
// than hides: an [auditEntry] carries no row id on the wire (see that
// type's doc comment and api/openapi.yaml's AuditResponse description —
// "unlike EventsResponse, this carries no gap/oldestRetainedSeq-shaped
// fields"). GET /api/v1/audit's own `since` parameter IS a row-id cursor
// server-side (internal/coordinator/store/audit.go: "entries with id >
// since"), but nothing in the response tells a client what id the last
// entry it received actually has, so this CLI cannot compute the next
// page's --since value from a page of results the way cmdEvents can
// chase `latestSeq`. This command therefore does not attempt
// auto-pagination or a --follow mode: it fetches exactly one page per
// invocation and, when that page is full enough that more entries might
// exist, says so honestly on stderr instead of silently stopping and
// leaving an operator to believe they saw the whole log.
func cmdAudit(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audit", stderr)
	var since, limit string
	fs.StringVar(&since, "since", "", "opaque row cursor: return entries after this value (default: from the beginning of retained history)")
	fs.StringVar(&limit, "limit", "", "maximum number of entries to return (default 100, coordinator clamps above 500)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audit [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the audit log (GET /api/v1/audit, ADR-024 decision 11).")
		_, _ = fmt.Fprintln(stderr, "Requires the audit:read scope (the admin role) regardless of whether")
		_, _ = fmt.Fprintln(stderr, "reads are otherwise open — this is not one of the four resources the")
		_, _ = fmt.Fprintln(stderr, "open-reads posture covers.")
		_, _ = fmt.Fprintln(stderr, "\nKNOWN LIMITATION: an audit entry carries no row id on the wire.")
		_, _ = fmt.Fprintln(stderr, "--since is the coordinator's own opaque cursor, but this CLI cannot")
		_, _ = fmt.Fprintln(stderr, "compute the NEXT page's --since value from a page of entries it")
		_, _ = fmt.Fprintln(stderr, "already has, because nothing in the entry it decodes carries that")
		_, _ = fmt.Fprintln(stderr, "cursor. This command fetches exactly one page; it does not")
		_, _ = fmt.Fprintln(stderr, "auto-paginate, and says so on stderr when a page might not be the")
		_, _ = fmt.Fprintln(stderr, "last one rather than silently presenting a partial log as complete.")
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

	query := url.Values{}
	if since != "" {
		// since is an int64 row-id cursor server-side (store.audit.go);
		// parsed as signed here to match exactly what the coordinator
		// itself accepts (parseAuditQuery: strconv.ParseInt, base 10, 64
		// bits, rejecting negative), rather than uint64 as cmdEvents does
		// for seq — audit's own cursor type is not the same as events'.
		if _, err := strconv.ParseInt(since, 10, 64); err != nil {
			return reportError(stderr, "audit", newCLIError(exitUsage, "invalid --since value %q: %v", since, err))
		}
		query.Set("since", since)
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
	if auditPageMayBeIncomplete(len(resp.Entries), requestedLimit) {
		_, _ = fmt.Fprintln(stderr, "showmeshctl audit: this page returned as many entries as were requested; "+
			"more may exist, but an audit entry carries no row id on the wire, so this CLI cannot compute the "+
			"next page's --since value from what it has (known contract limitation — see api/openapi.yaml's "+
			"AuditResponse and ADR-024 decision 11). Widen --limit (up to 500) or narrow --since to page through it.")
	}
	return exitOK
}

// effectiveAuditLimit mirrors the coordinator's own clamp
// (parseAuditQuery in internal/coordinator/api/audit.go) so this CLI can
// tell, without asking the server a second time, whether the page it just
// received is exactly as large as what was actually requested — the only
// signal available (see this file's doc comment) for "there might be
// more."
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

// auditPageMayBeIncomplete reports whether the page this command just
// printed filled its entire requested (or default) limit, which is the
// only evidence available that more entries might exist beyond it — see
// this file's doc comment on why this CLI cannot know for certain.
func auditPageMayBeIncomplete(got, requestedLimit int) bool {
	return got > 0 && got >= effectiveAuditLimit(requestedLimit)
}
