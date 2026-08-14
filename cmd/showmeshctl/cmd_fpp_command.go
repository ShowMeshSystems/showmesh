package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// This file is Step 8's own shared plumbing. Step 7 seam C built the
// request/response core for exactly one "showmeshctl fpp <verb>" write
// subcommand (stop-playlist, cmd_fpp_stop_playlist.go). Step 8 adds seven
// more (start-playlist, stop-playlist-gracefully, pause-playlist,
// resume-playlist, next-playlist-item, prev-playlist-item, set-volume) —
// docs/bench/fpp-command-vocabulary.md section 4's full eight-primitive
// registry — and every one of them dispatches exactly one action against
// POST /api/v1/fpp/{instanceId}/commands and reports its outcome under the
// identical honesty rules ADR-003 and ADR-020 require. That plumbing lived
// entirely inside cmd_fpp_stop_playlist.go through Step 7; it moved here,
// generalized, so the other seven subcommand files are each a flag
// definition plus a params map, not eight independent copies of this
// file's own history. cmd_fpp_stop_playlist.go's own behavior, wire shape,
// and exit codes are UNCHANGED by this move — see that file's own doc
// comment.

// minFPPCommandClientTimeout is EVERY "fpp <verb>" write subcommand's own
// minimum request budget, overriding --timeout's global default (10s) when
// it is smaller. This is Step 7 seam C review defect 1's fix
// (cmd_fpp_stop_playlist.go's original, narrower version of this same
// constant was named minStopPlaylistClientTimeout): the coordinator's own
// default confirmation deadline (pkg/command.DefaultFPPCommandConfirmDeadline,
// 20s) already exceeds --timeout's 10s default, so a command-dispatch
// subcommand — unlike a plain snapshot read, which really is fast — must
// never abort via context deadline before the coordinator can answer
// "unconfirmed" with a reason. Generalized to cover all eight primitives
// because docs/bench/fpp-command-vocabulary.md section 4's registry gives
// every one of them the identical confirmation-deadline base
// (internal/coordinator/api/fppcommand_primitives.go's
// fppConfirmDeadlineUnchanged, and pkg/command.MaxFPPCommandConfirmDeadline's
// own doc comment: "today this equals DefaultFPPCommandConfirmDeadline,
// because Step 8's own registry gives every primitive the identical
// ConfirmDeadline function"). There is exactly one deadline today, not
// eight, so exactly one client-side minimum is correct here too.
//
// This is a SECOND, independently chosen literal — this program
// deliberately does not import pkg/command for its own minting/decoding
// (see importgraph_test.go's forbiddenImports comment), so it cannot be
// DERIVED from pkg/command.MinClientTimeoutForConfirmation(
// pkg/command.MaxFPPCommandConfirmDeadline) the way
// internal/coordinator/api's own server-side deadline now is. It is
// instead RECONCILED against the server's real value the only way two
// independent literals safely can be: test/integration's
// TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline runs the real
// coordinator (with its real default) and the real showmeshctl binary
// together and fails if this value is ever too small for that default
// again — a unit test comparing two hand-copied literals could not catch
// the two silently disagreeing, only that one can.
//
// 35s = MaxFPPCommandConfirmDeadline's own value today (20s) + a 15s
// margin for the round trip itself (SHOWMESH HYPOTHESIS, NOT MEASURED —
// mirrors pkg/command.ClientTimeoutMargin's identical reasoning and value,
// chosen independently here for the reason above).
const minFPPCommandClientTimeout = 35 * time.Second

