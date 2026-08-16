//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Track E's own acceptance seam (TRACK-E-SESSION-SPEC.md
// section 7): every claim E1 through E6 make about authoring a show, its
// surfaces, and its assets is proven here against a REAL
// showmesh-coordinator subprocess and REAL showmesh-agent subprocesses over
// the real throwaway Mosquitto this package's harness starts, driven
// through the real showmeshctl binary exactly as an operator with no UI
// container running would drive it. CLAUDE.md's own lesson is the reason
// this file exists at all: Step 6 shipped three features that compiled,
// passed unit tests, and were unreachable by anything, and every one was
// found by someone trying to USE the system rather than by a test.
//
// Every action an acceptance criterion asks for is driven through the CLI
// subprocess (mustCtl/runCtl below). Two exceptions are deliberate, not
// convenience: (1) apiNodeManifestState/apiNodeManifest poll the coordinator
// directly over HTTP purely as waitFor's condition function, so a bounded
// wait does not spawn a fresh showmeshctl process every 100-200ms — every
// test's actual ACCEPTANCE assertion still goes through the CLI at least
// once; (2) acceptance criterion 9 (absent/null/zero) is undrivable through
// the CLI by construction (flag.FlagSet has no way to send a JSON `null` or
// omit a key the program always sends), so it issues the raw PUT
// TRACK-E-SESSION-SPEC.md section 1 rule 1 itself calls for.

// ctlServerToken returns the --server/--token flag pair every CLI
// invocation in this file needs, built from a running *testCoordinator.
// Must be inserted BEFORE any positional argument — Go's flag package stops
// parsing flags at the first non-flag token, so a trailing --server after a
// positional id would never be recognized as a flag at all.
func ctlServerToken(coord *testCoordinator, token string) []string {
	args := []string{"--server", "http://" + coord.httpAddr}
	if token != "" {
		args = append(args, "--token", token)
	}
	return args
}

// runCtl execs the real showmeshctl binary: flagArgs is the subcommand path
// plus its own flags (e.g. []string{"show", "set", "--name", "X"}),
// positional is appended LAST (after --server/--token), matching every
// subcommand's own "[flags] <id>" usage shape.
func runCtl(t *testing.T, coord *testCoordinator, token string, flagArgs []string, positional ...string) (code int, stdout, stderr string) {
	t.Helper()
	full := append(append([]string{}, flagArgs...), ctlServerToken(coord, token)...)
	full = append(full, positional...)
	return runShowmeshctl(t, 20*time.Second, full...)
}

// mustCtl is runCtl for the common case: the call is expected to succeed,
// and a non-zero exit is a test failure (dumping stdout/stderr) rather than
// something the caller inspects.
func mustCtl(t *testing.T, coord *testCoordinator, token string, flagArgs []string, positional ...string) string {
	t.Helper()
	code, stdout, stderr := runCtl(t, coord, token, flagArgs, positional...)
	if code != 0 {
		t.Fatalf("showmeshctl %v %v: exit %d\nstdout=%s\nstderr=%s", flagArgs, positional, code, stdout, stderr)
	}
	return stdout
}

// startAssetCoordinator provisions an admin token (before any coordinator
// subprocess touches dataDir — createAdminAndIssueToken's own requirement),
// creates a known coordinator-side asset directory, and starts a real
// showmesh-coordinator on a pre-allocated port. When enableSync is true,
// SHOWMESH_ASSET_CONTENT_BASE_URL is set to this coordinator's OWN address
// (derived from the same findFreePort/httpAddr this call allocates, per the
// session spec's "derive it... rather than hardcoding one"), so the sync
// service can actually dispatch and an agent can actually fetch. Left
// false, no node can ever become "ready" via a real transfer — used by
// tests that only need write/read surfaces, never a transfer.
func startAssetCoordinator(t *testing.T, dataDir string, enableSync bool) (coord *testCoordinator, token, assetDir string) {
	t.Helper()
	token = createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")

	assetDir = filepath.Join(dataDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir coordinator asset dir %s: %v", assetDir, err)
	}

	httpAddr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	cfg := coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: token, httpAddr: httpAddr, assetDir: assetDir,
	}
	if enableSync {
		cfg.assetContentBaseURL = "http://" + httpAddr
	}
	coord = startCoordinatorWithConfig(t, cfg)
	return coord, token, assetDir
}

// declareAndStartAgent declares nodeID (config:write) and starts a real
// agent subprocess for it, with a freshly created, EMPTY local asset
// directory. The directory is created (not merely named) before the agent
// starts: an agent asked to enumerate a directory that does not exist yet
// reports complete=false ("asset directory does not exist"), which reads as
// "unknown", never the "complete, holds nothing" report several acceptance
// criteria below need to reach not_ready deterministically.
func declareAndStartAgent(t *testing.T, coord *testCoordinator, token, nodeID, label string) (agentDir string) {
	t.Helper()
	mustCtl(t, coord, token, []string{"declare", "--label", label}, nodeID)
	agentDir = filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent asset dir %s: %v", agentDir, err)
	}
	startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})
	return agentDir
}

// writeTempFile writes content to a fresh temp file named name and returns
// its path.
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write temp file %s: %v", path, err)
	}
	return path
}

