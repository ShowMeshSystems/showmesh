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

// This file is Step 7 seam C's proof that the write endpoint it built has
// an actual, working non-UI caller — the exact failure Step 6 shipped
// three times (BUILD-PLAN: "a capability that compiles, tests green, and
// has no caller"). It is also this contract's honest client: a `200`
// from the coordinator is never printed or exited as unqualified success
// (ADR-003) — see [reportFPPCommandResult].

// minStopPlaylistClientTimeout is this subcommand's OWN minimum request
// budget, overriding --timeout's global default (10s) when it is smaller.
// Step 7 seam C review defect 1: the coordinator's own default
// confirmation deadline (internal/coordinator/api's
// defaultFPPCommandConfirmDeadline, 20s) already exceeds --timeout's
// 10s default, so this subcommand — unlike a plain snapshot read, which
// really is fast — used to abort via context deadline before the
// coordinator could ever answer "unconfirmed" with a reason, reporting a
// bare transport-timeout failure (exit 2, "coordinator unreachable ...
// timeout") for what was a successful conversation with a healthy
// coordinator. That is precisely the inverted failure direction ADR-024
// decision 7 exists to name.
//
// This is a SECOND, independently chosen literal — this program
// deliberately does not import pkg/command for its own minting/decoding
// (see importgraph_test.go's forbiddenImports comment; this constant is
// exactly that same independence applied to a number instead of a wire
// type), so it cannot be DERIVED from
// command.DefaultFPPCommandConfirmDeadline the way
// internal/coordinator/api's own default now is (api.go). It is instead
// RECONCILED against the server's real value the only way two
// independent literals safely can be: test/integration's
// TestCLIStopPlaylistTimeoutSurvivesServerConfirmDeadline runs the real
// coordinator (with its real default) and this real binary together and
// fails if this value is ever too small for that default again — a unit
// test comparing two hand-copied literals could not catch the two
// silently disagreeing, only that one can. See
// [effectiveStopPlaylistTimeout]'s own unit test for the fast, purely
// arithmetic half of this guarantee: that THIS number, whatever it is
// set to, is never overridden downward by a smaller --timeout.
//
// 35s = the coordinator's 20s default confirmation deadline + a 15s
// margin for the round trip itself (SHOWMESH HYPOTHESIS, NOT MEASURED —
// mirrors pkg/command.ClientTimeoutMargin's identical reasoning and
// value, chosen independently here for the reason above).
const minStopPlaylistClientTimeout = 35 * time.Second

// effectiveStopPlaylistTimeout returns the request budget this subcommand
// actually uses: flagTimeout (the --timeout flag) when it is already at
// least [minStopPlaylistClientTimeout], and minStopPlaylistClientTimeout
// otherwise. An operator who explicitly asks for MORE than the minimum
// (e.g. a slow or high-latency link) is still honored; only a budget too
// small to ever observe the server's own deadline is raised.
func effectiveStopPlaylistTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minStopPlaylistClientTimeout {
		return minStopPlaylistClientTimeout
	}
	return flagTimeout
}

// cmdFPPStopPlaylist implements "showmeshctl fpp stop-playlist
// <instance-id>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"stopPlaylist"}. It mints its own idempotency key per
// invocation (RES-015 section 7.3: FPP supplies nothing a coordinator
// could derive one from, so the CALLER mints it) — never accepts one as a
// flag, because reusing a key across two genuinely different invocations
// would make the second one a replay of the first, which is exactly the
// footgun an idempotency key exists to prevent a caller from stepping on
// accidentally.
func cmdFPPStopPlaylist(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp stop-playlist", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp stop-playlist [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Stop Now\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that playback actually")
		_, _ = fmt.Fprintln(stderr, "stopped. A 200 response is not itself success (ADR-003): this command")
		_, _ = fmt.Fprintln(stderr, "prints and exits on the response body's own \"confirmed\"/\"unconfirmed\"")
		_, _ = fmt.Fprintln(stderr, "outcome, never on the HTTP status alone. A fresh idempotency key is")
		_, _ = fmt.Fprintln(stderr, "minted for every invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp stop-playlist", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	// This request's own timeout, never the bare --timeout flag value: a
	// command dispatch is a long request by design (it waits out the
	// coordinator's own confirmation deadline) and must not share a
	// snapshot read's short budget — see [effectiveStopPlaylistTimeout]'s
	// own doc comment (Step 7 seam C review defect 1). Applied to BOTH the
	// underlying *http.Client's own Timeout and the request context's
	// deadline, matching newRequestClient's identical pairing for every
	// other subcommand.
	timeout := effectiveStopPlaylistTimeout(g.timeout)
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "fpp stop-playlist", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, "fpp stop-playlist", err)
	}

	var resp fppCommandResponse
	reqErr := c.postJSON(ctx, "/api/v1/fpp/"+url.PathEscape(instanceID)+"/commands",
		fppCommandRequest{Action: "stopPlaylist", IdempotencyKey: key}, &resp)
	if reqErr != nil {
		return reportError(stderr, "fpp stop-playlist", reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp stop-playlist", err)
		}
		return exitCodeForCommandResult(resp.Command)
	}
	return reportFPPCommandResult(stdout, stderr, resp.Command)
}

// newIdempotencyKey mints a fresh, random idempotency key: 16 bytes of
// crypto/rand, hex-encoded. Deliberately NOT pkg/command.NewIdempotencyKey
// — see types.go's fppCommandRequest doc comment for why this program
// mints its own value independently rather than importing the
// coordinator's shared package for it, matching the same independence
// this program's whole decode layer already keeps.
func newIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", newCLIError(exitAPIError, "generating an idempotency key: %v", err)
	}
	return hex.EncodeToString(buf), nil
}

// reportFPPCommandResult prints result's outcome honestly to stdout and
// returns the exit code it maps to — exitOK only for a genuinely
// "confirmed" outcome, [exitCommandUnconfirmed] for "unconfirmed", never
// the reverse. This is where ADR-003 actually gets enforced at this
// program's own boundary: it would be trivial (and wrong) to treat any
// 2xx HTTP response as success and print "OK" — this function's entire
// job is to refuse that shortcut.
func reportFPPCommandResult(stdout, stderr io.Writer, result fppCommandResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp stop-playlist: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", result.ID)
	}
	if result.AttributionDegraded {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp stop-playlist: WARNING: the coordinator's audit write "+
			"failed for this command; it proceeded anyway (ADR-024 decision 11's safety class) with degraded "+
			"attribution recorded only to its own stderr")
	}

	switch result.Outcome {
	case "confirmed":
		_, _ = fmt.Fprintf(stdout, "confirmed: %s stopped playing (command %s)\n", result.InstanceID, result.ID)
		return exitOK
	case "unconfirmed":
		_, _ = fmt.Fprintf(stdout, "unconfirmed: %s (command %s)\n", result.OutcomeReason, result.ID)
		return exitCommandUnconfirmed
	default:
		// Empty outcome: the one accepted race a REPLAY response can
		// return (v1.FPPCommandResult.Outcome's own doc comment) — the
		// original request's own dispatch/confirmation had not finished
		// when this replay was answered. Honestly reported, never
		// printed as either "confirmed" or "unconfirmed" it did not
		// actually claim.
		_, _ = fmt.Fprintf(stdout, "pending: command %s has not yet resolved (state %s)\n", result.ID, result.OutcomeState)
		return exitCommandUnconfirmed
	}
}

func exitCodeForCommandResult(result fppCommandResult) int {
	if result.Outcome == "confirmed" {
		return exitOK
	}
	return exitCommandUnconfirmed
}
