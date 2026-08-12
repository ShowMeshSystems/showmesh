package main

// This file is Step 6's test coverage for subcommands.go, ADR-024 decision
// 9's host-level bootstrap/recovery surface and decision 1's only path to
// mint a machine token. Before this file, the package had zero tests,
// which was a defensible precedent when this file was startup wiring; it
// is not a defensible precedent for security logic. See CLAUDE.md's
// "Standing rules while building" for why a test that would pass whether
// or not the underlying behavior is correct is treated as worse than no
// test at all — every property-shaped test below was written against that
// bar: the production behavior it names was broken, the test was
// confirmed to fail, and only then was the behavior restored. See this
// task's final report for the property-by-property mutation results.
//
// Argon2id cost: HashPassword is fixed at ADR-024 decision 1's production
// parameters (64 MiB, time 2, parallelism 1) and cannot be relaxed from
// this package without editing internal/coordinator/identity, which is out
// of scope here. Every test below that does not need a genuine password
// credential creates its principal with password "" — identity.svc.
// CreatePrincipal skips HashPassword entirely when password is empty (see
// service.go) — so the KDF cost is paid only by the tests that are
// actually exercising a password path (bootstrap, create-admin,
// reset-password), not by the token/audit/failure-mode tests that merely
// need *a* principal to exist.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// fakeClock lets a test drive [cliDeps.now] deterministically, matching
// this codebase's established injectable-clock pattern (see
// internal/coordinator/identity/identity_test.go's identical type).
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestDeps builds a *cliDeps wired entirely to in-memory/temp-dir
// doubles: a fresh SQLite data directory under t.TempDir(), buffered
// stdout/stderr a test can inspect instead of the real terminal, empty
// stdin (tests that need a password set deps.stdin explicitly), a
// fakeClock so bootstrap-expiry tests never sleep, and a *slog.Logger at
// Debug level writing to a captured buffer — lower than production's
// Warn-only filter (cliLogger) deliberately, so a test scanning for a
// leaked secret catches a future regression that logs at any level, not
// only the one level production happens to print today.
func newTestDeps(t *testing.T) (deps *cliDeps, stdout, stderr, logbuf *bytes.Buffer, clock *fakeClock) {
	t.Helper()
	dir := t.TempDir()
	clock = &fakeClock{t: mustTime(t, "2026-08-11T12:00:00Z")}
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	logbuf = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	deps = &cliDeps{
		stdout:  stdout,
		stderr:  stderr,
		stdin:   strings.NewReader(""),
		dataDir: filepath.Join(dir, "data"),
		now:     clock.now,
		logger:  logger,
	}
	return deps, stdout, stderr, logbuf, clock
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed.UTC()
}

// withStdin returns a copy of deps with stdin set to line, terminated with
// a newline so readPassword's non-terminal branch (bufio.ReadString('\n'))
// reads exactly one line — matching a piped password input, which is what
// every test here presents (deps.stdin is never an *os.File, so
// readPassword's terminal-echo-suppression branch never activates in a
// test; see readPassword's doc comment).
func withStdin(deps *cliDeps, line string) *cliDeps {
	cp := *deps
	cp.stdin = strings.NewReader(line + "\n")
	return &cp
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

// setupPrincipal opens deps' store directly (bypassing every
// run*Subcommand) and creates one principal, closing the store before
// returning so a subsequent subcommand invocation against the same
// dataDir does not contend with this connection. password "" skips
// HashPassword entirely (see this file's doc comment) — pass a real
// password only when the test is actually exercising that principal's
// password path.
func setupPrincipal(t *testing.T, deps *cliDeps, name string, kind identity.Kind, role identity.Role, password string) identity.Principal {
	t.Helper()
	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		t.Fatalf("setupPrincipal: open identity service: %v", err)
	}
	defer func() { _ = st.Close() }()

	p, err := svc.CreatePrincipal(ctx, name, kind, role, password)
	if err != nil {
		t.Fatalf("setupPrincipal: create principal: %v", err)
	}
	return p
}

// setupBootstrap triggers bootstrap-file generation via
// [identity.Service.EnsureBootstrap] — the code-generation half ADR-024
// decision 9 requires, deliberately separate from HasAnyPrincipal's own
// pure query (a review finding: this helper used to call HasAnyPrincipal
// for its then-undocumented generation side effect, which was itself the
// production defect — see EnsureBootstrap's own doc comment) — and
// returns the raw code an operator would read off the data volume. Opens
// and closes its own store connection, like setupPrincipal.
func setupBootstrap(t *testing.T, deps *cliDeps) (code string) {
	t.Helper()
	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		t.Fatalf("setupBootstrap: open identity service: %v", err)
	}
	defer func() { _ = st.Close() }()

	has, err := svc.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("setupBootstrap: HasAnyPrincipal: %v", err)
	}
	if has {
		t.Fatalf("setupBootstrap: a principal already exists")
	}
	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("setupBootstrap: EnsureBootstrap: %v", err)
	}
	return readBootstrapCode(t, deps.dataDir)
}

func readBootstrapCode(t *testing.T, dataDir string) string {
	t.Helper()
	code, err := readBootstrapCodeFromFile(dataDir)
	if err != nil {
		t.Fatalf("read bootstrap file: %v", err)
	}
	return code
}

