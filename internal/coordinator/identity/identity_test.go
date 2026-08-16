package identity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// fakeClock lets a test drive both this package's own clock and the
// store's bookkeeping clock deterministically from one source, matching
// store's own internal fakeClock (store_test.go) and this codebase's
// established pattern for injectable-clock tests (see CLAUDE.md's
// "Take the clock as an injectable func() time.Time seam").
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed.UTC()
}

// newTestService opens a throwaway *store.Store sharing clock's clock
// (via store.WithClock — see [NewService]'s doc comment for why this
// matters) and constructs a [Service] over it with the identical clock,
// so a test can advance one fakeClock and see both the service's own
// time-based decisions and the store's bookkeeping timestamps move
// together.
func newTestService(t *testing.T, clock *fakeClock, opts ...Option) (svc Service, st *store.Store, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now
	if clock != nil {
		now = clock.now
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dataDir = filepath.Join(dir, "data")
	svc = NewService(st, now, dataDir, opts...)
	return svc, st, dataDir
}

// readBootstrapCode reads the raw bootstrap code an operator would read
// off the data volume, for a test that needs to exercise
// [Service.ClaimBootstrap] with a genuinely correct code rather than one
// it fabricates independently of what was actually written.
func readBootstrapCode(t *testing.T, dataDir string) string {
	t.Helper()
	content, err := os.ReadFile(bootstrapFilePath(dataDir))
	if err != nil {
		t.Fatalf("read bootstrap file: %v", err)
	}
	return strings.TrimSpace(string(content))
}

// --- roles and scopes ---

func TestRoleScopes(t *testing.T) {
	cases := []struct {
		role Role
		want []Scope
	}{
		{RoleViewer, []Scope{ScopeNodeRead, ScopeFPPRead, ScopeObservationRead, ScopeEventRead}},
		{RoleOperator, []Scope{ScopeNodeRead, ScopeFPPRead, ScopeObservationRead, ScopeEventRead, ScopeShowMacroRun, ScopeDevicePower, ScopeFPPCommand, ScopeResolumeAction}},
		{RoleAdmin, []Scope{ScopeNodeRead, ScopeFPPRead, ScopeObservationRead, ScopeEventRead, ScopeShowMacroRun, ScopeDevicePower, ScopeFPPCommand, ScopeResolumeAction, ScopeConfigWrite, ScopePrincipalWrite, ScopeAuditRead, ScopeAssetWrite}},
		{RoleScheduler, []Scope{ScopeShowMacroRun}},
	}
	for _, tc := range cases {
		got := tc.role.Scopes()
		gotSorted := append([]Scope(nil), got...)
		wantSorted := append([]Scope(nil), tc.want...)
		sort.Slice(gotSorted, func(i, j int) bool { return gotSorted[i] < gotSorted[j] })
		sort.Slice(wantSorted, func(i, j int) bool { return wantSorted[i] < wantSorted[j] })
		if len(gotSorted) != len(wantSorted) {
			t.Errorf("%s.Scopes() = %v, want %v", tc.role, got, tc.want)
			continue
		}
		for i := range gotSorted {
			if gotSorted[i] != wantSorted[i] {
				t.Errorf("%s.Scopes() = %v, want %v", tc.role, got, tc.want)
				break
			}
		}
	}
}

func TestRoleScopesReturnsFreshCopyEachCall(t *testing.T) {
	first := RoleViewer.Scopes()
	first[0] = "tampered"
	second := RoleViewer.Scopes()
	if second[0] == "tampered" {
		t.Fatalf("mutating one Scopes() call's result affected a later call: %v", second)
	}
}

func TestRoleHas(t *testing.T) {
	if !RoleAdmin.Has(ScopePrincipalWrite) {
		t.Errorf("RoleAdmin.Has(principal:write) = false, want true")
	}
	if RoleViewer.Has(ScopePrincipalWrite) {
		t.Errorf("RoleViewer.Has(principal:write) = true, want false")
	}
	if RoleScheduler.Has(ScopeNodeRead) {
		t.Errorf("RoleScheduler.Has(node:read) = true, want false — scheduler holds only show:macro:run")
	}
}

func TestParseRole(t *testing.T) {
	for _, s := range []string{"viewer", "operator", "admin", "scheduler"} {
		got, err := ParseRole(s)
		if err != nil {
			t.Errorf("ParseRole(%q): %v", s, err)
		}
		if string(got) != s {
			t.Errorf("ParseRole(%q) = %q", s, got)
		}
	}
}

func TestParseRoleUnknownIsError(t *testing.T) {
	_, err := ParseRole("superuser")
	if !errors.Is(err, ErrUnknownRole) {
		t.Errorf("error = %v, want ErrUnknownRole", err)
	}
}

// --- password hashing ---

func TestHashAndVerifyPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Errorf("VerifyPassword with the correct password = false, want true")
	}
}

func TestVerifyPasswordWrongPasswordFails(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, err := VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Errorf("VerifyPassword with the wrong password = true, want false")
	}
}

func TestHashPasswordUsesFreshSaltEachCall(t *testing.T) {
	h1, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	h2, err := HashPassword("same password")
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("two hashes of the same password with fresh salts were identical: %q", h1)
	}
}

