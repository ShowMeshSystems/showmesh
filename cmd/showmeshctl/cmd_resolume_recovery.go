package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// This file is showmeshctl's Track D seam D-3a surface: "resolume
// recovery status|enable|disable|restore|revisions", over GET
// /api/v1/resolume/recovery, GET/PUT /api/v1/config/resolume.recovery
// (its own revisions list), and POST /api/v1/resolume/recovery/restore.
// ADR-030: the CLI is the "the show is broken and the UI is down" path,
// and this is the manual restore's own path (build contract §7.1).

// resolumeRecoveryRecordEntry mirrors v1.ResolumeRecoveryRecordEntry field
// for field — this program's own independent transcription, never a
// shared type with the coordinator.
type resolumeRecoveryRecordEntry struct {
	Layer              string `json:"layer"`
	LayerNameGenerated bool   `json:"layerNameGenerated"`
	State              string `json:"state"`
	Clip               string `json:"clip,omitempty"`
	ClipNameGenerated  bool   `json:"clipNameGenerated,omitempty"`
	Deck               string `json:"deck,omitempty"`
	EstablishedAt      string `json:"establishedAt,omitempty"`
	Source             string `json:"source,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type resolumeRecoveryRestoreLayer struct {
	Layer              string `json:"layer"`
	LayerNameGenerated bool   `json:"layerNameGenerated"`
	Result             string `json:"result"`
	Reason             string `json:"reason,omitempty"`
	Clip               string `json:"clip,omitempty"`
	ActionOutcome      string `json:"actionOutcome,omitempty"`
}

type resolumeRecoveryRestoreReport struct {
	StartedAt  string                         `json:"startedAt"`
	FinishedAt string                         `json:"finishedAt"`
	Trigger    string                         `json:"trigger"`
	Outcome    string                         `json:"outcome"`
	Principal  string                         `json:"principal"`
	Layers     []resolumeRecoveryRestoreLayer `json:"layers"`
}

type resolumeRecoveryResponse struct {
	ServerTime            time.Time                      `json:"serverTime"`
	AutoRestoreEnabled    bool                           `json:"autoRestoreEnabled"`
	AutoRestoreConfigured bool                           `json:"autoRestoreConfigured"`
	SettleDelaySeconds    float64                        `json:"settleDelaySeconds"`
	Record                []resolumeRecoveryRecordEntry  `json:"record"`
	LastRestore           *resolumeRecoveryRestoreReport `json:"lastRestore"`
}

type resolumeRecoveryRestoreResponse struct {
	ServerTime time.Time                     `json:"serverTime"`
	Restore    resolumeRecoveryRestoreReport `json:"restore"`
}

type configResolumeRecoveryPayload struct {
	AutoRestoreEnabled bool `json:"autoRestoreEnabled"`
}

type resolumeRecoveryConfigResponse struct {
	ServerTime             time.Time                     `json:"serverTime"`
	Kind                   string                        `json:"kind"`
	Revision               int64                         `json:"revision"`
	Payload                configResolumeRecoveryPayload `json:"payload"`
	UpdatedAt              time.Time                     `json:"updatedAt"`
	CreatedByPrincipalID   *string                       `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                       `json:"createdByPrincipalName"`
	Source                 string                        `json:"source"`
}

// minResolumeRecoveryRestoreClientTimeout is "resolume recovery restore"'s
// own minimum request budget — mirroring minResolumeActionClientTimeout's
// identical reasoning (cmd_resolume_action.go): the coordinator can hold
// this request open for up to resolumeRecoveryMaxLayers sequential D-3
// dispatches before answering. resolumeRecoveryMaxLayers is the reference
// installation's own measured layer count (LESSONS.md: "Arena held the
// real 18-layer show"), not an arbitrary round number, and
// resolumeActionSingleDispatchCeiling mirrors resolume.MaxDispatchDuration
// (40s) — this program cannot import that package, so this is a
// duplicated literal, reconciled against the server's own bound by
// TestMinResolumeRecoveryRestoreClientTimeoutExceedsServerFloor.
const (
	resolumeRecoveryMaxLayers               = 18
	resolumeActionSingleDispatchCeiling     = 40 * time.Second
	minResolumeRecoveryRestoreClientTimeout = resolumeRecoveryMaxLayers * resolumeActionSingleDispatchCeiling
)

