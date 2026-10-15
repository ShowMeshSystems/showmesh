package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	previewsAPIPath        = "/api/v1/previews"
	previewGatewayAPIPath  = "/api/v1/config/preview.gateway"
	previewCameraAPIPath   = "/api/v1/config/preview.camera"
	dashboardLayoutAPIPath = "/api/v1/config/dashboard.layout"
)

type previewsResponse struct {
	ServerTime time.Time `json:"serverTime"`
	Gateway    struct {
		State      string    `json:"state"`
		Message    string    `json:"message"`
		ObservedAt time.Time `json:"observedAt"`
	} `json:"gateway"`
	Tiles []previewTile `json:"tiles"`
}

type previewTile struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	State   string `json:"state"`
	Message string `json:"message"`
	Hidden  bool   `json:"hidden"`

	ObservedAt time.Time `json:"observedAt"`
}

// previewSnapshotSaved is what "preview snapshot" reports once the frame is on disk.
type previewSnapshotSaved struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// previewConfigMeta is the revision bookkeeping every preview configuration response carries.
type previewConfigMeta struct {
	Kind                   string    `json:"kind"`
	UpdatedAt              time.Time `json:"updatedAt"`
	CreatedByPrincipalID   *string   `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string   `json:"createdByPrincipalName"`
	Source                 string    `json:"source"`
}

type previewLayoutResponse struct {
	previewConfigMeta
	ServerTime time.Time `json:"serverTime"`
	Kind       string    `json:"kind"`
	Revision   int64     `json:"revision"`
	Payload    struct {
		Previews struct {
			Order  []string `json:"order"`
			Hidden []string `json:"hidden"`
		} `json:"previews"`
	} `json:"payload"`
}

type previewGatewayConfigResponse struct {
	previewConfigMeta
	ServerTime time.Time `json:"serverTime"`
	Revision   int64     `json:"revision"`
	Payload    struct {
		URL      string `json:"url"`
		TokenSet bool   `json:"tokenSet"`
	} `json:"payload"`
}

type previewCameraConfigResponse struct {
	previewConfigMeta
	ServerTime time.Time `json:"serverTime"`
	ID         string    `json:"id"`
	Revision   int64     `json:"revision"`
	Payload    struct {
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		URLSet     bool   `json:"urlSet"`
		URLDisplay string `json:"urlDisplay"`
	} `json:"payload"`
}

func cmdPreview(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPreviewUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printPreviewUsage(stdout)
		return exitOK
	case "list":
		return cmdPreviewList(rest, stdout, stderr, clock)
	case "snapshot":
		return cmdPreviewSnapshot(rest, stdout, stderr)
	case "gateway":
		return cmdPreviewGateway(rest, stdout, stderr, clock)
	case "camera":
		return cmdPreviewCamera(rest, stdout, stderr, clock)
	case "layout":
		return cmdPreviewLayout(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl preview: unknown subcommand %q\n\n", sub)
		printPreviewUsage(stderr)
		return exitUsage
	}
}

func printPreviewUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl preview <list|snapshot|gateway|camera|layout> [flags]

The dashboard's video previews. A tile key is "ndi:<NDI source name>" or
"camera:<camera id>"; "preview list" shows every key.

Subcommands:
  list                                   show the gateway state and every tile
  snapshot <key> -o <file.jpg>           save the latest frame of one tile
  gateway set --url <url> --token-file <path>
                                         set the preview gateway (requires config:write)
  camera add <id> --name <name> --url-file <path>
                                         add or change a camera (requires config:write)
  camera remove <id>                     remove a camera (requires config:write)
  layout move <key> --to <index>         move a tile; index counts from 0 (requires dashboard:write)
  layout hide <key>                      hide a tile from the dashboard
  layout show <key>                      show a hidden tile again

The gateway token and the camera address are secrets: they are read from a
file, or from standard input when the path is "-", never from a flag value.
`)
}

// parseInterleaved parses flags that may come before or after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// readSecretFile reads a secret from path, or from standard input when path is "-".
func readSecretFile(path string) (string, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", path, err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return secret, nil
}

func cmdPreviewList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview list"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the preview gateway state and every tile (GET "+previewsAPIPath+").")
		fs.PrintDefaults()
	}
	extra, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp previewsResponse
	if err := c.getJSON(ctx, previewsAPIPath, nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "gateway: %s\n", resp.Gateway.State)
	if resp.Gateway.Message != "" {
		_, _ = fmt.Fprintf(stdout, "         %s\n", resp.Gateway.Message)
	}
	if len(resp.Tiles) == 0 {
		return exitOK
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintln(tw, "KEY\tLABEL\tSTATE\tHIDDEN\tMESSAGE")
	for _, t := range resp.Tiles {
		hidden := ""
		if t.Hidden {
			hidden = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Key, t.Label, t.State, hidden, emptyOrDash(t.Message))
	}
	_ = tw.Flush()
	return exitOK
}

