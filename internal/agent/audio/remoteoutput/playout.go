package remoteoutput

import (
	"context"
	"errors"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// Transport is the playback transport intent a session pushes to a
// remote destination — the logical equivalent of start/pause/stop
// without any locally rendered audio.
type Transport string

const (
	TransportPlaying Transport = "playing"
	TransportPaused  Transport = "paused"
	TransportStopped Transport = "stopped"
)

// State is one logical playout snapshot: single-media or pinned-playlist
// selection, current item identity, requested item transition, source
// role, transport intent, position, loop, and gain — everything
// AUDIO-ENGINE section 8.1 lists for logical playout. It never carries
// PCM.
type State struct {
	SourceRole    pkgaudio.SourceRole
	Playlist      *pkgaudio.PlaylistRef
	Media         *pkgaudio.MediaRef
	CurrentItemID string
	CurrentIndex  int
	Transport     Transport
	Position      time.Duration

	// Seek is non-nil only on the Apply call requesting a seek, naming
	// the target position. It is never inferred from a Position change
	// between two snapshots.
	Seek *time.Duration

	Loop pkgaudio.RepeatMode
	Gain pkgaudio.Gain

	// Fade is non-nil only on the Apply call requesting a gain fade.
	Fade *pkgaudio.Fade

	// Mixing is true when this session is intended to play audibly
	// simultaneous with another session on the same destination.
	Mixing bool

	// Ducking is true while a higher-priority session is suppressing
	// this one's gain on the same destination.
	Ducking bool
}

// ErrStateHasBothMediaAndPlaylist mirrors [pkgaudio.SessionDesiredState]'s
// same rule: a playout state selects one exact asset or a playlist,
// never both.
var ErrStateHasBothMediaAndPlaylist = errors.New("remoteoutput: playout state has both a media reference and a playlist")

// Validate reports whether st is well-formed.
func (st State) Validate() error {
	if st.Media != nil && st.Playlist != nil {
		return ErrStateHasBothMediaAndPlaylist
	}
	if err := st.SourceRole.Validate(); err != nil {
		return err
	}
	if st.Playlist != nil {
		if err := st.Playlist.Validate(); err != nil {
			return err
		}
	}
	if st.Media != nil {
		if err := st.Media.Validate(); err != nil {
			return err
		}
	}
	if err := st.Loop.Validate(); err != nil {
		return err
	}
	if st.Fade != nil {
		if err := st.Fade.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Observation is a destination's report of its own playout state,
// collected at ObservedAt.
type Observation struct {
	State      State
	ObservedAt time.Time
	Result     pkgaudio.OutcomeResult
}

// PlayoutOutput drives logical playout on a remote destination. It holds
// no method capable of provisioning — see [Provisioner] — so a caller
// wired only to a PlayoutOutput cannot cause a transfer by dispatching a
// playback command.
type PlayoutOutput interface {
	// Capabilities returns the destination's currently declared
	// capability profile.
	Capabilities() Capabilities

	// Apply resolves one desired playout state against this
	// destination's revision discipline (identical anti-rewind and
	// idempotent-replay rules to [pkgaudio.RevisionState.Apply]) and, if
	// accepted, dispatches it. The returned [pkgaudio.RevisionDecision]
	// reports whether the requested revision became current; the
	// returned [Observation] reports the dispatch outcome, which is
	// [pkgaudio.OutcomeRefused] when st requires a capability this
	// destination has not confirmed [SupportSupported].
	Apply(ctx context.Context, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, st State) (pkgaudio.RevisionDecision, Observation, error)

	// Observe returns the destination's most recently confirmed playout
	// state with no side effects.
	Observe(ctx context.Context) (Observation, error)
}