// uploadAsset drives "showmeshctl assets upload" for a node-targeted asset
// and returns the CLI's own JSON response, decoded.
func uploadNodeAsset(t *testing.T, coord *testCoordinator, token, showID, sequence, mediaType, nodeID, filePath string) ctlAsset {
	t.Helper()
	out := mustCtl(t, coord, token, []string{
		"assets", "upload", "--show", showID, "--sequence", sequence, "--media-type", mediaType,
		"--target-kind", "node", "--target", nodeID, "--file", filePath, "--output", "json",
	})
	var resp ctlAssetResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode assets upload json: %v\noutput:\n%s", err, out)
	}
	return resp.Asset
}

// uploadShowAsset is uploadNodeAsset for a show-wide asset (targetKind
// "show"), which every declared node's expected set includes regardless of
// which node it lands on.
func uploadShowAsset(t *testing.T, coord *testCoordinator, token, showID, sequence, mediaType, filePath string) ctlAsset {
	t.Helper()
	out := mustCtl(t, coord, token, []string{
		"assets", "upload", "--show", showID, "--sequence", sequence, "--media-type", mediaType,
		"--target-kind", "show", "--file", filePath, "--output", "json",
	})
	var resp ctlAssetResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode assets upload json: %v\noutput:\n%s", err, out)
	}
	return resp.Asset
}

// --- CLI JSON wire shapes (this program's own types are unexported in
// package main and cannot be imported; these mirror their json tags,
// matching cmd_show.go/cmd_surface.go/cmd_assets.go field for field). ---

type ctlShowPayload struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
}
type ctlShowResp struct {
	Payload ctlShowPayload `json:"payload"`
}

type ctlChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}
type ctlGeometry struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pixelFormat"`
}
type ctlNDI struct {
	SourceName string `json:"sourceName"`
}
type ctlOutput struct {
	Transport string  `json:"transport"`
	NDI       *ctlNDI `json:"ndi,omitempty"`
}
type ctlSurfacePayload struct {
	Show         string          `json:"show"`
	Name         string          `json:"name"`
	Node         string          `json:"node"`
	ChannelRange ctlChannelRange `json:"channelRange"`
	Geometry     ctlGeometry     `json:"geometry"`
	FrameRate    int             `json:"frameRate"`
	Output       ctlOutput       `json:"output"`
}
type ctlSurfaceResp struct {
	Payload ctlSurfacePayload `json:"payload"`
}

type ctlShowActivePayload struct {
	Show string `json:"show"`
}
type ctlShowActiveResp struct {
	Payload ctlShowActivePayload `json:"payload"`
}

type ctlAsset struct {
	ID              string `json:"id"`
	Show            string `json:"show"`
	Sequence        string `json:"sequence"`
	TargetKind      string `json:"targetKind"`
	Target          string `json:"target"`
	MediaType       string `json:"mediaType"`
	ContentHash     string `json:"contentHash"`
	RuntimeFilename string `json:"runtimeFilename"`
	SizeBytes       int64  `json:"sizeBytes"`
}
type ctlAssetResp struct {
	Asset ctlAsset `json:"asset"`
}
type ctlAssetsListResp struct {
	Assets []ctlAsset `json:"assets"`
}

type ctlMissingAsset struct {
	AssetID     string `json:"assetId"`
	Sequence    string `json:"sequence"`
	Filename    string `json:"filename"`
	ContentHash string `json:"contentHash"`
	SizeBytes   int64  `json:"sizeBytes"`
}
type ctlGap struct {
	Sequence string   `json:"sequence"`
	Surfaces []string `json:"surfaces"`
}
type ctlExtraAsset struct {
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
}
type ctlNodeManifest struct {
	Node    string            `json:"node"`
	State   string            `json:"state"`
	Reason  *string           `json:"reason"`
	Missing []ctlMissingAsset `json:"missing"`
	Gaps    []ctlGap          `json:"gaps"`
	Extra   []ctlExtraAsset   `json:"extra"`
}
type ctlManifestResp struct {
	Nodes []ctlNodeManifest `json:"nodes"`
}

// getSurface drives "showmeshctl surface get --output json" and decodes
// the payload.
func getSurface(t *testing.T, coord *testCoordinator, token, surfaceID string) ctlSurfacePayload {
	t.Helper()
	out := mustCtl(t, coord, token, []string{"surface", "get", "--output", "json"}, surfaceID)
	var resp ctlSurfaceResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode surface get json: %v\noutput:\n%s", err, out)
	}
	return resp.Payload
}

// nodeManifest drives "showmeshctl assets manifest --node <id> --output
// json" — the real CLI path acceptance criteria 3, 6, 7, 8, and 11 assert
// against.
func nodeManifest(t *testing.T, coord *testCoordinator, token, nodeID string) ctlNodeManifest {
	t.Helper()
	out := mustCtl(t, coord, token, []string{"assets", "manifest", "--node", nodeID, "--output", "json"})
	var resp ctlManifestResp
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode assets manifest json: %v\noutput:\n%s", err, out)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("assets manifest --node %s returned %d node entries, want 1", nodeID, len(resp.Nodes))
	}
	return resp.Nodes[0]
}

