package resolume_test

// This file is acceptance criterion 8's own proof: resolve is callable with
// only a *TrackedComposition — no collector, no dispatcher, and no server.
// It is a black-box (resolume_test) package deliberately, importing only
// resolume and pkg/resolumecomp: neither *Collector, *ActionDispatcher, nor
// net/http appears anywhere in this file, which is what makes this a
// structural proof rather than a claim about what references.go merely
// happens not to call today.

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

func TestResolveCallableWithOnlyATrackedComposition(t *testing.T) {
	comp := &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips:  []resolumecomp.Clip{{ID: "201", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Snow"}},
	}
	tc, err := resolume.BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	if _, err := resolume.ResolveDeck(tc, "Main"); err != nil {
		t.Errorf("ResolveDeck: %v", err)
	}
	if _, err := resolume.ResolveLayer(tc, "Layer A"); err != nil {
		t.Errorf("ResolveLayer: %v", err)
	}
	if _, err := resolume.ResolveClip(tc, resolume.ClipReference{Clip: "Snow", Deck: "Main"}); err != nil {
		t.Errorf("ResolveClip: %v", err)
	}
}