func TestHashPasswordEncodesFixedADR024Parameters(t *testing.T) {
	hash, err := HashPassword("x")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// ADR-024 decision 1: memory 64 MiB (65536 KiB), time cost 2, parallelism 1.
	want := "$argon2id$v=19$m=65536,t=2,p=1$"
	if !strings.HasPrefix(hash, want) {
		t.Errorf("hash = %q, want prefix %q", hash, want)
	}
}

func TestVerifyPasswordMalformedHash(t *testing.T) {
	_, err := VerifyPassword("not-a-phc-string", "anything")
	if !errors.Is(err, ErrMalformedPasswordHash) {
		t.Errorf("error = %v, want ErrMalformedPasswordHash", err)
	}
}

// --- tokens ---

func TestGenerateTokenHasFixedPrefix(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(tok.Value, TokenPrefix) {
		t.Errorf("token = %q, want prefix %q", tok.Value, TokenPrefix)
	}
}

func TestGenerateTokenHasAtLeast128BitsOfRandomness(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	random := strings.TrimPrefix(tok.Value, TokenPrefix)
	// base64url with no padding: 4 chars per 3 bytes, so bits >=
	// len(random) * 6 (each base64 char carries 6 bits) is a safe lower
	// bound regardless of exact byte count.
	bits := len(random) * 6
	if bits < 128 {
		t.Fatalf("token random portion = %d chars (~%d bits), want >= 128 bits", len(random), bits)
	}
}

func TestGenerateTokenDigestMatchesHashToken(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tok.Digest != HashToken(tok.Value) {
		t.Errorf("Digest = %q, want HashToken(Value) = %q", tok.Digest, HashToken(tok.Value))
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	if a.Value == b.Value {
		t.Fatalf("two generated tokens were identical: %q", a.Value)
	}
}

func TestGenerateTokenHintIsShortAndNonEmpty(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tok.Hint == "" {
		t.Fatalf("Hint is empty")
	}
	if len(tok.Hint) >= len(strings.TrimPrefix(tok.Value, TokenPrefix)) {
		t.Fatalf("Hint (%q) is not shorter than the token's random portion — it must not be reconstructable from the hint", tok.Hint)
	}
}

func TestTokensEqualConstantTime(t *testing.T) {
	if !tokensEqualConstantTime("abc", "abc") {
		t.Errorf("equal digests compared unequal")
	}
	if tokensEqualConstantTime("abc", "abd") {
		t.Errorf("different digests compared equal")
	}
	if tokensEqualConstantTime("abc", "abcd") {
		t.Errorf("different-length digests compared equal")
	}
}

// --- bootstrap file mechanics ---

func TestGenerateBootstrapCodeUniqueAndNonEmpty(t *testing.T) {
	a, err := generateBootstrapCode()
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := generateBootstrapCode()
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	if a == "" || b == "" {
		t.Fatalf("generated code is empty")
	}
	if a == b {
		t.Fatalf("two generated codes were identical: %q", a)
	}
}

func TestWriteBootstrapFilePermissionsAreRestrictive(t *testing.T) {
	dir := t.TempDir()
	if err := writeBootstrapFile(dir, "ABCDEF"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(bootstrapFilePath(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("bootstrap file permissions = %v, want 0600", perm)
	}
	content, err := os.ReadFile(bootstrapFilePath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(content)) != "ABCDEF" {
		t.Errorf("file content = %q, want ABCDEF", content)
	}
}

func TestDeleteBootstrapFileIsNotAnErrorWhenAlreadyGone(t *testing.T) {
	dir := t.TempDir()
	if err := deleteBootstrapFile(dir); err != nil {
		t.Errorf("delete of a never-written file: %v, want nil", err)
	}
}

// --- Service: bootstrap lifecycle ---

// TestHasAnyPrincipalNeverGeneratesBootstrapFile closes a review finding:
// HasAnyPrincipal used to generate/maintain the bootstrap code and file as
// a documented side effect, which meant an unauthenticated caller of the
// API's GET /api/v1/session (that endpoint's only job is to call this
// method) silently reissued an expired code on the very next
// unauthenticated poll — the code's own expiry bounded nothing in
// practice, "a window that stays open with rotating contents" rather than
// a bounded, host-triggered one. HasAnyPrincipal is now a pure query;
// [EnsureBootstrap] is the only method that may create the file — proven
// by the two tests immediately below this one.
func TestHasAnyPrincipalNeverGeneratesBootstrapFile(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, dataDir := newTestService(t, clock)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		has, err := svc.HasAnyPrincipal(ctx)
		if err != nil {
			t.Fatalf("has any principal (call %d): %v", i, err)
		}
		if has {
			t.Fatalf("HasAnyPrincipal = true on a fresh service, want false")
		}
	}

	if _, err := os.Stat(bootstrapFilePath(dataDir)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file exists after repeated HasAnyPrincipal calls with EnsureBootstrap never called: err = %v", err)
	}
}

func TestEnsureBootstrapGeneratesBootstrapFileAndRow(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, dataDir := newTestService(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}

	has, err := svc.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal: %v", err)
	}
	if has {
		t.Fatalf("HasAnyPrincipal = true on a fresh service, want false")
	}

	rec, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap after EnsureBootstrap: %v", err)
	}
	if rec.ClaimedAt != nil {
		t.Errorf("bootstrap already claimed on a fresh service")
	}

	code := readBootstrapCode(t, dataDir)
	if code == "" {
		t.Fatalf("bootstrap file is empty")
	}
	if hashBootstrapCode(code) != rec.CodeDigest {
		t.Errorf("bootstrap file's code does not hash to the stored digest — file and DB row disagree")
	}
}