func nodeManifestState(t *testing.T, coord *testCoordinator, token, nodeID string) string {
	t.Helper()
	return nodeManifest(t, coord, token, nodeID).State
}

// apiNodeManifestState is nodeManifestState's fast, non-CLI twin, used ONLY
// as a waitFor condition function: polling the CLI as a subprocess every
// 100-200ms would spend most of a bounded wait on process startup rather
// than the coordinator's own state. Every test's actual acceptance
// assertion still calls nodeManifest/mustCtl (the real CLI path) at least
// once, never only this.
func apiNodeManifestState(t *testing.T, coord *testCoordinator, nodeID string) string {
	t.Helper()
	status, body := coord.getRaw(t, "/api/v1/nodes/"+url.PathEscape(nodeID)+"/assets")
	if status != http.StatusOK {
		t.Fatalf("GET node asset manifest for %s: status %d, body: %s", nodeID, status, body)
	}
	var resp v1.NodeAssetManifestResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode node asset manifest: %v; body: %s", err, body)
	}
	return resp.Manifest.State
}

// assetBlobPath reconstructs a content-addressed blob's on-disk path from
// its "sha256:<hex>" content hash, matching VolumeBackend's own
// "<root>/<hex[0:2]>/<hex>" layout exactly (assetstore/volume.go).
func assetBlobPath(assetDir, contentHash string) string {
	hash := strings.TrimPrefix(contentHash, "sha256:")
	return filepath.Join(assetDir, hash[:2], hash)
}

// --- 1. Authoring with no browser (acceptance criterion 1) ---

func TestShowSurfaceAuthoringNoBrowser(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	nodeID := "node-" + uniqueSuffix()
	showID := "show-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	mustCtl(t, coord, token, []string{"declare", "--label", "Garage renderer"}, nodeID)
	mustCtl(t, coord, token, []string{"show", "set", "--name", "Halloween 2026", "--notes", "the reference show"}, showID)
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Garage Door", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "3600", "--width", "40", "--height", "30",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "ShowMesh Garage",
	}, surfaceID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)

	// Read every object back and assert the values round-tripped.
	showOut := mustCtl(t, coord, token, []string{"show", "get", "--output", "json"}, showID)
	var showResp ctlShowResp
	if err := json.Unmarshal([]byte(showOut), &showResp); err != nil {
		t.Fatalf("decode show get json: %v\noutput:\n%s", err, showOut)
	}
	if showResp.Payload.Name != "Halloween 2026" || showResp.Payload.Notes != "the reference show" {
		t.Fatalf("show round-trip mismatch: %+v", showResp.Payload)
	}

	surface := getSurface(t, coord, token, surfaceID)
	if surface.Show != showID || surface.Node != nodeID {
		t.Errorf("surface show/node mismatch: %+v", surface)
	}
	if surface.ChannelRange.StartChannel != 1 || surface.ChannelRange.ChannelCount != 3600 {
		t.Errorf("surface channelRange mismatch: %+v", surface.ChannelRange)
	}
	if surface.Geometry.Width != 40 || surface.Geometry.Height != 30 || surface.Geometry.PixelFormat != "rgb" {
		t.Errorf("surface geometry mismatch: %+v", surface.Geometry)
	}
	if surface.FrameRate != 40 {
		t.Errorf("surface frameRate = %d, want 40", surface.FrameRate)
	}
	if surface.Output.Transport != "ndi" || surface.Output.NDI == nil || surface.Output.NDI.SourceName != "ShowMesh Garage" {
		t.Errorf("surface output mismatch: %+v", surface.Output)
	}

	activeOut := mustCtl(t, coord, token, []string{"show", "active", "--output", "json"})
	var activeResp ctlShowActiveResp
	if err := json.Unmarshal([]byte(activeOut), &activeResp); err != nil {
		t.Fatalf("decode show active json: %v\noutput:\n%s", err, activeOut)
	}
	if activeResp.Payload.Show != showID {
		t.Fatalf("active show = %q, want %q", activeResp.Payload.Show, showID)
	}
}

// --- 2. Three files, one filename (acceptance criterion 2) ---

