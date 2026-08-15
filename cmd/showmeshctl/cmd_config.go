package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// This file is Step 7 seam A's showmeshctl surface: `config get`, `config
// set`, and `config revisions`, over GET/PUT /api/v1/config/fpp.endpoints
// and GET /api/v1/config/fpp.endpoints/revisions. This is the FIRST write
// this CLI has ever issued — every prior command is GET-only (see
// main.go's top-level help text, corrected by this seam) — so `config set`
// is also the first exercise of [client.putJSON].
//
// Only "fpp.endpoints" exists as a configuration kind today (RES-008 D1),
// so none of these three subcommands takes a kind argument; a future
// config kind is a CLI change, not something this shape has to anticipate
// now.
//
// `config set`'s payload parsing ([parseConfigSetPayload]) is Step 7 seam
// A review defect 2's fix: it accepts either a bare {"endpoints":[...]}
// payload or the full object `config get --output json` prints (endpoints
// nested under "payload"), and refuses client-side, before any request is
// sent, when it cannot find an "endpoints" key in either shape — this CLI
// must never be the one that sends a nil/absent endpoints list, because
// showmeshctl is a deliberately independent client of the coordinator's
// API (an enforced import-graph test forbids it from importing any
// coordinator package), and a server that trusts its clients not to send
// a wipe is exactly how this defect happened.

func cmdConfig(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printConfigUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printConfigUsage(stdout)
		return exitOK
	case "get":
		return cmdConfigGet(rest, stdout, stderr, clock)
	case "set":
		return cmdConfigSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdConfigRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl config: unknown subcommand %q\n\n", sub)
		printConfigUsage(stderr)
		return exitUsage
	}
}

func printConfigUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl config <subcommand> [flags]

Read or write the coordinator's fpp.endpoints configuration (Step 7,
RES-008 D1): the list of FPP instances the coordinator polls, moved out
of SHOWMESH_FPP_ENDPOINTS into the coordinator's authoritative store.
Every subcommand requires the config:write scope (admin only) — there is
no config:read scope; reading this surface is exactly as sensitive as
writing it.

Subcommands:
  get         show the active configuration
  set         write a new configuration revision (reads a payload from
              --file, or from stdin if --file is not given)
  revisions   list revision history, newest first

"config set" accepts EITHER a bare {"endpoints":[...]} payload, or the
full object "config get --output json" prints (endpoints nested under
"payload") — so "config get --output json | config set" (or the same
piped through an edit in between) genuinely round-trips. It refuses,
before sending anything, if it cannot find an "endpoints" key in either
shape; it never sends a request with a nil or absent endpoints list.

A configuration change here takes effect without a restart (ADR-036):
command dispatch resolves the endpoint list per request, and the
collector set follows within about ten seconds. "showmeshctl config set"
and "showmeshctl config get" both print this fact; do not skip it when
scripting against this command.

While SHOWMESH_FPP_ENDPOINTS is still set in the coordinator's own
environment, "config set" is refused outright (409): remove it from the
coordinator's environment and restart the coordinator once, then retry.

