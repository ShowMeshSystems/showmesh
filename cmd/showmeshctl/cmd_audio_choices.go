package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type audioRoutingChoicesResponse struct {
	ServerTime  time.Time `json:"serverTime"`
	NodeID      string    `json:"nodeId"`
	Discovery   string    `json:"discovery"`
	Reason      string    `json:"reason,omitempty"`
	ManualEntry struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason,omitempty"`
	} `json:"manualEntry"`
	LTC struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason,omitempty"`
	} `json:"ltc"`
	Routes []struct {
		Route         string `json:"route"`
		Interface     string `json:"interface"`
		Source        string `json:"source"`
		Channels      int    `json:"channels"`
		ChannelBasis  string `json:"channelBasis"`
		LTCCapable    bool   `json:"ltcCapable"`
		LTCReason     string `json:"ltcReason,omitempty"`
		ProgramGroups []struct {
			Channels    []int `json:"channels"`
			LTCChannels []int `json:"ltcChannels"`
			Conflicts   []struct {
				Channel int    `json:"channel"`
				Reason  string `json:"reason"`
			} `json:"conflicts"`
		} `json:"programGroups"`
	} `json:"routes"`
	Current *struct {
		ProgramRoute    string `json:"programRoute"`
		ProgramChannels []int  `json:"programChannels"`
		LTCChannel      int    `json:"ltcChannel,omitempty"`
		Offered         bool   `json:"offered"`
		Reason          string `json:"reason,omitempty"`
	} `json:"current,omitempty"`
	Clock *struct {
		LocalClock   string `json:"localClock"`
		Source       string `json:"source"`
		Verification string `json:"verification"`
	} `json:"clock,omitempty"`
}

func cmdAudioNodeChoices(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl audio node choices", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl audio node choices [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow the program and LTC channels this node's own outputs offer (GET /api/v1/nodes/{nodeId}/audio/routing-choices).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "audio node choices", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "audio node choices", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp audioRoutingChoicesResponse
	if err := c.getJSON(ctx, "/api/v1/nodes/"+url.PathEscape(rest[0])+"/audio/routing-choices", nil, &resp); err != nil {
		return reportError(stderr, "audio node choices", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "audio node choices", err)
		}
		return exitOK
	}
	printAudioRoutingChoices(stdout, resp)
	return exitOK
}

func printAudioRoutingChoices(w io.Writer, resp audioRoutingChoicesResponse) {
	_, _ = fmt.Fprintf(w, "Node ID:                %s\n", resp.NodeID)
	_, _ = fmt.Fprintf(w, "Discovery:              %s\n", resp.Discovery)
	if resp.Reason != "" {
		_, _ = fmt.Fprintf(w, "                        %s\n", resp.Reason)
	}
	_, _ = fmt.Fprintf(w, "LTC:                    %s\n", availability(resp.LTC.Available, "available", resp.LTC.Reason))
	_, _ = fmt.Fprintf(w, "Manual entry:           %s\n", availability(resp.ManualEntry.Allowed, "allowed", resp.ManualEntry.Reason))
	if resp.Current != nil {
		cur := resp.Current
		ltc := "none"
		if cur.LTCChannel != 0 {
			ltc = strconv.Itoa(cur.LTCChannel)
		}
		_, _ = fmt.Fprintf(w, "Saved:                  %s program %s, LTC %s\n", cur.ProgramRoute, joinInts(cur.ProgramChannels), ltc)
		_, _ = fmt.Fprintf(w, "                        %s\n", availability(cur.Offered, "one of the choices below", cur.Reason))
	}
	if resp.Clock != nil {
		_, _ = fmt.Fprintf(w, "Local clock:            %s (%s, %s)\n", resp.Clock.LocalClock, resp.Clock.Source, resp.Clock.Verification)
	}
	if len(resp.Routes) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w)
	tw := newTabWriter(w)
	_, _ = fmt.Fprintln(tw, "ROUTE\tSOURCE\tCHANNELS\tPROGRAM\tLTC CHANNELS")
	for _, r := range resp.Routes {
		count := strconv.Itoa(r.Channels)
		if r.ChannelBasis == "atLeast" {
			count = "at least " + count
		}
		for i, g := range r.ProgramGroups {
			route, source, channels := r.Route, r.Source, count
			if i > 0 {
				route, source, channels = "", "", ""
			}
			ltc := joinInts(g.LTCChannels)
			if ltc == "" {
				ltc = "none"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", route, source, channels, joinInts(g.Channels), ltc)
		}
	}
	_ = tw.Flush()
}

func availability(ok bool, yes, reason string) string {
	if ok {
		return yes
	}
	return reason
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}
