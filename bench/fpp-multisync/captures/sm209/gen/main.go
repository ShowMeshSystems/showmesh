// Throwaway probe: builds a minimal FSEQ fixture for SM-209 FPP 10 default
// MultiSync-transport evidence capture. Not product code.
package main

import (
	"os"

	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

func main() {
	// 60s at 50ms step => 1200 frames. 8 channels to match this bench's
	// default configured output range (0-7).
	data := fseqtest.Build(8, 1200, 50)
	if err := os.WriteFile("bench/fpp-multisync/captures/sm209/gen/sm209-test.fseq", data, 0o644); err != nil {
		panic(err)
	}
}
