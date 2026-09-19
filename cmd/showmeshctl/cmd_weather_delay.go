package main

import (
	"context"
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
	case "resume":
		return cmdWeatherDelayAction(rest, stdout, stderr, clock, "showmeshctl weather-delay resume", "/api/v1/weather-delay/resume")
	case "status":
		return cmdWeatherDelayStatus(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl weather-delay: unknown subcommand %q\n\n", sub)
		printWeatherDelayUsage(stderr)
		return exitUsage
	}
}

func printWeatherDelayUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl weather-delay <subcommand> [flags]

Start or resume a weather delay: while active, nothing starts output
(playlists, Cues, the night lifecycle) and stop/blackout/power-off/
emergency stop keep working. See ADR-053.

  start    Start a weather delay. Requires show:weatherdelay:invoke. No
            confirmation prompt: one press starts it. Starting while
            already active re-sends everything without resetting when it
            started.
  resume   Resume from an active weather delay. Requires
            show:weatherdelay:resume, a separate scope from start.
  status   Report the current state and, per plan node, whether each
            configured alert asset is present and hash-verified there.
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
	Revision        int64                       `json:"revision"`
	Targets         []weatherDelayTargetOutcome `json:"targets"`
	NotSaved        bool                        `json:"notSaved"`
	NotSavedMessage string                      `json:"notSavedMessage"`
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
	ServerTime  time.Time                      `json:"serverTime"`
	Active      bool                           `json:"active"`
	Kind        string                         `json:"kind"`
	StartedAt   string                         `json:"startedAt"`
	StartedBy   string                         `json:"startedBy"`
	Revision    int64                          `json:"revision"`
	Assets      []weatherDelayNodeAssets       `json:"assets"`
	PowerGroups []weatherDelayPowerGroupStatus `json:"powerGroups"`
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
			result.Kind, result.StartedAt, result.StartedBy)
	} else {
		_, _ = fmt.Fprintln(stdout, "weather delay: resumed")
	}
	if result.NotSaved {
		_, _ = fmt.Fprintln(stdout, "  "+result.NotSavedMessage)
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
			resp.Kind, resp.StartedAt, resp.StartedBy)
	} else {
		_, _ = fmt.Fprintln(stdout, "weather delay: not active")
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
