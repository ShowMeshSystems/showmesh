package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func decodeRenderReport(t *testing.T, payload []byte) mqttproto.RenderPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	p, err := mqttproto.DecodeRenderPayload(env)
	if err != nil {
		t.Fatalf("DecodeRenderPayload() error = %v", err)
	}
	return p
}

// TestToRenderFPPConnectHeldFileBoundsEveryStringField is review round 6
// finding 5's own regression test: only Name was bounded (review round 5
// finding 6), but RegistrationReason can embed a colliding competitor's
// own raw Name verbatim (fppconnectregister.go's attemptRegister), up to
// the identical 16 KiB Upload-Name-header bound, and RegistrationAssetID
// is coordinator-supplied with no local bound of its own; neither was
// capped before reaching the wire. This proves every string field is now
// bounded to fppConnectMaxEventStringBytes, truncation marked, not silent.
func TestToRenderFPPConnectHeldFileBoundsEveryStringField(t *testing.T) {
	long := strings.Repeat("x", fppConnectMaxEventStringBytes*2)
	rec := fppConnectHeldRecord{
		Dir:                     long,
		Name:                    long,
		ContentHash:             "sha256:deadbeef",
		Show:                    long,
		ShowID:                  long,
		LogicalSequence:         long,
		UnboundReason:           long,
		RegistrationState:       long,
		RegistrationAssetID:     long,
		RegistrationReason:      long,
		RegistrationProblemType: long,
	}

	wire := toRenderFPPConnectHeldFile(rec)

	checks := map[string]string{
		"Dir":                     wire.Dir,
		"Name":                    wire.Name,
		"Show":                    wire.Show,
		"ShowID":                  wire.ShowID,
		"LogicalSequence":         wire.LogicalSequence,
		"UnboundReason":           wire.UnboundReason,
		"RegistrationState":       wire.RegistrationState,
		"RegistrationAssetID":     wire.RegistrationAssetID,
		"RegistrationReason":      wire.RegistrationReason,
		"RegistrationProblemType": wire.RegistrationProblemType,
	}
	for field, got := range checks {
		if len(got) > fppConnectMaxEventStringBytes {
			t.Errorf("%s length = %d, want at most %d", field, len(got), fppConnectMaxEventStringBytes)
		}
		if !strings.HasSuffix(got, fppConnectEventStringTruncatedSuffix) {
			t.Errorf("%s = %q, want it to end with the truncation marker %q", field, got, fppConnectEventStringTruncatedSuffix)
		}
	}
	if wire.ContentHash != rec.ContentHash {
		t.Fatalf("ContentHash = %q, want it passed through unbounded: %q", wire.ContentHash, rec.ContentHash)
	}
}

// TestFPPConnectRegisterCollidingSlugsBoundsOwnerNameInReason is review
// round 6 finding 5's own regression test for attemptRegister's own side:
// the competitor's raw Name is bounded (fppConnectBoundEventString)
// before it is ever written into RegistrationReason, not only where a
// held record is later read for the wire, since RegistrationReason is
// also persisted to disk and must never carry an unbounded competitor
// name into that file either. The winning competitor's record is seeded
// directly (bypassing a real upload, which a real filesystem's own
// filename-length limit would refuse long before Upload-Name's own much
// larger 16 KiB bound is ever reached): CollidingRecord only ever reads
// this held record from memory, never its bytes on disk, so this is a
// faithful stand-in for a Name this long, however it got there.
func TestFPPConnectRegisterCollidingSlugsBoundsOwnerNameInReason(t *testing.T) {
	held, _ := newTestHeldStore(t)

	fakeSrv, requests := newFPPConnectRegisterFake(t, func(req fppConnectRegisterRequest) (int, string) {
		hash := sha256Hex(req.fileBytes)
		return http.StatusOK, assetResponseBody(t, "asset-owner", hash, false)
	})
	defer fakeSrv.Close()

	newTestFPPConnectRegistrar(t, held, fakeSrv.URL, "")

	longName := "My Show" + strings.Repeat("Z", fppConnectMaxEventStringBytes*4) + ".fseq"
	held.mu.Lock()
	held.records["sequences/"+longName] = fppConnectHeldRecord{
		Dir: "sequences", Name: longName, ContentHash: "sha256:seeded",
		ReceivedAt: time.Now(), Bound: true, Show: "Halloween", ShowID: "halloween-2026",
		LogicalSequence: "my-show",
	}
	held.mu.Unlock()

	colliding := "my_show.fseq" // slugifies to the identical "my-show"
	secondData := []byte("second-file-is-longer")
	uploadAndBind(t, held, "sequences", colliding, secondData)

	pending := waitForRegistrationState(t, held, "sequences", colliding, fppConnectRegistrationPending)
	if len(pending.RegistrationReason) > 3*fppConnectMaxEventStringBytes {
		t.Fatalf("RegistrationReason length = %d, want the embedded competitor name bounded to roughly %d (however many times it appears), not the full %d-byte name", len(pending.RegistrationReason), fppConnectMaxEventStringBytes, len(longName))
	}
	if !strings.Contains(pending.RegistrationReason, fppConnectEventStringTruncatedSuffix) {
		t.Fatalf("RegistrationReason = %q, want it to show the competitor's name was truncated", pending.RegistrationReason)
	}

	if got := len(requests()); got != 0 {
		t.Fatalf("registration requests = %d, want 0: the colliding file must never even attempt", got)
	}
}