func TestEnsureBootstrapDoesNotRegenerateAValidUnclaimedCode(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, _ := newTestService(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap: %v", err)
	}

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
	second, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap again: %v", err)
	}

	if first.CodeDigest != second.CodeDigest {
		t.Errorf("bootstrap code digest changed across two EnsureBootstrap calls with nothing claimed or expired: %q -> %q", first.CodeDigest, second.CodeDigest)
	}
}

func TestEnsureBootstrapRegeneratesAnExpiredCode(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, _ := newTestService(t, clock, WithBootstrapCodeTTL(time.Hour))
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap: %v", err)
	}

	clock.advance(2 * time.Hour) // past the 1-hour TTL

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
	second, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap again: %v", err)
	}

	if first.CodeDigest == second.CodeDigest {
		t.Errorf("bootstrap code digest unchanged after its TTL elapsed, want a freshly generated one")
	}
}

// TestEnsureBootstrapIsNoOpOnceAPrincipalExists proves the OTHER half of
// the HasAnyPrincipal/EnsureBootstrap split: EnsureBootstrap must not
// (re)create a bootstrap file once a principal exists — the coordinator's
// own periodic watchUnclaimedBootstrap loop calls it unconditionally on
// every tick regardless of claim state, so this is a real, not merely
// theoretical, calling pattern.
func TestEnsureBootstrapIsNoOpOnceAPrincipalExists(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, dataDir := newTestService(t, clock)
	ctx := context.Background()

	if _, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleViewer, "some-password"); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}

	if _, err := os.Stat(bootstrapFilePath(dataDir)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file exists after EnsureBootstrap with a principal already present: err = %v", err)
	}
}

func TestHasAnyPrincipalTrueOnceAPrincipalExists(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	if _, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleViewer, "some-password"); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	has, err := svc.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal: %v", err)
	}
	if !has {
		t.Errorf("HasAnyPrincipal = false after creating a principal, want true")
	}
}

// TestHasAnyPrincipalTrueOnceAPrincipalExistsWithReservedPresent proves the
// reserved principal never masks a genuine one: once a real human principal
// exists, HasAnyPrincipal stays true and EnsureBootstrap stays a no-op
// regardless of whether the reserved recovery principal also exists.
func TestHasAnyPrincipalTrueOnceAPrincipalExistsWithReservedPresent(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, dataDir := newTestService(t, clock)
	ctx := context.Background()

	if _, err := svc.EnsureReservedRecoveryPrincipal(ctx); err != nil {
		t.Fatalf("ensure reserved recovery principal: %v", err)
	}
	if _, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleViewer, "some-password"); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	has, err := svc.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal: %v", err)
	}
	if !has {
		t.Errorf("HasAnyPrincipal = false with a real principal and the reserved principal both present, want true")
	}

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	if _, err := os.Stat(bootstrapFilePath(dataDir)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file exists after EnsureBootstrap with a real principal present, want none")
	}
}

// TestFirstBootWithReservedRecoveryPrincipalAlreadyPresent is the
// regression test for the first-boot bug: coordinator.go creates the
// built-in recovery principal before EnsureBootstrap is ever consulted, so
// this reproduces that exact order. Before the fix, HasAnyPrincipal counted
// the reserved principal, EnsureBootstrap treated the deployment as already
// claimed, and no bootstrap file was ever written.
func TestFirstBootWithReservedRecoveryPrincipalAlreadyPresent(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, dataDir := newTestService(t, clock)
	ctx := context.Background()

	if _, err := svc.EnsureReservedRecoveryPrincipal(ctx); err != nil {
		t.Fatalf("ensure reserved recovery principal: %v", err)
	}

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	code := readBootstrapCode(t, dataDir)
	if code == "" {
		t.Fatalf("bootstrap file is empty with only the reserved principal present")
	}

	admin, err := svc.ClaimBootstrap(ctx, code, "operator", "a-strong-password", "phone", "", FormPassword, clock.now())
	if err != nil {
		t.Fatalf("claim bootstrap: %v", err)
	}
	if admin.Role != RoleAdmin || admin.Kind != KindHuman || admin.Reserved {
		t.Errorf("created principal = %+v, want Role=admin Kind=human Reserved=false", admin)
	}

	authenticated, err := svc.AuthenticatePassword(ctx, "operator", "a-strong-password")
	if err != nil {
		t.Fatalf("authenticate as newly created admin: %v", err)
	}
	if authenticated.ID != admin.ID {
		t.Errorf("authenticated principal ID = %q, want %q", authenticated.ID, admin.ID)
	}
}

