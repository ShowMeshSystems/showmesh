package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeqTestSetPutsTheShowsList(t *testing.T) {
	var gotPath, gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodPut {
			gotPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-10-21T21:00:00Z","kind":"show.sequencetest","id":"halloween","revision":0,"source":"default","running":true,"updatedAt":"2026-10-21T21:00:00Z","createdByPrincipalId":null,"createdByPrincipalName":null,"payload":{"show":"halloween","entries":[{"sequenceFilename":"Thriller.fseq","cue":"thriller"}]}}`)
	}))
	defer ts.Close()
	file := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(file, []byte(`{"entries":[{"sequenceFilename":"Thriller.fseq","cue":"thriller"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := cmdSeqTest([]string{"set", "--show", "halloween", "--file", file, "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-10-21T21:00:00Z"))); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if gotPath != "/api/v1/config/show.sequencetest/halloween" || !strings.Contains(gotBody, "Thriller.fseq") {
		t.Fatalf("PUT %s body %s", gotPath, gotBody)
	}
	if !strings.Contains(stdout.String(), "Thriller.fseq -> cue thriller") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}
