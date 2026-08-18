// Throwaway probe: builds FSEQ fixtures for Track F seam F0 evidence
// capture. Not product code; deleted or left under bench/ per the seam
// spec.
package main

import (
	"fmt"
	"os"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

func write(path string, channelCount, frameCount uint32, stepTimeMS byte) {
	data := fseqtest.Build(channelCount, frameCount, stepTimeMS)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
}

func main() {
	dir := "bench/fpp-multisync/captures/trackf-f0/gen"
	// Representative resting FSEQ: 300s at stepTime 50ms => 6000 frames.
	write(dir+"/resting-300s.fseq", 48, 6000, 50)
	// Target A variant: 120s at 25ms step => 4800 frames, filename shared with B.
	write(dir+"/dup-name/A/trackf-resting.fseq", 48, 4800, 25)
	// Target B variant, same filename, different content/duration: 90s at 50ms.
	write(dir+"/dup-name/B/trackf-resting.fseq", 48, 1800, 50)
	// Degenerate: zero frame count.
	write(dir+"/zero-frames.fseq", 48, 0, 50)
	// Degenerate: zero step time.
	write(dir+"/zero-steptime.fseq", 48, 100, 0)
	// Short cadence-probe FSEQ: 20s at stepTime 50ms => 400 frames.
	write(dir+"/short-20s.fseq", 4, 400, 50)

	for _, p := range []string{
		dir + "/resting-300s.fseq",
		dir + "/dup-name/A/trackf-resting.fseq",
		dir + "/dup-name/B/trackf-resting.fseq",
		dir + "/zero-frames.fseq",
		dir + "/zero-steptime.fseq",
		dir + "/short-20s.fseq",
	} {
		f, err := fseq.Open(p)
		if err != nil {
			fmt.Printf("%s: Open error: %v\n", p, err)
			continue
		}
		fc := f.FrameCount()
		st := f.StepTimeMS()
		durMS := fc * int(st)
		fmt.Printf("%s: FrameCount=%d StepTimeMS=%d duration_ms=%d\n", p, fc, st, durMS)
		if err := f.Close(); err != nil {
			fmt.Printf("%s: Close error: %v\n", p, err)
		}
	}
}