// withServiceAfter reopens deps' store/service (a fresh connection,
// exactly as the next subcommand invocation would), runs fn against it,
// and closes it — for a test that needs to inspect state a subcommand run
// left behind (ListTokens, GetPrincipal, ListAudit, AuthenticateSession)
// without re-implementing openIdentityService's error handling in every
// test.
func withServiceAfter(t *testing.T, deps *cliDeps, fn func(ctx context.Context, svc identity.Service)) {
	t.Helper()
	ctx := context.Background()
	st, svc, err := openIdentityService(ctx, deps)
	if err != nil {
		t.Fatalf("withServiceAfter: open identity service: %v", err)
	}
	defer func() { _ = st.Close() }()
	fn(ctx, svc)
}

// assertNoSecretLeak fails the test if any of secrets appears anywhere in
// any of outputs — the property-2 check ("nothing writes a credential to
// a log") applied against captured output rather than by reading the
// source. An empty secret is skipped (a not-yet-known/optional value)
// rather than trivially matching every non-empty output.
func assertNoSecretLeak(t *testing.T, outputs map[string]string, secrets map[string]string) {
	t.Helper()
	for secretName, secret := range secrets {
		if secret == "" {
			continue
		}
		for outputName, output := range outputs {
			if strings.Contains(output, secret) {
				t.Errorf("%s leaked into %s: output contained the raw %s", secretName, outputName, secretName)
			}
		}
	}
}

// --- property 1: a secret is printed exactly once, never twice ---

func TestIssueTokenPrintsRawTokenExactlyOnceAndListTokensNeverRepeatsIt(t *testing.T) {
	deps, stdout, _, _, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "fpp-scheduler", identity.KindMachine, identity.RoleScheduler, "")

	code := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID, "-label=test"})
	if code != 0 {
		t.Fatalf("issue-token exit code = %d, want 0; stdout=%q", code, stdout.String())
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	token := lines[len(lines)-1]
	if !strings.HasPrefix(token, identity.TokenPrefix) {
		t.Fatalf("last line of issue-token output = %q, want it to look like a token (prefix %q)", token, identity.TokenPrefix)
	}

	if n := strings.Count(stdout.String(), token); n != 1 {
		t.Errorf("issue-token printed the raw token %d times, want exactly 1: %q", n, stdout.String())
	}

	digest := identity.HashToken(token)

	stdout.Reset()
	listCode := runListTokensSubcommandWithDeps(deps, []string{"-principal=" + principal.ID})
	if listCode != 0 {
		t.Fatalf("list-tokens exit code = %d, want 0", listCode)
	}
	listing := stdout.String()
	if strings.Contains(listing, token) {
		t.Errorf("list-tokens printed the raw token value: %q", listing)
	}
	if strings.Contains(listing, digest) {
		t.Errorf("list-tokens printed the token's digest: %q", listing)
	}
}

func TestIssueTokenFailsWhenItsOutputCannotBeWritten(t *testing.T) {
	deps, _, _, logbuf, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "fpp-scheduler", identity.KindMachine, identity.RoleScheduler, "")
	deps.stdout = failingWriter{err: errors.New("broken pipe")}

	if code := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID}); code != 1 {
		t.Fatalf("issue-token exit code with failed stdout = %d, want 1", code)
	}
	if !strings.Contains(logbuf.String(), "failed to write coordinator subcommand output") {
		t.Errorf("output failure was not logged: %q", logbuf.String())
	}
}

func TestBootstrapNeverEchoesTheCodeBack(t *testing.T) {
	deps, stdout, stderr, logbuf, _ := newTestDeps(t)
	code := setupBootstrap(t, deps)

	runDeps := withStdin(deps, "a-strong-bootstrap-password")
	exit := runBootstrapSubcommandWithDeps(runDeps, []string{"-name=Operator", "-code=" + code})
	if exit != 0 {
		t.Fatalf("bootstrap exit code = %d, want 0; stderr=%q", exit, stderr.String())
	}

	assertNoSecretLeak(t,
		map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "log": logbuf.String()},
		map[string]string{"bootstrap code": code},
	)
}

// --- property 2: nothing writes a credential to a log ---
//
// Exercised across every mutating subcommand in one pass: each is run with
// a known secret (a password, a token value, or a bootstrap code), and
// every captured output stream — stdout, stderr, and the structured logger
// — is scanned for that literal value.

