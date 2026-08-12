package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is ADR-024 decision 9's host-level half of bootstrap and
// lockout recovery: "a coordinator subcommand run against the data volume
// on the host, requiring filesystem access... NOT reachable over the API
// at any scope." internal/coordinator/api/bootstrap.go is the other,
// network-reachable half (POST /api/v1/bootstrap), which this file's
// `bootstrap` subcommand deliberately duplicates rather than shelling out
// to: the whole point of a host-level path is that it works when the
// coordinator's own HTTP server cannot be trusted or reached at all (a
// corrupt principal store the API itself cannot authenticate against, a
// coordinator that will not start, a host being recovered offline).
//
// Every subcommand here opens the SQLite store and an identity.Service
// directly against SHOWMESH_DATA_DIR — deliberately NOT the full
// config.LoadConfig()/Config.Validate() path cmd's default coordinator.Run()
// uses: an operator reaching for one of these commands is very often
// migrating away from a leftover SHOWMESH_API_TOKEN (config.Validate's
// ADR-024 decision 2 refusal) or fixing a broker/FPP misconfiguration
// unrelated to identity, and a full-config validation failure would block
// the exact recovery tool meant to survive a broken configuration.
// config.EnvDataDir/config.DefaultDataDir are exported for exactly this
// narrower need.

// dataDirFromEnv reads SHOWMESH_DATA_DIR directly, with the same default
// full config loading uses, but without requiring the rest of the
// coordinator's configuration to be valid — see this file's doc comment.
func dataDirFromEnv() string {
	if v, ok := os.LookupEnv(config.EnvDataDir); ok && v != "" {
		return v
	}
	return config.DefaultDataDir
}

// cliLogger is deliberately quiet (warnings and above only) and writes to
// stderr, not stdout: every subcommand's own stdout output is meant to be
// read (and potentially scripted against) directly, and every one of
// these commands' own real work is a handful of database calls, not
// something an operator needs Debug/Info-level narration of.
func cliLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// cliDeps bundles every subcommand's external dependencies — where its
// output goes, where a password is read from, which data directory to
// open, which clock stamps a claimed bootstrap code's comparison against
// its expiry, and where its (best-effort) log warnings go — behind one
// struct threaded through every run*Subcommand function.
//
// This is the only refactor this file received to add test coverage, and
// it exists for one reason: every run*Subcommand function talked to
// os.Stdout, os.Stderr, os.Stdin, SHOWMESH_DATA_DIR, and time.Now directly
// before this change, none of which a test can substitute a deterministic
// double for without either mutating process-wide globals (os.Stdout et
// al., unsafe under `go test -race` and across parallel tests) or waiting
// out a real 24-hour bootstrap-code expiry. Every run*Subcommand exported
// to main.go (unchanged, still `func(args []string) int`) is now a thin
// wrapper that builds a [defaultCLIDeps] and delegates to a
// `*WithDeps` function carrying the actual logic — production behavior is
// identical, only reachable now through an injectable seam.
type cliDeps struct {
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
	dataDir string
	now     func() time.Time
	logger  *slog.Logger
}

// defaultCLIDeps is exactly what every production entrypoint in this file
// used directly before the [cliDeps] refactor: os.Stdout, os.Stderr,
// os.Stdin, dataDirFromEnv(), time.Now, and cliLogger().
func defaultCLIDeps() *cliDeps {
	return &cliDeps{
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		stdin:   os.Stdin,
		dataDir: dataDirFromEnv(),
		now:     time.Now,
		logger:  cliLogger(),
	}
}

// openIdentityService opens the coordinator's real SQLite store and a real
// identity.Service over it, against deps.dataDir and deps.now. The caller
// must Close the returned *store.Store.
func openIdentityService(ctx context.Context, deps *cliDeps) (*store.Store, identity.Service, error) {
	st, err := store.Open(ctx, deps.dataDir, deps.logger)
	if err != nil {
		return nil, nil, fmt.Errorf("open store at %q (%s): %w", deps.dataDir, config.EnvDataDir, err)
	}
	svc := identity.NewService(st, deps.now, deps.dataDir, identity.WithLogger(deps.logger))
	return st, svc, nil
}

