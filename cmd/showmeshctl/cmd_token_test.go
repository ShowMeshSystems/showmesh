package main

import (
	"bytes"
	"testing"
	"time"
)

// TestTokenDocumentedInvocationsParse drives every documented flags-first
// invocation form through the real arg parser and out to a stub
// coordinator — same contract as TestPrincipalDocumentedInvocationsParse:
// the usage strings promise flags before positionals, and the standard
// flag package stops parsing at the first non-flag argument.
func TestTokenDocumentedInvocationsParse(t *testing.T) {
	cases := []struct {
		name     string
		args     func(server string) []string
		wantHit  string
		wantExit int
	}{
		{
			name:     "list [flags] <principalId>",
			args:     func(s string) []string { return []string{"list", "--server", s, "p1"} },
			wantHit:  "GET /api/v1/principals/p1/tokens",
			wantExit: exitOK,
		},
		{
			name: "issue [--label] [--expires] <principalId>",
			args: func(s string) []string {
				return []string{"issue", "--server", s, "--label", "bench", "--expires", "2027-01-15T00:00:00Z", "p1"}
			},
			wantHit:  "POST /api/v1/principals/p1/tokens",
			wantExit: exitOK,
		},
		{
			name:     "revoke [flags] <principalId> <tokenId>",
			args:     func(s string) []string { return []string{"revoke", "--server", s, "p1", "t1"} },
			wantHit:  "DELETE /api/v1/principals/p1/tokens/t1",
			wantExit: exitOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests []string
			ts := principalTestServer(t, &requests)
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdToken(tc.args(ts.URL), &stdout, &stderr, time.Now)
			if code != tc.wantExit {
				t.Fatalf("exit code = %d, want %d; stderr=%s", code, tc.wantExit, stderr.String())
			}
			if len(requests) != 1 || requests[0] != tc.wantHit {
				t.Errorf("requests = %v, want exactly [%s]", requests, tc.wantHit)
			}
		})
	}
}
