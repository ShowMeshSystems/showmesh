package agent

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

// fallbackProgramValidity is the validity the coordinator gives a program
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 5.12). The agent cannot
// import the coordinator's constant, and a node reads only expiresAt.
const fallbackProgramValidity = 24 * time.Hour

const cutoffTestFPPHost = "22222222-2222-4222-8222-222222222222"

// cutoffTestPlugin stands in for the plugin on the node routes: it holds a
// signed program and signs one request per entry occurrence, as section
// 5.6 and 5.8 fix them.
type cutoffTestPlugin struct {
	t        *testing.T
	node     *fallbackTestNode
	program  fallbackprogram.Program
	document string
	key      ed25519.PrivateKey
	sent     int
}

func newCutoffTestPlugin(t *testing.T, compiledAt time.Time) *cutoffTestPlugin {
	t.Helper()
	cases := loadFallbackFixtureCases(t)
	coordinatorSeed := make([]byte, ed25519.SeedSize)
	coordinatorSeed[0] = 9
	coordinator := ed25519.NewKeyFromSeed(coordinatorSeed)
	executorSeed := make([]byte, ed25519.SeedSize)
	executorSeed[0] = 3
	executor := ed25519.NewKeyFromSeed(executorSeed)

	cue := cases.NodeCatalog.Cues[0]
	program := fallbackprogram.Program{
		SchemaVersion: fallbackprogram.SchemaVersion, PackageID: "pkg-long-show", Revision: "rev-long-show",
		ExpiresAt: compiledAt.Add(fallbackProgramValidity), CompiledAt: compiledAt, FPPInstanceUUID: cutoffTestFPPHost,
		ExecutorPublicKey: fallbackprogram.ExecutorKeyEncoding.EncodeToString(executor.Public().(ed25519.PublicKey)),
		Show:              cases.NodeCatalog.Show, Generation: cases.NodeCatalog.Generation,
		PlaylistRevisions: map[string]int64{"playlist-1": 1},
		CatalogRevisions:  map[string]string{cases.NodeID: cases.NodeCatalog.CatalogRevision},
		Entries: []fallbackprogram.EntryMapping{{
			EntryKey: "entry-0", CueID: cue.CueID, CueRevision: cue.CueRevision,
			Targets: []fallbackprogram.NodeTarget{{NodeID: cases.NodeID, Address: "192.0.2.20:80",
				Render: &fallbackprogram.RenderActivation{Sequence: "seq", Filename: "seq.fseq", AssetHashes: []string{}}}},
		}},
		Rules: fallbackprogram.FixedRules,
	}
	canonical, err := program.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonical program bytes: %v", err)
	}
	document, err := json.Marshal(fallbackprogram.SignedProgram{
		Program: program, Signature: coordsig.Signature(ed25519.Sign(coordinator, canonical)),
	})
	if err != nil {
		t.Fatalf("marshal signed program: %v", err)
	}
	node := newFallbackTestNode(t, cases, coordinator.Public().(ed25519.PublicKey), compiledAt)
	p := &cutoffTestPlugin{t: t, node: node, program: program, document: string(document), key: executor}
	if status, resp := node.do(http.MethodPut, fallbackactivation.ProgramPathPrefix+cutoffTestFPPHost, p.document); status != http.StatusOK || resp.Outcome != "installed" {
		t.Fatalf("hand the node its program: status %d outcome %q (%s)", status, resp.Outcome, resp.Reason)
	}
	return p
}

// request is the signed body for a new entry occurrence.
func (p *cutoffTestPlugin) request() string {
	p.t.Helper()
	p.sent++
	body, err := fallbackactivation.Sign(fallbackactivation.Request{
		SchemaVersion:   fallbackactivation.SchemaVersion,
		ExecutionID:     fmt.Sprintf("00000000-0000-4000-8000-%012d", p.sent),
		FPPInstanceUUID: cutoffTestFPPHost, PackageID: p.program.PackageID, PackageRevision: p.program.Revision,
		ProgramExpiresAt: p.program.ExpiresAt, Generation: p.program.Generation,
		CatalogRevision: p.program.CatalogRevisions[p.node.nodeID], EntryKey: "entry-0",
		CueID: p.program.Entries[0].CueID, CueRevision: p.program.Entries[0].CueRevision, NodeID: p.node.nodeID,
	}, p.key)
	if err != nil {
		p.t.Fatalf("sign activation request: %v", err)
	}
	return string(body)
}

// The coordinator republishes a program once half its validity is gone,
// so the copy a plugin holds when the coordinator is lost has at least 12
// hours left. A show that was playing then, and every show after it that
// night, is carried entry by entry, and every entry runs once.
func TestFallbackProgramHeldWhenLossBeganCarriesTheShowToItsEnd(t *testing.T) {
	compiledAt := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	p := newCutoffTestPlugin(t, compiledAt)

	// The worst case: loss begins just as the copy has 12 hours left.
	lossBegan := compiledAt.Add(fallbackProgramValidity / 2)
	const entryLength = 4 * time.Minute
	entries := 0
	for at := lossBegan.Add(time.Minute); at.Before(lossBegan.Add(6 * time.Hour)); at = at.Add(entryLength) {
		p.node.setNow(at)
		body := p.request()
		status, resp := p.node.postActivation(body)
		if status != http.StatusOK || !resp.Accepted || resp.Outcome != string(fallbackactivation.OutcomeAuthorized) {
			t.Fatalf("%s into the outage: status %d outcome %q (%s), want the entry's Cue started",
				at.Sub(lossBegan), status, resp.Outcome, resp.Reason)
		}
		entries++
		// A retry of the same request must not start the Cue again.
		if _, retry := p.node.postActivation(body); retry.Accepted || retry.Outcome != string(fallbackactivation.OutcomeReplayedExecution) {
			t.Fatalf("a retry of an entry's request answered %q accepted %v, want a replay that starts nothing", retry.Outcome, retry.Accepted)
		}
	}
	if entries < 80 || p.node.activationCount() != entries {
		t.Fatalf("%d entries were sent and the node started %d Cues, want one start per entry over six hours", entries, p.node.activationCount())
	}
}

// At the cutoff the node starts nothing more under that copy, and does
// nothing else: section 5.14.
func TestFallbackNodeStartsNothingAfterTheCutoff(t *testing.T) {
	compiledAt := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	p := newCutoffTestPlugin(t, compiledAt)
	cutoff := p.program.ExpiresAt

	p.node.setNow(cutoff.Add(-time.Second))
	if status, resp := p.node.postActivation(p.request()); status != http.StatusOK || !resp.Accepted {
		t.Fatalf("one second before the cutoff: status %d outcome %q, want the Cue started", status, resp.Outcome)
	}
	for _, after := range []time.Duration{0, time.Second, time.Hour} {
		p.node.setNow(cutoff.Add(after))
		status, resp := p.node.postActivation(p.request())
		if resp.Accepted || resp.Outcome != string(fallbackactivation.OutcomeProgramExpired) || status != http.StatusConflict {
			t.Fatalf("%s after the cutoff: status %d outcome %q accepted %v, want program-expired", after, status, resp.Outcome, resp.Accepted)
		}
	}
	if p.node.activationCount() != 1 {
		t.Fatalf("the node started %d Cues, want only the one before the cutoff", p.node.activationCount())
	}
}
