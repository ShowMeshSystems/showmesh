package macro

import (
	"fmt"
	"net/http"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file builds this package's own [v1.Problem] values for the
// caller-facing refusals [Executor.SubmitRun] returns.
//
// This package cannot add a case to internal/coordinator/api's own closed
// ProblemType* constant set (api/problem.go) — that file is outside this
// builder's scope for this wave, and Wave 2's single later API-surface
// builder (STEP-9-SPEC.md section 13, "then one builder alone owns the
// routes ... v1 wire types, api/openapi.yaml") is who reconciles this
// package's problem types with that registry, api/openapi.yaml's enum,
// and any UI mapping. Three genuinely NEW conditions this step introduces
// (run already in flight; idempotency key reused for a different macro;
// idempotency key reused for the same macro at a different revision) each
// get their OWN Type URI here, minted from [api.ProblemBaseURI] itself
// rather than from a copy of that string, so they read as first-class members
// of the identical problem-document family a client already
// understands — never a generic api.ProblemTypeConflict shared across all
// three, which is exactly the "client branches on prose" defect
// LESSONS.md names. Type distinguishes WHICH of the three conflicts
// occurred, and [v1.Problem.ConflictingRunID] carries the run id all three
// point at, so no client ever recovers it by parsing Detail. That field
// did not exist when this file was first written and the run id was in the
// prose only, which is that same defect arriving on the one response whose
// entire purpose is to point at a run. Detail still names the run as well,
// for a human reading it.
//
// ServerTime is deliberately left unset on every value below, mirroring
// api/problem.go's own writeProblem doc comment ("every problem
// constructor deliberately leaves ServerTime unset ... only writeProblem
// can supply it"): whichever route eventually renders one of these problems
// on the wire is responsible for stamping it, exactly as api's own
// problem constructors already leave it to writeProblem.

// problemBaseURI is [api.ProblemBaseURI], not a second copy of the string.
// It was a copied literal when this file was written, because the api-side
// constant was unexported at the time; it has since been exported for
// exactly this consumer, so the literal is gone and the two cannot drift.
const problemBaseURI = api.ProblemBaseURI

const (
	// ProblemTypeMacroRunAlreadyInFlight is ADR-031 decision 6's overlap
	// refusal.
	ProblemTypeMacroRunAlreadyInFlight = problemBaseURI + "macro-run-already-in-flight"

	// ProblemTypeMacroRunIdempotencyMacroConflict is STEP-9-SPEC.md
	// section 6.2's "same key, different macro id" case.
	ProblemTypeMacroRunIdempotencyMacroConflict = problemBaseURI + "macro-run-idempotency-macro-conflict"

	// ProblemTypeMacroRunIdempotencyRevisionConflict is section 6.2's
	// "same key, same macro, different pinned revision" case.
	ProblemTypeMacroRunIdempotencyRevisionConflict = problemBaseURI + "macro-run-idempotency-revision-conflict"
)

func invalidParameterProblem(detail string) v1.Problem {
	return v1.Problem{
		Type:   problemBaseURI + "invalid-parameter",
		Title:  "Invalid parameter",
		Status: http.StatusBadRequest,
		Detail: detail,
	}
}

func macroNotFoundProblem(macroObjectID string) v1.Problem {
	return v1.Problem{
		Type:   problemBaseURI + "resource-not-found",
		Title:  "Resource not found",
		Status: http.StatusNotFound,
		Detail: fmt.Sprintf("no show.macro object with id %q exists", macroObjectID),
	}
}

func macroRunAlreadyInFlightProblem(e *store.MacroRunAlreadyInFlightError) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeMacroRunAlreadyInFlight,
		Title:  "Macro run refused: another run of this macro is already in flight",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"macro %q already has a run in progress (run %s). Wait for it to finish, or check its state before resubmitting.",
			e.InFlight.MacroObjectID, e.InFlight.ID),
		ConflictingRunID: e.InFlight.ID,
	}
}

func macroRunIdempotencyMacroConflictProblem(e *store.MacroRunIdempotencyMacroMismatchError) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeMacroRunIdempotencyMacroConflict,
		Title:  "Macro run refused: idempotency key already used for a different macro",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for macro %q (run %s); this request names a different macro %q. "+
				"Mint a fresh idempotencyKey for a genuinely new request.",
			e.Existing.MacroObjectID, e.Existing.ID, e.RequestedMacroObjectID),
		ConflictingRunID: e.Existing.ID,
	}
}

func macroRunIdempotencyRevisionConflictProblem(e *store.MacroRunIdempotencyRevisionMismatchError) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeMacroRunIdempotencyRevisionConflict,
		Title:  "Macro run refused: idempotency key already used for a different revision of this macro",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for macro %q at revision %d (run %s); this request would pin revision %d. "+
				"The macro was edited between the two submissions. Mint a fresh idempotencyKey for a genuinely new request.",
			e.Existing.MacroObjectID, e.Existing.MacroRevision, e.Existing.ID, e.RequestedMacroRevision),
		ConflictingRunID: e.Existing.ID,
	}
}
