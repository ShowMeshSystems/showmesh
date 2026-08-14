package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is shared plumbing for every "showmeshctl macro"/"run"/"action"
// subcommand: the client-side request timeout floor for the macro/action/
// run surface, and the "--follow" idle-timeout loop STEP-9-SPEC.md section
// 9 requires for "macro run --follow" and "run show --follow".

// minMacroClientTimeout is this program's own floor for every request
// against the macro/action/run surface (list, show, submit, read-back).
//
// This is DELIBERATELY a much smaller number than
// [minFPPCommandClientTimeout] (35s), and the reason is structural rather
// than a smaller margin on the same wait: unlike a primitive "fpp <verb>"
// command, POST /macros/{id}/runs never holds its HTTP response open
// waiting for anything to happen on an FPP host or an MQTT broker — Step
// 9's whole design point (STEP-9-SPEC.md section 2.1, ADR-031 decision 1)
// is that a run is accepted and answered with 202 before any step ever
// dispatches; the steps themselves run in a background goroutine the HTTP
// response does not wait on
// (internal/coordinator/macro/submit.go's SubmitRun: "Start background
// execution and answer 202-shaped: the run's initial state, never a
// completed result"). So the ONLY synchronous cost this floor needs to
// cover is the coordinator's own local work at submission — resolving and
// pinning the macro and every step's action (up to the section 5.4 cap of
// 32 reads), and the CreateMacroRun+audit transaction (section 2.5) — none
// of which legitimately approaches minFPPCommandClientTimeout's 20s+15s
// server-side confirmation wait. This floor exists only to keep an
// operator's own too-aggressive --timeout from turning ordinary local
// SQLite latency into a false transport failure, not to accommodate any
// server-side hold the way the FPP primitive floor does.
//
// A SECOND, independently chosen literal from every other client-side
// timeout constant in this package — see minFPPCommandClientTimeout's own
// doc comment for why that independence is deliberate rather than an
// oversight (the import-graph rule forbids sharing pkg/command's constants
// here regardless). Reconciled against the real server's actual submission
// latency by test/integration's
// TestCLIMacroRunSubmitTimeoutFloorCoversRealSubmissionLatency, which runs
// the real coordinator and the real bench fppd together and measures how
// long a real POST /macros/{id}/runs actually takes — see that test's own
// doc comment for exactly what it proves.
//
// 5s = SHOWMESH HYPOTHESIS, NOT MEASURED at the time this constant was
// written: no bench had timed a real macro submission's local work yet.
// The reconciliation test above is what turns "hypothesis" into "measured
// with headroom to spare" for whatever coordinator this build is tested
// against; it does not by itself make this a universally safe number for
// every possible deployment (a coordinator sharing a disk with heavy
// unrelated write load could still submission longer than the bench
// exercises).
const minMacroClientTimeout = 5 * time.Second

// effectiveMacroClientTimeout is [effectiveFPPCommandTimeout]'s sibling for
// the macro/action/run surface: flagTimeout when it already meets
// [minMacroClientTimeout], the floor otherwise. Never silent about firing —
// see dispatchMacroRun/followMacroRun's own stderr notes, matching
// dispatchFPPCommand's established posture (Step 8 review finding 2).
func effectiveMacroClientTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minMacroClientTimeout {
		return minMacroClientTimeout
	}
	return flagTimeout
}

// noteMacroTimeoutFloorIfRaised prints the same kind of stderr note
// dispatchFPPCommand prints when its own floor fires, so an operator who
// passed an explicit, too-small --timeout is told why this command did not
// honor it literally, rather than left to wonder why it did not fail fast.
func noteMacroTimeoutFloorIfRaised(stderr io.Writer, cmdLabel string, flagTimeout, effective time.Duration) {
	if effective == flagTimeout {
		return
	}
	_, _ = fmt.Fprintf(stderr,
		"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s instead.\n",
		cmdLabel, flagTimeout, minMacroClientTimeout, effective)
}

// --- "--follow" idle-timeout loop (STEP-9-SPEC.md section 9) ---

// defaultMacroFollowPollInterval and defaultMacroFollowIdleTimeout are
// SHOWMESH-chosen, unmeasured defaults for "--poll-interval" and
// "--idle-timeout" — no bench evidence sizes either. 2s keeps a follow feel
// responsive without hammering the coordinator; 90s is many multiples of
// the 2s poll interval so an operator's ordinary network hiccup does not
// trip it, while still being far shorter than an operator would tolerate
// staring at a silent terminal.
const (
	defaultMacroFollowPollInterval = 2 * time.Second
	defaultMacroFollowIdleTimeout  = 90 * time.Second
)

