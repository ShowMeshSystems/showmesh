package main

import (
	"fmt"
	"io"
	"time"
)

// This file is the CLI dispatch for the four remaining reserved
// audio.gain.*/audio.output.* operations: "showmeshctl audio gain
// set|fade" and "showmeshctl audio output mute|unmute", both built on
// cmd_audio_session.go's cmdAudioSessionLikeDispatch — same request and
// response shape, a different URL path suffix and CLI label.

func cmdAudioGain(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioGainUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioGainUsage(stdout)
		return exitOK
	case "set":
		return cmdAudioSessionLikeDispatch(rest, stdout, stderr, clock, "audio gain set", "gain")
	case "fade":
		return cmdAudioSessionLikeDispatch(rest, stdout, stderr, clock, "audio gain fade", "gain/fade")
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio gain: unknown subcommand %q\n\n", sub)
		printAudioGainUsage(stderr)
		return exitUsage
	}
}

func printAudioGainUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio gain <set|fade> [flags] <node-id> <session-id> [params-json]

Dispatch audio.gain.set or audio.gain.fade (requires audio:command).
Gain is linear (1.0 unity, 0.0 silence), never dB. It is always clamped
to the session's configured ceiling, and the clamp is reported as
evidence in the command's "reason" field when it changes the requested
value.

  set   params: {"gain": <number>}
  fade  params: {"targetGain": <number>, "durationMs": <number>, "curve": "linear"}
                A fade's own dispatch reports it as STARTED, never as
                complete: fade completion is observed by the engine
                reaching the target gain, never assumed once
                durationMs has elapsed.

--revision sets the desired-state revision this command carries
(pkg/audio.RevisionState); a stale or replayed value is reported as
"refused", not treated as a transport error.

The pipeline backend behind these operations is an open owner decision;
every dispatch against the shipped agent reports "unconfirmable" — this
is expected and does not mean the request failed to reach the node.

`)
}

func cmdAudioOutput(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioOutputUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioOutputUsage(stdout)
		return exitOK
	case "mute":
		return cmdAudioSessionLikeDispatch(rest, stdout, stderr, clock, "audio output mute", "output/mute")
	case "unmute":
		return cmdAudioSessionLikeDispatch(rest, stdout, stderr, clock, "audio output unmute", "output/unmute")
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio output: unknown subcommand %q\n\n", sub)
		printAudioOutputUsage(stderr)
		return exitUsage
	}
}

func printAudioOutputUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio output <mute|unmute> [flags] <node-id> <session-id> [params-json]

Dispatch audio.output.mute or audio.output.unmute (requires
audio:command). Both take no params body. Mute saves the session's
current gain and drives it to silence; unmute restores it, re-clamped
to whatever ceiling is current now. Both are idempotent: muting an
already-muted session, or unmuting one that is not muted, is a no-op
success rather than a refusal.

--revision sets the desired-state revision this command carries
(pkg/audio.RevisionState); a stale or replayed value is reported as
"refused", not treated as a transport error.

The pipeline backend behind these operations is an open owner decision;
every dispatch against the shipped agent reports "unconfirmable" — this
is expected and does not mean the request failed to reach the node.

`)
}
