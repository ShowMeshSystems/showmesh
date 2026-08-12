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

// openIdentityService opens the coordinator's real SQLite store and a real
// identity.Service over it, against SHOWMESH_DATA_DIR (see
// dataDirFromEnv). The caller must Close the returned *store.Store.
func openIdentityService(ctx context.Context, logger *slog.Logger) (*store.Store, identity.Service, string, error) {
	dataDir := dataDirFromEnv()
	st, err := store.Open(ctx, dataDir, logger)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open store at %q (%s): %w", dataDir, config.EnvDataDir, err)
	}
	svc := identity.NewService(st, time.Now, dataDir, identity.WithLogger(logger))
	return st, svc, dataDir, nil
}

// formCLI marks an audit entry written by one of these subcommands rather
// than by a request the HTTP API authenticated — a deliberate extension
// of identity.CredentialForm's value set, exactly like
// internal/coordinator/api's own formPassword: CredentialForm is a plain
// string underneath, so this compiles, stores, and decodes exactly like
// FormSession/FormToken even though it is not one of identity's own named
// constants. ADR-024 decision 11 requires "every principal, token, and
// session mutation" be audited; these subcommands mutate identity state
// exactly as much as the API's own principal-management surface would,
// and skipping the audit trail here just because the API layer is not
// involved would leave a real blind spot in "who changed what" for the
// one class of change that is, by construction, the hardest to attribute
// to anything other than "whoever had a shell on this host".
const formCLI identity.CredentialForm = "cli"

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
func auditCLIAction(ctx context.Context, svc identity.Service, logger *slog.Logger, action string, p identity.Principal) {
	err := svc.WriteAudit(ctx, identity.AuditEntry{
		Timestamp: time.Now(), PrincipalID: p.ID, PrincipalName: p.Name,
		Form: formCLI, Action: action, Target: p.ID,
		Kind: identity.AuditAdmin,
	})
	if err != nil {
		logger.Warn("failed to write audit entry for a coordinator subcommand action", "action", action, "error", err)
	}
}

// readPassword prompts on stderr and reads a password from stdin with
// echo suppressed when stdin is a real terminal (golang.org/x/term, pure
// Go, no CGo — consistent with ADR-012). When stdin is not a terminal
// (piped input, a script, a test), it falls back to reading one line: a
// deployment automating recovery already chose not to use an interactive
// terminal, so there is no echo to suppress and no reason to refuse the
// input. Never logs or echoes the password itself either way.
func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	reader := bufio.NewReader(os.Stdin)
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

// runBootstrapSubcommand implements `showmesh-coordinator bootstrap`: the
// host-level equivalent of POST /api/v1/bootstrap (see
// internal/coordinator/api/bootstrap.go), for a coordinator that cannot or
// should not be reached over HTTP to claim its own bootstrap code.
func runBootstrapSubcommand(args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	code := fs.String("code", "", "the one-time bootstrap code (default: read from <data-dir>/"+identity.BootstrapFileName+")")
	name := fs.String("name", "", "display name for the new administrator (required)")
	deviceLabel := fs.String("device-label", "bootstrap-cli", "device label for the session this creates")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: -name is required")
		return 2
	}

	logger := cliLogger()
	ctx := context.Background()

	dataDir := dataDirFromEnv()
	trimmedCode := strings.TrimSpace(*code)
	if trimmedCode == "" {
		var err error
		trimmedCode, err = readBootstrapCodeFromFile(dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bootstrap: -code was not given and could not be read from the data volume: %v\n", err)
			return 1
		}
	}

	st, svc, _, err := openIdentityService(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	password, err := readPassword("New administrator password: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: password must not be empty")
		return 1
	}

	principal, err := svc.ClaimBootstrap(ctx, trimmedCode, trimmedName, password, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap: claim failed: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, svc, logger, "bootstrap.claim", principal)

	fmt.Printf("Created administrator %q (id %s, device label %q). "+
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
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	name := fs.String("name", "", "display name for the new administrator (required)")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		fmt.Fprintln(os.Stderr, "create-admin: -name is required")
		return 2
	}

	logger := cliLogger()
	ctx := context.Background()
	st, svc, _, err := openIdentityService(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create-admin: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	password, err := readPassword(fmt.Sprintf("Password for new administrator %q: ", trimmedName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "create-admin: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "create-admin: password must not be empty")
		return 1
	}

	principal, err := svc.CreatePrincipal(ctx, trimmedName, identity.KindHuman, identity.RoleAdmin, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create-admin: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, svc, logger, "principal.create", principal)

	fmt.Printf("Created administrator %q (id %s).\n", principal.Name, principal.ID)
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
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	name := fs.String("name", "", "principal name to reset (mutually exclusive with -id)")
	id := fs.String("id", "", "principal id to reset (mutually exclusive with -name)")
	_ = fs.Parse(args)

	trimmedName := strings.TrimSpace(*name)
	trimmedID := strings.TrimSpace(*id)
	if (trimmedName == "" && trimmedID == "") || (trimmedName != "" && trimmedID != "") {
		fmt.Fprintln(os.Stderr, "reset-password: exactly one of -name or -id is required")
		return 2
	}

	logger := cliLogger()
	ctx := context.Background()
	st, svc, _, err := openIdentityService(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-password: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principal, err := findPrincipal(ctx, svc, trimmedID, trimmedName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-password: %v\n", err)
		return 1
	}

	password, err := readPassword(fmt.Sprintf("New password for %q: ", principal.Name))
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-password: %v\n", err)
		return 1
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "reset-password: password must not be empty")
		return 1
	}

	if _, err := svc.SetPassword(ctx, principal.ID, password); err != nil {
		fmt.Fprintf(os.Stderr, "reset-password: %v\n", err)
		return 1
	}
	auditCLIAction(ctx, svc, logger, "principal.reset_password", principal)

	fmt.Printf("Password reset for %q (id %s). Every existing session and open stream for this principal is now invalid.\n",
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
	fs := flag.NewFlagSet("list-principals", flag.ExitOnError)
	_ = fs.Parse(args)

	logger := cliLogger()
	ctx := context.Background()
	st, svc, _, err := openIdentityService(ctx, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list-principals: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	principals, err := svc.ListPrincipals(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list-principals: %v\n", err)
		return 1
	}

	fmt.Printf("%-36s  %-24s  %-8s  %-10s  %-8s  %s\n", "ID", "NAME", "KIND", "ROLE", "DISABLED", "CREATED")
	for _, p := range principals {
		fmt.Printf("%-36s  %-24s  %-8s  %-10s  %-8v  %s\n",
			p.ID, p.Name, p.Kind, p.Role, p.Disabled, p.CreatedAt.Format(time.RFC3339))
	}
	return 0
}
