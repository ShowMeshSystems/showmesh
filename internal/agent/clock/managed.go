package clock

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ptp4lBinary is a full path for the same reason [pmcBinary] is: Debian's
// linuxptp package installs it under /usr/sbin, off a non-interactive
// shell's default PATH.
var ptp4lBinary = "/usr/sbin/ptp4l"

// ManagedConfig configures [NewManagedProvider].
type ManagedConfig struct {
	Interface string
	Domain    int

	// ClientOnly is set when the operator declares an external domain
	// (RES-019 §1: "clientOnly when the operator declares an external
	// domain") — this node never attempts to become the domain's
	// grandmaster.
	ClientOnly bool

	// Priority1 is ptp4l's own BMCA priority1, defaulting to 248 in auto
	// so professional gear declaring 128 always wins the domain (RES-019
	// §1). A non-zero value here overrides the default outright — this
	// package does not implement "auto" as a distinct third state,
	// leaving that policy decision at the config-decode boundary (see
	// internal/coordinator/config/nodeclock.go).
	Priority1 int

	// HardwareTimestamping requests hardware timestamping with a
	// software fallback attempt (RES-019 §1). Whether the interface
	// actually has PHC support is [PHCIndexForInterface]'s own evidence,
	// checked at Start, not assumed from this flag.
	HardwareTimestamping bool

	// RunDir is the base directory this provider writes its ptp4l config
	// and UDS sockets under (RES-019 §1: "under /run or /tmp" — this
	// package always chooses /run: this seam's own bench found a custom
	// socket path under /tmp undeliverable in a sandboxed container,
	// while the identical path under /run worked, so /run is not merely
	// the production default, it is the one path shown to work).
	// Defaults to [DefaultManagedRunDir].
	RunDir string
}

// DefaultManagedRunDir is where [NewManagedProvider] writes its config
// and UDS sockets when [ManagedConfig.RunDir] is empty.
const DefaultManagedRunDir = "/run/showmesh-ptp4l"

// ManagedOwner is [RawStatus.Owner]'s value for every reading a
// ManagedProvider reports — this process, never a name implying some
// other, unidentified component (see [ExternalProvider]'s "external
// (unidentified)" for the contrast).
const ManagedOwner = "showmesh"

// ManagedProvider writes a ptp4l config, supervises the process, and
// polls its own management socket for status — RES-019 §1's
// ShowMesh-managed linuxptp provider. It refuses to start when any ptp4l
// is already bound on the configured interface and domain (RES-019 §5.3:
// exactly one component owns ptp4l on an interface) — see
// [NewManagedProvider].
type ManagedProvider struct {
	cfg ManagedConfig

	confPath string
	rwSocket string
	roSocket string

	mu       sync.Mutex
	cmd      *exec.Cmd
	running  bool
	lastExit string // human-readable reason the last run ended, "" while running or never started

	stop   chan struct{}
	done   chan struct{}
	logger Logger
}

// socketNameFor derives this provider's own deterministic UDS socket
// names from iface/domain, so two ManagedProvider instances for the same
// interface+domain (this process restarted, or a second one started by
// mistake) collide on the same path and the ownership pre-check below can
// see it.
func socketNameFor(runDir, iface string, domain int, suffix string) string {
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(iface)
	return filepath.Join(runDir, fmt.Sprintf("ptp4l-%s-%d-%s", safe, domain, suffix))
}

// NewManagedProvider validates cfg, runs the ownership pre-check
// (RES-019 §5.3), writes the ptp4l config file, and returns a
// ManagedProvider — but does NOT start the supervised process; call
// [ManagedProvider.Start] for that. Separating construction from Start
// lets a caller inspect the pre-check's own refusal (if any) before
// committing to run anything.
func NewManagedProvider(cfg ManagedConfig, logger Logger) (*ManagedProvider, error) {
	if cfg.Interface == "" {
		return nil, fmt.Errorf("clock: managed provider requires an interface")
	}
	if cfg.RunDir == "" {
		cfg.RunDir = DefaultManagedRunDir
	}
	if cfg.Priority1 == 0 {
		cfg.Priority1 = 248
	}

	if err := os.MkdirAll(cfg.RunDir, 0o755); err != nil {
		return nil, fmt.Errorf("clock: create managed provider run directory %s: %w", cfg.RunDir, err)
	}

	rwSocket := socketNameFor(cfg.RunDir, cfg.Interface, cfg.Domain, "rw")
	roSocket := socketNameFor(cfg.RunDir, cfg.Interface, cfg.Domain, "ro")

	if conflict, reason := ownershipConflict(cfg.Interface, rwSocket); conflict {
		return nil, fmt.Errorf("clock: refusing to start a managed ptp4l on interface %s domain %d: %s", cfg.Interface, cfg.Domain, reason)
	}

	confPath := filepath.Join(cfg.RunDir, fmt.Sprintf("ptp4l-%s-%d.conf", strings.NewReplacer("/", "_", " ", "_").Replace(cfg.Interface), cfg.Domain))
	if err := writePTP4LConfig(confPath, cfg, rwSocket, roSocket); err != nil {
		return nil, fmt.Errorf("clock: write ptp4l config: %w", err)
	}

	return &ManagedProvider{cfg: cfg, confPath: confPath, rwSocket: rwSocket, roSocket: roSocket, logger: logger}, nil
}