func TestNoCredentialEverAppearsInLogsAcrossEveryMutation(t *testing.T) {
	t.Run("issue-token", func(t *testing.T) {
		deps, stdout, stderr, logbuf, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "ctl", identity.KindMachine, identity.RoleOperator, "")
		exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID})
		if exit != 0 {
			t.Fatalf("issue-token exit = %d", exit)
		}
		lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
		token := lines[len(lines)-1]
		// The token belongs on stdout exactly once (property 1); this
		// test's job is stderr and the logger, which must never carry it.
		assertNoSecretLeak(t,
			map[string]string{"stderr": stderr.String(), "log": logbuf.String()},
			map[string]string{"token": token},
		)
	})

	t.Run("create-admin", func(t *testing.T) {
		deps, stdout, stderr, logbuf, _ := newTestDeps(t)
		password := "a-created-admin-password"
		exit := runCreateAdminSubcommandWithDeps(withStdin(deps, password), []string{"-name=Root"})
		if exit != 0 {
			t.Fatalf("create-admin exit = %d; stderr=%q", exit, stderr.String())
		}
		assertNoSecretLeak(t,
			map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "log": logbuf.String()},
			map[string]string{"password": password},
		)
	})

	t.Run("reset-password", func(t *testing.T) {
		deps, stdout, stderr, logbuf, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "old-password-value")
		newPassword := "a-brand-new-password"
		exit := runResetPasswordSubcommandWithDeps(withStdin(deps, newPassword), []string{"-id=" + principal.ID})
		if exit != 0 {
			t.Fatalf("reset-password exit = %d; stderr=%q", exit, stderr.String())
		}
		assertNoSecretLeak(t,
			map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "log": logbuf.String()},
			map[string]string{"new password": newPassword},
		)
	})

	t.Run("bootstrap", func(t *testing.T) {
		deps, stdout, stderr, logbuf, _ := newTestDeps(t)
		code := setupBootstrap(t, deps)
		password := "bootstrap-admin-password"
		exit := runBootstrapSubcommandWithDeps(withStdin(deps, password), []string{"-name=First Admin", "-code=" + code})
		if exit != 0 {
			t.Fatalf("bootstrap exit = %d; stderr=%q", exit, stderr.String())
		}
		assertNoSecretLeak(t,
			map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "log": logbuf.String()},
			map[string]string{"bootstrap code": code, "password": password},
		)
	})

	t.Run("revoke-token", func(t *testing.T) {
		deps, stdout, stderr, logbuf, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "ctl2", identity.KindMachine, identity.RoleOperator, "")
		issueOut := &bytes.Buffer{}
		issueDeps := *deps
		issueDeps.stdout = issueOut
		if exit := runIssueTokenSubcommandWithDeps(&issueDeps, []string{"-principal=" + principal.ID}); exit != 0 {
			t.Fatalf("issue-token exit = %d", exit)
		}
		lines := strings.Split(strings.TrimRight(issueOut.String(), "\n"), "\n")
		token := lines[len(lines)-1]

		withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
			tokens, err := svc.ListTokens(ctx, principal.ID)
			if err != nil || len(tokens) != 1 {
				t.Fatalf("ListTokens: %v, %d tokens", err, len(tokens))
			}
			exit := runRevokeTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID, "-id=" + tokens[0].ID})
			if exit != 0 {
				t.Fatalf("revoke-token exit = %d; stderr=%q", exit, stderr.String())
			}
		})

		assertNoSecretLeak(t,
			map[string]string{"stdout": stdout.String(), "stderr": stderr.String(), "log": logbuf.String()},
			map[string]string{"token": token},
		)
	})
}

// --- property 3: the bootstrap code is single-use ---

func TestBootstrapClaimSucceedsAndDeletesTheFile(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	code := setupBootstrap(t, deps)

	exit := runBootstrapSubcommandWithDeps(withStdin(deps, "correct-horse-battery"), []string{"-name=Admin", "-code=" + code})
	if exit != 0 {
		t.Fatalf("bootstrap exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(deps.dataDir, identity.BootstrapFileName)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file still exists (or a different error occurred) after a successful claim: %v", err)
	}
}

func TestBootstrapClaimTwiceFailsTheSecondTime(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	code := setupBootstrap(t, deps)

	first := runBootstrapSubcommandWithDeps(withStdin(deps, "first-claim-password"), []string{"-name=Admin", "-code=" + code})
	if first != 0 {
		t.Fatalf("first claim exit = %d, want 0; stderr=%q", first, stderr.String())
	}

	stderr.Reset()
	second := runBootstrapSubcommandWithDeps(withStdin(deps, "second-claim-password"), []string{"-name=Someone Else", "-code=" + code})
	if second != 1 {
		t.Errorf("second claim exit = %d, want 1", second)
	}
	if !strings.Contains(stderr.String(), "already used") {
		t.Errorf("second claim stderr = %q, want it to mention the code was already used", stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		principals, err := svc.ListPrincipals(ctx)
		if err != nil {
			t.Fatalf("ListPrincipals: %v", err)
		}
		if len(principals) != 1 {
			t.Errorf("got %d principals after a rejected second claim, want exactly 1 (from the first, successful claim)", len(principals))
		}
	})
}

func TestBootstrapClaimWrongCodeFails(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	setupBootstrap(t, deps) // a valid code exists, but this test never uses it

	exit := runBootstrapSubcommandWithDeps(withStdin(deps, "password"), []string{"-name=Admin", "-code=totally-wrong-code"})
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), "invalid credential") {
		t.Errorf("stderr = %q, want it to mention an invalid credential", stderr.String())
	}
}

func TestBootstrapClaimExpiredCodeFails(t *testing.T) {
	deps, _, stderr, _, clock := newTestDeps(t)
	code := setupBootstrap(t, deps)

	// identity.DefaultBootstrapCodeTTL is 24h; advance well past it. No
	// real sleep: the fake clock backs both the code's recorded expiry
	// (set when setupBootstrap ran ensureBootstrap against deps.now) and
	// the "now" runBootstrapSubcommandWithDeps compares it against.
	clock.advance(25 * time.Hour)

	exit := runBootstrapSubcommandWithDeps(withStdin(deps, "password"), []string{"-name=Admin", "-code=" + code})
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), "expired") {
		t.Errorf("stderr = %q, want it to mention the code expired", stderr.String())
	}
}

// --- property 4: reset-password bumps the generation counter ---