// TestTruncateHeldFilesForWirePartitionsUnboundFirstEvenUnderCap is review
// round 5 finding 4's regression test: before this fix,
// truncateHeldFilesForWire only partitioned records unbound-first inside
// its len(records) > maxEntries branch, so a records slice at or under the
// count cap came back in its original dir-then-name order, untouched.
// shrinkRenderPayloadToFitEnvelope's own tail-drop assumes unbound-first
// ordering to drop bound records before unbound ones once the byte budget,
// not the count cap, is what forces a cut, so a report under the count cap
// but still over budget could drop an unbound (operator-actionable) record
// before a bound (already-resolved) one. This proves the reordering
// happens regardless of whether the count cap ever fires.
func TestTruncateHeldFilesForWirePartitionsUnboundFirstEvenUnderCap(t *testing.T) {
	records := []fppConnectHeldRecord{
		{Dir: "sequences", Name: "A.fseq", Bound: true},
		{Dir: "sequences", Name: "B.fseq", Bound: false},
		{Dir: "sequences", Name: "C.fseq", Bound: true},
		{Dir: "sequences", Name: "D.fseq", Bound: false},
	}

	got := truncateHeldFilesForWire(records, len(records)+10)
	if len(got) != len(records) {
		t.Fatalf("got %d records, want %d (well under the cap, nothing should be dropped)", len(got), len(records))
	}
	want := []string{"B.fseq", "D.fseq", "A.fseq", "C.fseq"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("got[%d].Name = %q, want %q; got order = %+v", i, got[i].Name, w, got)
		}
	}
}

// TestToRenderSurfaceReportCarriesRealFrameCounters is a direct regression
// test for a defect this seam's review found: toRenderSurfaceReport
// hardcoded FramesWritten/FramesLate/FramesDropped to 0 behind a stale
// "B2a has no frame writer; B3 populates this" comment, so every frame
// counter this system ever published was a zero regardless of what the
// frame writer actually counted. FramesRate (ADR-040) must round-trip
// too, including staying nil when unmeasured — never a fabricated zero.
func TestToRenderSurfaceReportCarriesRealFrameCounters(t *testing.T) {
	rate := 39.4
	framesAt := time.Date(2026, 8, 17, 12, 0, 45, 0, time.UTC)
	stateAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) // deliberately earlier than framesAt
	snap := pipeline.Snapshot{
		SurfaceID:        "wall-1",
		State:            pipeline.StateRunning,
		FramesWritten:    1234,
		FramesLate:       12,
		FramesDropped:    3,
		FramesRate:       &rate,
		FramesObservedAt: framesAt,
		ObservedAt:       stateAt,
	}
	got := toRenderSurfaceReport(snap)
	if got.FramesWritten != 1234 {
		t.Errorf("FramesWritten = %d, want 1234 (got the real supervisor snapshot value, not a hardcoded 0)", got.FramesWritten)
	}
	if got.FramesLate != 12 {
		t.Errorf("FramesLate = %d, want 12", got.FramesLate)
	}
	if got.FramesDropped != 3 {
		t.Errorf("FramesDropped = %d, want 3", got.FramesDropped)
	}
	if got.FramesRate == nil || *got.FramesRate != rate {
		t.Errorf("FramesRate = %v, want %v", got.FramesRate, rate)
	}

	// This issue's own fix: FramesObservedAt is carried independently of
	// ObservedAt, never collapsed onto it. Proven here with two
	// deliberately different values.
	if !got.FramesObservedAt.Equal(framesAt) {
		t.Errorf("FramesObservedAt = %v, want %v (the frame writer's own window-close timestamp)", got.FramesObservedAt, framesAt)
	}
	if !got.ObservedAt.Equal(stateAt) {
		t.Errorf("ObservedAt = %v, want %v (the pipeline-lifecycle timestamp, unperturbed by frame evidence)", got.ObservedAt, stateAt)
	}

	unmeasured := toRenderSurfaceReport(pipeline.Snapshot{SurfaceID: "wall-1", State: pipeline.StateRunning})
	if unmeasured.FramesRate != nil {
		t.Errorf("FramesRate = %v for an unmeasured snapshot, want nil (never a fabricated zero)", unmeasured.FramesRate)
	}
	if !unmeasured.FramesObservedAt.IsZero() {
		t.Errorf("FramesObservedAt = %v for a snapshot whose frame writer never sampled, want zero", unmeasured.FramesObservedAt)
	}
}

