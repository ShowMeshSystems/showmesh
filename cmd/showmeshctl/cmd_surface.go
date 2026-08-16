package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"
)

// showmeshctl surface for the "show.surface" configuration kind: list
// (optional --show filter), get, full-replacement set, revisions.
// Declares its own wire types, matching cmd_show.go's reasoning.
//
// "surface set" is a full replacement: ShowSurfacePayload has no
// optional/defaulted field, so every flag except --pixel-format is
// required and this command never reads the current definition first.

// configShowSurfaceChannelRange mirrors v1.ConfigShowSurfaceChannelRange.
type configShowSurfaceChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

// configShowSurfaceGeometry mirrors v1.ConfigShowSurfaceGeometry.
type configShowSurfaceGeometry struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pixelFormat"`
}

// configShowSurfaceNDIOutput mirrors v1.ConfigShowSurfaceNDIOutput.
type configShowSurfaceNDIOutput struct {
	SourceName string `json:"sourceName"`
}

// configShowSurfaceHDMI mirrors v1.ConfigShowSurfaceHDMI.
type configShowSurfaceHDMI struct {
	Display string `json:"display"`
}

// configShowSurfaceOutput mirrors v1.ConfigShowSurfaceOutput.
type configShowSurfaceOutput struct {
	Transport string                      `json:"transport"`
	NDI       *configShowSurfaceNDIOutput `json:"ndi,omitempty"`
	HDMI      *configShowSurfaceHDMI      `json:"hdmi,omitempty"`
}

// configShowSurface mirrors v1.ConfigShowSurface: the "show.surface"
// configuration kind's decoded payload, shared by the request and the
// response — every field is required on write.
type configShowSurface struct {
	Show         string                        `json:"show"`
	Name         string                        `json:"name"`
	Node         string                        `json:"node"`
	ChannelRange configShowSurfaceChannelRange `json:"channelRange"`
	Geometry     configShowSurfaceGeometry     `json:"geometry"`
	FrameRate    int                           `json:"frameRate"`
	Output       configShowSurfaceOutput       `json:"output"`
}

// showSurfaceConfigResponse is the body of GET and PUT
// /config/show.surface/{id}.
type showSurfaceConfigResponse struct {
	ServerTime             time.Time         `json:"serverTime"`
	Kind                   string            `json:"kind"`
	ID                     string            `json:"id"`
	Revision               int64             `json:"revision"`
	Payload                configShowSurface `json:"payload"`
	UpdatedAt              time.Time         `json:"updatedAt"`
	CreatedByPrincipalID   *string           `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string           `json:"createdByPrincipalName"`
	Source                 string            `json:"source"`
}

func cmdSurface(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printSurfaceUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printSurfaceUsage(stdout)
		return exitOK
	case "list":
		return cmdSurfaceList(rest, stdout, stderr, clock)
	case "get":
		return cmdSurfaceGet(rest, stdout, stderr, clock)
	case "set":
		return cmdSurfaceSet(rest, stdout, stderr, clock)
	case "revisions":
		return cmdSurfaceRevisions(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl surface: unknown subcommand %q\n\n", sub)
		printSurfaceUsage(stderr)
		return exitUsage
	}
}

func printSurfaceUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl surface <subcommand> [flags]

Read or write the coordinator's "show.surface" configuration objects
(Track E, ADR-026: a surface owns its canvas, its virtual-matrix channel
extraction, and its output). Reads require show:macro:run OR
config:write; writes require config:write.

Subcommands:
  list [--show <id>]   enumerate surface objects, optionally narrowed to
                        one show
  get <id>             show one surface's full definition
  set <id>             write a new surface revision (write, full
                        replacement)
  revisions <id>       list revision history, newest first

Run "showmeshctl surface <subcommand> --help" for flags specific to one
subcommand.
`)
}

