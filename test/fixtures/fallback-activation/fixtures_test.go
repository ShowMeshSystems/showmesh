// Package fallbackactivationfixtures generates and checks the JSON files
// beside it. Run `go test ./test/fixtures/fallback-activation -update` to
// rewrite them; a plain run fails when a file no longer matches what the
// Go implementation produces.
package fallbackactivationfixtures

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fallbackactivation"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
)

var update = flag.Bool("update", false, "rewrite the fixture files")

// The three key pairs are the RFC 8032 section 7.1 test vectors, so no
// file here holds a key anyone uses.
const (
	executorSeedHex      = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	executorPublicHex    = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
	otherSeedHex         = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"
	otherPublicHex       = "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"
	coordinatorSeedHex   = "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7"
	coordinatorPublicHex = "fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025"
)

const (
	nodeID        = "node-a"
	otherNodeID   = "node-b"
	fppHost       = "22222222-2222-4222-8222-222222222222"
	otherFPPHost  = "33333333-3333-4333-8333-333333333333"
	absentFPPHost = "44444444-4444-4444-8444-444444444444"
	show          = "halloween"
	catalogRev    = "cat-rev-1"
)

var (
	compiledAt = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	expiresAt  = compiledAt.Add(15 * time.Minute)
	now        = compiledAt.Add(5 * time.Minute)
)

func keyFromSeed(t *testing.T, seedHex, publicHex string) ed25519.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	if got := hex.EncodeToString(key.Public().(ed25519.PublicKey)); got != publicHex {
		t.Fatalf("seed %s derives public key %s, want the RFC 8032 value %s", seedHex, got, publicHex)
	}
	return key
}

func publicB64(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func render(name string) *fallbackprogram.RenderActivation {
	return &fallbackprogram.RenderActivation{Sequence: name, Filename: name + ".fseq", AssetHashes: []string{"sha256-" + name}}
}

func buildProgram(host, packageID, revision, executorKey string, compiled time.Time) fallbackprogram.Program {
	return fallbackprogram.Program{
		SchemaVersion: fallbackprogram.SchemaVersion, PackageID: packageID, Revision: revision,
		ExpiresAt: compiled.Add(15 * time.Minute), CompiledAt: compiled,
		FPPInstanceUUID: host, ExecutorPublicKey: executorKey, Show: show, Generation: 7,
		PlaylistRevisions: map[string]int64{"pl-main": 4},
		CatalogRevisions:  map[string]string{nodeID: catalogRev, otherNodeID: "cat-rev-b"},
		Entries: []fallbackprogram.EntryMapping{
			{EntryKey: "entry-0", CueID: "cue-a", CueRevision: 3, Targets: []fallbackprogram.NodeTarget{
				{NodeID: nodeID, Address: "192.0.2.21:80", Render: render("seq-a")},
				{NodeID: otherNodeID, Address: "192.0.2.22:80", Render: render("seq-a")},
			}},
			{EntryKey: "entry-1", CueID: "cue-b", CueRevision: 5, Targets: []fallbackprogram.NodeTarget{
				{NodeID: otherNodeID, Address: "192.0.2.22:80", Render: render("seq-b")},
			}},
		},
		Rules: fallbackprogram.FixedRules,
	}
}

func signProgram(t *testing.T, p fallbackprogram.Program, coordinator ed25519.PrivateKey) []byte {
	t.Helper()
	canonical, err := p.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize program: %v", err)
	}
	doc, err := json.Marshal(struct {
		Program   json.RawMessage    `json:"program"`
		Signature coordsig.Signature `json:"signature"`
	}{Program: canonical, Signature: ed25519.Sign(coordinator, canonical)})
	if err != nil {
		t.Fatalf("marshal signed program: %v", err)
	}
	return doc
}

func baseRequest(p fallbackprogram.Program, executionID string) fallbackactivation.Request {
	return fallbackactivation.Request{
		SchemaVersion: fallbackactivation.SchemaVersion, ExecutionID: executionID,
		FPPInstanceUUID: p.FPPInstanceUUID, PackageID: p.PackageID, PackageRevision: p.Revision,
		ProgramExpiresAt: p.ExpiresAt, Generation: p.Generation, CatalogRevision: p.CatalogRevisions[nodeID],
		EntryKey: "entry-0", CueID: "cue-a", CueRevision: 3, NodeID: nodeID,
	}
}

type activationCase struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Installed       []string `json:"installed"`
	Now             string   `json:"now"`
	Body            string   `json:"body"`
	RepeatBody      bool     `json:"repeatBody,omitempty"`
	ExpectedStatus  int      `json:"expectedStatus"`
	ExpectedOutcome string   `json:"expectedOutcome"`
}

type programCase struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Installed       []string `json:"installed"`
	Now             string   `json:"now"`
	PathHost        string   `json:"pathFppInstanceUuid"`
	Document        string   `json:"document"`
	ExpectedStatus  int      `json:"expectedStatus"`
	ExpectedOutcome string   `json:"expectedOutcome"`
}