// writePTP4LConfig renders cfg into ptp4l's [global] config file syntax.
func writePTP4LConfig(path string, cfg ManagedConfig, rwSocket, roSocket string) error {
	var b strings.Builder
	fmt.Fprintln(&b, "[global]")
	fmt.Fprintf(&b, "domainNumber %d\n", cfg.Domain)
	fmt.Fprintf(&b, "priority1 %d\n", cfg.Priority1)
	fmt.Fprintln(&b, "priority2 128")
	if cfg.ClientOnly {
		fmt.Fprintln(&b, "clientOnly 1")
	}
	if cfg.HardwareTimestamping {
		fmt.Fprintln(&b, "time_stamping hardware")
	} else {
		fmt.Fprintln(&b, "time_stamping software")
	}
	fmt.Fprintf(&b, "uds_address %s\n", rwSocket)
	fmt.Fprintf(&b, "uds_ro_address %s\n", roSocket)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// ownershipConflict is RES-019 §5.3's mandatory pre-check, "checked by
// both the process table and the presence of the UDS socket": a ptp4l
// process already bound to iface (regardless of what domain it turns out
// to be running, per §5.3's stronger interface-wide statement: "exactly
// one component owns ptp4l on an interface" — a second instance on the
// same interface under a DIFFERENT domain would still fight the first one
// for the same UDP ports 319/320 on that interface), or this identity's
// own deterministic UDS socket path already existing (a still-running
// instance from an earlier start this process lost track of, or another
// ShowMesh agent process racing to start the same interface+domain).
func ownershipConflict(iface, rwSocket string) (conflict bool, reason string) {
	if pid, cmdline, ok := findRunningPTP4L(iface); ok {
		return true, fmt.Sprintf("a ptp4l process (pid %d) is already bound to interface %s: %s", pid, iface, cmdline)
	}
	if _, err := os.Stat(rwSocket); err == nil {
		return true, fmt.Sprintf("management socket %s already exists; another ptp4l instance may already own this interface and domain", rwSocket)
	}
	return false, ""
}

// findRunningPTP4L scans /proc for a running ptp4l process whose command
// line names iface via "-i <iface>". ok is false when /proc cannot be
// read at all (permissions) or no match is found — a read failure is
// intentionally NOT itself a refusal reason: an agent that cannot read
// /proc still has the UDS-socket check as a second signal (see
// [ownershipConflict]'s own doc comment on why this is checked twice).
func findRunningPTP4L(iface string) (pid int, cmdline string, ok bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, "", false
	}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) == 0 {
			continue
		}
		if filepath.Base(args[0]) != "ptp4l" {
			continue
		}
		for i, a := range args {
			if a == "-i" && i+1 < len(args) && args[i+1] == iface {
				return p, strings.Join(args, " "), true
			}
		}
	}
	return 0, "", false
}

func (p *ManagedProvider) Kind() ProviderKind { return ProviderManaged }
func (p *ManagedProvider) Interface() string  { return p.cfg.Interface }

// Now reads the PHC associated with this provider's interface, when
// hardware timestamping actually negotiated a PHC (RES-019 §1's
// ETHTOOL_GET_TS_INFO lookup), falling back to CLOCK_REALTIME when this
// provider's ptp4l instance runs in software timestamping mode (RES-019
// §1: "software-timestamped ptp4l disciplines CLOCK_REALTIME itself").
func (p *ManagedProvider) Now(ctx context.Context) MediaTime {
	if !p.cfg.HardwareTimestamping {
		return MediaTime{Time: time.Now(), Valid: true, Reason: "software timestamping: reading CLOCK_REALTIME, which this node's ptp4l disciplines directly"}
	}
	index, ok, err := PHCIndexForInterface(p.cfg.Interface)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("PHC lookup for %s failed: %v", p.cfg.Interface, err)}
	}
	if !ok {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("interface %s has no associated PHC despite hardware timestamping being requested", p.cfg.Interface)}
	}
	t, err := ReadPHC(index)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("reading /dev/ptp%d: %v (distributions ship this device root:root 0600; this agent may need a udev rule or group membership)", index, err)}
	}
	return MediaTime{Time: t, Valid: true}
}

