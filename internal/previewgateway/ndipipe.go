package previewgateway

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// NDIPipelineArgs is the gst-launch-1.0 argument vector for one NDI source,
// scaled to 640x360 at 10 fps and encoded as H.264 in MPEG-TS on stdout.
func NDIPipelineArgs(name string) ([]string, error) {
	if err := ValidateNDIName(name); err != nil {
		return nil, err
	}
	return []string{
		"gst-launch-1.0", "-q",
		"ndisrc", `ndi-name="` + name + `"`, "bandwidth=lowest", "!",
		"ndisrcdemux", "name=d", "d.video", "!",
		"queue", "max-size-buffers=4", "leaky=downstream", "!",
		"videoconvert", "!", "videoscale", "!", "videorate", "!",
		"video/x-raw,width=640,height=360,framerate=10/1", "!",
		"x264enc", "tune=zerolatency", "speed-preset=ultrafast", "key-int-max=20", "bitrate=800", "!",
		"video/x-h264,profile=baseline", "!",
		"h264parse", "config-interval=-1", "!",
		"mpegtsmux", "!",
		"fdsink", "fd=1",
	}, nil
}

// ExecNDIPipeline replaces this process with gst-launch-1.0 so that go2rtc's
// stop signal reaches GStreamer directly. It returns only on failure.
func ExecNDIPipeline(token string) error {
	name, err := DecodeNDIName(token)
	if err != nil {
		return err
	}
	argv, err := NDIPipelineArgs(name)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return errors.New("gst-launch-1.0 is not installed")
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("start gst-launch-1.0: %w", err)
	}
	return nil
}
