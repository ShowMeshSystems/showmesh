package audio

import "errors"

// SessionDesiredState is a session's merged desired state. A nil field
// means "not currently set."
type SessionDesiredState struct {
	SourceRole *SourceRole
	Playlist   *PlaylistRef
	Media      *MediaRef
	Gain       *Gain
	Ceiling    *Ceiling
	Fade       *Fade
	MixPolicy  *MixPolicy
	Outputs    *[]string
	Bookmark   *Bookmark
}

// ErrSessionHasBothMediaAndPlaylist is returned by
// [SessionDesiredState.Validate] when both Media and Playlist are set: a
// session source is either one exact asset or an ordered playlist, never
// both.
var ErrSessionHasBothMediaAndPlaylist = errors.New("audio: session desired state has both a media reference and a playlist")

// Validate reports whether s is well-formed: Media and Playlist are not
// both set, and every other non-nil field validates by its own Validate
// method (Bookmark has none, so a non-nil Bookmark's ItemID must be
// non-empty directly).
func (s SessionDesiredState) Validate() error {
	if s.Media != nil && s.Playlist != nil {
		return ErrSessionHasBothMediaAndPlaylist
	}
	if s.SourceRole != nil {
		if err := s.SourceRole.Validate(); err != nil {
			return err
		}
	}
	if s.Playlist != nil {
		if err := s.Playlist.Validate(); err != nil {
			return err
		}
	}
	if s.Gain != nil {
		if err := s.Gain.Validate(); err != nil {
			return err
		}
	}
	if s.Ceiling != nil {
		if err := s.Ceiling.Validate(); err != nil {
			return err
		}
	}
	if s.Fade != nil {
		if err := s.Fade.Validate(); err != nil {
			return err
		}
	}
	if s.MixPolicy != nil {
		if err := s.MixPolicy.Validate(); err != nil {
			return err
		}
	}
	if s.Bookmark != nil && s.Bookmark.ItemID == "" {
		return ErrBookmarkStale
	}
	return nil
}

// ApplyRequest is one audio.session.apply payload's mutable fields, each
// expressed as a [Field] so omitted, explicitly null, and provided are
// three distinguishable states.
type ApplyRequest struct {
	SourceRole Field[SourceRole]
	Playlist   Field[PlaylistRef]
	Media      Field[MediaRef]
	Gain       Field[Gain]
	Ceiling    Field[Ceiling]
	Fade       Field[Fade]
	MixPolicy  Field[MixPolicy]
	Outputs    Field[[]string]
	Bookmark   Field[Bookmark]
}

// mergeField resolves one Field against a session's current *T: unset
// leaves current untouched, null clears it to nil, and set replaces it
// with a pointer to the provided value.
func mergeField[T any](current *T, f Field[T]) *T {
	switch f.State() {
	case FieldUnset:
		return current
	case FieldNull:
		return nil
	default:
		v, _ := f.Value()
		return &v
	}
}

// mergePlaylist is [mergeField] for PlaylistRef, deep-copying Items so a
// caller reusing its own slice cannot mutate a pinned playlist in place.
func mergePlaylist(current *PlaylistRef, f Field[PlaylistRef]) *PlaylistRef {
	p := mergeField(current, f)
	if p == nil || f.State() != FieldSet {
		return p
	}
	items := make([]PlaylistItem, len(p.Items))
	copy(items, p.Items)
	p.Items = items
	return p
}

// mergeOutputs is [mergeField] for the output set, deep-copying the
// slice for the same reason as [mergePlaylist].
func mergeOutputs(current *[]string, f Field[[]string]) *[]string {
	o := mergeField(current, f)
	if o == nil || f.State() != FieldSet {
		return o
	}
	out := make([]string, len(*o))
	copy(out, *o)
	return &out
}

// MergeReport carries evidence about what [ApplyRequest.Merge] did
// beyond field replacement: the ceiling clamp outcome, present whenever
// a gain and a ceiling are both in effect after the merge.
type MergeReport struct {
	Ceiling *CeilingResult
}

// Merge applies r onto s and returns the resulting state and its report.
// s is not mutated. On error the returned state and report are zero
// values and must not be used: Merge validates the result (rejecting a
// state with both Media and Playlist set) and, when a gain and a
// ceiling are both in effect, clamps the gain to the ceiling via
// [ApplyCeiling] and records the outcome in the report rather than
// storing an unclamped or unreported value.
func (r ApplyRequest) Merge(s SessionDesiredState) (SessionDesiredState, MergeReport, error) {
	s.SourceRole = mergeField(s.SourceRole, r.SourceRole)
	s.Playlist = mergePlaylist(s.Playlist, r.Playlist)
	s.Media = mergeField(s.Media, r.Media)
	s.Gain = mergeField(s.Gain, r.Gain)
	s.Ceiling = mergeField(s.Ceiling, r.Ceiling)
	s.Fade = mergeField(s.Fade, r.Fade)
	s.MixPolicy = mergeField(s.MixPolicy, r.MixPolicy)
	s.Outputs = mergeOutputs(s.Outputs, r.Outputs)
	s.Bookmark = mergeField(s.Bookmark, r.Bookmark)

	if err := s.Validate(); err != nil {
		return SessionDesiredState{}, MergeReport{}, err
	}

	var report MergeReport
	if s.Gain != nil && s.Ceiling != nil {
		result, err := ApplyCeiling(*s.Gain, *s.Ceiling)
		if err != nil {
			return SessionDesiredState{}, MergeReport{}, err
		}
		report.Ceiling = &result
		if result.Clamped {
			effective := result.Effective
			s.Gain = &effective
		}
	}

	return s, report, nil
}
