package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// This file is ADR-053 decision 7's node-side alert sequence: "weatherdelay.
// start", node-scoped like audionodesilenceops.go's "audio.node.silence",
// no sessionId, no revision, idempotent, never refused for state reasons,
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
// "delay" or "cancelNight". Never refused for a state reason, an
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

// doStart sets the holder active, mutes every other session within a
// bound, starts the alert, then stops every other session in the
// background, detached from ctx and never waited on.
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
		return false, "This node has no audio engine. Configure one to play the weather alert."
	}

	plan := o.holder.Current().Plan
	ref := weatherDelayAlertAssetForKind(plan, kind)
	if ref == nil {
		return false, fmt.Sprintf("No alert sound is set for %s. Set one to play an alert.", kind)
	}
	if ref.Filename == "" {
		return false, fmt.Sprintf("The alert sound for %s has no file name. Set the alert sound again.", kind)
	}

	path := filepath.Join(o.assetDir, ref.Filename)
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("The alert sound %s is not on this node. Send it to this node to play it.", ref.Filename)
	}

	o.holder.alertStartMu.Lock()
	defer o.holder.alertStartMu.Unlock()
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
		return false, fmt.Sprintf("The alert could not be set up. Check this node's audio: %s", applyOutcome.Reason)
	}

	prepInv, prepRev := step("prepare")
	prepOutcome := o.audioMgr.Prepare(ctx, weatherDelayAlertSessionID, prepInv, prepRev)
	if audioOutcomeFailed(prepOutcome) {
		return false, fmt.Sprintf("The alert could not be loaded. Check this node's audio: %s", prepOutcome.Reason)
	}

	startInv, startRev := step("start")
	startOutcome := o.audioMgr.Start(ctx, weatherDelayAlertSessionID, startInv, startRev)
	if audioOutcomeFailed(startOutcome) {
		return false, fmt.Sprintf("The alert did not start. Check this node's audio: %s", startOutcome.Reason)
	}

	o.holder.alertMu.Lock()
	o.holder.alertKind = kind
	o.holder.alertMu.Unlock()
	return true, ""
}

// alertAlreadyPlaying reports whether kind's alert is already the one
// loaded into weatherDelayAlertSessionID and still Playing.
func (o *weatherDelayOperations) alertAlreadyPlaying(ctx context.Context, kind string) bool {
	o.holder.alertMu.Lock()
	current := o.holder.alertKind
	o.holder.alertMu.Unlock()
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
	o.holder.alertMu.Lock()
	o.holder.alertKind = ""
	o.holder.alertMu.Unlock()

	if o.audioMgr != nil {
		invocation := pkgaudio.InvocationID(fmt.Sprintf("weatherdelay:resume:%d", time.Now().UnixNano()))
		o.audioMgr.Stop(ctx, weatherDelayAlertSessionID, invocation, pkgaudio.Revision(time.Now().UnixNano()))
	}
	_ = o.holder.ClearLocal()
}

// weatherDelayRefusedOperations are the coordinator commands that can
// start output, each with the reason it is refused while a delay is active.
var weatherDelayRefusedOperations = map[string]string{
	string(pkgaudio.OperationSessionStart):  "A weather delay is active. Resume the show to start audio.",
	string(pkgaudio.OperationSessionResume): "A weather delay is active. Resume the show to start audio.",
	"render.surface.apply":                  "A weather delay is active. Resume the show to send output to a surface.",
}

// refuseWhileWeatherDelayActive wraps op so it is refused, without
// running, whenever holder reports an active delay.
func refuseWhileWeatherDelayActive(holder *WeatherDelayHolder, reason string, op OperationFunc) OperationFunc {
	return func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		if holder.Current().Active {
			return OperationResult{}, errors.New(reason)
		}
		return op(ctx, params, now)
	}
}
