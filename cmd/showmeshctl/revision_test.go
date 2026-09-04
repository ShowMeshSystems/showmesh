package main

import "testing"

func TestIfMatchHeaderValueQuotesTheInteger(t *testing.T) {
	if got := ifMatchHeaderValue(7); got != `"7"` {
		t.Errorf("ifMatchHeaderValue(7) = %q, want %q", got, `"7"`)
	}
}

// TestResolveIfMatchPrecedence pins the ruled precedence: --if-match wins
// over a payload revision, and a payload revision wins over a fresh GET.
// force disables the check entirely regardless of any other input.
func TestResolveIfMatchPrecedence(t *testing.T) {
	fetchCalled := false
	fetch := func() (int64, error) { fetchCalled = true; return 3, nil }

	// force wins over everything.
	fetchCalled = false
	got, err := resolveIfMatch(true, 9, true, 5, fetch)
	if err != nil {
		t.Fatalf("resolveIfMatch: %v", err)
	}
	if got != "" {
		t.Errorf("force=true: If-Match = %q, want \"\"", got)
	}
	if fetchCalled {
		t.Error("force=true: fetchCurrentRevision was called, want it skipped")
	}

	// explicit --if-match wins over a payload revision.
	fetchCalled = false
	got, err = resolveIfMatch(false, 9, true, 5, fetch)
	if err != nil {
		t.Fatalf("resolveIfMatch: %v", err)
	}
	if got != ifMatchHeaderValue(9) {
		t.Errorf("flagSet=true: If-Match = %q, want %q", got, ifMatchHeaderValue(9))
	}
	if fetchCalled {
		t.Error("flagSet=true: fetchCurrentRevision was called, want it skipped")
	}

	// a payload revision wins over a fresh GET.
	fetchCalled = false
	got, err = resolveIfMatch(false, 0, false, 5, fetch)
	if err != nil {
		t.Fatalf("resolveIfMatch: %v", err)
	}
	if got != ifMatchHeaderValue(5) {
		t.Errorf("payloadRevision=5: If-Match = %q, want %q", got, ifMatchHeaderValue(5))
	}
	if fetchCalled {
		t.Error("payloadRevision=5: fetchCurrentRevision was called, want it skipped")
	}

	// neither flag nor payload: falls through to a fresh GET.
	fetchCalled = false
	got, err = resolveIfMatch(false, 0, false, 0, fetch)
	if err != nil {
		t.Fatalf("resolveIfMatch: %v", err)
	}
	if got != ifMatchHeaderValue(3) {
		t.Errorf("no flag/payload: If-Match = %q, want %q", got, ifMatchHeaderValue(3))
	}
	if !fetchCalled {
		t.Error("no flag/payload: fetchCurrentRevision was not called")
	}
}

// TestResolveIfMatchNotFoundSendsNoHeader proves a *cliError with
// exitNotFound from fetchCurrentRevision (the object does not exist yet)
// is read as "send no If-Match", not propagated as a failure.
func TestResolveIfMatchNotFoundSendsNoHeader(t *testing.T) {
	fetch := func() (int64, error) { return 0, newCLIError(exitNotFound, "no such object") }
	got, err := resolveIfMatch(false, 0, false, 0, fetch)
	if err != nil {
		t.Fatalf("resolveIfMatch: %v, want a nil error for a not-found object", err)
	}
	if got != "" {
		t.Errorf("If-Match = %q, want \"\" for a not-yet-created object", got)
	}
}

// TestResolveIfMatchPropagatesOtherFetchErrors proves any OTHER error from
// fetchCurrentRevision (not exitNotFound) is returned to the caller rather
// than silently treated as "no header".
func TestResolveIfMatchPropagatesOtherFetchErrors(t *testing.T) {
	fetch := func() (int64, error) { return 0, newCLIError(exitForbidden, "no scope") }
	_, err := resolveIfMatch(false, 0, false, 0, fetch)
	if err == nil {
		t.Fatal("resolveIfMatch returned no error, want the fetch's own error propagated")
	}
	ce := requireCLIError(t, err)
	if ce.code != exitForbidden {
		t.Errorf("exit code = %d, want exitForbidden (%d)", ce.code, exitForbidden)
	}
}

func TestWrapperRevisionRecognizesTheGetResponseShape(t *testing.T) {
	const wrapper = `{"kind":"fpp.endpoints","revision":7,"payload":{"endpoints":[]},"updatedAt":"2026-08-12T00:00:00Z","source":"api"}`
	rev, ok := wrapperRevision([]byte(wrapper))
	if !ok {
		t.Fatal("wrapperRevision returned ok=false, want true for a full get-response object")
	}
	if rev != 7 {
		t.Errorf("revision = %d, want 7", rev)
	}
}

func TestWrapperRevisionFalseForBarePayload(t *testing.T) {
	const bare = `{"endpoints":[]}`
	if _, ok := wrapperRevision([]byte(bare)); ok {
		t.Error("wrapperRevision returned ok=true for a bare payload with no wrapper markers")
	}
}

func TestWrapperRevisionFalseForAmbiguousShape(t *testing.T) {
	// "revision" present without "kind": not the get-response wrapper.
	const ambiguous = `{"revision":7,"payload":{"endpoints":[]}}`
	if _, ok := wrapperRevision([]byte(ambiguous)); ok {
		t.Error("wrapperRevision returned ok=true for a shape missing \"kind\"")
	}
}
