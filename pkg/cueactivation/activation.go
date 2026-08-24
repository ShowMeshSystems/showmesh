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
