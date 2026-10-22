package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"
)

const integrationBrokerAPIPath = "/api/v1/config/integration.broker"

type integrationBroker struct {
	ID                     string  `json:"id"`
	URL                    string  `json:"url"`
	Username               string  `json:"username"`
	PasswordSet            bool    `json:"passwordSet"`
	BuiltIn                bool    `json:"builtIn"`
	TopicPrefix            string  `json:"topicPrefix"`
	Revision               int64   `json:"revision"`
	UpdatedAt              *string `json:"updatedAt"`
	CreatedByPrincipalName *string `json:"createdByPrincipalName"`
	Source                 string  `json:"source"`
}

type integrationBrokersListResponse struct {
	ServerTime   time.Time           `json:"serverTime"`
	Brokers      []integrationBroker `json:"brokers"`
	EditsBlocked string              `json:"editsBlocked"`
}

type integrationBrokerResponse struct {
	ServerTime time.Time         `json:"serverTime"`
	Broker     integrationBroker `json:"broker"`
}

func printMQTTBrokerUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl mqtt-broker <list|get|revisions|set|remove> [flags]

The MQTT brokers an MQTT action can publish to. The built-in broker is listed
as "built-in" and cannot be changed; actions on it must use topics under the
prefix it shows.

  list                                   list the brokers
  get <id>                               show one broker
  revisions <id>                         list one broker's saved changes, newest first
  set <id> --url <url> [--username <name>] [--password-file <path>|--clear-password]
                                         add or change a broker (requires config:write)
  remove <id>                            remove a broker no action uses (requires config:write)

The password is a secret: it is read from a file ("-" for standard input) and
never shown again.
`)
}

func cmdMQTTBroker(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printMQTTBrokerUsage(stderr)
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "list":
		return cmdMQTTBrokerList(args[1:], stdout, stderr, clock)
	case "get":
		return cmdMQTTBrokerGet(args[1:], stdout, stderr, clock)
	case "revisions":
		return cmdPreviewRevisions("mqtt-broker revisions", integrationBrokerAPIPath, true, args[1:], stdout, stderr, clock)
	case "set":
		return cmdMQTTBrokerSet(args[1:], stdout, stderr, clock)
	case "remove":
		return cmdMQTTBrokerRemove(args[1:], stdout, stderr)
	default:
		printMQTTBrokerUsage(stderr)
		return exitUsage
	}
}

func cmdMQTTBrokerList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "mqtt-broker list"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl mqtt-broker list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList the MQTT brokers actions can use (GET "+integrationBrokerAPIPath+").")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(pos) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp integrationBrokersListResponse
	if err := c.getJSON(ctx, integrationBrokerAPIPath, nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	if len(resp.Brokers) == 0 {
		_, _ = fmt.Fprintln(stdout, "(no brokers)")
	} else {
		tw := newTabWriter(stdout)
		_, _ = fmt.Fprintln(tw, "ID\tURL\tUSERNAME\tPASSWORD\tTOPICS")
		for _, b := range resp.Brokers {
			topics := "any"
			if b.TopicPrefix != "" {
				topics = b.TopicPrefix + "..."
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", b.ID, b.URL, dashIfEmpty(b.Username), setOrNot(b.PasswordSet), topics)
		}
		_ = tw.Flush()
	}
	if resp.EditsBlocked != "" {
		_, _ = fmt.Fprintln(stdout, resp.EditsBlocked)
	}
	return exitOK
}

func cmdMQTTBrokerGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "mqtt-broker get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl mqtt-broker get <id> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow one MQTT broker (GET "+integrationBrokerAPIPath+"/{id}).")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp integrationBrokerResponse
	if err := c.getJSON(ctx, integrationBrokerAPIPath+"/"+url.PathEscape(pos[0]), nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	printIntegrationBroker(stdout, resp.Broker)
	return exitOK
}

func cmdMQTTBrokerSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "mqtt-broker set"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var brokerURL, username, passwordFile string
	var clearPassword bool
	fs.StringVar(&brokerURL, "url", "", "the broker address, such as tcp://10.0.0.5:1883")
	fs.StringVar(&username, "username", "", "the login ShowMesh uses on this broker")
	fs.StringVar(&passwordFile, "password-file", "", "file holding the login's password; \"-\" reads standard input")
	fs.BoolVar(&clearPassword, "clear-password", false, "remove the stored password")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl mqtt-broker set <id> --url <url> [--username <name>] [--password-file <path>|--clear-password] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nAdd an MQTT broker, or change an existing one (requires config:write). Only")
		_, _ = fmt.Fprintln(stderr, "what you pass is changed. The password is never shown again.")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if len(pos) != 1 || (passwordFile != "" && clearPassword) ||
		(!visited["url"] && !visited["username"] && passwordFile == "" && !clearPassword) {
		fs.Usage()
		return exitUsage
	}
	body := map[string]any{}
	if visited["url"] {
		body["url"] = brokerURL
	}
	if visited["username"] {
		body["username"] = username
	}
	if clearPassword {
		body["password"] = nil
	}
	if passwordFile != "" {
		password, err := readSecretFile(passwordFile)
		if err != nil {
			return reportError(stderr, name, err)
		}
		body["password"] = password
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := integrationBrokerAPIPath + "/" + url.PathEscape(pos[0])
	rev, revSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), rev, revSet, 0, func() (int64, error) {
		var r integrationBrokerResponse
		if err := c.getJSON(ctx, path, nil, &r); err != nil {
			return 0, err
		}
		return r.Broker.Revision, nil
	})
	if err != nil {
		return reportError(stderr, name, err)
	}
	var resp integrationBrokerResponse
	if err := c.putJSON(ctx, path, ifMatch, body, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	printIntegrationBroker(stdout, resp.Broker)
	return exitOK
}

func cmdMQTTBrokerRemove(args []string, stdout, stderr io.Writer) int {
	const name = "mqtt-broker remove"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl mqtt-broker remove <id>")
		_, _ = fmt.Fprintln(stderr, "\nRemove an MQTT broker and its stored password (requires config:write). It is")
		_, _ = fmt.Fprintln(stderr, "refused while any action still uses the broker.")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	if err := c.deleteJSON(ctx, integrationBrokerAPIPath+"/"+url.PathEscape(pos[0]), configObjectDeleteRequest{Confirm: true}, nil); err != nil {
		return reportError(stderr, name, err)
	}
	_, _ = fmt.Fprintf(stdout, "broker %s removed\n", pos[0])
	return exitOK
}

func printIntegrationBroker(w io.Writer, b integrationBroker) {
	tw := newTabWriter(w)
	_, _ = fmt.Fprintf(tw, "id:\t%s\n", b.ID)
	_, _ = fmt.Fprintf(tw, "url:\t%s\n", b.URL)
	_, _ = fmt.Fprintf(tw, "username:\t%s\n", dashIfEmpty(b.Username))
	_, _ = fmt.Fprintf(tw, "password:\t%s\n", setOrNot(b.PasswordSet))
	if b.TopicPrefix != "" {
		_, _ = fmt.Fprintf(tw, "topics:\tmust start with %s\n", b.TopicPrefix)
	}
	if b.BuiltIn {
		_, _ = fmt.Fprintln(tw, "built-in:\ttrue")
	} else {
		_, _ = fmt.Fprintf(tw, "revision:\t%d\n", b.Revision)
	}
	_ = tw.Flush()
}

func setOrNot(set bool) string {
	if set {
		return "set"
	}
	return "not set"
}
