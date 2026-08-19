package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The help text an operator reads while a session is degraded has to match
// what the server actually accepts. It previously claimed end-session was
// the only command that ran against a degraded session, which would have
// sent an operator looking for a recovery path when three shutdown
// commands were available the whole time.

// nightHelpFor captures one lifecycle subcommand's usage text.
func nightHelpFor(t *testing.T, sub string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmdNight([]string{sub, "--help"}, &stdout, &stderr, time.Now)
	help := stdout.String() + stderr.String()
	if help == "" {
		t.Fatalf("%s --help printed nothing", sub)
	}
	return help
}

// The four commands accepted against a degraded session are named, and the
// distinction between abandoning the session and ending the night through
// it is stated.
func TestNightEndSessionHelpNamesEveryDegradedCommand(t *testing.T) {
	help := nightHelpFor(t, "end-session")

	for _, want := range []string{
		"request-final-show",
		"fade-out-night",
		"power-down-presentation",
		"end-session",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("end-session help does not name %q as accepted while degraded:\n%s", want, help)
		}
	}
	if strings.Contains(help, "The only command that runs against a DEGRADED session") {
		t.Error("end-session help still claims to be the only command a degraded session accepts")
	}
	if !strings.Contains(help, "abandons") {
		t.Errorf("end-session help does not say it abandons the session:\n%s", help)
	}
	if !strings.Contains(strings.ToLower(help), "directional") {
		t.Errorf("end-session help does not distinguish the directional shutdown commands:\n%s", help)
	}
}

// The three directional commands each say they still work while degraded,
// so an operator who reaches for one of them first is not turned away.
func TestNightDirectionalCommandHelpSaysItSurvivesDegradation(t *testing.T) {
	for _, sub := range []string{"final-show", "fade-out", "power-down"} {
		t.Run(sub, func(t *testing.T) {
			help := nightHelpFor(t, sub)
			if !strings.Contains(help, "DEGRADED") {
				t.Errorf("%s help does not say it is accepted while degraded:\n%s", sub, help)
			}
		})
	}
}

// fade-out-night no longer reaches stopped on acceptance, and its help
// must not imply that it does.
func TestNightFadeOutHelpDescribesTheConfirmedStop(t *testing.T) {
	help := nightHelpFor(t, "fade-out")
	for _, want := range []string{"fading-out", "idle", "stopped"} {
		if !strings.Contains(help, want) {
			t.Errorf("fade-out help does not mention %q:\n%s", want, help)
		}
	}
	if !strings.Contains(help, "deferred") {
		t.Errorf("fade-out help does not describe deferral during a live show:\n%s", help)
	}
}
