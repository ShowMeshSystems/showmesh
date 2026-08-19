package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is the CLI dispatch for playback session commands:
// "showmeshctl audio session <op> <node-id> <session-id> [params-json]"
// over POST /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/{op}, one
// shared core mirroring dispatchRenderCommand's identical shape one file
// over. All nine operations share one request/response shape: a session
// command's params are opaque JSON this program never validates (the
// node does, per internal/agent/audiosessionops.go) — this is the only
// dispatch surface in this program that takes a raw JSON blob rather
// than named flags, because "op-specific fields" would mean nine
// different flag sets for nine thin wire shapes with no shared meaning
// to name flags after.

const minAudioSessionCommandClientTimeout = 35 * time.Second

func effectiveAudioSessionCommandTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minAudioSessionCommandClientTimeout {
		return minAudioSessionCommandClientTimeout
	}
	return flagTimeout
}

// audioSessionOps is every operation this CLI dispatches, in the order
// [pkg/audio.Operations] declares the session verbs.
var audioSessionOps = []string{"apply", "prepare", "start", "pause", "resume", "seek", "advance", "stop", "clear"}

func cmdAudioSession(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioSessionUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioSessionUsage(stdout)
		return exitOK
	}
	for _, op := range audioSessionOps {
		if sub == op {
			return cmdAudioSessionDispatch(rest, stdout, stderr, clock, op)
		}
	}
	_, _ = fmt.Fprintf(stderr, "showmeshctl audio session: unknown subcommand %q\n\n", sub)
	printAudioSessionUsage(stderr)
	return exitUsage
}

func printAudioSessionUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio session <op> [flags] <node-id> <session-id> [params-json]

Dispatch one of the nine audio.session.* operations (requires
audio:command): apply, prepare, start, pause, resume, seek,
advance, stop, clear. params-json, when given, is passed through verbatim
as this command's "params" body field — the node validates its shape, not
this program. --revision sets the desired-state revision this command
carries (pkg/audio.RevisionState); a stale or replayed value is reported
as "refused", not treated as a transport error.

The pipeline backend behind these operations is an open owner decision
(Linear SM-68); every dispatch against the shipped agent reports
"unconfirmable" — this is expected and does not mean the request failed
to reach the node. See "showmeshctl audio session <op> --help".

`)
}

type audioSessionCommandRequest struct {
	Revision       uint64          `json:"revision"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Params         json.RawMessage `json:"params,omitempty"`
}

// audioSessionCommandResult mirrors
// internal/coordinator/api/v1.AudioSessionCommandResult field for field —
// this program's own independent transcription, matching
// renderCommandResult's identical "no shared import with the coordinator"
// rule.
type audioSessionCommandResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	SessionID      string `json:"sessionId"`
	Replay         bool   `json:"replay"`

	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`
}

type audioSessionCommandResponse struct {
	ServerTime time.Time                 `json:"serverTime"`
	Command    audioSessionCommandResult `json:"command"`
}

// cmdAudioSessionDispatch dispatches one of the nine audio.session.*
// operations, whose URL path suffix and CLI label are both the op name.
func cmdAudioSessionDispatch(args []string, stdout, stderr io.Writer, clock func() time.Time, op string) int {
	return cmdAudioSessionLikeDispatch(args, stdout, stderr, clock, "audio session "+op, op)
}

// cmdAudioSessionLikeDispatch is every audio session-shaped command's
// shared core: it POSTs to
// /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/{pathSuffix} with a
// {"revision", "idempotencyKey", "params"} body and reports the result.
// cmdLabel and pathSuffix are separate because the four audio.gain.*/
// audio.output.* operations are reachable as "showmeshctl audio gain
// set"/"showmeshctl audio output mute" — a different CLI verb than their
// URL path segment ("gain", "output/mute").
func cmdAudioSessionLikeDispatch(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, pathSuffix string) int {
	fs, g := newFlagSet("showmeshctl "+cmdLabel, stderr)
	revision := fs.Uint64("revision", 0, "the desired-state revision this command carries")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s [flags] <node-id> <session-id> [params-json]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	rest := fs.Args()
	if len(rest) != 2 && len(rest) != 3 {
		fs.Usage()
		return exitUsage
	}
	nodeID, sessionID := rest[0], rest[1]
	var params json.RawMessage
	if len(rest) == 3 {
		if !json.Valid([]byte(rest[2])) {
			return reportError(stderr, cmdLabel, newCLIError(exitUsage, "params-json is not valid JSON"))
		}
		params = json.RawMessage(rest[2])
	}

	timeout := effectiveAudioSessionCommandTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl %s: --timeout %s is below this command's own minimum request budget of %s; using %s instead.\n",
			cmdLabel, g.timeout, minAudioSessionCommandClientTimeout, timeout)
	}
	c, err := newClientWithTimeout(g, timeout)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// newRenderIdempotencyKey (cmd_render_command.go) is a generic
	// 16-random-byte hex key with no render-specific meaning; reused here
	// rather than duplicated.
	key, err := newRenderIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	path := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/sessions/" + url.PathEscape(sessionID) + "/" + pathSuffix
	var resp audioSessionCommandResponse
	reqErr := c.postJSON(ctx, path, audioSessionCommandRequest{Revision: *revision, IdempotencyKey: key, Params: params}, &resp)
	if reqErr != nil {
		return reportError(stderr, cmdLabel, reqErr)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForAudioSessionCommandResult(resp.Command)
	}
	return reportAudioSessionCommandResult(stdout, cmdLabel, resp.Command)
}

// reportAudioSessionCommandResult prints result's outcome honestly and
// returns the exit code it maps to. "unconfirmable" is expected and
// common while the only backend is internal/agent/audio.FakeEngine — see
// this file's package doc comment — so it maps to exitCommandUnconfirmed
// rather than any failure exit code, matching how this program already
// treats FPP/render's own "unconfirmed" outcomes.
func reportAudioSessionCommandResult(stdout io.Writer, cmdLabel string, result audioSessionCommandResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stdout, "showmeshctl %s: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", cmdLabel, result.CommandID)
	}
	switch result.Outcome {
	case "refused", "failed":
		_, _ = fmt.Fprintf(stdout, "%s: %s on %s/%s: %s (command %s)\n",
			result.Outcome, result.Action, result.NodeID, result.SessionID, result.Reason, result.CommandID)
		return exitCommandUnconfirmed
	case "unconfirmable":
		_, _ = fmt.Fprintf(stdout, "unconfirmable: %s on %s/%s: %s (command %s)\n",
			result.Action, result.NodeID, result.SessionID, result.Reason, result.CommandID)
		return exitCommandUnconfirmed
	case "":
		_, _ = fmt.Fprintf(stdout, "pending: %s on %s/%s: command %s has not yet resolved\n",
			result.Action, result.NodeID, result.SessionID, result.CommandID)
		return exitCommandUnconfirmed
	default:
		_, _ = fmt.Fprintf(stdout, "%s: %s on %s/%s (command %s)\n",
			result.Outcome, result.Action, result.NodeID, result.SessionID, result.CommandID)
		return exitOK
	}
}

func exitCodeForAudioSessionCommandResult(result audioSessionCommandResult) int {
	switch result.Outcome {
	case "refused", "failed", "unconfirmable", "":
		return exitCommandUnconfirmed
	default:
		return exitOK
	}
}
