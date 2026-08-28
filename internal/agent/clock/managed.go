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

	// HardwareTimestamping requests hardware timestamping (RES-019 §1).
	// [ManagedProvider.superviseLoop] attempts it first; if ptp4l exits
	// within its own startup probe window — the real, observed failure
	// mode of a driver with no hardware timestamping support, not a
	// hypothetical — it falls back to software and retries, exactly once
	// per Start. Whether the mode actually reached is hardware or
	// software is never assumed from this flag: read it from
	// [ManagedProvider.reachedTimestampingMode] (which
	// [ManagedProvider.Now] and [ManagedProvider.Poll] already do).
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

	// reached/reachedKnown are the timestamping mode this provider's
	// CURRENT ptp4l attempt has actually confirmed running past its
	// startup probe window — never the requested cfg.HardwareTimestamping.
	// RES-019 §4.1 item 6 names the hazard directly: reading a PHC that
	// ptp4l is not actually disciplining (because it silently fell back,
	// or because it never started at all) reports a locked clock whose
	// time is wrong, which is worse than reporting none. Set only by
	// [ManagedProvider.superviseLoop], read by [ManagedProvider.Now] and
	// [ManagedProvider.Poll] under mu.
	reached      Timestamping
	reachedKnown bool

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
	if err := writePTP4LConfig(confPath, cfg, cfg.HardwareTimestamping, rwSocket, roSocket); err != nil {
		return nil, fmt.Errorf("clock: write ptp4l config: %w", err)
	}

	return &ManagedProvider{cfg: cfg, confPath: confPath, rwSocket: rwSocket, roSocket: roSocket, logger: logger}, nil
}

