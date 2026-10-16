package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

func printPreviewCamera(w io.Writer, resp previewCameraConfigResponse) {
	tw := newTabWriter(w)
	_, _ = fmt.Fprintf(tw, "camera:\t%s\n", resp.ID)
	_, _ = fmt.Fprintf(tw, "name:\t%s\n", resp.Payload.Name)
	_, _ = fmt.Fprintf(tw, "enabled:\t%t\n", resp.Payload.Enabled)
	_, _ = fmt.Fprintf(tw, "address:\t%s\n", emptyOrDash(resp.Payload.URLDisplay))
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_ = tw.Flush()
}

func cmdPreviewGatewayGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview gateway get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview gateway get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the preview gateway address and whether a token is stored (GET "+previewGatewayAPIPath+").")
		_, _ = fmt.Fprintln(stderr, "The token itself is never shown.")
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

	var resp previewGatewayConfigResponse
	if err := c.getJSON(ctx, previewGatewayAPIPath, nil, &resp); err != nil {
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

func cmdPreviewCameraList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview camera list"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview camera list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nList the cameras (GET "+previewCameraAPIPath+").")
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
	if err := c.getJSON(ctx, previewCameraAPIPath, nil, &resp); err != nil {
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
		_, _ = fmt.Fprintln(stdout, "(no cameras)")
		return exitOK
	}
	tw := newTabWriter(stdout)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tREVISION\tUPDATED")
	for _, o := range resp.Objects {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", o.ID, o.Label, o.CurrentRevision, o.UpdatedAt.Format(time.RFC3339))
	}
	_ = tw.Flush()
	return exitOK
}

func cmdPreviewCameraGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview camera get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview camera get <id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one camera (GET "+previewCameraAPIPath+"/{id}). The address shows only its host and port.")
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

	var resp previewCameraConfigResponse
	if err := c.getJSON(ctx, previewCameraAPIPath+"/"+url.PathEscape(pos[0]), nil, &resp); err != nil {
		return reportError(stderr, name, err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, name, err)
		}
		return exitOK
	}
	printPreviewCamera(stdout, resp)
	return exitOK
}

func cmdPreviewLayoutGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	const name = "preview layout get"
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl preview layout get [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow the saved tile order and hidden tiles (GET "+dashboardLayoutAPIPath+").")
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

	var resp previewLayoutResponse
	if err := c.getJSON(ctx, dashboardLayoutAPIPath, nil, &resp); err != nil {
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
	_, _ = fmt.Fprintf(tw, "order:\t%s\n", joinOrDash(resp.Payload.Previews.Order))
	_, _ = fmt.Fprintf(tw, "hidden:\t%s\n", joinOrDash(resp.Payload.Previews.Hidden))
	_, _ = fmt.Fprintf(tw, "revision:\t%d\n", resp.Revision)
	_ = tw.Flush()
	return exitOK
}

// cmdPreviewRevisions lists the saved changes of one preview configuration kind; perObject adds the id argument.
func cmdPreviewRevisions(name, basePath string, perObject bool, args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl "+name, stderr)
	fs.Usage = func() {
		if perObject {
			_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s <id>\n", name)
		} else {
			_, _ = fmt.Fprintf(stderr, "usage: showmeshctl %s [flags]\n", name)
		}
		_, _ = fmt.Fprintln(stderr, "\nList the saved changes, newest first (GET "+basePath+"/revisions).")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, name, err)
	}
	want := 0
	path := basePath + "/revisions"
	if perObject {
		want = 1
		if len(pos) == 1 {
			path = basePath + "/" + url.PathEscape(pos[0]) + "/revisions"
		}
	}
	if len(pos) != want {
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
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
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
