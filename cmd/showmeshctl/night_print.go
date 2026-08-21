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