func TestAssetUploadSameFilenameDifferentNodesEachGetOwnBytes(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, true)

	showID := "show-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "Same Filename Show"}, showID)

	type nodeCase struct {
		nodeID   string
		content  []byte
		assetDir string
	}
	var cases []nodeCase
	for i := 0; i < 3; i++ {
		nodeID := fmt.Sprintf("node-%d-%s", i, uniqueSuffix())
		agentDir := declareAndStartAgent(t, coord, token, nodeID, fmt.Sprintf("node %d", i))
		content := []byte(fmt.Sprintf("distinct fseq payload #%d %s", i, uniqueSuffix()))

		filePath := writeTempFile(t, "Thriller.fseq", content)
		uploadNodeAsset(t, coord, token, showID, "opening", "fseq", nodeID, filePath)

		cases = append(cases, nodeCase{nodeID: nodeID, content: content, assetDir: agentDir})
	}

	mustCtl(t, coord, token, []string{"show", "activate"}, showID)

	for _, nc := range cases {
		nc := nc
		t.Run(nc.nodeID, func(t *testing.T) {
			waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
				return apiNodeManifestState(t, coord, nc.nodeID) == "ready"
			}, fmt.Sprintf("node %s manifest to become ready", nc.nodeID))

			if state := nodeManifestState(t, coord, token, nc.nodeID); state != "ready" {
				t.Fatalf("showmeshctl assets manifest --node %s reports %q, want ready", nc.nodeID, state)
			}

			got, err := os.ReadFile(filepath.Join(nc.assetDir, "Thriller.fseq"))
			if err != nil {
				t.Fatalf("read fetched Thriller.fseq for %s: %v", nc.nodeID, err)
			}
			if !bytes.Equal(got, nc.content) {
				t.Fatalf("node %s holds the wrong bytes for Thriller.fseq: got %q, want %q", nc.nodeID, got, nc.content)
			}
		})
	}

	listOut := mustCtl(t, coord, token, []string{"assets", "list", "--show", showID, "--output", "json"})
	var list ctlAssetsListResp
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("decode assets list json: %v\noutput:\n%s", err, listOut)
	}
	if len(list.Assets) != 3 {
		t.Fatalf("assets list --show %s returned %d assets, want 3", showID, len(list.Assets))
	}
	hashes := map[string]bool{}
	for _, a := range list.Assets {
		if a.RuntimeFilename != "Thriller.fseq" {
			t.Errorf("asset %s runtime filename = %q, want Thriller.fseq", a.ID, a.RuntimeFilename)
		}
		hashes[a.ContentHash] = true
	}
	if len(hashes) != 3 {
		t.Fatalf("expected 3 distinct content hashes among the 3 Thriller.fseq uploads, got %d", len(hashes))
	}
}

// --- 3. A node missing an asset says what is missing (acceptance criterion 3) ---

func TestAssetManifestReportsMissingBeforeShow(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	// Sync deliberately disabled: this asset must never be auto-fetched, so
	// the not_ready verdict this test asserts stays true no matter how long
	// the test takes to reach its assertion, rather than racing a tick.
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S3"}, showID)
	declareAndStartAgent(t, coord, token, nodeID, "n")

	filePath := writeTempFile(t, "Missing.fseq", []byte("expected but never fetched"))
	uploadNodeAsset(t, coord, token, showID, "opening", "fseq", nodeID, filePath)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)

	waitFor(t, 10*time.Second, 100*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "not_ready"
	}, "node manifest to report not_ready before any fetch happens")

	m := nodeManifest(t, coord, token, nodeID)
	if m.State != "not_ready" {
		t.Fatalf("State = %q, want not_ready", m.State)
	}
	if len(m.Missing) != 1 {
		t.Fatalf("Missing = %+v, want exactly 1 entry", m.Missing)
	}
	if m.Missing[0].Filename != "Missing.fseq" {
		t.Errorf("Missing[0].Filename = %q, want %q", m.Missing[0].Filename, "Missing.fseq")
	}
	if m.Reason == nil || *m.Reason == "" {
		t.Errorf("Reason is empty, want a reason naming the gap")
	}
}

// --- 4. A truncated asset is reported, never served; the agent discards a
// hash mismatch and reports it (acceptance criterion 4) ---

func TestAssetTruncatedNeverServedAndAgentDiscardsHashMismatch(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, assetDir := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S4"}, showID)
	mustCtl(t, coord, token, []string{"declare", "--label", "n"}, nodeID)

	t.Run("truncated blob is reported, never served", func(t *testing.T) {
		content := bytes.Repeat([]byte("legitimate fseq bytes. "), 64)
		filePath := writeTempFile(t, "Thriller.fseq", content)
		asset := uploadNodeAsset(t, coord, token, showID, "opening", "fseq", nodeID, filePath)

		blobPath := assetBlobPath(assetDir, asset.ContentHash)
		if err := os.Truncate(blobPath, int64(len(content)-5)); err != nil {
			t.Fatalf("truncate stored blob %s: %v", blobPath, err)
		}

		outPath := filepath.Join(t.TempDir(), "out.fseq")
		code, _, stderr := runCtl(t, coord, token, []string{"assets", "fetch", "--out", outPath}, asset.ID)
		if code == 0 {
			t.Fatalf("assets fetch of a truncated blob exited 0, want non-zero; stderr=%s", stderr)
		}
		if _, err := os.Stat(outPath); !os.IsNotExist(err) {
			t.Fatalf("assets fetch wrote %s despite the coordinator's stored blob being truncated", outPath)
		}

		// The raw content endpoint itself must also refuse rather than
		// serve the truncated bytes with a 200.
		status, _ := coord.getRaw(t, "/api/v1/assets/"+asset.ID+"/content")
		if status == http.StatusOK {
			t.Fatalf("GET .../content status = 200 for a truncated blob, want a failure status")
		}
	})

	t.Run("agent discards content whose hash does not match and reports it", func(t *testing.T) {
		badNode := "node-" + uniqueSuffix()
		agentDir := filepath.Join(t.TempDir(), "assets")
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatalf("mkdir agent asset dir: %v", err)
		}
		startAgent(t, agentConfig{nodeID: badNode, assetDir: agentDir})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not the bytes the coordinator promised"))
		}))
		defer srv.Close()

		cli, w := startCmdClient(t, badNode)
		awaitAgentReceivingCommands(t, cli, w, badNode)

		cmdID := "cmd-" + uniqueSuffix()
		wantHash := "sha256:" + strings.Repeat("0", 64) // deliberately does not match srv's bytes
		dispatchCmd(t, cli, badNode, assetFetchCmd(badNode, cmdID, "asset-mismatch", wantHash, "Mismatch.fseq", srv.URL, 10))

		result := waitForResult(t, w, cmdID, 10*time.Second)
		if result.Outcome != mqttproto.OutcomeFailed {
			t.Fatalf("Outcome = %q, want %q; reason=%q", result.Outcome, mqttproto.OutcomeFailed, result.Reason)
		}
		if !strings.Contains(result.Reason, "failed verification") {
			t.Errorf("Reason = %q, want it to name the hash verification failure", result.Reason)
		}

		if _, err := os.Stat(filepath.Join(agentDir, "Mismatch.fseq")); !os.IsNotExist(err) {
			t.Fatalf("agent renamed unverified content into place under its runtime filename despite a hash mismatch")
		}
		entries, err := os.ReadDir(filepath.Join(agentDir, ".staging"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read agent staging dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf(".staging still holds %d entries after a discarded hash mismatch, want 0", len(entries))
		}
	})
}

