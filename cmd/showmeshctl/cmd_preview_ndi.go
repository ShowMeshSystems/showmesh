package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"
)

const previewNDIAPIPath = "/api/v1/config/preview.ndi"

type previewNDIConfigResponse struct {
	previewConfigMeta
	ServerTime time.Time         `json:"serverTime"`
	ID         string            `json:"id"`
	Revision   int64             `json:"revision"`
	Payload    previewNDIPayload `json:"payload"`
}

type previewNDIPayload struct {
	Label   string `json:"label"`
	NDIName string `json:"ndiName"`
	Address string `json:"address"`
	Enabled bool   `json:"enabled"`
}

func printPreviewNDI(w io.Writer, resp previewNDIConfigResponse) {
	tw := newTabWriter(w)
	_, _ = fmt.Fprintf(tw, "ndi source:\t%s\n", resp.ID)
	_, _ = fmt.Fprintf(tw, "label:\t%s\n", emptyOrDash(resp.Payload.Label))
	_, _ = fmt.Fprintf(tw, "ndi name:\t%s\n", resp.Payload.NDIName)
	_, _ = fmt.Fprintf(tw, "address:\t%s\n", emptyOrDash(resp.Payload.Address))
	_, _ = fmt.Fprintf(tw, "enabled:\t%t\n", resp.Payload.Enabled)
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_ = tw.Flush()
}

func cmdPreviewNDI(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printPreviewUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return cmdPreviewNDIList(args[1:], stdout, stderr, clock)
	case "get":
		return cmdPreviewNDIGet(args[1:], stdout, stderr, clock)
	case "revisions":
		return cmdPreviewRevisions("preview ndi revisions", previewNDIAPIPath, true, args[1:], stdout, stderr, clock)
	case "add":
		return cmdPreviewNDIAdd(args[1:], stdout, stderr, clock)
	case "remove":
		return cmdPreviewNDIRemove(args[1:], stdout, stderr)
	default:
		printPreviewUsage(stderr)
		return exitUsage
	}
}

func cmdPreviewNDIList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview ndi list"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview ndi list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList the NDI sources added by hand (GET "+previewNDIAPIPath+").")
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

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, previewNDIAPIPath, nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	if len(resp.Objects) == 0 {
		_, _ = fmt.Fprintln(stdout, "(no NDI sources added by hand)")
		return exitOK
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintln(tw, "ID\tLABEL\tREVISION\tUPDATED")
	for _, o := range resp.Objects {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", o.ID, o.Label, o.CurrentRevision, o.UpdatedAt.Format(time.RFC3339))
	}
	_ = tw.Flush()
	return exitOK
}

func cmdPreviewNDIGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview ndi get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview ndi get <id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one NDI source added by hand (GET "+previewNDIAPIPath+"/{id}).")
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

	var resp previewNDIConfigResponse
	if err := c.getJSON(ctx, previewNDIAPIPath+"/"+url.PathEscape(pos[0]), nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	printPreviewNDI(stdout, resp)
	return exitOK
}

func cmdPreviewNDIAdd(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview ndi add"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	var ndiName, label, address string
	var disabled bool
	fs.StringVar(&ndiName, "ndi-name", "", "the full name the sender announces, such as \"ARENA (Composition)\"")
	fs.StringVar(&label, "label", "", "the tile's name on the dashboard; empty uses the NDI name")
	fs.StringVar(&address, "address", "", "the sender's host:port, for a source discovery cannot reach; empty finds it by name")
	fs.BoolVar(&disabled, "disabled", false, "keep the source but do not show it")
	ifMatchFlag, forceFlag := registerIfMatchFlags(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview ndi add <id> --ndi-name <name> [--label <label>] [--address <host:port>] [flags]")
		_, _ = fmt.Fprintln(stderr, "\nAdd an NDI source by hand, or change one (requires config:write). Only what")
		_, _ = fmt.Fprintln(stderr, "you pass is changed.")
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
	if len(pos) != 1 || (!visited["ndi-name"] && !visited["label"] && !visited["address"] && !visited["disabled"]) {
		fs.Usage()
		return exitUsage
	}
	id := pos[0]
	body := map[string]any{}
	if visited["ndi-name"] {
		body["ndiName"] = ndiName
	}
	if visited["label"] {
		body["label"] = label
	}
	if visited["address"] {
		body["address"] = address
	}
	if visited["disabled"] {
		body["enabled"] = !disabled
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	path := previewNDIAPIPath + "/" + url.PathEscape(id)
	rev, revSet := ifMatchFlag()
	ifMatch, err := resolveIfMatch(forceFlag(), rev, revSet, 0, func() (int64, error) {
		var r previewNDIConfigResponse
		if err := c.getJSON(ctx, path, nil, &r); err != nil {
			return 0, err
		}
		return r.Revision, nil
	})
	if err != nil {
		return reportError(stderr, name, err)
	}
	var resp previewNDIConfigResponse
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
	printPreviewNDI(stdout, resp)
	return exitOK
}

func cmdPreviewNDIRemove(args []string, stdout, stderr io.Writer) int {
	const name = "preview ndi remove"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview ndi remove <id>")
		_, _ = fmt.Fprintln(stderr, "\nRemove an NDI source added by hand (requires config:write). The revision")
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

	if err := c.deleteJSON(ctx, previewNDIAPIPath+"/"+url.PathEscape(pos[0]), configObjectDeleteRequest{Confirm: true}, nil); err != nil {
		return reportError(stderr, name, err)
	}
	_, _ = fmt.Fprintf(stdout, "ndi source %s removed\n", pos[0])
	return exitOK
}
