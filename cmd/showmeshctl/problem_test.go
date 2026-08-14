package main

import "testing"

// TestExitCodeForProblemConflict is Finding 4 (Step 8 client-side review):
// this Step 8 endpoint's 409s — originally fppStartPlaylistBusyProblem,
// fppStartPlaylistEvidenceNotCurrentProblem, fppCommandReplayConflictProblem,
// and fppCommandReplayParamsConflictProblem (internal/coordinator/api/problem.go)
// all carried the SAME ProblemTypeConflict — and exitCodeForProblem had no
// case for it and no 409 in its HTTP-status fallback, so it landed on
// exitAPIError (6) — documented in --help as "the coordinator returned some
// other error." A script cannot branch on that to distinguish "refused on
// purpose, retry with --if-busy=replace" from "the coordinator is broken."
// Broken to verify: deleting either the `case problemConflict:` arm or the
// `case 409:` fallback arm in exitCodeForProblem makes the corresponding
// half of this test fail — see this task's report.
//
// A LATER review finding (8) caught that the split naming
// fppStartPlaylistEvidenceNotCurrentProblem its own type had left
// fppStartPlaylistBusyProblem still sharing ProblemTypeConflict with the two
// idempotency-conflict constructors — opposite remedies ("mint a fresh key"
// versus "resend with ifBusy: replace"), same type. problemFPPStartPlaylistBusy
// finishes that split; this CLI still maps it to the SAME exitConflict (it
// prints `detail` verbatim rather than branching on remedy the way an
// Operator UI does), so the exit-code CONTRACT this test asserts is
// unchanged — only the explicit-case-vs-fallback path exercised differs, per
// the subtest below.
func TestExitCodeForProblemConflict(t *testing.T) {
	t.Run("by recognized type", func(t *testing.T) {
		p := &problem{Type: problemConflict, Title: "Start Playlist refused", Status: 409}
		got := exitCodeForProblem(409, p)
		if got != exitConflict {
			t.Errorf("exitCodeForProblem(409, conflict) = %d, want exitConflict (%d)", got, exitConflict)
		}
		if got == exitAPIError {
			t.Fatal("a deliberate 409 conflict must not collapse into exitAPIError (6), the generic \"coordinator " +
				"returned some other error\" code — a script cannot tell a refusal apart from a malfunction that way")
		}
	})

	// problemFPPStartPlaylistBusy's own explicit case (finding 8): proves
	// the NOW-SEPARATE type still lands on exitConflict via its own named
	// case, not merely via the status fallback below.
	t.Run("by recognized type (fpp-start-playlist-busy)", func(t *testing.T) {
		p := &problem{Type: problemFPPStartPlaylistBusy, Title: "Start Playlist refused: a different playlist is currently playing", Status: 409}
		got := exitCodeForProblem(409, p)
		if got != exitConflict {
			t.Errorf("exitCodeForProblem(409, fpp-start-playlist-busy) = %d, want exitConflict (%d)", got, exitConflict)
		}
	})

	t.Run("via status fallback when type is unrecognized", func(t *testing.T) {
		// contract §6.2 additive tolerance: an older build of this CLI
		// talking to a newer coordinator must still classify a 409 it does
		// not recognize the type URI for, exactly like the existing
		// forbidden/rate-limited fallback tests do.
		got := exitCodeForProblem(409, nil)
		if got != exitConflict {
			t.Errorf("exitCodeForProblem(409, nil) = %d, want exitConflict (%d) via status fallback", got, exitConflict)
		}
	})
}