func TestResetPasswordInvalidatesAnExistingSession(t *testing.T) {
	deps, _, stderr, _, clock := newTestDeps(t)
	principal := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "original-password")

	var sessionSecret string
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		_, secret, err := svc.CreateSession(ctx, principal.ID, principal.Name, "phone", "", clock.now())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sessionSecret = secret
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); err != nil {
			t.Fatalf("session does not authenticate before reset-password: %v", err)
		}
	})

	exit := runResetPasswordSubcommandWithDeps(withStdin(deps, "a-new-password"), []string{"-id=" + principal.ID})
	if exit != 0 {
		t.Fatalf("reset-password exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("session presented after reset-password: err = %v, want ErrInvalidCredential (the generation bump should have invalidated it)", err)
		}
	})
}

// --- property 5: revoke-token refuses cross-principal revocation ---

func TestRevokeTokenRefusesATokenBelongingToADifferentPrincipal(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	owner := setupPrincipal(t, deps, "owner", identity.KindMachine, identity.RoleOperator, "")
	other := setupPrincipal(t, deps, "other", identity.KindMachine, identity.RoleOperator, "")

	var tokenID string
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.IssueToken(ctx, owner.ID, "owner's token", nil); err != nil {
			t.Fatalf("IssueToken: %v", err)
		}
		tokens, err := svc.ListTokens(ctx, owner.ID)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("ListTokens: %v, %d", err, len(tokens))
		}
		tokenID = tokens[0].ID
	})

	exit := runRevokeTokenSubcommandWithDeps(deps, []string{"-principal=" + other.ID, "-id=" + tokenID})
	if exit != 1 {
		t.Errorf("revoking owner's token via -principal=other exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), "no token") {
		t.Errorf("stderr = %q, want it to say the token does not belong to that principal", stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		tokens, err := svc.ListTokens(ctx, owner.ID)
		if err != nil {
			t.Fatalf("ListTokens: %v", err)
		}
		if len(tokens) != 1 {
			t.Errorf("owner's token was revoked by a request scoped to a different principal: %d tokens remain, want 1", len(tokens))
		}
	})

	// The legitimate owner can still revoke it.
	exitOK := runRevokeTokenSubcommandWithDeps(deps, []string{"-principal=" + owner.ID, "-id=" + tokenID})
	if exitOK != 0 {
		t.Errorf("revoking owner's own token exit = %d, want 0; stderr=%q", exitOK, stderr.String())
	}
}

// --- property 6: issue-token defaults to no expiry; an explicit expiry is honored ---

func TestIssueTokenDefaultsToNoExpiry(t *testing.T) {
	deps, stdout, stderr, _, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "scheduler", identity.KindMachine, identity.RoleScheduler, "")

	exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID})
	if exit != 0 {
		t.Fatalf("issue-token exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Expires: never") {
		t.Errorf("issue-token output = %q, want it to say the token never expires", stdout.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		tokens, err := svc.ListTokens(ctx, principal.ID)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("ListTokens: %v, %d", err, len(tokens))
		}
		if tokens[0].ExpiresAt != nil {
			t.Errorf("token ExpiresAt = %v, want nil (no default expiry)", tokens[0].ExpiresAt)
		}
	})
}

func TestIssueTokenHonorsAnExplicitExpiry(t *testing.T) {
	deps, stdout, stderr, _, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "scheduler2", identity.KindMachine, identity.RoleScheduler, "")

	exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID, "-expires=2027-06-01T00:00:00Z"})
	if exit != 0 {
		t.Fatalf("issue-token exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "2027-06-01T00:00:00Z") {
		t.Errorf("issue-token output = %q, want it to report the expiry", stdout.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		tokens, err := svc.ListTokens(ctx, principal.ID)
		if err != nil || len(tokens) != 1 {
			t.Fatalf("ListTokens: %v, %d", err, len(tokens))
		}
		if tokens[0].ExpiresAt == nil || !tokens[0].ExpiresAt.Equal(mustTime(t, "2027-06-01T00:00:00Z")) {
			t.Errorf("token ExpiresAt = %v, want 2027-06-01T00:00:00Z", tokens[0].ExpiresAt)
		}
	})
}

// --- property 7: every mutation is audited with Form "cli" and correct attribution ---