// TestApplyContentIdentityStampsContentObservedAtFromTheReadTimeNotAppliedAt
// is this issue's agent-side regression test. The rejected fix (using
// Assignment.AppliedAt) ages exactly like the defect it's meant to fix: a
// one-shot stamp written when the writer was swapped, which a surface
// rendering steadily for ten minutes would still be stuck behind. The
// correct fix stamps ContentObservedAt from the caller's own read time
// (now(), passed in), which is deliberately far LATER than AppliedAt here —
// proving the two are not conflated.
func TestApplyContentIdentityStampsContentObservedAtFromTheReadTimeNotAppliedAt(t *testing.T) {
	appliedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	readAt := appliedAt.Add(10 * time.Minute) // a steady render, read fresh on a later report tick

	a := pipeline.Assignment{
		SurfaceID: "wall-1",
		RawParams: []byte(`{"fseqFilename":"halloween-01.fseq","fseqContentHash":"sha256:deadbeef"}`),
		AppliedAt: appliedAt,
		CueID:     "cue-42",
	}

	var rep mqttproto.RenderSurfaceReport
	applyContentIdentity(&rep, a, readAt, discardLogger())

	if rep.FSEQFilename != "halloween-01.fseq" {
		t.Errorf("FSEQFilename = %q, want %q", rep.FSEQFilename, "halloween-01.fseq")
	}
	if rep.FSEQContentHash != "sha256:deadbeef" {
		t.Errorf("FSEQContentHash = %q, want %q", rep.FSEQContentHash, "sha256:deadbeef")
	}
	if !rep.ContentObservedAt.Equal(readAt) {
		t.Errorf("ContentObservedAt = %v, want the caller's read time %v (not AppliedAt %v — a one-shot apply stamp ages exactly like this issue's defect)",
			rep.ContentObservedAt, readAt, appliedAt)
	}
}

// TestApplyContentIdentityLeavesContentObservedAtZeroWhenNoContent proves
// applyContentIdentity does not stamp ContentObservedAt for an assignment
// whose params carry no fseqFilename: no content identity was actually
// applied to rep, so there is nothing to date.
func TestApplyContentIdentityLeavesContentObservedAtZeroWhenNoContent(t *testing.T) {
	a := pipeline.Assignment{
		SurfaceID: "wall-1",
		RawParams: []byte(`{}`),
	}
	var rep mqttproto.RenderSurfaceReport
	applyContentIdentity(&rep, a, time.Now(), discardLogger())

	if !rep.ContentObservedAt.IsZero() {
		t.Errorf("ContentObservedAt = %v, want zero (no content identity was applied)", rep.ContentObservedAt)
	}
}

