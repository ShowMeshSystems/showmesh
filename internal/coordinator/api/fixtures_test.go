package api

import (
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// testTZ and testNow anchor every fixture and golden file to one fixed
// instant, so re-running a test never produces a different timestamp. The
// offset and fractional-second shape deliberately echo contract section
// 6.3's own pinned example ("2026-08-10T21:14:22.481-05:00"), so a golden
// file reviewer can compare this package's actual output against the
// contract's own example almost directly.
var testTZ = time.FixedZone("", -5*3600)
var testNow = time.Date(2026, 8, 10, 21, 14, 22, 481000000, testTZ)

func t3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("t3339(%q): %v", s, err)
	}
	return tm
}

// mustObs mirrors mapping.go's mustObservation: it must be called with the
// observation-constructor call as its ONLY argument (e.g.
// mustObs(observation.Measured(...))), never with a second argument added
// alongside it — Go's multi-value-argument spreading rule only applies
// when the inner call is the sole argument, so splitting this into
// mustObs(t, observation.Measured(...)) does not compile. Panicking rather
// than calling t.Fatalf keeps every fixture builder a plain expression
// instead of needing a *testing.T threaded through it.
func mustObs(o observation.Observation, err error) observation.Observation {
	if err != nil {
		panic("building fixture observation: " + err.Error())
	}
	return o
}

// onlineNodeFixture is a node with a full, live hello/lastWill/heartbeat
// trail, all observed shortly before testNow — the "everything is fine"
// case, and the pinned contract section 6.10 example's own shape.
func onlineNodeFixture(t *testing.T) inventory.NodeView {
	t.Helper()
	helloAt := t3339(t, "2026-08-10T20:00:05-05:00")
	lwtAt := t3339(t, "2026-08-10T21:14:18-05:00")
	healthAt := t3339(t, "2026-08-10T21:14:20-05:00")

	return inventory.NodeView{
		NodeID: "media-03",
		Hello: &store.HelloRecord{
			Label: "Garage media node", Platform: "linux-amd64", AgentVersion: "dev",
			BootID:    "boot-abc123",
			StartedAt: t3339(t, "2026-08-10T20:00:00-05:00"),
			Capabilities: capability.Set{
				{ID: "matrix.render", Version: 1, Attributes: map[string]any{"max_width": 1920}},
			},
			ObservedAt: &helloAt, Provenance: store.ProvenanceAgentReport, Retained: false,
		},
		LWT: &store.LWTRecord{
			Online: true, ObservedAt: &lwtAt, Provenance: store.ProvenanceAgentReport, Retained: false,
		},
		Health: &store.HealthRecord{
			BootID: "boot-abc123", Sequence: 42, AgentState: "running", UptimeMS: 4460000,
			ObservedAt: &healthAt, Provenance: store.ProvenanceAgentReport, Retained: false,
		},
		FirstSeenAt: t3339(t, "2026-08-10T20:00:01-05:00"),
		UpdatedAt:   healthAt,
		Liveness:    inventory.LivenessOnline, LivenessReason: "",
	}
}

// retainedOnlyNodeFixture is a node known only from a retained last-will
// replay (e.g. what a just-restarted coordinator sees on subscribe): no
// hello, no health, and an evidence entry whose observation time is
// unknown. This is the exact case contract section 3.3 exists to protect:
// a retained delivery must render observedAt: null and state:
// "unknown_age", never a fabricated freshness.
func retainedOnlyNodeFixture(t *testing.T) inventory.NodeView {
	t.Helper()
	return inventory.NodeView{
		NodeID: "shed-01",
		LWT: &store.LWTRecord{
			Online: true, ObservedAt: nil, Provenance: store.ProvenanceRetainedBrokerState, Retained: true,
		},
		FirstSeenAt:    t3339(t, "2026-08-10T18:00:00-05:00"),
		UpdatedAt:      t3339(t, "2026-08-10T18:00:00-05:00"),
		Liveness:       inventory.LivenessUnknown,
		LivenessReason: "last-will reports online but no health heartbeat has been observed yet",
	}
}

// fppInstanceFixture carries one current observation, one not-collected
// absence, and one stale observation, plus a credentialed endpoint the
// mapping layer must sanitize before it ever reaches the wire.
func fppInstanceFixture(t *testing.T) FPPInstanceView {
	t.Helper()
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}
	multisyncAt := t3339(t, "2026-08-10T21:14:10-05:00")
	staleAt := t3339(t, "2026-08-10T21:00:00-05:00")
	pollAt := t3339(t, "2026-08-10T21:14:20-05:00")
	// collectedAt is fixed, not time.Now() (the constructors' own
	// default), so this fixture — and every golden file built from it —
	// is deterministic across runs. Missing this is exactly the kind of
	// thing that makes a golden test flaky for a reason that has nothing
	// to do with the code under test.
	collectedAt := t3339(t, "2026-08-10T21:14:22.600-05:00")

	return FPPInstanceView{
		InstanceID: "player-01",
		Endpoint:   "http://user:pass@10.0.1.20",
		Observations: []observation.Observation{
			mustObs(observation.Measured(res, "fpp.multisync.enabled", false, multisyncAt,
				observation.WithSource("fpp-rest"), observation.WithValidFor(15*time.Second), observation.WithCollectedAt(collectedAt))),
			mustObs(observation.NotCollected(res, "fpp.status",
				"collector has not completed its first poll for this signal",
				observation.WithSource("fpp-rest"), observation.WithCollectedAt(collectedAt))),
			mustObs(observation.Measured(res, "fpp.uptime.seconds", int64(120), staleAt,
				observation.WithSource("fpp-rest"), observation.WithValidFor(30*time.Second), observation.WithCollectedAt(collectedAt))),
		},
		LastPollAt: &pollAt,
	}
}

func eventFixture() EventRecord {
	return EventRecord{
		Seq:        37,
		RecordedAt: testNow,
		OccurredAt: nil,
		Source:     "mqtt-inventory",
		Resource:   observation.ResourceRef{Kind: observation.ResourceNode, ID: "media-03"},
		Category:   "control_plane",
		Severity:   "informational",
		Summary:    "node control-plane state changed to offline",
		Details:    map[string]any{},
	}
}