func TestClaimBootstrapCreatesAdminAndInvalidatesCode(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, dataDir := newTestService(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	code := readBootstrapCode(t, dataDir)

	admin, err := svc.ClaimBootstrap(ctx, code, "operator", "a-strong-password", "phone", "", FormPassword, clock.now())
	if err != nil {
		t.Fatalf("claim bootstrap: %v", err)
	}
	if admin.Role != RoleAdmin || admin.Kind != KindHuman {
		t.Errorf("created principal = %+v, want Role=admin Kind=human", admin)
	}

	// The code must now be single-use: claiming again with the SAME code
	// must fail, even though nothing else has changed.
	if _, err := svc.ClaimBootstrap(ctx, code, "someone-else", "another-password", "phone", "", FormPassword, clock.now()); !errors.Is(err, ErrBootstrapClaimed) {
		t.Errorf("second claim with the same code: error = %v, want ErrBootstrapClaimed", err)
	}

	// The file must be gone.
	if _, err := os.Stat(bootstrapFilePath(dataDir)); !os.IsNotExist(err) {
		t.Errorf("bootstrap file still exists after a successful claim: err = %v", err)
	}

	// Authenticating with the new admin's password must now work.
	authenticated, err := svc.AuthenticatePassword(ctx, "operator", "a-strong-password")
	if err != nil {
		t.Fatalf("authenticate as newly created admin: %v", err)
	}
	if authenticated.ID != admin.ID {
		t.Errorf("authenticated principal ID = %q, want %q", authenticated.ID, admin.ID)
	}

	_ = st // st is used by other tests in this file via the shared helper
}

// TestClaimBootstrapAuditEntryRecordsCallerSuppliedFormAndDeviceLabel is
// F6's review finding, reproduced directly: a host-shell claim
// (cmd/showmesh-coordinator's `bootstrap` subcommand, form FormCLI) and a
// network claim (POST /api/v1/bootstrap, form FormPassword) used to write
// byte-identical "bootstrap.claim" audit entries — both hardcoded
// Form: FormPassword regardless of which credential was actually used,
// and neither carried the device label at all. ADR-024 decision 11
// requires the entry to record the credential form and which credential
// was used; this proves both are now threaded through from the caller
// rather than assumed.
func TestClaimBootstrapAuditEntryRecordsCallerSuppliedFormAndDeviceLabel(t *testing.T) {
	for _, tt := range []struct {
		name        string
		form        CredentialForm
		deviceLabel string
	}{
		{"network claim records FormPassword", FormPassword, "laptop"},
		{"host-shell claim records FormCLI", FormCLI, "bootstrap-cli"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
			svc, _, dataDir := newTestService(t, clock)
			ctx := context.Background()

			if err := svc.EnsureBootstrap(ctx); err != nil {
				t.Fatalf("ensure bootstrap: %v", err)
			}
			code := readBootstrapCode(t, dataDir)

			admin, err := svc.ClaimBootstrap(ctx, code, "operator", "a-strong-password", tt.deviceLabel, "203.0.113.5", tt.form, clock.now())
			if err != nil {
				t.Fatalf("claim bootstrap: %v", err)
			}

			entries, err := svc.ListAudit(ctx, 0, 10)
			if err != nil {
				t.Fatalf("list audit: %v", err)
			}
			var found *AuditEntry
			for i := range entries {
				if entries[i].Action == "bootstrap.claim" && entries[i].Target == admin.ID {
					found = &entries[i]
				}
			}
			if found == nil {
				t.Fatalf("no bootstrap.claim audit entry found for principal %q among %d entries", admin.ID, len(entries))
			}
			if found.Form != tt.form {
				t.Errorf("Form = %q, want %q", found.Form, tt.form)
			}
			if got, _ := found.Params["deviceLabel"].(string); got != tt.deviceLabel {
				t.Errorf("Params[deviceLabel] = %q, want %q", got, tt.deviceLabel)
			}
		})
	}
}

func TestClaimBootstrapWrongCodeFails(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}

	_, err := svc.ClaimBootstrap(ctx, "definitely-the-wrong-code", "operator", "password", "phone", "", FormPassword, clock.now())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestClaimBootstrapExpiredFails(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, dataDir := newTestService(t, clock, WithBootstrapCodeTTL(time.Hour))
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	code := readBootstrapCode(t, dataDir)

	clock.advance(2 * time.Hour)

	_, err := svc.ClaimBootstrap(ctx, code, "operator", "password", "phone", "", FormPassword, clock.now())
	if !errors.Is(err, ErrBootstrapExpired) {
		t.Errorf("error = %v, want ErrBootstrapExpired", err)
	}
}

func TestClaimBootstrapNoCodeAvailableFails(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	// Deliberately never calling HasAnyPrincipal first, so no bootstrap
	// row has ever been generated.
	_, err := svc.ClaimBootstrap(ctx, "any-code", "operator", "password", "phone", "", FormPassword, clock.now())
	if !errors.Is(err, ErrBootstrapNotAvailable) {
		t.Errorf("error = %v, want ErrBootstrapNotAvailable", err)
	}
}

// --- Service: password authentication ---

func TestAuthenticatePasswordUnknownNameIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	_, err := svc.AuthenticatePassword(context.Background(), "no-such-user", "whatever")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticatePasswordWrongPasswordIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	if _, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "correct-password"); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, err := svc.AuthenticatePassword(ctx, "operator", "wrong-password")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

