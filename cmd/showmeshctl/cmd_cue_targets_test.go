package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// This file tests ADR-049's "targets" list on show.cue outputs.audio and
// outputs.announcement, kept separate from cmd_cue_test.go's own additions
// on this branch to avoid colliding with parallel work on that file.

func TestCmdCueSetRoundTripsTargetsList(t *testing.T) {
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-15T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":{"audio":{"asset":"thriller","startOffsetMillis":0,"targets":["node-a","node-b"]}}},
			"updatedAt":"2026-09-15T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{
		"set", "--server", ts.URL,
		"--show", "halloween-2026", "--name", "Thriller",
		"--outputs-json", `{"audio":{"asset":"thriller","startOffsetMillis":0,"targets":["node-a","node-b"]}}`,
		"thriller",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-09-15T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var decoded configShowCue
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decoding request body: %v; body: %s", err, gotBody)
	}
	if !strings.Contains(string(decoded.Outputs), `"targets":["node-a","node-b"]`) {
		t.Errorf("outputs sent = %s, want the request body to carry targets unchanged", decoded.Outputs)
	}
	if !strings.Contains(stdout.String(), "Audio targets:        node-a, node-b") {
		t.Errorf("stdout = %q, want the response's display output to list both targets readably, not just round-trip the raw JSON", stdout.String())
	}
}

func TestCmdCueGetDisplaysTargetsReadably(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-15T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":{
				"audio":{"asset":"thriller","startOffsetMillis":0,"targets":["node-a","node-b"]},
				"announcement":{"policy":"duck","duckGainDb":-18,"fadeMillis":400,"targets":["node-b"]},
				"ltc":{"startOffsetMillis":0,"target":"node-a"}
			}},
			"updatedAt":"2026-09-15T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"get", "--server", ts.URL, "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-09-15T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Audio targets:        node-a, node-b") {
		t.Errorf("stdout = %q, want it to list both audio targets readably", out)
	}
	if !strings.Contains(out, "Announcement targets: node-b") {
		t.Errorf("stdout = %q, want it to list the announcement target readably", out)
	}
	if !strings.Contains(out, "LTC target:           node-a") {
		t.Errorf("stdout = %q, want it to name the LTC target readably", out)
	}
}

func TestCmdCueGetDisplaysEmptyTargetsAsResolvingToProgramLTCNode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-09-15T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":{
				"audio":{"asset":"thriller","startOffsetMillis":0},
				"ltc":{"startOffsetMillis":0}
			}},
			"updatedAt":"2026-09-15T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"get", "--server", ts.URL, "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-09-15T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Audio targets:        (inherits the show's audio nodes, or the program+ltc node if none are set)") {
		t.Errorf("stdout = %q, want an absent targets list to say it inherits the show's audio nodes or falls back to the program+ltc node", out)
	}
	if !strings.Contains(out, "LTC target:           (resolves to the program+ltc node)") {
		t.Errorf("stdout = %q, want an absent LTC target to say it resolves to the program+ltc node", out)
	}
	if strings.Contains(out, "Announcement targets:") {
		t.Errorf("stdout = %q, want no announcement line when the cue has no announcement output", out)
	}
}

