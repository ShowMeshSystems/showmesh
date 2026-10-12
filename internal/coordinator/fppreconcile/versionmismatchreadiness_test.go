package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/version"
)

func putNodeVersion(t *testing.T, st *store.Store, nodeID, agentVersion string) {
	t.Helper()
	now := time.Now()
	err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{
		Platform: "linux/arm64", AgentVersion: agentVersion, BootID: "boot-" + nodeID, StartedAt: now,
		ObservedAt: &now, Provenance: store.ProvenanceAgentReport,
	})
	if err != nil {
		t.Fatalf("UpsertHello(%s): %v", nodeID, err)
	}
}

func versionReadyFixture(t *testing.T, st *store.Store) (report func() Report) {
	t.Helper()
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "0.2.0"
	p := multisyncReadyPlaylistFixture(t, st)
	putFPPSignal(t, st, "inst-1", fpp.SignalMultiSyncEnabled, true)
	putFPPSignal(t, st, "inst-1", fpp.SignalMultiSyncSystemHostnames, "node-1")
	return func() Report {
		r, err := PlaylistReadiness(context.Background(), st, nil, nil, "playlist-1", 1, p)
		if err != nil {
			t.Fatalf("PlaylistReadiness: %v", err)
		}
		return r
	}
}

func TestPlaylistReadinessWarnsOncePerMismatchedNode(t *testing.T) {
	st := openTestStore(t)
	report := versionReadyFixture(t, st)
	putNodeVersion(t, st, "node-1", "0.1.5")
	putNodeVersion(t, st, "node-2", "0.2.0")
	putNodeVersion(t, st, "node-3", "0.1.9")

	r := report()
	if !r.Ready || r.FailingCondition != "" {
		t.Fatalf("Ready = %v, FailingCondition = %q: a version mismatch is a warning, never a failure", r.Ready, r.FailingCondition)
	}
	want := "Node node-1 runs 0.1.5. Install 0.2.0 on it to match the coordinator.; " +
		"Node node-3 runs 0.1.9. Install 0.2.0 on it to match the coordinator."
	if r.Warning != want {
		t.Fatalf("Warning = %q, want %q", r.Warning, want)
	}
}

func TestPlaylistReadinessIsSilentWhenEveryNodeMatches(t *testing.T) {
	st := openTestStore(t)
	report := versionReadyFixture(t, st)
	putNodeVersion(t, st, "node-1", "v0.2.0")

	if r := report(); !r.Ready || strings.Contains(r.Warning, "Install") {
		t.Fatalf("Ready = %v, Warning = %q, want ready with no version warning", r.Ready, r.Warning)
	}
}
