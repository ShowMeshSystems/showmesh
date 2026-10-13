package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestDecodeProblemErrorListsEachBlockedSafetyCheckOnItsOwnLine(t *testing.T) {
	body := []byte(`{"type":"https://showmesh.dev/problems/night-not-ready","title":"Night session not ready","status":409,` +
		`"detail":"2 safety checks blocked Start night. Fix them, or retry with an override.",` +
		`"blockedChecks":[{"name":"gate","reason":"The gate is open."},{"name":"cooldown","reason":"The projector is still hot."}]}`)
	err := decodeProblemError(&http.Response{StatusCode: http.StatusConflict, Header: http.Header{}}, body)
	want := "2 safety checks blocked Start night. Fix them, or retry with an override.\n  gate: The gate is open.\n  cooldown: The projector is still hot."
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("error = %v, want it to end with %q", err, want)
	}
}