// effectiveFPPCommandTimeout returns the request budget an "fpp <verb>"
// write subcommand actually uses: flagTimeout (the --timeout flag) when it
// is already at least [minFPPCommandClientTimeout], and
// minFPPCommandClientTimeout otherwise. An operator who explicitly asks for
// MORE than the minimum (e.g. a slow or high-latency link) is still
// honored; only a budget too small to ever observe the server's own
// confirmation deadline is raised — matching
// cmd_fpp_stop_playlist.go's own pre-existing, already-tested behavior
// exactly (see TestCmdFPPStopPlaylistSurvivesAResponseSlowerThanTheExplicitTimeoutFlag,
// which asserts an explicit --timeout well below this floor still
// succeeds rather than being refused outright as a usage error): the
// too-small VALUE is refused as the effective budget and replaced, but the
// command itself is not refused, matching precedent this task was told
// not to diverge from ("check what the existing command does and be
// consistent").
//
// The raise itself is NOT silent, as it was through Step 8's own review
// (finding 2): [dispatchFPPCommand] prints a stderr note naming both the
// requested value and the floor it was raised to whenever this function
// actually changes flagTimeout, because an operator who explicitly passed
// a too-small --timeout and then waits out the full floor with no
// explanation reads that silence as "my flag was honored and the
// coordinator is pathologically slow" — the opposite of the truth (the
// coordinator holds an unresolved command open for its own confirmation
// deadline regardless of what this client asked for). See
// TestDispatchFPPCommandNotesTimeoutFloorOnStderr and
// TestDispatchFPPCommandSaysNothingWhenTimeoutFlagAlreadyMeetsTheFloor.
func effectiveFPPCommandTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minFPPCommandClientTimeout {
		return minFPPCommandClientTimeout
	}
	return flagTimeout
}

// newIdempotencyKey mints a fresh, random idempotency key: 16 bytes of
// crypto/rand, hex-encoded. Deliberately NOT pkg/command.NewIdempotencyKey
// — see types.go's fppCommandRequest doc comment and importgraph_test.go's
// forbiddenImports comment for why this program mints its own value
// independently rather than importing the coordinator's shared package for
// it. Shared by every "fpp <verb>" write subcommand: each mints its OWN
// key per invocation, never reusing one across two calls — see
// fppCommandRequest.IdempotencyKey's own doc comment for why reuse is a
// footgun this function exists to keep a caller from stepping on by
// accident.
func newIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", newCLIError(exitAPIError, "generating an idempotency key: %v", err)
	}
	return hex.EncodeToString(buf), nil
}

// dispatchFPPCommand is the request/response core shared by every
// "showmeshctl fpp <verb>" write subcommand: build a client on this
// command's own [minFPPCommandClientTimeout]-floored budget, mint a fresh
// idempotency key, POST {action, idempotencyKey, params} to
// /api/v1/fpp/{instanceId}/commands, and report the outcome per
// [reportFPPCommandResult]'s own honesty rules.
//
// params is nil for a zero-parameter primitive (stopPlaylist,
// pausePlaylist, resumePlaylist, nextPlaylistItem, prevPlaylistItem):
// fppCommandRequest.Params carries a "params,omitempty" tag, so a nil map
// encodes as an OMITTED "params" key on the wire — never "null", never an
// explicit "{}" — matching docs/bench/fpp-command-vocabulary.md section 2's
// own absent/null/empty distinction, applied here to this program's
// OUTBOUND request rather than the coordinator's inbound decode.
func dispatchFPPCommand(stdout, stderr io.Writer, clock func() time.Time, g *globalFlags, cmdLabel, instanceID, action string, params map[string]any) int {
	timeout := effectiveFPPCommandTimeout(g.timeout)
	// Finding 2 (Step 8 client-side review): the floor above is silent —
	// it raises too-small a --timeout to minFPPCommandClientTimeout
	// without saying so, so an operator running e.g. "--timeout 200ms"
	// waits out the full 35s and is told nothing, and reads that silence
	// as "my flag was honored and the coordinator is pathologically
	// slow" — the opposite of the truth. The floor itself stays (it is
	// what makes "unconfirmed" observable at all against a coordinator
	// that legitimately holds a confirmation response for its own
	// deadline), but it must never be silent about having fired.
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s "+
				"instead. The coordinator holds an unresolved command's response open for its full confirmation "+
				"deadline before answering, so a shorter client budget can only ever produce a false "+
				"transport-timeout failure for a healthy, still-working conversation — never a genuinely faster "+
				"answer.\n",
			cmdLabel, g.timeout, minFPPCommandClientTimeout, timeout)
	}
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp fppCommandResponse
	reqErr := c.postJSON(ctx, "/api/v1/fpp/"+url.PathEscape(instanceID)+"/commands",
		fppCommandRequest{Action: action, IdempotencyKey: key, Params: params}, &resp)
	if reqErr != nil {
		// Covers every 4xx/5xx this endpoint can answer generically,
		// including the 409s docs/bench/fpp-command-vocabulary.md section
		// 5 and fppcommand_primitives.go's ValidateParams/PreDispatchCheck
		// produce (a replayed key reused with different params or a
		// different target, and startPlaylist's own ifBusy=refuse guard):
		// client.go's decodeProblemError already surfaces the RFC 9457
		// problem's title AND detail verbatim (problem.go's fppStartPlaylist*
		// and fppCommandReplay*ConflictProblem constructors put what is
		// actually playing, and which params differed, into that detail
		// text), so reportError below prints exactly what the operator
		// needs — including "how to override" (ifBusy=replace) and which
		// parameters differed — without this CLI inventing its own second
		// copy of that wording.
		return reportError(stderr, cmdLabel, reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForCommandResult(resp.Command)
	}
	return reportFPPCommandResult(stdout, stderr, cmdLabel, resp.Command)
}