// TestApplyContentIdentityWithholdsIdentityForAnEmptyHash is this issue's
// unit-level regression test: the apply path (renderops.go) always
// persists a non-empty fseqContentHash alongside a non-empty fseqFilename,
// so this shape (filename set, hash empty) only reaches assignments.json
// by hand-editing or predating the content-identity contract. Before this
// fix, applyContentIdentity copied the empty hash through unconditionally,
// which RenderPayload.Validate's both-empty-or-both-set invariant rejects
// once this surface reaches a real envelope build — see
// TestRunRenderReportPublishesRemainingSurfacesWhenOneAssignmentHasAnEmptyHash
// for that failure's full blast radius. This proves the malformed pair is
// withheld (both fields left "", satisfying Validate) with a stated
// reason, rather than publishing an unverified filename with no hash.
func TestApplyContentIdentityWithholdsIdentityForAnEmptyHash(t *testing.T) {
	a := pipeline.Assignment{
		SurfaceID: "wall-1",
		RawParams: []byte(`{"fseqFilename":"halloween-01.fseq","fseqContentHash":""}`),
		CueID:     "cue-42",
	}
	rep := mqttproto.RenderSurfaceReport{
		SurfaceID:     "wall-1",
		PipelineState: mqttproto.RenderPipelineStateRunning,
	}
	applyContentIdentity(&rep, a, time.Now(), discardLogger())

	if rep.FSEQFilename != "" {
		t.Errorf("FSEQFilename = %q, want empty: an unverified filename must never reach the wire", rep.FSEQFilename)
	}
	if rep.FSEQContentHash != "" {
		t.Errorf("FSEQContentHash = %q, want empty", rep.FSEQContentHash)
	}
	if rep.CueID != "" {
		t.Errorf("CueID = %q, want empty: no identity was actually applied", rep.CueID)
	}
	if rep.ContentIdentityReason == "" {
		t.Error("ContentIdentityReason is empty, want it to name why this surface's content identity was withheld")
	}
	if !rep.ContentObservedAt.IsZero() {
		t.Errorf("ContentObservedAt = %v, want zero: no genuine content identity was applied", rep.ContentObservedAt)
	}

	payload := mqttproto.RenderPayload{Surfaces: []mqttproto.RenderSurfaceReport{rep}}
	if err := payload.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil: withholding the pair must satisfy the both-empty-or-both-set invariant", err)
	}
}

// TestRunRenderReportPublishesRemainingSurfacesWhenOneAssignmentHasAnEmptyHash
// is this issue's own reproduction. One persisted assignment carrying a
// fseqFilename with no fseqContentHash (unreachable via the current apply
// path, but reachable by a hand-edited or pre-content-identity-contract
// assignments.json) must never silence this node's ENTIRE render report.
// Against the unmodified tree, applyContentIdentity copies the empty hash
// through, RenderPayload.Validate rejects the resulting surface, and
// publishOneRenderReport's "log and return" on a failed envelope build
// drops the healthy surface's report too — this test times out waiting for
// any publish at all. After the fix, the healthy surface's content
// identity survives intact and the malformed surface is named with a
// reason, in the same published report.
// TestApplyContentIdentityWithholdsIdentityForAnEmptyHashLogsAWarning proves
// the operator-visible half of TestApplyContentIdentityWithholdsIdentityForAnEmptyHash's
// case: a persisted assignment carrying a filename with no hash is not just
// silently withheld from the wire, it is also logged at Warn with the
// surface id, so this failure mode shows up in the node's own logs without
// waiting for a coordinator round trip. Deleting the logger.Warn call in
// applyContentIdentity (keeping everything else) must fail this test even
// though the wire-level withholding test above stays green.
func TestApplyContentIdentityWithholdsIdentityForAnEmptyHashLogsAWarning(t *testing.T) {
	a := pipeline.Assignment{
		SurfaceID: "wall-1",
		RawParams: []byte(`{"fseqFilename":"halloween-01.fseq","fseqContentHash":""}`),
		CueID:     "cue-42",
	}
	rep := mqttproto.RenderSurfaceReport{
		SurfaceID:     "wall-1",
		PipelineState: mqttproto.RenderPipelineStateRunning,
	}
	logger, logs := capturingLogger()
	applyContentIdentity(&rep, a, time.Now(), logger)

	got := logs.String()
	if !strings.Contains(got, "level=WARN") {
		t.Errorf("log output = %q, want a WARN-level entry", got)
	}
	if !strings.Contains(got, "fseqFilename with no fseqContentHash") {
		t.Errorf("log output = %q, want it to name the fseqFilename-with-no-fseqContentHash failure", got)
	}
	if !strings.Contains(got, "surface_id=wall-1") {
		t.Errorf("log output = %q, want it to name the surface id wall-1", got)
	}
}

