package agent

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// writeSynthFSEQ writes a minimal, valid, UNCOMPRESSED FSEQ v2 file (no
// sparse ranges, no block table — CompressionNone needs neither) at
// dir/name: frameCount frames of channelCount bytes each, byte value
// (frame%250)+1 repeated, matching fakeFrameSource's own pattern in
// pipeline/frame_test.go so a caller can assert on extracted content the
// same way. Field layout is RES-017's (see pkg/fseq's own Open, which this
// mirrors exactly for the CompressionNone case).
func writeSynthFSEQ(t *testing.T, dir, name string, channelCount uint32, frameCount uint32, stepTimeMS byte) string {
	t.Helper()
	const headerSize = 32
	hdr := make([]byte, headerSize)
	copy(hdr[0:4], "PSEQ")
	binary.LittleEndian.PutUint16(hdr[4:6], headerSize)  // chanDataOffset
	hdr[6] = 2                                           // versionMinor (2.2)
	hdr[7] = 2                                           // versionMajor
	binary.LittleEndian.PutUint16(hdr[8:10], headerSize) // declaredHeaderLen
	binary.LittleEndian.PutUint32(hdr[10:14], channelCount)
	binary.LittleEndian.PutUint32(hdr[14:18], frameCount)
	hdr[18] = stepTimeMS
	hdr[20] = 0                                  // compression none, high nibble of block count = 0
	hdr[21] = 0                                  // block count low byte
	hdr[22] = 0                                  // numSparse
	binary.LittleEndian.PutUint64(hdr[24:32], 1) // uniqueID

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fseq fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	for frame := uint32(0); frame < frameCount; frame++ {
		row := make([]byte, channelCount)
		for i := range row {
			row[i] = byte(frame%250) + 1
		}
		if _, err := f.Write(row); err != nil {
			t.Fatalf("write frame %d: %v", frame, err)
		}
	}
	return path
}

// fseqApplyParams builds a render.surface.apply params map carrying B3's
// FSEQ fields, matching what the coordinator's render.surface.apply
// dispatch actually sends (build contract ruling 4).
func fseqApplyParams(surfaceID, filename, contentHash string, startChannel1Based, channelCount, width, height int, pixelFormat string, frameRate int) map[string]any {
	return map[string]any{
		"surfaceId": surfaceID,
		"channelRange": map[string]any{
			"startChannel": float64(startChannel1Based),
			"channelCount": float64(channelCount),
		},
		"geometry": map[string]any{
			"width":       float64(width),
			"height":      float64(height),
			"pixelFormat": pixelFormat,
		},
		"frameRate":       float64(frameRate),
		"fseqFilename":    filename,
		"fseqContentHash": contentHash,
	}
}

