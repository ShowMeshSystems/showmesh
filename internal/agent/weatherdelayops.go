package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// weatherdelay.start and weatherdelay.resume. The MQTT command, the retained
// state and the signed HTTP start all run the start sequence here.

// weatherDelayAlertSessionID is delay's own audio session, kept as the
// pre-existing name since most tests exercise only that kind; cancelNight
// gets its own session from weatherDelayAlertSessionIDForKind so an Apply
// for one kind never replaces a playlist the audio manager already has
// loaded and playing for the other (see weatherDelayAlertSessionIDForKind's
// own doc comment for why that matters).
const weatherDelayAlertSessionID = pkgaudio.SessionID(weatherDelayAlertSessionPrefix + weatherdelay.KindDelay)

// weatherDelayLegacyAlertSessionID is the single alert session id an agent
// older than the per-kind sessions persisted. Matched at boot as well as
// the per-kind prefix, so an in-place upgrade never restores it playing.
const weatherDelayLegacyAlertSessionID = pkgaudio.SessionID("weatherdelay:alert")

// weatherDelayAlertSessionPrefix is the common prefix every weather delay
// alert session id shares, so a caller that only needs to recognize "some
// weather delay alert session" (never one kind specifically) can match on
// it, e.g. a background engine call log filtered by handle prefix.
const weatherDelayAlertSessionPrefix = "weatherdelay:alert:"

// weatherDelayAlertSessionIDForKind is the one audio session kind's alert
// plays on, so a second start of the same kind finds the alert rather than
// stacking one. Each kind gets its own, because an Apply over a playlist
// the manager already has loaded keeps the stale item index, the defect the
// audio package's own skipped test records: starting one kind mutes and
// stops the other kind's session, never replaces it. That defect still
// truncates a second alert of the same kind after a resume, recorded by
// TestWeatherDelaySecondAlertOfTheNightRestartsFromItemZero.
func weatherDelayAlertSessionIDForKind(kind string) pkgaudio.SessionID {
	return pkgaudio.SessionID(weatherDelayAlertSessionPrefix + kind)
}

// weatherDelayDefaultRepeatCount applies when the plan leaves RepeatCount unset.
const weatherDelayDefaultRepeatCount = 10

// weatherDelayMuteBound is the longest the alert waits for other sessions to
// be muted. A session not muted by then is reported and still stopped.
var weatherDelayMuteBound = 500 * time.Millisecond

var weatherDelayStartKnownKeys = map[string]bool{"kind": true, "plan": true}
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
	// The plan travels in this command's own params too, not only on the
	// retained state topic, so this node still knows what to play even if
	// it never received that retained message.
	if raw, ok := params["plan"]; ok {
		plan, err := decodeWeatherDelayPlanParam(raw)
		if err != nil {
			return OperationResult{}, fmt.Errorf("weatherdelay.start: params.plan: %w", err)
		}
		// An empty plan means the coordinator could not build one, not
		// that there is no alert: overwriting with it would throw away a
		// plan this node already had and silence the alert.
		if plan.Delay != nil || plan.CancelNight != nil {
			if err := o.holder.SetPlanLocal(plan); err != nil {
				o.holder.log().Warn("weatherdelay.start: failed to persist the plan carried in the command", "error", err)
			}
		}
	}

	executedAt := now()
	heldKind, alertPlaying, alertReason, unsilenced := o.startHeld(ctx, kind, executedAt)
	observedAt := now()

	value := map[string]any{
		"kind":         heldKind,
		"alertPlaying": alertPlaying,
		"alertReason":  alertReason,
		"unsilenced":   unsilenced,
	}
	if heldKind != kind {
		value["message"] = weatherDelayAlreadyCancelledMessage
	}
	return OperationResult{
		Confirmed:  true,
		Signal:     "node.weatherdelay.start",
		Value:      value,
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// weatherDelayAlreadyCancelledMessage is reported when a delay start finds
// the night already cancelled; the cancel alert keeps playing.
const weatherDelayAlreadyCancelledMessage = "The night is already cancelled, so the cancel alert keeps playing. Resume the show to clear it."

// decodeWeatherDelayPlanParam round-trips params.plan (already decoded by
// the command envelope into a generic map[string]any) through JSON into a
// validated [mqttproto.WeatherDelayPlan].
func decodeWeatherDelayPlanParam(raw any) (mqttproto.WeatherDelayPlan, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return mqttproto.WeatherDelayPlan{}, fmt.Errorf("encode: %w", err)
	}
	var plan mqttproto.WeatherDelayPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return mqttproto.WeatherDelayPlan{}, fmt.Errorf("decode: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return mqttproto.WeatherDelayPlan{}, err
	}
	return plan, nil
}

// doStart marks the holder active and runs the start sequence.
func (o *weatherDelayOperations) doStart(ctx context.Context, kind string, executedAt time.Time) (alertPlaying bool, alertReason string, unsilenced []string) {
	_, alertPlaying, alertReason, unsilenced = o.startHeld(ctx, kind, executedAt)
	return alertPlaying, alertReason, unsilenced
}

// startHeld is doStart that also returns the kind the node now holds, which
// stays cancelNight when a delay start arrives on a cancelled night.
func (o *weatherDelayOperations) startHeld(ctx context.Context, kind string, executedAt time.Time) (heldKind string, alertPlaying bool, alertReason string, unsilenced []string) {
	heldKind, _ = o.holder.setActiveLocal(kind, executedAt, "node-local")
	alertPlaying, alertReason, unsilenced = o.runStartSequence(ctx, heldKind)
	return heldKind, alertPlaying, alertReason, unsilenced
}

