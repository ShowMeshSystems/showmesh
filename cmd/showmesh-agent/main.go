// Command showmesh-agent is the ShowMesh node agent. It is not yet
// implemented; capability advertisement and health heartbeats land in
// Step 2.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/showmeshsystems/showmesh/internal/version"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		os.Exit(0)
	}

	slog.Info("showmesh-agent is not implemented yet; capability advertisement lands in Step 2",
		"version", version.Version,
	)
	os.Exit(0)
}
