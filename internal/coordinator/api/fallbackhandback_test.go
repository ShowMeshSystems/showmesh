package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackhold"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/coordsig"
	"github.com/showmeshsystems/showmesh/pkg/fallbackprogram"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// These tests run a show through a coordinator outage and back, Track J
// step J5's acceptance list. The coordinator is real: its HTTP routes, its
// store, its Cue loop and its hold rules. The plugin is a stand-in that
// follows FPP-PLUGIN-COORDINATOR-CONTRACTS.md sections 5.12 to 5.15 and
// reaches the coordinator only through those routes. The node is a model
// that records who started which Cue; the node's own ingress is tested in
// internal/agent.

const (
	handbackInstanceUUID = "22222222-2222-4222-8222-222222222222"
	handbackShow         = "halloween-2026"
	handbackNode         = "audio-01"
	handbackPlaylistName = "Main"
)

var handbackCues = []string{"cue-1", "cue-2", "cue-3"}

// handbackWorld is one show: a store that survives coordinator restarts,
// the coordinator process of the moment, the plugin, and the node model.
type handbackWorld struct {
	t     *testing.T
	setup *failToBlackComposedSetup
	now   time.Time

	playlist      config.ShowPlaylistPayload
	entryKeys     []string
	coordinatorSK ed25519.PrivateKey
	token         string
	operatorToken string

	// The coordinator process. up is false while it is stopped;
	// reachable is false while the plugin cannot reach a running one.
	up        bool
	reachable bool
	api       *API
	loop      *handlers
	held      *cueHeldTracker
	holds     *fallbackhold.Service
	seenSends int

	plugin *handbackPlugin
	// starts is every Cue start the node saw, in order, with who sent it.
	starts    []string
	inventory []store.NodeAssetInventoryRecord
}

func (w *handbackWorld) clock() time.Time { return w.now }

// reportInventory is the node saying again which files it holds, as a
// running node does on its own interval.
func (w *handbackWorld) reportInventory() {
	w.t.Helper()
	held := make([]store.NodeAssetInventoryRecord, len(w.inventory))
	for i, rec := range w.inventory {
		rec.VerifiedAt = w.now
		held[i] = rec
	}
	if err := w.setup.st.ReplaceNodeAssetInventory(w.t.Context(), handbackNode, held,
		store.NodeAssetReportRecord{NodeID: handbackNode, ReportedAt: w.now, Complete: true}); err != nil {
		w.t.Fatalf("replace node asset inventory: %v", err)
	}
}

func (w *handbackWorld) advance(d time.Duration) { w.now = w.now.Add(d) }

