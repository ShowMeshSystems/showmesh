package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

// This file is ADR-055's node enrollment client: "node enroll", "node
// enrollments" and "node enrollments cancel", over /api/v1/node-enrollments.

type createNodeEnrollmentRequest struct {
	NodeID           string `json:"nodeId"`
	Reenroll         bool   `json:"reenroll"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

type createNodeEnrollmentResponse struct {
	ServerTime     time.Time `json:"serverTime"`
	ID             string    `json:"id"`
	NodeID         string    `json:"nodeId"`
	Code           string    `json:"code"`
	Reenroll       bool      `json:"reenroll"`
	ExpiresAt      time.Time `json:"expiresAt"`
	CoordinatorURL string    `json:"coordinatorUrl"`
}

type nodeEnrollment struct {
	ID         string     `json:"id"`
	NodeID     string     `json:"nodeId"`
	Reenroll   bool       `json:"reenroll"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	State      string     `json:"state"`
	RedeemedAt *time.Time `json:"redeemedAt"`
}

type nodeEnrollmentsResponse struct {
	ServerTime  time.Time        `json:"serverTime"`
	Enrollments []nodeEnrollment `json:"enrollments"`
}

type nodeEnrollmentResponse struct {
	ServerTime time.Time      `json:"serverTime"`
	Enrollment nodeEnrollment `json:"enrollment"`
}

// enrollJSONFlag registers --json as shorthand for --output json.
func enrollJSONFlag(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "shorthand for --output json")
}

func applyJSONFlag(asJSON bool, g *globalFlags) {
	if asJSON {
		g.output = outputJSON
	}
}

func cmdNodeEnroll(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node enroll", stderr)
	reenroll := fs.Bool("reenroll", false, "replace an enrolled node's broker password and API token")
	expires := fs.Duration("expires", 15*time.Minute, "how long the code stays valid, 1m to 24h")
	asJSON := enrollJSONFlag(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node enroll [flags] <node-id>")
		_, _ = fmt.Fprintln(stderr, "\nMint a one-time enrollment code for a node (POST /api/v1/node-enrollments,")
		_, _ = fmt.Fprintln(stderr, "requires node:enroll). The code is shown once. Run the printed command on")
		_, _ = fmt.Fprintln(stderr, "the node before the code expires.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	applyJSONFlag(*asJSON, g)
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node enroll", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	if *expires < time.Minute || *expires > 24*time.Hour || *expires%time.Second != 0 {
		_, _ = fmt.Fprintln(stderr, "showmeshctl node enroll: --expires must be whole seconds between 1m and 24h. Choose a value in that range.")
		return exitUsage
	}

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node enroll", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	req := createNodeEnrollmentRequest{NodeID: rest[0], Reenroll: *reenroll, ExpiresInSeconds: int(*expires / time.Second)}
	var resp createNodeEnrollmentResponse
	if err := c.postJSON(ctx, "/api/v1/node-enrollments", req, &resp); err != nil {
		return reportError(stderr, "node enroll", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())

	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node enroll", err)
		}
		return exitOK
	}

	coordinatorURL := resp.CoordinatorURL
	if coordinatorURL == "" {
		coordinatorURL = strings.TrimRight(c.baseURL.String(), "/")
	}
	var desc serviceDescriptor
	version := ""
	if err := c.getJSON(ctx, "/api/v1/", nil, &desc); err == nil {
		version = desc.Coordinator.Version
	}

	reenrollText := "no"
	if resp.Reenroll {
		reenrollText = "yes, this replaces the node's current broker password and API token"
	}
	_, _ = fmt.Fprintf(stdout, "Code:        %s\n", resp.Code)
	_, _ = fmt.Fprintf(stdout, "Node:        %s\n", resp.NodeID)
	_, _ = fmt.Fprintf(stdout, "Expires:     %s (in %s)\n", resp.ExpiresAt.Format(time.RFC3339), resp.ExpiresAt.Sub(resp.ServerTime).Round(time.Second))
	_, _ = fmt.Fprintf(stdout, "Re-enroll:   %s\n", reenrollText)
	_, _ = fmt.Fprintf(stdout, "\nRun this on the node:\n  %s\n", nodeInstallCommand(version, coordinatorURL, resp.Code))
	_, _ = fmt.Fprintln(stdout, "\nThe code works once and is shown only now.")
	return exitOK
}