func TestEveryMutationWritesACLIAuditEntry(t *testing.T) {
	t.Run("issue-token", func(t *testing.T) {
		deps, _, _, _, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "ctl", identity.KindMachine, identity.RoleOperator, "")
		if exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID}); exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		assertAudited(t, deps, "token.issue", principal)
	})

	t.Run("revoke-token", func(t *testing.T) {
		deps, _, _, _, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "ctl", identity.KindMachine, identity.RoleOperator, "")
		var tokenID string
		withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
			if _, err := svc.IssueToken(ctx, principal.ID, "", nil); err != nil {
				t.Fatalf("IssueToken: %v", err)
			}
			tokens, err := svc.ListTokens(ctx, principal.ID)
			if err != nil || len(tokens) != 1 {
				t.Fatalf("ListTokens: %v, %d", err, len(tokens))
			}
			tokenID = tokens[0].ID
		})
		if exit := runRevokeTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID, "-id=" + tokenID}); exit != 0 {
			t.Fatalf("exit = %d", exit)
		}
		assertAudited(t, deps, "token.revoke", principal)
	})

	t.Run("create-admin", func(t *testing.T) {
		deps, _, stderr, _, _ := newTestDeps(t)
		if exit := runCreateAdminSubcommandWithDeps(withStdin(deps, "password"), []string{"-name=NewAdmin"}); exit != 0 {
			t.Fatalf("exit = %d; stderr=%q", exit, stderr.String())
		}
		var principal identity.Principal
		withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
			ps, err := svc.ListPrincipals(ctx)
			if err != nil || len(ps) != 1 {
				t.Fatalf("ListPrincipals: %v, %d", err, len(ps))
			}
			principal = ps[0]
		})
		assertAudited(t, deps, "principal.create", principal)
	})

	t.Run("reset-password", func(t *testing.T) {
		deps, _, stderr, _, _ := newTestDeps(t)
		principal := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "old-password")
		if exit := runResetPasswordSubcommandWithDeps(withStdin(deps, "new-password"), []string{"-id=" + principal.ID}); exit != 0 {
			t.Fatalf("exit = %d; stderr=%q", exit, stderr.String())
		}
		assertAudited(t, deps, "principal.reset_password", principal)
	})

	t.Run("bootstrap", func(t *testing.T) {
		deps, _, stderr, _, _ := newTestDeps(t)
		code := setupBootstrap(t, deps)
		if exit := runBootstrapSubcommandWithDeps(withStdin(deps, "password"), []string{"-name=First", "-code=" + code}); exit != 0 {
			t.Fatalf("exit = %d; stderr=%q", exit, stderr.String())
		}
		var principal identity.Principal
		withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
			ps, err := svc.ListPrincipals(ctx)
			if err != nil || len(ps) != 1 {
				t.Fatalf("ListPrincipals: %v, %d", err, len(ps))
			}
			principal = ps[0]
		})
		// Step 7 seam 0: identity.Service.ClaimBootstrap now writes its own
		// "bootstrap.claim" audit entry atomically with the principal
		// creation (ADR-024 decision 11's same-transaction rule), so
		// subcommands.go's runBootstrapSubcommandWithDeps no longer calls
		// auditCLIAction for this one action (see that call site's own
		// comment). F6 review finding: an earlier version of this diff
		// widened this assertion to expect Form "password" here, on the
		// theory that ClaimBootstrap always hardcoded FormPassword
		// regardless of caller — which was itself the defect (a host-shell
		// claim and a network claim were byte-identical in the audit log).
		// runBootstrapSubcommandWithDeps now passes identity.FormCLI
		// through explicitly (see that call site), so this test's own name
		// — "every mutation writes a CLI audit entry" — is true again for
		// bootstrap too: assertAudited (plain, not assertAuditedForm) is
		// the same check every other subtest in this test uses.
		assertAudited(t, deps, "bootstrap.claim", principal)
	})
}

// assertAudited fails the test unless deps' audit log contains exactly the
// kind of entry auditCLIAction writes for action against principal: Form
// "cli", Kind AuditAdmin, correct PrincipalID/PrincipalName attribution,
// and Target set to the principal's own id. Matches on BOTH action and
// PrincipalID — not action alone — so a caller asserting about several
// principals sharing the same action (invalidate-all-sessions writes one
// entry per principal) checks the entry that actually belongs to the
// principal it named, not merely the first entry for that action found in
// the log.
//
// The Form check is pinned against the literal string "cli", not against
// identity.FormCLI — a review finding (mutation-confirmed) caught that
// comparing e.Form to the same named constant auditCLIAction itself writes
// is a tautology: it can never fail no matter what value FormCLI is
// defined as, because both sides of the comparison move together. Pinning
// the literal is what actually asserts "a CLI-attributed audit entry's
// Form on the wire is the string \"cli\"", which is the wire-visible
// property this test's name claims.
func assertAudited(t *testing.T, deps *cliDeps, action string, principal identity.Principal) {
	t.Helper()
	assertAuditedForm(t, deps, action, principal, "cli")
}

// assertAuditedForm is [assertAudited] with the expected Form
// parameterized — every caller except the "bootstrap" subtest wants "cli"
// (auditCLIAction's own literal); see that subtest's comment for why it
// alone wants "password" instead.
func assertAuditedForm(t *testing.T, deps *cliDeps, action string, principal identity.Principal, wantForm string) {
	t.Helper()
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		entries, err := svc.ListAudit(ctx, 0, 100)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		for _, e := range entries {
			if e.Action != action || e.PrincipalID != principal.ID {
				continue
			}
			if string(e.Form) != wantForm {
				t.Errorf("audit entry for %q: Form = %q, want %q", action, e.Form, wantForm)
			}
			if e.Kind != identity.AuditAdmin {
				t.Errorf("audit entry for %q: Kind = %q, want AuditAdmin", action, e.Kind)
			}
			if e.PrincipalName != principal.Name {
				t.Errorf("audit entry for %q: PrincipalName = %q, want %q", action, e.PrincipalName, principal.Name)
			}
			if e.Target != principal.ID {
				t.Errorf("audit entry for %q: Target = %q, want %q", action, e.Target, principal.ID)
			}
			return
		}
		t.Errorf("no audit entry found for action %q among %d entries", action, len(entries))
	})
}

// --- property 8: failure modes are distinct and correctly exit-coded ---

func TestUnknownPrincipalFailsWithExitCodeOne(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=does-not-exist"})
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), "no principal named") {
		t.Errorf("stderr = %q, want it to name the unknown principal", stderr.String())
	}
}