// TestEmptyPasswordPrincipalIsNeverAuthenticatableByPassword closes a
// review finding: this file (and cmd/showmesh-coordinator's own
// subcommands_test.go) creates a large number of "empty password"
// principals purely to skip argon2id's real cost for tests that only need
// SOME principal to exist (see this file's own newTestService callers
// passing "" as password, and subcommands_test.go's setupPrincipal doc
// comment naming the same convention explicitly) — but neither suite ever
// asserted that an empty-password principal is actually unauthenticatable
// BY password, which is the one property every one of those speed
// shortcuts is silently trusting. [Service.CreatePrincipal] stores an
// empty PasswordHash for password == "" (see that method), and
// [VerifyPassword] treats an empty string as a malformed PHC hash — this
// test pins that chain end to end: neither an empty password nor any
// other guess ever authenticates a principal created this way.
func TestEmptyPasswordPrincipalIsNeverAuthenticatableByPassword(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	if _, err := svc.CreatePrincipal(ctx, "speed-only", KindMachine, RoleScheduler, ""); err != nil {
		t.Fatalf("create principal with empty password: %v", err)
	}

	if _, err := svc.AuthenticatePassword(ctx, "speed-only", ""); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("AuthenticatePassword with an empty password against an empty-password principal: error = %v, want ErrInvalidCredential", err)
	}
	if _, err := svc.AuthenticatePassword(ctx, "speed-only", "anything-at-all"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("AuthenticatePassword with a guessed password against an empty-password principal: error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticatePasswordSucceeds(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	created, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "correct-password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	got, err := svc.AuthenticatePassword(ctx, "operator", "correct-password")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("authenticated ID = %q, want %q", got.ID, created.ID)
	}
}

// TestAuthenticatePasswordDisabledAccountWithCorrectPasswordReturnsErrDisabled
// and TestAuthenticatePasswordDisabledAccountWithWrongPasswordStaysInvalidCredential
// together pin the ordering AuthenticatePassword's doc comment argues for:
// the disabled state is revealed ONLY once the password is already known
// to be correct, never to a caller who does not know the password. If a
// future edit reorders the checks (disabled before password), the second
// test here is what catches it — the first test alone would still pass.
func TestAuthenticatePasswordDisabledAccountWithCorrectPasswordReturnsErrDisabled(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	created, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "correct-password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if _, err := svc.SetDisabled(ctx, created.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_, err = svc.AuthenticatePassword(ctx, "operator", "correct-password")
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("error = %v, want ErrDisabled", err)
	}
}

func TestAuthenticatePasswordDisabledAccountWithWrongPasswordStaysInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	created, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "correct-password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if _, err := svc.SetDisabled(ctx, created.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_, err = svc.AuthenticatePassword(ctx, "operator", "wrong-password")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential (a wrong password must never reveal disabled state)", err)
	}
	if errors.Is(err, ErrDisabled) {
		t.Errorf("error also matches ErrDisabled — a wrong password must not leak account state")
	}
}

// --- Service: token authentication ---

func TestIssueAndAuthenticateToken(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	authed, err := svc.AuthenticateToken(ctx, tok.Value)
	if err != nil {
		t.Fatalf("authenticate token: %v", err)
	}
	if authed.Principal.ID != p.ID {
		t.Errorf("authenticated principal ID = %q, want %q", authed.Principal.ID, p.ID)
	}
	if authed.Form != FormToken {
		t.Errorf("Form = %q, want %q", authed.Form, FormToken)
	}
	if authed.CredentialID == tok.Value || authed.CredentialID == tok.Digest {
		t.Errorf("CredentialID = %q, must be neither the raw token nor its digest", authed.CredentialID)
	}
}

func TestAuthenticateTokenUnknownIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	_, err := svc.AuthenticateToken(context.Background(), TokenPrefix+"not-a-real-token")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateTokenRevokedIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	tokens, err := svc.ListTokens(ctx, p.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list tokens: %+v, %v", tokens, err)
	}
	if err := svc.RevokeToken(ctx, tokens[0].ID); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	_, err = svc.AuthenticateToken(ctx, tok.Value)
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateTokenExpiredIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	expiry := clock.now().Add(time.Hour)
	tok, err := svc.IssueToken(ctx, p.ID, "short-lived", &expiry)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	clock.advance(2 * time.Hour)

	_, err = svc.AuthenticateToken(ctx, tok.Value)
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateTokenDisabledPrincipalIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if _, err := svc.SetDisabled(ctx, p.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// The seam contract states AuthenticateToken collapses disabled into
	// ErrInvalidCredential (unlike AuthenticateSession/AuthenticatePassword,
	// which use ErrDisabled) — see AuthenticateToken's doc comment.
	_, err = svc.AuthenticateToken(ctx, tok.Value)
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
	if errors.Is(err, ErrDisabled) {
		t.Errorf("error also matches ErrDisabled, want it collapsed into ErrInvalidCredential for a token")
	}
}

// TestAuthenticateTokenStaleGenerationIsInvalidCredential closes review
// finding 4: AuthenticateToken used to check revocation, expiry, and
// disabled, but never generation, and tokens carried no generation at
// all — so a SetRole/SetDisabled(true)/RevokeAllSessions generation bump
// closed a cookie-backed SSE stream within one revalidation tick and did
// NOTHING to a token-backed one. ADR-024 decision 12's stale-scope bound
// ("a role change... increments the generation counter... which closes
// open streams and forces a re-fetch") therefore did not hold for token
// clients at all — which includes the UI's own bearer-paste break-glass
// path (decision 5). migrations.go's schemaV5 doc comment records the
// fix this test pins: principal_tokens.generation, stamped at issue time
// exactly like principal_sessions.generation, checked here exactly like
// checkSession already checks it for a session.
func TestAuthenticateTokenStaleGenerationIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	// Sanity: the token authenticates before any generation bump — so the
	// rejection asserted below is attributable to the bump, not to some
	// other, unrelated setup mistake.
	if _, err := svc.AuthenticateToken(ctx, tok.Value); err != nil {
		t.Fatalf("token does not authenticate before any generation bump: %v", err)
	}

	// Any of SetRole/SetDisabled(true)/RevokeAllSessions bumps generation
	// identically (see store.Store.bumpPrincipalGenerationTx); SetRole is
	// used here because it is also decision 12's own named example.
	if _, err := svc.SetRole(ctx, p.ID, RoleViewer); err != nil {
		t.Fatalf("set role: %v", err)
	}

	if _, err := svc.AuthenticateToken(ctx, tok.Value); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("AuthenticateToken after a generation bump on the owning principal: error = %v, want ErrInvalidCredential", err)
	}
}

