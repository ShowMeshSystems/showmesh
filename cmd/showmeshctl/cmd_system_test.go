package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const systemVersionsNode = `{"id":"node-a","version":"0.2.0","reason":null,"mismatch":false,"warning":null},
 {"id":"node-b","version":"0.1.5","reason":null,"mismatch":true,
  "warning":"Node node-b runs 0.1.5. Install 0.2.0 on it to match the coordinator."},
 {"id":"node-c","version":null,"reason":"This node has not reported a version yet.","mismatch":false,"warning":null}`

const systemVersionsPlugin = `{"id":"player-1","version":null,"reason":"The FPP plugin does not report its version.","mismatch":false,"warning":null}`

const systemInstall = "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.3.0/get-showmesh.sh | sudo bash -s -- --yes"

func systemVersionsBody(release string) string {
	return `{"serverTime":"2026-10-12T18:05:00Z","coordinator":{"version":"0.2.0"},"nodes":[` + systemVersionsNode +
		`],"plugins":[` + systemVersionsPlugin + `],"release":` + release + `}`
}

func runSystemVersions(t *testing.T, release string, args ...string) (int, string, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/system/versions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("ShowMesh-API-Version", "1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(systemVersionsBody(release)))
	}))
	t.Cleanup(ts.Close)
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"system", "versions", "--server", ts.URL}, args...), &stdout, &stderr, fixedClock(mustParse(t, "2026-10-12T18:05:00Z")))
	return code, stdout.String(), stderr.String()
}

func TestCmdSystemVersionsPrintsTheTableTheWarningsAndTheRelease(t *testing.T) {
	release := `{"checkEnabled":true,"checkIntervalSeconds":21600,"checkedAt":"2026-10-12T18:00:00Z","tag":"v0.3.0",
		"publishedAt":"2026-10-10T12:00:00Z","installCommand":"` + systemInstall + `","available":true,"problem":null}`
	code, out, errOut := runSystemVersions(t, release)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	wantRows := [][]string{
		{"COMPONENT", "ID", "VERSION", "STATUS"},
		{"coordinator", "-", "0.2.0", "-"},
		{"node", "node-a", "0.2.0", "matches"},
		{"node", "node-b", "0.1.5", "mismatch"},
		{"node", "node-c", "-", "This node has not reported a version yet."},
		{"fpp plugin", "player-1", "-", "The FPP plugin does not report its version."},
	}
	for i, want := range wantRows {
		got := strings.Join(strings.Fields(lines[i]), " ")
		if got != strings.Join(want, " ") {
			t.Errorf("row %d = %q, want %q", i, got, strings.Join(want, " "))
		}
	}
	wantTail := []string{
		"Warning: Node node-b runs 0.1.5. Install 0.2.0 on it to match the coordinator.",
		"Release v0.3.0 is available.",
		"Install: " + systemInstall,
		"Checked: 2026-10-12T18:00:00Z",
	}
	if got := lines[len(wantRows):]; strings.Join(got, "\n") != strings.Join(wantTail, "\n") {
		t.Errorf("after the table:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantTail, "\n"))
	}
}

func TestCmdSystemVersionsSaysWhyThereIsNoReleaseAnswer(t *testing.T) {
	for release, want := range map[string]string{
		`{"checkEnabled":false,"checkIntervalSeconds":21600,"checkedAt":null,"tag":null,"publishedAt":null,"installCommand":null,"available":false,"problem":null}`: "The release check is turned off.",
		`{"checkEnabled":true,"checkIntervalSeconds":21600,"checkedAt":null,"tag":null,"publishedAt":null,"installCommand":null,"available":false,"problem":null}`:  "The first release check has not finished.",
		`{"checkEnabled":true,"checkIntervalSeconds":21600,"checkedAt":null,"tag":null,"publishedAt":null,"installCommand":null,"available":false,
		  "problem":"GitHub could not be reached. The coordinator will try again."}`: "Latest check: GitHub could not be reached. The coordinator will try again.",
		`{"checkEnabled":true,"checkIntervalSeconds":21600,"checkedAt":"2026-10-12T18:00:00Z","tag":"v0.2.0","publishedAt":null,
		  "installCommand":"x","available":false,"problem":null}`: "Newest release: v0.2.0. Nothing newer than this coordinator.",
	} {
		code, out, errOut := runSystemVersions(t, release)
		if code != exitOK || !strings.Contains(out, want) {
			t.Errorf("exit %d, output missing %q:\n%s\nstderr=%s", code, want, out, errOut)
		}
		if strings.Contains(out, "is available") {
			t.Errorf("output claims an available release:\n%s", out)
		}
	}
}

func TestCmdSystemVersionsAsJSONPrintsTheCoordinatorsBody(t *testing.T) {
	release := `{"checkEnabled":false,"checkIntervalSeconds":21600,"checkedAt":null,"tag":null,"publishedAt":null,"installCommand":null,"available":false,"problem":null}`
	code, out, errOut := runSystemVersions(t, release, "--output", "json")
	if code != exitOK || !strings.Contains(out, `"coordinator"`) || !strings.Contains(out, `"checkEnabled"`) {
		t.Fatalf("exit %d, out=%s, stderr=%s", code, out, errOut)
	}
}

func TestCmdSystemWithNoSubcommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"system"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-12T18:05:00Z"))); code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage", code)
	}
	if !strings.Contains(stderr.String(), "versions") {
		t.Fatalf("usage does not name the versions subcommand: %s", stderr.String())
	}
}
