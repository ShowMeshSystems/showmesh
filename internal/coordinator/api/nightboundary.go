package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F3: content-anchored boundary derivation (RESTING-MODE.md
// §6.2, ADR-038 decision 3). Stored in store.NightSessionRecord's
// ContentAnchorJSON/BoundaryJSON columns, which F2's schemaV10 migration
// already created — this file is what fills them. Every function here is
// pure given its inputs; nightloop.go supplies live evidence.

// nightAnchorPurpose names which lifecycle use a content anchor serves —
// carried in the anchor so a stale anchor from a superseded purpose is
// never read as if it were the current one.
const (
	nightAnchorPurposeRestingOneShot = "resting-oneshot" // preshow, resting-intershow
	nightAnchorPurposeRestingRepeat  = "resting-repeat"  // end-of-night-resting (no boundary)
	nightAnchorPurposeShow           = "show"            // live (completion, not a boundary)
)

// nightContentAnchor is post-dispatch evidence that the intended item is
// playing, including identity and position. ObservedAt is the evidence's
// own CollectedAt, never dispatch time or wall-clock "now": an anchor
// whose ObservedAt does not postdate DispatchedAt is invalid by
// construction.
type nightContentAnchor struct {
	Purpose         string `json:"purpose"`
	FPPInstanceID   string `json:"fppInstanceId"`
	Playlist        string `json:"playlist"`
	Item            string `json:"item"`
	DurationMS      int64  `json:"durationMs"`
	PositionSeconds int64  `json:"positionSeconds"`
	// PositionMSKnown false means the ms signal was not current at the
	// anchoring observation — distinct from a genuine 0ms position.
	PositionMS      int64     `json:"positionMs"`
	PositionMSKnown bool      `json:"positionMsKnown"`
	RepeatMode      bool      `json:"repeatMode"`
	DispatchedAt    time.Time `json:"dispatchedAt"`
	ObservedAt      time.Time `json:"observedAt"`
	Source          string    `json:"source"`

	// These three describe a dispatch that never reached the wire.
	// DispatchedAt stays zero for those; FirstAttemptAt bounds the retry
	// window, AttemptedAt paces the backoff, and RefusalTerminal marks a
	// refusal retrying cannot fix.
	FirstAttemptAt  time.Time `json:"firstAttemptAt,omitempty"`
	AttemptedAt     time.Time `json:"attemptedAt,omitempty"`
	RefusalTerminal bool      `json:"refusalTerminal,omitempty"`

	// Attempts counts dispatches of a shutdown stop that were not
	// confirmed, so each retry takes its own command identity.
	Attempts int64 `json:"attempts,omitempty"`

	// DerivationInvalidAttempts counts consecutive ticks on which THIS
	// anchor was re-derived and came back invalid (nightBoundaryKindDerivation).
	// It is distinct from Attempts above (an unrelated shutdown-stop
	// counter): this one paces nightAdvanceRestingIntershow's own bounded
	// retry-from-fresh-evidence before it gives up and degrades. Reset to
	// zero on a successful (armed) re-derivation, and naturally zero on any
	// freshly dispatched anchor.
	DerivationInvalidAttempts int `json:"derivationInvalidAttempts,omitempty"`
}

// nightBoundary is the derived expected content-end time E, or the
// explicit reason it is not currently known — rule 7: missing evidence is
// "unknown", never a guess. LastTickAt is transition-to-show's own clock
// sanity checkpoint (nightloop.go's clock-jump guard); every other state
// leaves it nil.
//
// Kind classifies WHY an invalid boundary is invalid, which nightAdvanceRestingIntershow's
// own persisted-invalid check needs to decide whether a retry from fresh
// evidence is safe (nightBoundaryRetryEligible). Every boundary persisted
// before this field existed decodes with Kind empty, and so does a
// boundary this coordinator cannot classify (nightBoundaryKindUnresolvedAsset,
// below); both read as NOT eligible, the conservative default: an
// invalid boundary this coordinator cannot positively identify as a
// derivation is treated exactly like a contradiction, which is rule 3's
// own "never recompute past an invalidation" default, not a preference.
type nightBoundary struct {
	State      string     `json:"state"` // "armed" | "invalid" | "unknown"
	Reason     string     `json:"reason"`
	ExpectedAt *time.Time `json:"expectedAt,omitempty"`
	LastTickAt *time.Time `json:"lastTickAt,omitempty"`
	Kind       string     `json:"kind,omitempty"`
}

const (
	nightBoundaryStateArmed   = "armed"
	nightBoundaryStateInvalid = "invalid"
	nightBoundaryStateUnknown = "unknown"
)

const (
	// nightBoundaryKindDerivation marks an invalid boundary produced by
	// deriveNightBoundary from an anchor's own observed evidence (arithmetic
	// came back invalid, nothing contradicted anything): the only kind
	// nightBoundaryRetryEligible allows a later tick to retry.
	nightBoundaryKindDerivation = "derivation"
	// nightBoundaryKindContradiction marks an invalid boundary produced by
	// nightBoundaryContradicted (fresh evidence disagreed with an already-
	// armed boundary) or the clock-backstep check: rule 3's own
	// load-bearing invalidation, never retried automatically.
	nightBoundaryKindContradiction = "contradiction"
	// nightBoundaryKindUnresolvedAsset marks an invalid boundary written
	// when the resting FSEQ's own duration could not be resolved at all
	// (nightResolveFSEQDuration failure). This never reaches the
	// persisted-invalid retry check in practice: that branch is only
	// taken when no anchor has yet been committed for this cycle, so the
	// pairing nightBoundaryRetryEligible gates (an anchor with ObservedAt
	// set) cannot exist yet. It is stamped anyway, though, so this coordinator
	// never persists an invalid boundary it declines to classify.
	nightBoundaryKindUnresolvedAsset = "unresolved-asset"
)

// nightBoundaryRetryEligible reports whether a persisted invalid boundary
// may be retried from fresh observation rather than degrading immediately.
// Only an explicit derivation stamp is eligible; everything else, Kind
// empty included, is not. See nightBoundary's own doc comment for why
// empty must default to conservative rather than to derivation.
func nightBoundaryRetryEligible(b nightBoundary) bool {
	return b.Kind == nightBoundaryKindDerivation
}

// FPP signal names this file reads beyond the ones fppcommand_primitives.go
// already declares (fppStatusSignal, fppPlaylistNameSignal). Inlined
// string literals rather than an import of
// internal/coordinator/collector/fpp's own SignalXxx constants — the same
// deliberate decoupling fppPlaylistNameSignal's own doc comment states,
// applied to the three additional signals rule 2/3's position and repeat
// evidence need.
const (
	nightSignalSequenceName           = "fpp.sequence.name"
	nightSignalPositionElapsedSeconds = "fpp.position.elapsed.seconds"
	nightSignalPositionElapsedMS      = "fpp.position.elapsed.ms"
	nightSignalPlaylistRepeatMode     = "fpp.playlist.repeat_mode"
)

func encodeNightContentAnchor(a nightContentAnchor) string {
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeNightContentAnchor(raw string) (nightContentAnchor, bool) {
	if raw == "" {
		return nightContentAnchor{}, false
	}
	var a nightContentAnchor
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nightContentAnchor{}, false
	}
	return a, true
}

func encodeNightBoundary(b nightBoundary) string {
	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(raw)
}

func decodeNightBoundary(raw string) (nightBoundary, bool) {
	if raw == "" {
		return nightBoundary{}, false
	}
	var b nightBoundary
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nightBoundary{}, false
	}
	return b, true
}

