package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// This file is showmeshctl's weather delay surface: "weather-delay
// start", "weather-delay resume", and "weather-delay status". No
// confirmation prompt on start (ADR-053 decision 1: one press, no
// confirmation step), mirroring emergency-stop's own "stop"/
// "stop-power-down", never emergency-stop hard-stop's arm/fire gate,
// which this feature has no equivalent of.

func cmdWeatherDelay(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printWeatherDelayUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printWeatherDelayUsage(stdout)
		return exitOK
	case "start":
		return cmdWeatherDelayAction(rest, stdout, stderr, clock, "showmeshctl weather-delay start", "/api/v1/weather-delay/start")
	case "cancel-night":
		return cmdWeatherDelayAction(rest, stdout, stderr, clock, "showmeshctl weather-delay cancel-night", "/api/v1/weather-delay/cancel-night")
	case "resume":
		return cmdWeatherDelayAction(rest, stdout, stderr, clock, "showmeshctl weather-delay resume", "/api/v1/weather-delay/resume")
	case "clear":
		// clear is resume for a cancelled night: the same endpoint, at
		// parity with start/cancel-night sharing one mechanism.
		return cmdWeatherDelayAction(rest, stdout, stderr, clock, "showmeshctl weather-delay clear", "/api/v1/weather-delay/resume")
	case "status":
		return cmdWeatherDelayStatus(rest, stdout, stderr, clock)
	case "presign":
		return cmdWeatherDelayPresign(rest, stdout, stderr, clock)
	case "decision":
		return cmdWeatherDelayDecision(rest, stdout, stderr, clock)
	case "trigger":
		return cmdWeatherDelayTrigger(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl weather-delay: unknown subcommand %q\n\n", sub)
		printWeatherDelayUsage(stderr)
		return exitUsage
	}
}

func printWeatherDelayUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl weather-delay <subcommand> [flags]

Start, cancel or resume for weather: while either is active, nothing
starts output (playlists, Cues, the night lifecycle) and stop/blackout/
power-off/emergency stop keep working. See ADR-053.

  start         Start a weather delay: the show resumes tonight. Requires
                show:weatherdelay:invoke. No confirmation prompt: one
                press starts it. Starting while already active re-sends
                everything without resetting when it started. Cannot turn
                a cancelled night back into a mere delay; use "clear" for
                that.
  cancel-night  Cancel the night for weather: the show will not resume.
                Requires show:weatherdelay:invoke, the same scope as
                start. Changes an active delay into a cancel in place.
                Stays set until "clear" releases it, even across a
                coordinator restart.
  resume        Resume from an active weather delay. Requires
                show:weatherdelay:resume, a separate scope from start.
  clear         Clear a cancelled night. Requires show:weatherdelay:resume.
                Does not restart the show; start a night session
                separately once cleared.
  status        Report the current state and, per plan node, whether each
                configured alert asset is present and hash-verified there.
  presign       Mint a pre-signed start an outside system can hold and
                send directly to a node if this coordinator is down.
                Requires config:write. Replaying it can only start a
                delay or a cancel night, never a resume.
  decision      Answer the one pending automatic-trigger decision:
                -id and -answer (delay, cancel-night, or dismiss).
                Requires show:weatherdelay:invoke.
  trigger       Report an automatic trigger from an outside system, the
                same intake the built-in NWS poller uses internally:
                -source, -kind (warning or lightning), and the optional
                -event-type, -severity, -expires-at, -distance-km,
                -suggest-cancel. Requires show:weatherdelay:invoke. Never
                starts, ends, or changes a delay itself: it only raises
                (or leaves alone) a pending decision.
