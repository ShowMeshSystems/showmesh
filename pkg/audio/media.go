package audio

import (
	"errors"
	"fmt"
	"time"
)

// MediaRef identifies one exact Track E audio asset: asset id and content
// hash together are identity (ADR-028), and RuntimeFilename is preserved
// separately and is never part of that identity.
type MediaRef struct {
	AssetID         string
	ContentHash     string
	SizeBytes       int64
	RuntimeFilename string
}

// ErrMediaRefIncomplete is returned by [MediaRef.Validate] when AssetID
// or ContentHash is empty.
var ErrMediaRefIncomplete = errors.New("audio: media reference is missing asset id or content hash")

// Validate reports whether m carries both identity fields.
func (m MediaRef) Validate() error {
	if m.AssetID == "" || m.ContentHash == "" {
		return ErrMediaRefIncomplete
	}
	return nil
}

// PlaylistItem is one pinned playlist slot: a stable item identity, its
// index, and the exact asset it resolved to at session start.
type PlaylistItem struct {
	ItemID string
	Index  int
	Media  MediaRef
}

// PlaylistRef is a pinned ordered playlist: an immutable owner kind,
// owner id, and owner revision, the ordered pinned items, repeat mode,
// resume policy, and requested item transition.
type PlaylistRef struct {
	OwnerKind           string
	OwnerID             string
	OwnerRevision       Revision
	Items               []PlaylistItem
	Repeat              RepeatMode
	Resume              ResumePolicy
	RequestedTransition ItemTransition
}

// ErrPlaylistRefIncomplete is returned by [PlaylistRef.Validate] when
// OwnerKind, OwnerID, or Items is empty.
var ErrPlaylistRefIncomplete = errors.New("audio: playlist reference is missing owner identity or items")

// ErrPlaylistItemIDInvalid is returned by [PlaylistRef.Validate] when an
// item's ItemID is empty or repeated elsewhere in the same playlist.
var ErrPlaylistItemIDInvalid = errors.New("audio: playlist item id is empty or duplicated")

// Validate reports whether p is well-formed: owner identity and at least
// one item are present, every ItemID is non-empty and unique in p,
// every item's MediaRef validates, and Repeat, Resume, and
// RequestedTransition are members of their closed vocabularies. It does
// not check output support for a required Gapless or Crossfade
// transition — see [ValidateItemTransitionSupport], which needs output
// capability information this package does not hold.
func (p PlaylistRef) Validate() error {
	if p.OwnerKind == "" || p.OwnerID == "" || len(p.Items) == 0 {
		return ErrPlaylistRefIncomplete
	}
	if err := p.Repeat.Validate(); err != nil {
		return err
	}
	if err := p.Resume.Validate(); err != nil {
		return err
	}
	if err := p.RequestedTransition.Validate(); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(p.Items))
	for i, item := range p.Items {
		if item.ItemID == "" {
			return fmt.Errorf("%w: item %d has an empty id", ErrPlaylistItemIDInvalid, i)
		}
		if _, dup := seen[item.ItemID]; dup {
			return fmt.Errorf("%w: item %d repeats id %q", ErrPlaylistItemIDInvalid, i, item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
		if err := item.Media.Validate(); err != nil {
			return fmt.Errorf("audio: playlist item %d (%s): %w", i, item.ItemID, err)
		}
	}
	return nil
}

// Bookmark pins a resume position: playlist revision, item identity,
// index, and position. Identity pins the exact asset content behind
// ItemID (ADR-028 identity, "itemID|assetID|contentHash") — required for
// a media (non-playlist) session, whose ItemID is always the constant
// "media" and so cannot by itself distinguish one Apply'd asset from
// another.
type Bookmark struct {
	PlaylistRevision Revision
	ItemID           string
	Identity         string
	Index            int
	Position         time.Duration
}

// ErrBookmarkStale is returned by [Bookmark.Resolve] when the bookmark's
// item id is empty, its revision no longer matches p, or its item no
// longer exists in p.
var ErrBookmarkStale = errors.New("audio: bookmark references a playlist revision or item that no longer exists")

// Resolve looks up b's item in p by ItemID after confirming p's own
// revision still matches b.PlaylistRevision. An empty item id, a
// revision mismatch, or a missing item id is [ErrBookmarkStale], never
// resolved by falling back to b.Index or a filename match.
func (b Bookmark) Resolve(p PlaylistRef) (PlaylistItem, error) {
	if b.ItemID == "" {
		return PlaylistItem{}, fmt.Errorf("%w: empty item id", ErrBookmarkStale)
	}
	if p.OwnerRevision != b.PlaylistRevision {
		return PlaylistItem{}, fmt.Errorf("%w: playlist is at revision %d, bookmark wants %d", ErrBookmarkStale, p.OwnerRevision, b.PlaylistRevision)
	}
	for _, item := range p.Items {
		if item.ItemID == b.ItemID {
			return item, nil
		}
	}
	return PlaylistItem{}, fmt.Errorf("%w: item %q not found", ErrBookmarkStale, b.ItemID)
}
