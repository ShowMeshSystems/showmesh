// Package cueactivation is TRACK-H-cues-and-playlists.md section H4's one
// runner-neutral Cue activation envelope. It carries no HTTP, no MQTT
// wiring, no store access, and nothing FPP-specific: [Activation] is the
// one shape "fpp" and "showmesh-audio" runners, and every node-side
// consumer (rendering, audio, LTC), build against, so a redelivery or a
// reconnect can always be re-applied as full state rather than as a
// relative adjustment. This package lives under pkg/, not internal/agent,
// for the identical reason pkg/cueauth and pkg/cuecatalog do (see either
// package's own doc comment): internal/agent must never import
// internal/coordinator or vice versa, and a runner-neutral envelope is
// exactly the kind of shape both a runner (external to this repository)
// and this repository's node agent need to agree on byte-for-byte.
package cueactivation

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/cueauth"
)

// Activation is one runner's full-state statement of which Cue is active
// and where it is. Full state, never a delta: a redelivery or a reconnect
// re-applies it safely, which a relative adjustment would not (H4's own
// framing). Runner is "fpp" or "showmesh-audio"; RunnerInstance names
// which FPP instance or which runner process reported it. ActivationID is
// stable per activation and is this envelope's own idempotency identity —
// distinct from, but conventionally carried as, the MQTT command
// envelope's own IdempotencyKey (see internal/agent/command.go's
// idempotencyCache, which is what actually enforces exactly-once execution
// for a redelivered "cue.activate" command).
//
// Show, Generation, CatalogRevision, CueID, and CueRevision are
// TRACK-H-H3-SPEC.md section 5's authorization tuple, projected via
// [Activation.Tuple]. Playlist, PlaylistRevision, and EntryID are present
// only when a Playlist (rather than a directly activated announcement)
// selected this Cue — EntryID is empty for a directly activated
// announcement, per section 5's own "absent for a directly activated
// announcement" rule.
type Activation struct {
	Runner           string    `json:"runner"`
	RunnerInstance   string    `json:"runnerInstance"`
	ActivationID     string    `json:"activationId"`
	Show             string    `json:"show"`
	Generation       int64     `json:"generation"`
	CatalogRevision  string    `json:"catalogRevision"`
	Playlist         string    `json:"playlist,omitempty"`
	PlaylistRevision int64     `json:"playlistRevision,omitempty"`
	EntryID          string    `json:"entryId,omitempty"`
	CueID            string    `json:"cueId"`
	CueRevision      int64     `json:"cueRevision"`
	PositionMS       int64     `json:"positionMs"`
	EvidenceAt       time.Time `json:"evidenceAt"`
}

// Tuple projects a's authorization fields into [cueauth.AuthorizationTuple]
// so there is one authorization check ([cueauth.Check] /
// [cueauth.CheckLazy]), not two independently-written comparisons of the
// same five fields.
func (a Activation) Tuple() cueauth.AuthorizationTuple {
	return cueauth.AuthorizationTuple{
		Show:            a.Show,
		Generation:      a.Generation,
		CatalogRevision: a.CatalogRevision,
		CueID:           a.CueID,
		CueRevision:     a.CueRevision,
	}
}

// Validate reports whether a carries every field a checking side's
// authorization tuple and idempotency identity actually need. It does not,
// and cannot, validate that a's tuple is AUTHORIZED — that is
// [cueauth.Check]'s job once a is decoded, against whatever the checking
// side currently holds.
func (a Activation) Validate() error {
	if a.Runner == "" {
		return fmt.Errorf("cueactivation: runner is required")
	}
	if a.RunnerInstance == "" {
		return fmt.Errorf("cueactivation: runnerInstance is required")
	}
	if a.ActivationID == "" {
		return fmt.Errorf("cueactivation: activationId is required")
	}
	if a.Show == "" {
		return fmt.Errorf("cueactivation: show is required")
	}
	if a.Generation < 1 {
		return fmt.Errorf("cueactivation: generation must be a positive integer, got %d", a.Generation)
	}
	if a.CatalogRevision == "" {
		return fmt.Errorf("cueactivation: catalogRevision is required")
	}
	if a.CueID == "" {
		return fmt.Errorf("cueactivation: cueId is required")
	}
	if a.CueRevision < 1 {
		return fmt.Errorf("cueactivation: cueRevision must be a positive integer, got %d", a.CueRevision)
	}
	if a.PositionMS < 0 {
		return fmt.Errorf("cueactivation: positionMs must not be negative, got %d", a.PositionMS)
	}
	if a.EvidenceAt.IsZero() {
		return fmt.Errorf("cueactivation: evidenceAt is required")
	}
	return nil
}

// DecodeParams decodes an agent operation's params map (as delivered over
// MQTT — see mqttproto.CmdPayload.Params, a map[string]any produced by
// decoding JSON) into an Activation, via json.Marshal+json.Unmarshal —
// matching this codebase's standing convention for a nested wire shape
// that does not scale to field-by-field type assertions (see
// internal/agent/cuecatalogops.go's catalogDeployWireParams, the identical
// pattern one seam earlier). The returned Activation is decoded only; call
// [Activation.Validate] separately before trusting it.
func DecodeParams(params map[string]any) (Activation, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Activation{}, fmt.Errorf("cueactivation: encoding params: %w", err)
	}
	var a Activation
	if err := json.Unmarshal(raw, &a); err != nil {
		return Activation{}, fmt.Errorf("cueactivation: decoding params: %w", err)
	}
	return a, nil
}