// releaseVersionPattern matches a release version as the release workflow
// stamps it: the tag without its leading v.
var releaseVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// nodeInstallCommand is the one command a node runs to join with code.
func nodeInstallCommand(version, coordinatorURL, code string) string {
	args := "--coordinator " + coordinatorURL + " --code " + code
	if releaseVersionPattern.MatchString(version) {
		return "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v" + version +
			"/get-showmesh.sh | sudo bash -s -- " + args
	}
	return "sudo showmesh-install " + args
}

func cmdNodeEnrollments(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	if len(args) > 0 && args[0] == "cancel" {
		return cmdNodeEnrollmentsCancel(args[1:], stdout, stderr, clock)
	}
	fs, g := newFlagSet("showmeshctl node enrollments", stderr)
	asJSON := enrollJSONFlag(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node enrollments [flags]")
		_, _ = fmt.Fprintln(stderr, "       showmeshctl node enrollments cancel [flags] <id>")
		_, _ = fmt.Fprintln(stderr, "\nList every enrollment code, never the code itself, or cancel a pending one")
		_, _ = fmt.Fprintln(stderr, "(GET and DELETE /api/v1/node-enrollments, requires node:enroll).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	applyJSONFlag(*asJSON, g)
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node enrollments", err)
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node enrollments", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nodeEnrollmentsResponse
	raw, err := c.getJSONKeepingRaw(ctx, "/api/v1/node-enrollments", nil, &resp)
	if err != nil {
		return reportError(stderr, "node enrollments", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSONBody(stdout, raw); err != nil {
			return reportError(stderr, "node enrollments", err)
		}
		return exitOK
	}
	if len(resp.Enrollments) == 0 {
		_, _ = fmt.Fprintln(stdout, "No enrollment codes have been minted. Mint one with showmeshctl node enroll <node-id>.")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNODE\tSTATE\tRE-ENROLL\tCREATED BY\tCREATED\tEXPIRES\tREDEEMED")
	for _, e := range resp.Enrollments {
		redeemed := "-"
		if e.RedeemedAt != nil {
			redeemed = e.RedeemedAt.Format(time.RFC3339)
		}
		reenroll := "no"
		if e.Reenroll {
			reenroll = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.NodeID, e.State, reenroll, e.CreatedBy,
			e.CreatedAt.Format(time.RFC3339), e.ExpiresAt.Format(time.RFC3339), redeemed)
	}
	_ = tw.Flush()
	return exitOK
}

func cmdNodeEnrollmentsCancel(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl node enrollments cancel", stderr)
	asJSON := enrollJSONFlag(fs)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl node enrollments cancel [flags] <id>")
		_, _ = fmt.Fprintln(stderr, "\nCancel a pending enrollment code (DELETE /api/v1/node-enrollments/{id},")
		_, _ = fmt.Fprintln(stderr, "requires node:enroll). A code that is not pending is refused with 409.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	applyJSONFlag(*asJSON, g)
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "node enrollments cancel", err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitUsage
	}
	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "node enrollments cancel", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var resp nodeEnrollmentResponse
	if err := c.deleteJSON(ctx, "/api/v1/node-enrollments/"+url.PathEscape(rest[0]), nil, &resp); err != nil {
		return reportError(stderr, "node enrollments cancel", err)
	}
	printClockSkew(stderr, resp.ServerTime, clock())
	if g.output == outputJSON {
		if err := printJSON(stdout, resp); err != nil {
			return reportError(stderr, "node enrollments cancel", err)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(stdout, "Cancelled the enrollment code %s for node %s.\n", resp.Enrollment.ID, resp.Enrollment.NodeID)
	return exitOK
}
