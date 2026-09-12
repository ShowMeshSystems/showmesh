package clock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestExternalProviderPMCUnavailableReportsUnknownNotFailed covers the
// second half of the pmc-socket bug: pmc's own client-side setup failing
// (here, the binary is simply missing) is never evidence that the
// OBSERVED ptp4l is down, and must not report StateFailed. The read-only
// UDS socket exists and is healthy; only the agent's own pmc tooling is
// broken.
func TestExternalProviderPMCUnavailableReportsUnknownNotFailed(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "ptp4lro")
	if err := os.WriteFile(uds, nil, 0o666); err != nil {
		t.Fatalf("create fake socket file: %v", err)
	}

	orig := pmcBinary
	pmcBinary = filepath.Join(t.TempDir(), "no-such-pmc")
	defer func() { pmcBinary = orig }()

	p := NewExternalProvider(ExternalConfig{Interface: "eth0", UDSAddress: uds})
	raw := p.Poll(context.Background())

	if !raw.Reachable {
		t.Fatalf("pmc tooling failure must not report Reachable=false (that reads as StateFailed): %+v", raw)
	}
	if raw.Locked {
		t.Fatalf("a failed pmc invocation must never claim a lock: %+v", raw)
	}
	if raw.Reason == "" {
		t.Fatalf("Reason is required when the state is not locked")
	}

	tr := NewTracker(&fakeProvider{raws: []RawStatus{raw}}, TrackerConfig{}, nil)
	status := tr.Poll(context.Background())
	if status.State == StateFailed {
		t.Fatalf("pmc tooling failure must not surface as node.clock.ptp.state=failed, got %q with reason %q", status.State, status.Reason)
	}
}