func newHandbackWorld(t *testing.T) *handbackWorld {
	t.Helper()
	w := &handbackWorld{t: t, now: time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC), reachable: true}
	w.setup = newFailToBlackComposedSetup(t, w.clock)
	st := w.setup.st

	putShowForTest(t, st, handbackShow, "Halloween 2026")
	putShowModeForTest(t, st, config.ShowModeProgram)
	putAudioNodeForTest(t, st, handbackNode)
	declareNodeForTest(t, st, handbackNode)
	putFreshReportForTest(t, st, handbackNode, w.now)
	w.playlist = config.ShowPlaylistPayload{
		Show: handbackShow, Name: handbackPlaylistName, Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: handbackInstanceUUID, PlaylistName: handbackPlaylistName, PlaylistHash: hash64ForTest("a1"),
		},
	}
	for i, cue := range handbackCues {
		putAudioOnlyCueForTest(t, st, cue, handbackShow)
		w.playlist.Entries = append(w.playlist.Entries, config.ShowPlaylistEntry{
			ID: fmt.Sprintf("entry-%d", i+1), Cue: cue,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: i},
		})
	}
	putPlaylistForTest(t, st, "playlist-1", w.playlist)
	putActiveShowForTest(t, st, handbackShow)
	var inventory []store.NodeAssetInventoryRecord
	for _, cue := range handbackCues {
		hash, filename := "sha256:authorized-"+cue, cue+".wav"
		if _, _, err := st.CreateAsset(t.Context(), store.AssetRecord{
			ID: hash + "-node-" + handbackNode, ShowID: handbackShow, SequenceID: "asset-" + cue,
			TargetKind: store.AssetTargetKindNode, TargetID: handbackNode, MediaType: "audio",
			ContentHash: hash, RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: hash,
		}); err != nil {
			t.Fatalf("create asset for %s: %v", cue, err)
		}
		inventory = append(inventory, store.NodeAssetInventoryRecord{
			NodeID: handbackNode, ContentHash: hash, RuntimeFilename: filename, SizeBytes: 1024, VerifiedAt: w.now,
		})
	}
	w.inventory = inventory
	w.reportInventory()
	for _, entry := range w.playlist.Entries {
		key, err := config.DerivePlaylistEntryKey(w.playlist, entry.ID)
		if err != nil {
			t.Fatalf("derive entry key: %v", err)
		}
		w.entryKeys = append(w.entryKeys, key)
	}

	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	w.coordinatorSK = ed25519.NewKeyFromSeed(seed)

	if _, _, err := st.RecordFPPInstanceUUIDObservation(t.Context(), "bench-fpp", handbackInstanceUUID, w.now); err != nil {
		t.Fatalf("record instance uuid: %v", err)
	}
	pluginPrincipal, err := w.setup.svc.CreatePrincipal(t.Context(), fppPairingPrincipalPrefix+"bench-fpp", identity.KindMachine, identity.RoleScheduler, "")
	if err != nil {
		t.Fatalf("create plugin principal: %v", err)
	}
	w.token = mustIssueToken(t, w.setup.svc, pluginPrincipal.ID)
	w.operatorToken = mustIssueToken(t, w.setup.svc, mustCreatePrincipal(t, w.setup.svc, "operator-1", identity.RoleAdmin).ID)

	w.setup.audioPub.result = cueActivationNodeResultPayload(true, "authorized")
	w.plugin = &handbackPlugin{w: w, state: fallbackhold.StateNormal, since: w.now, bootID: uuid.NewString(), probeOK: true}
	w.publishProgram("pkg-1", 24*time.Hour)
	w.startCoordinator()
	return w
}

// publishProgram stores a signed program that maps all three entries, as
// the fallback reconciler would, valid for validity from now.
func (w *handbackWorld) publishProgram(packageID string, validity time.Duration) {
	w.t.Helper()
	program := fallbackprogram.Program{
		SchemaVersion: fallbackprogram.SchemaVersion, PackageID: packageID, Revision: "rev-" + packageID,
		ExpiresAt: w.now.Add(validity), CompiledAt: w.now, FPPInstanceUUID: handbackInstanceUUID,
		ExecutorPublicKey: executorKeyFor(1), Show: handbackShow, Generation: 1,
		PlaylistRevisions: map[string]int64{"playlist-1": 1}, CatalogRevisions: map[string]string{handbackNode: "catalog-1"},
		Rules: fallbackprogram.FixedRules,
	}
	for i, cue := range handbackCues {
		program.Entries = append(program.Entries, fallbackprogram.EntryMapping{
			EntryKey: w.entryKeys[i], CueID: cue, CueRevision: 1,
			Targets: []fallbackprogram.NodeTarget{{NodeID: handbackNode, Address: "192.0.2.20:80",
				Audio: &fallbackprogram.AudioActivation{Asset: "asset-" + cue, Filename: cue + ".wav", AssetHashes: []string{}}}},
		})
	}
	canonical, err := program.CanonicalBytes()
	if err != nil {
		w.t.Fatalf("canonical program bytes: %v", err)
	}
	sig := coordsig.Signature(ed25519.Sign(w.coordinatorSK, canonical))
	raw, err := json.Marshal(fallbackprogram.SignedProgram{Program: program, Signature: sig})
	if err != nil {
		w.t.Fatalf("marshal signed program: %v", err)
	}
	if err := w.setup.st.PutFallbackProgram(w.t.Context(), store.FallbackProgramRecord{
		FPPInstanceUUID: handbackInstanceUUID, PackageID: program.PackageID, Revision: program.Revision,
		ShowID: handbackShow, Generation: 1, ProgramJSON: string(raw), SignatureB64: base64.StdEncoding.EncodeToString(sig),
		ExpiresAt: program.ExpiresAt, CompiledAt: program.CompiledAt,
	}); err != nil {
		w.t.Fatalf("put fallback program: %v", err)
	}
}