// followMacroRun polls GET /macro-runs/{runId} until the run reaches its
// terminal state, the idle window elapses with no successful poll, or ctx
// is cancelled (Ctrl+C). It never waits past idleTimeout with no update —
// STEP-9-SPEC.md section 9: "A total timeout is forbidden... follow mode
// times out on SILENCE, not on duration." The idle deadline resets on
// EVERY successful poll, whether or not the run's own state changed
// between polls — a healthy coordinator answering "still running, nothing
// new" is itself the evidence that watching is still worthwhile, exactly
// the same fact a run event would carry on a push-based connection.
//
// This program follows by POLLING rather than by also opening the SSE
// change stream and racing the two: GET /macro-runs/{runId} already
// returns the full run AND its step detail on every call (unlike
// macroRun.changed, section 6.6: "step-level detail is not carried [on the
// stream]... a client wanting step detail fetches the run"), so a second,
// stream-based signal path would only ever reset the SAME idle deadline
// this loop already resets on its own poll, at the cost of ADR-020 decision
// 3's own reconnect/reset handling (watch.go's stream.reset and seq-gap
// machinery) duplicated here for a run that a 2-second poll already
// observes promptly. See the report for this trade-off stated explicitly.
//
// On a transport-level failure (coordinator unreachable), this loop does
// NOT give up immediately: a single dropped connection is not the run
// finishing, and Step 7's own lesson is that a client giving up early
// deletes an outcome from existence. It notes the failure once per attempt
// on stderr and keeps polling; only sustained failure for the FULL idle
// window reaches the same "stopped watching" exit as ordinary silence
// would. A DEFINITIVE answer from a healthy coordinator (404 the run does
// not exist, 401/403 authorization) is different: that is not silence, it
// is information, and is reported immediately rather than absorbed as
// "no update yet".
func followMacroRun(ctx context.Context, c *client, g *globalFlags, cmdLabel, runID string, pollInterval, idleTimeout time.Duration, stdout, stderr io.Writer, clock func() time.Time) int {
	readTimeout := effectiveMacroClientTimeout(g.timeout)
	noteMacroTimeoutFloorIfRaised(stderr, cmdLabel, g.timeout, readTimeout)

	var lastRendered string
	deadline := clock().Add(idleTimeout)

	poll := func() (macroRun, error) {
		reqCtx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()
		var resp macroRunResponse
		if err := c.getJSON(reqCtx, "/api/v1/macro-runs/"+url.PathEscape(runID), nil, &resp); err != nil {
			return macroRun{}, err
		}
		return resp.Run, nil
	}

	for {
		run, err := poll()
		now := clock()

		if err == nil {
			deadline = now.Add(idleTimeout)
			if rendered := renderMacroRunProgress(stdout, g, run); rendered != lastRendered {
				lastRendered = rendered
			}
			if run.State == "finished" {
				return exitCodeForMacroRun(run)
			}
		} else {
			var ce *cliError
			if errors.As(err, &ce) && ce.code == exitUnreachable {
				// A single dropped connection is silence, not an answer —
				// keep polling; see this function's own doc comment.
				_, _ = fmt.Fprintf(stderr, "showmeshctl %s: temporarily unable to reach the coordinator (%v); still watching\n", cmdLabel, err)
			} else {
				// A definitive, healthy-coordinator answer (404/401/403/
				// version mismatch/etc.) — report it now rather than
				// waiting out the idle window for something that will
				// never change.
				return reportError(stderr, cmdLabel, err)
			}
		}

		if now.After(deadline) {
			_, _ = fmt.Fprintf(stdout,
				"%s: no update on run %s in %s; this command stopped watching. The run may still be in progress "+
					"or may already have finished — check with \"showmeshctl run show %s\".\n",
				cmdLabel, runID, idleTimeout, runID)
			return exitFollowStillWatching
		}

		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(pollInterval):
		}
	}
}

// renderMacroRunProgress prints run's current state (JSON or text,
// per --output) and returns a fingerprint string the caller can use to
// avoid printing an unchanged state twice in text mode. JSON mode prints
// one object per poll unconditionally (each is a complete, self-describing
// snapshot a scripted consumer can line-diff itself) rather than trying to
// suppress duplicates on its behalf.
func renderMacroRunProgress(stdout io.Writer, g *globalFlags, run macroRun) string {
	if g.output == outputJSON {
		_ = printJSON(stdout, run)
		return ""
	}
	fp := macroRunFingerprint(run)
	printMacroRunProgressLine(stdout, run)
	return fp
}

// macroRunFingerprint is a cheap, order-stable string summarizing
// everything about run that would change what an operator sees on the
// terminal: its own state/completed/confirmed/reason plus every step's
// state/outcome. Two calls returning the same fingerprint mean nothing
// visible has changed since the last poll.
func macroRunFingerprint(run macroRun) string {
	fp := fmt.Sprintf("%s|%v|%v|%s", run.State, run.Completed, run.Confirmed, run.Reason)
	for _, st := range run.Steps {
		fp += fmt.Sprintf("|%s:%s:%s", st.StepID, st.State, st.Outcome)
	}
	return fp
}

// exitCodeForMacroRun maps a FINISHED run's own two booleans (STEP-9-SPEC.md
// section 2.3) to this program's exit code convention. Callers must only
// call this once run.State == "finished" — Completed/Confirmed are nil
// before then (ADR-031 decision 3), and this function's own zero-value
// reading of a nil pointer would otherwise silently report a still-running
// run as an unqualified success.
func exitCodeForMacroRun(run macroRun) int {
	if run.Completed != nil && !*run.Completed {
		return exitMacroRunAborted
	}
	if run.Confirmed != nil && !*run.Confirmed {
		return exitCommandUnconfirmed
	}
	return exitOK
}
