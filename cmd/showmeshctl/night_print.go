package main

import (
	"fmt"
	"io"
	"strings"
)

// This file renders types_night.go's wire types as text tables, following
// macro_print.go's own established conventions (a tabwriter for lists, a
// labeled block for one detail view, no colour as the only signal).

func printNightSessionDetail(w io.Writer, resp nightSessionConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Session ID:  %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:        %s\n", p.Show)
	_, _ = fmt.Fprintf(w, "Label:       %s\n", p.Label)
	_, _ = fmt.Fprintf(w, "Revision:    %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:     %s\n", resp.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:  %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:  (no principal recorded)\n")
	}

	_, _ = fmt.Fprintf(w, "\nShow playlist: fpp=%s playlist=%s\n", p.ShowPlaylist.FPPInstanceID, p.ShowPlaylist.Playlist)

	_, _ = fmt.Fprintf(w, "\nResting:\n")
	_, _ = fmt.Fprintf(w, "  FPP instance:        %s\n", p.Resting.FPPInstanceID)
	_, _ = fmt.Fprintf(w, "  Playlist:            %s\n", p.Resting.Playlist)
	_, _ = fmt.Fprintf(w, "  End-of-night playlist: %s\n", p.Resting.EndOfNightPlaylist)
	_, _ = fmt.Fprintf(w, "  Timeline asset:      show=%s sequence=%s target=%s\n",
		p.Resting.TimelineAsset.Show, p.Resting.TimelineAsset.Sequence, p.Resting.TimelineAsset.Target)
	_, _ = fmt.Fprintf(w, "  End-of-night repeat: %v\n", p.Resting.EndOfNightRepeat)

	if p.Resting.BackgroundAudio == nil {
		_, _ = fmt.Fprintf(w, "  Background audio:    (not configured)\n")
	} else {
		ba := p.Resting.BackgroundAudio
		if ba.MediaPlaylist != "" {
			_, _ = fmt.Fprintf(w, "  Background audio: mediaPlaylist=%s\n", ba.MediaPlaylist)
		} else {
			_, _ = fmt.Fprintf(w, "  Background audio: repeat=%s resume=%s itemTransition=%s maxGainDb=%g\n",
				ba.Repeat, ba.Resume, ba.ItemTransition, ba.MaxGainDb)
			if ba.ItemTransition == "crossfade" && ba.CrossfadeMs != nil {
				_, _ = fmt.Fprintf(w, "    crossfadeMs: %d\n", *ba.CrossfadeMs)
			}
			tw := newTabWriter(w)
			_, _ = fmt.Fprintln(tw, "    ITEM ID\tSHOW\tSEQUENCE\tTARGET")
			for _, it := range ba.Items {
				_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n", it.ItemID, it.Show, it.Sequence, it.Target)
			}
			_ = tw.Flush()
		}
		// ADR-049 decision 7: absent/empty targets is today's per-node behavior, not "no bed".
		if len(ba.Targets) > 0 {
			_, _ = fmt.Fprintf(w, "    Targets: %s\n", strings.Join(ba.Targets, ", "))
		} else if len(ba.ExcludeNodes) > 0 {
			_, _ = fmt.Fprintf(w, "    Targets: the show's audio nodes, excluding %s\n", strings.Join(ba.ExcludeNodes, ", "))
		} else {
			_, _ = fmt.Fprintf(w, "    Targets: none, so each node plays its own items or the show's audio nodes\n")
		}
	}

	printNightSessionCues(w, "Enter-show cues", p.EnterShow.Cues)
	_, _ = fmt.Fprintf(w, "  Blackout hold: %dms\n", p.EnterShow.BlackoutHoldMs)

	printNightSessionCues(w, "Enter-resting cues", p.EnterResting.Cues)
	_, _ = fmt.Fprintf(w, "  Blackout after show: %dms\n", p.EnterResting.BlackoutAfterShowMs)

	printNightSessionEndOfNight(w, p)

	printNightSessionLightsFades(w, p)

	printNightSessionSiteActions(w, p.SiteActions)
	printNightSessionInterlocks(w, p.Interlocks)
}

