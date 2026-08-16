package resolume

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// TestDispatchRefusesWhenCompositionRevisionMovedSinceResolve is review
// finding 6: Arena preserves an object's own uniqueId across a rename, so
// a name resolved against one composition revision must never dispatch
// against a later revision holding a different object under that same id
// — a resolved id does not fail safe on its own against a rename-and-
// re-upload landing between resolve and dispatch.
func TestDispatchRefusesWhenCompositionRevisionMovedSinceResolve(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	compA := &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips:  []resolumecomp.Clip{{ID: "3001", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Snow"}},
	}
	var store CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, compA)
	if err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh (revision 1): %v", err)
	}

	c := newTestCollector(t, srv.URL, Options{CompositionStore: &store})
	d := NewActionDispatcher(c, ActionDispatcherOptions{})

	tc, revision, err := d.CurrentCompositionWithRevision()
	if err != nil {
		t.Fatalf("CurrentCompositionWithRevision: %v", err)
	}
	id, err := ResolveClip(tc, ClipReference{Clip: "Snow", Deck: "Main"})
	if err != nil {
		t.Fatalf("ResolveClip: %v", err)
	}

	// Same object id (3001, Arena's own uniqueId, preserved across a
	// rename), renamed to "Rain" and re-uploaded as revision 2 — landing
	// between resolve and dispatch.
	compB := &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips:  []resolumecomp.Clip{{ID: "3001", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Rain"}},
	}
	reader.setRevision(t, 2, compB)
	if err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh (revision 2): %v", err)
	}

	outcome, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: id, ResolvedAtRevision: revision})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if outcome.State != ActionRefused {
		t.Fatalf("outcome.State = %q, want %q; reason: %s", outcome.State, ActionRefused, outcome.Reason)
	}
	if !strings.Contains(outcome.Reason, "replaced") {
		t.Errorf("reason = %q, want it to say the composition was replaced", outcome.Reason)
	}
	if len(requests) != 0 {
		t.Errorf("arena received %d request(s), want 0 — the revision guard must refuse before any dispatch: %v", len(requests), requests)
	}
}

// TestDispatchAllowsUnchangedRevision proves the guard's negative case: a
// ResolvedAtRevision that still matches the store's current revision never
// produces the "replaced" refusal, so the fix does not make every
// launchClip refuse. (This composition has no survey behind it, so the
// dispatch still refuses for the identity gate's own, unrelated reason —
// this test is only about the revision guard's own outcome.)
func TestDispatchAllowsUnchangedRevision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	comp := &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips:  []resolumecomp.Clip{{ID: "3001", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Snow"}},
	}
	var store CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, comp)
	if err := store.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	c := newTestCollector(t, srv.URL, Options{CompositionStore: &store})
	d := NewActionDispatcher(c, ActionDispatcherOptions{})

	tc, revision, err := d.CurrentCompositionWithRevision()
	if err != nil {
		t.Fatalf("CurrentCompositionWithRevision: %v", err)
	}
	id, err := ResolveClip(tc, ClipReference{Clip: "Snow", Deck: "Main"})
	if err != nil {
		t.Fatalf("ResolveClip: %v", err)
	}

	outcome, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: id, ResolvedAtRevision: revision})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if strings.Contains(outcome.Reason, "replaced") {
		t.Errorf("outcome refused as replaced with an unchanged revision: %s", outcome.Reason)
	}
}
