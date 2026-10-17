package previewgateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Source is one NDI source on the network.
type Source struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// SourceProvider reports NDI sources.
type SourceProvider interface {
	// Sources lists announced sources whose names pass validation.
	Sources() []Source
	// Announced is the set of announced names, and whether discovery works.
	Announced() (names map[string]bool, ok bool)
	// Unavailable states why discovery does not work and what to do, or is
	// empty while it works.
	Unavailable() string
}

const (
	avahiSocket     = "/run/avahi-daemon/socket"
	avahiWatchEvery = 5 * time.Second
	msgRestarting   = "NDI discovery stopped. The gateway is restarting it."
	msgNoAvahi      = "NDI discovery is not running because the Avahi service stopped. Restart the gateway container."
	msgNoNDIRuntime = "NDI is not available on this host. Mount the NDI runtime and restart the gateway."
	msgStarting     = "NDI discovery is starting. Wait a few seconds."
	msgCannotStart  = "NDI discovery cannot start. Check the NDI runtime and the gateway log."
	ndiRuntimeDir   = "/opt/ndi"
)

// ndiRuntimeNames are the only file names the GStreamer NDI plugin opens.
var ndiRuntimeNames = []string{"libndi.so.6", "libndi.so.5"}

const (
	unavailableWait = 10 * time.Second
	stableRun       = time.Minute
)

var (
	retryDelay = 5 * time.Second
	// startGrace is how long the monitor must run before discovery counts as started.
	startGrace = 10 * time.Second
)

// Discovery follows gst-device-monitor-1.0, which reports an NDI source when
// it appears and again when it goes away.
type Discovery struct {
	Log *slog.Logger
	// Available reports whether Avahi runs and the NDI plugin and runtime load.
	Available func(ctx context.Context) error
	// Avahi reports whether the Avahi service the NDI runtime browses through
	// accepts connections.
	Avahi func() error
	// Monitor streams the monitor's output to w until it exits or ctx ends.
	Monitor func(ctx context.Context, w io.Writer) error

	mu      sync.Mutex
	sources map[string]Source
	ok      bool
	msg     string
}

func NewDiscovery(log *slog.Logger) *Discovery {
	d := &Discovery{Log: log, Avahi: checkAvahi, Monitor: runDeviceMonitor, sources: map[string]Source{}, msg: msgStarting}
	d.Available = func(ctx context.Context) error {
		if err := d.Avahi(); err != nil {
			return err
		}
		if err := checkNDIRuntime(runtimeDirs()); err != nil {
			return err
		}
		return checkNDIPlugin(ctx)
	}
	return d
}

func checkAvahi() error {
	c, err := net.DialTimeout("unix", avahiSocket, 2*time.Second)
	if err != nil {
		return errNoAvahi
	}
	return c.Close()
}

var errNoAvahi = errors.New("avahi is not running")

// unavailableError carries the message an operator sees.
type unavailableError struct{ msg string }

func (e *unavailableError) Error() string { return e.msg }

func runtimeDirs() []string {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("LD_LIBRARY_PATH")) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{ndiRuntimeDir}
	}
	return dirs
}

// checkNDIRuntime fails unless a runtime file the NDI plugin opens is in dirs.
// The plugin loads the runtime only when it starts, so gst-inspect cannot tell.
func checkNDIRuntime(dirs []string) error {
	var other []string
	for _, dir := range dirs {
		for _, name := range ndiRuntimeNames {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() {
				return nil
			}
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "libndi*.so*"))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
				other = append(other, filepath.Base(m))
			}
		}
	}
	if len(other) == 0 {
		return &unavailableError{msgNoNDIRuntime}
	}
	sort.Strings(other)
	return &unavailableError{fmt.Sprintf("The NDI runtime is named %s. Rename or link it to libndi.so.6.", other[0])}
}

func checkNDIPlugin(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gst-inspect-1.0", "ndisrc")
	cmd.Env = minimalEnv(gstEnvKeys...)
	return cmd.Run()
}

func runDeviceMonitor(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "gst-device-monitor-1.0", "-f", "-i", "Source/Network")
	cmd.Env = minimalEnv(gstEnvKeys...)
	cmd.Stdout = w
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

