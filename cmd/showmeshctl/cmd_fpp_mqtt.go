package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// Track G seam G-3's showmeshctl surface (ADR-039): `fpp-mqtt get|set`,
// over GET/PUT /api/v1/config/fpp.mqtt and GET
// /api/v1/config/fpp.mqtt/revisions. A dedicated top-level command,
// mirroring `resolume instance`'s identical choice
// (cmd_resolume_instance.go) — not folded into `showmeshctl config`
// (fpp.endpoints-specific) or under `fpp <verb>` (playback command
// dispatch, unrelated to configuration).
//
// "set" is a PARTIAL UPDATE: only flags the operator actually passes on
// the command line are sent, matching the API's own absent-key-keeps-
// stored-value contract (ADR-039 decision 5) — required for the
// credential rule (decision 7): GET never returns the password, so
// re-submitting every flag on every call would otherwise erase it.

type fppMQTTConfigResponse struct {
	ServerTime             time.Time      `json:"serverTime"`
	Kind                   string         `json:"kind"`
	Revision               int64          `json:"revision"`
	Payload                fppMQTTPayload `json:"payload"`
	UpdatedAt              time.Time      `json:"updatedAt"`
	CreatedByPrincipalID   *string        `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string        `json:"createdByPrincipalName"`
	Source                 string         `json:"source"`
	RestartRequired        bool           `json:"restartRequired"`
	RestartRequiredReason  string         `json:"restartRequiredReason"`
}

type fppMQTTPayload struct {
	BrokerURL   string            `json:"brokerURL"`
	Username    string            `json:"username"`
	TopicPrefix string            `json:"topicPrefix"`
	Hosts       map[string]string `json:"hosts"`
	PasswordSet bool              `json:"passwordSet"`
}

func cmdFPPMQTT(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printFPPMQTTUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printFPPMQTTUsage(stdout)
		return exitOK
	case "get":
		return cmdFPPMQTTGet(rest, stdout, stderr, clock)
	case "set":
		return cmdFPPMQTTSet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl fpp-mqtt: unknown subcommand %q\n\n", sub)
		printFPPMQTTUsage(stderr)
		return exitUsage
	}
}

func printFPPMQTTUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl fpp-mqtt <subcommand> [flags]

Read or write the coordinator's fpp.mqtt configuration (Track G seam G-3,
ADR-039): the Step 5 FPP MQTT collector's broker, credentials, topic
prefix, and host map, moved out of SHOWMESH_FPP_MQTT_* into the
coordinator's authoritative store. Every subcommand requires the
config:write scope (admin only).

Subcommands:
  get   show the active configuration (the broker password is never
        returned — "passwordSet" reports only whether one is stored)
  set   write a new configuration revision, changing only the fields
        named on the command line; every other field, including a
        previously stored password, is left exactly as it was

A configuration change here takes effect without a restart (ADR-036): the
FPP MQTT collector follows within about ten seconds.

While SHOWMESH_FPP_MQTT_BROKER_URL is still set in the coordinator's own
environment, "set" is refused outright (409): remove
SHOWMESH_FPP_MQTT_BROKER_URL, SHOWMESH_FPP_MQTT_USERNAME,
SHOWMESH_FPP_MQTT_PASSWORD, SHOWMESH_FPP_MQTT_TOPIC_PREFIX, and
SHOWMESH_FPP_MQTT_HOSTS from the coordinator's environment and restart the
coordinator once, then retry.

Run "showmeshctl fpp-mqtt <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdFPPMQTTGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp-mqtt get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp-mqtt get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the active fpp.mqtt configuration. The broker password is never")
		_, _ = fmt.Fprintln(stderr, "returned; \"passwordSet\" reports only whether one is stored.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp-mqtt get", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp-mqtt get", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp fppMQTTConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/fpp.mqtt", nil, &resp); err != nil {
		return reportError(stderr, "fpp-mqtt get", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp-mqtt get", err)
		}
		return exitOK
	}
	printFPPMQTTConfig(stdout, resp)
	return exitOK
}

// hostFlag accumulates repeated --host id=HostName flags into a map, the
// same shape SHOWMESH_FPP_MQTT_HOSTS' id=HostName,id2=HostName2 syntax
// carries per entry.
type hostFlag struct {
	m map[string]string
}

func (h *hostFlag) String() string {
	if h == nil || len(h.m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(h.m))
	for id, name := range h.m {
		parts = append(parts, id+"="+name)
	}
	return strings.Join(parts, ",")
}

func (h *hostFlag) Set(value string) error {
	id, name, ok := strings.Cut(value, "=")
	if !ok || id == "" || name == "" {
		return fmt.Errorf("must have the form id=HostName, got %q", value)
	}
	if h.m == nil {
		h.m = map[string]string{}
	}
	h.m[id] = name
	return nil
}

func cmdFPPMQTTSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl fpp-mqtt set", stderr)
	var (
		brokerURL, username, topicPrefix, password string
		hosts                                      hostFlag
		clearHosts, clearPassword                  bool
	)
	fs.StringVar(&brokerURL, "broker-url", "", `the MQTT broker URL, e.g. tcp://broker:1883 (pass "" to clear it)`)
	fs.StringVar(&username, "username", "", `the broker username (pass "" to clear it)`)
	fs.StringVar(&topicPrefix, "topic-prefix", "", `the topic root FPP publishes under (pass "" to reset to the default)`)
	fs.Var(&hosts, "host", "id=HostName, repeatable; adds or replaces one entry in the stored host map")
	fs.BoolVar(&clearHosts, "clear-hosts", false, "configure zero hosts (--host alone only adds/replaces entries)")
	fs.StringVar(&password, "password", "", "the broker password")
	fs.BoolVar(&clearPassword, "clear-password", false, "remove the stored broker password")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl fpp-mqtt set [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new fpp.mqtt configuration revision, changing only the fields")
		_, _ = fmt.Fprintln(stderr, "named below (requires config:write, admin only). A field never named on")
		_, _ = fmt.Fprintln(stderr, "the command line keeps its currently stored value (ADR-039 decision 5) —")
		_, _ = fmt.Fprintln(stderr, "in particular, omitting --password leaves a previously stored password")
		_, _ = fmt.Fprintln(stderr, "untouched, since \"fpp-mqtt get\" never returns it to re-submit.")
		_, _ = fmt.Fprintln(stderr, "\nSends If-Match by default (a fresh read), refusing with a 409 if the")
		_, _ = fmt.Fprintln(stderr, "configuration changed since it was read.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "fpp-mqtt set", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	if hosts.m != nil && clearHosts {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp-mqtt set: --host and --clear-hosts are mutually exclusive")
		fs.Usage()
		return exitUsage
	}
	if password != "" && clearPassword {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp-mqtt set: --password and --clear-password are mutually exclusive")
		fs.Usage()
		return exitUsage
	}

	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	body := map[string]any{}
	if visited["broker-url"] {
		body["brokerURL"] = brokerURL
	}
	if visited["username"] {
		body["username"] = username
	}
	if visited["topic-prefix"] {
		body["topicPrefix"] = topicPrefix
	}
	if clearHosts {
		body["hosts"] = map[string]string{}
	} else if hosts.m != nil {
		body["hosts"] = hosts.m
	}
	if clearPassword {
		body["password"] = nil
	} else if visited["password"] {
		body["password"] = password
	}

	if len(body) == 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl fpp-mqtt set: no fields named; pass at least one flag")
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "fpp-mqtt set", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	const apiPath = "/api/v1/config/fpp.mqtt"
	ifMatchRevision, ifMatchSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), ifMatchRevision, ifMatchSet, 0, func() (int64, error) {
		var r fppMQTTConfigResponse
		if err := c.getJSON(ctx, apiPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, "fpp-mqtt set", err)
	}

	var resp fppMQTTConfigResponse
	if err := c.putJSON(ctx, apiPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, "fpp-mqtt set", err)
	}

	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "fpp-mqtt set", err)
		}
		return exitOK
	}
	printFPPMQTTConfig(stdout, resp)
	_, _ = fmt.Fprintf(stderr, "\nshowmeshctl fpp-mqtt set: revision %d is now active. %s\n", resp.Revision, resp.RestartRequiredReason)
	return exitOK
}