func TestMalformedExpiryFailsWithExitCodeTwo(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "ctl", identity.KindMachine, identity.RoleOperator, "")

	exit := runIssueTokenSubcommandWithDeps(deps, []string{"-principal=" + principal.ID, "-expires=not-a-timestamp-or-duration"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr.String(), "-expires") {
		t.Errorf("stderr = %q, want it to name the -expires flag", stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		tokens, err := svc.ListTokens(ctx, principal.ID)
		if err != nil {
			t.Fatalf("ListTokens: %v", err)
		}
		if len(tokens) != 0 {
			t.Errorf("a token was issued despite a malformed -expires: %d tokens", len(tokens))
		}
	})
}

func TestMissingDataDirectoryFailsWithExitCodeOne(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	// Point dataDir underneath a path component that is a regular file,
	// not a directory, so store.Open's os.MkdirAll cannot create it —
	// simulating a data volume that is not there (or not mountable) the
	// way an operator running this on the wrong host would hit.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	deps.dataDir = filepath.Join(blocker, "data")

	exit := runListPrincipalsSubcommandWithDeps(deps, nil)
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if stderr.String() == "" {
		t.Errorf("expected a non-empty error message on stderr")
	}
}

func TestBootstrapAlreadyClaimedFailsWithExitCodeOne(t *testing.T) {
	// Distinct scenario from TestBootstrapClaimTwiceFailsTheSecondTime:
	// here a principal already exists (via create-admin, not via a first
	// bootstrap claim), so ClaimBootstrap must be reached through
	// HasAnyPrincipal never having generated a code — exercised instead by
	// generating one, claiming it, and confirming the specific "already
	// claimed" exit code and message this property names explicitly.
	deps, _, stderr, _, _ := newTestDeps(t)
	code := setupBootstrap(t, deps)
	if exit := runBootstrapSubcommandWithDeps(withStdin(deps, "password"), []string{"-name=First", "-code=" + code}); exit != 0 {
		t.Fatalf("first claim exit = %d; stderr=%q", exit, stderr.String())
	}
	stderr.Reset()

	exit := runBootstrapSubcommandWithDeps(withStdin(deps, "password2"), []string{"-name=Second", "-code=" + code})
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), "already used") {
		t.Errorf("stderr = %q, want it to mention the code was already used", stderr.String())
	}
}

// --- create-principal: closes review finding 10 (no way to create a
// non-admin principal, so ADR-024 decision 7's own scenario — provisioning
// FPP's scheduler machine principal — could not be exercised at all) ---

func TestCreatePrincipalCreatesGivenRoleAndKind(t *testing.T) {
	deps, stdout, stderr, _, _ := newTestDeps(t)
	exit := runCreatePrincipalSubcommandWithDeps(withStdin(deps, ""), []string{"-name=fpp-scheduler", "-role=scheduler", "-kind=machine"})
	if exit != 0 {
		t.Fatalf("create-principal exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "fpp-scheduler") {
		t.Errorf("create-principal output = %q, want it to name the principal", stdout.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		ps, err := svc.ListPrincipals(ctx)
		if err != nil || len(ps) != 1 {
			t.Fatalf("ListPrincipals: %v, %d", err, len(ps))
		}
		if ps[0].Role != identity.RoleScheduler {
			t.Errorf("Role = %q, want %q", ps[0].Role, identity.RoleScheduler)
		}
		if ps[0].Kind != identity.KindMachine {
			t.Errorf("Kind = %q, want %q", ps[0].Kind, identity.KindMachine)
		}
	})
}

func TestCreatePrincipalRejectsUnknownRole(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	exit := runCreatePrincipalSubcommandWithDeps(withStdin(deps, ""), []string{"-name=x", "-role=superuser", "-kind=machine"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr.String(), "-role") {
		t.Errorf("stderr = %q, want it to name the -role flag", stderr.String())
	}
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		ps, err := svc.ListPrincipals(ctx)
		if err != nil {
			t.Fatalf("ListPrincipals: %v", err)
		}
		if len(ps) != 0 {
			t.Errorf("a principal was created despite an unknown role: %+v", ps)
		}
	})
}

func TestCreatePrincipalRejectsUnknownKind(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	exit := runCreatePrincipalSubcommandWithDeps(withStdin(deps, ""), []string{"-name=x", "-role=viewer", "-kind=robot"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr.String(), "-kind") {
		t.Errorf("stderr = %q, want it to name the -kind flag", stderr.String())
	}
}

// TestCreatePrincipalAllowsEmptyPasswordAndItIsUnauthenticatable proves
// both halves of this subcommand's own doc comment: an empty password is
// accepted (the machine-principal-with-a-token-only scenario this
// subcommand exists for), and — unlike create-admin, which refuses an
// empty password outright — the resulting principal genuinely cannot
// authenticate by password at all, matching
// internal/coordinator/identity's own
// TestEmptyPasswordPrincipalIsNeverAuthenticatableByPassword.
func TestCreatePrincipalAllowsEmptyPasswordAndItIsUnauthenticatable(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	exit := runCreatePrincipalSubcommandWithDeps(withStdin(deps, ""), []string{"-name=scheduler-bot", "-role=scheduler", "-kind=machine"})
	if exit != 0 {
		t.Fatalf("create-principal exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticatePassword(ctx, "scheduler-bot", ""); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("AuthenticatePassword with an empty password = %v, want ErrInvalidCredential", err)
		}
	})
}

