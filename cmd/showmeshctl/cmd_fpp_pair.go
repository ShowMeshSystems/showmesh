package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// cmdFPPPair implements "showmeshctl fpp pair <instance-id> <code>":
// POST /api/v1/fpp/{instanceId}/pairing, behind principal:write.
//
// It opens a pairing and nothing more. The plugin finishes it by
// presenting the secret its own code was derived from, and the token it
// then receives is never shown here or anywhere else.
func cmdFPPPair(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp pair", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp pair [flags] <instance-id> <code>")
		_, _ = fmt.Fprintln(stderr, "\nStart pairing one FPP host's ShowMesh plugin with this coordinator")
		_, _ = fmt.Fprintln(stderr, "(POST /api/v1/fpp/{instanceId}/pairing, behind the principal:write scope).")
		_, _ = fmt.Fprintln(stderr, "<code> is the XXXX-XXXX code the plugin's own page displays.")
		_, _ = fmt.Fprintln(stderr, "\nThe pairing stays open for ten minutes and is used once. The plugin")
		_, _ = fmt.Fprintln(stderr, "finishes it by itself; no token is printed here, because the operator")
		_, _ = fmt.Fprintln(stderr, "never holds the plugin's credential.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp pair", err)
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fs.Usage()
		return exitUsage
	}
	instanceID, code := rest[0], strings.ToUpper(strings.TrimSpace(rest[1]))

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp pair", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := "/api/v1/fpp/" + url.PathEscape(instanceID) + "/pairing"
	var resp fppPairingResponse
	if err := c.postJSON(ctx, path, fppPairingRequest{Code: code}, &resp); err != nil {
		return reportError(stderr, "fpp pair", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp pair", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "waiting: %s is waiting for the plugin to finish pairing with code %s; it expires at %s\n",
		resp.InstanceID, resp.Code, resp.ExpiresAt)
	return exitOK
}

// cmdFPPPairing implements "showmeshctl fpp pairing <instance-id>":
// GET /api/v1/fpp/{instanceId}/pairing.
func cmdFPPPairing(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp pairing", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp pairing [flags] <instance-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow whether one FPP host's ShowMesh plugin is paired with this")
		_, _ = fmt.Fprintln(stderr, "coordinator (GET /api/v1/fpp/{instanceId}/pairing).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp pairing", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp pairing", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppPairingStateResponse
	if err := c.getJSON(ctx, "/api/v1/fpp/"+url.PathEscape(rest[0])+"/pairing", nil, &resp); err != nil {
		return reportError(stderr, "fpp pairing", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp pairing", err)
		}
		return exitOK
	}
	printFPPPairingState(stdout, rest[0], resp)
	return exitOK
}

// printFPPPairingState states the fact first and then what to do about
// it, in one sentence per state.
func printFPPPairingState(stdout io.Writer, instanceID string, resp fppPairingStateResponse) {
	switch resp.State {
	case "waiting":
		expires := ""
		if resp.ExpiresAt != nil {
			expires = *resp.ExpiresAt
		}
		_, _ = fmt.Fprintf(stdout, "waiting: %s is waiting for the plugin to finish pairing with code %s; it expires at %s\n",
			instanceID, resp.Code, expires)
	case "paired":
		pairedAt := ""
		if resp.PairedAt != nil {
			pairedAt = *resp.PairedAt
		}
		_, _ = fmt.Fprintf(stdout, "paired: %s paired at %s as %s\n", instanceID, pairedAt, resp.PrincipalID)
	default:
		_, _ = fmt.Fprintf(stdout, "none: %s has no pairing. Start one with \"showmeshctl fpp pair %s <code>\".\n",
			instanceID, instanceID)
	}
}