// deriveNightBoundary is rule 1/rule 2's arithmetic: E = anchor.ObservedAt
// + (duration - position). It never guesses a boundary at "now" — a
// degenerate or contradictory input is reported invalid with a reason
// naming exactly which check failed, matching F0's own finding that
// FrameCount()==0 and StepTimeMS()==0 both collapse to duration_ms==0 and
// must be told apart at the point duration was read, not here (this
// function trusts DurationMS is already that resolved, checked value).
func deriveNightBoundary(a nightContentAnchor) nightBoundary {
	if a.ObservedAt.IsZero() || a.DispatchedAt.IsZero() {
		return nightBoundary{State: nightBoundaryStateUnknown, Reason: "no post-dispatch playback evidence has been observed yet"}
	}
	if a.ObservedAt.Before(a.DispatchedAt) {
		// Rule 2: evidence that predates dispatch can never anchor a
		// boundary, no matter how plausible it looks.
		return nightBoundary{State: nightBoundaryStateInvalid, Reason: "the only available evidence was collected before this playback was dispatched"}
	}
	if a.DurationMS <= 0 {
		return nightBoundary{State: nightBoundaryStateInvalid, Reason: "the resting FSEQ's duration is not usable (readiness must have already rejected it)"}
	}

	// Millisecond-precision position is preferred when the anchoring
	// observation had it (measured advancing in exact 50ms quanta); the
	// whole-second signal is the stated fallback otherwise.
	var positionMS int64
	var reason string
	if a.PositionMSKnown {
		positionMS = a.PositionMS
		reason = "derived from millisecond-precision position (fpp.position.elapsed.ms)"
	} else {
		positionMS = a.PositionSeconds * 1000
		reason = "derived from whole-second position; fpp.position.elapsed.ms was not available at the anchoring observation"
	}

	remainingMS := a.DurationMS - positionMS
	if remainingMS < 0 {
		return nightBoundary{State: nightBoundaryStateInvalid, Reason: "observed position is already past the asset's own duration"}
	}
	expected := a.ObservedAt.Add(time.Duration(remainingMS) * time.Millisecond)
	return nightBoundary{State: nightBoundaryStateArmed, Reason: reason, ExpectedAt: &expected}
}

