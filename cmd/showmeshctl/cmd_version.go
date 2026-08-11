package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/showmeshsystems/showmesh/internal/version"
)

// versionOutput is what "showmeshctl version --output json" prints. It is
// this program's own shape, not a wire type from types.go: there is no
// single coordinator endpoint that returns "the CLI's version", so this
// struct exists purely to give --output json something structured to
// marshal for this one command.
type versionOutput struct {
	CLI struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"buildDate"`
	} `json:"cli"`
	Coordinator *coordinatorInfo `json:"coordinator"`
	APIVersion  struct {
		Requested         string `json:"requested"`
		CoordinatorServes int    `json:"coordinatorServes,omitempty"`
		SupportedByServer []int  `json:"supportedByServer,omitempty"`
		Compatible        bool   `json:"compatible"`
	} `json:"apiVersion"`
	Error string `json:"error,omitempty"`
}

func cmdVersion(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl version", stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl version [flags]")
		_, _ = fmt.Fprintln(stderr, "\nShow this CLI's own version and the coordinator's version and API")
		_, _ = fmt.Fprintln(stderr, "negotiation result, side by side (GET /api/v1/).")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "version", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	out := versionOutput{}
	out.CLI.Version = version.Version
	out.CLI.Commit = version.Commit
	out.CLI.BuildDate = version.BuildDate
	out.APIVersion.Requested = clientAPIVersion

	c, err := newRequestClient(g)
	if err != nil {
		return reportError(stderr, "version", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	defer cancel()

	var desc serviceDescriptor
	getErr := c.getJSON(ctx, "/api/v1/", nil, &desc)
	code := exitOK
	if getErr != nil {
		out.Error = getErr.Error()
		var ce *cliError
		if errors.As(getErr, &ce) {
			code = ce.code
		} else {
			code = exitAPIError
		}
	} else {
		printClockSkew(stderr, desc.ServerTime, clock())
		out.Coordinator = &desc.Coordinator
		out.APIVersion.CoordinatorServes = desc.APIVersion
		out.APIVersion.SupportedByServer = desc.SupportedVersions
		out.APIVersion.Compatible = intInSlice(1, desc.SupportedVersions) || desc.APIVersion == 1
	}

	if g.output == outputJSON {
		if err := printJSON(stdout, out); err != nil {
			return reportError(stderr, "version", err)
		}
		return code
	}

	_, _ = fmt.Fprintf(stdout, "showmeshctl:  %s (commit %s, built %s)\n", out.CLI.Version, out.CLI.Commit, out.CLI.BuildDate)
	_, _ = fmt.Fprintf(stdout, "API version requested by this CLI: %s\n", clientAPIVersion)
	if getErr != nil {
		_, _ = fmt.Fprintf(stdout, "coordinator:  unreachable or incompatible: %s\n", getErr)
		return code
	}
	_, _ = fmt.Fprintf(stdout, "coordinator:  %s (commit %s, built %s, %s)\n",
		desc.Coordinator.Version, desc.Coordinator.Commit, desc.Coordinator.BuildDate, desc.Coordinator.GoVersion)
	_, _ = fmt.Fprintf(stdout, "coordinator API version: %d, supports %v\n", desc.APIVersion, desc.SupportedVersions)
	if out.APIVersion.Compatible {
		_, _ = fmt.Fprintln(stdout, "negotiation: compatible")
	} else {
		_, _ = fmt.Fprintln(stdout, "negotiation: INCOMPATIBLE — this CLI only speaks version 1")
	}
	return code
}

func intInSlice(v int, s []int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
