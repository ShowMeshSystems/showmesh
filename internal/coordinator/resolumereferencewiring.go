package coordinator

// This file joins internal/coordinator/config's ResolumeReferenceResolver
// interface (declared at the consumer, config/showaction.go — Track D seam
// C, ADR-037) to internal/coordinator/collector/resolume's own
// TrackedComposition and name-resolution functions (references.go), the
// identical adapter role resolumeactionwiring.go already plays for
// api.ResolumeActionDispatcher one seam over. config must not import
// resolume for its own decode/resolve logic (showaction.go's own top
// comment), so this package — which already imports both — is where the
// two meet.
//
// Deliberately built over *resolume.CompositionStore alone, never a
// *resolume.Collector: write-time show.action validation must work
// whether or not a live Resolume instance is configured at all, exactly
// as resolumeCompositionWiring itself is constructed unconditionally in
// coordinator.go (composition upload has no relationship to whether a
// live Resolume instance exists — see that type's own doc comment).

import (
	"errors"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// resolumeReferenceResolverAdapter implements
// [config.ResolumeReferenceResolver] over a *resolume.CompositionStore.
// Every method's own job ends at translating one name-resolution call and
// translating resolume.ErrCompositionNotUploaded to config's own sentinel
// at this boundary (config must not depend on resolume's own error
// vocabulary — see [config.ErrResolumeCompositionNotUploaded]'s own doc
// comment); the resolution logic itself is entirely
// internal/coordinator/collector/resolume's, read here, never
// reimplemented.
type resolumeReferenceResolverAdapter struct {
	store *resolume.CompositionStore
}

func newResolumeReferenceResolverAdapter(store *resolume.CompositionStore) resolumeReferenceResolverAdapter {
	return resolumeReferenceResolverAdapter{store: store}
}

var _ config.ResolumeReferenceResolver = resolumeReferenceResolverAdapter{}

// current reads the tracked composition, translating
// resolume.ErrCompositionNotUploaded to config's own sentinel.
func (a resolumeReferenceResolverAdapter) current() (*resolume.TrackedComposition, error) {
	tc, err := a.store.Current()
	if errors.Is(err, resolume.ErrCompositionNotUploaded) {
		return nil, config.ErrResolumeCompositionNotUploaded
	}
	return tc, err
}

func (a resolumeReferenceResolverAdapter) ResolveClip(ref config.ResolumeClipReference) error {
	tc, err := a.current()
	if err != nil {
		return err
	}
	_, err = resolume.ResolveClip(tc, resolume.ClipReference{
		Clip: ref.Clip, Deck: ref.Deck, Persistent: ref.Persistent, Layer: ref.Layer,
	})
	return err
}

func (a resolumeReferenceResolverAdapter) ResolveLayer(name string) error {
	tc, err := a.current()
	if err != nil {
		return err
	}
	_, err = resolume.ResolveLayer(tc, name)
	return err
}

func (a resolumeReferenceResolverAdapter) ResolveColumn(deck, column string) error {
	tc, err := a.current()
	if err != nil {
		return err
	}
	_, err = resolume.ResolveColumn(tc, deck, column)
	return err
}

func (a resolumeReferenceResolverAdapter) ResolveDeck(name string) error {
	tc, err := a.current()
	if err != nil {
		return err
	}
	_, err = resolume.ResolveDeck(tc, name)
	return err
}
