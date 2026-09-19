package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// This file is ADR-053 decision 7's node-side alert sequence: "weatherdelay.
// start", node-scoped like audionodesilenceops.go's "audio.node.silence" —
// no sessionId, no revision, idempotent, never refused for state reasons —
// and "weatherdelay.resume", which ends it. Both run the SAME code this
// file exposes as doStart/doResume, whether dispatched as an agent
// operation (command.go) or over the signed HTTP route
// (fppconnecthttp.go): ADR-053 decision 8's parallel delivery paths must
// reach one implementation, not two independently written ones.

// weatherDelayAlertSessionID is the one audio session id a weather delay
// alert ever plays on, matching cueActivationAudioSessionID's identical
// one-well-known-id-per-purpose convention: a second weatherdelay.start
// while the alert is already playing addresses the same session, so it
// can be recognized as already in progress rather than stacked.
const weatherDelayAlertSessionID = pkgaudio.SessionID("weatherdelay:alert")

// weatherDelayDefaultRepeatCount is ADR-053 decision 7's own default: "ten
// by default", used when the coordinator's plan has not set RepeatCount
// (0, "unset" per [mqttproto.WeatherDelayPlan]'s own doc comment).
const weatherDelayDefaultRepeatCount = 10

var weatherDelayStartKnownKeys = map[string]bool{"kind": true}
var weatherDelayResumeKnownKeys = map[string]bool{}

// weatherDelayOperations builds the node-scoped "weatherdelay.start" and
// "weatherdelay.resume" allowlist entries and is also called directly by
// the signed HTTP start route (fppconnecthttp.go), so a valid signed
// request runs the identical code a dispatched operation runs.
type weatherDelayOperations struct {
	holder   *WeatherDelayHolder
	audioMgr *audio.Manager
	assetDir string

	// alertMu guards alertKind: which kind's alert (if any) this node
	// most recently started into weatherDelayAlertSessionID. pkg/audio's
	// own session identity comparison (TargetMediaIdentity) is keyed to a
	// single, non-playlist media reference; the alert is a playlist, so
	// this node-agent-level record is what lets a second
	// "weatherdelay.start" for the SAME kind recognize the alert is
	// already in progress rather than restarting it, matching
	// cueactivationops.go's own announcementCueID precedent for the
	// identical "pkg/audio carries no identity a playlist call can read
	// back" gap.
	alertMu   sync.Mutex
	alertKind string
}

// weatherDelayNodeOperations builds the two allowlist entries against
// holder and mgr. Both are nil-safe at construction, matching this
// package's identical nil-disables convention elsewhere: a node with no
// asset directory configured wires neither the audio manager nor (per
// newOperationRegistry) this holder.
func weatherDelayNodeOperations(holder *WeatherDelayHolder, mgr *audio.Manager, assetDir string) map[string]OperationFunc {
	if holder == nil {
		return nil
	}
	o := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: assetDir}
	return map[string]OperationFunc{
		"weatherdelay.start":  o.start,
		"weatherdelay.resume": o.resume,
	}
}

