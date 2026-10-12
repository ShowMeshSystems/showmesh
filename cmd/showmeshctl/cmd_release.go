package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"
)

type releaseSettingsPayload struct {
	Enabled              bool  `json:"enabled"`
	CheckIntervalSeconds int64 `json:"checkIntervalSeconds"`
}

// releaseSettingsConfigResponse is the body of GET and PUT /api/v1/config/release.settings.
type releaseSettingsConfigResponse struct {
	ServerTime             time.Time              `json:"serverTime"`
	Kind                   string                 `json:"kind"`
	Revision               int64                  `json:"revision"`
	Payload                releaseSettingsPayload `json:"payload"`
	UpdatedAt              time.Time              `json:"updatedAt"`
	CreatedByPrincipalID   *string                `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                `json:"createdByPrincipalName"`
	Source                 string                 `json:"source"`
}

const releaseSettingsAPIPath = "/api/v1/config/release.settings"

func cmdRelease(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printReleaseUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printReleaseUsage(stdout)
		return exitOK
	case "settings":
		return cmdReleaseSettings(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl release: unknown subcommand %q\n\n", sub)
		printReleaseUsage(stderr)
		return exitUsage
	}
}

func cmdReleaseSettings(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printReleaseUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printReleaseUsage(stdout)
		return exitOK
	case "get":
		return cmdReleaseSettingsGet(rest, stdout, stderr, clock)
	case "set":
		return cmdReleaseSettingsSet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl release settings: unknown subcommand %q\n\n", sub)
		printReleaseUsage(stderr)
		return exitUsage
	}
}

func printReleaseUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl release settings <get|set> [flags]

The newest-release check: whether the coordinator asks GitHub for the newest
ShowMesh release, and how often. It installs nothing. See the answer with
"showmeshctl system versions".

Subcommands:
  settings get   show the release check settings
  settings set   change them (requires config:write); takes effect at once
`)
}

func cmdReleaseSettingsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl release settings get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl release settings get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the release check settings (GET /api/v1/config/release.settings).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "release settings get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "release settings get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp releaseSettingsConfigResponse
	if err := c.getJSON(ctx, releaseSettingsAPIPath, nil, &resp); err != nil {
		return reportError(stderr, "release settings get", err)
	}
	return printReleaseSettingsResult(stdout, stderr, "release settings get", g, resp, clock)
}

func cmdReleaseSettingsSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl release settings set", stderr)
	var enabled bool
	var interval time.Duration
	fs.BoolVar(&enabled, "enabled", true, "whether the coordinator asks GitHub for the newest release; --enabled=false turns it off")
	fs.DurationVar(&interval, "interval", 0, "how often it asks, e.g. 6h; at least 5m")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl release settings set [--enabled=true|false] [--interval 6h] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nChange the release check settings (requires config:write). Only the flags")
		_, _ = fmt.Fprintln(stderr, "you pass are changed. The running check follows at once, with no restart.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "settings changed since they were read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "release settings set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	// Only the flags passed on this invocation are sent, so an omitted flag keeps the stored value.
	body := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "enabled":
			body["enabled"] = enabled
		case "interval":
			body["checkIntervalSeconds"] = int64(interval / time.Second)
		}
	})
	if len(body) == 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl release settings set: pass at least one of --enabled, --interval")
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "release settings set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r releaseSettingsConfigResponse
		if err := c.getJSON(ctx, releaseSettingsAPIPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "release settings set", err)
	}
	var resp releaseSettingsConfigResponse
	if err := c.putJSON(ctx, releaseSettingsAPIPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "release settings set", err)
	}
	return printReleaseSettingsResult(stdout, stderr, "release settings set", g, resp, clock)
}

func printReleaseSettingsResult(stdout, stderr io.Writer, name string, g *globalFlags, resp releaseSettingsConfigResponse, clock func() time.Time) int {
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	check := "off"
	if resp.Payload.Enabled {
		check = "on"
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintf(tw, "check:\t%s\n", check)
	_, _ = fmt.Fprintf(tw, "interval:\t%s\n", time.Duration(resp.Payload.CheckIntervalSeconds)*time.Second)
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_, _ = fmt.Fprintf(tw, "source:\t%s\n", resp.Source)
	_, _ = fmt.Fprintf(tw, "changedBy:\t%s\n", stringOrDash(resp.CreatedByPrincipalName))
	_ = tw.Flush()
	return exitOK
}
