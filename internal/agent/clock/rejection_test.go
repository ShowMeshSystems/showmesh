package clock

import (
	"context"
	"strings"
	"testing"
	"time"
)

func stubHost(t *testing.T, interfaces []string, externalService bool) {
	t.Helper()
	prevIface, prevExt := interfaceExists, externalPTPServiceRunning
	interfaceExists = func(name string) bool {
		for _, n := range interfaces {
			if n == name {
				return true
			}
		}
		return false
	}
	externalPTPServiceRunning = func() bool { return externalService }
	t.Cleanup(func() { interfaceExists, externalPTPServiceRunning = prevIface, prevExt })
}

func TestRejectionReason(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		external bool
		want     string
	}{
		{"audio output named as the interface", Config{Provider: ProviderManaged, Interface: "alsa_output.usb-MOTU_M4"}, true, "alsa_output.usb-MOTU_M4 is not a network interface on this node"},
		{"agent ptp4l beside the installed service", Config{Provider: ProviderManaged, Interface: "eno2"}, true, "already runs its own PTP service"},
		{"anything else", Config{Provider: ProviderManaged, Interface: "eno2"}, false, "could not use its clock settings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubHost(t, []string{"eno1", "eno2"}, tc.external)
			if got := RejectionReason(tc.cfg); !strings.Contains(got, tc.want) {
				t.Fatalf("RejectionReason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestManagerReportsRejectedSettingsAsFailed(t *testing.T) {
	stubHost(t, []string{"eno2"}, false)
	m := NewManager(func() time.Time { return time.Unix(0, 0) }, nil)
	if err := m.SetConfig(context.Background(), Config{Provider: "bogus", Interface: "wlan9"}); err == nil {
		t.Fatalf("expected the unknown provider to be refused")
	}

	s := m.Poll(context.Background())
	if s.State != StateFailed || !strings.Contains(s.Reason, "wlan9 is not a network interface") {
		t.Fatalf("status = %+v, want failed with the refusal reason", s)
	}

	if err := m.SetConfig(context.Background(), Config{Provider: ProviderExternal, Interface: "eno2", Domain: 1,
		ExternalUDSAddress: "/run/showmesh-clock-rejection-test-nonexistent"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	defer func() { _ = m.Close() }()
	if s := m.Poll(context.Background()); strings.Contains(s.Reason, "not a network interface") {
		t.Fatalf("status = %+v, want the old refusal cleared once settings are accepted", s)
	}
}
