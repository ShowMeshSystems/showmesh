package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationV5AddsIdentityTablesAndPreservesV4Data builds a v1..v4
// database directly (bypassing open/migrate, which always brings a
// database to the newest known version), then reopens it through the
// normal [open] path and checks migration 5 both creates every schemaV5
// table and preserves the v4 data that was already there — the same
// pattern [TestMigrationV4WidensObservationsPrimaryKeyAndPreservesData]
// exercises for schemaV4, applied here to schemaV5. Unlike schemaV2 and
// schemaV4, schemaV5 touches no existing table, so "preservation" here is
// really "migration 5 running at all does not disturb migration 1-4's
// tables" — worth proving explicitly rather than assuming a pure-addition
// migration can't regress an unrelated table.
func TestMigrationV5AddsIdentityTablesAndPreservesV4Data(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, dbFileName)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	for _, s := range []string{schemaV1, schemaV2, schemaV3, schemaV4} {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA user_version = 4`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	now := timeToDB(time.Now())
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO observations (
			resource_kind, resource_id, signal,
			value_kind, value_text, unit,
			observed_at, collected_at, source, quality, valid_for_ns,
			absence, reason, first_seen_at, updated_at
		) VALUES ('fpp', 'player-01', 'fpp.multisync.enabled', 'bool', 'true', '', ?, ?, 'fpp-rest', 'direct', 0, '', '', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatalf("insert v4 observation row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 5): %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d (len(migrations))", version, len(migrations))
	}

	got, err := st.ListObservations(context.Background(), ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations after migration 5: %v", err)
	}
	if len(got) != 1 || got[0].Source != "fpp-rest" || got[0].Value != true {
		t.Fatalf("pre-migration observation not preserved: %+v", got)
	}

	for _, table := range []string{"principals", "principal_tokens", "principal_sessions", "audit_log", "bootstrap"} {
		var name string
		err := st.db.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after migration 5: %v", table, err)
		}
	}

	// Prove the new tables are not just present but usable end to end.
	rec, err := st.CreatePrincipal(context.Background(), PrincipalRecord{
		ID: "p-1", Name: "operator", Kind: "human", Role: "viewer",
	})
	if err != nil {
		t.Fatalf("create principal on migrated database: %v", err)
	}
	if rec.Generation != 0 {
		t.Errorf("Generation = %d, want 0 for a brand-new principal", rec.Generation)
	}
}

// --- principals ---

func TestCreatePrincipalAndGetByIDAndName(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	created, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "operator"})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("CreatedAt/UpdatedAt not stamped: %+v", created)
	}

	byID, err := st.GetPrincipal(ctx, "p-1")
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Name != "operator" {
		t.Errorf("GetPrincipal Name = %q, want operator", byID.Name)
	}

	byName, err := st.GetPrincipalByName(ctx, "operator")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if byName.ID != "p-1" {
		t.Errorf("GetPrincipalByName ID = %q, want p-1", byName.ID)
	}
}

func TestGetPrincipalNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetPrincipal(context.Background(), "no-such-id")
	if !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("error = %v, want ErrPrincipalNotFound", err)
	}
	_, err = st.GetPrincipalByName(context.Background(), "no-such-name")
	if !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("error = %v, want ErrPrincipalNotFound", err)
	}
}

func TestCreatePrincipalDuplicateNameIsErrPrincipalNameTaken(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "operator"}); err != nil {
		t.Fatalf("create first principal: %v", err)
	}
	_, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-2", Name: "operator", Kind: "human", Role: "viewer"})
	if !errors.Is(err, ErrPrincipalNameTaken) {
		t.Errorf("error = %v, want ErrPrincipalNameTaken", err)
	}
}

func TestHasAnyPrincipal(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	has, err := st.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal (empty): %v", err)
	}
	if has {
		t.Errorf("HasAnyPrincipal = true on an empty store, want false")
	}

	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "admin"}); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	has, err = st.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal (non-empty): %v", err)
	}
	if !has {
		t.Errorf("HasAnyPrincipal = false after creating a principal, want true")
	}
}

func TestSetPrincipalPasswordHashBumpsGeneration(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "admin"}); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	gen, err := st.SetPrincipalPasswordHash(ctx, "p-1", "new-hash")
	if err != nil {
		t.Fatalf("set password hash: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation after first password change = %d, want 1", gen)
	}

	rec, err := st.GetPrincipal(ctx, "p-1")
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	if rec.PasswordHash != "new-hash" {
		t.Errorf("PasswordHash = %q, want new-hash", rec.PasswordHash)
	}
	if rec.Generation != 1 {
		t.Errorf("stored Generation = %d, want 1", rec.Generation)
	}
}

func TestSetPrincipalDisabledBumpsGenerationButEnablingDoesNot(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "admin"}); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	gen, err := st.SetPrincipalDisabled(ctx, "p-1", true)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation after disable = %d, want 1 (disabling must invalidate open sessions)", gen)
	}

	rec, err := st.GetPrincipal(ctx, "p-1")
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	if !rec.Disabled {
		t.Errorf("Disabled = false after SetPrincipalDisabled(true)")
	}

	gen, err = st.SetPrincipalDisabled(ctx, "p-1", false)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation after re-enable = %d, want unchanged at 1 (nothing to invalidate)", gen)
	}
}

// TestSetPrincipalRoleBumpsGeneration proves ADR-024 decision 12's
// guarantee has something to hang on at the store layer: a role change
// must bump the generation counter exactly like a password change or a
// disable does, so an open session/stream is closed and forced to
// re-fetch its now-possibly-narrower scope list. It also proves the role
// column itself is actually written, not just the generation counter —
// a test that only checked the generation bump could pass even if
// SetPrincipalRole's UPDATE forgot to touch the role column at all.
func TestSetPrincipalRoleBumpsGeneration(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "viewer"}); err != nil {
		t.Fatalf("create principal: %v", err)
	}

	gen, err := st.SetPrincipalRole(ctx, "p-1", "admin")
	if err != nil {
		t.Fatalf("set role: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation after role change = %d, want 1 (a role change must invalidate open sessions/streams)", gen)
	}

	rec, err := st.GetPrincipal(ctx, "p-1")
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	if rec.Role != "admin" {
		t.Errorf("Role = %q, want %q", rec.Role, "admin")
	}
	if rec.Generation != 1 {
		t.Errorf("Generation = %d, want 1", rec.Generation)
	}

	// A second role change bumps again — this is not a one-shot
	// "first role change only" mechanism.
	gen, err = st.SetPrincipalRole(ctx, "p-1", "operator")
	if err != nil {
		t.Fatalf("set role again: %v", err)
	}
	if gen != 2 {
		t.Errorf("generation after second role change = %d, want 2", gen)
	}
}

// TestSetPrincipalRoleUnknownPrincipalIsError proves SetPrincipalRole
// does not silently succeed against a principal id that does not exist —
// bumpPrincipalGenerationTx's read-current-generation step is what would
// catch this, and this test breaks that path's own claim (its doc
// comment says it returns ErrPrincipalNotFound) to confirm it actually
// does rather than, say, silently reporting generation 1 for a row that
// was never inserted.
func TestSetPrincipalRoleUnknownPrincipalIsError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.SetPrincipalRole(ctx, "no-such-principal", "admin"); !errors.Is(err, ErrPrincipalNotFound) {
		t.Fatalf("SetPrincipalRole(unknown) error = %v, want ErrPrincipalNotFound", err)
	}
}

func TestBumpPrincipalGeneration(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.CreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "admin"}); err != nil {
		t.Fatalf("create principal: %v", err)
	}
	gen, err := st.BumpPrincipalGeneration(ctx, "p-1")
	if err != nil {
		t.Fatalf("bump generation: %v", err)
	}
	if gen != 1 {
		t.Errorf("generation = %d, want 1", gen)
	}
	gen, err = st.BumpPrincipalGeneration(ctx, "p-1")
	if err != nil {
		t.Fatalf("bump generation again: %v", err)
	}
	if gen != 2 {
		t.Errorf("generation = %d, want 2", gen)
	}
}

func TestBumpPrincipalGenerationUnknownPrincipalIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.BumpPrincipalGeneration(context.Background(), "no-such-id")
	if !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("error = %v, want ErrPrincipalNotFound", err)
	}
}

// --- tokens ---

func mustPrincipal(t *testing.T, st *Store) string {
	t.Helper()
	rec, err := st.CreatePrincipal(context.Background(), PrincipalRecord{ID: "p-1", Name: "scheduler-bot", Kind: "machine", Role: "scheduler"})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	return rec.ID
}

func TestCreateAndGetTokenByDigest(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)

	created, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "digest-abc", Hint: "smsh_abcd1234", Label: "showmeshctl"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if created.CreatedAt.IsZero() {
		t.Errorf("CreatedAt not stamped")
	}

	got, err := st.GetTokenByDigest(ctx, "digest-abc")
	if err != nil {
		t.Fatalf("get token by digest: %v", err)
	}
	if got.ID != "t-1" || got.PrincipalID != pid {
		t.Errorf("got = %+v, want ID=t-1 PrincipalID=%s", got, pid)
	}
}

// TestCreateTokenCapturesPrincipalGenerationAtCreationTime mirrors
// TestCreateSessionCapturesPrincipalGenerationAtCreationTime exactly,
// against CreateToken instead of CreateSession — see migrations.go's
// schemaV5 doc comment for why a token needs the identical treatment a
// session already got.
func TestCreateTokenCapturesPrincipalGenerationAtCreationTime(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)

	if _, err := st.BumpPrincipalGeneration(ctx, pid); err != nil {
		t.Fatalf("bump generation: %v", err)
	}

	created, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "digest-abc"})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if created.Generation != 1 {
		t.Errorf("Generation = %d, want 1 (the principal's current generation at creation)", created.Generation)
	}

	got, err := st.GetTokenByDigest(ctx, "digest-abc")
	if err != nil {
		t.Fatalf("get token by digest: %v", err)
	}
	if got.Generation != 1 {
		t.Errorf("stored Generation = %d, want 1", got.Generation)
	}
}

func TestGetTokenByDigestNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetTokenByDigest(context.Background(), "no-such-digest")
	if !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("error = %v, want ErrTokenNotFound", err)
	}
}

func TestCreateTokenDuplicateDigestIsError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "same-digest"}); err != nil {
		t.Fatalf("create first token: %v", err)
	}
	_, err := st.CreateToken(ctx, TokenRecord{ID: "t-2", PrincipalID: pid, Digest: "same-digest"})
	if err == nil {
		t.Fatalf("create token with a duplicate digest succeeded, want an error")
	}
}

func TestRevokeTokenExcludesFromLookupAndListing(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "digest-abc"}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	if err := st.RevokeToken(ctx, "t-1"); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	if _, err := st.GetTokenByDigest(ctx, "digest-abc"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("GetTokenByDigest after revoke: error = %v, want ErrTokenNotFound", err)
	}

	tokens, err := st.ListTokens(ctx, pid)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("ListTokens after revoke = %+v, want empty", tokens)
	}
}

func TestRevokeTokenTwiceIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "digest-abc"}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if err := st.RevokeToken(ctx, "t-1"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := st.RevokeToken(ctx, "t-1"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("second revoke error = %v, want ErrTokenNotFound", err)
	}
}

func TestTouchTokenAdvancesLastUsedAt(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "digest-abc"}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	touchTime := mustTime(t, "2026-01-02T03:04:05Z")
	if err := st.TouchToken(ctx, "t-1", touchTime); err != nil {
		t.Fatalf("touch token: %v", err)
	}

	got, err := st.GetTokenByDigest(ctx, "digest-abc")
	if err != nil {
		t.Fatalf("get token: %v", err)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(touchTime) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, touchTime)
	}
}

func TestListTokensOrderedByCreatedAt(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "d1", Label: "first"}); err != nil {
		t.Fatalf("create token 1: %v", err)
	}
	if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-2", PrincipalID: pid, Digest: "d2", Label: "second"}); err != nil {
		t.Fatalf("create token 2: %v", err)
	}
	got, err := st.ListTokens(ctx, pid)
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	if len(got) != 2 || got[0].ID != "t-1" || got[1].ID != "t-2" {
		t.Fatalf("got = %+v, want [t-1, t-2] in creation order", got)
	}
}

// --- sessions ---

func TestCreateSessionCapturesPrincipalGenerationAtCreationTime(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)

	// Bump the principal's generation BEFORE creating the session, so a
	// session created afterward must capture the bumped value rather than
	// a stale 0 — this is the exact property AuthenticateSession's
	// generation check depends on.
	if _, err := st.BumpPrincipalGeneration(ctx, pid); err != nil {
		t.Fatalf("bump generation: %v", err)
	}

	created, err := st.CreateSession(ctx, SessionRecord{ID: "s-1", PrincipalID: pid, Digest: "digest-abc", DeviceLabel: "phone"}, mustTime(t, "2026-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if created.Generation != 1 {
		t.Errorf("Generation = %d, want 1 (the principal's current generation at creation)", created.Generation)
	}
}

func TestCreateSessionUnknownPrincipalIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.CreateSession(context.Background(), SessionRecord{ID: "s-1", PrincipalID: "no-such-id", Digest: "d"}, time.Now())
	if !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("error = %v, want ErrPrincipalNotFound", err)
	}
}

func TestGetSessionByDigestNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetSessionByDigest(context.Background(), "no-such-digest")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("error = %v, want ErrSessionNotFound", err)
	}
}

func TestRevokeSessionExcludesFromLookupAndListing(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	now := mustTime(t, "2026-01-01T00:00:00Z")
	if _, err := st.CreateSession(ctx, SessionRecord{ID: "s-1", PrincipalID: pid, Digest: "digest-abc"}, now); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := st.RevokeSession(ctx, "s-1"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	if _, err := st.GetSessionByDigest(ctx, "digest-abc"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("GetSessionByDigest after revoke: error = %v, want ErrSessionNotFound", err)
	}
	sessions, err := st.ListSessions(ctx, pid)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("ListSessions after revoke = %+v, want empty", sessions)
	}
}

func TestRevokeSessionTwiceIsNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	if _, err := st.CreateSession(ctx, SessionRecord{ID: "s-1", PrincipalID: pid, Digest: "digest-abc"}, time.Now()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := st.RevokeSession(ctx, "s-1"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := st.RevokeSession(ctx, "s-1"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("second revoke error = %v, want ErrSessionNotFound", err)
	}
}

func TestTouchSessionAdvancesLastUsedAtToCallerSuppliedNow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	pid := mustPrincipal(t, st)
	created := mustTime(t, "2026-01-01T00:00:00Z")
	if _, err := st.CreateSession(ctx, SessionRecord{ID: "s-1", PrincipalID: pid, Digest: "digest-abc"}, created); err != nil {
		t.Fatalf("create session: %v", err)
	}

	touchTime := mustTime(t, "2026-03-15T12:00:00Z")
	if err := st.TouchSession(ctx, "s-1", touchTime); err != nil {
		t.Fatalf("touch session: %v", err)
	}

	got, err := st.GetSessionByDigest(ctx, "digest-abc")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !got.LastUsedAt.Equal(touchTime) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, touchTime)
	}
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want unchanged at %v (touch must not move creation time)", got.CreatedAt, created)
	}
}

// --- bootstrap ---

func TestPutBootstrapAndGet(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	expires := mustTime(t, "2026-02-01T00:00:00Z")

	if _, err := st.PutBootstrap(ctx, BootstrapRecord{CodeDigest: "digest-abc", ExpiresAt: expires}); err != nil {
		t.Fatalf("put bootstrap: %v", err)
	}

	got, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap: %v", err)
	}
	if got.CodeDigest != "digest-abc" {
		t.Errorf("CodeDigest = %q, want digest-abc", got.CodeDigest)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
	if got.ClaimedAt != nil {
		t.Errorf("ClaimedAt = %v, want nil for a fresh code", got.ClaimedAt)
	}
}

func TestGetBootstrapNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetBootstrap(context.Background())
	if !errors.Is(err, ErrBootstrapNotFound) {
		t.Errorf("error = %v, want ErrBootstrapNotFound", err)
	}
}

func TestPutBootstrapReplacesExistingRow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.PutBootstrap(ctx, BootstrapRecord{CodeDigest: "old-digest", ExpiresAt: mustTime(t, "2026-02-01T00:00:00Z")}); err != nil {
		t.Fatalf("put first bootstrap: %v", err)
	}
	if _, err := st.PutBootstrap(ctx, BootstrapRecord{CodeDigest: "new-digest", ExpiresAt: mustTime(t, "2026-03-01T00:00:00Z")}); err != nil {
		t.Fatalf("put second bootstrap: %v", err)
	}
	got, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap: %v", err)
	}
	if got.CodeDigest != "new-digest" {
		t.Errorf("CodeDigest = %q, want new-digest (PutBootstrap must replace, not accumulate)", got.CodeDigest)
	}
}

func TestClaimBootstrapAndCreatePrincipalSucceeds(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.PutBootstrap(ctx, BootstrapRecord{CodeDigest: "digest-abc", ExpiresAt: mustTime(t, "2026-02-01T00:00:00Z")}); err != nil {
		t.Fatalf("put bootstrap: %v", err)
	}

	rec, err := st.ClaimBootstrapAndCreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "admin", Kind: "human", Role: "admin", PasswordHash: "hash"})
	if err != nil {
		t.Fatalf("claim bootstrap: %v", err)
	}
	if rec.ID != "p-1" {
		t.Errorf("created principal ID = %q, want p-1", rec.ID)
	}

	has, err := st.HasAnyPrincipal(ctx)
	if err != nil {
		t.Fatalf("has any principal: %v", err)
	}
	if !has {
		t.Errorf("HasAnyPrincipal = false after claiming bootstrap, want true")
	}

	got, err := st.GetBootstrap(ctx)
	if err != nil {
		t.Fatalf("get bootstrap after claim: %v", err)
	}
	if got.ClaimedAt == nil {
		t.Errorf("ClaimedAt = nil after a successful claim, want non-nil")
	}
}

// TestClaimBootstrapAndCreatePrincipalTwiceFailsSecondTime is the test
// whose name is the claim this package makes about bootstrap being
// single-use: breaking the "claimed_at IS NULL" guard in the UPDATE's
// WHERE clause (e.g. deleting that clause) must make this test fail, or
// the test is not actually checking anything — see the Step 6 task's
// mutation-check requirement.
func TestClaimBootstrapAndCreatePrincipalTwiceFailsSecondTime(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.PutBootstrap(ctx, BootstrapRecord{CodeDigest: "digest-abc", ExpiresAt: mustTime(t, "2026-02-01T00:00:00Z")}); err != nil {
		t.Fatalf("put bootstrap: %v", err)
	}
	if _, err := st.ClaimBootstrapAndCreatePrincipal(ctx, PrincipalRecord{ID: "p-1", Name: "admin", Kind: "human", Role: "admin"}); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	_, err := st.ClaimBootstrapAndCreatePrincipal(ctx, PrincipalRecord{ID: "p-2", Name: "second-admin", Kind: "human", Role: "admin"})
	if !errors.Is(err, ErrBootstrapClaimedRace) {
		t.Fatalf("second claim error = %v, want ErrBootstrapClaimedRace", err)
	}

	// The failed second claim must not have created a principal either —
	// proving the transaction really rolled back rather than partially
	// committing.
	if _, err := st.GetPrincipalByName(ctx, "second-admin"); !errors.Is(err, ErrPrincipalNotFound) {
		t.Errorf("second principal exists after a failed claim: error = %v, want ErrPrincipalNotFound", err)
	}
}

// --- audit ---

func TestAppendAuditEntryAssignsSequentialIDs(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	id1, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "auth_failure", Action: "session.create"})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	id2, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "auth_failure", Action: "session.create"})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("id2 = %d, want > id1 = %d", id2, id1)
	}
}

func TestAppendAuditEntryRequiresKindAndAction(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Action: "x"}); err == nil {
		t.Errorf("append with empty Kind succeeded, want an error")
	}
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin"}); err == nil {
		t.Errorf("append with empty Action succeeded, want an error")
	}
}

func TestListAuditEntriesReturnsEmptyParamsAsJSONObject(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "principal.create"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ParamsJSON != "{}" {
		t.Fatalf("got = %+v, want one entry with ParamsJSON = {}", got)
	}
}

func TestListAuditEntriesSinceExcludesEarlierEntries(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	first, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "one"})
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "two"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	got, err := st.ListAuditEntries(ctx, first, 0)
	if err != nil {
		t.Fatalf("list since first: %v", err)
	}
	if len(got) != 1 || got[0].Action != "two" {
		t.Fatalf("got = %+v, want only the entry after id %d", got, first)
	}
}

// TestAuditLogNeverUpdatedOnlyInserted directly asserts the shape
// migrations.go's schemaV5 doc comment (rule 3) promises: exercising the
// dispatch-then-outcome pattern ADR-024 decision 11 requires (two rows
// correlated by CommandID, never one row mutated) and checking BOTH rows
// still exist afterward with their original, distinct content — a bug
// that "helpfully" updated the dispatch row in place would make this fail
// by leaving only one row, or by leaving the dispatch row's Outcome field
// non-empty.
func TestAuditLogNeverUpdatedOnlyInserted(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	commandID := "cmd-123"
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "dispatch", Action: "fpp:command", CommandID: commandID}); err != nil {
		t.Fatalf("append dispatch: %v", err)
	}
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "outcome", Action: "fpp:command", CommandID: commandID, Outcome: "succeeded"}); err != nil {
		t.Fatalf("append outcome: %v", err)
	}

	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (dispatch and outcome as separate rows)", len(got))
	}
	if got[0].Kind != "dispatch" || got[0].Outcome != "" {
		t.Errorf("dispatch row = %+v, want Kind=dispatch and an empty Outcome (never mutated in place)", got[0])
	}
	if got[1].Kind != "outcome" || got[1].Outcome != "succeeded" {
		t.Errorf("outcome row = %+v, want Kind=outcome and Outcome=succeeded", got[1])
	}
}

func TestPruneAuditByRowCount(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditRows(2), WithMaxAuditAge(0))
	ctx := context.Background()

	// pruneEveryNAuditEntries is 100, far more than the 3 rows this test
	// appends, so the row-count trigger alone would never fire the prune
	// pass on insert-count grounds within this test — advancing the clock
	// past pruneCheckInterval between appends is what makes the AGE
	// trigger fire and run pruneAudit, which then enforces maxAuditRows.
	// This exercises the same two-trigger interaction
	// TestAppendEventPruningByAge already exercises for events (see
	// events_test.go), applied to audit.
	for i := 0; i < 3; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		clock.advance(2 * time.Hour)
	}

	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (maxAuditRows=2 keeps only the newest two)", len(got))
	}
}

func TestPruneAuditByAge(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	st := openTestStore(t, clock, WithMaxAuditAge(24*time.Hour), WithMaxAuditRows(1_000_000))
	ctx := context.Background()

	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "old"}); err != nil {
		t.Fatalf("append old: %v", err)
	}
	// Advance well past both maxAuditAge and pruneCheckInterval so the
	// next append's age-triggered prune pass actually evicts the old row.
	clock.advance(48 * time.Hour)
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "new"}); err != nil {
		t.Fatalf("append new: %v", err)
	}

	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Action != "new" {
		t.Fatalf("got = %+v, want only the entry younger than maxAuditAge", got)
	}
}

// TestWithClockControlsBookkeepingTimestamps proves the identity
// package's central dependency on [WithClock]: [Open] (the exported
// constructor, not the package-internal open helper) must actually honor
// a caller-supplied clock for the bookkeeping timestamps this package
// stamps itself (principals.created_at here), not just accept the option
// silently.
func TestWithClockControlsBookkeepingTimestamps(t *testing.T) {
	dir := t.TempDir()
	fixed := mustTime(t, "2030-06-15T12:00:00Z")
	st, err := Open(context.Background(), dir, nil, WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("open with WithClock: %v", err)
	}
	defer func() { _ = st.Close() }()

	rec, err := st.CreatePrincipal(context.Background(), PrincipalRecord{ID: "p-1", Name: "operator", Kind: "human", Role: "viewer"})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	if !rec.CreatedAt.Equal(fixed) {
		t.Errorf("CreatedAt = %v, want the WithClock-injected time %v", rec.CreatedAt, fixed)
	}
}
