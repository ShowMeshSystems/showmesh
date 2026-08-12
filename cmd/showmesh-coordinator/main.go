// Command showmesh-coordinator is the ShowMesh coordinator: the management
// plane that observes and commands node agents over MQTT. Per ADR-001 it is
// never a scheduler and per ADR-008 its loss (and the broker's) must never
// affect a running show.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/version"
)

func main() {
	// Subcommand dispatch happens BEFORE the top-level flag.Parse() below,
	// and is checked first: the flag package stops parsing at the first
	// non-flag argument, so a call like `showmesh-coordinator bootstrap
	// -name=...` would otherwise leave -version/-healthcheck both false
	// and fall straight through to a normal coordinator.Run(), silently
	// ignoring "bootstrap" and every flag after it. See subcommands.go for
	// what each of these does and why they exist outside the HTTP API
	// (ADR-024 decision 9: bootstrap and lockout recovery are host-level,
	// never network-level).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "bootstrap":
			os.Exit(runBootstrapSubcommand(os.Args[2:]))
		case "create-admin":
			os.Exit(runCreateAdminSubcommand(os.Args[2:]))
		case "reset-password":
			os.Exit(runResetPasswordSubcommand(os.Args[2:]))
		case "list-principals":
			os.Exit(runListPrincipalsSubcommand(os.Args[2:]))
		}
	}

	versionFlag := flag.Bool("version", false, "print version and exit")
	healthcheckFlag := flag.Bool("healthcheck", false, "check the local /healthz endpoint and exit (used by container HEALTHCHECK)")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		os.Exit(0)
	}

	if *healthcheckFlag {
		os.Exit(runHealthcheck())
	}

	os.Exit(coordinator.Run())
}

// runHealthcheck performs a local HTTP GET against /healthz and returns a
// process exit code (0 on success). It exists so a shell-less distroless
// container can define a Docker HEALTHCHECK; it prints nothing on success.
func runHealthcheck() int {
	addr := os.Getenv(config.EnvHTTPAddr)
	if addr == "" {
		addr = config.DefaultHTTPAddr
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: invalid %s %q: %v\n", config.EnvHTTPAddr, addr, err)
		return 1
	}

	// Always target the local process, regardless of the configured bind
	// host (which may be "0.0.0.0" or empty, neither of which is dialable).
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: unexpected status %s\n", resp.Status)
		return 1
	}

	return 0
}