// TestRevalidateTokenAndRevalidateSessionNeverTouchLastUsedAt closes
// review finding 11's third smaller item at this package's own boundary
// (internal/coordinator/api/stream_auth_test.go's
// TestStreamRevalidationDoesNotSlideSessionLastUsedAt proves the same
// property end to end through the API layer): [Service.RevalidateSession]
// and [Service.RevalidateToken] must perform every check
// AuthenticateSession/AuthenticateToken do, EXCEPT the final
// TouchSession/TouchToken write — a periodic SSE revalidation tick is not
// an operator using the credential, and touching LastUsedAt there would
// make ADR-024 decision 5's 90-day idle window unenforceable for exactly
// the abandoned-tab case it exists to catch.
func TestRevalidateTokenAndRevalidateSessionNeverTouchLastUsedAt(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, _ := newTestService(t, clock)
	ctx := context.Background()

	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if _, err := svc.RevalidateToken(ctx, tok.Value); err != nil {
		t.Fatalf("RevalidateToken: %v", err)
	}
	tokRec, err := st.GetTokenByDigest(ctx, HashToken(tok.Value))
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if tokRec.LastUsedAt != nil {
		t.Errorf("token LastUsedAt = %v after RevalidateToken, want nil (never touched by revalidation)", tokRec.LastUsedAt)
	}

	sess, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.RevalidateSession(ctx, secret, clock.now()); err != nil {
		t.Fatalf("RevalidateSession: %v", err)
	}
	sessRec, err := st.GetSessionByDigest(ctx, hashSessionSecret(secret))
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !sessRec.LastUsedAt.Equal(sess.CreatedAt) {
		t.Errorf("session LastUsedAt = %v after RevalidateSession, want unchanged from CreatedAt %v (never touched by revalidation)", sessRec.LastUsedAt, sess.CreatedAt)
	}
}

// TestRevalidateTokenStillRejectsGenerationStale proves
// RevalidateToken is not merely a no-touch pass-through that skips every
// check along with the touch: it must still enforce the identical
// generation comparison AuthenticateToken does, so a revoked/demoted
// principal's token-backed SSE stream is actually closed by
// [Hub.revalidateSubscribers], not kept alive by a revalidation path that
// silently stopped checking anything.
func TestRevalidateTokenStillRejectsGenerationStale(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if _, err := svc.SetRole(ctx, p.ID, RoleViewer); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if _, err := svc.RevalidateToken(ctx, tok.Value); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("RevalidateToken after a generation bump: error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateTokenNeverAppearsInListTokens(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "scheduler-bot", KindMachine, RoleScheduler, "")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	tok, err := svc.IssueToken(ctx, p.ID, "showmeshctl", nil)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	tokens, err := svc.ListTokens(ctx, p.ID)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("len(tokens) = %d, want 1", len(tokens))
	}
	if tokens[0].ID == tok.Value || tokens[0].ID == tok.Digest || tokens[0].Hint == tok.Value {
		t.Errorf("ListTokens exposed the raw token or its digest: %+v", tokens[0])
	}
}

// --- Service: session authentication ---

func TestCreateAndAuthenticateSession(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	session, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.ID == secret {
		t.Fatalf("Session.ID equals the raw cookie secret — this is exactly the disclosure this package's doc comment says must never happen")
	}

	authed, err := svc.AuthenticateSession(ctx, secret, clock.now())
	if err != nil {
		t.Fatalf("authenticate session: %v", err)
	}
	if authed.Principal.ID != p.ID {
		t.Errorf("authenticated principal ID = %q, want %q", authed.Principal.ID, p.ID)
	}
	if authed.CredentialID != session.ID {
		t.Errorf("CredentialID = %q, want the session row id %q", authed.CredentialID, session.ID)
	}
}

func TestAuthenticateSessionUnknownIsInvalidCredential(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	_, err := svc.AuthenticateSession(context.Background(), "not-a-real-secret", clock.now())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestAuthenticateSessionSlidesLastUsedAt(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	session, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	clock.advance(10 * 24 * time.Hour)
	if _, err := svc.AuthenticateSession(ctx, secret, clock.now()); err != nil {
		t.Fatalf("authenticate session: %v", err)
	}

	sessions, err := st.ListSessions(ctx, p.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions: %+v, %v", sessions, err)
	}
	if sessions[0].ID != session.ID {
		t.Fatalf("unexpected session row: %+v", sessions[0])
	}
	if !sessions[0].LastUsedAt.Equal(clock.now()) {
		t.Errorf("LastUsedAt = %v, want it slid to %v", sessions[0].LastUsedAt, clock.now())
	}
}

// TestAuthenticateSessionRejectsStaleGeneration is the test whose name is
// the claim ADR-024 decision 5's central mechanism makes: revoking a
// session by bumping the owning principal's generation must reject a
// session created before the bump, without touching that session's own
// row at all. Breaking the `rec.Generation < principal.Generation` check
// in AuthenticateSession (e.g. deleting it, or flipping the comparison)
// must make this test fail — verified by hand during this package's
// mutation check.
func TestAuthenticateSessionRejectsStaleGeneration(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A password change is the concrete decision-5 trigger; RevokeAllSessions
	// (used here) is the same mechanism minus the credential change.
	if err := svc.RevokeAllSessions(ctx, p.ID); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}

	_, err = svc.AuthenticateSession(ctx, secret, clock.now())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential — a session predating a generation bump must be rejected", err)
	}
}