// startCoordinator starts a coordinator process on the surviving store. A
// second call is a restart: nothing held in memory carries over.
func (w *handbackWorld) startCoordinator() {
	w.holds = fallbackhold.NewService(w.setup.st, w.now, testLogger())
	deps := w.setup.deps()
	deps.FallbackPrograms = w.setup.st
	deps.FallbackHolds = w.holds
	w.api = New(deps, Options{Clock: w.clock, Logger: testLogger()})
	w.loop = &handlers{deps: deps.withDefaults(), clock: w.clock, logger: testLogger()}
	w.held = &cueHeldTracker{}
	w.up = true
}

func (w *handbackWorld) stopCoordinator() { w.up = false }

// editCue saves a new revision of cue, as an operator editing the show
// does. The entry-1 observation the coordinator still holds then resolves
// to a start it has not sent yet, which is what a hold must keep back.
func (w *handbackWorld) editCue(cue string) {
	w.t.Helper()
	ctx := w.t.Context()
	revisions, err := w.setup.st.ListConfigRevisions(ctx, config.ShowCueConfigKind, cue)
	if err != nil {
		w.t.Fatalf("list revisions of %s: %v", cue, err)
	}
	next := int64(len(revisions) + 1)
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: handbackShow, Name: fmt.Sprintf("%s take %d", cue, next),
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cue}},
	})
	if err != nil {
		w.t.Fatalf("encode %s: %v", cue, err)
	}
	if _, err := w.setup.st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: cue, Revision: next, PayloadJSON: payload, Source: "api",
	}); err != nil {
		w.t.Fatalf("create revision %d of %s: %v", next, cue, err)
	}
	if _, err := w.setup.st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, cue, next); err != nil {
		w.t.Fatalf("activate revision %d of %s: %v", next, cue, err)
	}
}

// tick runs one pass of the Cue loop when the coordinator is up, and
// records the Cues it started. It fails the test when the coordinator sends
// the node anything at all while the plugin is the one running the show.
func (w *handbackWorld) tick() {
	w.t.Helper()
	if !w.up {
		return
	}
	w.loop.cueActivationTick(context.Background(), w.now, nil, w.held)
	w.loop.cueActivationFailToBlackWG.Wait()

	w.setup.audioPub.mu.Lock()
	sent := append([]dispatchedAudioCommand(nil), w.setup.audioPub.dispatched[w.seenSends:]...)
	w.seenSends = len(w.setup.audioPub.dispatched)
	w.setup.audioPub.mu.Unlock()
	for _, cmd := range sent {
		if w.plugin.state != fallbackhold.StateNormal {
			w.t.Fatalf("two owners: the coordinator sent %s to %s while the plugin was in %s", cmd.Action, cmd.NodeID, w.plugin.state)
		}
		if cmd.Action == "cue.activate" {
			w.starts = append(w.starts, fmt.Sprintf("%v by coordinator", cmd.Params["cueId"]))
		}
	}
}

// settle lets time pass with the plugin's and the coordinator's periodic
// work running, as it would between two playlist entries.
func (w *handbackWorld) settle(d time.Duration) {
	w.t.Helper()
	for elapsed := time.Duration(0); elapsed < d; elapsed += 5 * time.Second {
		w.advance(5 * time.Second)
		w.reportInventory()
		w.plugin.periodic()
		w.tick()
	}
}

func (w *handbackWorld) wantStarts(want ...string) {
	w.t.Helper()
	if !reflect.DeepEqual(w.starts, want) {
		w.t.Fatalf("Cue starts on the node:\n got  %q\n want %q", w.starts, want)
	}
}

func (w *handbackWorld) verdict() fallbackhold.Verdict {
	w.t.Helper()
	v, err := w.holds.Evaluate(context.Background(), handbackInstanceUUID, w.now)
	if err != nil {
		w.t.Fatalf("evaluate hold: %v", err)
	}
	return v
}

