package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// This file is Track G seam G-5's CLI surface: `showmeshctl principal
// list|create|disable|enable|reset-password|set-role`, over
// GET/POST/PUT /api/v1/principals... (internal/coordinator/api/principals.go).
// `showmeshctl token ...` (cmd_token.go) is this surface's sibling.
//
// A target principal is always its coordinator-minted id, never its
// display name: `principal list` is the way to find it, matching every
// other id-addressed subcommand in this program (`declare <id>`,
// `assets get <assetId>`). This program never resolves a name to an id
// client-side.
//
// set-role has no assigned verb in Track G's own spec table, which lists
// only list/create/disable/enable/reset-password — but the API surface it
// specifies explicitly includes "change role", and CLAUDE.md's own rule is
// that no API capability ships without CLI coverage in the step that adds
// it. "set-role" is this program's own choice of verb, kebab-case
// matching this file's siblings (reset-password).

func cmdPrincipal(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPrincipalUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printPrincipalUsage(stdout)
		return exitOK
	case "list":
		return cmdPrincipalList(rest, stdout, stderr, clock)
	case "create":
		return cmdPrincipalCreate(rest, stdout, stderr, clock)
	case "disable":
		return cmdPrincipalSetDisabled(rest, stdout, stderr, clock, true)
	case "enable":
		return cmdPrincipalSetDisabled(rest, stdout, stderr, clock, false)
	case "reset-password":
		return cmdPrincipalResetPassword(rest, stdout, stderr, clock)
	case "set-role":
		return cmdPrincipalSetRole(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl principal: unknown subcommand %q\n\n", sub)
		printPrincipalUsage(stderr)
		return exitUsage
	}
}

func printPrincipalUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl principal <subcommand> [flags]

Identity administration (Track G seam G-5, ADR-024, ADR-039 decision 8):
principals, their role and enabled state, and their passwords. "showmeshctl
token" (a sibling command) manages one principal's API tokens.

Reads require principal:read; every write requires principal:write and is
audited. Disabling the coordinator's last enabled administrator, or
changing its role away from one that holds principal:write, is refused
with 409 — this is a deliberate refusal (ADR-039 decision 8), not a bug:
it costs an administrative retry rather than an unrecoverable coordinator
with no shell to recover it from.

Creating the FIRST principal (bootstrap) is not here — ADR-024 decision 9
keeps that coordinator-local, since no principal exists yet to authenticate
this surface's own writes against. See "showmesh-coordinator bootstrap" on
the coordinator host.

Subcommands:
  list                              list every principal
  create                            create a principal (write)
  disable <id>                      disable a principal (write)
  enable <id>                       enable a principal (write)
  reset-password <id>               reset a principal's password (write)
  set-role <id> <role>              change a principal's role (write)

The coordinator's own "showmesh-coordinator list-principals",
"create-principal", "reset-password", "issue-token", "list-tokens",
"revoke-token", and "invalidate-all-sessions" subcommands stay: they are
the break-glass path for a coordinator with no reachable administrator at
all (host/container access, no network credential required), not the
ordinary path this command now is.