func TestAuthenticateSessionAfterRevokeAllStillAllowsANewSession(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if err := svc.RevokeAllSessions(ctx, p.ID); err != nil {
		t.Fatalf("revoke all sessions: %v", err)
	}

	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session after revoke-all: %v", err)
	}
	if _, err := svc.AuthenticateSession(ctx, secret, clock.now()); err != nil {
		t.Errorf("authenticate a session created AFTER revoke-all: %v, want success", err)
	}
}

// TestAuthenticateSessionRejectsIdleExpiry is the test whose name is the
// claim ADR-024 decision 5's sliding-idle rule makes: a session untouched
// for more than SessionMaxIdle must be rejected. Breaking the idle check
// (e.g. deleting it or comparing against the wrong duration) must make
// this fail — verified by hand during this package's mutation check.
func TestAuthenticateSessionRejectsIdleExpiry(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, st, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	session, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	clock.advance(SessionMaxIdle + time.Hour)

	_, err = svc.AuthenticateSession(ctx, secret, clock.now())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential after %v of idle time", err, SessionMaxIdle+time.Hour)
	}

	// The rejected attempt must not have slid LastUsedAt — a rejected
	// authentication is not "use" for decision 5's purposes.
	sessions, err := st.ListSessions(ctx, p.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("list sessions: %+v, %v", sessions, err)
	}
	if sessions[0].ID != session.ID {
		t.Fatalf("unexpected session row: %+v", sessions[0])
	}
	if !sessions[0].LastUsedAt.Equal(mustTime(t, "2026-01-01T00:00:00Z")) {
		t.Errorf("LastUsedAt = %v, want unchanged at creation time (a rejected attempt must not slide it)", sessions[0].LastUsedAt)
	}
}

func TestAuthenticateSessionJustUnderIdleLimitStillSucceeds(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	clock.advance(SessionMaxIdle - time.Hour)
	if _, err := svc.AuthenticateSession(ctx, secret, clock.now()); err != nil {
		t.Errorf("authenticate just under the idle limit: %v, want success", err)
	}
}

func TestAuthenticateSessionDisabledPrincipalReturnsErrDisabled(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.SetDisabled(ctx, p.ID, true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	_, err = svc.AuthenticateSession(ctx, secret, clock.now())
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("error = %v, want ErrDisabled", err)
	}
}

func TestRevokeSessionThenAuthenticateFails(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	session, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.RevokeSession(ctx, session.ID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	_, err = svc.AuthenticateSession(ctx, secret, clock.now())
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("error = %v, want ErrInvalidCredential", err)
	}
}

func TestListSessionsNeverExposesTheCookieSecret(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	sessions, err := svc.ListSessions(ctx, p.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}
	if sessions[0].ID == secret || sessions[0].ID == hashSessionSecret(secret) {
		t.Errorf("ListSessions exposed the raw secret or its digest as Session.ID: %+v", sessions[0])
	}
}

// --- Service: audit ---

