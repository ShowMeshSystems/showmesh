package clock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFPPStatusURLNativePort(t *testing.T) {
	got, err := fppStatusURL("http://fpp-host.local:8080", ":32322/api/aes67/status")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://fpp-host.local:32322/api/aes67/status"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFPPStatusURLPHPProxy(t *testing.T) {
	got, err := fppStatusURL("http://fpp-host.local", "/api/pipewire/aes67/status")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://fpp-host.local/api/pipewire/aes67/status"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFPPStatusURLRequiresBaseURL(t *testing.T) {
	if _, err := fppStatusURL("", "/api/pipewire/aes67/status"); err == nil {
		t.Fatal("expected an error for an empty base URL")
	}
}

func TestFPPProviderPollSyncedReportsLocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ptp": map[string]any{
				"synced": true, "offsetNs": 120, "grandmasterId": "aa:bb:cc:dd:ee:ff",
				"portState": "SLAVE", "isGrandmaster": false, "enabled": true, "domain": 24, "role": "follower",
			},
		})
	}))
	defer srv.Close()

	p := NewFPPProvider(FPPConfig{Interface: "eth0", BaseURL: srv.URL, StatusPaths: []string{"/"}})
	raw := p.Poll(context.Background())
	if !raw.Reachable {
		t.Fatalf("expected Reachable=true, got false: %s", raw.Reason)
	}
	if !raw.Locked {
		t.Fatalf("expected Locked=true")
	}
	if !raw.GMKnown || raw.GrandmasterIdentity != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("gm = %q/%v", raw.GrandmasterIdentity, raw.GMKnown)
	}
	if raw.Role != RoleFollower {
		t.Fatalf("role = %q, want follower", raw.Role)
	}
}

func TestFPPProviderPollDisabledIsReachableNotFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ptp": map[string]any{"enabled": false}})
	}))
	defer srv.Close()

	p := NewFPPProvider(FPPConfig{Interface: "eth0", BaseURL: srv.URL, StatusPaths: []string{"/"}})
	raw := p.Poll(context.Background())
	// RES-019 §1: absence is a normal state to report, never an error to
	// retry into — this must stay Reachable=true (which a Tracker maps to
	// acquiring, not failed).
	if !raw.Reachable {
		t.Fatalf("PTP-not-enabled must be reported as Reachable=true, got false")
	}
	if raw.Locked {
		t.Fatalf("expected Locked=false")
	}
	if raw.Reason == "" {
		t.Fatalf("expected a stated reason")
	}
}

func TestFPPProviderPollUnreachableHostIsStillReachableTrue(t *testing.T) {
	p := NewFPPProvider(FPPConfig{Interface: "eth0", BaseURL: "http://127.0.0.1:1", StatusPaths: []string{"/"}})
	raw := p.Poll(context.Background())
	// A network failure reaching FPP is also not this NODE's own
	// interface/link failure (RES-019 §1 scopes StateFailed to this
	// node's own PTP source, not to FPP being unreachable), so this stays
	// Reachable=true with a reason — a Tracker maps it to acquiring.
	if !raw.Reachable {
		t.Fatalf("expected Reachable=true even when the FPP host cannot be reached")
	}
	if raw.Reason == "" {
		t.Fatalf("expected a stated reason")
	}
}

func TestFPPRoleToRole(t *testing.T) {
	if r, ok := fppRoleToRole("", true); !ok || r != RoleGrandmaster {
		t.Fatalf("isGrandmaster fallback: got %q/%v", r, ok)
	}
	if _, ok := fppRoleToRole("", false); ok {
		t.Fatalf("expected unknown role for empty role and isGrandmaster=false")
	}
}