Run "showmeshctl principal <subcommand> --help" for flags specific to one
subcommand.
`)
}

// resolvePassword implements this file's three-way password preference,
// shared by "principal create" and "principal reset-password":
// --password (visible in shell history and `ps`), --password-stdin (one
// line from stdin), or an interactive TTY prompt with echo suppressed.
// required is false for "principal create" (an empty password
// is the coordinator's own documented tolerance for a machine principal
// that will only ever use an issued token) and true for
// "reset-password" (the coordinator itself rejects an empty reset, but
// refusing client-side before a request is sent matches this program's
// existing posture elsewhere, e.g. parseConfigSetPayload).
func resolvePassword(stdin io.Reader, stderr io.Writer, password string, useStdin bool, promptLabel string, required bool) (string, error) {
	isTTY := false
	fd := -1
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		isTTY = true
		fd = int(f.Fd())
	}
	return resolvePasswordFrom(stdin, stderr, password, useStdin, promptLabel, required, isTTY, func() (string, error) {
		b, err := term.ReadPassword(fd)
		return string(b), err
	})
}

// resolvePasswordFrom is resolvePassword's decision logic with the
// terminal detection and echo-suppressed read injected, so the flag and
// TTY paths are unit-testable without a real terminal. Order: an explicit
// flag always wins; a TTY prompts, and when required is false an empty
// answer means "no password"; a non-TTY with no flag is a usage error
// when required (never a hang on a read from a pipe with nothing behind
// it) and "no password" otherwise.
func resolvePasswordFrom(stdin io.Reader, stderr io.Writer, password string, useStdin bool, promptLabel string, required bool, isTTY bool, readSecret func() (string, error)) (string, error) {
	if useStdin {
		reader := bufio.NewReader(stdin)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if password != "" {
		return password, nil
	}
	if isTTY {
		if _, err := fmt.Fprint(stderr, promptLabel); err != nil {
			return "", fmt.Errorf("writing password prompt: %w", err)
		}
		s, err := readSecret()
		if _, werr := fmt.Fprintln(stderr); werr != nil {
			return "", fmt.Errorf("writing password prompt newline: %w", werr)
		}
		if err != nil {
			return "", fmt.Errorf("reading password: %w", err)
		}
		return s, nil
	}
	if !required {
		return "", nil
	}
	return "", newCLIError(exitUsage, "a password is required: pass --password, --password-stdin, or run interactively")
}

func cmdPrincipalList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl principal list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl principal list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList every principal. Requires principal:read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "principal list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "principal list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp principalsResponse
	if err := c.getJSON(ctx, "/api/v1/principals", nil, &resp); err != nil {
		return reportError(stderr, "principal list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "principal list", err)
		}
		return exitOK
	}
	printPrincipalsTable(stdout, resp)
	return exitOK
}

func cmdPrincipalCreate(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl principal create", stderr)
	var name, kind, role, password string
	var passwordStdin bool
	fs.StringVar(&name, "name", "", "display name for the new principal (required)")
	fs.StringVar(&kind, "kind", "", "human or machine (required)")
	fs.StringVar(&role, "role", "", "viewer, operator, admin, or scheduler (required)")
	fs.StringVar(&password, "password", "",
		"password, in plaintext on the command line -- visible in shell history and `ps`; prefer --password-stdin. "+
			"Empty (the default) creates a machine principal with no password that will only ever use an issued token.")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password as one line from stdin")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl principal create -name=<name> -kind=human|machine -role=<role> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nCreate a principal (requires principal:write). Never refused for want of")
		_, _ = fmt.Fprintln(stderr, "an existing administrator -- creating a principal only ever adds a way to")
		_, _ = fmt.Fprintln(stderr, "authenticate.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "principal create", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(role) == "" {
		return reportError(stderr, "principal create", newCLIError(exitUsage, "-name, -kind, and -role are all required"))
	}

	resolved, err := resolvePassword(os.Stdin, stderr, password, passwordStdin,
		fmt.Sprintf("Password for new %s principal %q (leave empty for a machine principal that will only ever use a token): ", kind, name),
		false)
	if err != nil {
		return reportError(stderr, "principal create", err)
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "principal create", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := createPrincipalRequest{Name: name, Kind: kind, Role: role, Password: resolved}
	var resp principalResponse
	if err := c.postJSON(ctx, "/api/v1/principals", req, &resp); err != nil {
		return reportError(stderr, "principal create", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "principal create", err)
		}
		return exitOK
	}
	printPrincipalDetail(stdout, resp.Principal)
	return exitOK
}

func cmdPrincipalSetDisabled(args []string, stdout, stderr io.Writer, clock func() time.Time, disabled bool) int {
	verb := "enable"
	action := "Enable"
	if disabled {
		verb = "disable"
		action = "Disable"
	}
	fs, g := newFlagSet("showmeshctl principal "+verb, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl principal %s [flags] <id>\n", verb)
		_, _ = fmt.Fprintf(stderr, "\n%s a principal (requires principal:write).\n", action)
		if disabled {
			_, _ = fmt.Fprintln(stderr, "Refused with 409 if this is the coordinator's last enabled administrator")
			_, _ = fmt.Fprintln(stderr, "(ADR-039 decision 8).")
		}
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "principal "+verb, err)
	}
	extra := fs.Args()
	if len(extra) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := extra[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "principal "+verb, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp principalResponse
	if err := c.postJSON(ctx, "/api/v1/principals/"+id+"/"+verb, nil, &resp); err != nil {
		return reportError(stderr, "principal "+verb, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "principal "+verb, err)
		}
		return exitOK
	}
	printPrincipalDetail(stdout, resp.Principal)
	return exitOK
}

func cmdPrincipalResetPassword(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl principal reset-password", stderr)
	var password string
	var passwordStdin bool
	fs.StringVar(&password, "password", "",
		"password, in plaintext on the command line -- visible in shell history and `ps`; prefer --password-stdin")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read the password as one line from stdin")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl principal reset-password [flags] <id>")
		_, _ = fmt.Fprintln(stderr, "\nReset a principal's password (requires principal:write). Bumps that")
		_, _ = fmt.Fprintln(stderr, "principal's generation counter, invalidating every session and token it")
		_, _ = fmt.Fprintln(stderr, "currently holds.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "principal reset-password", err)
	}
	extra := fs.Args()
	if len(extra) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := extra[0]

	resolved, err := resolvePassword(os.Stdin, stderr, password, passwordStdin,
		fmt.Sprintf("New password for %q: ", id), true)
	if err != nil {
		return reportError(stderr, "principal reset-password", err)
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "principal reset-password", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := setPrincipalPasswordRequest{Password: resolved}
	var resp principalResponse
	if err := c.postJSON(ctx, "/api/v1/principals/"+id+"/password", req, &resp); err != nil {
		return reportError(stderr, "principal reset-password", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "principal reset-password", err)
		}
		return exitOK
	}
	printPrincipalDetail(stdout, resp.Principal)
	_, _ = fmt.Fprintln(stderr, "\nshowmeshctl principal reset-password: every existing session and token for this principal is now invalid.")
	return exitOK
}

func cmdPrincipalSetRole(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl principal set-role", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl principal set-role [flags] <id> <role>")
		_, _ = fmt.Fprintln(stderr, "\nChange a principal's role (requires principal:write). Refused with 409")
		_, _ = fmt.Fprintln(stderr, "if this would leave no enabled principal able to reach principal:write")
		_, _ = fmt.Fprintln(stderr, "(ADR-039 decision 8).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "principal set-role", err)
	}
	extra := fs.Args()
	if len(extra) != 2 {
		fs.Usage()
		return exitUsage
	}
	id, role := extra[0], extra[1]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "principal set-role", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := setPrincipalRoleRequest{Role: role}
	var resp principalResponse
	if err := c.putJSON(ctx, "/api/v1/principals/"+id+"/role", req, &resp); err != nil {
		return reportError(stderr, "principal set-role", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "principal set-role", err)
		}
		return exitOK
	}
	printPrincipalDetail(stdout, resp.Principal)
	return exitOK
}
