package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"
)

// showmeshctl surface for files ShowMesh Companions uploaded, such as
// Resolume Arena crash reports.

type companionReportSummary struct {
	ID            string `json:"id"`
	CompanionID   string `json:"companionId"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	FileName      string `json:"fileName"`
	CapturedAt    string `json:"capturedAt"`
	ReceivedAt    string `json:"receivedAt"`
	SizeBytes     int64  `json:"sizeBytes"`
	ContentSHA256 string `json:"contentSha256"`
}

type companionReportListResponse struct {
	ServerTime time.Time                `json:"serverTime"`
	Reports    []companionReportSummary `json:"reports"`
}

type companionReportResponse struct {
	ServerTime time.Time              `json:"serverTime"`
	Report     companionReportSummary `json:"report"`
	Content    string                 `json:"content"`
}

func cmdCompanionReports(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) == 0 {
		printCompanionReportsUsage(stderr)
		return exitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		printCompanionReportsUsage(stdout)
		return exitOK
	case "list":
		return cmdCompanionReportsList(rest, stdout, stderr, clock)
	case "get":
		return cmdCompanionReportsGet(rest, stdout, stderr, clock)
	default:
		_, _ = fmt.Fprintf(stderr, "showmeshctl companion-reports: unknown subcommand %q\n\n", sub)
		printCompanionReportsUsage(stderr)
		return exitUsage
	}
}

func printCompanionReportsUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: showmeshctl companion-reports <subcommand> [flags]

Files ShowMesh Companions uploaded from their own machines, such as
Resolume Arena crash reports. The coordinator keeps the newest 200 per
companion.

Subcommands:
  list                 uploaded reports, newest first
  get <report-id>      print one report's content (use --output json for
                       the content with its details)

Run "showmeshctl companion-reports <subcommand> --help" for flags.
`)
}

func cmdCompanionReportsList(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl companion-reports list", stderr)
	var companion string
	var limit int
	fs.StringVar(&companion, "companion", "", "list only this companion's reports")
	fs.IntVar(&limit, "limit", 50, "how many reports to list, 1 to 500")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl companion-reports list [flags]")
		_, _ = fmt.Fprintln(stderr, "\nUploaded companion reports, newest first (GET /api/v1/companion-reports).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "companion-reports list", err)
	}
	if len(fs.Args()) > 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "companion-reports list", err)
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if companion != "" {
		query.Set("companionId", companion)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp companionReportListResponse
	if err := c.getJSON(ctx, "/api/v1/companion-reports", query, &resp); err != nil {
		return reportError(stderr, "companion-reports list", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "companion-reports list", err)
		}
		return exitOK
	}
	if len(resp.Reports) == 0 {
		_, _ = fmt.Fprintln(stdout, "No companion has uploaded a report.")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tCOMPANION\tSOURCE\tFILE\tCAPTURED\tRECEIVED\tBYTES")
	for _, r := range resp.Reports {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", r.ID, r.CompanionID, r.Source, r.FileName, r.CapturedAt, r.ReceivedAt, r.SizeBytes)
	}
	_ = tw.Flush()
	return exitOK
}

func cmdCompanionReportsGet(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl companion-reports get", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl companion-reports get [flags] <report-id>")
		_, _ = fmt.Fprintln(stderr, "\nPrint one report's content exactly as uploaded, so it can be redirected")
		_, _ = fmt.Fprintln(stderr, "to a file (GET /api/v1/companion-reports/{reportId}).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "companion-reports get", err)
	}
	if len(fs.Args()) != 1 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "companion-reports get", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()
	var resp companionReportResponse
	if err := c.getJSON(ctx, "/api/v1/companion-reports/"+url.PathEscape(fs.Arg(0)), nil, &resp); err != nil {
		return reportError(stderr, "companion-reports get", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "companion-reports get", err)
		}
		return exitOK
	}
	_, _ = io.WriteString(stdout, resp.Content)
	return exitOK
}
