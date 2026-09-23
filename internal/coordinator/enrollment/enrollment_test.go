package enrollment

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateCodeFormatAndNormalize(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 9 || code[4] != '-' {
			t.Fatalf("code %q is not XXXX-XXXX", code)
		}
		if strings.ContainsAny(code, "ILOU") {
			t.Fatalf("code %q uses a letter Crockford base32 excludes", code)
		}
		want := code[:4] + code[5:]
		for _, variant := range []string{code, strings.ToLower(code), want, strings.ToLower(want), " " + code + " "} {
			got, ok := NormalizeCode(variant)
			if !ok || got != want {
				t.Fatalf("NormalizeCode(%q) = %q, %v; want %q", variant, got, ok, want)
			}
		}
	}
}

func TestNormalizeCodeRefusesMalformed(t *testing.T) {
	for _, bad := range []string{"", "ABCD-234", "ABCD-23456", "ABCD_2345", "ABCU-2345", "AB-CD2345", "ABCD--2345"} {
		if got, ok := NormalizeCode(bad); ok {
			t.Errorf("NormalizeCode(%q) = %q, accepted; want refused", bad, got)
		}
	}
	if got, ok := NormalizeCode("abco-il23"); !ok || got != "ABC01123" {
		t.Errorf("NormalizeCode applies Crockford substitutions: got %q, %v; want ABC01123", got, ok)
	}
}

