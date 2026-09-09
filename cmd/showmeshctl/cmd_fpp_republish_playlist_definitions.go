package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// cmdFPPRepublishPlaylistDefinitions implements "showmeshctl fpp
// republish-playlist-definitions <instance-id>": POST
// /api/v1/fpp/{instanceId}/playlist-definitions/republish, behind
// fpp:command. Asks one FPP host's ShowMesh plugin to drop its record of
// which playlist definitions it has already published and to sweep now.
//
// Like set-transition-gain this is not one of FPP's own command
// primitives, so it does not go through cmd_fpp_command.go's dispatch
// core, and it needs no confirmation wait: there is nothing to confirm at
// this end yet. The plugin answers before it has attempted a single post,
// so this command reports an accepted request and never an import.
func cmdFPPRepublishPlaylistDefinitions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp republish-playlist-definitions", stderr)
	var requestID string
	fs.StringVar(&requestID, "request-id", "",
		"idempotency key; minted fresh per invocation when omitted. Reuse one to poll whether the resend finished")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp republish-playlist-definitions [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nAsk one FPP host's ShowMesh plugin to resend its playlist definitions (POST")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/{instanceId}/playlist-definitions/republish, behind the fpp:command")
		_, _ = fmt.Fprintln(stderr, "scope). The plugin drops its record of what it has already published and")
		_, _ = fmt.Fprintln(stderr, "sweeps now rather than at the end of its own re-scan interval. It writes")
		_, _ = fmt.Fprintln(stderr, "nothing to FPP and can send only definitions it read from the host itself.")
		_, _ = fmt.Fprintln(stderr, "\nThis reports an ACCEPTED REQUEST, not an import. When the plugin answers it")
		_, _ = fmt.Fprintln(stderr, "has not attempted a single post, so the counts printed describe the plugin's")
		_, _ = fmt.Fprintln(stderr, "own state and the resend is owed rather than done. To see what actually")
		_, _ = fmt.Fprintln(stderr, "arrived, run: showmeshctl fpp playlist-definitions list")
		_, _ = fmt.Fprintln(stderr, "\nRepeat the same --request-id later and the plugin clears nothing and reports")
		_, _ = fmt.Fprintln(stderr, "the state as it stands, which is how you learn the resend finished sending.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp republish-playlist-definitions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	if requestID == "" {
		key, keyErr := newIdempotencyKey()
		if keyErr != nil {
			return reportError(stderr, "fpp republish-playlist-definitions", keyErr)
		}
		requestID = key
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp republish-playlist-definitions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/playlist-definitions/republish"
	var resp fppDefinitionRepublishResponse
	if err := c.postJSON(ctx, path, fppDefinitionRepublishRequest{RequestID: requestID}, &resp); err != nil {
		return reportError(stderr, "fpp republish-playlist-definitions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp republish-playlist-definitions", err)
		}
		return exitOK
	}
	printFPPDefinitionRepublishResult(stdout, resp.Republish)
	return exitOK
}

// printFPPDefinitionRepublishResult prints what the plugin agreed to and
// keeps that separate, in words, from what has arrived. The two are
// different facts and an operator reading this must not have to infer it:
// the counts are the plugin's own state, and the line after them says
// where arrival is actually read.
func printFPPDefinitionRepublishResult(stdout io.Writer, r fppDefinitionRepublishResult) {
	lead := "accepted: the plugin agreed to resend"
	if !r.Applied {
		lead = "unchanged (this requestId was already applied): nothing cleared a second time"
	}
	_, _ = fmt.Fprintf(stdout, "%s: %s cleared %d definition(s) for resend, %d still held, %d refused terminally and will not be re-sent (request %s)\n",
		lead, r.InstanceID, r.DefinitionsCleared, r.DefinitionsHeld, r.DefinitionsRefusedTerminally, r.RequestID)
	if r.SweepPending {
		_, _ = fmt.Fprintln(stdout, "the resend is owed, not done: no definition has reached the coordinator on account of this request yet.")
	} else {
		_, _ = fmt.Fprintln(stdout, "the plugin has finished sending, which is not the same as the coordinator having stored what it sent.")
	}
	_, _ = fmt.Fprintln(stdout, "to see what actually arrived, run: showmeshctl fpp playlist-definitions list")
}
