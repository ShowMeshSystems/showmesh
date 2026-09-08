package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// This file is showmeshctl's Track B seam B4 surface: "render transport
// <surface-id>", reading a surface's most recently probed output-transport
// evidence over the existing generic GET /api/v1/observations endpoint
// (resourceKind=surface). It adds no coordinator route of its own: B4's
// agent-side probe (internal/agent/pipeline.ProbeNDISend, dispatched by
// render.transport.probe) already writes surface.transport.available and
// surface.transport.reason into the standing observation model the moment
// it runs, so this command only ever reads, matching ADR-024's "reads stay
// open by default" — no scope is required.
//
// This is an open read, not a live re-probe: it reports the newest
// evidence this coordinator currently holds, which is only as fresh as the
// last render.surface.apply or render.transport.probe this surface's node
// actually ran. There is deliberately no "probe now" flag here — dispatching
// a fresh probe on demand needs the render command-dispatch path a
// sibling seam is building (see cmd_render.go's "status|apply|clear|restart"
// note); this command reads what is already known.

// observationsResponse is the body of GET /api/v1/observations
// (v1.ObservationsResponse) — this program's own independent
// transcription, matching every other CLI response type's "reproduced,
// not imported" rule (the import-graph guard forbids importing any
// internal/coordinator package).
type observationsResponse struct {
	ServerTime   time.Time          `json:"serverTime"`
	Observations []observationEntry `json:"observations"`
}

const (
	signalSurfaceTransportAvailable = "surface.transport.available"
	signalSurfaceTransportReason    = "surface.transport.reason"
)

func cmdRenderTransport(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl render transport", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl render transport [flags] <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow a surface's most recently probed output-transport evidence")
		_, _ = fmt.Fprintln(stderr, "(GET /api/v1/observations?resourceKind=surface). Open read, no scope")
		_, _ = fmt.Fprintln(stderr, "required. Exits 22 when the transport is confirmed unavailable, or")
		_, _ = fmt.Fprintln(stderr, "when no probe has ever run for this surface.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "render transport", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	surfaceID := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "render transport", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	query := url.Values{}
	query.Set("resourceKind", "surface")
	query.Set("resourceId", surfaceID)

	var resp observationsResponse
	if err := c.getJSON(ctx, "/api/v1/observations", query, &resp); err != nil {
		return reportError(stderr, "render transport", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	result := interpretTransportObservations(resp.Observations)

	if g.output == outputJSON {
		if err := printJSON(stdout, result); err != nil {
			return reportError(stderr, "render transport", err)
		}
		return renderTransportExitCode(result)
	}
	printRenderTransportResult(stdout, surfaceID, result)
	return renderTransportExitCode(result)
}

// renderTransportResult is this command's own decoded verdict — never
// printed or exit-coded straight off raw observation Value/State fields,
// so the three real cases (confirmed available, confirmed unavailable,
// never probed) are computed in exactly one place.
type renderTransportResult struct {
	// Probed is false when this coordinator holds no
	// surface.transport.available evidence for this surface at all — the
	// surface has never been applied with an ndi output, and
	// render.transport.probe has never run against it.
	Probed bool `json:"probed"`

	// Available is meaningless when Probed is false.
	Available bool `json:"available"`

	// Reason is the transport probe's own reason (only ever populated
	// alongside Available=false — see mqttproto.RenderSurfaceReport.
	// TransportReason's identical rule) or, when Probed is false, an
	// explanation of why there is nothing to report.
	Reason string `json:"reason"`

	// State is the raw observation state backing Available/Probed
	// (contract §4 / pkg/observation.State's six-value vocabulary),
	// exposed for a caller that wants to distinguish "confirmed
	// unavailable" from "stale" or "unknown_age" itself rather than
	// trusting this command's own collapse of those into one exit code.
	State string `json:"state"`
}

// interpretTransportObservations finds surface.transport.available (and,
// alongside it, surface.transport.reason) in obs and turns them into one
// verdict. Probed is true ONLY for state "current" — every other state
// (absent, not_collected, collection_failed, stale, unknown_age,
// unsupported) collapses to Probed: false, per ADR-011: "stale = unknown,
// never healthy." Finding 17: this used to collapse only not_collected and
// collection_failed, so a `surface.transport.available = true` row aged
// into stale or unknown_age still reported Probed: true carrying that aged
// boolean, and renderTransportExitCode exited 0 on it — the human-readable
// line said "(stale)" but the exit code, which scripts actually read,
// lied. --output json's State field preserves the discarded distinction
// for a caller that wants it.
func interpretTransportObservations(obs []observationEntry) renderTransportResult {
	var available *evidence
	var reason *evidence
	for i := range obs {
		switch obs[i].Signal {
		case signalSurfaceTransportAvailable:
			available = &obs[i].evidence
		case signalSurfaceTransportReason:
			reason = &obs[i].evidence
		}
	}

	if available == nil {
		return renderTransportResult{Probed: false, State: stateNotCollected,
			Reason: "no transport probe evidence for this surface: this coordinator has never observed a render report naming it"}
	}

	if available.State != stateCurrent {
		r := untrustworthyTransportReason(available.State)
		if available.Reason != nil && *available.Reason != "" {
			r = *available.Reason
		}
		return renderTransportResult{Probed: false, State: available.State, Reason: r}
	}

	v, _ := available.Value.(bool)
	r := ""
	if reason != nil {
		if s, ok := reason.Value.(string); ok {
			r = s
		}
	}
	return renderTransportResult{Probed: true, Available: v, Reason: r, State: available.State}
}

// untrustworthyTransportReason is the default Probed: false reason for
// every non-current state, used when the observation itself carries no
// more specific Reason.
func untrustworthyTransportReason(state string) string {
	switch state {
	case stateStale:
		return "the last transport probe result is stale and no longer trustworthy"
	case stateUnknownAge:
		return "the last transport probe result's age cannot be confirmed"
	case stateUnsupported:
		return "this surface does not report transport-availability evidence"
	default: // stateNotCollected, stateCollectionFailed
		return "transport has never been probed for this surface"
	}
}

func renderTransportExitCode(r renderTransportResult) int {
	if r.Probed && r.Available {
		return exitOK
	}
	return exitRenderUnavailable
}

func printRenderTransportResult(w io.Writer, surfaceID string, r renderTransportResult) {
	switch {
	case !r.Probed:
		_, _ = fmt.Fprintf(w, "surface %q: transport not probed (%s)\n", surfaceID, r.State)
		if r.Reason != "" {
			_, _ = fmt.Fprintf(w, "  %s\n", r.Reason)
		}
	case r.Available:
		_, _ = fmt.Fprintf(w, "surface %q: transport available (%s)\n", surfaceID, r.State)
	default:
		_, _ = fmt.Fprintf(w, "surface %q: transport UNAVAILABLE (%s)\n", surfaceID, r.State)
		if r.Reason != "" {
			_, _ = fmt.Fprintf(w, "  %s\n", r.Reason)
		}
	}
}