// weatherDelayOtherKind is the alert kind that is not kind.
func weatherDelayOtherKind(kind string) string {
	if kind == weatherdelay.KindCancelNight {
		return weatherdelay.KindDelay
	}
	return weatherdelay.KindCancelNight
}

// runStartSequence mutes every other session within weatherDelayMuteBound,
// starts the alert, stops the other kind's alert, then stops every
// remaining session in the background.
//
// The whole sequence is serialized and re-reads the kind the node holds, so
// two deliveries of different kinds racing each other both end on the newer
// kind's alert. The background pass leaves BOTH alert sessions alone,
// because it outlives the lock and must never be able to stop an alert a
// newer start has since begun.
func (o *weatherDelayOperations) runStartSequence(ctx context.Context, kind string) (alertPlaying bool, alertReason string, unsilenced []string) {
	o.holder.alertStartMu.Lock()
	defer o.holder.alertStartMu.Unlock()

	if held := o.holder.Current().Kind; held != "" {
		kind = held
	}
	own := weatherDelayAlertSessionIDForKind(kind)
	other := weatherDelayAlertSessionIDForKind(weatherDelayOtherKind(kind))
	if o.audioMgr != nil {
		for _, id := range o.audioMgr.ZeroGainExcept(ctx, own, weatherDelayMuteBound) {
			unsilenced = append(unsilenced, fmt.Sprintf("Audio session %s could not be silenced before the alert started.", id))
		}
	}

	alertPlaying, alertReason = o.startAlertLocked(ctx, kind)

	if o.audioMgr != nil {
		o.audioMgr.SilenceSession(ctx, other)
		weatherDelayBackground.Add(1)
		go func() {
			defer weatherDelayBackground.Done()
			o.audioMgr.SilenceAllExcept(context.Background(), own, other)
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

// startAlertLocked starts kind's own alert session, or reports why nothing
// plays. It does nothing when kind's alert is already playing or the delay
// has cleared. The other kind's session, if any, is left to
// runStartSequence, which holds alertStartMu for this call.
func (o *weatherDelayOperations) startAlertLocked(ctx context.Context, kind string) (played bool, reason string) {
	if o.audioMgr == nil {
		return false, "This node has no audio engine. Configure one to play the weather alert."
	}
	own := weatherDelayAlertSessionIDForKind(kind)

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

	if !o.holder.Current().Active {
		return false, "The weather delay ended before the alert started. Start it again to play the alert."
	}
	if o.holder.startedAlertKind == kind {
		// Read from the session, never from the kind alone: an emergency
		// stop can have silenced this alert since it started, and a repeat
		// of the same start must report that rather than restart it.
		return o.alertSessionPlayingLocked(ctx, own), ""
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
	applyOutcome := o.audioMgr.Apply(ctx, own, applyInv, applyRev, applyReq)
	if audioOutcomeFailed(applyOutcome) {
		return false, fmt.Sprintf("The alert could not be set up. Check this node's audio: %s", applyOutcome.Reason)
	}

	prepInv, prepRev := step("prepare")
	prepOutcome := o.audioMgr.Prepare(ctx, own, prepInv, prepRev)
	if audioOutcomeFailed(prepOutcome) {
		return false, fmt.Sprintf("The alert could not be loaded. Check this node's audio: %s", prepOutcome.Reason)
	}

	startInv, startRev := step("start")
	startOutcome := o.audioMgr.Start(ctx, own, startInv, startRev)
	if audioOutcomeFailed(startOutcome) {
		return false, fmt.Sprintf("The alert did not start. Check this node's audio: %s", startOutcome.Reason)
	}
	o.holder.startedAlertKind = kind
	return true, ""
}

// alertSessionPlayingLocked reports whether id is playing right now. The
// caller holds alertStartMu.
func (o *weatherDelayOperations) alertSessionPlayingLocked(ctx context.Context, id pkgaudio.SessionID) bool {
	for _, snap := range o.audioMgr.Snapshot(ctx) {
		if snap.ID == id {
			return snap.State == pkgaudio.StatePlaying
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

// stopAlert stops both kinds' alert sessions whatever state each is in, and
// does nothing for a session that has none.
func (o *weatherDelayOperations) stopAlert(ctx context.Context) {
	o.holder.alertStartMu.Lock()
	defer o.holder.alertStartMu.Unlock()
	o.holder.startedAlertKind = ""
	if o.audioMgr != nil {
		o.audioMgr.SilenceSession(ctx, weatherDelayAlertSessionIDForKind(weatherdelay.KindDelay))
		o.audioMgr.SilenceSession(ctx, weatherDelayAlertSessionIDForKind(weatherdelay.KindCancelNight))
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
// either kind's alert (and an older agent's single alert session) stopped on
// disk, and every session during a delay, so nothing from before the delay
// plays now or on a later boot.
func restoreAudioSessionsAtBoot(ctx context.Context, mgr *audio.Manager, delayActive bool, logger *slog.Logger) {
	match := func(id pkgaudio.SessionID) bool {
		return id == weatherDelayLegacyAlertSessionID || strings.HasPrefix(string(id), weatherDelayAlertSessionPrefix)
	}
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