func cmdSurfaceList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl surface list", stderr)
	var show string
	fs.StringVar(&show, "show", "", "narrow the list to surfaces belonging to this show id")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl surface list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nEnumerate show.surface objects (GET /api/v1/config/show.surface).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "surface list", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "surface list", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var query url.Values
	if show != "" {
		query = url.Values{"show": {show}}
	}

	var resp showConfigObjectsListResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.surface", query, &resp); err != nil {
		return reportError(stderr, "surface list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "surface list", err)
		}
		return exitOK
	}
	printShowConfigObjectsTable(stdout, resp)
	return exitOK
}

func cmdSurfaceGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl surface get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl surface get [flags] <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nShow one surface's full definition (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.surface/{id}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "surface get", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "surface get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp showSurfaceConfigResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.surface/"+url.PathEscape(id), nil, &resp); err != nil {
		return reportError(stderr, "surface get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "surface get", err)
		}
		return exitOK
	}
	printSurfaceDetail(stdout, resp)
	return exitOK
}

func cmdSurfaceSet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl surface set", stderr)
	var (
		show, name, node                   string
		startChannel, channelCount         int
		width, height                      int
		pixelFormat                        string
		frameRate                          int
		transport, ndiSourceName, hdmiDisp string
	)
	fs.StringVar(&show, "show", "", "the show this surface belongs to (required)")
	fs.StringVar(&name, "name", "", "the surface's name (required)")
	fs.StringVar(&node, "node", "", "the node id this surface is assigned to (required)")
	fs.IntVar(&startChannel, "start-channel", 0, "channelRange.startChannel, >= 1 (required)")
	fs.IntVar(&channelCount, "channel-count", 0, "channelRange.channelCount, >= 1 (required)")
	fs.IntVar(&width, "width", 0, "geometry.width, >= 1 (required)")
	fs.IntVar(&height, "height", 0, "geometry.height, >= 1 (required)")
	fs.StringVar(&pixelFormat, "pixel-format", "rgb", "geometry.pixelFormat: rgb|rgbw")
	fs.IntVar(&frameRate, "frame-rate", 0, "frameRate, 1-120 (required)")
	fs.StringVar(&transport, "transport", "", "output.transport: ndi|hdmi (required)")
	fs.StringVar(&ndiSourceName, "ndi-source-name", "", "output.ndi.sourceName (required when --transport=ndi)")
	fs.StringVar(&hdmiDisp, "hdmi-display", "", "output.hdmi.display (required when --transport=hdmi)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl surface set [flags] <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nWrite a new show.surface revision (PUT")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.surface/{id}). Requires config:write, admin only.")
		_, _ = fmt.Fprintln(stderr, "\nThis is a FULL REPLACEMENT, never a read-modify-write: every flag above")
		_, _ = fmt.Fprintln(stderr, "except --pixel-format (default rgb) must be given on every call, and this")
		_, _ = fmt.Fprintln(stderr, "command never reads the surface's current definition first. width * height")
		_, _ = fmt.Fprintln(stderr, "* channelsPerPixel(pixel-format) must equal --channel-count exactly, or the")
		_, _ = fmt.Fprintln(stderr, "coordinator refuses the write and names both numbers.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "surface set", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	var missing []string
	if show == "" {
		missing = append(missing, "--show")
	}
	if name == "" {
		missing = append(missing, "--name")
	}
	if node == "" {
		missing = append(missing, "--node")
	}
	if startChannel == 0 {
		missing = append(missing, "--start-channel")
	}
	if channelCount == 0 {
		missing = append(missing, "--channel-count")
	}
	if width == 0 {
		missing = append(missing, "--width")
	}
	if height == 0 {
		missing = append(missing, "--height")
	}
	if frameRate == 0 {
		missing = append(missing, "--frame-rate")
	}
	if transport == "" {
		missing = append(missing, "--transport")
	}
	if len(missing) > 0 {
		_, _ = fmt.Fprintf(stderr, "showmeshctl surface set: missing required flag(s): %v\n", missing)
		return exitUsage
	}

	output := configShowSurfaceOutput{Transport: transport}
	switch transport {
	case "ndi":
		if ndiSourceName == "" {
			_, _ = fmt.Fprintln(stderr, "showmeshctl surface set: --ndi-source-name is required when --transport=ndi")
			return exitUsage
		}
		output.NDI = &configShowSurfaceNDIOutput{SourceName: ndiSourceName}
	case "hdmi":
		if hdmiDisp == "" {
			_, _ = fmt.Fprintln(stderr, "showmeshctl surface set: --hdmi-display is required when --transport=hdmi")
			return exitUsage
		}
		output.HDMI = &configShowSurfaceHDMI{Display: hdmiDisp}
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl surface set: --transport must be ndi or hdmi, got %q\n", transport)
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "surface set", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	body := configShowSurface{
		Show: show, Name: name, Node: node,
		ChannelRange: configShowSurfaceChannelRange{StartChannel: startChannel, ChannelCount: channelCount},
		Geometry:     configShowSurfaceGeometry{Width: width, Height: height, PixelFormat: pixelFormat},
		FrameRate:    frameRate,
		Output:       output,
	}
	var resp showSurfaceConfigResponse
	if err := c.putJSON(ctx, "/api/v1/config/show.surface/"+url.PathEscape(id), body, &resp); err != nil {
		return reportError(stderr, "surface set", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "surface set", err)
		}
		return exitOK
	}
	printSurfaceDetail(stdout, resp)
	return exitOK
}

