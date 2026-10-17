package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// cmdFPPBrightness implements "showmeshctl fpp brightness": GET
// /api/v1/fpp/brightness, the display ceiling and how each FPP follows it.
func cmdFPPBrightness(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp brightness", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp brightness [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the display brightness ceiling every FPP follows, and each FPP's")
		_, _ = fmt.Fprintln(stderr, "reported ceiling and whether it is following (GET /api/v1/fpp/brightness).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp brightness", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp brightness", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp fppBrightnessResponse
	if err := c.getJSON(ctx, "/api/v1/fpp/brightness", nil, &resp); err != nil {
		return reportError(stderr, "fpp brightness", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp brightness", err)
		}
		return exitOK
	}
	printFPPBrightness(stdout, resp)
	return exitOK
}

// cmdFPPSetBrightnessCeiling implements "showmeshctl fpp
// set-brightness-ceiling <0-100> [--fade-seconds N]": PUT
// /api/v1/fpp/brightness/ceiling. Out of range is refused here and by the
// coordinator, never clamped.
func cmdFPPSetBrightnessCeiling(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp set-brightness-ceiling", stderr)
	var fadeSeconds int
	fs.IntVar(&fadeSeconds, "fade-seconds", 0, "fade duration in seconds, 0-86400; 0 applies immediately")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp set-brightness-ceiling [flags] <percent>")
		_, _ = fmt.Fprintln(stderr, "\nSet the display brightness ceiling every FPP follows (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/fpp/brightness/ceiling, behind the fpp:command scope). <percent>")
		_, _ = fmt.Fprintln(stderr, "is 0-100. Every FPP is brought to it within about one poll, five seconds.")
		_, _ = fmt.Fprintln(stderr, "\nAn out-of-range percent or fade is refused, never clamped.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}
	percent, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-brightness-ceiling: invalid percent %q: must be an integer 0-100\n", fs.Arg(0))
		return exitUsage
	}
	if percent < 0 || percent > 100 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-brightness-ceiling: percent %d out of range: must be 0-100\n", percent)
		return exitUsage
	}
	if fadeSeconds < 0 || fadeSeconds > 86400 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp set-brightness-ceiling: --fade-seconds %d out of range: must be 0-86400\n", fadeSeconds)
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp fppBrightnessResponse
	if err := c.putJSON(ctx, "/api/v1/fpp/brightness/ceiling", "", fppBrightnessCeilingRequest{TargetPercent: percent, FadeSeconds: fadeSeconds}, &resp); err != nil {
		return reportError(stderr, "fpp set-brightness-ceiling", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp set-brightness-ceiling", err)
		}
		return exitOK
	}
	printFPPBrightness(stdout, resp)
	return exitOK
}

func printFPPBrightness(w io.Writer, r fppBrightnessResponse) {
	if r.Ceiling == nil {
		_, _ = fmt.Fprintln(w, "display ceiling: not set")
	} else {
		c := r.Ceiling
		fade := ""
		if c.FadeEndsAt != nil {
			fade = ", fading until " + c.FadeEndsAt.UTC().Format(time.RFC3339)
		}
		by := c.ChangedBy.ID
		if c.ChangedBy.Name != "" {
			by = c.ChangedBy.Name
		}
		_, _ = fmt.Fprintf(w, "display ceiling: %d%%%s (set %s by %s %s)\n",
			c.TargetPercent, fade, c.ChangedAt.UTC().Format(time.RFC3339), c.ChangedBy.Kind, by)
	}
	if len(r.Players) == 0 {
		_, _ = fmt.Fprintln(w, "no FPP players are configured")
		return
	}
	for _, p := range r.Players {
		reported := "-"
		if p.Ceiling != nil {
			reported = fmt.Sprintf("%d%%", *p.Ceiling)
			if p.CeilingTarget != nil && *p.CeilingTarget != *p.Ceiling {
				reported += fmt.Sprintf(" -> %d%%", *p.CeilingTarget)
			}
		}
		line := fmt.Sprintf("%-20s %-13s %s", p.InstanceID, p.State, reported)
		if p.Reason != "" {
			line += "  " + p.Reason
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}
