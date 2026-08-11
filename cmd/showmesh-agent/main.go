// Command showmesh-agent is the ShowMesh node agent: it runs natively on
// media hardware, advertises capabilities over MQTT, and reports health.
// Per ARCHITECTURE 4.3 it should not require containers on machines that
// need direct GPU, HDMI, audio, EDID, or NDI access — see ADR-012.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/showmeshsystems/showmesh/internal/agent"
	"github.com/showmeshsystems/showmesh/internal/version"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		os.Exit(0)
	}

	os.Exit(agent.Run())
}
