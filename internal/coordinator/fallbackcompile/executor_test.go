package fallbackcompile

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

const testExecutorKey = "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="

type fakeNodeAddresses map[string]string

func (f fakeNodeAddresses) InboundListener(nodeID string) (string, bool) {
	addr, ok := f[nodeID]
	return addr, ok
}

func (f baseFixture) compileWith(t *testing.T, addrs NodeAddresses) Result {
	t.Helper()
	result, err := CompileWithAddresses(context.Background(), f.st, fakeSigner{}, addrs, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("CompileWithAddresses: %v", err)
	}
	f.requirePublished(t, result)
	return result
}

func (f baseFixture) registerExecutorKey(t *testing.T, key string) {
	t.Helper()
	if _, _, err := f.st.PutFallbackExecutorKey(context.Background(), store.FallbackExecutorKeyRecord{
		FPPInstanceUUID: testInstanceUUID, PublicKeyB64: key, RegisteredAt: f.now,
	}); err != nil {
		t.Fatalf("put executor key: %v", err)
	}
}

func TestProgramWithNoRegisteredKeyCarriesNone(t *testing.T) {
	f := newBaseFixture(t)
	result := f.compileWith(t, nil)
	if got := result.Program.Program.ExecutorPublicKey; got != "" {
		t.Fatalf("a host with no registered key published executor key %q", got)
	}
}

func TestRegisteredExecutorKeyIsCarriedAndProducesNewSignedPackage(t *testing.T) {
	f := newBaseFixture(t)
	before := f.compileWith(t, nil)

	f.registerExecutorKey(t, testExecutorKey)
	after := f.compileWith(t, nil)

	if after.Program.Program.ExecutorPublicKey != testExecutorKey {
		t.Fatalf("program executor key = %q, want the registered key", after.Program.Program.ExecutorPublicKey)
	}
	if after.Program.Program.Revision == before.Program.Program.Revision {
		t.Fatalf("a first registered key did not change the package revision %q", before.Program.Program.Revision)
	}
	if after.Program.Program.PackageID == before.Program.Program.PackageID {
		t.Fatalf("a first registered key kept package id %q", before.Program.Program.PackageID)
	}
}

func TestRevisionHashCoversExecutorPublicKey(t *testing.T) {
	f := newBaseFixture(t)
	f.registerExecutorKey(t, testExecutorKey)
	result := f.compileWith(t, nil)
	requireRevisionInputFieldCovered(t, result.Program.Program, "ExecutorPublicKey", func(in *fallbackprogram.RevisionInput) {
		in.ExecutorPublicKey = ""
	})
}

func TestTargetCarriesTheNodesReportedAddress(t *testing.T) {
	f := newBaseFixture(t)
	without := f.compileWith(t, nil)
	if got := without.Program.Program.Entries[0].Targets[0].Address; got != "" {
		t.Fatalf("a node with no reported address was published with address %q", got)
	}

	with := f.compileWith(t, fakeNodeAddresses{f.nodeID: "192.0.2.21:80"})
	if got := with.Program.Program.Entries[0].Targets[0].Address; got != "192.0.2.21:80" {
		t.Fatalf("target address = %q, want the node's reported address", got)
	}
	if with.Program.Program.Revision == without.Program.Program.Revision {
		t.Fatalf("a target address did not change the package revision")
	}
}

// A coordinator that has just restarted knows no node address until the
// nodes report again. It must not publish a program that forgets them.
func TestUnknownAddressKeepsTheOneAlreadyPublished(t *testing.T) {
	f := newBaseFixture(t)
	published := f.compileWith(t, fakeNodeAddresses{f.nodeID: "192.0.2.21:80"})
	if err := f.st.PutFallbackProgram(context.Background(), publishedRecord(t, published)); err != nil {
		t.Fatalf("store published program: %v", err)
	}

	afterRestart := f.compileWith(t, fakeNodeAddresses{})
	if got := afterRestart.Program.Program.Entries[0].Targets[0].Address; got != "192.0.2.21:80" {
		t.Fatalf("target address after a restart = %q, want the published one kept", got)
	}
	if afterRestart.Program.Program.Revision != published.Program.Program.Revision {
		t.Fatalf("an unchanged show republished under a new revision after a restart")
	}
}