// reportFPPCommandResult prints result's outcome honestly to stdout and
// returns the exit code it maps to — exitOK only for a genuinely
// "confirmed" outcome, [exitCommandUnconfirmed] for "unconfirmed", never
// the reverse. This is where ADR-003 actually gets enforced at this
// program's own boundary: it would be trivial (and wrong) to treat any 2xx
// HTTP response as success and print "OK" — this function's entire job is
// to refuse that shortcut. cmdLabel is this invocation's own name (e.g.
// "fpp stop-playlist", "fpp start-playlist"), used exactly where
// reportError already uses it, so stderr diagnostics stay consistent
// across every subcommand in this package.
//
// outcomeReason is surfaced on stdout whenever the server sent one — not
// only on "unconfirmed". This matters specifically for
// stopPlaylistGracefully (docs/bench/fpp-command-vocabulary.md section
// 3.3/4): its own predicate can answer "confirmed" while FPP has only
// entered a stopping state and the show is still running, and its
// outcomeReason says so explicitly ("FPP accepted the graceful stop and
// the show is winding down, but has NOT stopped yet"). Printing a bare
// "confirmed" for that case, or substituting a cheerier CLI-invented
// summary in its place, would let an operator read the show as stopped
// when it is not — this function never omits or replaces the reason
// string the server went to the trouble of writing, on a confirmed
// outcome or otherwise.
func reportFPPCommandResult(stdout, stderr io.Writer, cmdLabel string, result fppCommandResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", cmdLabel, result.ID)
	}
	if result.AttributionDegraded {
		_, _ = fmt.Fprintf(stderr, "showmeshctl %s: WARNING: the coordinator's audit write "+
			"failed for this command; it proceeded anyway (ADR-024 decision 11's safety class) with degraded "+
			"attribution recorded only to its own stderr\n", cmdLabel)
	}

	switch result.Outcome {
	case "confirmed":
		if result.OutcomeReason != "" {
			_, _ = fmt.Fprintf(stdout, "confirmed: %s on %s (command %s): %s\n",
				result.Action, result.InstanceID, result.ID, result.OutcomeReason)
		} else {
			_, _ = fmt.Fprintf(stdout, "confirmed: %s on %s (command %s)\n",
				result.Action, result.InstanceID, result.ID)
		}
		return exitOK
	case "unconfirmed":
		_, _ = fmt.Fprintf(stdout, "unconfirmed: %s on %s: %s (command %s)\n",
			result.Action, result.InstanceID, result.OutcomeReason, result.ID)
		return exitCommandUnconfirmed
	default:
		// Empty outcome: the one accepted race a REPLAY response can
		// return (v1.FPPCommandResult.Outcome's own doc comment) — the
		// original request's own dispatch/confirmation had not finished
		// when this replay was answered. Honestly reported, never
		// printed as either "confirmed" or "unconfirmed" it did not
		// actually claim.
		_, _ = fmt.Fprintf(stdout, "pending: %s on %s: command %s has not yet resolved (state %s)\n",
			result.Action, result.InstanceID, result.ID, result.OutcomeState)
		return exitCommandUnconfirmed
	}
}

// exitCodeForCommandResult maps a decoded command result to this program's
// exit code convention, for --output json's caller (which prints the raw
// decoded response rather than [reportFPPCommandResult]'s own text and so
// needs the same confirmed/unconfirmed -> exit code mapping applied
// separately).
func exitCodeForCommandResult(result fppCommandResult) int {
	if result.Outcome == "confirmed" {
		return exitOK
	}
	return exitCommandUnconfirmed
}