// auditCLIAction writes a best-effort AuditAdmin entry for a subcommand's
// mutation. Best-effort — a failure is logged, not fatal — for the same
// reason handleCreateSession's failed audit path is NOT best-effort but
// this one is: ADR-024 decision 11's "a write that cannot be attributed
// does not proceed" rule protects a NETWORK caller's ability to be
// silently impersonated by a race with a failed audit write; a coordinator
// subcommand run by an operator holding a shell on the host has already
// proven exactly the possession this whole record treats as sufficient
// (decision 9: "requiring filesystem access, which is equivalent to
// owning the deployment"), so refusing to complete an otherwise-successful
// recovery action because the audit table itself is the thing broken
// would be the audit-gates-blackout mistake decision 11 explicitly
// rejected, applied to the tool meant to recover from exactly that kind of
// breakage.
func auditCLIAction(ctx context.Context, deps *cliDeps, svc identity.Service, action string, p identity.Principal) {
	err := svc.WriteAudit(ctx, identity.AuditEntry{
		Timestamp: deps.now(), PrincipalID: p.ID, PrincipalName: p.Name,
		Form: identity.FormCLI, Action: action, Target: p.ID,
		Kind: identity.AuditAdmin,
	})
	if err != nil {
		deps.logger.Warn("failed to write audit entry for a coordinator subcommand action", "action", action, "error", err)
	}
}

