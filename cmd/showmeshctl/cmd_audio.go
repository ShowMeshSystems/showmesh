package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is showmeshctl's audio surface: "audio settings
// get|set|revisions" over GET/PUT /api/v1/config/audio.settings, and
// "audio node list|get|set|revisions" over
// GET/PUT /api/v1/config/audio.node[/{id}[/revisions]]. Declares its own
// wire types rather than importing internal/coordinator/api/v1 (the
// import-graph test forbids it), matching cmd_render.go/cmd_surface.go's
// identical precedent one kind over. showConfigObjectsListResponse and
// configRevisionsResponse (types_macro.go, types.go) are already
// kind-agnostic and reused verbatim rather than declared a third time.

// configAudioSettingsPayload mirrors v1.ConfigAudioSettingsPayload.
type configAudioSettingsPayload struct {
	DriftIgnoreThresholdMs     int     `json:"driftIgnoreThresholdMs"`
	DefaultFadeCurve           string  `json:"defaultFadeCurve"`
	DefaultFadeDurationMs      int     `json:"defaultFadeDurationMs"`
	DefaultMaxBackgroundGainDb float64 `json:"defaultMaxBackgroundGainDb"`
	DuckTargetGainDb           float64 `json:"duckTargetGainDb"`
	LTCFrameRate               string  `json:"ltcFrameRate"`
	LTCDefaultStartOffset      string  `json:"ltcDefaultStartOffset"`
}

type audioSettingsConfigResponse struct {
	ServerTime             time.Time                  `json:"serverTime"`
	Kind                   string                     `json:"kind"`
	Revision               int64                      `json:"revision"`
	Payload                configAudioSettingsPayload `json:"payload"`
	UpdatedAt              time.Time                  `json:"updatedAt"`
	CreatedByPrincipalID   *string                    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                    `json:"createdByPrincipalName"`
	Source                 string                     `json:"source"`
}

// configAudioNode mirrors v1.ConfigAudioNode. LTCRoute and LTCChannel
// carry omitempty for the same reason the server side does: a
// program-only node declares neither, and the API refuses an ltcRoute
// present but empty. Omitting the pair is how "this node emits no LTC"
// is expressed on the wire.
type configAudioNode struct {
	ProgramRoute          string `json:"programRoute"`
	LTCRoute              string `json:"ltcRoute,omitempty"`
	ProgramChannels       []int  `json:"programChannels"`
	LTCChannel            int    `json:"ltcChannel,omitempty"`
	ClockDomain           string `json:"clockDomain"`
	ClockDomainProvenance string `json:"clockDomainProvenance"`
}

type audioNodeConfigResponse struct {
	ServerTime             time.Time       `json:"serverTime"`
	Kind                   string          `json:"kind"`
	ID                     string          `json:"id"`
	Revision               int64           `json:"revision"`
	Payload                configAudioNode `json:"payload"`
	UpdatedAt              time.Time       `json:"updatedAt"`
	CreatedByPrincipalID   *string         `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string         `json:"createdByPrincipalName"`
	Source                 string          `json:"source"`
}

func cmdAudio(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioUsage(stdout)
		return exitOK
	case "settings":
		return cmdAudioSettings(rest, stdout, stderr, clock)
	case "node":
		return cmdAudioNode(rest, stdout, stderr, clock)
	case "session":
		return cmdAudioSession(rest, stdout, stderr, clock)
	case "gain":
		return cmdAudioGain(rest, stdout, stderr, clock)
	case "output":
		return cmdAudioOutput(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio: unknown subcommand %q\n\n", sub)
		printAudioUsage(stderr)
		return exitUsage
	}
}

func printAudioUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio <subcommand> [flags]

The audio engine's own configuration (ADR-039). "settings" is the
audio.settings singleton: engine-wide operator defaults (drift ignore
threshold, default fade curve/duration, default background gain ceiling).
"node" is the audio.node collection: which discovered output route on one
node carries program, which carries LTC, and the operator-declared clock
domain they share. A write to "node" is refused unless the node has
ALREADY ADVERTISED both routes in its own capability report — the
coordinator never accepts a route name on the operator's claim alone.

Subcommands:
  settings get|set|revisions   audio.settings configuration (see
                                "showmeshctl audio settings --help")
  node list|get|set|revisions  audio.node configuration (see
                                "showmeshctl audio node --help")
  session <op>                 dispatch a playback session command (see
                                "showmeshctl audio session --help")
  gain set|fade                dispatch audio.gain.set/audio.gain.fade (see
                                "showmeshctl audio gain --help")
  output mute|unmute           dispatch audio.output.mute/audio.output.unmute
                                (see "showmeshctl audio output --help")

Run "showmeshctl audio <subcommand> --help" for flags specific to one
subcommand.
`)
}

