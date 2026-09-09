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
	StartedAt         string                         `json:"startedAt"`
	FinishedAt        string                         `json:"finishedAt"`
	Trigger           string                         `json:"trigger"`
	Outcome           string                         `json:"outcome"`
	Principal         string                         `json:"principal"`
	Layers            []resolumeRecoveryRestoreLayer `json:"layers"`
	OmittedLayerCount int                            `json:"omittedLayerCount"`
}

// resolumeRecoveryResponse.ResolumeConfigured is *false* when this
// coordinator has no Resolume instance configured at all
// (SHOWMESH_RESOLUME_URL unset), distinct from AutoRestoreConfigured,
// which is about whether the toggle has a stored value. printResolumeRecoveryStatus
// renders "not configured" rather than AutoRestoreEnabled's default-ON
// value when this is explicitly false: an operator who believes recovery
// is armed and is wrong is worse off than one who knows it is unavailable.
//
// Declared *bool, not bool: this package sets no
// json.Decoder.DisallowUnknownFields anywhere (types.go's own doc comment)
// and has no absent-field detection of its own, so a coordinator that
// predates this field OMITS "resolumeConfigured" from the response body
// entirely rather than sending it false. A bare bool cannot tell that case
// apart from an explicit false: encoding/json leaves a missing bool field
// at its zero value, which IS false, so a newer CLI reading an older
// coordinator's response would render "not configured" about a fully
// configured, armed coordinator: exactly the harm this field exists to
// prevent, inverted. nil (absent) falls through to the ordinary toggle
// rendering instead of asserting "not configured" on no evidence.
type resolumeRecoveryResponse struct {
	ServerTime            time.Time                      `json:"serverTime"`
	ResolumeConfigured    *bool                          `json:"resolumeConfigured"`
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
// own minimum request budget, composed from the SAME terms the server's
// own resolumeRecoveryRestoreDeadline is (internal/coordinator/api/resolumerecovery.go),
// duplicated by value — this program cannot import the coordinator, the
// identical reasoning minResolumeActionClientTimeout's own doc comment
// states — plus resolumeRecoveryClientMargin strictly on top. Reconciled
// against the server's own bound by
// TestMinResolumeRecoveryRestoreClientTimeoutExceedsServerBound, which
// asserts strict inequality: a client floor merely EQUAL to the server's
// worst case is the Step 7 defect — a 5s post-restore audit write on the
// server side alone could still answer after a client with zero margin
// had already given up.
const (
	// resolumeRecoveryMaxLayers duplicates resolume.MaxRestoreLayers by
	// value — the SAME clamp both a restore itself and the server's own
	// write deadline apply, so a composition larger than this never makes
	// either bound grow without limit.
	resolumeRecoveryMaxLayers           = 30
	resolumeActionSingleDispatchCeiling = 40 * time.Second
	resolumeRecoveryBookkeepingBudget   = 5 * time.Second
	resolumeRecoveryDeadlineMargin      = 5 * time.Second
	resolumeRecoveryClientMargin        = 30 * time.Second

	minResolumeRecoveryRestoreClientTimeout = resolumeRecoveryMaxLayers*resolumeActionSingleDispatchCeiling +
		2*resolumeRecoveryBookkeepingBudget + resolumeRecoveryDeadlineMargin + resolumeRecoveryClientMargin
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
	raw, err := c.getJSONKeepingRaw(ctx, "/api/v1/resolume/recovery", nil, &resp)
	if err != nil {
		return reportError(stderr, "resolume recovery status", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSONBody(stdout, raw); err != nil {
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
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s [flags]\n", label)
		_, _ = fmt.Fprintln(stderr, "\nWrite a new resolume.recovery configuration revision (requires config:write, admin only).")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "configuration changed since it was read.")
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

	const apiPath = "/api/v1/config/resolume.recovery"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r resolumeRecoveryConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, label, err)
	}

	var resp resolumeRecoveryConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, configResolumeRecoveryPayload{AutoRestoreEnabled: enabled}, &resp); err != nil {
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
	if restore.OmittedLayerCount > 0 {
		_, _ = fmt.Fprintf(stdout, "  %d further layer(s) were not attempted (composition larger than one restore covers) — run the restore again to continue\n", restore.OmittedLayerCount)
	}
	return exitCodeForResolumeRecoveryRestore(restore)
}

// exitRestoreIncomplete is build contract §1.6's own mint: a restore
// finished with outcome "partial".
const exitRestoreIncomplete = 16

func exitCodeForResolumeRecoveryRestore(restore resolumeRecoveryRestoreReport) int {
	// A composition larger than one restore covers is never a clean
	// success regardless of how the attempted layers went — the operator
	// must run the restore again to reach the rest.
	if restore.OmittedLayerCount > 0 && (restore.Outcome == "restored" || restore.Outcome == "nothing_to_do") {
		return exitRestoreIncomplete
	}
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

// printResolumeRecoveryToggleLine renders the auto-restore toggle, or
// "not configured" in its place when resp.ResolumeConfigured is a
// non-nil, explicit false. nil (the field was absent -- an older
// coordinator that predates it) and true both fall through to the
// ordinary toggle line: there is no evidence to assert "not configured"
// against a coordinator that never said so.
func printResolumeRecoveryToggleLine(stdout io.Writer, resp resolumeRecoveryResponse) {
	if resp.ResolumeConfigured != nil && !*resp.ResolumeConfigured {
		_, _ = fmt.Fprintln(stdout, "resolume: not configured (no Resolume instance configured on this coordinator)")
		return
	}
	configuredNote := "stored choice"
	if !resp.AutoRestoreConfigured {
		configuredNote = "default"
	}
	_, _ = fmt.Fprintf(stdout, "auto-restore: %v (%s), settle delay: %.0fs\n", resp.AutoRestoreEnabled, configuredNote, resp.SettleDelaySeconds)
}

// printResolumeRecoveryStatus renders resp as human-readable text.
//
// Only the auto-restore toggle line is replaced when ResolumeConfigured is
// explicitly false (never merely nil/absent -- see that field's own doc
// comment): record and lastRestore are both separately required fields in
// the same schema, and an unconfigured coordinator can still hold a stored
// recovery record and a previous restore outcome, so this renders the
// configured-state distinction without discarding either.
func printResolumeRecoveryStatus(stdout io.Writer, resp resolumeRecoveryResponse) {
	printResolumeRecoveryToggleLine(stdout, resp)
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
