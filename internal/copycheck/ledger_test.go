package copycheck

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settled(t *testing.T, l *Ledger, code int, partial bool) (int, string) {
	t.Helper()
	var out bytes.Buffer
	return l.settle(code, partial, &out), out.String()
}

func listWith(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLedgerFailsOnAViolationThatIsNotListed(t *testing.T) {
	l := NewLedger(listWith(t, ""))
	l.Observe("problem conflict in handleStart", "cue bg1 failed.")

	code, out := settled(t, l, 0, false)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	for _, want := range []string{"cue bg1 failed.", "capital", "problem conflict in handleStart"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestLedgerPassesOnAListedViolation(t *testing.T) {
	l := NewLedger(listWith(t, "# heading\ncapital\tproblem conflict in handleStart\n"))
	l.Observe("problem conflict in handleStart", "cue bg1 failed.")
	l.Observe("problem conflict in handleStart", "Cue bg1 failed.")

	if code, out := settled(t, l, 0, false); code != 0 {
		t.Fatalf("code = %d, want 0:\n%s", code, out)
	}
}

func TestLedgerFailsOnAListedViolationThatNoLongerOccurs(t *testing.T) {
	path := listWith(t, "capital\tproblem conflict in handleStart\n")

	code, out := settled(t, NewLedger(path), 0, false)
	if code != 1 || !strings.Contains(out, "no longer occurs") {
		t.Fatalf("code = %d, want 1 with a stale notice:\n%s", code, out)
	}
	if code, out := settled(t, NewLedger(path), 0, true); code != 0 {
		t.Fatalf("a filtered run must skip the stale check, code = %d:\n%s", code, out)
	}
	if code, _ := settled(t, NewLedger(path), 2, false); code != 2 {
		t.Fatalf("a failing run keeps its own code, got %d", code)
	}
}

func TestLedgerKeepsExemptMessagesUnderTheirOwnHeading(t *testing.T) {
	path := listWith(t, "")
	l := NewLedger(path)
	l.Observe("problem validation-failed in handlePut", "outputs.audio[0].device must be set")
	l.Observe("problem validation-failed in handlePut", "the config is wrong")

	t.Setenv(UpdateEnv, "1")
	if code, out := settled(t, l, 0, false); code != 0 {
		t.Fatalf("code = %d:\n%s", code, out)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	known, exempt, found := strings.Cut(string(written), "# "+exemptHeading)
	if !found || !strings.Contains(known, "capital\tproblem validation-failed in handlePut") || !strings.Contains(exempt, "kind-name\tproblem validation-failed in handlePut") || strings.Contains(known, "kind-name") {
		t.Fatalf("list is not split by heading:\n%s", written)
	}

	t.Setenv(UpdateEnv, "")
	if code, out := settled(t, l, 0, false); code != 0 {
		t.Fatalf("the rewritten list must accept the same run, code = %d:\n%s", code, out)
	}
}

func TestLedgerDoesNotRewriteFromAPartialRun(t *testing.T) {
	path := listWith(t, "capital\told key\n")
	t.Setenv(UpdateEnv, "1")
	settled(t, NewLedger(path), 0, true)
	settled(t, NewLedger(path), 1, false)

	if written, _ := os.ReadFile(path); string(written) != "capital\told key\n" {
		t.Fatalf("list was rewritten:\n%s", written)
	}
}

func TestObserveJSONFindsMessageFieldsAtAnyDepth(t *testing.T) {
	l := NewLedger(listWith(t, ""))
	l.ObserveJSON("v1.Reply", []byte(`{"name":"lowercase is fine here","session":{"reason":"bad one","nodes":[{"warnings":["bad two","Good."]}]}}`))

	_, out := settled(t, l, 0, false)
	for _, want := range []string{"v1.Reply session.reason", "v1.Reply session.nodes[].warnings", "bad one", "bad two"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lowercase is fine here") {
		t.Errorf("a field that is not a message was checked:\n%s", out)
	}
}