// writePTP4LConfig renders cfg into ptp4l's [global] config file syntax.
// hardware selects the "time_stamping" line WRITTEN, deliberately separate
// from cfg.HardwareTimestamping (the operator's own REQUESTED mode): the
// supervise loop rewrites this file to request software after a hardware
// attempt fails its startup probe (see [ManagedProvider.superviseLoop]),
// and that rewrite must not be able to silently un-request client-only or
// any other cfg-derived line by reconstructing cfg from scratch.
func writePTP4LConfig(path string, cfg ManagedConfig, hardware bool, rwSocket, roSocket string) error {
	var b strings.Builder
	fmt.Fprintln(&b, "[global]")
	fmt.Fprintf(&b, "domainNumber %d\n", cfg.Domain)
	fmt.Fprintf(&b, "priority1 %d\n", cfg.Priority1)
	fmt.Fprintln(&b, "priority2 128")
	if cfg.ClientOnly {
		fmt.Fprintln(&b, "clientOnly 1")
	}
	if hardware {
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

// reachedTimestampingMode reports the timestamping mode CURRENTLY IN
// EFFECT: the mode this provider's ptp4l confirmed running past its
// startup probe window, AND that same process is still running right
// now. known is false whenever either half is not true — before any
// attempt has run long enough to confirm a mode, and again the instant
// that process is no longer running (a crash, or a Stop): a confirmation
// from a process that already exited is exactly the "ptp4l is not
// actually disciplining this" hazard [ManagedProvider.reached]'s own doc
// comment names, so it must not outlive the process it describes.
func (p *ManagedProvider) reachedTimestampingMode() (Timestamping, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return "", false
	}
	return p.reached, p.reachedKnown
}

// Now reads the PHC associated with this provider's interface when
// hardware timestamping is the mode THIS PROVIDER'S PTP4L HAS ACTUALLY
// REACHED (never the requested [ManagedConfig.HardwareTimestamping]: a
// request that fails its startup probe falls back to software within
// [ManagedProvider.superviseLoop], and reading a PHC ptp4l is not
// disciplining reports a locked clock with the wrong time — RES-019 §4.1
// item 6 names this exact hazard in FPP's own code), falling back to
// CLOCK_REALTIME when the reached mode is software (RES-019 §1:
// "software-timestamped ptp4l disciplines CLOCK_REALTIME itself").
func (p *ManagedProvider) Now(ctx context.Context) MediaTime {
	reached, known := p.reachedTimestampingMode()
	if !known {
		return MediaTime{Valid: false, Reason: "no timestamping mode confirmed yet: this node's managed ptp4l has not been observed running past its startup probe"}
	}
	if reached != TimestampingHardware {
		return MediaTime{Time: time.Now(), Valid: true, Reason: "software timestamping: reading CLOCK_REALTIME, which this node's ptp4l disciplines directly"}
	}
	index, ok, err := PHCIndexForInterface(p.cfg.Interface)
	if err != nil {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("PHC lookup for %s failed: %v", p.cfg.Interface, err)}
	}
	if !ok {
		return MediaTime{Valid: false, Reason: fmt.Sprintf("interface %s has no associated PHC despite hardware timestamping being reached", p.cfg.Interface)}
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

// managedRestartBackoff and hardwareTimestampingProbeWindow are package
// vars (not consts), matching [ptp4lBinary]/[pmcBinary]'s own override
// convention, so a test can shrink both rather than waiting on production
// timing.
var (
	managedRestartBackoff           = 5 * time.Second
	hardwareTimestampingProbeWindow = 2 * time.Second
)

// superviseLoop is this provider's entire process-supervision state
// machine, running on its own goroutine for the life of one Start call.
//
// When ManagedConfig.HardwareTimestamping is set, the FIRST attempt
// requests hardware; every attempt after a fallback, and every attempt
// when hardware was never requested, requests software — RES-019 §1's
// "hardware timestamping with a software fallback attempt" (FPP itself
// makes the analogous attempt: hardware+DSCP, software+DSCP, software).
// "Exits early" is judged against hardwareTimestampingProbeWindow, an
// explicit bounded startup window, not a guess: a driver that cannot
// timestamp in hardware fails ptp4l's own clock-creation call within
// milliseconds (confirmed against a real NIC while building this seam),
// so a process still alive past this window has genuinely reached the
// mode it was started with. fellBack is scoped to ONE Start call (a local
// variable in this function, reset by construction on every fresh
// superviseLoop goroutine) so a later explicit restart may attempt
// hardware again, but a fallback already taken within this Start is never
// undone by a subsequent crash-restart.
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

	mode := TimestampingSoftware
	if p.cfg.HardwareTimestamping {
		mode = TimestampingHardware
	}
	fellBack := false

	backoff := time.NewTimer(0)
	defer backoff.Stop()
	for {
		select {
		case <-stop:
			return
		case <-backoff.C:
		}

		attemptingHardware := mode == TimestampingHardware
		if err := writePTP4LConfig(p.confPath, p.cfg, attemptingHardware, p.rwSocket, p.roSocket); err != nil {
			p.setRunResult(false, fmt.Sprintf("failed to write ptp4l config for a %s timestamping attempt: %v", mode, err))
			backoff.Reset(managedRestartBackoff)
			continue
		}

		cmd := exec.Command(ptp4lBinary, "-f", p.confPath, "-i", p.cfg.Interface, "-m")
		if err := cmd.Start(); err != nil {
			p.setRunResult(false, fmt.Sprintf("failed to start ptp4l: %v", err))
			backoff.Reset(managedRestartBackoff)
			continue
		}

		p.mu.Lock()
		p.cmd = cmd
		p.mu.Unlock()

		exitCh := make(chan error, 1)
		go func() { exitCh <- cmd.Wait() }()

		probeTimer := time.NewTimer(hardwareTimestampingProbeWindow)
		var exitErr error
		exitedEarly := false
		stoppedDuringProbe := false
		select {
		case exitErr = <-exitCh:
			exitedEarly = true
			probeTimer.Stop()
		case <-probeTimer.C:
		case <-stop:
			stoppedDuringProbe = true
			probeTimer.Stop()
		}

		if stoppedDuringProbe {
			_ = cmd.Process.Kill()
			<-exitCh
			return
		}

		if exitedEarly {
			p.mu.Lock()
			reallyStopping := p.stop == nil
			p.mu.Unlock()
			if reallyStopping {
				// Lost the race against Stop() closing stop and killing
				// this process: exitCh fired for that reason, not a
				// genuine early exit — see this file's own doc comment on
				// why this defensive re-check exists alongside the <-stop
				// select arm above.
				return
			}
		}

		if exitedEarly && attemptingHardware && !fellBack {
			fellBack = true
			mode = TimestampingSoftware
			reason := "ptp4l exited immediately while attempting hardware timestamping"
			if exitErr != nil {
				reason = fmt.Sprintf("%s: %v", reason, exitErr)
			}
			reason += "; falling back to software timestamping and retrying"
			p.setRunResult(false, reason)
			if p.logger != nil {
				p.logger.Warn("managed ptp4l fell back to software timestamping", "interface", p.cfg.Interface, "domain", p.cfg.Domain, "reason", reason)
			}
			p.mu.Lock()
			p.cmd = nil
			p.mu.Unlock()
			// Retry immediately: this is a one-time capability probe, not
			// a crash, so the ordinary restart backoff does not apply.
			// backoff already fired once (for THIS attempt) and, being a
			// time.Timer, never fires again on its own without a Reset —
			// without this, the loop's own top-of-iteration select would
			// block forever on backoff.C and the fallback would never
			// actually retry.
			backoff.Reset(0)
			continue
		}

		if exitedEarly {
			// A software attempt that failed outright, or a hardware
			// attempt that already fell back once this Start and failed
			// again — an ordinary crash from here on.
			reason := "ptp4l exited"
			if exitErr != nil {
				reason = fmt.Sprintf("ptp4l exited: %v", exitErr)
			}
			p.setRunResult(false, reason)
			if p.logger != nil {
				p.logger.Warn("managed ptp4l exited; restarting after backoff", "interface", p.cfg.Interface, "domain", p.cfg.Domain, "reason", reason)
			}
			backoff.Reset(managedRestartBackoff)
			continue
		}

		// Ran past the probe window: this attempt's mode is confirmed
		// reached — see [ManagedProvider.reached]'s own doc comment.
		p.mu.Lock()
		p.running = true
		p.lastExit = ""
		p.reached = mode
		p.reachedKnown = true
		p.mu.Unlock()

		err := <-exitCh

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

// setRunResult records that this provider is not (or is once again)
// confirmed running. reached/reachedKnown are left untouched here on
// purpose — they still name the mode the LAST run reached, for logging
// and debugging — but [ManagedProvider.reachedTimestampingMode] never
// reports them while running is false, which is what actually keeps
// [ManagedProvider.Now] from reading a PHC nothing is disciplining
// anymore.
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
		// The REACHED mode, never the requested ManagedConfig.
		// HardwareTimestamping — see [ManagedProvider.reached]'s own doc
		// comment. running is already true here (checked above), so
		// reachedTimestampingMode's "known" should always be true too;
		// TimestampingKnown still tracks it explicitly rather than
		// assuming, matching this package's "never fabricate a Known flag"
		// rule everywhere else.
		if reached, known := p.reachedTimestampingMode(); known {
			raw.Timestamping, raw.TimestampingKnown = reached, true
		}
	}
	return raw
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