// assetFetchCmd builds a well-formed "asset.fetch" mqttproto.CmdPayload
// targeting nodeID, mirroring agent_command_test.go's echoCmd one action
// over.
func assetFetchCmd(nodeID, commandID, assetID, contentHash, filename, rawURL string, sizeBytes int64) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:      commandID,
		IdempotencyKey: commandID,
		Action:         "asset.fetch",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"assetId": assetID, "contentHash": contentHash, "filename": filename,
			"sizeBytes": sizeBytes, "url": rawURL,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

// --- 5. An interrupted upload registers nothing (acceptance criterion 5) ---

func TestAssetUploadInterruptedRegistersNothing(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, assetDir := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S5"}, showID)
	mustCtl(t, coord, token, []string{"declare", "--label", "n"}, nodeID)

	content := bytes.Repeat([]byte("bytes that will never fully arrive. "), 256)
	attemptInterruptedAssetUpload(t, coord, token, map[string]string{
		"show": showID, "sequence": "opening", "mediaType": "fseq", "targetKind": "node", "target": nodeID,
	}, "Interrupted.fseq", content, len(content)/3)

	listOut := mustCtl(t, coord, token, []string{"assets", "list", "--show", showID, "--output", "json"})
	var list ctlAssetsListResp
	if err := json.Unmarshal([]byte(listOut), &list); err != nil {
		t.Fatalf("decode assets list json: %v\noutput:\n%s", err, listOut)
	}
	if len(list.Assets) != 0 {
		t.Fatalf("assets list --show %s = %+v, want no asset registered from the interrupted upload", showID, list.Assets)
	}

	staging := filepath.Join(assetDir, ".staging")
	waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		entries, err := os.ReadDir(staging)
		return err == nil && len(entries) == 0
	}, "the coordinator's own staging directory to end up empty after the interrupted upload")
}