func TestCreatePrincipalRequiresName(t *testing.T) {
	deps, _, stderr, _, _ := newTestDeps(t)
	exit := runCreatePrincipalSubcommandWithDeps(deps, []string{"-role=viewer", "-kind=machine"})
	if exit != 2 {
		t.Errorf("exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr.String(), "-name") {
		t.Errorf("stderr = %q, want it to name the -name flag", stderr.String())
	}
}

// --- invalidate-all-sessions: closes review finding 5 (a database
// restore resurrects revoked sessions; decision 5 states restore
// invalidation as decided behavior with nothing implementing it) ---

func TestInvalidateAllSessionsRequiresYesFlag(t *testing.T) {
	deps, _, stderr, _, clock := newTestDeps(t)
	principal := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "pw")

	var sessionSecret string
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		_, secret, err := svc.CreateSession(ctx, principal.ID, principal.Name, "phone", "", clock.now())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sessionSecret = secret
	})

	exit := runInvalidateAllSessionsSubcommandWithDeps(deps, nil)
	if exit != 2 {
		t.Errorf("exit = %d, want 2 (refused without -yes)", exit)
	}
	if !strings.Contains(stderr.String(), "-yes") {
		t.Errorf("stderr = %q, want it to name the -yes flag", stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); err != nil {
			t.Errorf("session no longer authenticates after a REFUSED invalidate-all-sessions: %v", err)
		}
	})
}

// TestInvalidateAllSessionsInvalidatesEverySessionAcrossAllPrincipals is
// this subcommand's own core property, reproducing review finding 5's
// exact scenario: create a session, "back up" (nothing to do here — the
// property under test is what the RESTORE PROCEDURE is documented to run
// immediately afterward), then run invalidate-all-sessions -yes and
// confirm every principal's session — across MULTIPLE principals, not
// just one — is genuinely invalid afterward.
func TestInvalidateAllSessionsInvalidatesEverySessionAcrossAllPrincipals(t *testing.T) {
	deps, stdout, stderr, _, clock := newTestDeps(t)
	alice := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "pw-a")
	bob := setupPrincipal(t, deps, "bob", identity.KindHuman, identity.RoleOperator, "pw-b")

	var aliceSecret, bobSecret string
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		_, s1, err := svc.CreateSession(ctx, alice.ID, alice.Name, "alice-phone", "", clock.now())
		if err != nil {
			t.Fatalf("CreateSession alice: %v", err)
		}
		aliceSecret = s1
		_, s2, err := svc.CreateSession(ctx, bob.ID, bob.Name, "bob-phone", "", clock.now())
		if err != nil {
			t.Fatalf("CreateSession bob: %v", err)
		}
		bobSecret = s2
	})

	// Sanity: both sessions genuinely authenticate before the restore
	// scenario — otherwise the assertions below would pass for the wrong
	// reason.
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, aliceSecret, clock.now()); err != nil {
			t.Fatalf("alice's session does not authenticate before invalidate-all-sessions: %v", err)
		}
		if _, err := svc.AuthenticateSession(ctx, bobSecret, clock.now()); err != nil {
			t.Fatalf("bob's session does not authenticate before invalidate-all-sessions: %v", err)
		}
	})

	exit := runInvalidateAllSessionsSubcommandWithDeps(deps, []string{"-yes"})
	if exit != 0 {
		t.Fatalf("invalidate-all-sessions exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "2") {
		t.Errorf("output = %q, want it to report 2 principals invalidated", stdout.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, aliceSecret, clock.now()); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("alice's session still authenticates after invalidate-all-sessions: err = %v, want ErrInvalidCredential", err)
		}
		if _, err := svc.AuthenticateSession(ctx, bobSecret, clock.now()); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("bob's session still authenticates after invalidate-all-sessions: err = %v, want ErrInvalidCredential", err)
		}
	})

	assertAudited(t, deps, "session.invalidate_all", alice)
	assertAudited(t, deps, "session.invalidate_all", bob)
}

