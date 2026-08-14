package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigDirPrecedence(t *testing.T) {
	// Hermetic against whatever the real host environment happens to have
	// set — FPP's own MEDIADIR in particular, since a developer running
	// these tests on a machine that also has an FPP install nearby is not
	// impossible.
	t.Setenv("SHOWMESH_FPP_PLUGIN_CONFIG_DIR", "")
	t.Setenv("MEDIADIR", "")

	if got := resolveConfigDir(""); got != defaultConfigDir {
		t.Errorf("with no flag, no env, and no $MEDIADIR, got %q, want the pinned default %q", got, defaultConfigDir)
	}

	t.Setenv("MEDIADIR", "/home/fpp/media")
	wantUnderMedia := "/home/fpp/media/config/plugin.fpp-showmesh"
	if got := resolveConfigDir(""); got != wantUnderMedia {
		t.Errorf("with $MEDIADIR set and nothing higher-priority, got %q, want %q (derived under MEDIADIR)", got, wantUnderMedia)
	}

	t.Setenv("SHOWMESH_FPP_PLUGIN_CONFIG_DIR", "/env/dir")
	if got := resolveConfigDir(""); got != "/env/dir" {
		t.Errorf("with both $MEDIADIR and $SHOWMESH_FPP_PLUGIN_CONFIG_DIR set, got %q, want /env/dir (the plugin-specific env var must win over $MEDIADIR)", got)
	}

	if got := resolveConfigDir("/flag/dir"); got != "/flag/dir" {
		t.Errorf("with a flag value, got %q, want /flag/dir (the flag must win over everything else)", got)
	}
}

// TestResolveConfigDirMediaDirUsesAnArbitraryRoot is the property that
// justifies preferring $MEDIADIR over the pinned literal at all: a host
// whose media root was moved off /home/fpp/media entirely still resolves
// correctly, because this program reads what FPP itself hands it rather
// than assuming the default.
func TestResolveConfigDirMediaDirUsesAnArbitraryRoot(t *testing.T) {
	t.Setenv("SHOWMESH_FPP_PLUGIN_CONFIG_DIR", "")
	t.Setenv("MEDIADIR", "/mnt/usb-media")
	want := "/mnt/usb-media/config/plugin.fpp-showmesh"
	if got := resolveConfigDir(""); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadCredentialRefusesWrongMode(t *testing.T) {
	dir := t.TempDir()
	path := credentialPath(dir)
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadCredential(dir)
	if err == nil {
		t.Fatal("expected an error for a 0644 credential file, got nil")
	}
	// This test's own name is a claim: verify it actually distinguishes
	// mode from every other possible failure by confirming the SAME file,
	// re-chmod'd to 0600, succeeds.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := loadCredential(dir)
	if err != nil {
		t.Fatalf("expected success once mode is 0600, got %v", err)
	}
	if token != "secret-token" {
		t.Errorf("token = %q, want %q (whitespace must be trimmed)", token, "secret-token")
	}
}

func TestLoadCredentialRefusesTooRestrictiveMode(t *testing.T) {
	// The rule is EXACT equality with 0600, not "no more permissive than"
	// — a file this program did not itself write should not be trusted
	// merely because it happens to be private, per config.go's own doc
	// comment. 0400 is more restrictive than 0600 and must still be
	// refused.
	dir := t.TempDir()
	path := credentialPath(dir)
	if err := os.WriteFile(path, []byte("secret-token"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential(dir); err == nil {
		t.Fatal("expected an error for a 0400 credential file (stricter than 0600 is still refused), got nil")
	}
}

func TestLoadCredentialMissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadCredential(dir); err == nil {
		t.Fatal("expected an error for a missing credential file, got nil")
	}
}

func TestLoadCredentialEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(credentialPath(dir), []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential(dir); err == nil {
		t.Fatal("expected an error for a whitespace-only credential file, got nil")
	}
}

func TestLoadCoordinatorURLAbsentNullEmptyAreDistinct(t *testing.T) {
	// "Absent, null and explicitly empty are three different things on
	// every field": exercised on this program's own inbound decode of
	// config.json, which is exactly what this test asserts stays true.
	cases := []struct {
		name string
		body string
	}{
		{"missing file", ""},
		{"absent key", `{}`},
		{"null value", `{"coordinatorUrl": null}`},
		{"empty string", `{"coordinatorUrl": ""}`},
		{"not http/https", `{"coordinatorUrl": "ftp://example.com"}`},
		{"no host", `{"coordinatorUrl": "http://"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.body != "" {
				if err := os.WriteFile(coordinatorConfigPath(dir), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadCoordinatorURL(dir); err == nil {
				t.Errorf("case %q: expected an error, got nil", tc.name)
			}
		})
	}
}

func TestLoadCoordinatorURLValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(coordinatorConfigPath(dir), []byte(`{"coordinatorUrl": "http://coordinator.local:8080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := loadCoordinatorURL(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.String() != "http://coordinator.local:8080" {
		t.Errorf("got %q, want http://coordinator.local:8080", u.String())
	}
}

func TestConfigPathsAreUnderConfigDir(t *testing.T) {
	dir := "/some/dir"
	for _, p := range []string{
		credentialPath(dir),
		coordinatorConfigPath(dir),
		statusPath(dir),
		failureBufferPath(dir),
		macroCachePath(dir),
	} {
		if filepath.Dir(p) != dir {
			t.Errorf("path %q is not directly under %q", p, dir)
		}
	}
}