// request sends one request from the plugin to the coordinator. ok is
// false when no coordinator answered.
func (w *handbackWorld) request(method, path, token string, body any) (status int, raw []byte, ok bool) {
	w.t.Helper()
	if !w.up || !w.reachable {
		return 0, nil, false
	}
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			w.t.Fatalf("encode request body: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, raw := doRawRequest(w.t, w.api.Handler, req)
	return resp.StatusCode, raw, true
}

// handbackPlugin is the plugin stand-in. It keeps no knowledge of the
// coordinator beyond what the section 5 routes answer.
type handbackPlugin struct {
	w *handbackWorld

	bootID    string
	reportSeq int64
	obsSeq    int64

	state string
	since time.Time
	// probeOK is the last health probe's result. lossConfirmed stands in
	// for the plugin's own detector, which is outside this contract.
	probeOK       bool
	lossConfirmed bool

	installed       *fallbackprogram.Program
	enteredPlaylist string
	pass            int
	// reports counts state reports the coordinator answered with 200.
	reports int
}

func (p *handbackPlugin) setState(state string) {
	p.state, p.since = state, p.w.now
	p.report()
}

// report sends section 5.15's body with the state at this moment.
func (p *handbackPlugin) report() {
	p.w.t.Helper()
	if !p.probeOK {
		return
	}
	p.reportSeq++
	body := v1.FallbackStateReportRequest{
		SchemaVersion: 1, BootID: p.bootID, Sequence: p.reportSeq, State: p.state, Since: p.since.Format(time.RFC3339),
	}
	if p.state != fallbackhold.StateNormal {
		body.PlaylistName, body.PackageID = p.enteredPlaylist, p.installed.PackageID
		body.PackageRevision, body.CutoffAt = p.installed.Revision, p.installed.ExpiresAt.Format(time.RFC3339)
	}
	status, raw, ok := p.w.request(http.MethodPut, "/api/v1/fallback-programs/"+handbackInstanceUUID+"/fallback-state", p.w.token, body)
	if !ok {
		return
	}
	if status != http.StatusOK {
		p.w.t.Fatalf("state report %s: status %d, body %s", p.state, status, raw)
	}
	p.reports++
}

// probe is one health probe. The first request after a recovered probe is
// the state report (section 5.15 rule 4).
func (p *handbackPlugin) probe() {
	p.w.t.Helper()
	ok := p.w.up && p.w.reachable
	recovered := ok && !p.probeOK
	p.probeOK = ok
	if !ok {
		p.lossConfirmed = true
		return
	}
	p.lossConfirmed = false
	if recovered {
		p.report()
	}
}

// periodic is what the plugin does on its own timers: probe, check the
// cutoff, report every 10 seconds, and refetch while normal.
func (p *handbackPlugin) periodic() {
	p.w.t.Helper()
	p.probe()
	if p.state == fallbackhold.StateFallback && !p.w.now.Before(p.installed.ExpiresAt) {
		p.setState(fallbackhold.StateResting)
		return
	}
	p.report()
	if p.state == fallbackhold.StateNormal && p.probeOK {
		p.fetchProgram()
	}
}

// fetchProgram is section 5.12: fetch, and when the copy differs, verify,
// install and acknowledge it.
func (p *handbackPlugin) fetchProgram() {
	p.w.t.Helper()
	status, raw, ok := p.w.request(http.MethodGet, "/api/v1/fallback-programs/"+handbackInstanceUUID, p.w.token, nil)
	if !ok {
		return
	}
	if status != http.StatusOK {
		p.w.t.Fatalf("fetch program: status %d, body %s", status, raw)
	}
	var resp struct {
		Published       bool            `json:"published"`
		Program         json.RawMessage `json:"program"`
		SignatureBase64 string          `json:"signatureBase64"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || !resp.Published {
		p.w.t.Fatalf("fetch program: published %v err %v, body %s", resp.Published, err, raw)
	}
	sig, err := base64.StdEncoding.DecodeString(resp.SignatureBase64)
	if err != nil {
		p.w.t.Fatalf("decode program signature: %v", err)
	}
	canonical, _, err := fppidentity.HashCanonical(resp.Program)
	if err != nil {
		p.w.t.Fatalf("canonicalize fetched program: %v", err)
	}
	if !ed25519.Verify(p.w.coordinatorSK.Public().(ed25519.PublicKey), canonical, sig) {
		p.w.t.Fatal("the fetched program does not verify against the coordinator key")
	}
	var program fallbackprogram.Program
	if err := json.Unmarshal(resp.Program, &program); err != nil {
		p.w.t.Fatalf("decode fetched program: %v", err)
	}
	if p.installed != nil && p.installed.PackageID == program.PackageID && p.installed.Revision == program.Revision &&
		p.installed.ExpiresAt.Equal(program.ExpiresAt) {
		return
	}
	p.installed = &program
	status, raw, _ = p.w.request(http.MethodPost, "/api/v1/fallback-programs/"+handbackInstanceUUID+"/acknowledge", p.w.token, map[string]any{
		"packageId": program.PackageID, "revision": program.Revision, "verificationResult": "verified",
		"installedAt": p.w.now.Format(time.RFC3339),
	})
	if status != http.StatusOK {
		p.w.t.Fatalf("acknowledge program: status %d, body %s", status, raw)
	}
}

// usable is section 5.13's usable program.
func (p *handbackPlugin) usable() bool {
	return p.installed != nil && p.w.now.Before(p.installed.ExpiresAt) &&
		p.installed.ExecutorPublicKey == executorKeyFor(1) && p.installed.Rules == fallbackprogram.FixedRules
}

func (p *handbackPlugin) mapped(entryKey string) (string, bool) {
	if p.installed == nil {
		return "", false
	}
	for _, e := range p.installed.Entries {
		if e.EntryKey == entryKey {
			return e.CueID, true
		}
	}
	return "", false
}

// startPlaylist is FPP beginning a new run of the playlist.
func (p *handbackPlugin) startPlaylist() { p.pass++ }

// entry is an entry boundary: FPP's playing callback for a new occurrence
// of the entry at position.
func (p *handbackPlugin) entry(position int) {
	p.w.t.Helper()
	p.probe()
	entryKey := p.w.entryKeys[position]
	switch p.state {
	case fallbackhold.StateNormal:
		if cue, ok := p.mapped(entryKey); p.lossConfirmed && p.usable() && ok {
			p.enteredPlaylist = handbackPlaylistName
			p.setState(fallbackhold.StateFallback)
			p.w.starts = append(p.w.starts, cue+" by plugin")
			return
		}
		p.observe(position)
	case fallbackhold.StateFallback:
		if !p.w.now.Before(p.installed.ExpiresAt) {
			p.setState(fallbackhold.StateResting)
			return
		}
		if cue, ok := p.mapped(entryKey); ok {
			p.w.starts = append(p.w.starts, cue+" by plugin")
		}
	}
}

// observe posts section 1's observation for the playing entry. A plugin
// that cannot reach the coordinator skips it and never sends it later.
func (p *handbackPlugin) observe(position int) {
	p.w.t.Helper()
	p.obsSeq++
	if p.lossConfirmed {
		return
	}
	loop := p.pass
	status, raw, ok := p.w.request(http.MethodPost, "/api/v1/integrations/fpp/playlist-entry-observations", p.w.token, v1.FPPPlaylistEntryObservationRequest{
		SchemaVersion: 1, InstanceUUID: handbackInstanceUUID, PlaylistName: handbackPlaylistName,
		PlaylistHash: p.w.playlist.FPP.PlaylistHash, Section: "mainPlaylist", Position: &position,
		EntryKey: p.w.entryKeys[position], Action: "playing", Sequence: p.obsSeq,
		ObservedAtMillis: p.w.now.UnixMilli(), PlaylistLoop: &loop,
	})
	if ok && status != http.StatusOK {
		p.w.t.Fatalf("post observation: status %d, body %s", status, raw)
	}
}

// stopPlaylist is the boundary: FPP stops playing the playlist. The four
// hand-back steps of section 5.13 follow, in order.
func (p *handbackPlugin) stopPlaylist() {
	p.w.t.Helper()
	p.probe()
	if p.state == fallbackhold.StateNormal {
		return
	}
	p.enteredPlaylist = ""
	p.setState(fallbackhold.StateNormal)
	if p.probeOK {
		p.fetchProgram()
	}
}

// restart is a plugin restart that finds its persisted state: the same
// state and playlist, a new boot id, and a sequence that starts again.
func (p *handbackPlugin) restart() {
	p.bootID, p.reportSeq = uuid.NewString(), 0
	p.report()
}

// playNormalEntry plays one entry with the coordinator in charge.
func (w *handbackWorld) playEntry(position int) {
	w.t.Helper()
	w.plugin.entry(position)
	w.tick()
	w.settle(30 * time.Second)
}

func TestHandbackOutageRunsTheRestOfThePlaylistFromThePluginOnly(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator")

	w.stopCoordinator()
	w.settle(20 * time.Second)
	w.playEntry(1)
	w.playEntry(2)
	w.plugin.stopPlaylist()
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin")
	if w.plugin.state != fallbackhold.StateNormal {
		t.Fatalf("after the playlist stopped the plugin is in %s, want normal", w.plugin.state)
	}
}

func TestHandbackCoordinatorRestartDuringFallbackDoesNotTakeTheShowBack(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)

	w.stopCoordinator()
	w.settle(20 * time.Second)
	w.playEntry(1)

	// The coordinator comes back. Before the plugin has said anything it
	// holds the stored entry-1 observation and must not act on it.
	w.startCoordinator()
	w.editCue("cue-1")
	w.tick()
	if v := w.verdict(); !v.Held || v.Reason != fallbackhold.ReasonCoordinatorStarting {
		t.Fatalf("right after the restart: held %v reason %q, want held while starting", v.Held, v.Reason)
	}
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin")

	w.settle(30 * time.Second)
	if v := w.verdict(); !v.Held || v.Reason != fallbackhold.ReasonExecutor {
		t.Fatalf("after the plugin reported: held %v reason %q, want held while the plugin runs the show", v.Held, v.Reason)
	}
	w.playEntry(2)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin")

	// The boundary: the plugin hands back, and the next run is the
	// coordinator's again.
	w.plugin.stopPlaylist()
	w.settle(10 * time.Second)
	if v := w.verdict(); v.Held {
		t.Fatalf("after the hand-back the player is still held as %q", v.Reason)
	}
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin")

	w.plugin.startPlaylist()
	w.playEntry(0)
	w.playEntry(1)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin", "cue-1 by coordinator", "cue-2 by coordinator")
}

// The coordinator never stops, the plugin only loses its way to it. When
// the way comes back mid-playlist the coordinator still held Cue 1 as the
// last thing it started, and must neither stop it nor start anything.
func TestHandbackRecoveryBeforeTheBoundaryLeavesThePluginInCharge(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)

	w.reachable = false
	w.settle(20 * time.Second)
	w.playEntry(1)

	w.reachable = true
	w.settle(20 * time.Second)
	if v := w.verdict(); !v.Held || v.Reason != fallbackhold.ReasonExecutor {
		t.Fatalf("after recovery mid-playlist: held %v reason %q, want held while the plugin runs the show", v.Held, v.Reason)
	}
	if w.plugin.state != fallbackhold.StateFallback {
		t.Fatalf("a recovered probe moved the plugin to %s, want it to stay in fallback", w.plugin.state)
	}
	w.editCue("cue-1")
	w.tick()
	w.playEntry(2)
	w.settle(5 * time.Minute)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin")

	w.plugin.stopPlaylist()
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin", "cue-1 by coordinator")
}

func TestHandbackAtTheCutoffNothingNewStartsUntilTheBoundary(t *testing.T) {
	w := newHandbackWorld(t)
	w.publishProgram("pkg-short", 90*time.Second)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)

	w.stopCoordinator()
	w.settle(10 * time.Second)
	w.playEntry(1)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin")

	// The program's end passes while the playlist is still running.
	w.settle(time.Minute)
	if w.plugin.state != fallbackhold.StateResting {
		t.Fatalf("past the program's end the plugin is in %s, want resting", w.plugin.state)
	}
	w.startCoordinator()
	w.settle(20 * time.Second)
	if v := w.verdict(); !v.Held || v.Record.State != fallbackhold.StateResting {
		t.Fatalf("with the plugin resting: held %v state %q, want held and resting", v.Held, v.Record.State)
	}
	w.playEntry(2)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin")

	// A new program is published meanwhile. The plugin does not take it,
	// and does not leave resting, before the boundary.
	w.publishProgram("pkg-next", 24*time.Hour)
	w.settle(time.Minute)
	if w.plugin.state != fallbackhold.StateResting || w.plugin.installed.PackageID != "pkg-short" {
		t.Fatalf("before the boundary the plugin is in %s with %s, want resting with the copy it entered with",
			w.plugin.state, w.plugin.installed.PackageID)
	}

	w.plugin.stopPlaylist()
	w.settle(10 * time.Second)
	if v := w.verdict(); v.Held {
		t.Fatalf("after the hand-back the player is still held as %q", v.Reason)
	}
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-1 by coordinator")
}

func TestHandbackASecondOutageIsCoveredAgainAndNothingStartsTwice(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)

	w.stopCoordinator()
	w.settle(20 * time.Second)
	w.playEntry(1)
	w.startCoordinator()
	w.settle(20 * time.Second)
	w.playEntry(2)
	w.plugin.stopPlaylist()
	w.settle(10 * time.Second)

	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin", "cue-1 by coordinator")

	// The second outage, in the second run. The coordinator stays down
	// through the boundary this time.
	w.stopCoordinator()
	w.settle(20 * time.Second)
	w.playEntry(1)
	w.playEntry(2)
	w.plugin.stopPlaylist()
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin", "cue-1 by coordinator",
		"cue-2 by plugin", "cue-3 by plugin", "cue-1 by plugin")

	w.startCoordinator()
	w.settle(30 * time.Second)
	w.playEntry(1)
	w.plugin.stopPlaylist()
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin", "cue-1 by coordinator",
		"cue-2 by plugin", "cue-3 by plugin", "cue-1 by plugin", "cue-2 by plugin", "cue-1 by coordinator")
}

// A plugin that restarts mid-playlist resumes fallback from its own file
// and says so under a new boot id. The coordinator keeps holding.
func TestHandbackPluginRestartInFallbackKeepsTheHold(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.reachable = false
	w.settle(20 * time.Second)
	w.playEntry(1)
	w.reachable = true
	w.settle(10 * time.Second)

	w.plugin.restart()
	w.tick()
	if v := w.verdict(); !v.Held || v.Record.BootID != w.plugin.bootID {
		t.Fatalf("after the plugin restarted: held %v under boot %q, want held under the new boot id", v.Held, v.Record.BootID)
	}
	w.playEntry(2)
	w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-3 by plugin")
}

// ADR-048 decision 4: after the boundary the coordinator resumes only once
// the player has acknowledged its current program.
func TestHandbackWaitsForTheProgramAcknowledgementAndAnOperatorCanClearIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(w *handbackWorld)
	}{
		{"the plugin acknowledges", func(w *handbackWorld) { w.plugin.fetchProgram() }},
		{"an operator clears the stored state", func(w *handbackWorld) {
			status, raw, _ := w.request(http.MethodDelete, "/api/v1/fallback-programs/"+handbackInstanceUUID+"/fallback-state", w.operatorToken, nil)
			if status != http.StatusNoContent {
				w.t.Fatalf("clear fallback state: status %d, body %s", status, raw)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHandbackWorld(t)
			w.settle(10 * time.Second)
			w.plugin.startPlaylist()
			w.playEntry(0)
			w.reachable = false
			w.settle(20 * time.Second)
			w.playEntry(1)
			w.reachable = true
			w.settle(10 * time.Second)

			// The show changed while the plugin was running it, so the
			// copy the plugin acknowledged is no longer the current one.
			w.publishProgram("pkg-2", 24*time.Hour)

			// The plugin hands back and reports, and its fetch does not
			// get through yet.
			w.plugin.probe()
			w.plugin.enteredPlaylist = ""
			w.plugin.setState(fallbackhold.StateNormal)
			w.advance(20 * time.Second)
			w.tick()
			v := w.verdict()
			if !v.Held || v.Reason != fallbackhold.ReasonAwaitingAcknowledgement {
				t.Fatalf("after a hand-back with no current acknowledgement: held %v reason %q, want held waiting", v.Held, v.Reason)
			}
			state := w.listedPlayerState()
			if state == nil || !state.Held || state.HoldReason != string(fallbackhold.ReasonAwaitingAcknowledgement) ||
				state.AcknowledgementWaitSeconds == nil || *state.AcknowledgementWaitSeconds != 20 || state.Message == "" {
				t.Fatalf("the listing shows %+v, want a held player that has waited 20 seconds with a message", state)
			}

			// The next run starts before the acknowledgement arrives. Its
			// first Cue is started once the hold ends, and only once.
			w.plugin.startPlaylist()
			w.plugin.entry(0)
			w.tick()
			w.wantStarts("cue-1 by coordinator", "cue-2 by plugin")

			tc.release(w)
			w.tick()
			w.tick()
			w.wantStarts("cue-1 by coordinator", "cue-2 by plugin", "cue-1 by coordinator")
		})
	}
}

func (w *handbackWorld) listedPlayerState() *v1.FallbackPlayerState {
	w.t.Helper()
	status, raw, _ := w.request(http.MethodGet, "/api/v1/fallback-programs", w.operatorToken, nil)
	if status != http.StatusOK {
		w.t.Fatalf("list fallback programs: status %d, body %s", status, raw)
	}
	var resp v1.FallbackProgramListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		w.t.Fatalf("decode fallback program list: %v", err)
	}
	for _, p := range resp.Programs {
		if p.FPPInstanceUUID == handbackInstanceUUID {
			return p.PlayerState
		}
	}
	return nil
}

// While a plugin runs the show an operator can read that it does.
func TestHandbackTheListingSaysThePlayerIsRunningFromItsFallbackProgram(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	if state := w.listedPlayerState(); state == nil || state.State != fallbackhold.StateNormal || state.Held || !state.PluginReporting || state.Message != "" {
		t.Fatalf("with a normal, reporting plugin the listing shows %+v", state)
	}
	w.plugin.startPlaylist()
	w.playEntry(0)
	w.reachable = false
	w.settle(20 * time.Second)
	w.playEntry(1)
	w.reachable = true
	w.settle(10 * time.Second)

	state := w.listedPlayerState()
	if state == nil || state.State != fallbackhold.StateFallback || !state.Held || state.PlaylistName != handbackPlaylistName ||
		state.CutoffAt == "" || state.HoldReason != string(fallbackhold.ReasonExecutor) {
		t.Fatalf("with the plugin in fallback the listing shows %+v", state)
	}
	if !strings.HasPrefix(state.Message, "This player is running the show from its fallback program.") {
		t.Fatalf("the operator message is %q", state.Message)
	}
	if got := countAuditActionsIn(t, w.setup.svc, auditActionFallbackPlayerStateReport); got != 2 {
		t.Fatalf("%d audit entries for state reports, want 2: one for the first report and one for the change to fallback", got)
	}
}

// A reporting plugin dies while FPP keeps playing and the coordinator is
// healthy. The player is not held, and the listing says the plugin is quiet.
func TestHandbackADeadPluginDoesNotHoldItsPlayer(t *testing.T) {
	w := newHandbackWorld(t)
	w.settle(10 * time.Second)
	w.plugin.startPlaylist()
	w.playEntry(0)

	// The plugin is gone: no probes, no reports, no observations.
	for i := 0; i < 30; i++ {
		w.advance(10 * time.Second)
		w.reportInventory()
		w.tick()
	}
	if v := w.verdict(); v.Held || v.PluginReporting {
		t.Fatalf("five minutes after the plugin died: held %v reporting %v, want neither", v.Held, v.PluginReporting)
	}
	state := w.listedPlayerState()
	if state == nil || state.Held || state.PluginReporting || state.Message == "" {
		t.Fatalf("the listing shows %+v, want a player that is not held, with a plugin that is not reporting and a message", state)
	}
	w.wantStarts("cue-1 by coordinator")
}

func countAuditActionsIn(t *testing.T, svc identity.Service, action string) int {
	t.Helper()
	entries, err := svc.ListAudit(t.Context(), 0, 500)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == action {
			n++
		}
	}
	return n
}
