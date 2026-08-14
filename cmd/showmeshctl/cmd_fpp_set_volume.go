package main

import (
	"fmt"
	"io"
	"strconv"
	"time"
)

// cmdFPPSetVolume implements "showmeshctl fpp set-volume <instance-id>
// <volume>": POST /api/v1/fpp/{instanceId}/commands with
// {"action":"setVolume","params":{"volume":...}}. See
// cmd_fpp_command.go's dispatchFPPCommand for the shared request/response
// core.
//
// docs/bench/fpp-command-vocabulary.md section 1.5: FPP does not validate
// "Volume Set" — an out-of-range value is silently CLAMPED (999 -> 100)
// and a non-numeric one is silently COERCED to zero. This command rejects
// an out-of-range or non-integer volume itself, before dispatch, exactly
// because "let FPP reject it" does not work — FPP never rejects it, it
// just does something the operator did not ask for.
func cmdFPPSetVolume(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp set-volume", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp set-volume [flags] <instance-id> <volume>")
		_, _ = fmt.Fprintln(stderr, "\nDispatch FPP's own \"Volume Set\" command (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/commands, behind the fpp:command scope) and wait")
		_, _ = fmt.Fprintln(stderr, "for the coordinator to confirm, by evidence, that fpp.volume actually")
		_, _ = fmt.Fprintln(stderr, "equals the requested value. <volume> must be an integer 0-100 — FPP itself")
		_, _ = fmt.Fprintln(stderr, "clamps an out-of-range value and coerces a non-numeric one to zero rather")
		_, _ = fmt.Fprintln(stderr, "than rejecting it (docs/bench/fpp-command-vocabulary.md section 1.5), so")
		_, _ = fmt.Fprintln(stderr, "this command validates it itself before ever dispatching.")
		_, _ = fmt.Fprintln(stderr, "\nA 200 response is not itself success (ADR-003): this command prints and")
		_, _ = fmt.Fprintln(stderr, "exits on the response body's own \"confirmed\"/\"unconfirmed\" outcome, never")
		_, _ = fmt.Fprintln(stderr, "on the HTTP status alone. A fresh idempotency key is minted for every")
		_, _ = fmt.Fprintln(stderr, "invocation of this command.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp set-volume", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, volumeArg := rest[0], rest[1]

	volume, err := strconv.Atoi(volumeArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-volume: invalid volume %q: must be an integer 0-100\n", volumeArg)
		return exitUsage
	}
	if volume < 0 || volume > 100 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-volume: volume %d out of range: must be 0-100\n", volume)
		return exitUsage
	}

	params := map[string]any{"volume": volume}
	return dispatchFPPCommand(stdout, stderr, clock, g, "fpp set-volume", instanceID, "setVolume", params)
}
