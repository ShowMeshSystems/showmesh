package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"
)

type eventsMQTTPayload struct {
	Enabled     bool   `json:"enabled"`
	Broker      string `json:"broker"`
	TopicPrefix string `json:"topicPrefix"`
}

// eventsMQTTConfigResponse is the body of GET and PUT /api/v1/config/events.mqtt.
type eventsMQTTConfigResponse struct {
	ServerTime             time.Time         `json:"serverTime"`
	Kind                   string            `json:"kind"`
	Revision               int64             `json:"revision"`
	Payload                eventsMQTTPayload `json:"payload"`
	UpdatedAt              time.Time         `json:"updatedAt"`
	CreatedByPrincipalID   *string           `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string           `json:"createdByPrincipalName"`
	Source                 string            `json:"source"`
}

const eventsMQTTAPIPath = "/api/v1/config/events.mqtt"

func printEventsMQTTUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl events-mqtt <get|set|revisions> [flags]

Where the coordinator publishes its events and the night state, for Home
Assistant or any other MQTT subscriber. Nothing is published until it is turned
on and a broker is picked.

Subcommands:
  get         show the setting
  set         change it (requires config:write); takes effect within a second
  revisions   list every saved change, newest first
`)
}

func cmdEventsMQTT(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printEventsMQTTUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printEventsMQTTUsage(stdout)
		return exitOK
	case "get":
		return cmdEventsMQTTGet(rest, stdout, stderr, clock)
	case "set":
		return cmdEventsMQTTSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdEventsMQTTRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl events-mqtt: unknown subcommand %q\n\n", sub)
		printEventsMQTTUsage(stderr)
		return exitUsage
	}
}

func cmdEventsMQTTGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "events-mqtt get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl events-mqtt get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow where events are published (GET "+eventsMQTTAPIPath+").")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(fs.Args()) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp eventsMQTTConfigResponse
	if err := c.getJSON(ctx, eventsMQTTAPIPath, nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	return printEventsMQTTResult(stdout, stderr, name, g, resp, clock)
}

func cmdEventsMQTTRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "events-mqtt revisions"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl events-mqtt revisions [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList every saved change, newest first (GET "+eventsMQTTAPIPath+"/revisions).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(fs.Args()) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, eventsMQTTAPIPath+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func cmdEventsMQTTSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "events-mqtt set"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var enabled bool
	var broker, prefix string
	fs.BoolVar(&enabled, "enabled", true, "whether events are published; --enabled=false turns it off")
	fs.StringVar(&broker, "broker", "", "the id of the integration broker to publish to")
	fs.StringVar(&prefix, "topic-prefix", "", "the first part of every topic, showmesh by default")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl events-mqtt set [--enabled=true|false] [--broker id] [--topic-prefix prefix] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nChange where events are published (requires config:write). Only the flags")
		_, _ = fmt.Fprintln(stderr, "you pass are changed. The running publisher follows within a second.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "setting changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(fs.Args()) > 0 {
		fs.Usage()
		return exitUsage
	}
	body := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "enabled":
			body["enabled"] = enabled
		case "broker":
			body["broker"] = broker
		case "topic-prefix":
			body["topicPrefix"] = prefix
		}
	})
	if len(body) == 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl events-mqtt set: pass at least one of --enabled, --broker, --topic-prefix")
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r eventsMQTTConfigResponse
		if err := c.getJSON(ctx, eventsMQTTAPIPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, name, err)
	}
	// Nothing saved yet is revision 0, which If-Match cannot name.
	if ifMatch == ifMatchHeaderValue(0) {
		ifMatch = ""
	}
	var resp eventsMQTTConfigResponse
	if err := c.putJSON(ctx, eventsMQTTAPIPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	return printEventsMQTTResult(stdout, stderr, name, g, resp, clock)
}

func printEventsMQTTResult(stdout, stderr io.Writer, name string, g *globalFlags, resp eventsMQTTConfigResponse, clock func() time.Time) int {
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	publishing := "off"
	if resp.Payload.Enabled {
		publishing = "on"
	}
	broker := "-"
	if resp.Payload.Broker != "" {
		broker = resp.Payload.Broker
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintf(tw, "publishing:\t%s\n", publishing)
	_, _ = fmt.Fprintf(tw, "broker:\t%s\n", broker)
	_, _ = fmt.Fprintf(tw, "topicPrefix:\t%s\n", resp.Payload.TopicPrefix)
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_, _ = fmt.Fprintf(tw, "source:\t%s\n", resp.Source)
	_, _ = fmt.Fprintf(tw, "changedBy:\t%s\n", stringOrDash(resp.CreatedByPrincipalName))
	_ = tw.Flush()
	return exitOK
}
