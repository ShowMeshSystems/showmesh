package main

import (
	"context"
	"fmt"
	"io"
	"time"
)

func cmdSnapshot(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl snapshot", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl snapshot [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the authoritative snapshot (GET /api/v1/snapshot) that the")
		_, _ = fmt.Fprintln(stderr, "change stream's deltas are relative to.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "snapshot", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "snapshot", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var snap snapshot
	if err := c.getJSON(ctx, "/api/v1/snapshot", nil, &snap); err != nil {
		return reportError(stderr, "snapshot", err)
	}

	printClockSkew(stderr, snap.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, snap); err != nil {
			return reportError(stderr, "snapshot", err)
		}
		return exitOK
	}
	printSnapshotDetail(stdout, snap)
	return exitOK
}
