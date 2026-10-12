package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

type systemVersionsResponse struct {
	ServerTime  time.Time                `json:"serverTime"`
	Coordinator systemCoordinatorVersion `json:"coordinator"`
	Nodes       []systemComponentVersion `json:"nodes"`
	Plugins     []systemComponentVersion `json:"plugins"`
	Release     systemRelease            `json:"release"`
}

type systemCoordinatorVersion struct {
	Version string `json:"version"`
}

type systemComponentVersion struct {
	ID       string  `json:"id"`
	Version  *string `json:"version"`
	Reason   *string `json:"reason"`
	Mismatch bool    `json:"mismatch"`
	Warning  *string `json:"warning"`
}

type systemRelease struct {
	CheckEnabled         bool       `json:"checkEnabled"`
	CheckIntervalSeconds int64      `json:"checkIntervalSeconds"`
	CheckedAt            *time.Time `json:"checkedAt"`
	Tag                  *string    `json:"tag"`
	PublishedAt          *time.Time `json:"publishedAt"`
	InstallCommand       *string    `json:"installCommand"`
	Available            bool       `json:"available"`
	Problem              *string    `json:"problem"`
}

func cmdSystem(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printSystemUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printSystemUsage(stdout)
		return exitOK
	case "versions":
		return cmdSystemVersions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl system: unknown subcommand %q\n\n", sub)
		printSystemUsage(stderr)
		return exitUsage
	}
}

func printSystemUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl system <subcommand> [flags]

Subcommands:
  versions   the coordinator's, every node's and every FPP plugin's version,
             and the newest published release
`)
}

func cmdSystemVersions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl system versions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl system versions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nThe version every component runs, a warning for each one that does not")
		_, _ = fmt.Fprintln(stderr, "match the coordinator, and the newest release (GET /api/v1/system/versions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "system versions", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "system versions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp systemVersionsResponse
	raw, err := c.getJSONKeepingRaw(ctx, "/api/v1/system/versions", nil, &resp)
	if err != nil {
		return reportError(stderr, "system versions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSONBody(stdout, raw); err != nil {
			return reportError(stderr, "system versions", err)
		}
		return exitOK
	}
	printSystemVersions(stdout, resp)
	return exitOK
}

func printSystemVersions(w io.Writer, resp systemVersionsResponse) {
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "COMPONENT\tID\tVERSION\tSTATUS")
	_, _ = fmt.Fprintf(tw, "coordinator\t-\t%s\t-\n", resp.Coordinator.Version)
	var warnings []string
	row := func(kind string, c systemComponentVersion) {
		status := "matches"
		switch {
		case c.Mismatch:
			status = "mismatch"
		case c.Version == nil:
			status = stringOrDash(c.Reason)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", kind, c.ID, stringOrDash(c.Version), status)
		if c.Warning != nil {
			warnings = append(warnings, *c.Warning)
		}
	}
	for _, n := range resp.Nodes {
		row("node", n)
	}
	for _, p := range resp.Plugins {
		row("fpp plugin", p)
	}
	_ = tw.Flush()

	for _, warning := range warnings {
		_, _ = fmt.Fprintln(w, "Warning: "+warning)
	}
	for _, line := range systemReleaseLines(resp.Release) {
		_, _ = fmt.Fprintln(w, line)
	}
}

// systemReleaseLines is the release check's last result, one fact per line.
func systemReleaseLines(r systemRelease) []string {
	var lines []string
	switch {
	case !r.CheckEnabled:
		return []string{"The release check is turned off."}
	case r.Tag == nil && r.Problem == nil:
		return []string{"The first release check has not finished."}
	case r.Tag != nil && r.Available:
		lines = append(lines, "Release "+*r.Tag+" is available.", "Install: "+stringOrDash(r.InstallCommand))
	case r.Tag != nil:
		lines = append(lines, "Newest release: "+*r.Tag+". Nothing newer than this coordinator.")
	}
	if r.CheckedAt != nil {
		lines = append(lines, "Checked: "+r.CheckedAt.UTC().Format(time.RFC3339))
	}
	if r.Problem != nil {
		lines = append(lines, "Latest check: "+*r.Problem)
	}
	return lines
}
