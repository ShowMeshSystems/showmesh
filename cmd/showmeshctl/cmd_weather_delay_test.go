package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCmdWeatherDelayStatusPrintsHeldPlayers(t *testing.T) {
	const msg = "Player player-01 is being held dark and no weather delay is active. Press Resume to release it."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-10-31T20:30:00Z","active":false,"revision":8,"assets":[],"powerGroups":[],"heldPlayers":[{"instanceId":"player-01","message":%q}]}`, msg)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := cmdWeatherDelay([]string{"status", "--server", srv.URL}, &stdout, &stderr, func() time.Time { return time.Date(2026, 10, 31, 20, 30, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "weather delay: not active") || !strings.Contains(stdout.String(), msg) {
		t.Fatalf("stdout = %q, want the not-active line and the held player sentence", stdout.String())
	}
}