// Loop runs discovery until ctx ends.
func (d *Discovery) Loop(ctx context.Context) {
	warned, restartWarned, failingAtStart := false, false, false
	for ctx.Err() == nil {
		if err := d.Available(ctx); err != nil {
			msg := msgNoNDIRuntime
			var ue *unavailableError
			if errors.Is(err, errNoAvahi) {
				msg = msgNoAvahi
			} else if errors.As(err, &ue) {
				msg = ue.msg
			}
			d.set(false, msg)
			if !warned {
				d.Log.Warn(msg, "error", err)
				warned = true
			}
			sleep(ctx, unavailableWait)
			continue
		}
		warned = false
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.consume(pr)
		}()
		mctx, stopMonitor := context.WithCancel(ctx)
		// After a failed start, discovery counts as running only once the monitor stays up.
		var grace *time.Timer
		if failingAtStart {
			d.set(false, msgCannotStart)
			grace = time.AfterFunc(startGrace, func() { d.markRunning(mctx) })
		} else {
			d.set(true, "")
		}
		go d.watchAvahi(mctx, stopMonitor)
		started := time.Now()
		err := d.Monitor(mctx, pw)
		if grace != nil {
			grace.Stop()
		}
		failingAtStart = ctx.Err() == nil && mctx.Err() == nil && time.Since(started) < startGrace
		stopMonitor()
		_ = pw.Close()
		<-done
		if failingAtStart {
			d.set(false, msgCannotStart)
		} else {
			d.set(false, msgRestarting)
		}
		if ctx.Err() == nil {
			// A monitor that keeps dying at once is one condition, so it is logged once until a run lasts.
			if time.Since(started) >= stableRun {
				restartWarned = false
			}
			if !restartWarned {
				d.Log.Warn("NDI discovery stopped. The gateway is restarting it.", "error", err)
				restartWarned = true
			}
			sleep(ctx, retryDelay)
		}
	}
}

// markRunning reports discovery as working, keeping the sources found so far,
// unless the monitor run it belongs to has ended.
func (d *Discovery) markRunning(run context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if run.Err() == nil {
		d.ok, d.msg = true, ""
	}
}

// watchAvahi ends the monitor when Avahi stops, which leaves it silent.
func (d *Discovery) watchAvahi(ctx context.Context, stop context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(avahiWatchEvery):
			if d.Avahi() != nil {
				stop()
				return
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (d *Discovery) set(ok bool, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ok, d.msg = ok, msg
	d.sources = map[string]Source{}
}

func (d *Discovery) consume(r io.Reader) {
	p := &monitorParser{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if ev := p.Feed(sc.Text()); ev != nil {
			d.apply(*ev)
		}
	}
	if ev := p.Flush(); ev != nil {
		d.apply(*ev)
	}
}

func (d *Discovery) apply(ev monitorEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if ev.removed {
		delete(d.sources, ev.src.Name)
		return
	}
	if ValidateNDIName(ev.src.Name) != nil {
		d.Log.Warn("Skipping an NDI source with an unusable name.")
		return
	}
	d.sources[ev.src.Name] = ev.src
}

func (d *Discovery) Sources() []Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Source, 0, len(d.sources))
	for _, s := range d.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (d *Discovery) Unavailable() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ok {
		return ""
	}
	return d.msg
}

func (d *Discovery) Announced() (map[string]bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	names := make(map[string]bool, len(d.sources))
	for n := range d.sources {
		names[n] = true
	}
	return names, d.ok
}

type monitorEvent struct {
	removed bool
	src     Source
}

// monitorParser reads the "Device found:" and "Device removed:" blocks of
// gst-device-monitor-1.0. A block ends at its gst-launch line, or when the
// next block starts.
type monitorParser struct {
	cur *monitorEvent
}

func (p *monitorParser) Feed(line string) *monitorEvent {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "Device found"), strings.HasPrefix(t, "Device removed"):
		done := p.Flush()
		p.cur = &monitorEvent{removed: strings.HasPrefix(t, "Device removed")}
		return done
	case p.cur == nil:
		return nil
	case strings.HasPrefix(t, "ndi-name = "):
		p.cur.src.Name = unquoteProp(strings.TrimPrefix(t, "ndi-name = "))
	case strings.HasPrefix(t, "url-address = "):
		p.cur.src.Address = unquoteProp(strings.TrimPrefix(t, "url-address = "))
	case strings.HasPrefix(t, "gst-launch-1.0"):
		return p.Flush()
	}
	return nil
}

func (p *monitorParser) Flush() *monitorEvent {
	ev := p.cur
	p.cur = nil
	if ev == nil || ev.src.Name == "" {
		return nil
	}
	return ev
}

func unquoteProp(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return v
}
