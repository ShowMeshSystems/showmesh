package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// This file is Track G seam G-5's token half:
// `showmeshctl token list|issue|revoke`, over
// GET/POST/DELETE /api/v1/principals/{id}/tokens... — one principal's
// tokens, addressed by that principal's id (see cmd_principal.go's own
// doc comment on why this program never resolves a display name client
// side).

func cmdToken(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printTokenUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printTokenUsage(stdout)
		return exitOK
	case "list":
		return cmdTokenList(rest, stdout, stderr, clock)
	case "issue":
		return cmdTokenIssue(rest, stdout, stderr, clock)
	case "revoke":
		return cmdTokenRevoke(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl token: unknown subcommand %q\n\n", sub)
		printTokenUsage(stderr)
		return exitUsage
	}
}

func printTokenUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl token <subcommand> [flags]

Manage one principal's API tokens (Track G seam G-5, ADR-024 decision 1).
Reads require principal:read; issue and revoke require principal:write and
are audited. A token's plaintext value is rendered exactly once, at issue
time, and never again -- "token list" shows only its non-secret hint and
label.

Revoking the last credential able to reach principal:write (a password on
another enabled administrator, or another active token) is refused with
409 (ADR-039 decision 8) -- the same lockout protection
"showmeshctl principal disable" carries.

Subcommands:
  list <principalId>                            list a principal's tokens
  issue [--label] [--expires] <principalId>      issue a new token (write)
  revoke <principalId> <tokenId>                 revoke a token (write)

Run "showmeshctl token <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdTokenList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl token list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl token list [flags] <principalId>")
		_, _ = fmt.Fprintln(stderr, "\nList a principal's API tokens. Requires principal:read. Never shows a")
		_, _ = fmt.Fprintln(stderr, "digest or a raw value.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "token list", err)
	}
	extra := fs.Args()
	if len(extra) != 1 {
		fs.Usage()
		return exitUsage
	}
	principalID := extra[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "token list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp tokensResponse
	if err := c.getJSON(ctx, "/api/v1/principals/"+principalID+"/tokens", nil, &resp); err != nil {
		return reportError(stderr, "token list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "token list", err)
		}
		return exitOK
	}
	printTokensTable(stdout, resp)
	return exitOK
}

func cmdTokenIssue(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl token issue", stderr)
	var label, expires string
	fs.StringVar(&label, "label", "", "label to help tell this token apart from others in \"token list\"")
	fs.StringVar(&expires, "expires", "",
		"optional expiry: an RFC3339 timestamp (e.g. 2027-01-15T00:00:00Z); default: never (ADR-024 decision 1)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl token issue [flags] <principalId>")
		_, _ = fmt.Fprintln(stderr, "\nIssue a new API token for a principal (requires principal:write). The")
		_, _ = fmt.Fprintln(stderr, "printed value is this token's only appearance, ever -- store it now.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "token issue", err)
	}
	extra := fs.Args()
	if len(extra) != 1 {
		fs.Usage()
		return exitUsage
	}
	principalID := extra[0]

	req := issueTokenRequest{Label: strings.TrimSpace(label)}
	trimmedExpires := strings.TrimSpace(expires)
	if trimmedExpires != "" {
		req.ExpiresAt = &trimmedExpires
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "token issue", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp issueTokenResponse
	if err := c.postJSON(ctx, "/api/v1/principals/"+principalID+"/tokens", req, &resp); err != nil {
		return reportError(stderr, "token issue", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "token issue", err)
		}
		return exitOK
	}

	_, _ = fmt.Fprintf(stdout, "ID:          %s\n", resp.Token.ID)
	_, _ = fmt.Fprintf(stdout, "Principal:   %s\n", resp.Token.PrincipalID)
	_, _ = fmt.Fprintf(stdout, "Hint:        %s\n", resp.Token.Hint)
	label = resp.Token.Label
	if label == "" {
		label = "-"
	}
	_, _ = fmt.Fprintf(stdout, "Label:       %s\n", label)
	if resp.Token.ExpiresAt != nil {
		_, _ = fmt.Fprintf(stdout, "Expires:     %s\n", resp.Token.ExpiresAt.Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintln(stdout, "Expires:     never (ADR-024 decision 1's default -- pass --expires to set one; \"token revoke\" is the control)")
	}
	_, _ = fmt.Fprintf(stdout, "\nThis token is displayed exactly once and cannot be retrieved again -- store it now:\n%s\n", resp.Value)
	return exitOK
}

func cmdTokenRevoke(args []string, stdout, stderr io.Writer, _ func() time.Time) int {
	fs, g := newFlagSet("showmeshctl token revoke", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl token revoke [flags] <principalId> <tokenId>")
		_, _ = fmt.Fprintln(stderr, "\nRevoke an API token (requires principal:write). Refused with 409 if this")
		_, _ = fmt.Fprintln(stderr, "is the last credential able to reach principal:write (ADR-039 decision 8).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "token revoke", err)
	}
	extra := fs.Args()
	if len(extra) != 2 {
		fs.Usage()
		return exitUsage
	}
	principalID, tokenID := extra[0], extra[1]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "token revoke", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	if err := c.deleteJSON(ctx, "/api/v1/principals/"+principalID+"/tokens/"+tokenID, nil, nil); err != nil {
		return reportError(stderr, "token revoke", err)
	}
	_, _ = fmt.Fprintf(stdout, "Revoked token %s for principal %s.\n", tokenID, principalID)
	return exitOK
}