// attemptInterruptedAssetUpload sends a real multipart POST /api/v1/assets
// over a raw TCP connection, declaring the FULL body's length in
// Content-Length but writing only a PREFIX of it before closing the
// connection — a real client that stops writing mid-body, not a simulation
// of one. Deliberately bypasses net/http's client (which would refuse to
// send a body shorter than its own declared Content-Length in the first
// place) because that refusal is exactly the client-side behavior this
// test needs to defeat to reach the server's own handling of it.
func attemptInterruptedAssetUpload(t *testing.T, coord *testCoordinator, token string, fields map[string]string, filename string, content []byte, truncateAt int) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, k := range []string{"show", "sequence", "mediaType", "targetKind", "target"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	full := buf.Bytes()
	if truncateAt > len(full) {
		truncateAt = len(full)
	}
	partial := full[:truncateAt]

	conn, err := net.DialTimeout("tcp", coord.httpAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial coordinator: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var header bytes.Buffer
	fmt.Fprintf(&header, "POST /api/v1/assets HTTP/1.1\r\n")
	fmt.Fprintf(&header, "Host: %s\r\n", coord.httpAddr)
	fmt.Fprintf(&header, "Content-Type: %s\r\n", mw.FormDataContentType())
	fmt.Fprintf(&header, "Content-Length: %d\r\n", len(full))
	if token != "" {
		fmt.Fprintf(&header, "Authorization: Bearer %s\r\n", token)
	}
	fmt.Fprintf(&header, "Connection: close\r\n\r\n")

	if _, err := conn.Write(header.Bytes()); err != nil {
		t.Fatalf("write request headers: %v", err)
	}
	// A write error here is exactly the outcome under test (the server
	// already gave up on this connection), not a harness failure.
	_, _ = conn.Write(partial)
	// Close without ever writing the declared remainder: this is the
	// interruption itself.
}

// --- 6. Changing the active show updates every manifest (acceptance criterion 6) ---

func TestActiveShowChangeUpdatesManifestsWithoutDeleting(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	token := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	assetDir := filepath.Join(dataDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir coordinator asset dir: %v", err)
	}

	// The content endpoint runs behind a proxy this test stops before the
	// second show is activated. Without that, activating a show nudges the
	// sync service (api/showobjects.go), the node fetches the new show's
	// asset within milliseconds, and the manifest is legitimately ready
	// before this test can read it. Stopping the proxy is what makes
	// not_ready a stable state to assert rather than a window to race.
	httpAddr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	proxy := startStoppableProxy(t, httpAddr)
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: token, httpAddr: httpAddr, assetDir: assetDir,
		assetContentBaseURL: "http://" + proxy.addr(),
	})

	nodeID := "node-" + uniqueSuffix()
	agentDir := declareAndStartAgent(t, coord, token, nodeID, "n")

	show1 := "show1-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "Show One"}, show1)
	aContent := []byte("show one's own bytes " + uniqueSuffix())
	aPath := writeTempFile(t, "A.fseq", aContent)
	uploadNodeAsset(t, coord, token, show1, "opening", "fseq", nodeID, aPath)
	mustCtl(t, coord, token, []string{"show", "activate"}, show1)

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node to become ready under show1")

	// From here on the node can no longer reach the content endpoint, so
	// anything it lacks it keeps lacking.
	proxy.stop()

	show2 := "show2-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "Show Two"}, show2)
	bContent := []byte("show two's own bytes, unrelated to show one " + uniqueSuffix())
	bPath := writeTempFile(t, "B.fseq", bContent)
	uploadShowAsset(t, coord, token, show2, "closing", "fseq", bPath)

	mustCtl(t, coord, token, []string{"show", "activate"}, show2)

	// Deterministic: the content endpoint is unreachable, so B can never be
	// fetched and not_ready is a resting state rather than a window.
	m := nodeManifest(t, coord, token, nodeID)
	if m.State != "not_ready" {
		t.Fatalf("State = %q immediately after activating show2, want not_ready; manifest=%+v", m.State, m)
	}
	foundMissingB := false
	for _, missing := range m.Missing {
		if missing.Filename == "B.fseq" {
			foundMissingB = true
		}
	}
	if !foundMissingB {
		t.Errorf("Missing = %+v, want it to name B.fseq", m.Missing)
	}

	// Nothing was deleted: show1's asset is still on disk, even though it
	// is no longer expected.
	got, err := os.ReadFile(filepath.Join(agentDir, "A.fseq"))
	if err != nil {
		t.Fatalf("A.fseq is gone from the node's asset directory after switching the active show: %v", err)
	}
	if !bytes.Equal(got, aContent) {
		t.Fatalf("A.fseq's bytes changed after switching the active show")
	}
}

// --- 7. Store unreachable, node keeps what it has (acceptance criterion 7) ---

// stoppableProxy is a plain TCP forwarder to target, simulating the asset
// content endpoint becoming unreachable without touching the coordinator
// process, which stays running and answers every other route throughout.
//
// stop() closes the listener AND every live connection. Closing only the
// listener is not enough and was a defect in this test: the agent's HTTP
// client keeps a connection alive between fetches, so a proxy that stopped
// accepting kept forwarding over the connection it already had, the node
// fetched the asset it was supposed to be unable to reach, and the manifest
// correctly reported ready while this test waited for it to report missing.
type stoppableProxy struct {
	ln     net.Listener
	target string

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

func startStoppableProxy(t *testing.T, target string) *stoppableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for stoppable proxy: %v", err)
	}
	p := &stoppableProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	go p.acceptLoop()
	t.Cleanup(p.stop)
	return p
}

func (p *stoppableProxy) addr() string { return p.ln.Addr().String() }

func (p *stoppableProxy) acceptLoop() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *stoppableProxy) handle(client net.Conn) {
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	if !p.track(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	p.untrack(client, upstream)
}

// track registers both ends of one forwarded connection so stop() can close
// them. It reports false if stop() already ran, so a connection accepted in
// the race window is closed rather than left forwarding.
func (p *stoppableProxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
	return true
}

func (p *stoppableProxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range conns {
		delete(p.conns, c)
		_ = c.Close()
	}
}

// stop closes the listener and every connection currently being forwarded.
// Both halves are required: see this type's own doc comment.
func (p *stoppableProxy) stop() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for c := range p.conns {
		_ = c.Close()
		delete(p.conns, c)
	}
}

