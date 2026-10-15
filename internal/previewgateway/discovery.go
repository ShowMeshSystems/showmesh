package previewgateway

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
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
}

const (
	retryDelay      = 5 * time.Second
	unavailableWait = 10 * time.Second
)

// Discovery follows gst-device-monitor-1.0, which reports an NDI source when
// it appears and again when it goes away.
type Discovery struct {
	Log *slog.Logger
	// Available reports whether the NDI plugin and runtime load.
	Available func(ctx context.Context) error
	// Monitor streams the monitor's output to w until it exits or ctx ends.
	Monitor func(ctx context.Context, w io.Writer) error

	mu      sync.Mutex
	sources map[string]Source
	ok      bool
}

func NewDiscovery(log *slog.Logger) *Discovery {
	return &Discovery{Log: log, Available: checkNDIPlugin, Monitor: runDeviceMonitor, sources: map[string]Source{}}
}

func checkNDIPlugin(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "gst-inspect-1.0", "ndisrc").Run()
}

func runDeviceMonitor(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "gst-device-monitor-1.0", "-f", "-i", "Source/Network")
	cmd.Stdout = w
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

// Loop runs discovery until ctx ends.
func (d *Discovery) Loop(ctx context.Context) {
	warned := false
	for ctx.Err() == nil {
		if err := d.Available(ctx); err != nil {
			d.set(false)
			if !warned {
				d.Log.Warn("NDI is not available on this host. Mount the NDI runtime and restart the gateway.", "error", err)
				warned = true
			}
			sleep(ctx, unavailableWait)
			continue
		}
		warned = false
		d.set(true)
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			d.consume(pr)
		}()
		err := d.Monitor(ctx, pw)
		pw.Close()
		<-done
		d.set(false)
		if ctx.Err() == nil {
			d.Log.Warn("NDI discovery stopped. The gateway is restarting it.", "error", err)
			sleep(ctx, retryDelay)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (d *Discovery) set(ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ok = ok
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
	cur     *monitorEvent
	removed bool
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
