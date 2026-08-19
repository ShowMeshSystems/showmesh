package audio

import (
	"errors"
	"testing"
	"time"
)

func TestMediaRefValidate(t *testing.T) {
	ok := MediaRef{AssetID: "asset-1", ContentHash: "sha256:abc"}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate(complete): got %v, want nil", err)
	}
	if err := (MediaRef{ContentHash: "sha256:abc"}).Validate(); !errors.Is(err, ErrMediaRefIncomplete) {
		t.Errorf("Validate(no asset id): got %v, want ErrMediaRefIncomplete", err)
	}
	if err := (MediaRef{AssetID: "asset-1"}).Validate(); !errors.Is(err, ErrMediaRefIncomplete) {
		t.Errorf("Validate(no content hash): got %v, want ErrMediaRefIncomplete", err)
	}
}

func validPlaylist() PlaylistRef {
	return PlaylistRef{
		OwnerKind:           "night_session",
		OwnerID:             "ns-1",
		OwnerRevision:       3,
		Repeat:              RepeatPlaylist,
		Resume:              ResumePolicyResume,
		RequestedTransition: ItemTransitionSequential,
		Items: []PlaylistItem{
			{ItemID: "item-1", Index: 0, Media: MediaRef{AssetID: "a1", ContentHash: "h1"}},
			{ItemID: "item-2", Index: 1, Media: MediaRef{AssetID: "a2", ContentHash: "h2"}},
		},
	}
}

func TestPlaylistRefValidate(t *testing.T) {
	if err := validPlaylist().Validate(); err != nil {
		t.Errorf("Validate(valid playlist): got %v, want nil", err)
	}

	empty := validPlaylist()
	empty.Items = nil
	if err := empty.Validate(); !errors.Is(err, ErrPlaylistRefIncomplete) {
		t.Errorf("Validate(no items): got %v, want ErrPlaylistRefIncomplete", err)
	}

	badRepeat := validPlaylist()
	badRepeat.Repeat = "forever"
	if err := badRepeat.Validate(); err == nil {
		t.Error("Validate(bad repeat mode): got nil, want error")
	}

	badItem := validPlaylist()
	badItem.Items[0].Media = MediaRef{}
	if err := badItem.Validate(); !errors.Is(err, ErrMediaRefIncomplete) {
		t.Errorf("Validate(bad item media): got %v, want ErrMediaRefIncomplete", err)
	}
}

func TestPlaylistRefValidateRejectsEmptyItemID(t *testing.T) {
	p := validPlaylist()
	p.Items[0].ItemID = ""
	if err := p.Validate(); !errors.Is(err, ErrPlaylistItemIDInvalid) {
		t.Errorf("Validate(empty item id): got %v, want ErrPlaylistItemIDInvalid", err)
	}
}

func TestPlaylistRefValidateRejectsDuplicateItemID(t *testing.T) {
	p := validPlaylist()
	p.Items[1].ItemID = p.Items[0].ItemID
	if err := p.Validate(); !errors.Is(err, ErrPlaylistItemIDInvalid) {
		t.Errorf("Validate(duplicate item id): got %v, want ErrPlaylistItemIDInvalid", err)
	}
}

func TestBookmarkResolveFailsOnRevisionMismatch(t *testing.T) {
	p := validPlaylist()
	b := Bookmark{PlaylistRevision: p.OwnerRevision + 1, ItemID: "item-1", Index: 0}
	if _, err := b.Resolve(p); !errors.Is(err, ErrBookmarkStale) {
		t.Errorf("Resolve(stale revision): got %v, want ErrBookmarkStale", err)
	}
}

// index 0 exists (as item-1) but under a different ItemID than the
// bookmark names — an index-based fallback would wrongly resolve this to
// item-1.
func TestBookmarkResolveFailsOnMissingItem(t *testing.T) {
	p := validPlaylist()
	b := Bookmark{PlaylistRevision: p.OwnerRevision, ItemID: "item-removed", Index: 0}
	if _, err := b.Resolve(p); !errors.Is(err, ErrBookmarkStale) {
		t.Errorf("Resolve(missing item id): got %v, want ErrBookmarkStale", err)
	}
}

func TestBookmarkResolveFailsOnEmptyItemID(t *testing.T) {
	p := validPlaylist()
	b := Bookmark{PlaylistRevision: p.OwnerRevision, ItemID: "", Index: 0}
	if _, err := b.Resolve(p); !errors.Is(err, ErrBookmarkStale) {
		t.Errorf("Resolve(empty item id): got %v, want ErrBookmarkStale", err)
	}
}

func TestBookmarkResolveSucceeds(t *testing.T) {
	p := validPlaylist()
	b := Bookmark{PlaylistRevision: p.OwnerRevision, ItemID: "item-2", Index: 1, Position: 12500 * time.Millisecond}
	item, err := b.Resolve(p)
	if err != nil {
		t.Fatalf("Resolve: got %v, want nil", err)
	}
	if item.ItemID != "item-2" {
		t.Errorf("Resolve: got item %q, want item-2", item.ItemID)
	}
}