Run "showmeshctl config <subcommand> --help" for flags specific to one
subcommand.
`)
}

// cmdConfigGet implements `showmeshctl config get`
// (GET /api/v1/config/fpp.endpoints).
func cmdConfigGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active fpp.endpoints configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config get", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppEndpointsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/fpp.endpoints", nil, &resp); err != nil {
		return reportError(stderr, "config get", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config get", err)
		}
		return exitOK
	}
	printFPPEndpointsConfig(stdout, resp)
	return exitOK
}

// cmdConfigSet implements `showmeshctl config set`
// (PUT /api/v1/config/fpp.endpoints): this CLI's first write. The payload
// is read from --file, or from stdin when --file is not given, and is
// parsed by [parseConfigSetPayload] — see that function's doc comment for
// exactly which two shapes it accepts. This now genuinely composes with
// `showmeshctl config get --output json` piped through an edit and back
// in (TestCmdConfigSetAcceptsAFullConfigGetResponse pins the actual round
// trip): an earlier version of this doc comment made that same claim while
// `config set` only ever accepted a bare {"endpoints":[...]} payload, so
// piping `config get`'s own output back in silently decoded to nil
// endpoints and PUT a body that wiped every configured instance — Step 7
// seam A review defect 2. A hand-written file kept under version control,
// in the bare shape, still works exactly as before.
func cmdConfigSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching {\"endpoints\":[{\"id\":string,\"url\":string},...]}; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new fpp.endpoints configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). Validated before activation: an invalid payload is rejected")
		_, _ = fmt.Fprintln(stderr, "and appends no revision (ADR-009).")
		_, _ = fmt.Fprintln(stderr, "\nThis takes effect without a restart (ADR-036): dispatch resolves the")
		_, _ = fmt.Fprintln(stderr, "endpoint list per request, and the collector set follows within about")
		_, _ = fmt.Fprintln(stderr, "ten seconds.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "config set", newCLIError(exitUsage, "%v", err))
	}

	payload, err := parseConfigSetPayload(raw)
	if err != nil {
		return reportError(stderr, "config set", newCLIError(exitUsage, "%v", err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config set", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppEndpointsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/fpp.endpoints", payload, &resp); err != nil {
		return reportError(stderr, "config set", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config set", err)
		}
		return exitOK
	}
	printFPPEndpointsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl config set: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}

// readConfigPayload reads file's contents, or stdin when file is "".
func readConfigPayload(file string) ([]byte, error) {
	if file == "" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading payload from stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading payload from %s: %w", file, err)
	}
	return raw, nil
}

// parseConfigSetPayload implements `config set`'s own half of Step 7 seam
// A review defect 2, described precisely: the coordinator's own PUT
// requires a bare {"endpoints":[...]} body (config.go's own
// decodeFPPEndpointsConfigPutBody), while `config get --output json`
// emits the FULL response object, with endpoints nested three levels down
// under "payload" — the two shapes are genuinely different, and this
// command's own doc comment recommended piping one into the other. Before
// this fix, `config set` only ever decoded raw directly as
// configFPPEndpointsPayload: fed the full response shape, "endpoints"
// does not exist at that level, so Endpoints silently stayed nil — no
// error, no warning — and this command PUT a body that wiped every
// endpoint the coordinator had configured. I reproduced this against a
// live coordinator: it printed "revision N is now active" and "(no FPP
// endpoints configured)".
//
// The two shapes are told apart DETERMINISTICALLY, by which top-level key
// is actually present — never by trial-and-error unmarshalling into a
// permissive struct that would hide which shape was actually matched:
//
//   - A top-level "endpoints" key means the bare payload shape. Used
//     as-is.
//   - Otherwise, a top-level "payload" key means the full `config get`
//     response shape. Its OWN "endpoints" key is required to be present —
//     a "payload" object with no "endpoints" key at all does not silently
//     become an empty list.
//   - Neither key present: refused, naming both shapes this command
//     accepts, rather than guessing.
//
// In every case reached, if the resolved "endpoints" value is present but
// literally JSON `null`, this is ALSO refused rather than silently
// producing a nil slice — the identical "a JSON null is not an absent
// key" rule the coordinator's own decodeFPPEndpointsConfigPutBody
// enforces, applied here so this CLI can never be the one that sends a
// nil/absent endpoints list to the server, matching this seam's own
// review finding that the server-side fix alone is not enough: showmeshctl
// is a deliberately independent client (its own enforced import-graph
// test), and a server that trusts its clients not to send a wipe is
// exactly how this defect happened in the first place.
func parseConfigSetPayload(raw []byte) (configFPPEndpointsPayload, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return configFPPEndpointsPayload{}, fmt.Errorf(
			`payload must be a JSON object matching {"endpoints":[{"id":string,"url":string},...]}: %w`, err)
	}

	endpointsRaw, ok := top["endpoints"]
	source := `the top-level "endpoints" key`
	if !ok {
		payloadRaw, hasPayload := top["payload"]
		if !hasPayload {
			return configFPPEndpointsPayload{}, errors.New(
				`no "endpoints" key found at the top level, and no "payload" key either; this command accepts either ` +
					`a bare {"endpoints":[...]} payload, or the full object "showmeshctl config get --output json" prints`)
		}
		var payloadFields map[string]json.RawMessage
		if err := json.Unmarshal(payloadRaw, &payloadFields); err != nil {
			return configFPPEndpointsPayload{}, fmt.Errorf(`"payload" must be a JSON object matching {"endpoints":[...]}: %w`, err)
		}
		endpointsRaw, ok = payloadFields["endpoints"]
		if !ok {
			return configFPPEndpointsPayload{}, errors.New(
				`"payload" has no "endpoints" key; this does not look like a "config get" response`)
		}
		source = `"payload"'s "endpoints" key`
	}

	if bytes.Equal(bytes.TrimSpace(endpointsRaw), []byte("null")) {
		return configFPPEndpointsPayload{}, fmt.Errorf(
			`%s is JSON null, not an array; pass an empty array ("endpoints": []) to deliberately configure zero endpoints`, source)
	}

	var endpoints []configFPPEndpoint
	if err := json.Unmarshal(endpointsRaw, &endpoints); err != nil {
		return configFPPEndpointsPayload{}, fmt.Errorf(`%s must be an array of {"id":string,"url":string}: %w`, source, err)
	}
	return configFPPEndpointsPayload{Endpoints: endpoints}, nil
}

// cmdConfigRevisions implements `showmeshctl config revisions`
// (GET /api/v1/config/fpp.endpoints/revisions).
func cmdConfigRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl config revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl config revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList fpp.endpoints revision history, newest first. Metadata only —")
		_, _ = fmt.Fprintln(stderr, "no payload; rollback tooling is deliberately out of scope (RES-008).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "config revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "config revisions", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/fpp.endpoints/revisions", nil, &resp); err != nil {
		return reportError(stderr, "config revisions", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "config revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}