// TestApplySurfaceWithFSEQBuildsRealSpecAndStartsFrameWriter is the
// end-to-end proof that a render.surface.apply carrying real FSEQ fields
// (not just surfaceId, as the pre-B3 tests in renderops_test.go use) opens
// the real file, builds FSEQSourceSpec instead of DefaultTestPatternSpec,
// and starts a frame writer that actually writes bytes into the pipeline's
// stdin — using a real *fseq.File, never a fake, per this seam's own
// "prove extraction against real components" requirement.
func TestApplySurfaceWithFSEQBuildsRealSpecAndStartsFrameWriter(t *testing.T) {
	dir := t.TempDir()
	const channelCount = 12 // 2x2 rgb: width*height*3 = 12
	path := writeSynthFSEQ(t, dir, "surface-1.fseq", channelCount, 100, 5)
	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	params := fseqApplyParams("surface-1", "surface-1.fseq", hash, 1, channelCount, 2, 2, "rgb", 40)
	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", params)
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	result := decodeResultFromCall(t, pub.snapshot()[0])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("Outcome = %q, want confirmed; reason = %q", result.Outcome, result.Reason)
	}

	// The pipeline must have been built via FSEQSourceSpec, not
	// DefaultTestPatternSpec: proven indirectly by the frame writer
	// actually producing written frames (a test-pattern pipeline has no
	// frame writer attached at all).
	deadline := time.Now().Add(2 * time.Second)
	var snap pipeline.Snapshot
	for time.Now().Before(deadline) {
		var ok bool
		snap, ok = sup.Snapshot("surface-1")
		if ok && snap.FramesWritten > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if snap.FramesWritten == 0 {
		t.Fatalf("FramesWritten = 0 after 2s, want > 0 (frame writer never started or never wrote)")
	}

	// render.surface.clear must stop the frame writer cleanly (no panic,
	// no goroutine leak asserted here beyond t.Cleanup's own
	// sup.Shutdown, which would hang if a writer's Stop() never returned).
	clearCmd := renderCmd("render.surface.clear", "cmd-2", "idem-2", map[string]any{"surfaceId": "surface-1"})
	topic2, payload2 := buildCmdMessage(t, clock, clearCmd)
	h.HandleMessage(context.Background(), pub, topic2, payload2)
	result2 := decodeResultFromCall(t, pub.snapshot()[1])
	if result2.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("clear Outcome = %q, want confirmed; reason = %q", result2.Outcome, result2.Reason)
	}
}

// TestApplySurfaceWithFSEQRefusesContentHashMismatch proves ADR-028's rule
// is enforced at the agent boundary: a coordinator-declared content hash
// that does not match the actual on-disk bytes refuses the assignment
// rather than rendering unverified content.
func TestApplySurfaceWithFSEQRefusesContentHashMismatch(t *testing.T) {
	dir := t.TempDir()
	writeSynthFSEQ(t, dir, "surface-1.fseq", 12, 10, 25)

	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	params := fseqApplyParams("surface-1", "surface-1.fseq", "sha256:0000000000000000000000000000000000000000000000000000000000000000", 1, 12, 2, 2, "rgb", 40)
	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", params)
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	result := decodeResultFromCall(t, pub.snapshot()[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed for a content hash mismatch; reason = %q", result.Outcome, result.Reason)
	}
}

// TestApplySurfaceWithFSEQRefusesUncoveredChannelRange proves a channel
// range the file does not cover (start+count beyond channelCount, for this
// uncompressed no-sparse fixture) refuses the assignment with a stated
// reason, rather than starting a pipeline that will silently render wrong
// or absent pixels forever.
func TestApplySurfaceWithFSEQRefusesUncoveredChannelRange(t *testing.T) {
	dir := t.TempDir()
	path := writeSynthFSEQ(t, dir, "surface-1.fseq", 12, 10, 25)
	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	// startChannel 100 (1-based) is far past this 12-channel file.
	params := fseqApplyParams("surface-1", "surface-1.fseq", hash, 100, 12, 2, 2, "rgb", 40)
	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", params)
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	result := decodeResultFromCall(t, pub.snapshot()[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed for an uncovered channel range; reason = %q", result.Outcome, result.Reason)
	}
}

// TestApplySurfaceWithFSEQRejectsPathInFilename proves fseqFilename cannot
// be used to escape the asset directory (matching assets.go's
// validateAssetFilename rule one level up in this same seam).
func TestApplySurfaceWithFSEQRejectsPathInFilename(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	h := newCommandHandler(testNodeID, dir, "", nil, renderOps, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	params := fseqApplyParams("surface-1", "../escape.fseq", "sha256:whatever", 1, 12, 2, 2, "rgb", 40)
	cmd := renderCmd("render.surface.apply", "cmd-1", "idem-1", params)
	topic, payload := buildCmdMessage(t, clock, cmd)
	h.HandleMessage(context.Background(), pub, topic, payload)

	result := decodeResultFromCall(t, pub.snapshot()[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want failed for a path-shaped fseqFilename; reason = %q", result.Outcome, result.Reason)
	}
}