// start is the OperationFunc for "weatherdelay.start": params.kind is
// "delay" or "cancelNight". Never refused for a state reason — an
// already-active delay, a plan with no configured alert, or a missing
// local asset are all valid outcomes, reported in Value, not errors.
func (o *weatherDelayOperations) start(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	if err := rejectUnknownKeys("weatherdelay.start", params, weatherDelayStartKnownKeys); err != nil {
		return OperationResult{}, err
	}
	raw, ok := params["kind"]
	if !ok {
		return OperationResult{}, fmt.Errorf("weatherdelay.start: params.kind is required")
	}
	kind, ok := raw.(string)
	if !ok {
		return OperationResult{}, fmt.Errorf("weatherdelay.start: params.kind must be a string, got %T", raw)
	}
	if !weatherdelay.ValidKind(kind) {
		return OperationResult{}, fmt.Errorf("weatherdelay.start: params.kind %q must be %q or %q", kind, weatherdelay.KindDelay, weatherdelay.KindCancelNight)
	}

	executedAt := now()
	alertPlaying, alertReason := o.doStart(ctx, kind, executedAt)
	observedAt := now()

	return OperationResult{
		Confirmed: true,
		Signal:    "node.weatherdelay.start",
		Value: map[string]any{
			"kind":         kind,
			"alertPlaying": alertPlaying,
			"alertReason":  alertReason,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// doStart is weatherdelay.start's whole effect, shared verbatim with the
// signed HTTP route: it sets the holder active (persisted) BEFORE
// touching audio, so a crash between the two still leaves the node
// delayed, then runs ADR-053 decision 7's own three steps in order.
// (a) runs and completes (or times out per session, never unbounded)
// before (b) starts — see [audio.Manager.ZeroGainExcept]'s own doc
// comment for why that is still "does not wait on anything" in decision
// 7's sense. (b) is synchronous, because its outcome is this call's own
// evidence. (c) is dispatched afterward, detached from ctx (a caller's
// request context, most notably the HTTP route's, is cancelled the
// moment that caller returns, and (c) must keep running after this
// function does) and never waited on, so a session whose stop is slow or
// wedged can never delay this call's return or the alert it already
// started.
func (o *weatherDelayOperations) doStart(ctx context.Context, kind string, executedAt time.Time) (alertPlaying bool, alertReason string) {
	if err := o.holder.SetActiveLocal(kind, executedAt, "node-local"); err != nil {
		// Best effort: the in-memory holder is already active regardless
		// (SetActiveLocal updates it before attempting the write), which
		// is what every consumer actually reads. A failed persist only
		// means a restart before the next successful write would forget
		// this delay, not that the delay itself failed to take effect now.
		_ = err
	}

	if o.audioMgr != nil {
		o.audioMgr.ZeroGainExcept(ctx, weatherDelayAlertSessionID)
	}

	alertPlaying, alertReason = o.startAlert(ctx, kind)

	if o.audioMgr != nil {
		go o.audioMgr.SilenceAllExcept(context.Background(), weatherDelayAlertSessionID)
	}

	return alertPlaying, alertReason
}

// startAlert is ADR-053 decision 7's step (b): start the alert session,
// or report why nothing plays. A session already playing this exact
// alert media is left alone (idempotent: a second weatherdelay.start
// while the alert is in progress does not restart or stack it).
func (o *weatherDelayOperations) startAlert(ctx context.Context, kind string) (played bool, reason string) {
	if o.audioMgr == nil {
		return false, "no audio engine is configured on this node"
	}

	plan := o.holder.Current().Plan
	ref := weatherDelayAlertAssetForKind(plan, kind)
	if ref == nil {
		return false, fmt.Sprintf("no alert asset is configured for %q", kind)
	}
	if ref.Filename == "" {
		return false, fmt.Sprintf("the alert asset for %q has no local filename", kind)
	}

	path := filepath.Join(o.assetDir, ref.Filename)
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("the alert asset %q is not present on this node", ref.Filename)
	}

	if o.alertAlreadyPlaying(ctx, kind) {
		return true, ""
	}

	media := pkgaudio.MediaRef{AssetID: ref.AssetID, ContentHash: ref.ContentHash, SizeBytes: info.Size(), RuntimeFilename: ref.Filename}

	repeatCount := plan.RepeatCount
	if repeatCount <= 0 {
		repeatCount = weatherDelayDefaultRepeatCount
	}
	items := make([]pkgaudio.PlaylistItem, repeatCount)
	for i := range items {
		items[i] = pkgaudio.PlaylistItem{ItemID: fmt.Sprintf("%s-%d", kind, i), Index: i, Media: media}
	}
	playlist := pkgaudio.PlaylistRef{
		OwnerKind: "weatherdelay", OwnerID: kind, OwnerRevision: pkgaudio.Revision(time.Now().UnixNano()),
		Items: items, Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
	}

	applyReq := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleAnnouncement),
		Playlist:   pkgaudio.SetField(playlist),
	}
	stamp := time.Now().UnixNano()
	step := func(name string) (pkgaudio.InvocationID, pkgaudio.Revision) {
		stamp++
		return pkgaudio.InvocationID(fmt.Sprintf("weatherdelay:%s:%s:%d", kind, name, stamp)), pkgaudio.Revision(stamp)
	}

	applyInv, applyRev := step("apply")
	applyOutcome := o.audioMgr.Apply(ctx, weatherDelayAlertSessionID, applyInv, applyRev, applyReq)
	if audioOutcomeFailed(applyOutcome) {
		return false, fmt.Sprintf("the alert could not be set up (%s): %s", applyOutcome.Outcome, applyOutcome.Reason)
	}

	prepInv, prepRev := step("prepare")
	prepOutcome := o.audioMgr.Prepare(ctx, weatherDelayAlertSessionID, prepInv, prepRev)
	if audioOutcomeFailed(prepOutcome) {
		return false, fmt.Sprintf("the alert was not ready (%s): %s", prepOutcome.Outcome, prepOutcome.Reason)
	}

	startInv, startRev := step("start")
	startOutcome := o.audioMgr.Start(ctx, weatherDelayAlertSessionID, startInv, startRev)
	if audioOutcomeFailed(startOutcome) {
		return false, fmt.Sprintf("the alert did not start (%s): %s", startOutcome.Outcome, startOutcome.Reason)
	}

	o.alertMu.Lock()
	o.alertKind = kind
	o.alertMu.Unlock()
	return true, ""
}

// alertAlreadyPlaying reports whether kind's alert is already the one
// loaded into weatherDelayAlertSessionID and still Playing.
func (o *weatherDelayOperations) alertAlreadyPlaying(ctx context.Context, kind string) bool {
	o.alertMu.Lock()
	current := o.alertKind
	o.alertMu.Unlock()
	if current != kind {
		return false
	}
	for _, snap := range o.audioMgr.Snapshot(ctx) {
		if snap.ID == weatherDelayAlertSessionID && snap.State == pkgaudio.StatePlaying {
			return true
		}
	}
	return false
}

// resume is the OperationFunc for "weatherdelay.resume": stop the alert
// session and clear the holder. It restores or restarts nothing else.
func (o *weatherDelayOperations) resume(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	if err := rejectUnknownKeys("weatherdelay.resume", params, weatherDelayResumeKnownKeys); err != nil {
		return OperationResult{}, err
	}

	executedAt := now()
	o.doResume(ctx)
	observedAt := now()

	return OperationResult{
		Confirmed:  true,
		Signal:     "node.weatherdelay.resume",
		Value:      map[string]any{"active": false},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// doResume is weatherdelay.resume's whole effect. Stopping the alert
// runs before clearing the holder, so a fault stopping it never leaves
// the holder cleared while the alert is still audible.
func (o *weatherDelayOperations) doResume(ctx context.Context) {
	o.alertMu.Lock()
	o.alertKind = ""
	o.alertMu.Unlock()

	if o.audioMgr != nil {
		invocation := pkgaudio.InvocationID(fmt.Sprintf("weatherdelay:resume:%d", time.Now().UnixNano()))
		o.audioMgr.Stop(ctx, weatherDelayAlertSessionID, invocation, pkgaudio.Revision(time.Now().UnixNano()))
	}
	_ = o.holder.ClearLocal()
}
