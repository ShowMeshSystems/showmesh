//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// This file is Task F spec item 8: "the full stack survives its
// dependencies: broker unreachable at startup, broker stopped and
// restarted underneath a running coordinator, every FPP endpoint
// unreachable, and SIGTERM in each state. /api/v1 must answer throughout
// with honest absence states rather than 500s — an unreachable FPP is an
// observation, not a server error." ADR-012's core promise, proven against
// the real binary rather than assumed from the source.
//
// "Broker stopped and restarted underneath a running coordinator" is
// TestBrokerRestartResubscribesAndObservesSubsequentChanges
// (broker_restart_test.go), which already proves it more thoroughly than
// this file would (it also proves resubscription, not merely survival) —
// not duplicated here.

// closedPort finds a TCP port on 127.0.0.1 that is guaranteed to refuse
// connections: bind, then immediately close, so nothing else can be
// listening on it at the moment this function returns, but nothing else
// races to claim it either (this process itself never binds it again) —
// good enough for "connection refused" rather than "connection accepted".
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a port to close: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestCoordinatorSurvivesBrokerUnreachableAtStartup is ADR-012's core
// promise: "the coordinator must start and stay up with no broker
// reachable" — checked against a real subprocess pointed at a port nothing
// is listening on, not merely a unit test's fake broker.BrokerManager.
func TestCoordinatorSurvivesBrokerUnreachableAtStartup(t *testing.T) {
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		brokerURL: fmt.Sprintf("tcp://127.0.0.1:%d", closedPort(t)),
	})

	// /healthz must be 200 regardless — it is liveness, not readiness.
	if status, _ := coord.getRaw(t, "/healthz"); status != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200 with the broker unreachable", status)
	}

	// The versioned API must answer normally: an unreachable broker is not
	// a reason the read API itself should fail. /api/v1/nodes legitimately
	// returns an empty list (inventory has nothing yet, honestly), never a
	// 500.
	if status, body := coord.getRaw(t, "/api/v1/nodes"); status != http.StatusOK {
		t.Errorf("/api/v1/nodes status = %d, want 200 with the broker unreachable; body: %s", status, body)
	}
	if status, body := coord.getRaw(t, "/api/v1/"); status != http.StatusOK {
		t.Errorf("/api/v1/ status = %d, want 200 with the broker unreachable; body: %s", status, body)
	}

	// /readyz is honestly not-ready (the broker really is unreachable) —
	// this is the one endpoint contract explicitly allows to say so, via
	// 503, never via the versioned API failing. Bounded wait rather than an
	// immediate single check: readiness.Aggregate needs the broker
	// manager's own first evidence (a failed or pending connection
	// attempt) to exist before it has anything to report at all.
	waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		status, _ := coord.getRaw(t, "/readyz")
		return status == http.StatusServiceUnavailable
	}, "/readyz to report 503 with the broker genuinely unreachable")

	// SIGTERM must still produce a clean, bounded exit even in this state —
	// ADR-012's "must still exit cleanly on SIGTERM" half, per the Task F
	// spec. coord.shutdown() sends SIGTERM and fails t itself if the
	// process does not exit within its own bound; calling it directly
	// (rather than only via t.Cleanup) makes this assertion explicit.
	coord.shutdown()
}

// TestCoordinatorSurvivesUnreachableFPPEndpoint proves the FPP collector's
// own "must never make the coordinator not-ready" design decision (Task C
// spec section 3) end to end: a configured FPP instance nothing is
// listening for reports collection_failed evidence through the read API,
// never a 500, and never makes /readyz depend on it.
func TestCoordinatorSurvivesUnreachableFPPEndpoint(t *testing.T) {
	requireBroker(t)
	instanceID := "unreachable-fpp"
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		fppEndpoints: fmt.Sprintf("%s=http://127.0.0.1:%d", instanceID, closedPort(t)),
	})

	// The instance must appear (it is configured, per contract section 4's
	// not_collected/collection_failed distinction — a configured-but-down
	// instance is not the same as "nothing configured") and must never 500.
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		status, b := coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		if status != http.StatusOK {
			t.Fatalf("/api/v1/fpp/%s status = %d, want 200 even though the instance is unreachable; body: %s", instanceID, status, b)
		}
		var resp struct {
			Instance struct {
				Observations []struct {
					Signal string `json:"signal"`
					State  string `json:"state"`
				} `json:"observations"`
			} `json:"instance"`
		}
		if err := json.Unmarshal(b, &resp); err != nil {
			t.Fatalf("decode fpp instance response: %v; body: %s", err, b)
		}
		for _, o := range resp.Instance.Observations {
			if o.Signal == "fpp.reachable" && o.State == "collection_failed" {
				return true
			}
		}
		return false
	}, "fpp.reachable to read collection_failed for an instance nothing is listening on")

	// Bounded wait, not an immediate check: the broker side of readiness
	// needs its own live evidence to arrive (readiness.Aggregate{bm, st}),
	// independent of anything FPP-related, and that is a genuine
	// asynchronous race against this test process, not something this test
	// should treat as instantaneous.
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		status, _ := coord.getRaw(t, "/readyz")
		return status == http.StatusOK
	}, "/readyz to report 200 (the FPP collector must never affect readiness, Task C spec section 3, once the broker itself is confirmed connected)")

	coord.shutdown()
}
