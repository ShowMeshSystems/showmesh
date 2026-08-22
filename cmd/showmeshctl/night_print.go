package main

import (
	"fmt"
	"io"
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

	printNightSessionCues(w, "Enter-show cues", p.EnterShow.Cues)
	_, _ = fmt.Fprintf(w, "  Blackout hold: %dms\n", p.EnterShow.BlackoutHoldMs)

	printNightSessionCues(w, "Enter-resting cues", p.EnterResting.Cues)
	_, _ = fmt.Fprintf(w, "  Blackout after show: %dms\n", p.EnterResting.BlackoutAfterShowMs)

	printNightSessionSiteControl(w, p.SiteControl)
	printNightSessionInterlocks(w, p.Interlocks)
}

// printNightSessionSiteControl renders night.session.siteControl
// (RESTING-MODE.md §10.2/§10.4, Track F seam F6): "(not configured)"
// when the deployment omits the whole block, per RESTING-MODE.md §10's
// own opening line.
func printNightSessionSiteControl(w io.Writer, sc *nightSessionSiteControl) {
	_, _ = fmt.Fprintf(w, "\nSite control:")
	if sc == nil {
		_, _ = fmt.Fprintf(w, " (not configured)\n")
		return
	}
	_, _ = fmt.Fprintln(w)
	if sc.RequestThermalProfile != "" {
		_, _ = fmt.Fprintf(w, "  Thermal profile action: %s\n", sc.RequestThermalProfile)
	}
	if sc.PresentationPowerOn == nil {
		_, _ = fmt.Fprintf(w, "  Presentation power-on:  (not configured)\n")
	} else {
		b := sc.PresentationPowerOn
		_, _ = fmt.Fprintf(w, "  Presentation power-on:  action=%s domain=%s provenance=%s\n", b.Action, b.PowerDomain, b.DomainProvenance)
	}
	if sc.PresentationPowerOff == nil {
		_, _ = fmt.Fprintf(w, "  Presentation power-off: (not configured)\n")
		return
	}
	off := sc.PresentationPowerOff
	_, _ = fmt.Fprintf(w, "  Presentation power-off: action=%s domain=%s provenance=%s removalPolicy=%s\n",
		off.Action, off.PowerDomain, off.DomainProvenance, off.RemovalPolicy)
	if off.RemovalPolicy == "immediate" {
		_, _ = fmt.Fprintf(w, "    immediateSafeAttestation: %v\n", off.ImmediateSafeAttestation)
		return
	}
	_, _ = fmt.Fprintf(w, "    prerequisites (%d):\n", len(off.Prerequisites))
	for _, p := range off.Prerequisites {
		switch p.Kind {
		case "delay":
			_, _ = fmt.Fprintf(w, "      - delay: %dms\n", p.DelayMs)
		default:
			_, _ = fmt.Fprintf(w, "      - %s: action=%s requireConfirmation=%v\n", p.Kind, p.Action, p.RequireConfirmation)
		}
	}
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