func printNightSessionLightsFades(w io.Writer, p nightSession) {
	for _, f := range []struct {
		label string
		ms    *int
	}{{"Lights fade-out before a show", p.LightsFadeOutMs}, {"Lights fade-in after a show", p.LightsFadeInMs}} {
		if f.ms == nil {
			_, _ = fmt.Fprintf(w, "  %s: none\n", f.label)
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s: %dms\n", f.label, *f.ms)
	}
	for _, f := range []struct {
		label string
		ms    *int
	}{{"Projection fade-out", p.RenderFadeOutMs}, {"Projection fade-in", p.RenderFadeInMs}} {
		if f.ms == nil {
			_, _ = fmt.Fprintf(w, "  %s: same as the lights\n", f.label)
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s: %dms\n", f.label, *f.ms)
	}
}

// printNightSessionSiteActions renders night.session.siteActions (ADR-065).
func printNightSessionSiteActions(w io.Writer, a *nightSessionSiteActions) {
	_, _ = fmt.Fprintf(w, "\nSite actions:")
	if a == nil {
		_, _ = fmt.Fprintf(w, " (not configured; power down runs every site sequence)\n")
		return
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "  Prepare site: %s\n", listOrNone(a.PrepareSite))
	if a.PowerDown == nil {
		_, _ = fmt.Fprintf(w, "  Power down:   every site sequence\n")
	} else {
		_, _ = fmt.Fprintf(w, "  Power down:   every site sequence except %s\n", listOrNone(a.PowerDown.SkipSequences))
		for _, seq := range a.PowerDown.AdditionalSequences {
			_, _ = fmt.Fprintf(w, "    night sequence %s (%s): %d steps\n", seq.ID, seq.Label, len(seq.Steps))
		}
	}
	_, _ = fmt.Fprintf(w, "  End session:  %s\n", listOrNone(a.EndSession))
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// printNightSessionInterlocks renders night.session.interlocks
// (RESTING-MODE.md §10.1, Track F seam F6). Each rule's live evaluation
// (whether it currently withholds ITS OWN phase) is reported by
// run-readiness's own checks, not here; this is the authored
// configuration, not evidence.
func printNightSessionInterlocks(w io.Writer, rules []nightSessionInterlock) {
	_, _ = fmt.Fprintf(w, "\nInterlocks (%d):\n", len(rules))
	if len(rules) == 0 {
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  NAME\tPHASE\tPOSTURE\tSIGNAL\tON UNAVAILABLE\tOVERRIDE POLICY")
	for _, r := range rules {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Phase, r.Posture, r.Signal, r.OnUnavailable, r.OverridePolicy)
	}
	_ = tw.Flush()
}

func printNightSessionEndOfNight(w io.Writer, p nightSession) {
	e := p.EnterEndOfNight
	switch e.Mode {
	case "none":
		_, _ = fmt.Fprintf(w, "\nAfter the last show: no cues run, only the end-of-night playlist starts.\n")
	case "custom":
		printNightSessionCues(w, "After the last show, end-of-night cues", e.Cues)
	default:
		_, _ = fmt.Fprintf(w, "\nAfter the last show: the enter-resting cues run, because no end-of-night cues are set.\n")
	}
	if e.BlackoutAfterShowMs != nil {
		_, _ = fmt.Fprintf(w, "  Blackout after the last show: %dms\n", *e.BlackoutAfterShowMs)
	} else {
		_, _ = fmt.Fprintf(w, "  Blackout after the last show: %dms, the same as after every show\n", p.EnterResting.BlackoutAfterShowMs)
	}
}

func printNightSessionCues(w io.Writer, heading string, cues []nightSessionCue) {
	_, _ = fmt.Fprintf(w, "\n%s (%d):\n", heading, len(cues))
	if len(cues) == 0 {
		return
	}
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "  NAME\tROLE\tACTION\tOFFSET MS\tFADE MS\tBARRIER\tON FAILURE")
	for _, c := range cues {
		fadeStr := "-"
		if c.FadeDurationMs != nil {
			fadeStr = fmt.Sprintf("%d", *c.FadeDurationMs)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%v\t%s\n", c.Name, c.Role, c.Action, c.OffsetMs, fadeStr, c.Barrier, c.OnFailure)
	}
	_ = tw.Flush()
}

func printNightSessionActiveDetail(w io.Writer, resp nightSessionActiveConfigResponse) {
	session := resp.Payload.Session
	if session == "" {
		session = "(none — cleared)"
	}
	_, _ = fmt.Fprintf(w, "Active night session: %s\n", session)
	_, _ = fmt.Fprintf(w, "Revision:             %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:              %s\n", resp.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:           %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:           (no principal recorded)\n")
	}
}