func cmdSurfaceRevisions(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl surface revisions", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl surface revisions [flags] <surface-id>")
		_, _ = fmt.Fprintln(stderr, "\nList show.surface revision history, newest first (GET")
		_, _ = fmt.Fprintln(stderr, "/api/v1/config/show.surface/{id}/revisions). Metadata only, no payload.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "surface revisions", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	id := rest[0]

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "surface revisions", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp configRevisionsResponse
	if err := c.getJSON(ctx, "/api/v1/config/show.surface/"+url.PathEscape(id)+"/revisions", nil, &resp); err != nil {
		return reportError(stderr, "surface revisions", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "surface revisions", err)
		}
		return exitOK
	}
	printConfigRevisionsTable(stdout, resp)
	return exitOK
}

func printSurfaceDetail(w io.Writer, resp showSurfaceConfigResponse) {
	p := resp.Payload
	_, _ = fmt.Fprintf(w, "Surface ID:   %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "Show:         %s\n", p.Show)
	_, _ = fmt.Fprintf(w, "Name:         %s\n", p.Name)
	_, _ = fmt.Fprintf(w, "Node:         %s\n", p.Node)
	_, _ = fmt.Fprintf(w, "Channels:     %d-%d (%d)\n", p.ChannelRange.StartChannel,
		p.ChannelRange.StartChannel+p.ChannelRange.ChannelCount-1, p.ChannelRange.ChannelCount)
	_, _ = fmt.Fprintf(w, "Geometry:     %dx%d %s\n", p.Geometry.Width, p.Geometry.Height, p.Geometry.PixelFormat)
	_, _ = fmt.Fprintf(w, "Frame rate:   %d\n", p.FrameRate)
	_, _ = fmt.Fprintf(w, "Transport:    %s\n", p.Output.Transport)
	switch p.Output.Transport {
	case "ndi":
		if p.Output.NDI != nil {
			_, _ = fmt.Fprintf(w, "NDI source:   %s\n", p.Output.NDI.SourceName)
		}
	case "hdmi":
		if p.Output.HDMI != nil {
			_, _ = fmt.Fprintf(w, "HDMI display: %s\n", p.Output.HDMI.Display)
		}
	}
	_, _ = fmt.Fprintf(w, "Revision:     %d\n", resp.Revision)
	_, _ = fmt.Fprintf(w, "Updated:      %s\n", resp.UpdatedAt.Format(time.RFC3339))
	if resp.CreatedByPrincipalName != nil {
		_, _ = fmt.Fprintf(w, "Created by:   %s\n", *resp.CreatedByPrincipalName)
	} else {
		_, _ = fmt.Fprintf(w, "Created by:   (no principal recorded)\n")
	}
}