// nightPlaybackObservation is one poll's worth of FPP playback evidence,
// resolved through the same [resolveConfirmationEvidence] precedence and
// currency rules every FPP command confirmation already uses — never a
// second, independently-derived reading of the same signals.
type nightPlaybackObservation struct {
	Current           bool
	CollectedAt       time.Time
	Status            string
	Playlist          string
	PlaylistCurrent   bool
	Item              string
	ItemCurrent       bool
	PositionSeconds   int64
	PositionCurrent   bool
	PositionMS        int64
	PositionMSCurrent bool
	RepeatMode        bool
	RepeatCurrent     bool
	Reason            string
}

// nightObservePlayback reads fpp.status, fpp.playlist.name,
// fpp.sequence.name, fpp.position.elapsed.seconds, and
// fpp.playlist.repeat_mode for instanceID, each independently resolved
// and currency-checked (F0 §3: three different absent/null/empty shapes
// on three fields of the same underlying response, so each signal is
// checked on its own rather than assumed to rise and fall together).
// notBefore fences out evidence that predates a dispatch this reading is
// meant to confirm; pass the zero time.Time for an ordinary correction
// poll with no dispatch to fence against.
func nightObservePlayback(ctx context.Context, lister ObservationLister, instanceID string, notBefore, now time.Time) nightPlaybackObservation {
	var out nightPlaybackObservation

	statusVal, _, statusCurrent, _, statusReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	if !statusCurrent {
		out.Reason = statusReason
		return out
	}
	statusStr, _ := statusVal.(string)
	out.Current = true
	out.Status = statusStr

	if playlistVal, _, cur, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistNameSignal, notBefore, now); cur {
		out.Playlist, _ = playlistVal.(string)
		out.PlaylistCurrent = true
	}
	if itemVal, _, cur, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, string(nightSignalSequenceName), notBefore, now); cur {
		out.Item, _ = itemVal.(string)
		out.ItemCurrent = true
	}
	if posVal, _, cur, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, string(nightSignalPositionElapsedSeconds), notBefore, now); cur {
		out.PositionSeconds, out.PositionCurrent = nightAsInt64(posVal), true
	}
	if msVal, _, cur, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, nightSignalPositionElapsedMS, notBefore, now); cur {
		out.PositionMS, out.PositionMSCurrent = nightAsInt64(msVal), true
	}
	if repVal, _, cur, _, _ := resolveConfirmationEvidence(ctx, lister, instanceID, string(nightSignalPlaylistRepeatMode), notBefore, now); cur {
		out.RepeatMode, out.RepeatCurrent = nightAsRepeatBool(repVal), true
	}

	obsCollectedAt, ok := nightResolveCollectedAt(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	if ok {
		out.CollectedAt = obsCollectedAt
	}
	return out
}

// nightResolveCollectedAt re-resolves signal purely to read the winning
// observation's own CollectedAt — [resolveConfirmationEvidence] reports
// currency and value but not this field, and the anchor needs it (rule 2:
// "every anchor carries the observation time it came from").
func nightResolveCollectedAt(ctx context.Context, lister ObservationLister, instanceID string, signal string, notBefore, now time.Time) (time.Time, bool) {
	kind := observation.ResourceFPP
	sig := observation.SignalID(signal)
	obs, err := lister.ListObservations(ctx, ObservationFilter{ResourceKind: &kind, ResourceID: &instanceID, Signal: &sig})
	if err != nil || len(obs) == 0 {
		return time.Time{}, false
	}
	var candidates []observation.Observation
	for _, o := range obs {
		if o.Resource.Kind != kind || o.Resource.ID != instanceID || o.Signal != sig {
			continue
		}
		candidates = append(candidates, o)
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}
	resolved := ResolveObservations(candidates)
	o := resolved[0]
	if o.CollectedAt.Before(notBefore) {
		return time.Time{}, false
	}
	if o.StateAt(now) != observation.StateCurrent {
		return time.Time{}, false
	}
	return o.CollectedAt, true
}

