package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"time"
)

// cmdAudioSessionShow is "showmeshctl audio session show", a read-only
// sibling to cmd_audio_session.go's nine dispatch ops. It reads GET
// /api/v1/observations (resourceKind=audio_session), the same open-read
// surface cmd_render_transport.go already uses, and renders through the
// existing generic printObservations rather than any command-specific
// interpretation: a future audio_session signal becomes visible here with
// no CLI change.
func cmdAudioSessionShow(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio session show", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio session show [flags] [<session-id>]")
		_, _ = fmt.Fprintln(stderr, "\nShow a session's audio evidence (GET /api/v1/observations?resourceKind=audio_session).")
		_, _ = fmt.Fprintln(stderr, "With no session id, shows every audio session this coordinator holds evidence for,")
		_, _ = fmt.Fprintln(stderr, "grouped by session id. Open read, no scope required. audio_session observations carry")
		_, _ = fmt.Fprintln(stderr, "no node field, so there is no --node flag here.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio session show", err)
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return exitUsage
	}
	var sessionID string
	if len(rest) == 1 {
		sessionID = rest[0]
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio session show", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	query := url.Values{}
	query.Set("resourceKind", "audio_session")
	if sessionID != "" {
		query.Set("resourceId", sessionID)
	}

	var resp observationsResponse
	if err := c.getJSON(ctx, "/api/v1/observations", query, &resp); err != nil {
		return reportError(stderr, "audio session show", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp.Observations); err != nil {
			return reportError(stderr, "audio session show", err)
		}
		return exitOK
	}
	printAudioSessionObservations(stdout, resp.Observations, resp.ServerTime)
	return exitOK
}

// printAudioSessionObservations groups obs by session id and renders each
// group through printObservations, mirroring printFPPTable and
// printResolumeInstancesTable's "header line, then that resource's
// observation table" shape.
func printAudioSessionObservations(w io.Writer, obs []observationEntry, serverTime time.Time) {
	if len(obs) == 0 {
		_, _ = fmt.Fprintln(w, "(no audio session evidence)")
		return
	}
	bySession := map[string][]evidence{}
	var sessionIDs []string
	for _, o := range obs {
		if _, seen := bySession[o.Resource.ID]; !seen {
			sessionIDs = append(sessionIDs, o.Resource.ID)
		}
		bySession[o.Resource.ID] = append(bySession[o.Resource.ID], o.evidence)
	}
	sort.Strings(sessionIDs)
	for i, id := range sessionIDs {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintf(w, "session %s observations:\n", id)
		printObservations(w, bySession[id], serverTime)
	}
}