func TestAssetSyncSurvivesContentEndpointUnreachable(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	token := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	assetDir := filepath.Join(dataDir, "assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir coordinator asset dir: %v", err)
	}

	httpAddr := fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	proxy := startStoppableProxy(t, httpAddr)

	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: token, httpAddr: httpAddr, assetDir: assetDir,
		assetContentBaseURL: "http://" + proxy.addr(),
	})

	nodeID := "node-" + uniqueSuffix()
	agentDir := declareAndStartAgent(t, coord, token, nodeID, "n")

	showID := "show-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S7"}, showID)
	aContent := []byte("bytes fetched while the proxy was up " + uniqueSuffix())
	aPath := writeTempFile(t, "Held.fseq", aContent)
	uploadNodeAsset(t, coord, token, showID, "opening", "fseq", nodeID, aPath)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node to fetch its asset while the content endpoint is reachable")

	proxy.stop()

	bContent := []byte("bytes that can never be fetched " + uniqueSuffix())
	bPath := writeTempFile(t, "Unreachable.fseq", bContent)
	uploadNodeAsset(t, coord, token, showID, "closing", "fseq", nodeID, bPath)

	// At least one sync tick gets a chance to attempt (and fail) dispatching
	// the new asset through the now-dead proxy.
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		m := nodeManifest(t, coord, token, nodeID)
		for _, missing := range m.Missing {
			if missing.Filename == "Unreachable.fseq" {
				return true
			}
		}
		return false
	}, "manifest to name the unreachable asset as missing")

	// Nothing lost: the previously-held asset is still on disk, unaffected,
	// and the manifest still reports the node as holding it (not among
	// Missing) rather than regressing because a later fetch failed.
	got, err := os.ReadFile(filepath.Join(agentDir, "Held.fseq"))
	if err != nil {
		t.Fatalf("Held.fseq is gone from the node's asset directory: %v", err)
	}
	if !bytes.Equal(got, aContent) {
		t.Fatalf("Held.fseq's bytes changed after the content endpoint went unreachable")
	}
	m := nodeManifest(t, coord, token, nodeID)
	for _, missing := range m.Missing {
		if missing.Filename == "Held.fseq" {
			t.Fatalf("Held.fseq is listed as missing after the content endpoint went unreachable; the node's own holding should still be reported")
		}
	}
}

// --- 8. Exit codes (acceptance criterion 8) ---

func TestAssetsManifestRequireReadyExitCodes(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S8"}, showID)

	t.Run("not_ready exits 20", func(t *testing.T) {
		nodeID := "node-" + uniqueSuffix()
		declareAndStartAgent(t, coord, token, nodeID, "n")

		filePath := writeTempFile(t, "X.fseq", []byte("bytes never fetched"))
		uploadNodeAsset(t, coord, token, showID, "opening", "fseq", nodeID, filePath)
		mustCtl(t, coord, token, []string{"show", "activate"}, showID)

		waitFor(t, 10*time.Second, 100*time.Millisecond, func() bool {
			return apiNodeManifestState(t, coord, nodeID) == "not_ready"
		}, "node to report not_ready")

		code, _, stderr := runCtl(t, coord, token, []string{"assets", "manifest", "--node", nodeID, "--require-ready"})
		if code != exitAssetsNotReady {
			t.Fatalf("exit code = %d, want %d (exitAssetsNotReady); stderr=%s", code, exitAssetsNotReady, stderr)
		}
	})

	t.Run("unknown (none not_ready) exits 21", func(t *testing.T) {
		phantomNode := "node-" + uniqueSuffix()
		mustCtl(t, coord, token, []string{"declare", "--label", "phantom, never runs an agent"}, phantomNode)
		// No agent is ever started for this node, so its manifest stays
		// unknown/never_reported — ComputeNodeManifest checks that BEFORE
		// it ever looks at expected assets, so not_ready is unreachable for
		// a node with no report at all.
		waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
			return apiNodeManifestState(t, coord, phantomNode) == "unknown"
		}, "phantom node to report unknown")

		code, _, stderr := runCtl(t, coord, token, []string{"assets", "manifest", "--node", phantomNode, "--require-ready"})
		if code != exitAssetsUnknown {
			t.Fatalf("exit code = %d, want %d (exitAssetsUnknown); stderr=%s", code, exitAssetsUnknown, stderr)
		}
	})
}

// exitAssetsNotReady and exitAssetsUnknown mirror cmd/showmeshctl/problem.go's
// own constants (20 and 21) — this package may not import cmd/showmeshctl
// (it is a "package main", not importable), so these are restated here the
// same way cmd_assets.go itself restates assetstore's upload budget
// constants across the import-graph boundary.
const (
	exitAssetsNotReady = 20
	exitAssetsUnknown  = 21
)

// --- 9. Absent, null, and empty are three different refusals (acceptance criterion 9) ---