func TestRunRenderReportPublishesRemainingSurfacesWhenOneAssignmentHasAnEmptyHash(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	for _, id := range []string{"good-surface", "bad-surface"} {
		if err := sup.Apply(pipeline.DefaultTestPatternSpec(id)); err != nil {
			t.Fatalf("Apply(%s): %v", id, err)
		}
	}
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer awaitCancel()
	for _, id := range []string{"good-surface", "bad-surface"} {
		if _, ok := sup.AwaitState(awaitCtx, id, []pipeline.State{pipeline.StateRunning}, time.Time{}, -1, 5*time.Millisecond); !ok {
			t.Fatalf("setup: %s never reached running", id)
		}
	}

	store := pipeline.NewAssignmentStore(t.TempDir())
	if err := store.Save([]pipeline.Assignment{
		{SurfaceID: "good-surface", RawParams: []byte(`{"fseqFilename":"halloween-01.fseq","fseqContentHash":"sha256:deadbeef"}`)},
		{SurfaceID: "bad-surface", RawParams: []byte(`{"fseqFilename":"corrupt.fseq","fseqContentHash":""}`)},
	}); err != nil {
		t.Fatalf("setup: Save: %v", err)
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, store, newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for publish: one malformed persisted assignment silenced the entire render report")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runRenderReport to return")
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.Surfaces) != 2 {
		t.Fatalf("got %d surfaces, want 2 (the malformed assignment must not drop the healthy surface)", len(report.Surfaces))
	}

	byID := map[string]mqttproto.RenderSurfaceReport{}
	for _, s := range report.Surfaces {
		byID[s.SurfaceID] = s
	}

	good, ok := byID["good-surface"]
	if !ok {
		t.Fatal("good-surface missing from report")
	}
	if good.FSEQFilename != "halloween-01.fseq" || good.FSEQContentHash != "sha256:deadbeef" {
		t.Errorf("good-surface content identity = %+v, want the persisted filename/hash intact", good)
	}

	bad, ok := byID["bad-surface"]
	if !ok {
		t.Fatal("bad-surface missing from report")
	}
	if bad.FSEQFilename != "" || bad.FSEQContentHash != "" {
		t.Errorf("bad-surface content identity = %+v, want both empty: an unverified filename must never reach the wire", bad)
	}
	if bad.ContentIdentityReason == "" {
		t.Error("bad-surface ContentIdentityReason is empty, want it to name the malformed persisted assignment")
	}
}

// TestRunRenderReportPublishesOnTick proves a tick produces a retained
// publish on the node's observed/render topic reflecting the supervisor's
// real state, following runAssetInventory's identical test shape.
func TestRunRenderReportPublishesOnTick(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer awaitCancel()
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateRunning}, time.Time{}, -1, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached running")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runRenderReport to return")
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	if calls[0].topic != "showmesh/nodes/media-03/observed/render" {
		t.Fatalf("topic = %q, want showmesh/nodes/media-03/observed/render", calls[0].topic)
	}
	if !calls[0].retain {
		t.Fatalf("retain = false, want true (ObservedDeliveryPolicy is retained)")
	}
	if calls[0].qos != mqttproto.ObservedDeliveryPolicy.QoS {
		t.Fatalf("qos = %d, want %d", calls[0].qos, mqttproto.ObservedDeliveryPolicy.QoS)
	}

	report := decodeRenderReport(t, calls[0].payload)
	if len(report.Surfaces) != 1 {
		t.Fatalf("got %d surfaces, want 1", len(report.Surfaces))
	}
	if report.Surfaces[0].SurfaceID != "surface-1" {
		t.Fatalf("SurfaceID = %q, want surface-1", report.Surfaces[0].SurfaceID)
	}
	if report.Surfaces[0].PipelineState != mqttproto.RenderPipelineStateRunning {
		t.Fatalf("PipelineState = %q, want %q", report.Surfaces[0].PipelineState, mqttproto.RenderPipelineStateRunning)
	}
}