// nightAnchorEvidenceTolerance bounds how far apart the status
// observation's own CollectedAt and the position observation's own
// CollectedAt may be before an anchor refuses to combine them. Position
// ticks in FSEQ step-time quanta (F0 measured 50ms); a few seconds of
// collector-poll skew between two signals read off the same underlying
// FPP response is normal, more than that means they came from different
// polls and must not be combined into one boundary.
const nightAnchorEvidenceTolerance = 3 * time.Second

// nightResolvePositionCollectedAt resolves whichever position signal is
// current (millisecond preferred, matching deriveNightBoundary's own
// preference) and returns THAT signal's own CollectedAt, not the status
// signal's — an anchor's ObservedAt must be the position reading's own
// timestamp, since that is the instant "duration minus position" is
// actually measured at.
func nightResolvePositionCollectedAt(ctx context.Context, lister ObservationLister, instanceID string, notBefore, now time.Time) (collectedAt time.Time, ok bool) {
	if c, k := nightResolveCollectedAt(ctx, lister, instanceID, nightSignalPositionElapsedMS, notBefore, now); k {
		return c, true
	}
	return nightResolveCollectedAt(ctx, lister, instanceID, nightSignalPositionElapsedSeconds, notBefore, now)
}

func nightAsInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// nightAsRepeatBool: F0 §3's own finding, repeated here rather than
// re-derived — repeat_mode is a JSON number while playing and a JSON
// string while idle, and the FPP collector's own decoder already handles
// that; this just tolerates whichever normalized Go type reaches this
// layer (bool, float64/int64 non-zero, or the strings "1"/"true").
func nightAsRepeatBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int64:
		return t != 0
	case string:
		return t == "1" || t == "true"
	default:
		return false
	}
}

// nightBoundaryContradicted is rule 3: "A restart, seek, pause, delayed
// start, item mismatch, or a repeat-state change moves or invalidates the
// boundary" (RESTING-MODE.md §6.2). Reports true plus a reason the moment
// fresh evidence disagrees with the anchor this boundary was armed from;
// it never re-arms silently — the caller re-derives a fresh anchor only
// after seeing this.
func nightBoundaryContradicted(anchor nightContentAnchor, obs nightPlaybackObservation, now time.Time) (bool, string) {
	if !obs.Current {
		// F0 does not measure "missing evidence near the boundary" as an
		// invalidation by itself — a 15s collector gap is expected and
		// handled by the nudge/backstop poll, not by declaring the
		// boundary wrong. Rule 7 covers the caller's own display of this
		// as "unknown" separately.
		return false, ""
	}
	if obs.Status == fppStatusValueIdle {
		return nightStoppedPlaybackContradicts(anchor, now)
	}
	switch obs.Status {
	case fppStatusValuePaused:
		// F0's own "Needs real hardware" list: pause was not exercised,
		// and the deadline arithmetic (rule 3) cannot be assumed to still
		// hold while paused. Invalidate rather than guess.
		return true, "playback is paused; the armed boundary no longer reflects real elapsed time"
	case fppStatusValueUnknown:
		return true, "fpp.status reads \"unknown\"; this coordinator cannot tell whether the armed boundary still holds"
	}
	if obs.Item != "" && anchor.Item != "" && obs.Item != anchor.Item {
		return true, "a different item is now playing than the one this boundary was armed from"
	}
	if obs.Playlist != "" && anchor.Playlist != "" && obs.Playlist != anchor.Playlist {
		return true, "a different playlist is now playing than the one this boundary was armed from"
	}
	if anchor.Purpose == nightAnchorPurposeRestingOneShot && obs.RepeatCurrent && obs.RepeatMode {
		return true, "FPP reports repeat mode active for a one-shot resting item"
	}
	// Prefer millisecond-precision position when both sides have it;
	// whole-second position is the fallback, matching deriveNightBoundary's
	// own preference.
	if anchor.PositionMSKnown && obs.PositionMSCurrent {
		if obs.PositionMS < anchor.PositionMS {
			return true, "observed position moved backward; the item appears to have restarted or looped"
		}
	} else if obs.PositionCurrent && obs.PositionSeconds < anchor.PositionSeconds {
		// F0 §5: for a one-shot item, decreasing elapsed time between two
		// polls is the only signal a loop restarted — never expected here.
		return true, "observed position moved backward; the item appears to have restarted or looped"
	}
	return false, ""
}

