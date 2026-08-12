package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

// This file is ADR-024 decision 1's proof from a non-browser client: "a
// human may mint an API token to use showmeshctl from a terminal," and the
// audit log then attributes every action to that person, not to a robot.
// `showmeshctl session` is how an operator at a terminal confirms that
// worked — which principal, kind, role, and effective scopes a bearer
// token resolves to — the same information GET /api/v1/session gives a
// browser after ADR-022 decision 4's shared secret was superseded.
//
// Deliberately bearer-only, with no `login` subcommand and no cookie jar.
// ADR-024 decision 5's whole HttpOnly-cookie design exists for the
// cold-phone browser case: a persistent credential a person never has to
// retype outdoors at night. None of that reasoning applies to a terminal
// session, which already has a natural, durable credential store (an
// environment variable, a secrets manager, a password manager) that a
// browser does not. Building a cookie jar here would duplicate ADR-024's
// session machinery (device labels, sliding expiry, generation-counter
// invalidation) for a client that already has the simpler answer decision
// 5 itself names as the alternative it rejected only because of what a
// browser cannot do: "API tokens only... rejected on the same constraint
// that produced decision 4... 'paste a forty character secret' is the
// single worst thing to ask of someone on a phone in the cold." A
// terminal is not a phone in the cold. A bearer token set once in
// $SHOWMESH_CTL_TOKEN is this CLI's answer, and it is the natural CLI
// credential ADR-024 decision 6 already exempts from the CSRF machinery
// cookies require. See the report for this call spelled out in full.
//
// $SHOWMESH_CTL_TOKEN, not $SHOWMESH_API_TOKEN: the latter is the ADR-021
// shared secret ADR-024 decision 2 retired, and a coordinator that still
// sees it set refuses to start. Reusing that name here would mean an
// operator who exports it to use this CLI could not start a coordinator
// from the same shell.
func cmdSession(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl session", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl session [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the principal, role, and effective scopes the configured")
		_, _ = fmt.Fprintln(stderr, "credential resolves to (GET /api/v1/session, ADR-024).")
		_, _ = fmt.Fprintln(stderr, "\nAlways reachable with no credential at all: with no --token and no")
		_, _ = fmt.Fprintln(stderr, "$SHOWMESH_CTL_TOKEN set, this reports \"not authenticated\" rather than")
		_, _ = fmt.Fprintln(stderr, "failing — that is this one endpoint's own contract (being signed out")
		_, _ = fmt.Fprintln(stderr, "is a readable state here, never a 401).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "session", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "session", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp sessionResponse
	if err := c.getJSON(ctx, "/api/v1/session", nil, &resp); err != nil {
		return reportError(stderr, "session", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "session", err)
		}
		return exitOK
	}
	printSessionDetail(stdout, resp)
	return exitOK
}
