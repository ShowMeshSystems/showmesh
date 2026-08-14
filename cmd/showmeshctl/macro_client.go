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
//
// A CANCELLED ctx (Ctrl+C / SIGTERM, via signal.NotifyContext in
// cmd_macro.go/cmd_run.go) is a THIRD case, distinct from both of the
// above, and is checked before either: cancelling ctx aborts poll()'s
// own in-flight request too, which classifyRequestError reports as
// "could not reach coordinator" — indistinguishable, by error value alone,
// from an actually dead coordinator. Printing that "temporarily unable to
// reach the coordinator" note for an operator's own Ctrl+C would tell them
// their coordinator just went unreachable at the exact moment they asked
// this program to stop, which is false and alarming. So ctx.Err() is
// checked FIRST: when it is non-nil, this loop prints nothing about the
// poll's own error at all and falls straight through to the ctx.Done()
// case below, which is where the actual clean exit (exitOK) already lives.
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

		switch {
		case err == nil:
			deadline = now.Add(idleTimeout)
			lastRendered = renderMacroRunProgress(stdout, g, run, lastRendered)
			if run.State == "finished" {
				return exitCodeForMacroRun(run)
			}

		case ctx.Err() != nil:
			// Ctrl+C/SIGTERM cancelled ctx, which is what made poll()
			// fail — not a genuine transport problem. Say nothing here;
			// the ctx.Done() case below is this loop's actual, silent,
			// exitOK exit for this. See this function's own doc comment.

		default:
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
			printMacroRunFollowIdleNotice(stdout, g, cmdLabel, runID, idleTimeout)
			return exitFollowStillWatching
		}

		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(pollInterval):
		}
	}
}

// macroRunFollowIdleNotice is --output json's own shape for the terminal
// "stopped watching" notice below — see printMacroRunFollowIdleNotice's
// doc comment for why this exists as a distinct type rather than reusing
// the text-mode sentence.
type macroRunFollowIdleNotice struct {
	Event       string `json:"event"`
	RunID       string `json:"runId"`
	IdleTimeout string `json:"idleTimeout"`
	Message     string `json:"message"`
}

// printMacroRunFollowIdleNotice prints the "this command stopped
// watching" terminal message that fires when followMacroRun's idle window
// elapses. In --output json mode this is a JSON object on stdout, not the
// human sentence: a "showmeshctl macro run --follow --output json <id> |
// jq" pipeline is exactly the case STEP-9-SPEC.md section 9's whole
// --follow design exists to keep clean, and a prose line landing on stdout
// at the one moment this loop legitimately stops watching would break
// that pipeline's JSON parsing at precisely the point a script most needs
// a clean, parseable signal that watching ended without a definite
// outcome.
func printMacroRunFollowIdleNotice(stdout io.Writer, g *globalFlags, cmdLabel, runID string, idleTimeout time.Duration) {
	msg := fmt.Sprintf(
		"%s: no update on run %s in %s; this command stopped watching. The run may still be in progress "+
			"or may already have finished — check with \"showmeshctl run show %s\".",
		cmdLabel, runID, idleTimeout, runID)
	if g.output == outputJSON {
		_ = printJSONCompact(stdout, macroRunFollowIdleNotice{
			Event: "idleTimeout", RunID: runID, IdleTimeout: idleTimeout.String(), Message: msg,
		})
		return
	}
	_, _ = fmt.Fprintln(stdout, msg)
}

// renderMacroRunProgress prints run's current state (JSON or text, per
// --output) and returns the fingerprint of what this call rendered, which
// the caller feeds back in as lastRendered on the next poll.
//
// JSON mode prints one object per poll UNCONDITIONALLY — each is a
// complete, self-describing snapshot a scripted consumer can line-diff
// itself, per this function's own established doc comment — and uses
// printJSONCompact (one line, no indent) rather than [printJSON]'s
// multi-line pretty form: a follow stream is many objects concatenated
// over time, and a multi-line encoding of each one is not line-diffable
// at all, which was this function's own claim about itself and was false
// until this fix.
//
// TEXT mode is genuinely deduplicated: run is rendered, and lastRendered
// is updated, ONLY when its fingerprint differs from lastRendered passed
// in. A prior version of this function always printed and returned a
// fingerprint the CALLER then compared to its own separate lastRendered
// variable and then did nothing with the result — an unchanging run
// printed once per poll regardless, measured at 11 identical lines for 11
// polls of one unchanging run (roughly 180 identical lines for a six
// minute run at the shipped defaults). Text mode is deduplicated (unlike
// JSON) because it exists for a human watching a terminal, who benefits
// from silence meaning "nothing changed"; a scripted JSON consumer wants
// the opposite — a steady heartbeat proving the loop is still alive.
func renderMacroRunProgress(stdout io.Writer, g *globalFlags, run macroRun, lastRendered string) string {
	if g.output == outputJSON {
		_ = printJSONCompact(stdout, run)
		return lastRendered
	}
	fp := macroRunFingerprint(run)
	if fp == lastRendered {
		return lastRendered
	}
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
// while a run is still running (ADR-031 decision 3), and that "still
// running" case is not this function's business at all.
//
// A FINISHED run whose Completed or Confirmed pointer is STILL nil is a
// fourth, distinct case this function must not collapse into either
// neighbor: reading a nil pointer's zero value as false would report
// "the coordinator never told us" as the same outright failure exit
// (exitMacroRunAborted) as a run the coordinator explicitly said aborted —
// exactly the "absent evidence asserted as a negative" defect this
// project's LESSONS.md keeps finding in new disguises. There is no exit
// code of its own for "finished but the coordinator didn't say" (the task
// this shipped under deliberately reuses one from the existing scheme
// rather than adding a 13th): exitAPIError is chosen because it is this
// program's own established catch-all for "a response this program
// received and could parse, but does not trust to render a definite
// verdict from" (see reportError's doc comment) — never exitOK, which
// would silently report an unknown outcome as an unqualified success, and
// never exitMacroRunAborted/exitCommandUnconfirmed, both of which assert a
// SPECIFIC negative fact this run's response never actually stated.
func exitCodeForMacroRun(run macroRun) int {
	if run.Completed == nil || run.Confirmed == nil {
		return exitAPIError
	}
	if !*run.Completed {
		return exitMacroRunAborted
	}
	if !*run.Confirmed {
		return exitCommandUnconfirmed
	}
	return exitOK
}