func cmdPreviewSnapshot(args []string, stdout, stderr io.Writer) int {
	const name = "preview snapshot"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var out string
	fs.StringVar(&out, "o", "", "file to write the JPEG to (required)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview snapshot <key> -o <file.jpg>")
		_, _ = fmt.Fprintln(stderr, "\nSave the latest frame of one tile (GET "+previewsAPIPath+"/{key}/snapshot).")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(pos) != 1 || out == "" {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	frame, err := c.getRaw(ctx, previewsAPIPath+"/"+url.PathEscape(pos[0])+"/snapshot", nil)
	if err != nil {
		return reportError(stderr, name, err)
	}
	if err := os.WriteFile(out, frame, 0o644); err != nil {
		return reportError(stderr, name, fmt.Errorf("could not write %s: %w", out, err))
	}
	if g.output == outputJSON {
		if err := printJSON(stdout, previewSnapshotSaved{Path: out, Bytes: len(frame)}); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "saved %d bytes to %s\n", len(frame), out)
	return exitOK
}

func cmdPreviewGateway(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 || args[0] != "set" {
		printPreviewUsage(stderr)
		return exitUsage
	}
	const name = "preview gateway set"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var gatewayURL, tokenFile string
	var clearToken bool
	fs.StringVar(&gatewayURL, "url", "", "the gateway's address, e.g. http://192.168.130.176:8095; an empty value turns previews off")
	fs.StringVar(&tokenFile, "token-file", "", "file holding the gateway token; \"-\" reads standard input")
	fs.BoolVar(&clearToken, "clear-token", false, "remove the stored token")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview gateway set [--url <url>] [--token-file <path>] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nSet the preview gateway (requires config:write). Only what you pass is changed.")
		fs.PrintDefaults()
	}
	extra, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if len(extra) > 0 || (!visited["url"] && tokenFile == "" && !clearToken) || (tokenFile != "" && clearToken) {
		fs.Usage()
		return exitUsage
	}
	body := map[string]any{}
	if visited["url"] {
		body["url"] = gatewayURL
	}
	switch {
	case clearToken:
		body["token"] = nil
	case tokenFile != "":
		token, err := readSecretFile(tokenFile)
		if err != nil {
			return reportError(stderr, name, err)
		}
		body["token"] = token
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	rev, revSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), rev, revSet, 0, func() (int64, error) {
		var r previewGatewayConfigResponse
		if err := c.getJSON(ctx, previewGatewayAPIPath, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, name, err)
	}
	var resp previewGatewayConfigResponse
	if err := c.putJSON(ctx, previewGatewayAPIPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	address := resp.Payload.URL
	if address == "" {
		address = "(none: previews are off)"
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintf(tw, "url:\t%s\n", address)
	_, _ = fmt.Fprintf(tw, "tokenSet:\t%t\n", resp.Payload.TokenSet)
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_ = tw.Flush()
	return exitOK
}

func cmdPreviewCamera(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPreviewUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "add":
		return cmdPreviewCameraAdd(args[1:], stdout, stderr, clock)
	case "remove":
		return cmdPreviewCameraRemove(args[1:], stdout, stderr)
	default:
		printPreviewUsage(stderr)
		return exitUsage
	}
}

func cmdPreviewCameraAdd(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview camera add"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var cameraName, urlFile string
	var disabled bool
	fs.StringVar(&cameraName, "name", "", "the camera's name on the dashboard")
	fs.StringVar(&urlFile, "url-file", "", "file holding the camera's rtsp:// address; \"-\" reads standard input")
	fs.BoolVar(&disabled, "disabled", false, "keep the camera but do not show it")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview camera add <id> --name <name> --url-file <path> [flags]")
		_, _ = fmt.Fprintln(stderr, "\nAdd a camera, or change an existing one (requires config:write). The address")
		_, _ = fmt.Fprintln(stderr, "is a secret and is never shown again. Only what you pass is changed.")
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
	if len(pos) != 1 || (!visited["name"] && urlFile == "" && !visited["disabled"]) {
		fs.Usage()
		return exitUsage
	}
	id := pos[0]
	body := map[string]any{}
	if visited["name"] {
		body["name"] = cameraName
	}
	if visited["disabled"] {
		body["enabled"] = !disabled
	}
	if urlFile != "" {
		streamURL, err := readSecretFile(urlFile)
		if err != nil {
			return reportError(stderr, name, err)
		}
		body["url"] = streamURL
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := previewCameraAPIPath + "/" + url.PathEscape(id)
	rev, revSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), rev, revSet, 0, func() (int64, error) {
		var r previewCameraConfigResponse
		if err := c.getJSON(ctx, path, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, name, err)
	}
	var resp previewCameraConfigResponse
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
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintf(tw, "camera:\t%s\n", resp.ID)
	_, _ = fmt.Fprintf(tw, "name:\t%s\n", resp.Payload.Name)
	_, _ = fmt.Fprintf(tw, "enabled:\t%t\n", resp.Payload.Enabled)
	_, _ = fmt.Fprintf(tw, "address:\t%s\n", emptyOrDash(resp.Payload.URLDisplay))
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_ = tw.Flush()
	return exitOK
}

func cmdPreviewCameraRemove(args []string, stdout, stderr io.Writer) int {
	const name = "preview camera remove"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview camera remove <id>")
		_, _ = fmt.Fprintln(stderr, "\nRemove a camera and its stored address (requires config:write). The revision")
		_, _ = fmt.Fprintln(stderr, "history stays readable.")
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

	if err := c.deleteJSON(ctx, previewCameraAPIPath+"/"+url.PathEscape(pos[0]), configObjectDeleteRequest{Confirm: true}, nil); err != nil {
		return reportError(stderr, name, err)
	}
	_, _ = fmt.Fprintf(stdout, "camera %s removed\n", pos[0])
	return exitOK
}

func cmdPreviewLayout(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPreviewUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "move", "hide", "show":
		return cmdPreviewLayoutChange(args[0], args[1:], stdout, stderr, clock)
	default:
		printPreviewUsage(stderr)
		return exitUsage
	}
}