func TestSurfaceChannelRangeRefusesAbsentNullAndZeroDistinctly(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S9"}, showID)
	mustCtl(t, coord, token, []string{"declare", "--label", "n"}, nodeID)
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Original", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "3600", "--width", "40", "--height", "30",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "Src",
	}, surfaceID)

	base := func() map[string]any {
		return map[string]any{
			"show": showID, "name": "Original", "node": nodeID,
			"geometry":  map[string]any{"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output":    map[string]any{"transport": "ndi", "ndi": map[string]any{"sourceName": "Src"}},
		}
	}

	absentBody := base() // no "channelRange" key at all

	nullBody := base()
	nullBody["channelRange"] = nil

	zeroBody := base()
	zeroBody["channelRange"] = map[string]any{"startChannel": 1, "channelCount": 0}

	path := "/api/v1/config/show.surface/" + surfaceID
	status1, body1 := putRawWithToken(t, coord, path, token, absentBody)
	status2, body2 := putRawWithToken(t, coord, path, token, nullBody)
	status3, body3 := putRawWithToken(t, coord, path, token, zeroBody)

	for i, status := range []int{status1, status2, status3} {
		if status != http.StatusBadRequest {
			t.Errorf("attempt %d: status = %d, want 400", i+1, status)
		}
	}

	var p1, p2, p3 v1.Problem
	for i, pair := range []struct {
		body []byte
		dst  *v1.Problem
	}{{body1, &p1}, {body2, &p2}, {body3, &p3}} {
		if err := json.Unmarshal(pair.body, pair.dst); err != nil {
			t.Fatalf("decode problem %d: %v; body=%s", i+1, err, pair.body)
		}
	}
	t.Logf("absent -> %s (%s)", p1.Type, p1.Detail)
	t.Logf("null   -> %s (%s)", p2.Type, p2.Detail)
	t.Logf("zero   -> %s (%s)", p3.Type, p3.Detail)

	if p1.Type == p2.Type || p1.Type == p3.Type || p2.Type == p3.Type {
		t.Fatalf("absent/null/zero channelRange did not each produce a DISTINCT refusal type: absent=%q null=%q zero=%q",
			p1.Type, p2.Type, p3.Type)
	}
	if p1.Detail == p2.Detail || p1.Detail == p3.Detail || p2.Detail == p3.Detail {
		t.Errorf("absent/null/zero channelRange did not each produce a distinct message: absent=%q null=%q zero=%q",
			p1.Detail, p2.Detail, p3.Detail)
	}

	// None of the three refusals silently changed anything.
	surface := getSurface(t, coord, token, surfaceID)
	if surface.ChannelRange.StartChannel != 1 || surface.ChannelRange.ChannelCount != 3600 {
		t.Fatalf("surface channelRange changed after three refused writes: %+v", surface.ChannelRange)
	}
}

// --- 10. Two surfaces on one node are accepted (acceptance criterion 10) ---

func TestTwoSurfacesOnOneNodeAccepted(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S10"}, showID)
	mustCtl(t, coord, token, []string{"declare", "--label", "n"}, nodeID)

	surface1 := "surface-a-" + uniqueSuffix()
	surface2 := "surface-b-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Front", "--node", nodeID,
		"--start-channel", "1", "--channel-count", "300", "--width", "10", "--height", "10",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "Front",
	}, surface1)
	mustCtl(t, coord, token, []string{
		"surface", "set", "--show", showID, "--name", "Back", "--node", nodeID,
		"--start-channel", "301", "--channel-count", "300", "--width", "10", "--height", "10",
		"--pixel-format", "rgb", "--frame-rate", "40", "--transport", "ndi", "--ndi-source-name", "Back",
	}, surface2)

	p1 := getSurface(t, coord, token, surface1)
	p2 := getSurface(t, coord, token, surface2)
	if p1.Node != nodeID || p2.Node != nodeID {
		t.Fatalf("both surfaces should be assigned to %s: got %q and %q", nodeID, p1.Node, p2.Node)
	}

	out := mustCtl(t, coord, token, []string{"surface", "list", "--show", showID})
	if !strings.Contains(out, surface1) || !strings.Contains(out, surface2) {
		t.Fatalf("surface list for %s does not contain both surfaces:\n%s", showID, out)
	}
}

// --- 11. A stale inventory report reads unknown (acceptance criterion 11) ---

func TestStaleInventoryReportReadsUnknown(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	showID := "show-" + uniqueSuffix()
	nodeID := "node-" + uniqueSuffix()
	mustCtl(t, coord, token, []string{"show", "set", "--name", "S11"}, showID)
	mustCtl(t, coord, token, []string{"declare", "--label", "n"}, nodeID)
	mustCtl(t, coord, token, []string{"show", "activate"}, showID)

	agentDir := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent asset dir: %v", err)
	}
	agent := startAgent(t, agentConfig{nodeID: nodeID, assetDir: agentDir})

	// No assets are expected for this show at all, so once a fresh,
	// complete report arrives the manifest is trivially "ready" — the
	// deterministic way to reach ready without a real transfer.
	waitFor(t, 10*time.Second, 100*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "ready"
	}, "node to report ready with nothing expected")
	if state := nodeManifestState(t, coord, token, nodeID); state != "ready" {
		t.Fatalf("showmeshctl assets manifest --node %s reports %q, want ready", nodeID, state)
	}

	agent.sigkill(t) // no further report is ever published again

	inventoryInterval := parseDurationEnv(envAssetInventoryInterval, 2*time.Minute)
	stalenessWindow := 3 * inventoryInterval
	waitFor(t, stalenessWindow+15*time.Second, 200*time.Millisecond, func() bool {
		return apiNodeManifestState(t, coord, nodeID) == "unknown"
	}, "node's report to age past the staleness window and read unknown")

	m := nodeManifest(t, coord, token, nodeID)
	if m.State != "unknown" {
		t.Fatalf("State = %q, want unknown (never not_ready, never ready, for a report that has gone stale)", m.State)
	}
	if m.Reason == nil || !strings.Contains(*m.Reason, "staleness window") {
		t.Errorf("Reason = %v, want it to name the staleness window", m.Reason)
	}
}