func effectiveResolumeRecoveryRestoreTimeout(flagTimeout time.Duration) time.Duration {
	if flagTimeout < minResolumeRecoveryRestoreClientTimeout {
		return minResolumeRecoveryRestoreClientTimeout
	}
	return flagTimeout
}

// cmdResolumeRecovery implements "showmeshctl resolume recovery".
func cmdResolumeRecovery(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printResolumeRecoveryUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printResolumeRecoveryUsage(stdout)
		return exitOK
	case "status":
		return cmdResolumeRecoveryStatus(rest, stdout, stderr, clock)
	case "enable":
		return cmdResolumeRecoverySetToggle(rest, stdout, stderr, clock, true)
	case "disable":
		return cmdResolumeRecoverySetToggle(rest, stdout, stderr, clock, false)
	case "restore":
		return cmdResolumeRecoveryRestore(rest, stdout, stderr, clock)
	case "revisions":
		return cmdResolumeRecoveryRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl resolume recovery: unknown subcommand %q\n\n", sub)
		printResolumeRecoveryUsage(stderr)
		return exitUsage
	}
}

func printResolumeRecoveryUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl resolume recovery <subcommand> [flags]

Arena crash recovery (Track D seam D-3a): ShowMesh notices Resolume is
gone, says so, and — if the auto-restore toggle is on — restores the
layers it was explicitly driving once Resolume is reachable again with
the right composition confirmed loaded.

Subcommands:
  status      the open read: the toggle, the recovery record, and the
              last restore report (GET /resolume/recovery, no session
              required)
  enable      turn auto-restore on (requires config:write, admin only)
  disable     turn auto-restore off (requires config:write, admin only)
  restore     run the restore on demand (requires resolume:action) — for
              when the toggle is off, or a second pass after fixing the
              cause of a partial restore
  revisions   the auto-restore toggle's own revision history, newest first
              (requires config:write)