func cmdPreviewLayoutChange(verb string, args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	name := "preview layout " + verb
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	to := -1
	if verb == "move" {
		fs.IntVar(&to, "to", -1, "the new position, counting from 0 (required)")
	}
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		if verb == "move" {
			_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview layout move <key> --to <index>")
			_, _ = fmt.Fprintln(stderr, "\nMove one tile to a new position (requires dashboard:write). Position 0 is first.")
		} else {
			_, _ = fmt.Fprintf(stderr, "usage: showmeshctl preview layout %s <key>\n", verb)
			_, _ = fmt.Fprintf(stderr, "\n%s one tile on the dashboard (requires dashboard:write).\n", map[string]string{"hide": "Hide", "show": "Show again"}[verb])
		}
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	if len(pos) != 1 || (verb == "move" && to < 0) {
		fs.Usage()
		return exitUsage
	}
	key := pos[0]
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var tiles previewsResponse
	if err := c.getJSON(ctx, previewsAPIPath, nil, &tiles); err != nil {
		return reportError(stderr, name, err)
	}
	var layout previewLayoutResponse
	if err := c.getJSON(ctx, dashboardLayoutAPIPath, nil, &layout); err != nil {
		var ce *cliError
		if !errors.As(err, &ce) || ce.code != exitNotFound {
			return reportError(stderr, name, err)
		}
	}
	order := make([]string, 0, len(tiles.Tiles))
	known := false
	for _, t := range tiles.Tiles {
		order = append(order, t.Key)
		known = known || t.Key == key
	}
	hidden := slices.Clone(layout.Payload.Previews.Hidden)
	if !known && (verb != "show" || !slices.Contains(hidden, key)) {
		return reportError(stderr, name, fmt.Errorf("no tile has the key %q; run \"showmeshctl preview list\" to see the keys", key))
	}

	body := map[string]any{}
	switch verb {
	case "move":
		if to >= len(order) {
			return reportError(stderr, name, fmt.Errorf("position %d is past the last tile; use 0 to %d", to, len(order)-1))
		}
		order = slices.DeleteFunc(order, func(k string) bool { return k == key })
		order = slices.Insert(order, to, key)
		body["previews"] = map[string]any{"order": order}
	case "hide":
		if !slices.Contains(hidden, key) {
			hidden = append(hidden, key)
		}
		body["previews"] = map[string]any{"hidden": hidden}
	case "show":
		hidden = slices.DeleteFunc(hidden, func(k string) bool { return k == key })
		if hidden == nil {
			hidden = []string{}
		}
		body["previews"] = map[string]any{"hidden": hidden}
	}

	rev, revSet := ifMatchFlag()
	ifMatch := ""
	if layout.Revision > 0 || revSet {
		if ifMatch, err = resolveIfMatch(forceFlag(), rev, revSet, layout.Revision, nil); err != nil {
			return reportError(stderr, name, err)
		}
	}
	var resp previewLayoutResponse
	if err := c.putJSON(ctx, dashboardLayoutAPIPath, ifMatch, body, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "layout saved at revision %d\n", resp.Revision)
	return exitOK
}