// --- audio settings ---

func cmdAudioSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioSettingsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioSettingsUsage(stdout)
		return exitOK
	case "get":
		return cmdAudioSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdAudioSettingsSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdAudioSettingsRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio settings: unknown subcommand %q\n\n", sub)
		printAudioSettingsUsage(stderr)
		return exitUsage
	}
}

func printAudioSettingsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio settings <subcommand> [flags]

Read or write the coordinator's audio.settings configuration (ADR-039):
driftIgnoreThresholdMs (never measured — a starting point, not a tuned
value), defaultFadeCurve (only "linear" ships today), defaultFadeDurationMs,
defaultMaxBackgroundGainDb (DECIBELS: 0 dB is unity gain, at most +12 dB;
a linear-looking 0.5 here is only half a decibel, not a halving, so enter
-6.02 if you meant half amplitude),
duckTargetGainDb (how far a bed drops under an announcement, also in
decibels: must be negative and at least -60 dB, where -60 dB is silence.
The shipped value is PROVISIONAL and has never been heard on real
speakers),
ltcFrameRate (one of 24, 25, 29.97, 30 — non-drop-frame at every rate),
and ltcDefaultStartOffset (HH:MM:SS:FF, a session's own audio.session.apply
ltcStartOffset overrides this).
Every subcommand requires the config:write scope (admin only) — there is
no config:read scope.

This never 404s: nothing ever written reports the built-in default with
revision 0 and source "default".

Subcommands:
  get         show the active configuration (or the built-in default)
  set         write a new configuration revision — a FULL REPLACEMENT
              (reads a payload from --file, or from stdin if --file is
              not given); every field is required
  revisions   list revision history, newest first