// TestPrintCueOutputTargetsAlignsWithEachOtherNotTheRestOfTheBlock pins the
// exact column every target line's value starts at, not merely that a
// label and its value both appear somewhere in the output: a Contains
// check on "label: value" would pass unchanged at any column, which is
// exactly how the misalignment this guards against went unnoticed before.
// The three target lines share their own gutter (one past "Announcement
// targets:", the longest of them, deliberately not printCueDetail's own
// 14-character one: 14 cannot hold that label at all), so this test
// pins them to each other and asserts they differ from Cue ID's column.
func TestPrintCueOutputTargetsAlignsWithEachOtherNotTheRestOfTheBlock(t *testing.T) {
	const outputsJSON = `{"audio":{"asset":"thriller","startOffsetMillis":0,"targets":["node-a","node-b"]},"announcement":{"policy":"duck","duckGainDb":-18,"fadeMillis":400,"targets":["node-b"]},"ltc":{"startOffsetMillis":0,"target":"node-a"}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-09-15T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
			"payload":{"show":"halloween-2026","name":"Thriller","outputs":%s},
			"updatedAt":"2026-09-15T20:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`, outputsJSON)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"get", "--server", ts.URL, "thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-09-15T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	want := "Cue ID:       thriller\n" +
		"Show:         halloween-2026\n" +
		"Name:         Thriller\n" +
		"Outputs:      " + outputsJSON + "\n" +
		"Audio targets:        node-a, node-b\n" +
		"Announcement targets: node-b\n" +
		"LTC target:           node-a\n" +
		"Revision:     1\n" +
		"Updated:      2026-09-15T20:00:00Z\n" +
		"Created by:   admin\n"
	if stdout.String() != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", stdout.String(), want)
	}

	// valueColumn locates where value starts within line, given the exact
	// label prefix (including trailing padding) that precedes it: a direct
	// measurement of the printed column, not a guess from character content.
	valueColumn := func(t *testing.T, line, labelPrefix string) int {
		t.Helper()
		if !strings.HasPrefix(line, labelPrefix) {
			t.Fatalf("line %q does not start with %q", line, labelPrefix)
		}
		return len(labelPrefix)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	cueIDColumn := valueColumn(t, lines[0], "Cue ID:       ")
	audioColumn := valueColumn(t, lines[4], "Audio targets:        ")
	announceColumn := valueColumn(t, lines[5], "Announcement targets: ")
	ltcColumn := valueColumn(t, lines[6], "LTC target:           ")

	if audioColumn != targetLineWidth {
		t.Fatalf("Audio targets value column = %d, want %d", audioColumn, targetLineWidth)
	}
	if announceColumn != targetLineWidth {
		t.Errorf("Announcement targets value column = %d, want %d (aligned with Audio targets)", announceColumn, targetLineWidth)
	}
	if ltcColumn != targetLineWidth {
		t.Errorf("LTC target value column = %d, want %d (aligned with Audio targets)", ltcColumn, targetLineWidth)
	}
	if cueIDColumn == targetLineWidth {
		t.Errorf("Cue ID value column = %d, unexpectedly equals the target group's own gutter %d; this file keeps two deliberately distinct gutters", cueIDColumn, targetLineWidth)
	}
}

// TestResolvedTargetsFoldsDeprecatedSingularIntoOneElementList tests
// resolvedTargets directly as the pure helper it is. A show.cue GET can
// never actually return the singular-only shape this exercises: the
// coordinator's own decode normalizes a stored "target" into "targets"
// before it is ever written (internal/coordinator/config/showcue.go), so
// simulating that shape via a fake httptest response would assert a
// server behavior that does not exist.
func TestResolvedTargetsFoldsDeprecatedSingularIntoOneElementList(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		targets []string
		want    []string
	}{
		{name: "targets present wins", target: "node-a", targets: []string{"node-b", "node-c"}, want: []string{"node-b", "node-c"}},
		{name: "singular target folds to a one-element list", target: "node-a", targets: nil, want: []string{"node-a"}},
		{name: "neither present yields nil", target: "", targets: nil, want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolvedTargets(c.target, c.targets)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("resolvedTargets(%q, %v) = %v, want %v", c.target, c.targets, got, c.want)
			}
		})
	}
}

func TestCmdCueSetUsageNamesTargetsList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdCue([]string{"set", "--help"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-09-15T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, `"targets"`) {
		t.Errorf("usage = %q, want it to name the \"targets\" list field", out)
	}
	if !strings.Contains(out, "one aligned instant") {
		t.Errorf("usage = %q, want it to explain the targets list plainly", out)
	}
}