`)
}

type weatherDelayTargetOutcome struct {
	InstanceID    string  `json:"instanceId"`
	TargetKind    string  `json:"targetKind"`
	Outcome       string  `json:"outcome"`
	OutcomeReason string  `json:"outcomeReason"`
	DeliveredVia  string  `json:"deliveredVia"`
	DispatchedAt  *string `json:"dispatchedAt"`
}

type weatherDelayActionResult struct {
	Kind            string                      `json:"kind"`
	IdempotencyKey  string                      `json:"idempotencyKey"`
	Active          bool                        `json:"active"`
	StartedAt       string                      `json:"startedAt"`
	StartedBy       string                      `json:"startedBy"`
	StartedByName   string                      `json:"startedByName"`
	Revision        int64                       `json:"revision"`
	Targets         []weatherDelayTargetOutcome `json:"targets"`
	NotSaved        bool                        `json:"notSaved"`
	NotSavedMessage string                      `json:"notSavedMessage"`
	Message         string                      `json:"message"`
}

type weatherDelayActionResponse struct {
	ServerTime time.Time                `json:"serverTime"`
	Result     weatherDelayActionResult `json:"result"`
}

type weatherDelayAssetStatus struct {
	AssetID  string `json:"assetId"`
	Present  bool   `json:"present"`
	Filename string `json:"filename"`
}

type weatherDelayNodeAssets struct {
	NodeID      string                   `json:"nodeId"`
	DelayAsset  *weatherDelayAssetStatus `json:"delayAsset"`
	CancelAsset *weatherDelayAssetStatus `json:"cancelNightAsset"`
}

type weatherDelayStateResponse struct {
	ServerTime      time.Time                      `json:"serverTime"`
	Active          bool                           `json:"active"`
	Kind            string                         `json:"kind"`
	StartedAt       string                         `json:"startedAt"`
	StartedBy       string                         `json:"startedBy"`
	StartedByName   string                         `json:"startedByName"`
	Revision        int64                          `json:"revision"`
	Assets          []weatherDelayNodeAssets       `json:"assets"`
	PowerGroups     []weatherDelayPowerGroupStatus `json:"powerGroups"`
	HeldPlayers     []weatherDelayHeldPlayer       `json:"heldPlayers"`
	LastNotifyError string                         `json:"lastNotifyError"`
	PendingDecision *weatherDelayPendingDecision   `json:"pendingDecision"`
	Sources         []weatherDelaySourceHealth     `json:"sources"`
	SourcesMessage  string                         `json:"sourcesMessage"`
}

type weatherDelayPendingDecision struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Reason        string `json:"reason"`
	Question      string `json:"question"`
	DefaultAction string `json:"defaultAction"`
	AskedAt       string `json:"askedAt"`
	Deadline      string `json:"deadline"`
}

type weatherDelaySourceHealth struct {
	Source        string `json:"source"`
	Enabled       bool   `json:"enabled"`
	LastError     string `json:"lastError"`
	LastErrorAt   string `json:"lastErrorAt"`
	LastSuccessAt string `json:"lastSuccessAt"`
}

type weatherDelayHeldPlayer struct {
	InstanceID string `json:"instanceId"`
	Message    string `json:"message"`
}

type weatherDelayPowerGroupMember struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Dark   bool   `json:"dark"`
	Reason string `json:"reason"`
}

type weatherDelayPowerGroupStatus struct {
	ID            string                         `json:"id"`
	Label         string                         `json:"label"`
	ConfirmedDark bool                           `json:"confirmedDark"`
	Since         string                         `json:"since"`
	Members       []weatherDelayPowerGroupMember `json:"members"`
}

func cmdWeatherDelayAction(args []string, stdout, stderr io.Writer, clock func() time.Time, cmdLabel, apiPath string) int {
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	key, err := newIdempotencyKey()
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}

	var resp weatherDelayActionResponse
	if err := c.postJSON(ctx, apiPath, map[string]string{"idempotencyKey": key}, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitCodeForWeatherDelayResult(resp.Result)
	}
	return reportWeatherDelayActionResult(stdout, resp.Result)
}

func reportWeatherDelayActionResult(stdout io.Writer, result weatherDelayActionResult) int {
	if result.Active {
		_, _ = fmt.Fprintf(stdout, "weather delay: active (kind=%s, startedAt=%s, startedBy=%s)\n",
			result.Kind, result.StartedAt, weatherDelayStartedByLabel(result.StartedBy, result.StartedByName))
	} else {
		_, _ = fmt.Fprintln(stdout, "weather delay: resumed")
	}
	if result.NotSaved {
		_, _ = fmt.Fprintln(stdout, "  "+result.NotSavedMessage)
	}
	if result.Message != "" {
		_, _ = fmt.Fprintln(stdout, "  "+result.Message)
	}
	if len(result.Targets) == 0 {
		_, _ = fmt.Fprintln(stdout, "  no targets were configured to dispatch to")
	}
	for _, t := range result.Targets {
		via := ""
		if t.DeliveredVia != "" {
			via = fmt.Sprintf(" via %s", t.DeliveredVia)
		}
		_, _ = fmt.Fprintf(stdout, "  %s %s: %s%s (%s)\n", t.TargetKind, t.InstanceID, t.Outcome, via, t.OutcomeReason)
	}
	return exitCodeForWeatherDelayResult(result)
}

// exitCodeForWeatherDelayResult mirrors
// exitCodeForEmergencyStopResult's identical worst-outcome-across-targets
// reasoning, on this feature's own three-word target outcome vocabulary
// (confirmed/refused/failed; "unconfirmed" from a node command awaiting
// evidence is treated the same as emergency-stop's own default case).
func exitCodeForWeatherDelayResult(result weatherDelayActionResult) int {
	worst := "confirmed"
	for _, t := range result.Targets {
		if emergencyStopOutcomeSeverity(t.Outcome) > emergencyStopOutcomeSeverity(worst) {
			worst = t.Outcome
		}
	}
	switch worst {
	case "confirmed":
		return exitOK
	case "failed":
		return exitActionFailed
	case "refused":
		return exitActionRefused
	default:
		return exitCommandUnconfirmed
	}
}

func cmdWeatherDelayStatus(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl weather-delay status"
	fs, g := newFlagSet(cmdLabel, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp weatherDelayStateResponse
	if err := c.getJSON(ctx, "/api/v1/weather-delay", nil, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	if resp.Active {
		_, _ = fmt.Fprintf(stdout, "weather delay: active (kind=%s, startedAt=%s, startedBy=%s)\n",
			resp.Kind, resp.StartedAt, weatherDelayStartedByLabel(resp.StartedBy, resp.StartedByName))
	} else {
		_, _ = fmt.Fprintln(stdout, "weather delay: not active")
	}
	if resp.LastNotifyError != "" {
		_, _ = fmt.Fprintf(stdout, "  webhook: last delivery failed: %s\n", resp.LastNotifyError)
	}
	for _, na := range resp.Assets {
		_, _ = fmt.Fprintf(stdout, "  node %s:\n", na.NodeID)
		printWeatherDelayAssetStatus(stdout, "delay alert", na.DelayAsset)
		printWeatherDelayAssetStatus(stdout, "cancel-night alert", na.CancelAsset)
	}
	for _, g := range resp.PowerGroups {
		dark := "not confirmed dark"
		if g.ConfirmedDark {
			dark = "confirmed dark since " + g.Since
		}
		label := g.Label
		if label == "" {
			label = g.ID
		}
		_, _ = fmt.Fprintf(stdout, "  power group %s (%s): %s\n", g.ID, label, dark)
		for _, m := range g.Members {
			state := "dark"
			if !m.Dark {
				state = "not dark: " + m.Reason
			}
			_, _ = fmt.Fprintf(stdout, "    %s %s: %s\n", m.Kind, m.ID, state)
		}
	}
	for _, p := range resp.HeldPlayers {
		_, _ = fmt.Fprintln(stdout, "  "+p.Message)
	}
	if resp.PendingDecision != nil {
		d := resp.PendingDecision
		_, _ = fmt.Fprintf(stdout, "  pending decision %s from %s: %s (question=%s, default=%s, deadline=%s)\n",
			d.ID, d.Source, d.Reason, d.Question, d.DefaultAction, d.Deadline)
	}
	for _, s := range resp.Sources {
		state := "disabled"
		if s.Enabled {
			state = "enabled"
			if s.LastError != "" {
				state = fmt.Sprintf("enabled, last error at %s: %s", s.LastErrorAt, s.LastError)
			} else if s.LastSuccessAt != "" {
				state = "enabled, last success at " + s.LastSuccessAt
			}
		}
		_, _ = fmt.Fprintf(stdout, "  source %s: %s\n", s.Source, state)
	}
	if resp.SourcesMessage != "" {
		_, _ = fmt.Fprintln(stdout, "  "+resp.SourcesMessage)
	}
	return exitOK
}

// weatherDelayStartedByLabel prefers the operator-recognizable name; an
// older coordinator or a row written before it existed falls back to the
// raw principal id.
func weatherDelayStartedByLabel(startedBy, startedByName string) string {
	if startedByName != "" {
		return startedByName
	}
	return startedBy
}

type weatherDelaySignedStartRequest struct {
	Kind     string `json:"kind"`
	IssuedAt string `json:"issuedAt"`
	Nonce    string `json:"nonce"`
	NotAfter string `json:"notAfter,omitempty"`
}

type weatherDelaySignedStart struct {
	Request   weatherDelaySignedStartRequest `json:"request"`
	Signature string                         `json:"signature"`
}

type weatherDelayPresignedStartResponse struct {
	ServerTime time.Time               `json:"serverTime"`
	Request    weatherDelaySignedStart `json:"request"`
	NodeURLs   []string                `json:"nodeUrls"`
}

// cmdWeatherDelayPresign mints a pre-signed start an outside system can
// POST to a node while the coordinator is down. It can never resume.
func cmdWeatherDelayPresign(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl weather-delay presign"
	fs, g := newFlagSet(cmdLabel, stderr)
	kind := fs.String("kind", "delay", `the kind to presign: "delay" or "cancelNight"`)
	validDays := fs.Int("valid-days", 1, "how many days from now this presigned start stays acceptable to a node (1-400); "+
		"an outside system can hold and send it any time before then, and replaying it afterward can only start a "+
		"delay or a cancel night, never a resume")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp weatherDelayPresignedStartResponse
	body := map[string]any{"kind": *kind, "validDays": *validDays}
	if err := c.postJSON(ctx, "/api/v1/weather-delay/presigned-start", body, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "weather delay presigned start: kind=%s issuedAt=%s notAfter=%s\n",
		resp.Request.Request.Kind, resp.Request.Request.IssuedAt, resp.Request.Request.NotAfter)
	doc, err := json.Marshal(resp.Request)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	_, _ = fmt.Fprintln(stdout, "  hold this document and POST it, unmodified, to any of the URLs below when this coordinator is down:")
	_, _ = fmt.Fprintln(stdout, "  "+string(doc))
	if len(resp.NodeURLs) == 0 {
		_, _ = fmt.Fprintln(stdout, "  (no node URL is known right now; use -o json and this coordinator's own node inventory instead)")
	}
	for _, u := range resp.NodeURLs {
		_, _ = fmt.Fprintln(stdout, "    "+u)
	}
	return exitOK
}

func printWeatherDelayAssetStatus(stdout io.Writer, label string, s *weatherDelayAssetStatus) {
	if s == nil {
		return
	}
	state := "NOT present"
	if s.Present {
		state = "present and hash-verified"
	}
	_, _ = fmt.Fprintf(stdout, "    %s (%s): %s\n", label, s.AssetID, state)
}

// weatherDelayTriggerResponse is the body of POST
// /weather-delay/triggers/{source}.
type weatherDelayTriggerResponse struct {
	ServerTime      time.Time                    `json:"serverTime"`
	Accepted        bool                         `json:"accepted"`
	Message         string                       `json:"message"`
	PendingDecision *weatherDelayPendingDecision `json:"pendingDecision"`
}

// weatherDelayDecisionResponse is the body of POST /weather-delay/decision.
type weatherDelayDecisionResponse struct {
	ServerTime time.Time                 `json:"serverTime"`
	Answer     string                    `json:"answer"`
	Result     *weatherDelayActionResult `json:"result"`
}

// cmdWeatherDelayDecision answers the one pending automatic-trigger
// decision.
func cmdWeatherDelayDecision(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl weather-delay decision"
	fs, g := newFlagSet(cmdLabel, stderr)
	id := fs.String("id", "", "the pending decision's own id, from \"weather-delay status\"")
	answer := fs.String("answer", "", `"delay", "cancel-night", or "dismiss"`)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s -id <id> -answer <delay|cancel-night|dismiss> [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}
	if *id == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl weather-delay decision: -id is required")
		fs.Usage()
		return exitUsage
	}
	wireAnswer, err := weatherDelayCLIAnswer(*answer)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl weather-delay decision: %v\n", err)
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp weatherDelayDecisionResponse
	body := map[string]string{"id": *id, "answer": wireAnswer}
	if err := c.postJSON(ctx, "/api/v1/weather-delay/decision", body, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		if resp.Result != nil {
			return exitCodeForWeatherDelayResult(*resp.Result)
		}
		return exitOK
	}
	if resp.Result != nil {
		return reportWeatherDelayActionResult(stdout, *resp.Result)
	}
	_, _ = fmt.Fprintln(stdout, "weather delay decision: dismissed")
	return exitOK
}

// weatherDelayCLIAnswer maps the CLI's own "cancel-night" spelling (this
// build's file-path/flag convention throughout) onto the wire's
// "cancelNight", and passes "delay"/"dismiss" through unchanged.
func weatherDelayCLIAnswer(answer string) (string, error) {
	switch answer {
	case "delay", "dismiss":
		return answer, nil
	case "cancel-night":
		return "cancelNight", nil
	default:
		return "", fmt.Errorf(`-answer must be "delay", "cancel-night", or "dismiss", got %q`, answer)
	}
}

// cmdWeatherDelayTrigger reports an automatic trigger from an outside
// system, the same intake the built-in NWS poller uses internally.
func cmdWeatherDelayTrigger(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const cmdLabel = "showmeshctl weather-delay trigger"
	fs, g := newFlagSet(cmdLabel, stderr)
	source := fs.String("source", "", "a short id for this trigger source, same syntax as an MQTT node id")
	kind := fs.String("kind", "", `"warning" or "lightning"`)
	eventType := fs.String("event-type", "", `the warning's own type, e.g. "Severe Thunderstorm Warning"`)
	severity := fs.String("severity", "", "the warning's own severity")
	expiresAt := fs.String("expires-at", "", "the warning's own expiry, RFC 3339")
	distanceKm := fs.Float64("distance-km", 0, "a lightning report's own distance, kilometers (0 means unknown)")
	suggestCancel := fs.Bool("suggest-cancel", false,
		"set only when this source itself knows tonight's schedule and judges too little of it would remain")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s -source <id> -kind <warning|lightning> [flags]\n", cmdLabel)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}
	if *source == "" {
		_, _ = fmt.Fprintln(stderr, "showmeshctl weather-delay trigger: -source is required")
		fs.Usage()
		return exitUsage
	}
	if *kind != "warning" && *kind != "lightning" {
		_, _ = fmt.Fprintf(stderr, "showmeshctl weather-delay trigger: -kind must be \"warning\" or \"lightning\", got %q\n", *kind)
		fs.Usage()
		return exitUsage
	}

	body := map[string]any{"kind": *kind}
	if *eventType != "" {
		body["eventType"] = *eventType
	}
	if *severity != "" {
		body["severity"] = *severity
	}
	if *expiresAt != "" {
		body["expiresAt"] = *expiresAt
	}
	if *distanceKm != 0 {
		body["distanceKm"] = *distanceKm
	}
	if *suggestCancel {
		body["suggestCancel"] = true
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp weatherDelayTriggerResponse
	if err := c.postJSON(ctx, "/api/v1/weather-delay/triggers/"+*source, body, &resp); err != nil {
		return reportError(stderr, cmdLabel, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, cmdLabel, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "weather delay trigger: accepted=%t\n", resp.Accepted)
	if resp.Message != "" {
		_, _ = fmt.Fprintln(stdout, "  "+resp.Message)
	}
	if resp.PendingDecision != nil {
		d := resp.PendingDecision
		_, _ = fmt.Fprintf(stdout, "  pending decision %s: %s (question=%s, default=%s, deadline=%s)\n",
			d.ID, d.Reason, d.Question, d.DefaultAction, d.Deadline)
	}
	return exitOK
}