Run "showmeshctl audio settings <subcommand> --help" for flags specific to
one subcommand.
`)
}

func cmdAudioSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active audio.settings configuration.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioSettingsConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.settings", nil, &resp); err != nil {
		return reportError(stderr, "audio settings get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings get", err)
		}
		return exitOK
	}
	printAudioSettingsConfig(stdout, resp)
	return exitOK
}

// cmdAudioSettingsSet implements `audio settings set`: a FULL REPLACEMENT
// — every field of the payload is required. The payload is read from
// --file, or from stdin when --file is not given, and sent to the
// coordinator verbatim as json.RawMessage after only a shape check (a JSON
// object) — this command never decodes it into configAudioSettingsPayload
// and re-encodes, because that would silently turn an operator's omitted
// field into an explicit zero value the server would then accept, making
// the server's field_required/field_null refusals unreachable through this
// emergency-path client. Matches cmd_action.go/cmd_macro.go's identical
// pass-through precedent one config kind over.
func cmdAudioSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings set", stderr)
	var file string
	fs.StringVar(&file, "file", "", "path to a JSON file matching configAudioSettingsPayload; reads stdin if not given")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new audio.settings configuration revision (requires config:write,")
		_, _ = fmt.Fprintln(stderr, "admin only). A FULL REPLACEMENT: every field is required — an absent field")
		_, _ = fmt.Fprintln(stderr, "is refused by name, never silently defaulted or carried forward from the")
		_, _ = fmt.Fprintln(stderr, "previous revision.")
		_, _ = fmt.Fprintln(stderr, "Validated before activation: an invalid payload is rejected and appends no")
		_, _ = fmt.Fprintln(stderr, "revision (ADR-009).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	raw, err := readConfigPayload(file)
	if err != nil {
		return reportError(stderr, "audio settings set", newCLIError(exitUsage, "%v", err))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return reportError(stderr, "audio settings set", newCLIError(exitUsage, "payload must be a JSON object matching configAudioSettingsPayload: %v", err))
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioSettingsConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/audio.settings", json.RawMessage(raw), &resp); err != nil {
		return reportError(stderr, "audio settings set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings set", err)
		}
		return exitOK
	}
	printAudioSettingsConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl audio settings set: revision %d is now active.\n", resp.Revision)
	return exitOK
}

func cmdAudioSettingsRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio settings revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio settings revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList audio.settings revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/audio.settings/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.settings/revisions", nil, &resp); err != nil {
		return reportError(stderr, "audio settings revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio settings revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// parseChannelList parses a comma-separated flag value into an ordered
// []int, one whole number per element. It does not itself enforce
// positivity or distinctness — the coordinator is the single source of
// truth for those rules and reports its own refusal by name.
func parseChannelList(csv string) ([]int, error) {
	parts := strings.Split(csv, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%q is not a whole number", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// --- audio node ---

func cmdAudioNode(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printAudioNodeUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printAudioNodeUsage(stdout)
		return exitOK
	case "list":
		return cmdAudioNodeList(rest, stdout, stderr, clock)
	case "get":
		return cmdAudioNodeGet(rest, stdout, stderr, clock)
	case "set":
		return cmdAudioNodeSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdAudioNodeRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio node: unknown subcommand %q\n\n", sub)
		printAudioNodeUsage(stderr)
		return exitUsage
	}
}

func printAudioNodeUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl audio node <subcommand> [flags]

Read or write the coordinator's audio.node configuration objects, one per
node (ADR-018, ADR-039): which discovered output route carries program and
which channels on it, which channel on that SAME route carries LTC, and
the clock domain the operator declares them to share (never inferred — no
software call proves two outputs share a hardware clock). Reads and
writes both require config:write, admin only.

"set" is refused with the node's own advertised routes named in the error
unless BOTH --program-route and --ltc-route are already present in that
node's own capability advertisement (audio.output.local / audio.output.ltc)
— never accepted on the operator's claim alone. --program-route and
--ltc-route must also name the SAME route: program and LTC leave through
one interface in one clock domain. --program-channels lists distinct,
positive, 1-based indices (1,2 for reference stereo, 1 for mono);
--ltc-channel is a positive 1-based index that must not appear in
--program-channels. Advertise the node first (the agent must be running
and have probed its audio hardware) before configuring it here.

Subcommands:
  list             enumerate audio.node objects (id is the node id)
  get <node-id>    show one node's full audio placement
  set <node-id>    write a new audio.node revision (write, full
                   replacement)
  revisions <node-id>
                   list revision history, newest first

Run "showmeshctl audio node <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdAudioNodeList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node list", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate audio.node objects (GET /api/v1/config/audio.node).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node", nil, &resp); err != nil {
		return reportError(stderr, "audio node list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdAudioNodeGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node get [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one node's audio placement (GET /api/v1/config/audio.node/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioNodeConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "audio node get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node get", err)
		}
		return exitOK
	}
	printAudioNodeDetail(stdout, resp)
	return exitOK
}

func cmdAudioNodeSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node set", stderr)
	var programRoute, ltcRoute, programChannels, clockDomain, clockDomainProvenance string
	var ltcChannel int
	fs.StringVar(&programRoute, "program-route", "", "the advertised output route to carry program audio (required)")
	fs.StringVar(&ltcRoute, "ltc-route", "", "the advertised output route to carry LTC, must equal --program-route (omit with --ltc-channel for a program-only node)")
	fs.StringVar(&programChannels, "program-channels", "", "comma-separated, ordered, distinct 1-based channel indices carrying program audio, e.g. 1,2 (required)")
	fs.IntVar(&ltcChannel, "ltc-channel", 0, "1-based channel index carrying LTC, distinct from --program-channels (omit with --ltc-route for a program-only node)")
	fs.StringVar(&clockDomain, "clock-domain", "", "the operator's own name for the shared clock domain (required)")
	fs.StringVar(&clockDomainProvenance, "clock-domain-provenance", "", "the stated basis for the clock domain declaration (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node set [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new audio.node revision (PUT /api/v1/config/audio.node/{id}).")
		_, _ = fmt.Fprintln(stderr, "Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT: this command never reads the node's current")
		_, _ = fmt.Fprintln(stderr, "definition first. Refused unless the node has already advertised the")
		_, _ = fmt.Fprintln(stderr, "routes in its own capability report — never accepted on the operator's")
		_, _ = fmt.Fprintln(stderr, "claim alone. --program-route and --ltc-route must name the same route.")
		_, _ = fmt.Fprintln(stderr, "\n--ltc-route and --ltc-channel are the one OPTIONAL pair, and they are")
		_, _ = fmt.Fprintln(stderr, "optional TOGETHER: omit both to declare a program-only node that emits")
		_, _ = fmt.Fprintln(stderr, "no LTC. That is the only way to declare a two-output interface, which")
		_, _ = fmt.Fprintln(stderr, "has no channel to spare for a discrete LTC signal. Passing one without")
		_, _ = fmt.Fprintln(stderr, "the other is refused here rather than sent. Every other flag is required.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]
	// --ltc-channel's presence, not its value, decides whether it was
	// passed: fs.Visit walks only flags the operator actually set on this
	// invocation, so "--ltc-channel 0" (a value the coordinator alone is
	// the authority on rejecting, mirroring assets settings set's
	// identical fs.Visit-over-zero-value pattern) is sent through rather
	// than refused here as if it had been omitted.
	ltcChannelSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "ltc-channel" {
			ltcChannelSet = true
		}
	})
	if programRoute == "" || programChannels == "" || clockDomain == "" || clockDomainProvenance == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl audio node set: --program-route, --program-channels, --clock-domain, and --clock-domain-provenance are all required")
		return exitUsage
	}
	// --ltc-route and --ltc-channel are optional TOGETHER: omitting both
	// declares a program-only node that emits no LTC, which is the only
	// shape a two-output interface can be declared in. Half a pair is
	// refused here rather than sent: the API refuses an empty ltcRoute as
	// field-empty, so passing --ltc-channel alone would otherwise cost a
	// round trip to earn a worse error message.
	wantLTC := ltcRoute != "" || ltcChannelSet
	if wantLTC && ltcRoute == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl audio node set: --ltc-route is required when --ltc-channel is given; omit both to declare a program-only node that emits no LTC")
		return exitUsage
	}
	if wantLTC && !ltcChannelSet {
		_, _ = fmt.Fprintln(stderr, "showmeshctl audio node set: --ltc-channel is required when --ltc-route is given; omit both to declare a program-only node that emits no LTC")
		return exitUsage
	}
	channels, err := parseChannelList(programChannels)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl audio node set: --program-channels: %v\n", err)
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configAudioNode{
		ProgramRoute:    programRoute,
		ProgramChannels: channels,
		ClockDomain:     clockDomain, ClockDomainProvenance: clockDomainProvenance,
	}
	if wantLTC {
		body.LTCRoute, body.LTCChannel = ltcRoute, ltcChannel
	}
	var resp audioNodeConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id), body, &resp); err != nil {
		return reportError(stderr, "audio node set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node set", err)
		}
		return exitOK
	}
	printAudioNodeDetail(stdout, resp)
	return exitOK
}

func cmdAudioNodeRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node revisions [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nList audio.node revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/audio.node/{id}/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/audio.node/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "audio node revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// --- printers ---

func printAudioSettingsConfig(w io.Writer, resp audioSettingsConfigResponse) {
	by := "(built-in default; no revision has ever been written)"
	if resp.CreatedByPrincipalName != nil {
		by = "by " + *resp.CreatedByPrincipalName
	}
	_, _ = fmt.Fprintf(w, "audio.settings revision %d (source %s, %s):\n", resp.Revision, resp.Source, by)
	_, _ = fmt.Fprintf(w, "  driftIgnoreThresholdMs:     %d\n", resp.Payload.DriftIgnoreThresholdMs)
	_, _ = fmt.Fprintf(w, "  defaultFadeCurve:           %s\n", resp.Payload.DefaultFadeCurve)
	_, _ = fmt.Fprintf(w, "  defaultFadeDurationMs:      %d\n", resp.Payload.DefaultFadeDurationMs)
	_, _ = fmt.Fprintf(w, "  defaultMaxBackgroundGainDb: %v dB\n", resp.Payload.DefaultMaxBackgroundGainDb)
	_, _ = fmt.Fprintf(w, "  duckTargetGainDb:           %v dB\n", resp.Payload.DuckTargetGainDb)
	_, _ = fmt.Fprintf(w, "  ltcFrameRate:               %s\n", resp.Payload.LTCFrameRate)
	_, _ = fmt.Fprintf(w, "  ltcDefaultStartOffset:      %s\n", resp.Payload.LTCDefaultStartOffset)
}

func printAudioNodeDetail(w io.Writer, resp audioNodeConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Node ID:                %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Program route:          %s\n", p.ProgramRoute)
	_, _ = fmt.Fprintf(w, "Program channels:       %v\n", p.ProgramChannels)
	// A program-only node carries neither LTC field. Say that outright
	// rather than rendering a blank route and "LTC channel: 0", which
	// reads as a real channel zero.
	if p.LTCRoute == "" && p.LTCChannel == 0 {
		_, _ = fmt.Fprintln(w, "LTC:                    none (program-only node)")
	} else {
		_, _ = fmt.Fprintf(w, "LTC route:              %s\n", p.LTCRoute)
		_, _ = fmt.Fprintf(w, "LTC channel:            %d\n", p.LTCChannel)
	}
	_, _ = fmt.Fprintf(w, "Clock domain:           %s\n", p.ClockDomain)
	_, _ = fmt.Fprintf(w, "Clock domain provenance: %s\n", p.ClockDomainProvenance)
	_, _ = fmt.Fprintf(w, "Revision:               %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:                %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:             %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintln(w, "Created by:             (unknown)")
	}
}
