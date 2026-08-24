package agent

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

func TestDecideBootResumeNoHeldCatalogClearsEverything(t *testing.T) {
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"},
	}
	decision := decideBootResume(a, heldcatalog.HeldCatalog{}, false)
	if decision.Authorized {
		t.Fatalf("decideBootResume authorized an assignment when the node holds no catalog at all")
	}
	if decision.Reason == "" {
		t.Fatalf("decideBootResume returned no reason for a stated discard")
	}
}

func TestDecideBootResumeNoTupleIsNeverGrandfathered(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{SurfaceID: "surface-1", Auth: nil}
	decision := decideBootResume(a, held, true)
	if decision.Authorized {
		t.Fatalf("decideBootResume authorized an assignment persisted with no authorization tuple; build item 4 requires it be treated as unauthorized, never grandfathered")
	}
	if decision.Reason == "" {
		t.Fatalf("decideBootResume returned no reason for a stated discard")
	}
}

func TestDecideBootResumeMismatchedShowDiscards(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "christmas-2026", Generation: 3, CatalogRevision: "rev-a"},
	}
	decision := decideBootResume(a, held, true)
	if decision.Authorized {
		t.Fatalf("decideBootResume authorized a cross-show assignment")
	}
}

func TestDecideBootResumeMismatchedGenerationDiscards(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 2, CatalogRevision: "rev-a"},
	}
	decision := decideBootResume(a, held, true)
	if decision.Authorized {
		t.Fatalf("decideBootResume authorized an assignment from a stale generation")
	}
}

func TestDecideBootResumeMismatchedCatalogRevisionDiscards(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-old"},
	}
	decision := decideBootResume(a, held, true)
	if decision.Authorized {
		t.Fatalf("decideBootResume authorized an assignment from a stale catalog revision")
	}
}

// TestDecideBootResumeSameShowSameGenerationIsResumed is the positive
// case build item 5 requires alongside every discard case: a persisted
// assignment whose tuple matches the currently held catalog exactly IS
// resumed, not discarded by accident of an overly broad check.
func TestDecideBootResumeSameShowSameGenerationIsResumed(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"},
	}
	decision := decideBootResume(a, held, true)
	if !decision.Authorized {
		t.Fatalf("decideBootResume discarded a same-show, same-generation, same-catalog-revision assignment: %s", decision.Reason)
	}
	if decision.Reason != "" {
		t.Fatalf("decideBootResume authorized an assignment but still carried a discard reason: %q", decision.Reason)
	}
}