// TestRunRenderReportIncludesHeldFilesAndEvents is review round 1 finding
// 1's regression test: before this fix, fppConnectHeldStore's Held() and
// Events() had no non-test caller at all, so ADR-044 decision 8's
// "reported as an unbound held file the operator can claim" had no place
// to actually reach an operator, since xLights never inspects the
// playlist POST's status. A completed-but-unbound held file and an
// unknown-playlist evidence event must both appear in a published report.
func TestRunRenderReportIncludesHeldFilesAndEvents(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }
	outcome, reason, rec := held.WriteChunk("sequences", "Unbound.fseq", 0, 3, strings.NewReader("abc"), 3, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkCompleted {
		t.Fatalf("setup: outcome = %v reason = %q, want completed", outcome, reason)
	}
	if rec.Bound {
		t.Fatalf("setup: record unexpectedly bound: %+v", rec)
	}
	held.RecordUnknownPlaylist("Mystery", []string{"Foo.fseq"}, time.Now())

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runRenderReport to return")
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)

	if report.FPPConnectHeldCount != 1 {
		t.Fatalf("FPPConnectHeldCount = %d, want 1", report.FPPConnectHeldCount)
	}
	if len(report.FPPConnectHeld) != 1 {
		t.Fatalf("got %d held files, want 1: %+v", len(report.FPPConnectHeld), report.FPPConnectHeld)
	}
	f := report.FPPConnectHeld[0]
	if f.Name != "Unbound.fseq" || f.Dir != "sequences" {
		t.Fatalf("held file = %+v, want sequences/Unbound.fseq", f)
	}
	if f.Bound {
		t.Fatalf("held file = %+v, want unbound", f)
	}
	if f.UnboundReason == "" {
		t.Fatal("UnboundReason is empty, want it to name why this file is unbound")
	}

	var foundEvent bool
	for _, ev := range report.FPPConnectHeldEvents {
		if ev.Kind == "unknown" && ev.Name == "Mystery" {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("no unknown-playlist event in the published report; events = %+v", report.FPPConnectHeldEvents)
	}
	if report.FPPConnectHeldEventsTotal != len(report.FPPConnectHeldEvents) {
		t.Fatalf("FPPConnectHeldEventsTotal = %d, want it to equal the published length %d when nothing was trimmed", report.FPPConnectHeldEventsTotal, len(report.FPPConnectHeldEvents))
	}
}

// TestRunRenderReportSetsFPPConnectHeldEventsTotalWhenEventsAreDroppedForSize
// is review round 8 finding 2's own regression test: shrinkRenderPayloadToFitEnvelope
// drops events (and then held records) to fit the envelope's size budget
// with nothing on the wire saying so; a consumer reading only
// len(FPPConnectHeldEvents) could never tell "this node genuinely has
// exactly this many events" from "some were cut to fit." Many held
// records, each carrying several near-maximum bounded string fields, push
// this report well past its size budget on their own, so
// shrinkRenderPayloadToFitEnvelope's event-drop branch (checked first)
// has to give up at least one of the held events to make room. This
// proves FPPConnectHeldEventsTotal still reports the true total,
// independent of how many of those events survive onto the wire.
func TestRunRenderReportSetsFPPConnectHeldEventsTotalWhenEventsAreDroppedForSize(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)

	held.mu.Lock()
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("Big%04d.fseq", i)
		held.records["sequences/"+name] = fppConnectHeldRecord{
			Dir: "sequences", Name: name, ContentHash: "sha256:deadbeef",
			ReceivedAt:         time.Now(),
			Bound:              true,
			Show:               strings.Repeat("S", 2000),
			ShowID:             strings.Repeat("I", 2000),
			LogicalSequence:    strings.Repeat("L", 2000),
			RegistrationState:  fppConnectRegistrationPending,
			RegistrationReason: strings.Repeat("R", 2000),
		}
	}
	const wantEventsTotal = 5
	for i := 0; i < wantEventsTotal; i++ {
		held.events = append(held.events, fppConnectEvent{
			Kind: "unknown", Name: fmt.Sprintf("Mystery%d", i), At: time.Now(),
		})
	}
	held.mu.Unlock()

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)

	if report.FPPConnectHeldEventsTotal != wantEventsTotal {
		t.Fatalf("FPPConnectHeldEventsTotal = %d, want %d (the true total, independent of how many the size budget left standing)", report.FPPConnectHeldEventsTotal, wantEventsTotal)
	}
	if len(report.FPPConnectHeldEvents) >= wantEventsTotal {
		t.Fatalf("published events = %d, want fewer than the true total %d: the size budget must have dropped at least one", len(report.FPPConnectHeldEvents), wantEventsTotal)
	}
}