// nightBoundaryCompletionTolerance is how close to a boundary's expected
// end an idle reading counts as that content finishing normally rather
// than stopping early. It is the same collector-skew allowance
// [nightAnchorEvidenceTolerance] applies to combining two signals, and
// deliberately not a separate operator setting.
const nightBoundaryCompletionTolerance = nightAnchorEvidenceTolerance

// nightStoppedPlaybackContradicts decides what an idle FPP means for the
// anchor it is read against. Only fpp.status is required here, unlike
// [handlers.nightAdvanceLive]'s stricter completion test: that one asserts
// a session may move forward, while this one only ever holds it, so
// erring toward "the playback is gone" launches nothing.
func nightStoppedPlaybackContradicts(anchor nightContentAnchor, now time.Time) (bool, string) {
	switch anchor.Purpose {
	case nightAnchorPurposeShow:
		// A show reaching its own end is completion, which
		// nightAdvanceLive owns, never a contradicted boundary.
		return false, ""
	case nightAnchorPurposeRestingRepeat:
		return true, "FPP is idle, but the repeating resting playlist this session started should still be running"
	}

	b := deriveNightBoundary(anchor)
	if b.State != nightBoundaryStateArmed || b.ExpectedAt == nil {
		return false, ""
	}
	if now.Before(b.ExpectedAt.Add(-nightBoundaryCompletionTolerance)) {
		return true, fmt.Sprintf(
			"FPP is idle %s before this boundary's expected end; the playback it was derived from stopped early",
			b.ExpectedAt.Sub(now).Round(time.Second))
	}
	return false, ""
}

// nightInvalidateAnchor is rule 3's own requirement that an invalidated
// boundary is load-bearing: it clears the observed half of the anchor
// (ObservedAt, item, position, repeat) so the next tick's nightEnsureAnchor
// treats it as dispatched-but-not-yet-observed and re-derives from a FRESH
// observation, never recomputes from the contradicted one. Purpose,
// instance, playlist, duration, and DispatchedAt survive unchanged: no new
// dispatch is issued, only re-observation.
func nightInvalidateAnchor(a nightContentAnchor, reason string) nightContentAnchor {
	a.ObservedAt = time.Time{}
	a.Item = ""
	a.PositionSeconds = 0
	a.PositionMS = 0
	a.PositionMSKnown = false
	a.RepeatMode = false
	a.Source = reason
	return a
}

// mapNightTransition is the wire form of this session's own content
// anchor/boundary. A dispatched-but-not-yet-observed anchor reports
// "unknown" rather than a stale or invented boundary; otherwise the
// persisted boundary's own state and reason govern, with or without an
// anchor present.
func mapNightTransition(rec store.NightSessionRecord) v1.NightPhaseEvidence {
	anchor, hasAnchor := decodeNightContentAnchor(rec.ContentAnchorJSON)
	boundary, hasBoundary := decodeNightBoundary(rec.BoundaryJSON)
	if hasAnchor && anchor.ObservedAt.IsZero() {
		reason := anchor.Source
		if reason == "" {
			reason = "playback was dispatched but is not yet confirmed by evidence"
		}
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: reason}
	}
	if !hasAnchor && !hasBoundary {
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "no content anchor is armed for the current state"}
	}
	switch {
	case hasBoundary && boundary.State == nightBoundaryStateArmed && boundary.ExpectedAt != nil:
		return v1.NightPhaseEvidence{State: v1.NightEvidenceRecorded, Reason: nightBoundaryReasonOrFallback(boundary, "boundary armed for "+boundary.ExpectedAt.Format(time.RFC3339))}
	case hasBoundary && boundary.State == nightBoundaryStateInvalid:
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: "boundary invalidated: " + nightBoundaryReasonOrFallback(boundary, "no reason was recorded")}
	case hasBoundary:
		return v1.NightPhaseEvidence{State: v1.NightEvidenceUnknown, Reason: nightBoundaryReasonOrFallback(boundary, "no reason was recorded")}
	default:
		return v1.NightPhaseEvidence{State: v1.NightEvidenceRecorded, Reason: "playback confirmed; this purpose carries no show-transition boundary"}
	}
}

// nightBoundaryReasonOrFallback never lets a blank boundary.Reason render
// as an empty string (no writer leaves it blank today, but this is a
// wire surface, not an invariant the type system enforces).
func nightBoundaryReasonOrFallback(boundary nightBoundary, fallback string) string {
	if boundary.Reason != "" {
		return boundary.Reason
	}
	return fallback
}