// readPassword prompts on stderr and reads a password from stdin with
// echo suppressed when stdin is a real terminal (golang.org/x/term, pure
// Go, no CGo — consistent with ADR-012). When stdin is not a terminal
// (piped input, a script, a test), it falls back to reading one line: a
// deployment automating recovery already chose not to use an interactive
// terminal, so there is no echo to suppress and no reason to refuse the
// input. Never logs or echoes the password itself either way.
//
// stdin is typed as io.Reader (deps.stdin) rather than *os.File so a test
// can substitute a strings.Reader/bytes.Buffer; the terminal-echo-
// suppression path only ever activates for a genuine *os.File that
// term.IsTerminal reports true for, which production's real os.Stdin can
// be and a test double never is, so this preserves the exact production
// behavior of the two branches this function had before the [cliDeps]
// refactor.
func readPassword(prompt string, stdin io.Reader, stderr io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readBootstrapCodeFromFile reads identity.BootstrapFileName straight out
// of dataDir. This is what lets `showmesh-coordinator bootstrap` be run
// with no -code flag at all on the same host the coordinator's data
// volume is mounted on: the file IS the credential (ADR-024 decision 9),
// so an operator who can read it does not need to also transcribe it onto
// a command line, which per decision 1's "never carried in a URL"
// reasoning applied here too would otherwise leave it sitting in shell
// history and a `ps` listing for no benefit.
func readBootstrapCodeFromFile(dataDir string) (string, error) {
	path := filepath.Join(dataDir, identity.BootstrapFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// findPrincipal resolves a principal by id (if given) or by exact name
// (via a full ListPrincipals scan — identity.Service exposes no
// get-by-name method beyond AuthenticatePassword, which requires the
// password this caller is trying to reset). Exactly one of id/name must
// be non-empty; callers validate that before calling this.
func findPrincipal(ctx context.Context, svc identity.Service, id, name string) (identity.Principal, error) {
	if id != "" {
		return svc.GetPrincipal(ctx, id)
	}
	principals, err := svc.ListPrincipals(ctx)
	if err != nil {
		return identity.Principal{}, fmt.Errorf("list principals: %w", err)
	}
	for _, p := range principals {
		if p.Name == name {
			return p, nil
		}
	}
	return identity.Principal{}, fmt.Errorf("no principal named %q", name)
}

// resolvePrincipal resolves a single `-principal` value that may be
// either a principal id or its exact display name, for the three token
// subcommands below. Unlike reset-password (which forces the caller to
// say up front whether they hold an id or a name via two mutually
// exclusive flags), a single flag is what the task spec asks for
// ("issue-token -principal=<name|id>"), so this tries an id lookup first
// — generated principal ids (see identity.Principal.ID) do not collide
// with operator-chosen display names — and falls back to findPrincipal's
// name scan on store.ErrPrincipalNotFound. Any other error (a closed
// store, a corrupt row) is returned as-is rather than masked by a second
// lookup attempt.
func resolvePrincipal(ctx context.Context, svc identity.Service, ref string) (identity.Principal, error) {
	p, err := svc.GetPrincipal(ctx, ref)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, store.ErrPrincipalNotFound) {
		return identity.Principal{}, err
	}
	return findPrincipal(ctx, svc, "", ref)
}

// parseExpiry interprets `-expires` as either an absolute RFC3339
// timestamp or a Go duration (e.g. "4380h") measured from now. It only
// ever runs when an operator explicitly passes -expires: ADR-024 decision
// 1 fixes machine tokens' default to no expiry at all ("the default is
// none, and the control is revocation"), so runIssueTokenSubcommand never
// calls this unless the flag was set, and passes nil straight to
// identity.Service.IssueToken otherwise.
func parseExpiry(raw string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("not a valid RFC3339 timestamp (e.g. 2027-01-15T00:00:00Z) or Go duration (e.g. 4380h): %w", err)
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("duration must be positive")
	}
	return now.Add(d), nil
}

// runIssueTokenSubcommand implements `showmesh-coordinator issue-token`:
// this record's fix for a defect found while folding in Step 6.
// identity.Service.IssueToken was implemented and audited by this
// package's own tests, but had exactly one caller in the whole tree — the
// no-op stub in internal/coordinator/api/auth.go — leaving no way for an
// operator to actually mint a token at all. ADR-024 decision 1's central
// promise, that a human may mint a token so the audit log records a
// person rather than a robot, and that FPP's scheduler principal and
// showmeshctl authenticate as machine principals holding tokens, was
// therefore unusable.
//
// This is a coordinator subcommand rather than an HTTP endpoint
// deliberately, matching BUILD-PLAN Step 6's "adds no write endpoint of
// its own" and ADR-024 decision 0's "does not decide... node enrollment
// automation" framing: principal/token management over HTTP is a surface
// ADR-024 did not specify, and a local subcommand matches decision 9's
// host-level posture, where possessing filesystem access on this data
// volume is already the proof of authority the rest of this file relies
// on. It shares this file's SHOWMESH_DATA_DIR bypass of
// config.LoadConfig() for the same reason bootstrap/create-admin/
// reset-password do (see the file-level doc comment): an operator minting
// a recovery token is often the operator with a leftover
// SHOWMESH_API_TOKEN blocking a normal coordinator start.
func runIssueTokenSubcommand(args []string) int {
	return runIssueTokenSubcommandWithDeps(defaultCLIDeps(), args)
}

func runIssueTokenSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("issue-token", flag.ExitOnError)
	principalRef := fs.String("principal", "", "principal id or exact display name to mint a token for (required)")
	label := fs.String("label", "", "label to help tell this token apart from others in list-tokens")
	expires := fs.String("expires", "", "optional expiry: an RFC3339 timestamp or a Go duration from now (e.g. 4380h); default: never (ADR-024 decision 1)")
	_ = fs.Parse(args)

	trimmedRef := strings.TrimSpace(*principalRef)
	if trimmedRef == "" {
		fmt.Fprintln(deps.stderr, "issue-token: -principal is required")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "issue-token: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principal, err := resolvePrincipal(ctx, svc, trimmedRef)
	if err != nil {
		fmt.Fprintf(deps.stderr, "issue-token: %v\n", err)
		return 1
	}

	var expiresAt *time.Time
	if trimmedExpires := strings.TrimSpace(*expires); trimmedExpires != "" {
		t, err := parseExpiry(trimmedExpires, deps.now())
		if err != nil {
			fmt.Fprintf(deps.stderr, "issue-token: -expires: %v\n", err)
			return 2
		}
		expiresAt = &t
	}

	tok, err := svc.IssueToken(ctx, principal.ID, strings.TrimSpace(*label), expiresAt)
	if err != nil {
		fmt.Fprintf(deps.stderr, "issue-token: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "token.issue", principal)

	fmt.Fprintf(deps.stdout, "Issued a token for %q (id %s, kind %s, role %s).\n", principal.Name, principal.ID, principal.Kind, principal.Role)
	if expiresAt != nil {
		fmt.Fprintf(deps.stdout, "Expires: %s\n", expiresAt.Format(time.RFC3339))
	} else {
		fmt.Fprintln(deps.stdout, "Expires: never (ADR-024 decision 1's default — pass -expires to set one; revoke-token is the control)")
	}
	fmt.Fprintln(deps.stdout)
	fmt.Fprintln(deps.stdout, "This token is displayed exactly once and cannot be retrieved again — store it now:")
	fmt.Fprintln(deps.stdout, tok.Value)
	return 0
}

// runListTokensSubcommand implements `showmesh-coordinator list-tokens`: a
// companion to issue-token so tokens minted from the host can actually be
// inventoried and told apart before deciding what to revoke-token. Never
// prints a digest or a raw token value — identity.Service.ListTokens
// itself returns neither (see TokenInfo's doc comment); this command adds
// nothing that would change that.
func runListTokensSubcommand(args []string) int {
	return runListTokensSubcommandWithDeps(defaultCLIDeps(), args)
}

func runListTokensSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("list-tokens", flag.ExitOnError)
	principalRef := fs.String("principal", "", "principal id or exact display name (required)")
	_ = fs.Parse(args)

	trimmedRef := strings.TrimSpace(*principalRef)
	if trimmedRef == "" {
		fmt.Fprintln(deps.stderr, "list-tokens: -principal is required")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "list-tokens: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principal, err := resolvePrincipal(ctx, svc, trimmedRef)
	if err != nil {
		fmt.Fprintf(deps.stderr, "list-tokens: %v\n", err)
		return 1
	}

	tokens, err := svc.ListTokens(ctx, principal.ID)
	if err != nil {
		fmt.Fprintf(deps.stderr, "list-tokens: %v\n", err)
		return 1
	}

	fmt.Fprintf(deps.stdout, "Tokens for %q (id %s):\n", principal.Name, principal.ID)
	fmt.Fprintf(deps.stdout, "%-36s  %-8s  %-24s  %-24s  %-24s  %s\n", "ID", "HINT", "LABEL", "CREATED", "EXPIRES", "LAST USED")
	for _, t := range tokens {
		label := t.Label
		if label == "" {
			label = "-"
		}
		expires := "never"
		if t.ExpiresAt != nil {
			expires = t.ExpiresAt.Format(time.RFC3339)
		}
		lastUsed := "never"
		if t.LastUsedAt != nil {
			lastUsed = t.LastUsedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(deps.stdout, "%-36s  %-8s  %-24s  %-24s  %-24s  %s\n",
			t.ID, t.Hint, label, t.CreatedAt.Format(time.RFC3339), expires, lastUsed)
	}
	return 0
}

// runRevokeTokenSubcommand implements `showmesh-coordinator revoke-token`:
// issue-token's undo, and per ADR-024 decision 1 the intended control for
// a token that never expires by default. Requires -principal as well as
// -id, and confirms the token belongs to that principal via ListTokens
// before revoking, both so the audit entry attributes the right principal
// and so a copy-pasted wrong id fails loudly rather than silently
// revoking an unrelated token id (token ids are UUIDs and not namespaced
// per principal at the store layer).
func runRevokeTokenSubcommand(args []string) int {
	return runRevokeTokenSubcommandWithDeps(defaultCLIDeps(), args)
}

func runRevokeTokenSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("revoke-token", flag.ExitOnError)
	principalRef := fs.String("principal", "", "principal id or exact display name the token belongs to (required)")
	tokenID := fs.String("id", "", "token id to revoke, from list-tokens (required)")
	_ = fs.Parse(args)

	trimmedRef := strings.TrimSpace(*principalRef)
	trimmedID := strings.TrimSpace(*tokenID)
	if trimmedRef == "" || trimmedID == "" {
		fmt.Fprintln(deps.stderr, "revoke-token: -principal and -id are both required")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "revoke-token: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principal, err := resolvePrincipal(ctx, svc, trimmedRef)
	if err != nil {
		fmt.Fprintf(deps.stderr, "revoke-token: %v\n", err)
		return 1
	}

	tokens, err := svc.ListTokens(ctx, principal.ID)
	if err != nil {
		fmt.Fprintf(deps.stderr, "revoke-token: %v\n", err)
		return 1
	}
	owned := false
	for _, t := range tokens {
		if t.ID == trimmedID {
			owned = true
			break
		}
	}
	if !owned {
		fmt.Fprintf(deps.stderr, "revoke-token: no token %q belongs to %q (id %s)\n", trimmedID, principal.Name, principal.ID)
		return 1
	}

	if err := svc.RevokeToken(ctx, trimmedID); err != nil {
		fmt.Fprintf(deps.stderr, "revoke-token: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "token.revoke", principal)

	fmt.Fprintf(deps.stdout, "Revoked token %s for %q (id %s).\n", trimmedID, principal.Name, principal.ID)
	return 0
}

// runBootstrapSubcommand implements `showmesh-coordinator bootstrap`: the
// host-level equivalent of POST /api/v1/bootstrap (see
// internal/coordinator/api/bootstrap.go), for a coordinator that cannot or
// should not be reached over HTTP to claim its own bootstrap code.
func runBootstrapSubcommand(args []string) int {
	return runBootstrapSubcommandWithDeps(defaultCLIDeps(), args)
}

func runBootstrapSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	code := fs.String("code", "", "the one-time bootstrap code (default: read from <data-dir>/"+identity.BootstrapFileName+")")
	name := fs.String("name", "", "display name for the new administrator (required)")
	deviceLabel := fs.String("device-label", "bootstrap-cli", "device label for the session this creates")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		fmt.Fprintln(deps.stderr, "bootstrap: -name is required")
		return 2
	}

	ctx := context.Background()

	trimmedCode := strings.TrimSpace(*code)
	if trimmedCode == "" {
		var err error
		trimmedCode, err = readBootstrapCodeFromFile(deps.dataDir)
		if err != nil {
			fmt.Fprintf(deps.stderr, "bootstrap: -code was not given and could not be read from the data volume: %v\n", err)
			return 1
		}
	}

	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "bootstrap: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	password, err := readPassword("New administrator password: ", deps.stdin, deps.stderr)
	if err != nil {
		fmt.Fprintf(deps.stderr, "bootstrap: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(deps.stderr, "bootstrap: password must not be empty")
		return 1
	}

	principal, err := svc.ClaimBootstrap(ctx, trimmedCode, trimmedName, password, deps.now())
	if err != nil {
		fmt.Fprintf(deps.stderr, "bootstrap: claim failed: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "bootstrap.claim", principal)

	fmt.Fprintf(deps.stdout, "Created administrator %q (id %s, device label %q). "+
		"The bootstrap code has been invalidated and its file deleted.\n", principal.Name, principal.ID, *deviceLabel)
	return 0
}

// runCreateAdminSubcommand implements `showmesh-coordinator create-admin`:
// ADR-024 decision 9's lockout-recovery floor, "create an admin" — for
// when bootstrap has already been claimed (so no code exists to reclaim)
// but every existing administrator's password or account has been lost.
// Unlike bootstrap, this needs no code at all: reaching this subcommand
// already required the filesystem access decision 9 treats as equivalent
// to owning the deployment.
func runCreateAdminSubcommand(args []string) int {
	return runCreateAdminSubcommandWithDeps(defaultCLIDeps(), args)
}

func runCreateAdminSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	name := fs.String("name", "", "display name for the new administrator (required)")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		fmt.Fprintln(deps.stderr, "create-admin: -name is required")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-admin: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	password, err := readPassword(fmt.Sprintf("Password for new administrator %q: ", trimmedName), deps.stdin, deps.stderr)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-admin: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(deps.stderr, "create-admin: password must not be empty")
		return 1
	}

	principal, err := svc.CreatePrincipal(ctx, trimmedName, identity.KindHuman, identity.RoleAdmin, password)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-admin: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "principal.create", principal)

	fmt.Fprintf(deps.stdout, "Created administrator %q (id %s).\n", principal.Name, principal.ID)
	return 0
}

// runCreatePrincipalSubcommand implements
// `showmesh-coordinator create-principal`: this record's fix for a defect
// found while folding in Step 6's review. runCreateAdminSubcommand's only
// caller ever created a principal was hardcoded to
// identity.KindHuman/identity.RoleAdmin — deliberately, since it is
// decision 9's lockout-recovery floor, not a general provisioning tool —
// which left NO way to create any other principal shape at all. ADR-024
// decision 7's own scenario, "the operator rotates the scheduler machine
// token in November", presupposes a scheduler principal exists to hold
// that token; nothing in this binary could create one. This subcommand is
// the general case create-admin deliberately is not: an arbitrary
// -role/-kind pair, matching decision 1's "kind does not restrict
// credential form, and role is independent of kind" exactly — a human may
// get any role, a machine may get any role, including admin.
//
// Kept a subcommand, not an HTTP endpoint: ADR-024 decision 0 does not
// specify principal management over the API at all (see this package's
// existing issue-token/list-tokens/revoke-token, all subcommands for the
// identical reason), and a general principal-creation endpoint is exactly
// the kind of write surface a future record should decide deliberately —
// not one this task should back into as a side effect of closing a
// review finding.
func runCreatePrincipalSubcommand(args []string) int {
	return runCreatePrincipalSubcommandWithDeps(defaultCLIDeps(), args)
}

func runCreatePrincipalSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("create-principal", flag.ExitOnError)
	name := fs.String("name", "", "display name for the new principal (required)")
	roleFlag := fs.String("role", "", "role: viewer, operator, admin, or scheduler (required)")
	kindFlag := fs.String("kind", "", "kind: human or machine (required)")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		fmt.Fprintln(deps.stderr, "create-principal: -name is required")
		return 2
	}

	role, err := identity.ParseRole(strings.TrimSpace(*roleFlag))
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-principal: -role: %v\n", err)
		return 2
	}

	var kind identity.Kind
	switch strings.TrimSpace(*kindFlag) {
	case string(identity.KindHuman):
		kind = identity.KindHuman
	case string(identity.KindMachine):
		kind = identity.KindMachine
	default:
		fmt.Fprintf(deps.stderr, "create-principal: -kind must be %q or %q\n", identity.KindHuman, identity.KindMachine)
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-principal: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	// A password is prompted for, exactly like create-admin/bootstrap/
	// reset-password, but — unlike create-admin — an EMPTY one is
	// accepted: identity.Service.CreatePrincipal already tolerates it
	// (skipping HashPassword entirely, see that method's doc comment),
	// which is the right default for the scenario this subcommand exists
	// for — a machine principal (FPP's scheduler, showmeshctl run
	// unattended) that will only ever authenticate with a token minted by
	// issue-token, never a password at all.
	password, err := readPassword(
		fmt.Sprintf("Password for new %s principal %q (leave empty for a machine principal that will only ever use a token): ", kind, trimmedName),
		deps.stdin, deps.stderr)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-principal: %v\n", err)
		return 1
	}

	principal, err := svc.CreatePrincipal(ctx, trimmedName, kind, role, password)
	if err != nil {
		fmt.Fprintf(deps.stderr, "create-principal: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "principal.create", principal)

	fmt.Fprintf(deps.stdout, "Created principal %q (id %s, kind %s, role %s).\n", principal.Name, principal.ID, principal.Kind, principal.Role)
	return 0
}

// runInvalidateAllSessionsSubcommand implements
// `showmesh-coordinator invalidate-all-sessions`: ADR-024 decision 5's
// third named generation-bump trigger, alongside a password change and an
// administrative revoke-all — "a database restore increments it" — which
// shipped as decided behavior with no code implementing it. A review
// finding reproduced the gap directly: create a session, back up the data
// directory, reset-password (which bumps generation and correctly kills
// that session), restore the backup, and the session authenticates again,
// because restoring rolls the ENTIRE database back to the backup's point
// in time — including principals.generation itself. Nothing left behind
// BY a restore can distinguish a session that was legitimately revoked
// after the backup point from one the restore just resurrected, so
// detecting this automatically is not attempted here (that would be the
// "fragile mechanism" this task's spec warned against inventing) —
// instead, exactly like bootstrap and lockout recovery, this is a
// deliberate, host-level, operator-run action: run this once, immediately
// after restoring a backup and before trusting the restored coordinator
// with any traffic, and every principal's every existing session and
// open change stream is invalidated, unconditionally, forcing a fresh
// login. It is NOT reachable over the API, matching decision 9's posture
// for the rest of this file exactly.
func runInvalidateAllSessionsSubcommand(args []string) int {
	return runInvalidateAllSessionsSubcommandWithDeps(defaultCLIDeps(), args)
}

func runInvalidateAllSessionsSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("invalidate-all-sessions", flag.ExitOnError)
	confirm := fs.Bool("yes", false, "required: confirms this operator intends to sign out every principal on this coordinator")
	_ = fs.Parse(args)

	if !*confirm {
		fmt.Fprintln(deps.stderr, "invalidate-all-sessions: refusing to run without -yes — this signs out EVERY principal's EVERY session and open stream on this coordinator, unconditionally")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "invalidate-all-sessions: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principals, err := svc.ListPrincipals(ctx)
	if err != nil {
		fmt.Fprintf(deps.stderr, "invalidate-all-sessions: %v\n", err)
		return 1
	}

	if err := svc.InvalidateAllSessions(ctx); err != nil {
		fmt.Fprintf(deps.stderr, "invalidate-all-sessions: %v\n", err)
		return 1
	}

	// One best-effort AuditAdmin entry per principal, exactly like every
	// other mutation this file performs — see auditCLIAction's doc
	// comment for why this is best-effort rather than gating: an operator
	// who can run this already holds filesystem access to the data
	// volume, decision 9's own standard for "equivalent to owning the
	// deployment".
	for _, p := range principals {
		auditCLIAction(ctx, deps, svc, "session.invalidate_all", p)
	}

	fmt.Fprintf(deps.stdout, "Invalidated every session and open stream for %d principal(s).\n", len(principals))
	return 0
}

// runResetPasswordSubcommand implements
// `showmesh-coordinator reset-password`: ADR-024 decision 9's lockout-
// recovery floor, "reset a principal's password" — for an operator who
// remembers who they are but not their password. Goes through
// identity.Service.SetPassword, which — critically for decision 5's
// guarantee — already bumps the principal's generation counter
// (store.Store.SetPrincipalPasswordHash), invalidating every session and
// closing every open change stream this principal currently holds, so a
// stolen-but-still-valid cookie cannot survive a password reset issued
// from the host.
func runResetPasswordSubcommand(args []string) int {
	return runResetPasswordSubcommandWithDeps(defaultCLIDeps(), args)
}

func runResetPasswordSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	name := fs.String("name", "", "principal name to reset (mutually exclusive with -id)")
	id := fs.String("id", "", "principal id to reset (mutually exclusive with -name)")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	trimmedID := strings.TrimSpace(*id)
	if (trimmedName == "" && trimmedID == "") || (trimmedName != "" && trimmedID != "") {
		fmt.Fprintln(deps.stderr, "reset-password: exactly one of -name or -id is required")
		return 2
	}

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "reset-password: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principal, err := findPrincipal(ctx, svc, trimmedID, trimmedName)
	if err != nil {
		fmt.Fprintf(deps.stderr, "reset-password: %v\n", err)
		return 1
	}

	password, err := readPassword(fmt.Sprintf("New password for %q: ", principal.Name), deps.stdin, deps.stderr)
	if err != nil {
		fmt.Fprintf(deps.stderr, "reset-password: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(deps.stderr, "reset-password: password must not be empty")
		return 1
	}

	if _, err := svc.SetPassword(ctx, principal.ID, password); err != nil {
		fmt.Fprintf(deps.stderr, "reset-password: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, deps, svc, "principal.reset_password", principal)

	fmt.Fprintf(deps.stdout, "Password reset for %q (id %s). Every existing session and open stream for this principal is now invalid.\n",
		principal.Name, principal.ID)
	return 0
}

// runListPrincipalsSubcommand implements
// `showmesh-coordinator list-principals`: a read-only convenience, not
// itself one of ADR-024 decision 9's two named floor operations, but
// cheap and directly useful alongside reset-password (which needs an
// exact principal name) and create-admin (an operator locked out often
// wants to first confirm who currently exists and who is disabled before
// deciding which recovery path applies). Local-only, exactly like the
// other subcommands in this file — never reachable over the API.
func runListPrincipalsSubcommand(args []string) int {
	return runListPrincipalsSubcommandWithDeps(defaultCLIDeps(), args)
}

func runListPrincipalsSubcommandWithDeps(deps *cliDeps, args []string) int {
	fs := flag.NewFlagSet("list-principals", flag.ExitOnError)
	_ = fs.Parse(args)

	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		fmt.Fprintf(deps.stderr, "list-principals: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principals, err := svc.ListPrincipals(ctx)
	if err != nil {
		fmt.Fprintf(deps.stderr, "list-principals: %v\n", err)
		return 1
	}

	fmt.Fprintf(deps.stdout, "%-36s  %-24s  %-8s  %-10s  %-8s  %s\n", "ID", "NAME", "KIND", "ROLE", "DISABLED", "CREATED")
	for _, p := range principals {
		fmt.Fprintf(deps.stdout, "%-36s  %-24s  %-8s  %-10s  %-8v  %s\n",
			p.ID, p.Name, p.Kind, p.Role, p.Disabled, p.CreatedAt.Format(time.RFC3339))
	}
	return 0
}