type heldCue struct {
	CueID       string `json:"cueId"`
	CueRevision int64  `json:"cueRevision"`
}

type casesFile struct {
	Description string `json:"description"`
	NodeID      string `json:"nodeId"`
	NodeCatalog struct {
		Show            string    `json:"show"`
		Generation      int64     `json:"generation"`
		CatalogRevision string    `json:"catalogRevision"`
		Cues            []heldCue `json:"cues"`
	} `json:"nodeCatalog"`
	ValidRequest struct {
		Canonical string `json:"canonical"`
		Signature string `json:"signature"`
	} `json:"validRequest"`
	Activations []activationCase `json:"activations"`
	Programs    []programCase    `json:"programs"`
}

type keysFile struct {
	Description          string `json:"description"`
	CoordinatorPublicKey string `json:"coordinatorPublicKey"`
	ExecutorSeedHex      string `json:"executorSeedHex"`
	ExecutorPublicKey    string `json:"executorPublicKey"`
	OtherExecutorSeedHex string `json:"otherExecutorSeedHex"`
	OtherExecutorPublic  string `json:"otherExecutorPublicKey"`
}

func build(t *testing.T) map[string][]byte {
	t.Helper()
	executor := keyFromSeed(t, executorSeedHex, executorPublicHex)
	other := keyFromSeed(t, otherSeedHex, otherPublicHex)
	coordinator := keyFromSeed(t, coordinatorSeedHex, coordinatorPublicHex)

	current := buildProgram(fppHost, "pkg-current", "rev-current", publicB64(executor), compiledAt)
	older := buildProgram(fppHost, "pkg-older", "rev-older", publicB64(executor), compiledAt.Add(-time.Minute))
	noKey := buildProgram(fppHost, "pkg-no-key", "rev-no-key", "", compiledAt)
	otherHost := buildProgram(otherFPPHost, "pkg-other-host", "rev-other-host", publicB64(other), compiledAt)
	notTargeted := buildProgram(fppHost, "pkg-not-targeted", "rev-not-targeted", publicB64(executor), compiledAt)
	notTargeted.Entries = notTargeted.Entries[1:]

	files := map[string][]byte{
		"program.json":                 signProgram(t, current, coordinator),
		"program-older.json":           signProgram(t, older, coordinator),
		"program-no-executor-key.json": signProgram(t, noKey, coordinator),
		"program-other-host.json":      signProgram(t, otherHost, coordinator),
		"program-not-targeted.json":    signProgram(t, notTargeted, coordinator),
		"program-wrong-signer.json":    signProgram(t, current, other),
	}

	sign := func(r fallbackactivation.Request, key ed25519.PrivateKey) string {
		body, err := fallbackactivation.Sign(r, key)
		if err != nil {
			t.Fatalf("sign request: %v", err)
		}
		return string(body)
	}
	id := func(n byte) string {
		return "00000000-0000-4000-8000-0000000000" + hex.EncodeToString([]byte{n})
	}
	mutated := func(n byte, mutate func(*fallbackactivation.Request)) string {
		r := baseRequest(current, id(n))
		mutate(&r)
		return sign(r, executor)
	}
	nowText := now.Format(time.RFC3339)
	held := []string{"program.json", "program-other-host.json"}

	valid := baseRequest(current, id(1))
	validBody := sign(valid, executor)
	validCanonical, err := valid.CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalize valid request: %v", err)
	}

	tampered := bytes.Replace([]byte(sign(baseRequest(current, id(3)), executor)), []byte(`"cueId":"cue-a"`), []byte(`"cueId":"cue-b"`), 1)
	extraMember := func() string {
		r := baseRequest(current, id(4))
		raw, _ := json.Marshal(r)
		var members map[string]any
		_ = json.Unmarshal(raw, &members)
		members["action"] = "blackout"
		withExtra, _ := json.Marshal(members)
		body, _ := json.Marshal(map[string]any{
			"request": json.RawMessage(withExtra), "signature": coordsig.Signature(ed25519.Sign(executor, withExtra)),
		})
		return string(body)
	}()

	var out casesFile
	out.Description = "Fallback activation cases for contract section 5. Each case states what the node holds and its clock, one request body, and the answer."
	out.NodeID = nodeID
	out.NodeCatalog.Show, out.NodeCatalog.Generation, out.NodeCatalog.CatalogRevision = show, 7, catalogRev
	out.NodeCatalog.Cues = []heldCue{{"cue-a", 3}, {"cue-b", 5}}
	out.ValidRequest.Canonical = string(validCanonical)
	out.ValidRequest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(executor, validCanonical))

	out.Activations = []activationCase{
		{"valid", "A request for a listed entry, Cue and target, signed by the enrolled executor.", held, nowText, validBody, false, 200, "authorized"},
		{"replayed-execution", "The valid request sent a second time.", held, nowText, validBody, true, 409, "replayed-execution"},
		{"invalid-signature", "A well formed request signed by a different key.", held, nowText, sign(baseRequest(current, id(2)), other), false, 403, "signature-invalid"},
		{"tampered-after-signing", "A signed request whose Cue was changed afterwards.", held, nowText, string(tampered), false, 403, "signature-invalid"},
		{"arbitrary-member", "A correctly signed request that carries a member this route does not accept.", held, nowText, extraMember, false, 400, "malformed-request"},
		{"wrong-fpp-host-with-program", "This executor names another FPP player whose program the node also holds.", held, nowText, mutated(5, func(r *fallbackactivation.Request) { r.FPPInstanceUUID = otherFPPHost }), false, 403, "signature-invalid"},
		{"wrong-fpp-host-no-program", "This executor names an FPP player the node holds no program for.", held, nowText, mutated(6, func(r *fallbackactivation.Request) { r.FPPInstanceUUID = absentFPPHost }), false, 409, "program-not-installed"},
		{"wrong-target", "A request addressed to another node.", held, nowText, mutated(7, func(r *fallbackactivation.Request) { r.NodeID = otherNodeID }), false, 403, "wrong-target"},
		{"entry-not-for-this-node", "A listed entry whose targets do not include this node.", held, nowText, mutated(8, func(r *fallbackactivation.Request) { r.EntryKey, r.CueID, r.CueRevision = "entry-1", "cue-b", 5 }), false, 403, "wrong-target"},
		{"stale-generation", "A request for an earlier activation of the show.", held, nowText, mutated(9, func(r *fallbackactivation.Request) { r.Generation = 6 }), false, 409, "stale-generation"},
		{"stale-catalog", "A request for a different Cue catalog than the program names for this node.", held, nowText, mutated(10, func(r *fallbackactivation.Request) { r.CatalogRevision = "cat-rev-0" }), false, 409, "stale-catalog"},
		{"unknown-entry", "A request for an entry the program does not list.", held, nowText, mutated(11, func(r *fallbackactivation.Request) { r.EntryKey = "entry-9" }), false, 409, "unknown-entry"},
		{"arbitrary-cue", "A listed entry with a Cue the program does not map it to.", held, nowText, mutated(12, func(r *fallbackactivation.Request) { r.CueID, r.CueRevision = "cue-b", 5 }), false, 403, "cue-not-authorized"},
		{"wrong-cue-revision", "A listed entry and Cue at a different Cue revision.", held, nowText, mutated(13, func(r *fallbackactivation.Request) { r.CueRevision = 2 }), false, 403, "cue-not-authorized"},
		{"program-not-current", "A request made under a copy of the program the node does not hold.", held, nowText, mutated(14, func(r *fallbackactivation.Request) { r.PackageID = "pkg-older" }), false, 409, "program-not-current"},
		{"expired-program", "The valid request shape after the held program has expired.", held, expiresAt.Add(time.Second).Format(time.RFC3339), sign(baseRequest(current, id(15)), executor), false, 409, "program-expired"},
		{"executor-not-enrolled", "The held program carries no executor key.", []string{"program-no-executor-key.json"}, nowText, sign(baseRequest(noKey, id(16)), executor), false, 403, "executor-not-enrolled"},
	}

	out.Programs = []programCase{
		{"valid", "A program signed by the coordinator for this FPP player that names this node.", nil, nowText, fppHost, "program.json", 200, "installed"},
		{"wrong-signer", "The same program signed by a key that is not the coordinator's.", nil, nowText, fppHost, "program-wrong-signer.json", 403, "program-signature-invalid"},
		{"wrong-fpp-host", "A valid program delivered under another FPP player's path.", nil, nowText, otherFPPHost, "program.json", 403, "wrong-fpp-host"},
		{"not-targeted", "A valid program that does not name this node.", nil, nowText, fppHost, "program-not-targeted.json", 403, "wrong-target"},
		{"expired", "A valid program delivered after its expiry.", nil, expiresAt.Add(time.Second).Format(time.RFC3339), fppHost, "program.json", 409, "program-expired"},
		{"superseded", "An older copy delivered after a newer one is held.", []string{"program.json"}, nowText, fppHost, "program-older.json", 409, "program-superseded"},
		{"same-copy-again", "The held copy delivered again.", []string{"program.json"}, nowText, fppHost, "program.json", 200, "installed"},
	}

	cases, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}
	files["cases.json"] = append(cases, '\n')

	keys, err := json.MarshalIndent(keysFile{
		Description:          "RFC 8032 section 7.1 test vectors. The coordinator's seed is test 3 and is not needed by a consumer.",
		CoordinatorPublicKey: publicB64(coordinator),
		ExecutorSeedHex:      executorSeedHex, ExecutorPublicKey: publicB64(executor),
		OtherExecutorSeedHex: otherSeedHex, OtherExecutorPublic: publicB64(other),
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal keys: %v", err)
	}
	files["keys.json"] = append(keys, '\n')
	return files
}

func TestFixturesMatchTheImplementation(t *testing.T) {
	for name, want := range build(t) {
		if *update {
			if err := os.WriteFile(name, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			continue
		}
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v (run with -update to create it)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s no longer matches what the implementation produces; run with -update and review the diff", name)
		}
	}
}