Run "showmeshctl resolume recovery <subcommand> --help" for flags specific
to one subcommand.
`)
}

func cmdResolumeRecoveryStatus(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume recovery status", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume recovery status [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the auto-restore toggle, the recovery record, and the last restore.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume recovery status", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume recovery status", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp resolumeRecoveryResponse
	if err := c.getJSON(ctx, "/api/v1/resolume/recovery", nil, &resp); err != nil {
		return reportError(stderr, "resolume recovery status", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume recovery status", err)
		}
		return exitOK
	}
	printResolumeRecoveryStatus(stdout, resp)
	return exitOK
}

func cmdResolumeRecoverySetToggle(args []string, stdout, stderr io.Writer, clock func() time.Time, enabled bool) int {
	label := "showmeshctl resolume recovery enable"
	if !enabled {
		label = "showmeshctl resolume recovery disable"
	}
	fs, g := newFlagSet(label, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", label)
		_, _ = fmt.Fprintln(stderr, "\nWrite a new resolume.recovery configuration revision (requires config:write, admin only).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, label, err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, label, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp resolumeRecoveryConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/resolume.recovery", configResolumeRecoveryPayload{AutoRestoreEnabled: enabled}, &resp); err != nil {
		return reportError(stderr, label, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, label, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "resolume.recovery revision %d is now active: autoRestoreEnabled=%v\n", resp.Revision, resp.Payload.AutoRestoreEnabled)
	return exitOK
}

func cmdResolumeRecoveryRestore(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume recovery restore", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume recovery restore [flags]")
		_, _ = fmt.Fprintln(stderr, "\nRun the crash-recovery restore on demand (requires resolume:action).")
		_, _ = fmt.Fprintln(stderr, "Always attempts, regardless of the auto-restore toggle.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume recovery restore", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	timeout := effectiveResolumeRecoveryRestoreTimeout(g.timeout)
	if timeout != g.timeout {
		_, _ = fmt.Fprintf(stderr,
			"showmeshctl resolume recovery restore: --timeout %s is below this command's own minimum request "+
				"budget of %s; using %s instead. The coordinator can hold this request open for up to %d "+
				"sequential dispatches before answering, so a shorter client budget could only ever abort a "+
				"healthy conversation early and misreport it as failed.\n",
			g.timeout, minResolumeRecoveryRestoreClientTimeout, timeout, resolumeRecoveryMaxLayers)
	}
	c, err := newClient(g.server, g.token, &http.Client{Timeout: timeout})
	if err != nil {
		return reportError(stderr, "resolume recovery restore", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var resp resolumeRecoveryRestoreResponse
	if err := c.postJSON(ctx, "/api/v1/resolume/recovery/restore", nil, &resp); err != nil {
		return reportError(stderr, "resolume recovery restore", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume recovery restore", err)
		}
		return exitCodeForResolumeRecoveryRestore(resp.Restore)
	}
	return reportResolumeRecoveryRestore(stdout, resp.Restore)
}

// reportResolumeRecoveryRestore prints restore's outcome honestly and
// returns the exit code it maps to.
func reportResolumeRecoveryRestore(stdout io.Writer, restore resolumeRecoveryRestoreReport) int {
	_, _ = fmt.Fprintf(stdout, "%s (trigger=%s, principal=%s):\n", restore.Outcome, restore.Trigger, restore.Principal)
	for _, l := range restore.Layers {
		if l.Reason != "" {
			_, _ = fmt.Fprintf(stdout, "  %s: %s (%s)\n", l.Layer, l.Result, l.Reason)
		} else {
			_, _ = fmt.Fprintf(stdout, "  %s: %s\n", l.Layer, l.Result)
		}
	}
	return exitCodeForResolumeRecoveryRestore(restore)
}

// exitRestoreIncomplete is build contract §1.6's own mint: a restore
// finished with outcome "partial".
const exitRestoreIncomplete = 16

func exitCodeForResolumeRecoveryRestore(restore resolumeRecoveryRestoreReport) int {
	switch restore.Outcome {
	case "restored", "nothing_to_do":
		return exitOK
	case "partial":
		return exitRestoreIncomplete
	case "failed":
		return exitActionFailed
	default:
		return exitAPIError
	}
}

func cmdResolumeRecoveryRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl resolume recovery revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl resolume recovery revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList resolume.recovery revision history, newest first.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "resolume recovery revisions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "resolume recovery revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/resolume.recovery/revisions", nil, &resp); err != nil {
		return reportError(stderr, "resolume recovery revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "resolume recovery revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

// printResolumeRecoveryStatus renders resp as human-readable text.
func printResolumeRecoveryStatus(stdout io.Writer, resp resolumeRecoveryResponse) {
	configuredNote := "stored choice"
	if !resp.AutoRestoreConfigured {
		configuredNote = "default"
	}
	_, _ = fmt.Fprintf(stdout, "auto-restore: %v (%s), settle delay: %.0fs\n", resp.AutoRestoreEnabled, configuredNote, resp.SettleDelaySeconds)
	if len(resp.Record) == 0 {
		_, _ = fmt.Fprintln(stdout, "no recovery record (no composition uploaded, or no layers)")
	}
	for _, e := range resp.Record {
		switch e.State {
		case "clip":
			_, _ = fmt.Fprintf(stdout, "  %s: clip %q (deck %s, source %s, established %s)\n", e.Layer, e.Clip, e.Deck, e.Source, e.EstablishedAt)
		case "dark":
			_, _ = fmt.Fprintf(stdout, "  %s: dark (source %s, established %s)\n", e.Layer, e.Source, e.EstablishedAt)
		default:
			_, _ = fmt.Fprintf(stdout, "  %s: unknown (%s)\n", e.Layer, e.Reason)
		}
	}
	if resp.LastRestore == nil {
		_, _ = fmt.Fprintln(stdout, "no restore has run yet")
		return
	}
	_, _ = fmt.Fprintf(stdout, "last restore: %s (trigger=%s, principal=%s, at %s)\n",
		resp.LastRestore.Outcome, resp.LastRestore.Trigger, resp.LastRestore.Principal, resp.LastRestore.FinishedAt)
}
