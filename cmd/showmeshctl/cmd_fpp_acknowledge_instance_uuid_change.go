package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// cmdFPPAcknowledgeInstanceUUIDChange implements "showmeshctl fpp
// acknowledge-instance-uuid-change --confirm <instance-id>": POST
// /api/v1/fpp/{instanceId}/instance-uuid/acknowledge, behind
// config:write. Clears the ONLY marker that a configured FPP endpoint's
// observed instanceUuid changed since it was last seen (an SD card clone,
// a restored backup, or a swapped controller, ADR-025's own list of
// operational look-alikes). --confirm mirrors "fpp
// reset-observation-sequence"'s identical guard: the API route takes no
// request body, so this program refuses to send the write without an
// explicit, operator-typed confirmation.
func cmdFPPAcknowledgeInstanceUUIDChange(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp acknowledge-instance-uuid-change", stderr)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "required: confirms this endpoint's current instance uuid change has been verified")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp acknowledge-instance-uuid-change --confirm <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nClear a pending, unacknowledged FPP instance uuid change (the changed-uuid rule:")
		_, _ = fmt.Fprintln(stderr, "a changed uuid on a known endpoint is a visible conflict, never a silent")
		_, _ = fmt.Fprintln(stderr, "re-association). Never changes the recorded uuid itself, only the")
		_, _ = fmt.Fprintln(stderr, "conflict marker. Refused with 409 when the instance has no pending")
		_, _ = fmt.Fprintln(stderr, "unacknowledged change.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp acknowledge-instance-uuid-change", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	instanceID := rest[0]

	if !confirm {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp acknowledge-instance-uuid-change: refusing to acknowledge the instance uuid change for "+instanceID+" without --confirm")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp acknowledge-instance-uuid-change", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/instance-uuid/acknowledge"
	var resp acknowledgeFPPInstanceUUIDChangeResponse
	if err := c.postJSON(ctx, path, nil, &resp); err != nil {
		return reportError(stderr, "fpp acknowledge-instance-uuid-change", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp acknowledge-instance-uuid-change", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "instance uuid change acknowledged for %s (current uuid: %s)\n",
		instanceID, stringOrDash(resp.Instance.InstanceUUID))
	return exitOK
}