// TestRunRenderReportStaysUnderEnvelopeLimitWithAnOversizedPlaylistPost is
// review round 3 finding 2's regression test: one unauthenticated
// POST /api/playlist/{name} can name tens of thousands of distinct
// sequenceName/mediaName values in a single 1 MiB body. Before this fix,
// every one of them rode the resulting event onto every subsequent render
// report, with no cap anywhere between the store and this report's own
// size budget. NewRenderEnvelope's Validate call would not have caught
// this: it never checks the payload's total serialized size, only field
// counts (review round 5 finding 3). Left unbounded, this would have
// built and published successfully, then been rejected by the
// coordinator's DecodeEnvelope once the oversized bytes actually arrived,
// a report skipped forever, not degraded once, for as long as the event
// kept riding every publish. This proves the publish still succeeds and
// the resulting bytes still decode under the wire envelope limit.
func TestRunRenderReportStaysUnderEnvelopeLimitWithAnOversizedPlaylistPost(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	// Build a playlist body naming as many distinct sequenceName entries
	// as fit under fppConnectMaxPlaylistBodyBytes (1 MiB), against an
	// unknown show name so every one lands in a single "unknown" event.
	var sb strings.Builder
	sb.WriteString(`{"mainPlaylist":[`)
	for i := 0; sb.Len() < fppConnectMaxPlaylistBodyBytes-256; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"sequenceName":"S%08d.fseq"}`, i)
	}
	sb.WriteString(`]}`)

	resp, body := postPlaylist(t, srv, "DoesNotExist", []byte(sb.String()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	events := held.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Entries) > fppConnectMaxEventEntries {
		t.Fatalf("event carries %d entries, want at most %d", len(events[0].Entries), fppConnectMaxEventEntries)
	}
	if events[0].EntriesTruncated == 0 {
		t.Fatal("EntriesTruncated = 0, want a positive count given how oversized the posted body was")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish (an unbounded field would push the published bytes over DecodeEnvelope's size limit on the receiving end)")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.FPPConnectHeldEvents) == 0 {
		t.Fatal("no events in the published report")
	}
}

// TestRunRenderReportStaysUnderEnvelopeLimitWithASingleOversizedEntry is
// review round 4 finding 1's regression test for its first case: a
// single ~900 KiB sequenceName in an otherwise tiny playlist body defeats
// fppConnectMaxEventEntries entirely (that cap bounds how MANY entries an
// event carries, not how large any one of them is), so before this fix
// this one outsized entry alone could already carry the resulting event,
// and so the whole report, past the envelope limit.
func TestRunRenderReportStaysUnderEnvelopeLimitWithASingleOversizedEntry(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	hugeName := strings.Repeat("N", 900*1024) + ".fseq"
	body := fmt.Sprintf(`{"mainPlaylist":[{"sequenceName":%q}]}`, hugeName)

	resp, respBody := postPlaylist(t, srv, "DoesNotExist", []byte(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, respBody)
	}

	events := held.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(events[0].Entries))
	}
	if got := len(events[0].Entries[0]); got > fppConnectMaxEventStringBytes {
		t.Fatalf("entry length = %d, want at most %d (review round 4 finding 1's per-string bound)", got, fppConnectMaxEventStringBytes)
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish (an unbounded entry would push the published bytes over DecodeEnvelope's size limit on the receiving end)")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.FPPConnectHeldEvents) == 0 {
		t.Fatal("no events in the published report")
	}
}

// TestRunRenderReportStaysUnderEnvelopeLimitWithManyBadNameRefusals is
// review round 4 finding 1's regression test for its second case: an
// Upload-Name header can carry up to fppConnectMaxHeaderBytes (16 KiB),
// and a bad-name refusal's Reason echoes that name straight back inside a
// formatted sentence, so before this fix 50 such refusals alone (well
// under fppConnectMaxEvents, so none of them would ever be evicted by the
// log's own length cap) could already carry the report past the envelope
// limit on Name and Reason bulk alone.
func TestRunRenderReportStaysUnderEnvelopeLimitWithManyBadNameRefusals(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	badName := strings.Repeat("a", 8*1024) + "/" + strings.Repeat("b", 8*1024)
	for i := 0; i < 50; i++ {
		req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/file/sequences", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("building request %d: %v", i, err)
		}
		req.Header.Set("Upload-Name", badName)
		req.Header.Set("Upload-Offset", "0")
		req.Header.Set("Upload-Length", "1")
		req.ContentLength = 1
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PATCH %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("PATCH %d: status = %d, want 403", i, resp.StatusCode)
		}
	}

	events := held.Events()
	if len(events) == 0 {
		t.Fatal("no bad-name events recorded")
	}
	for _, ev := range events {
		if got := len(ev.Name); got > fppConnectMaxEventStringBytes {
			t.Fatalf("event Name length = %d, want at most %d", got, fppConnectMaxEventStringBytes)
		}
		if got := len(ev.Reason); got > fppConnectMaxEventStringBytes {
			t.Fatalf("event Reason length = %d, want at most %d", got, fppConnectMaxEventStringBytes)
		}
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish (unbounded Name/Reason fields would push the published bytes over DecodeEnvelope's size limit on the receiving end)")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.FPPConnectHeldEvents) == 0 {
		t.Fatal("no events in the published report")
	}
}

// TestRunRenderReportPublishesOnTrigger proves a signal on triggered
// produces an immediate publish, out of cadence: the render-state
// counterpart to runAssetInventory's identical trigger behaviour.
func TestRunRenderReportPublishesOnTrigger(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	pub := newFakePublisher()
	triggered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, nil, triggered, discardLogger())
	}()

	select {
	case triggered <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending trigger")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	<-done

	if len(pub.snapshot()) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(pub.snapshot()))
	}
}

// TestRunRenderReportStartingStateDecodes is a regression test for F8: a
// report published while a surface is StateStarting must round-trip through
// DecodeRenderPayload, exactly as the publisher's own decoder would see it
// on the wire. Before the fix, StateStarting carried an empty Reason, which
// RenderPayload.Validate refuses for any non-running state — so a tick
// landing during startup produced a message the coordinator's own decoder
// would discard whole.
func TestRunRenderReportStartingStateDecodes(t *testing.T) {
	clock := newTestClock()
	fs := &blockingRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		fs.release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer awaitCancel()
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateStarting}, time.Time{}, -1, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached starting")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, pipeline.NewAssignmentStore(t.TempDir()), newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}

	// The bug this guards against makes DecodeRenderPayload itself fail, so
	// decodeRenderReport (which calls it) is the assertion.
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.Surfaces) != 1 {
		t.Fatalf("got %d surfaces, want 1", len(report.Surfaces))
	}
	if report.Surfaces[0].PipelineState != mqttproto.RenderPipelineStateStarting {
		t.Fatalf("PipelineState = %q, want %q", report.Surfaces[0].PipelineState, mqttproto.RenderPipelineStateStarting)
	}
	if report.Surfaces[0].Reason == "" {
		t.Fatalf("Reason is empty for a non-running state, which RenderPayload.Validate should have refused")
	}
}

// blockingRenderStarter never calls onRunningMarker and never exits on its
// own, so the supervisor stays in StateStarting until release() is called —
// unlike fakeRenderStarter, which fires the marker immediately.
type blockingRenderStarter struct {
	exitCh chan pipeline.ExitResult
}

func (f *blockingRenderStarter) Start(_ context.Context, _ string, _ []string, _ func()) (pipeline.ProcessHandle, error) {
	f.exitCh = make(chan pipeline.ExitResult, 1)
	return &fakeRenderProcess{exitCh: f.exitCh}, nil
}

func (f *blockingRenderStarter) release() {
	if f.exitCh != nil {
		select {
		case f.exitCh <- pipeline.ExitResult{Signaled: true}:
		default:
		}
	}
}

// newTestClock is a small time.Time-returning helper distinct from
// heartbeat_test.go's own fakeClock type (unexported in this package, and
// this file intentionally does not reach into it) — a fixed, monotonically
// advancing wrapper is unnecessary here since these tests do not assert on
// backoff timing, only on publish behaviour.
type testClock struct{ t time.Time }

func newTestClock() *testClock      { return &testClock{t: time.Now()} }
func (c *testClock) now() time.Time { return c.t }

// fakeRenderStarter is a minimal pipeline.ProcessStarter for this file's
// tests: it reports the running marker immediately and never exits on its
// own, matching pipeline package's own fakeStarter default behaviour
// (duplicated here rather than exported across the package boundary, since
// pipeline's fakes are deliberately test-only and unexported).
type fakeRenderStarter struct{}

func (f *fakeRenderStarter) Start(_ context.Context, _ string, _ []string, onRunningMarker func()) (pipeline.ProcessHandle, error) {
	if onRunningMarker != nil {
		onRunningMarker()
	}
	return &fakeRenderProcess{exitCh: make(chan pipeline.ExitResult, 1)}, nil
}

type fakeRenderProcess struct {
	exitCh chan pipeline.ExitResult
}

func (p *fakeRenderProcess) Wait() pipeline.ExitResult { return <-p.exitCh }
func (p *fakeRenderProcess) Kill() error {
	select {
	case p.exitCh <- pipeline.ExitResult{Signaled: true}:
	default:
	}
	return nil
}
func (p *fakeRenderProcess) Pid() int                  { return 1 }
func (p *fakeRenderProcess) Stdin() (io.Writer, error) { return io.Discard, nil }
