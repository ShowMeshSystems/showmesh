package api

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// This file is Step 9 wave 2 Builder A's ONLY other file inside
// internal/coordinator/api (the wave 2 shared contract section 3): it
// implements config.FPPPrimitiveRegistry over this package's own,
// pre-existing, unexported Step 8 registry (fppcommand_primitives.go,
// fppcommand_dispatch.go), so internal/coordinator/config can validate a
// show.action's fpp target against the SAME eight-primitive vocabulary
// this endpoint dispatches, without config importing api (that import
// direction is forced the other way — internal/coordinator/macro imports
// api, so api must never import macro, and config must not import api
// either, or a caller reaching config through macro would pull api in
// twice down two different paths for no reason). It must not touch the
// route table, handlers.go, config.go, or any other existing file in this
// package.

// FPPPrimitiveRegistryAdapter implements [config.FPPPrimitiveRegistry] over
// this package's fppPrimitivesByWireAction/decodeFPPCommandParams/
// FPPCommandDecision11ClassForAction/fppCommandWireActions — the identical
// registry POST /api/v1/fpp/{instanceId}/commands and the Step 9 macro
// executor's own in-process FPPCommandDispatcher (fppcommand_dispatch.go)
// both already dispatch against. It carries no state of its own: every
// method call resolves fresh against the package-level registry, so a
// primitive added to that registry is visible here automatically.
type FPPPrimitiveRegistryAdapter struct{}

// DecodeActionParams implements [config.FPPPrimitiveRegistry]. It runs the
// SAME two steps a live command dispatch runs, in the SAME order: generic
// JSON-shape decode ([decodeFPPCommandParams] — the absent/null/unknown-key
// rules), then the primitive's own value-level [fppPrimitive.ValidateParams]
// (playlist name syntax, volume range, ifBusy's two-value set). STEP-9-
// SPEC.md section 5.3 requires both to run at authoring time ("an action
// authored with a bad playlist type fails at write time rather than at
// 17:00"); fppcommand_dispatch.go's own [FPPCommandInput] doc comment
// confirms the executor still re-runs ValidateParams unconditionally at
// dispatch time as defense in depth, which means skipping it here would
// only defer a bad value's discovery, not prevent it — the point of
// running it here is catching it EARLY.
func (FPPPrimitiveRegistryAdapter) DecodeActionParams(wireAction string, raw map[string]json.RawMessage) (map[string]any, error) {
	primitive, ok := fppPrimitivesByWireAction[wireAction]
	if !ok {
		return nil, fmt.Errorf("api: %q is not a registered FPP primitive (supported: %s)", wireAction, fppQuotedWireActions())
	}

	params, problem := decodeFPPCommandParams(primitive, raw)
	if problem != nil {
		return nil, errors.New(problem.Detail)
	}

	if primitive.ValidateParams != nil {
		if err := primitive.ValidateParams(params); err != nil {
			return nil, err
		}
	}

	return params, nil
}

// Decision11Class implements [config.FPPPrimitiveRegistry] over
// [FPPCommandDecision11ClassForAction] (fppcommand_dispatch.go, wave 1's
// own addition for exactly this consumer), converting its
// [FPPCommandDecision11Class] string type to the plain string
// config.FPPPrimitiveRegistry declares — the two are the same four-member
// wire vocabulary by design (see FPPCommandDecision11Class's own doc
// comment), so this is a type conversion, never a translation that could
// drift.
func (FPPPrimitiveRegistryAdapter) Decision11Class(wireAction string) (class string, ok bool) {
	c, ok := FPPCommandDecision11ClassForAction(wireAction)
	return string(c), ok
}

// WireActions implements [config.FPPPrimitiveRegistry] over
// [fppCommandWireActions], this package's own single source of the eight
// supported wire actions (sorted). TestFPPPrimitiveRegistryAdapterWireActionsMatchesRegistry
// pins this to stay byte-for-byte [fppCommandWireActions]'s own output, "so
// a ninth primitive cannot be added to one and not the other" (wave 2
// shared contract section 3).
func (FPPPrimitiveRegistryAdapter) WireActions() []string {
	return fppCommandWireActions()
}

// FPPPrimitiveRegistry is a package-level instance of
// [FPPPrimitiveRegistryAdapter] — stateless, so one shared value is all any
// caller (Step 9's macro layer, wired by a later wave) needs, matching how
// this package already exposes stateless package-level values like
// [ErrMacroRunNotFound] in macro_seam.go rather than requiring every caller
// to construct its own.
var FPPPrimitiveRegistry config.FPPPrimitiveRegistry = FPPPrimitiveRegistryAdapter{}
