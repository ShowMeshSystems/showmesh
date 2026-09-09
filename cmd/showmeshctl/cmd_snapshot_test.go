package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
)

// TestSnapshotAudioConfigPushAbsentAgainstPreSM357Coordinator is this
// review round's own regression test: a coordinator that predates PR #189
// (this field's addition) omits "audioConfigPush" from GET
// /api/v1/snapshot's body entirely — not "usable", not any of the enum's
// members, simply absent. Per this file's own doc comment elsewhere in
// this package, an absent optional field must decode to nil, distinguishable
// from a JSON zero value, and must never be reprinted as if the
// coordinator had actually reported an empty string state.
func TestSnapshotAudioConfigPushAbsentAgainstPreSM357Coordinator(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","latestEventSeq":0,"nodes":[],
			"fpp":{"instances":[]},"collectors":[],"macroRuns":[],"resolume":[],
			"auditStore":{"state":"usable","reason":null}}`)
	})

	var snap snapshot
	if err := c.getJSON(context.Background(), "/api/v1/snapshot", nil, &snap); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if snap.AudioConfigPush != nil {
		t.Fatalf("AudioConfigPush = %+v, want nil for a body that omits the field entirely", snap.AudioConfigPush)
	}

	var out bytes.Buffer
	printSnapshotDetail(&out, snap)
	if !containsAll(out.String(), "not reported by this coordinator") {
		t.Errorf("printed output does not name the absent field distinctly; got:\n%s", out.String())
	}
	if containsAll(out.String(), `Audio config push: `+"\n") {
		t.Errorf("printed output shows a blank state instead of naming it absent; got:\n%s", out.String())
	}

	var jsonOut bytes.Buffer
	if err := printJSON(&jsonOut, snap); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	if containsAll(jsonOut.String(), `"state": ""`) {
		t.Errorf("JSON output re-emits state=\"\" for an absent field, which is not a member of the state enum; got:\n%s", jsonOut.String())
	}
}
