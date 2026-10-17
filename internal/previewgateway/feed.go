package previewgateway

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"os/exec"
	"syscall"
	"time"
)

// NDIPipelineArgs is the gst-launch-1.0 argument vector for one NDI source:
// lowest bandwidth, 640x360 at 10 fps, H.264 in MPEG-TS on stdout. The name is
// a single argument that gst-launch takes as one token.
func NDIPipelineArgs(name, address string) ([]string, error) {
	if err := ValidateNDIName(name); err != nil {
		return nil, err
	}
	src := []string{"ndisrc", "ndi-name=" + name}
	if address != "" {
		if err := ValidateNDIAddress(address); err != nil {
			return nil, err
		}
		src = append(src, "url-address="+address)
	}
	return append(append([]string{"-q"}, src...),
		"bandwidth=0", "!",
		"ndisrcdemux", "name=d", "d.video", "!",
		"queue", "max-size-buffers=4", "leaky=downstream", "!",
		"videoconvert", "!", "videoscale", "!", "videorate", "!",
		"video/x-raw,width=640,height=360,framerate=10/1", "!",
		"x264enc", "tune=zerolatency", "speed-preset=ultrafast", "key-int-max=20", "bitrate=800", "!",
		"video/x-h264,profile=baseline", "!",
		"h264parse", "config-interval=-1", "!",
		"mpegtsmux", "!",
		"fdsink", "fd=1",
	), nil
}

// Feed serves each NDI stream's video to go2rtc over loopback. go2rtc connects
// only while someone watches, and the pipeline stops when it disconnects.
type Feed struct {
	secret  string
	log     *slog.Logger
	manager *Manager
	sources SourceProvider
	// Command builds the process for one NDI source; tests replace it.
	Command func(ctx context.Context, name, address string) (*exec.Cmd, error)
}

func NewFeed(secret string, m *Manager, sources SourceProvider, log *slog.Logger) *Feed {
	return &Feed{secret: secret, log: log, manager: m, sources: sources, Command: gstreamerCommand}
}

func gstreamerCommand(ctx context.Context, name, address string) (*exec.Cmd, error) {
	args, err := NDIPipelineArgs(name, address)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "gst-launch-1.0", args...)
	cmd.Env = minimalEnv(gstEnvKeys...)
	return cmd, nil
}

func (f *Feed) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ndi/{secret}/{id}", f.serve)
	return mux
}

func (f *Feed) serve(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(f.secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	name, address, ok := f.manager.NDISource(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A source with an address is reached directly, so discovery need not see it.
	if address == "" {
		announced, available := f.sources.Announced()
		if !available {
			http.Error(w, "NDI is not available", http.StatusServiceUnavailable)
			return
		}
		if !announced[name] {
			http.NotFound(w, r)
			return
		}
	}
	cmd, err := f.Command(r.Context(), name, address)
	if err != nil {
		http.Error(w, "bad source", http.StatusInternalServerError)
		return
	}
	cmd.Stdout = &flushWriter{w: w, rc: http.NewResponseController(w)}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	w.Header().Set("Content-Type", "video/mp2t")
	if err := cmd.Run(); err != nil && r.Context().Err() == nil {
		f.log.Warn("An NDI pipeline stopped.", "error", err)
	}
}

type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.rc.Flush()
	}
	return n, err
}