// Start launches the supervised ptp4l process against the config
// [NewManagedProvider] already wrote, and begins a background restart
// loop (a crash is restarted with a fixed backoff; ShowMesh never treats
// a supervised ptp4l crash as a reason to give up supervising it, the
// same standing rule every other supervised process in this codebase
// follows).
func (p *ManagedProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.stop != nil {
		p.mu.Unlock()
		return fmt.Errorf("clock: managed provider for %s domain %d is already started", p.cfg.Interface, p.cfg.Domain)
	}
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	p.mu.Unlock()

	go p.superviseLoop()
	return nil
}

const managedRestartBackoff = 5 * time.Second

func (p *ManagedProvider) superviseLoop() {
	// stop and done are captured once, locally, right after Start()
	// already set them (happens-before via the goroutine's own creation)
	// rather than read from p.stop/p.done on every loop iteration:
	// [ManagedProvider.Stop] mutates those fields under p.mu from another
	// goroutine, and reading a field the select statement itself touches
	// without holding that lock would race against it.
	p.mu.Lock()
	stop, done := p.stop, p.done
	p.mu.Unlock()

	defer close(done)
	backoff := time.NewTimer(0)
	defer backoff.Stop()
	for {
		select {
		case <-stop:
			return
		case <-backoff.C:
		}

		cmd := exec.Command(ptp4lBinary, "-f", p.confPath, "-i", p.cfg.Interface, "-m")
		if err := cmd.Start(); err != nil {
			p.setRunResult(false, fmt.Sprintf("failed to start ptp4l: %v", err))
			backoff.Reset(managedRestartBackoff)
			continue
		}

		p.mu.Lock()
		p.cmd = cmd
		p.running = true
		p.lastExit = ""
		p.mu.Unlock()

		err := cmd.Wait()

		p.mu.Lock()
		alreadyStopping := p.stop == nil
		p.mu.Unlock()
		if alreadyStopping {
			return
		}

		reason := "ptp4l exited"
		if err != nil {
			reason = fmt.Sprintf("ptp4l exited: %v", err)
		}
		p.setRunResult(false, reason)
		if p.logger != nil {
			p.logger.Warn("managed ptp4l exited; restarting after backoff", "interface", p.cfg.Interface, "domain", p.cfg.Domain, "reason", reason)
		}
		backoff.Reset(managedRestartBackoff)
	}
}

func (p *ManagedProvider) setRunResult(running bool, reason string) {
	p.mu.Lock()
	p.running = running
	p.lastExit = reason
	p.mu.Unlock()
}

// Poll reports this provider's own supervised process's status: not
// reachable at all when the process is not currently running (RES-019
// §9: "the ptp4l owner stopping or restarting it is failed then
// acquiring" — this provider IS the owner, so its own process's lifecycle
// is exactly that signal), otherwise the same UDS-socket read
// [ExternalProvider.Poll] performs, against this provider's own
// read-only socket.
func (p *ManagedProvider) Poll(ctx context.Context) RawStatus {
	p.mu.Lock()
	running := p.running
	lastExit := p.lastExit
	p.mu.Unlock()

	if !running {
		reason := "this node's managed ptp4l is not currently running"
		if lastExit != "" {
			reason = lastExit
		}
		return RawStatus{Reachable: false, Reason: reason}
	}

	if _, err := os.Stat(p.roSocket); err != nil {
		return RawStatus{Reachable: false, Reason: fmt.Sprintf("managed ptp4l process is running but its management socket %s is not yet present: %v", p.roSocket, err)}
	}

	raw := pollViaUDS(ctx, p.roSocket, p.cfg.Domain, ManagedOwner)
	if raw.Reachable {
		raw.Timestamping, raw.TimestampingKnown = timestampingFromConfig(p.cfg), true
	}
	return raw
}

func timestampingFromConfig(cfg ManagedConfig) Timestamping {
	if cfg.HardwareTimestamping {
		return TimestampingHardware
	}
	return TimestampingSoftware
}

// Stop signals the supervise loop to stop restarting and kills the
// current process, if any, waiting for the loop to fully exit before
// returning. Distinct from [ManagedProvider.Close] only in name — Close
// exists to satisfy [Provider], and calls Stop.
func (p *ManagedProvider) Stop() {
	p.mu.Lock()
	if p.stop == nil {
		p.mu.Unlock()
		return
	}
	stop := p.stop
	done := p.done
	cmd := p.cmd
	p.stop = nil
	p.mu.Unlock()

	close(stop)
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-done
}

// Close stops the supervised process — see [ManagedProvider.Stop].
func (p *ManagedProvider) Close() error {
	p.Stop()
	return nil
}