// AudioSessionID is the one audio session a Cue activation's audio output
// runs in. It lives here, in the package both sides already import, because
// the coordinator dispatches audio.session.stop against this id (the
// blackAndSilence mismatch policy) while the node creates the session under
// it: two independently declared copies of the string would compile, drift,
// and leave blackAndSilence stopping a session that does not exist, which is
// silently not silencing at exactly the moment an operator chose that policy
// to guarantee silence.
const AudioSessionID = "cue-activation:show"

// BackgroundSessionID is the one audio session the showmesh-audio
// runner's background Playlist runs in (TRACK-H-cues-and-playlists.md
// section H5, ruling 3): [pkgaudio.SourceRoleBackground].
// Declared here, beside [AudioSessionID], for the identical reason that
// constant lives here rather than being independently declared on each
// side of the wire — the coordinator resolves and Applies a
// show.playlist's pkgaudio.PlaylistRef against this id, and the node's
// own Manager.RunWatcher advances whatever session is Playing under it,
// so a second, independently declared copy of this string would compile,
// drift, and leave the coordinator applying to a session the node's own
// watcher never observes as the one it just started.
const BackgroundSessionID = "cue-activation:background"

// AnnouncementSessionID is the one audio session a directly-activated
// announcement Cue runs in (TRACK-H-cues-and-playlists.md section H5 ruling 3):
// [pkgaudio.SourceRoleAnnouncement]. An announcement Cue's EntryID is
// empty (H3 spec section 5's "absent for a directly activated
// announcement" rule, projected onto [Activation.EntryID] above) — this
// id is what a coordinator dispatch and a node's own activateAudio agree
// names the one announcement session, independent of which Cue most
// recently activated into it.
const AnnouncementSessionID = "cue-activation:announcement"

// Ordered steps a Cue activation's audio session moves through on the node
// (Apply, Prepare, Start, Seek — internal/agent/cueactivationaudio.go's
// activateAudio, in that order), plus the one step the coordinator itself
// ever drives directly against the SAME session: Stop, H0.2's
// blackAndSilence policy (internal/coordinator/api/cueactivationloop.go's
// dispatchBlackAndSilenceAudioStop). Numbered as one closed, ordered set —
// not two independently chosen ranges — specifically so [AudioSessionRevision]
// called with the identical timestamp from both sides can never collide:
// AudioSessionStepStop sorts after every activation step, so a stop
// dispatched in the same wall-clock nanosecond as the activation it is
// reacting to still outranks it.
const (
	AudioSessionStepApply = iota
	AudioSessionStepPrepare
	AudioSessionStepStart
	AudioSessionStepSeek
	AudioSessionStepStop
)

// AudioSessionRevision derives [AudioSessionID]'s own pkg/audio.Revision-
// shaped uint64 from t (a real wall-clock reading) and step (one of the
// AudioSessionStep* constants above). Both sides that drive this session —
// the node's own activateAudio steps, keyed off the Activation's own
// EvidenceAt, and the coordinator's blackAndSilence audio.session.stop —
// MUST derive their revision through this one function, never two
// independently written copies of the same rule: internal/agent/
// cueactivationaudio.go used to multiply t.UnixNano() by 4 while
// internal/coordinator/api/cueactivationloop.go dispatched the stop as a
// bare t.UnixNano() with no multiplier, so the node's own session was
// already, permanently past the coordinator's derived revision the instant
// the first Cue activated — pkg/audio's RevisionState.Apply refuses
// anything not strictly greater than the session's current revision
// (pkg/audio/identity.go), so the stop was refused as stale every time,
// and blackAndSilence's audio half never touched the engine.
//
// Unifying the rule fixed the multiplier defect but left a second one: t
// itself is read from two DIFFERENT clocks. The node's activateAudio steps
// pass act.EvidenceAt, a reading taken on the FPP player; the coordinator's
// blackAndSilence stop cannot pass its own "now" directly, because an FPP
// player is a Raspberry Pi with no real-time clock and no guaranteed
// internet — it can boot with a badly wrong clock, ahead of the
// coordinator's — and this function has no way to compare two callers'
// readings for it. A stop computed from the coordinator's now alone would
// then be smaller than the session's own current revision and get refused
// as stale for the life of the session, the identical symptom the
// multiplier defect produced, arriving through clock skew instead.
// internal/coordinator/api/cueactivationloop.go's
// dispatchBlackAndSilenceAudioStop handles this OUTSIDE this function, by
// never trusting its own now alone: it reads back the EvidenceAt of the
// last cue.activate this coordinator itself dispatched to the node (the
// commands table, not the node's clock) and passes the LATER of that
// reading and its own now. AudioSessionStepStop still sorts after every
// activation step, so an identical timestamp — the ordinary case, where
// the coordinator's clock is caught up or ahead — still yields a strictly
// greater revision the ordinary way; the skew case is what the LATER-of
// comparison exists for.
//
// step is kept as a small, single-digit ADDITIVE offset rather than a
// multiplier deliberately: POST
// /nodes/{nodeId}/audio/sessions/{sessionId}/stop takes a plain,
// caller-supplied revision most naturally also a raw nanosecond reading
// (matching every other revision this codebase derives — see this
// function's own doc comment above), and a multiplied revision inflates
// the session's current revision to several times a real nanosecond value,
// permanently out of reach of an unmultiplied operator-supplied one for
// the life of the session. Staying additive keeps this derivation's
// magnitude within a handful of nanoseconds of t itself, so a later real
// moment — an operator's own "now" included — still produces a strictly
// larger revision the ordinary way.
func AudioSessionRevision(t time.Time, step int) uint64 {
	return uint64(t.UnixNano()) + uint64(step)
}