// TestInvalidateAllSessionsClosesARealBackupRestoreResurrection is review
// finding 5's literal reproduction, end to end against a real data
// directory on disk: create a session, back up the data directory (a real
// recursive file copy — this is what "the operator backs up the data
// volume" means in practice), reset-password (which correctly kills the
// session by bumping its principal's generation), restore the backup
// (overwriting the post-reset data directory with the pre-reset copy —
// principals.generation included), and confirm the session RESURRECTS —
// proving the vulnerability is real, not hypothetical, before proving
// invalidate-all-sessions, run once against the restored directory as the
// documented restore procedure requires, closes it.
func TestInvalidateAllSessionsClosesARealBackupRestoreResurrection(t *testing.T) {
	deps, _, stderr, _, clock := newTestDeps(t)
	principal := setupPrincipal(t, deps, "alice", identity.KindHuman, identity.RoleAdmin, "original-password")

	var sessionSecret string
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		_, secret, err := svc.CreateSession(ctx, principal.ID, principal.Name, "phone", "", clock.now())
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		sessionSecret = secret
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); err != nil {
			t.Fatalf("session does not authenticate before backup: %v", err)
		}
	})

	// "Back up the data volume": a real recursive copy of deps.dataDir —
	// the same directory store.Open puts showmesh.db in and identity's
	// bootstrap.go puts BootstrapFileName in — to a separate directory
	// this test controls independently.
	backupDir := t.TempDir()
	if err := copyDirRecursive(t, deps.dataDir, backupDir); err != nil {
		t.Fatalf("back up data directory: %v", err)
	}

	// A real reset-password: correctly bumps the principal's generation
	// and kills the session — proven directly, so the resurrection below
	// is attributed to the restore, not to reset-password having silently
	// failed to work.
	resetExit := runResetPasswordSubcommandWithDeps(withStdin(deps, "a-new-password"), []string{"-id=" + principal.ID})
	if resetExit != 0 {
		t.Fatalf("reset-password exit = %d, want 0; stderr=%q", resetExit, stderr.String())
	}
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Fatalf("session still authenticates immediately after reset-password: err = %v, want ErrInvalidCredential", err)
		}
	})

	// "Restore the backup": overwrite the post-reset data directory with
	// the pre-reset copy.
	if err := os.RemoveAll(deps.dataDir); err != nil {
		t.Fatalf("remove post-reset data directory: %v", err)
	}
	if err := copyDirRecursive(t, backupDir, deps.dataDir); err != nil {
		t.Fatalf("restore backup: %v", err)
	}

	// The vulnerability, reproduced: the restored session resurrects.
	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); err != nil {
			t.Fatalf("restored session does not authenticate — this test's own reproduction of the restore-resurrection defect did not occur, so the fix below proves nothing: %v", err)
		}
	})

	// The fix: run the documented restore procedure's own step against
	// the now-restored data directory.
	invalidateExit := runInvalidateAllSessionsSubcommandWithDeps(deps, []string{"-yes"})
	if invalidateExit != 0 {
		t.Fatalf("invalidate-all-sessions exit = %d, want 0; stderr=%q", invalidateExit, stderr.String())
	}

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		if _, err := svc.AuthenticateSession(ctx, sessionSecret, clock.now()); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("resurrected session still authenticates after invalidate-all-sessions: err = %v, want ErrInvalidCredential", err)
		}
	})
}

// copyDirRecursive copies every regular file under src to the identical
// relative path under dst, creating directories as needed — a minimal
// stand-in for an operator's real backup/restore tooling (tar, rsync, a
// volume snapshot), sufficient for this test's purpose: reproducing what
// "restore the data directory" does to every file store.Open and
// identity's bootstrap.go put there, not exercising any particular real
// backup tool.
func copyDirRecursive(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
}

// --- direct unit coverage for pure/near-pure helpers (cheap: no KDF) ---

func TestParseExpiryAbsoluteRFC3339(t *testing.T) {
	now := mustTime(t, "2026-01-01T00:00:00Z")
	got, err := parseExpiry("2027-01-15T00:00:00Z", now)
	if err != nil {
		t.Fatalf("parseExpiry: %v", err)
	}
	if !got.Equal(mustTime(t, "2027-01-15T00:00:00Z")) {
		t.Errorf("got %v, want 2027-01-15T00:00:00Z", got)
	}
}

func TestParseExpiryRelativeDuration(t *testing.T) {
	now := mustTime(t, "2026-01-01T00:00:00Z")
	got, err := parseExpiry("24h", now)
	if err != nil {
		t.Fatalf("parseExpiry: %v", err)
	}
	want := now.Add(24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseExpiryRejectsGarbage(t *testing.T) {
	if _, err := parseExpiry("not-a-time-or-duration", time.Now()); err == nil {
		t.Errorf("expected an error for a garbage -expires value")
	}
}

func TestParseExpiryRejectsNonPositiveDuration(t *testing.T) {
	if _, err := parseExpiry("-1h", time.Now()); err == nil {
		t.Errorf("expected an error for a non-positive duration")
	}
	if _, err := parseExpiry("0h", time.Now()); err == nil {
		t.Errorf("expected an error for a zero duration")
	}
}

func TestResolvePrincipalByIDAndByName(t *testing.T) {
	deps, _, _, _, _ := newTestDeps(t)
	principal := setupPrincipal(t, deps, "by-name-or-id", identity.KindMachine, identity.RoleViewer, "")

	withServiceAfter(t, deps, func(ctx context.Context, svc identity.Service) {
		byID, err := resolvePrincipal(ctx, svc, principal.ID)
		if err != nil || byID.ID != principal.ID {
			t.Errorf("resolvePrincipal(id) = %+v, %v", byID, err)
		}
		byName, err := resolvePrincipal(ctx, svc, principal.Name)
		if err != nil || byName.ID != principal.ID {
			t.Errorf("resolvePrincipal(name) = %+v, %v", byName, err)
		}
		if _, err := resolvePrincipal(ctx, svc, "nope"); err == nil {
			t.Errorf("resolvePrincipal(unknown) succeeded, want an error")
		}
	})
}

func TestReadPasswordNonTerminalStdinReadsOneLine(t *testing.T) {
	stderr := &bytes.Buffer{}
	got, err := readPassword("prompt: ", strings.NewReader("secret-value\nnext-line\n"), stderr)
	if err != nil {
		t.Fatalf("readPassword: %v", err)
	}
	if got != "secret-value" {
		t.Errorf("got %q, want %q", got, "secret-value")
	}
	// A non-terminal stdin (never an *os.File in these tests) must not
	// print the prompt to stderr — that branch is terminal-only.
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty for a non-terminal stdin", stderr.String())
	}
}
