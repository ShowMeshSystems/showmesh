package clock

import (
	"context"
	"testing"
	"time"
)

func TestManagerPollUnconfiguredReportsUnsynchronized(t *testing.T) {
	m := NewManager(func() time.Time { return time.Unix(0, 0) }, nil)
	s := m.Poll(context.Background())
	if s.State != StateUnsynchronized {
		t.Fatalf("state = %v, want unsynchronized", s.State)
	}
	if s.Provider != ProviderNone {
		t.Fatalf("provider = %v, want none", s.Provider)
	}
	if s.Reason == "" {
		t.Fatalf("expected a stated reason")
	}
}

func TestManagerSetConfigExternalBuildsTracker(t *testing.T) {
	m := NewManager(time.Now, nil)
	err := m.SetConfig(context.Background(), Config{
		Provider: ProviderExternal, Interface: "eth0", Domain: 24,
		ExternalUDSAddress: "/run/showmesh-clock-manager-test-nonexistent",
	})
	if err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	defer func() { _ = m.Close() }()

	s := m.Poll(context.Background())
	if s.Provider != ProviderExternal {
		t.Fatalf("provider = %v, want external", s.Provider)
	}
	// The socket does not exist, so this should read as unreachable/failed,
	// never a fabricated locked reading.
	if s.State == StateLocked {
		t.Fatalf("did not expect a locked reading against a nonexistent socket")
	}
}

func TestManagerSetConfigRejectsUnknownProvider(t *testing.T) {
	m := NewManager(time.Now, nil)
	err := m.SetConfig(context.Background(), Config{Provider: "bogus", Interface: "eth0"})
	if err == nil {
		t.Fatalf("expected an error for an unknown provider kind")
	}
	// A rejected SetConfig must leave the Manager unconfigured, not half-built.
	s := m.Poll(context.Background())
	if s.Provider != ProviderNone {
		t.Fatalf("provider = %v, want none after a rejected SetConfig", s.Provider)
	}
}

func TestManagerSetConfigReplacesPreviousProvider(t *testing.T) {
	m := NewManager(time.Now, nil)
	if err := m.SetConfig(context.Background(), Config{Provider: ProviderExternal, Interface: "eth0", Domain: 1}); err != nil {
		t.Fatalf("first SetConfig: %v", err)
	}
	if err := m.SetConfig(context.Background(), Config{Provider: ProviderFPP, Interface: "eth1", Domain: 2, FPPBaseURL: "http://fpp.local"}); err != nil {
		t.Fatalf("second SetConfig: %v", err)
	}
	defer func() { _ = m.Close() }()

	s := m.Poll(context.Background())
	if s.Provider != ProviderFPP {
		t.Fatalf("provider = %v, want fpp (second SetConfig should have replaced the first)", s.Provider)
	}
}
