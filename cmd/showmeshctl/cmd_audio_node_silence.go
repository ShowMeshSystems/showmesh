package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is the CLI dispatch for "showmeshctl audio silence <node-id>"
// over POST /api/v1/nodes/{nodeId}/audio/silence: the one node-scoped
// audio.* operation, unlike cmd_audio_session.go's nine session-shaped
// ones. No sessionId, no revision, no params - the request carries only
// an idempotencyKey - and the response is node-scoped too: every
// session the node's agent silenced, plus the count it reported,
// rather than one session's own outcome.

// audioNodeSilenceCommandRequest mirrors
// internal/coordinator/api/v1.AudioNodeSilenceRequest field for field -
// this program's own independent transcription, matching
// audioSessionCommandRequest's identical "no shared import with the
// coordinator" rule.
type audioNodeSilenceCommandRequest struct {
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// audioNodeSilenceSessionResult mirrors
// internal/coordinator/api/v1.AudioNodeSilenceSessionResult field for
// field.
type audioNodeSilenceSessionResult struct {
	SessionID string `json:"sessionId"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
}

// audioNodeSilenceCommandResult mirrors
// internal/coordinator/api/v1.AudioNodeSilenceResult field for field.
type audioNodeSilenceCommandResult struct {
	CommandID      string `json:"commandId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	NodeID         string `json:"nodeId"`
	Replay         bool   `json:"replay"`

	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`

	SessionsFound int                             `json:"sessionsFound"`
	Sessions      []audioNodeSilenceSessionResult `json:"sessions"`

	DispatchedAt string  `json:"dispatchedAt"`
	ResolvedAt   *string `json:"resolvedAt"`

	AttributionDegraded bool `json:"attributionDegraded"`
}

type audioNodeSilenceCommandResponse struct {
	ServerTime time.Time                     `json:"serverTime"`
	Command    audioNodeSilenceCommandResult `json:"command"`
}

// cmdAudioSilence is a leaf subcommand (no sub-subcommand of its own,
// unlike session/gain/output), so its usage text lives directly in
// fs.Usage, matching cmdAudioSettingsGet's identical leaf-command
// pattern one file over rather than session/gain/output's separate
// group-level printXxxUsage.
func cmdAudioSilence(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio silence", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio silence [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch audio.node.silence (requires audio:command): the unconditional")
		_, _ = fmt.Fprintln(stderr, "per-node emergency stop. Stops every playback session the node's agent")
		_, _ = fmt.Fprintln(stderr, "currently holds, regardless of what this coordinator itself knew about")
		_, _ = fmt.Fprintln(stderr, "any of them - no session id, no revision, no params. Idempotent:")
		_, _ = fmt.Fprintln(stderr, "silencing an already-silent node is a success reporting zero sessions,")
		_, _ = fmt.Fprintln(stderr, "never a refusal. A node whose agent predates this operation reports")
		_, _ = fmt.Fprintln(stderr, `"refused" with that agent's own refusal reason, not a generic failure.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio silence", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	nodeID := rest[0]

	timeout := effectiveAudioSessionCommandTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl audio silence: --timeout %s is below this command's own minimum request budget of %s; using %s instead.\n",
			g.timeout, minAudioSessionCommandClientTimeout, timeout)
	}
	c, err := newClientWithTimeout(g, timeout)
	if err != nil {
		return reportError(stderr, "audio silence", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// newRenderIdempotencyKey (cmd_render_command.go) is a generic
	// 16-random-byte hex key with no render-specific meaning; reused here
	// rather than duplicated, matching cmd_audio_session.go's identical
	// reuse.
	key, err := newRenderIdempotencyKey()
	if err != nil {
		return reportError(stderr, "audio silence", err)
	}

	path := "/api/v1/nodes/" + url.PathEscape(nodeID) + "/audio/silence"
	var resp audioNodeSilenceCommandResponse
	if err := c.postJSON(ctx, path, audioNodeSilenceCommandRequest{IdempotencyKey: key}, &resp); err != nil {
		return reportError(stderr, "audio silence", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio silence", err)
		}
		return exitCodeForAudioNodeSilenceResult(resp.Command)
	}
	return reportAudioNodeSilenceResult(stdout, resp.Command)
}

// audioNodeSilenceKnownOutcomes is every outcome
// AudioNodeSilenceResult.outcome's own wire description names: the
// mqttproto Outcome vocabulary (confirmed/unconfirmed/refused/failed)
// this endpoint reports VERBATIM, plus this coordinator's own
// "unconfirmable" for a deadline exceeded with no result at all - see
// internal/coordinator/api/audionodesilence.go's
// executeAudioNodeSilenceDispatch, which reports the wire-level result
// directly rather than audio.session.*'s own finer per-session outcome
// vocabulary (audioSessionKnownOutcomes, cmd_audio_session.go).
var audioNodeSilenceKnownOutcomes = map[string]bool{
	"confirmed": true, "unconfirmed": true, "refused": true, "failed": true, "unconfirmable": true,
}

func reportAudioNodeSilenceResult(stdout io.Writer, result audioNodeSilenceCommandResult) int {
	if result.Replay {
		_, _ = fmt.Fprintf(stdout, "showmeshctl audio silence: this idempotency key was already used; "+
			"returning the ORIGINAL command's result (id %s), nothing was dispatched\n", result.CommandID)
	}
	switch result.Outcome {
	case "":
		_, _ = fmt.Fprintf(stdout, "pending: %s on %s: command %s has not yet resolved\n", result.Action, result.NodeID, result.CommandID)
		return exitCommandUnconfirmed
	case "confirmed":
		_, _ = fmt.Fprintf(stdout, "confirmed: %s on %s, %d session(s) found (command %s)\n",
			result.Action, result.NodeID, result.SessionsFound, result.CommandID)
		for _, s := range result.Sessions {
			reason := s.Reason
			if reason != "" {
				reason = ": " + reason
			}
			_, _ = fmt.Fprintf(stdout, "  %s: %s%s\n", s.SessionID, s.Outcome, reason)
		}
		return exitOK
	default:
		if !audioNodeSilenceKnownOutcomes[result.Outcome] {
			_, _ = fmt.Fprintf(stdout, "showmeshctl audio silence: the coordinator reported an outcome %q this program does not "+
				"recognize (command %s); treat this as unverified, not as success\n", result.Outcome, result.CommandID)
			return exitAPIError
		}
		_, _ = fmt.Fprintf(stdout, "%s: %s on %s: %s (command %s)\n",
			result.Outcome, result.Action, result.NodeID, result.Reason, result.CommandID)
		return exitCommandUnconfirmed
	}
}

func exitCodeForAudioNodeSilenceResult(result audioNodeSilenceCommandResult) int {
	switch result.Outcome {
	case "confirmed":
		return exitOK
	case "unconfirmed", "refused", "failed", "unconfirmable", "":
		return exitCommandUnconfirmed
	default:
		return exitAPIError
	}
}
