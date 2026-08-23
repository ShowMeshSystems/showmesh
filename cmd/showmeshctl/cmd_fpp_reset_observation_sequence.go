package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// cmdFPPResetObservationSequence implements "showmeshctl fpp
// reset-observation-sequence --confirm <instance-id>": DELETE
// /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}
// (TRACK-H-H2-SPEC.md §5.1), behind fpp:command. Requires --confirm for
// the same reason "showmeshctl undeclare" does (cmd_discovery.go): a
// mis-issued call cannot quietly clear evidence. The API route itself
// takes no request body — §5.1 fixes no confirmation field on the wire —
// so --confirm is this program's own guard, not a server-enforced one.
func cmdFPPResetObservationSequence(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp reset-observation-sequence", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms clearing this instance's stored observation and sequence anchor")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp reset-observation-sequence --confirm <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nClear one FPP instance's stored playlist-entry observation and its")
		_, _ = fmt.Fprintln(stderr, "sequence anchor (DELETE /api/v1/integrations/fpp/")
		_, _ = fmt.Fprintln(stderr, "playlist-entry-observations/{instanceUuid}, TRACK-H-H2-SPEC.md §5.1).")
		_, _ = fmt.Fprintln(stderr, "Requires fpp:command, deliberately not fpp:observe — clearing wedged")
		_, _ = fmt.Fprintln(stderr, "evidence and manufacturing it are different powers. Recovers a wedged")
		_, _ = fmt.Fprintln(stderr, "instance: a single observation carrying a wildly high sequence otherwise")
		_, _ = fmt.Fprintln(stderr, "refuses every later legitimate observation for that instance permanently.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp reset-observation-sequence", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp reset-observation-sequence: refusing to clear the stored observation for "+instanceID+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp reset-observation-sequence", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/integrations/fpp/playlist-entry-observations/" + url.PathEscape(instanceID)
	if err := c.deleteJSON(ctx, path, nil, nil); err != nil {
		return reportError(stderr, "fpp reset-observation-sequence", err)
	}

	_, _ = fmt.Fprintf(stdout, "observation sequence cleared for %s\n", instanceID)
	return exitOK
}