func TestPasswdEntryVerifies(t *testing.T) {
	entry, err := passwdEntry("render-01", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(entry, "render-01:$7$101$") {
		t.Fatalf("entry %q lacks the Mosquitto 2.0 $7$101$ prefix", entry)
	}
	parts := strings.Split(entry, "$")
	if len(parts[3]) != 16 || len(parts[4]) != 88 {
		t.Fatalf("salt/hash lengths = %d/%d, want 16/88 (12 and 64 bytes, padded base64)", len(parts[3]), len(parts[4]))
	}
	if !verifyPasswdEntry(entry, "render-01", "s3cret") || verifyPasswdEntry(entry, "render-01", "wrong") {
		t.Fatal("entry does not verify its own password, or verifies a wrong one")
	}
}

func TestUpsertPasswdEntryKeepsOtherLines(t *testing.T) {
	old := []byte("coordinator:$7$101$aaa$bbb\nrender-01:$7$101$old$old\nfpp:$7$101$ccc$ddd\n")
	got := upsertPasswdEntry(old, "render-01", "render-01:NEW")
	want := "coordinator:$7$101$aaa$bbb\nrender-01:NEW\nfpp:$7$101$ccc$ddd\n"
	if string(got) != want {
		t.Fatalf("replace: got %q, want %q", got, want)
	}
	got = upsertPasswdEntry(old, "audio-01", "audio-01:NEW")
	if string(got) != string(old)+"audio-01:NEW\n" {
		t.Fatalf("append: got %q", got)
	}
}

// scriptSandbox copies generate-credentials.sh and acl.conf into a fresh
// mosquitto directory beside an empty deploy directory, with the marker set.
func scriptSandbox(t *testing.T, passwd string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "mosquitto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"generate-credentials.sh", "acl.conf"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mosquitto", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, passwdFileName), []byte(passwd), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, aclMigrationMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runRenderACL runs the real script's --render-acl with a PATH holding only
// the tools it needs, so it never reaches docker.
func runRenderACL(t *testing.T, dir string) error {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	bin := t.TempDir()
	for _, tool := range []string{"cat", "mktemp", "chmod", "mv", "rm", "dirname"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not installed", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bash, filepath.Join(dir, "generate-credentials.sh"), "--render-acl")
	cmd.Env = []string{"PATH=" + bin}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}
	return nil
}

func TestRenderACLMatchesScriptByteForByte(t *testing.T) {
	passwd := "coordinator:$7$101$a$b\nfpp:$7$101$a$b\nhealthcheck:$7$101$a$b\nrender-01:$7$101$a$b\n" +
		"observer:$7$101$a$b\naudio-node-2:$7$101$a$b\n\nx9:$7$101$a$b\ntrailing-no-newline:$7$101$a$b"
	dir := scriptSandbox(t, passwd)
	if err := runRenderACL(t, dir); err != nil {
		t.Fatalf("script --render-acl failed: %v", err)
	}
	fromScript, err := os.ReadFile(filepath.Join(dir, aclGeneratedFileName))
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(dir, aclBaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	fromGo, err := renderACL(base, []byte(passwd))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fromGo, fromScript) {
		t.Fatalf("Go ACL differs from the script's.\n--- script ---\n%s\n--- go ---\n%s", fromScript, fromGo)
	}
	if !bytes.Contains(fromGo, []byte("user render-01\n")) || bytes.Contains(fromGo, []byte("user trailing-no-newline")) {
		t.Fatal("fixture did not exercise the node block or the unterminated last line")
	}
}

func TestRenderACLRefusesInvalidUsernameLikeScript(t *testing.T) {
	passwd := "coordinator:$7$101$a$b\nBad_User:$7$101$a$b\n"
	dir := scriptSandbox(t, passwd)
	if err := runRenderACL(t, dir); err == nil {
		t.Fatal("script accepted an invalid username; the fixture no longer tests the refusal")
	}
	if _, err := renderACL(nil, []byte(passwd)); !errors.Is(err, ErrBrokerFilesUnavailable) {
		t.Fatalf("renderACL error = %v, want a refusal", err)
	}
}

func brokerDir(t *testing.T, passwd string, marker bool) string {
	t.Helper()
	dir := t.TempDir()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mosquitto", aclBaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, aclBaseFileName), base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, passwdFileName), []byte(passwd), 0o640); err != nil {
		t.Fatal(err)
	}
	if marker {
		if err := os.WriteFile(filepath.Join(dir, aclMigrationMarker), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestProvisionWritesBothFilesAndUndoRestoresThem(t *testing.T) {
	passwd := "coordinator:$7$101$a$b\nrender-01:$7$101$old$old\n"
	dir := brokerDir(t, passwd, true)
	b := NewBrokerFiles(dir)
	password, undo, err := b.Provision("render-01")
	if err != nil {
		t.Fatal(err)
	}
	gotPasswd, _ := os.ReadFile(filepath.Join(dir, passwdFileName))
	lines := strings.Split(strings.TrimSpace(string(gotPasswd)), "\n")
	if len(lines) != 2 || lines[0] != "coordinator:$7$101$a$b" || !verifyPasswdEntry(lines[1], "render-01", password) {
		t.Fatalf("passwd after provision = %q", gotPasswd)
	}
	acl, err := os.ReadFile(filepath.Join(dir, aclGeneratedFileName))
	if err != nil || !bytes.Contains(acl, []byte("user render-01\n")) {
		t.Fatalf("acl.generated.conf missing the node block: %v\n%s", err, acl)
	}
	if fi, _ := os.Stat(filepath.Join(dir, passwdFileName)); fi.Mode().Perm() != passwdFileMode {
		t.Errorf("passwd mode = %v, want %v", fi.Mode().Perm(), passwdFileMode)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(filepath.Join(dir, passwdFileName))
	if string(restored) != passwd {
		t.Fatalf("undo left passwd = %q, want %q", restored, passwd)
	}
	if _, err := os.Stat(filepath.Join(dir, aclGeneratedFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("undo left an acl.generated.conf that did not exist before: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") || strings.HasPrefix(e.Name(), ".showmesh-write-check-") {
			t.Errorf("left a temporary file behind: %s", e.Name())
		}
	}
}

func TestCheckExplainsWhatToFix(t *testing.T) {
	cases := map[string]struct {
		b    *BrokerFiles
		want string
	}{
		"unset":      {NewBrokerFiles(""), "SHOWMESH_BROKER_CONFIG_DIR is not set"},
		"no marker":  {NewBrokerFiles(brokerDir(t, "coordinator:x\n", false)), "--migrate-existing"},
		"no passwd":  {NewBrokerFiles(t.TempDir()), "generate-credentials.sh"},
		"good state": {NewBrokerFiles(brokerDir(t, "coordinator:x\n", true)), ""},
	}
	for name, tc := range cases {
		err := tc.b.Check()
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: Check() = %v, want nil", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrBrokerFilesUnavailable) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Check() = %v, want a refusal mentioning %q", name, err, tc.want)
		}
	}
}
