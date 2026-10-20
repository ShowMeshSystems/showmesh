package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The first write to an unsaved show.sitepower sends no If-Match, since
// revision 0 cannot be named in one.
func TestSitePowerSetFirstWriteSendsNoIfMatch(t *testing.T) {
	var gotIfMatch []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodPut {
			gotIfMatch = r.Header.Values("If-Match")
		}
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-19T21:00:00Z","kind":"show.sitepower","revision":0,"source":"default","updatedAt":"2026-10-19T21:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"payload":{"shutdownSequences":[],"powerOnActions":[]}}`)
	}))
	defer ts.Close()
	file := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(file, []byte(`{"powerOnActions":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cmdSitePower([]string{"set", "--file", file, "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-19T21:00:00Z"))); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if len(gotIfMatch) != 0 {
		t.Fatalf("If-Match = %v, want none on the first write", gotIfMatch)
	}
}
