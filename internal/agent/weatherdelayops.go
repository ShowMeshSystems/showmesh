package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// weatherdelay.start and weatherdelay.resume. The MQTT command, the retained
// state and the signed HTTP start all run the start sequence here.

// weatherDelayAlertSessionID is the one audio session a weather delay alert
// plays on, so a second start finds the alert rather than stacking one.
const weatherDelayAlertSessionID = pkgaudio.SessionID("weatherdelay:alert")

// weatherDelayDefaultRepeatCount applies when the plan leaves RepeatCount unset.
const weatherDelayDefaultRepeatCount = 10

// weatherDelayMuteBound is the longest the alert waits for other sessions to
// be muted. A session not muted by then is reported and still stopped.
var weatherDelayMuteBound = 500 * time.Millisecond

var weatherDelayStartKnownKeys = map[string]bool{"kind": true}
var weatherDelayResumeKnownKeys = map[string]bool{}

// weatherDelayBackground counts the stop passes still running after a start
// sequence returned, so a test can wait for them before its directory goes.
var weatherDelayBackground sync.WaitGroup

type weatherDelayOperations struct {
	holder   *WeatherDelayHolder
	audioMgr *audio.Manager
	assetDir string
}

// weatherDelayNodeOperations builds the two allowlist entries, or none when
// holder is nil.
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

// start is weatherdelay.start: params.kind is "delay" or "cancelNight". It is
// never refused for a state reason; a missing alert is reported in Value.
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
	alertPlaying, alertReason, unsilenced := o.doStart(ctx, kind, executedAt)
	observedAt := now()

	return OperationResult{
		Confirmed: true,
		Signal:    "node.weatherdelay.start",
		Value: map[string]any{
			"kind":         kind,
			"alertPlaying": alertPlaying,
			"alertReason":  alertReason,
			"unsilenced":   unsilenced,
		},
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// doStart marks the holder active and runs the start sequence.
func (o *weatherDelayOperations) doStart(ctx context.Context, kind string, executedAt time.Time) (alertPlaying bool, alertReason string, unsilenced []string) {
	_ = o.holder.SetActiveLocal(kind, executedAt, "node-local")
	return o.runStartSequence(ctx, kind)
}

// runStartSequence mutes every other session within weatherDelayMuteBound,
// starts the alert, then stops every other session in the background. The
// returned messages name sessions that were not muted before the alert.
func (o *weatherDelayOperations) runStartSequence(ctx context.Context, kind string) (alertPlaying bool, alertReason string, unsilenced []string) {
	if o.audioMgr != nil {
		for _, id := range o.audioMgr.ZeroGainExcept(ctx, weatherDelayAlertSessionID, weatherDelayMuteBound) {
			unsilenced = append(unsilenced, fmt.Sprintf("Audio session %s could not be silenced before the alert started.", id))
		}
	}

	alertPlaying, alertReason = o.startAlert(ctx, kind)

	if o.audioMgr != nil {
		weatherDelayBackground.Add(1)
		go func() {
			defer weatherDelayBackground.Done()
			o.audioMgr.SilenceAllExcept(context.Background(), weatherDelayAlertSessionID)
		}()
	}
	return alertPlaying, alertReason, unsilenced
}

// react runs the audio side of a state message: a start runs the start
// sequence and a clear stops the alert.
func (o *weatherDelayOperations) react(ctx context.Context, transition weatherDelayTransition, kind string) {
	switch transition {
	case weatherDelayStarted, weatherDelayKindChanged:
		playing, reason, unsilenced := o.runStartSequence(ctx, kind)
		o.holder.log().Warn("weather delay started from the coordinator state topic", "kind", kind, "alert_playing", playing, "alert_reason", reason, "unsilenced", unsilenced)
	case weatherDelayCleared:
		o.stopAlert(ctx)
	}
}

// startAlert starts the alert session, or reports why nothing plays. It does
// nothing when this kind's alert is already playing or the delay has cleared.
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
	if !o.holder.Current().Active {
		return false, "The weather delay ended before the alert started. Start it again to play the alert."
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

// alertAlreadyPlaying reports whether kind's alert is loaded and playing.
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

// resume is weatherdelay.resume: clear the delay and stop the alert. It
// restores nothing else and succeeds when no alert exists.
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

// doResume clears the holder before stopping the alert, so a start racing
// the resume finds the delay cleared and cannot restart the alert.
func (o *weatherDelayOperations) doResume(ctx context.Context) {
	_ = o.holder.ClearLocal()
	o.stopAlert(ctx)
}

// stopAlert stops the alert session whatever state it is in, and does
// nothing when there is none.
func (o *weatherDelayOperations) stopAlert(ctx context.Context) {
	o.holder.alertStartMu.Lock()
	defer o.holder.alertStartMu.Unlock()
	o.holder.alertMu.Lock()
	o.holder.alertKind = ""
	o.holder.alertMu.Unlock()
	if o.audioMgr != nil {
		o.audioMgr.SilenceSession(ctx, weatherDelayAlertSessionID)
	}
}

// weatherDelayRefusedOperations are the coordinator commands that can
// start output, each with the reason it is refused while a delay is active.
var weatherDelayRefusedOperations = map[string]string{
	string(pkgaudio.OperationSessionStart):  "A weather delay is active. Resume the show to start audio.",
	string(pkgaudio.OperationSessionResume): "A weather delay is active. Resume the show to start audio.",
	"render.surface.apply":                  "A weather delay is active. Resume the show to send output to a surface.",
}

// refuseWhileWeatherDelayActive refuses op without running it during a delay.
func refuseWhileWeatherDelayActive(holder *WeatherDelayHolder, reason string, op OperationFunc) OperationFunc {
	return func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		if holder.Current().Active {
			return OperationResult{}, errors.New(reason)
		}
		return op(ctx, params, now)
	}
}

// restoreAudioSessionsAtBoot restores persisted audio sessions, first marking
// the alert stopped on disk, and every session during a delay, so nothing from
// before the delay plays now or on a later boot.
func restoreAudioSessionsAtBoot(ctx context.Context, mgr *audio.Manager, delayActive bool, logger *slog.Logger) {
	match := func(id pkgaudio.SessionID) bool { return id == weatherDelayAlertSessionID }
	if delayActive {
		match = func(pkgaudio.SessionID) bool { return true }
	}
	if err := mgr.StopPersisted(match); err != nil {
		logger.Error("failed to mark persisted audio sessions stopped at startup", "delay_active", delayActive, "error", err)
		if delayActive {
			logger.Warn("skipping persisted audio session restore at startup: a weather delay is active")
			return
		}
	}
	if err := mgr.RestoreAll(ctx); err != nil {
		logger.Warn("failed to restore persisted audio sessions at startup", "error", err)
	}
}