func TestWriteAndListAuditRoundTripsParams(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	err := svc.WriteAudit(ctx, AuditEntry{
		Kind:   AuditAdmin,
		Action: "principal.create",
		Target: "principal:p-1",
		Params: map[string]any{"role": "operator"},
	})
	if err != nil {
		t.Fatalf("write audit: %v", err)
	}

	got, err := svc.ListAudit(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Params["role"] != "operator" {
		t.Errorf("Params = %+v, want role=operator", got[0].Params)
	}
}

// TestWriteAuditHonorsCallerTimestamp is Step 7 seam A review defect 5's
// identity-level regression test: WriteAudit must pass
// AuditEntry.Timestamp through to the store rather than dropping it
// (store.AppendAuditEntry then honors it — see that package's own
// TestAppendAuditEntryHonorsCallerRecordedAt). The Service's own clock is
// fixed to a DIFFERENT instant than the one passed in the entry, so a
// regression to "the store always stamps its own now" fails rather than
// passing by coincidence.
func TestWriteAuditHonorsCallerTimestamp(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	// One hour before the service's clock — distinguishable from it, but
	// safely inside the store's default 180-day audit retention window so
	// the amortized on-insert prune pass does not immediately remove the
	// row and produce a false "honored" failure (store's own
	// TestAppendAuditEntryHonorsCallerRecordedAt hit exactly this).
	caller := mustTime(t, "2025-12-31T23:00:00Z")
	if err := svc.WriteAudit(ctx, AuditEntry{Kind: AuditAdmin, Action: "x", Timestamp: caller}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	got, err := svc.ListAudit(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if !got[0].Timestamp.Equal(caller) {
		t.Errorf("Timestamp = %v, want the caller-supplied %v (service clock was %v — this must not be it)",
			got[0].Timestamp, caller, clock.t)
	}
}

// TestAuditedWriteHonorsCallerTimestamp is TestWriteAuditHonorsCallerTimestamp
// for AuditedWrite's own conversion, the identical fix applied at the
// second call site.
func TestAuditedWriteHonorsCallerTimestamp(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	// One hour before the service's clock — distinguishable from it, but
	// safely inside the store's default 180-day audit retention window so
	// the amortized on-insert prune pass does not immediately remove the
	// row and produce a false "honored" failure (store's own
	// TestAppendAuditEntryHonorsCallerRecordedAt hit exactly this).
	caller := mustTime(t, "2025-12-31T23:00:00Z")
	err := svc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		return AuditEntry{Kind: AuditAdmin, Action: "x", Timestamp: caller}, nil
	})
	if err != nil {
		t.Fatalf("audited write: %v", err)
	}

	got, err := svc.ListAudit(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if !got[0].Timestamp.Equal(caller) {
		t.Errorf("Timestamp = %v, want the caller-supplied %v (service clock was %v — this must not be it)",
			got[0].Timestamp, caller, clock.t)
	}
}

// TestAuditRecordsDispatchAndOutcomeAsSeparateEntries exercises ADR-024
// decision 11's central shape end to end through the identity package's
// own domain types, mirroring store's TestAuditLogNeverUpdatedOnlyInserted
// one layer up.
func TestAuditRecordsDispatchAndOutcomeAsSeparateEntries(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()

	if err := svc.WriteAudit(ctx, AuditEntry{Kind: AuditDispatch, Action: "fpp:command", CommandID: "cmd-1"}); err != nil {
		t.Fatalf("write dispatch: %v", err)
	}
	if err := svc.WriteAudit(ctx, AuditEntry{Kind: AuditOutcome, Action: "fpp:command", CommandID: "cmd-1", Outcome: "succeeded"}); err != nil {
		t.Fatalf("write outcome: %v", err)
	}

	got, err := svc.ListAudit(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Kind != AuditDispatch || got[0].Outcome != "" {
		t.Errorf("first entry = %+v, want Kind=dispatch and empty Outcome", got[0])
	}
	if got[1].Kind != AuditOutcome || got[1].Outcome != "succeeded" {
		t.Errorf("second entry = %+v, want Kind=outcome and Outcome=succeeded", got[1])
	}
}

// --- Service: principal administration ---

func TestCreatePrincipalDuplicateNamePropagatesError(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	if _, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "password"); err != nil {
		t.Fatalf("create first: %v", err)
	}
	_, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleViewer, "different-password")
	if !errors.Is(err, store.ErrPrincipalNameTaken) {
		t.Errorf("error = %v, want it to wrap store.ErrPrincipalNameTaken", err)
	}
}

func TestSetPasswordBumpsGenerationAndInvalidatesExistingSessions(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleOperator, "old-password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := svc.SetPassword(ctx, p.ID, "new-password"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	if _, err := svc.AuthenticateSession(ctx, secret, clock.now()); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("old session after password change: error = %v, want ErrInvalidCredential", err)
	}
	if _, err := svc.AuthenticatePassword(ctx, "operator", "old-password"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("old password after change: error = %v, want ErrInvalidCredential", err)
	}
	if _, err := svc.AuthenticatePassword(ctx, "operator", "new-password"); err != nil {
		t.Errorf("new password: %v, want success", err)
	}
}

// TestSetRoleChangesRoleAndInvalidatesExistingSessions is [SetRole]'s own
// version of the test directly above it: ADR-024 decision 12's entire
// reason for existing is "a role change... increments the generation
// counter in decision 5, which closes open streams and forces a
// re-fetch". A test that only checked the returned Principal.Role changed
// would miss the property decision 12 actually cares about, so this
// proves both halves — the role itself changed, AND a session minted
// before the change no longer authenticates afterward — in one test,
// mirroring TestSetPasswordBumpsGenerationAndInvalidatesExistingSessions's
// shape exactly.
func TestSetRoleChangesRoleAndInvalidatesExistingSessions(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	ctx := context.Background()
	p, err := svc.CreatePrincipal(ctx, "operator", KindHuman, RoleViewer, "correct-password")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	_, secret, err := svc.CreateSession(ctx, p.ID, p.Name, "phone", "", clock.now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	updated, err := svc.SetRole(ctx, p.ID, RoleAdmin)
	if err != nil {
		t.Fatalf("set role: %v", err)
	}
	if updated.Role != RoleAdmin {
		t.Errorf("Role after SetRole = %q, want %q", updated.Role, RoleAdmin)
	}

	if _, err := svc.AuthenticateSession(ctx, secret, clock.now()); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("old session after role change: error = %v, want ErrInvalidCredential (decision 12's whole guarantee)", err)
	}

	// The principal itself is not locked out by its own role change —
	// only the SESSION minted before it is invalid. A fresh
	// AuthenticatePassword call must see the new role.
	reAuthed, err := svc.AuthenticatePassword(ctx, "operator", "correct-password")
	if err != nil {
		t.Fatalf("re-authenticate after role change: %v", err)
	}
	if reAuthed.Role != RoleAdmin {
		t.Errorf("Role after re-authenticating = %q, want %q", reAuthed.Role, RoleAdmin)
	}
}
