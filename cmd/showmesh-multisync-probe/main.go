// Command showmesh-multisync-probe is a bench instrument for RES-002
// (docs/research/RES-002-fpp-multisync-compatibility.md), not a general
// purpose packet dumper. It exists to collect evidence against RES-002's
// five open bench-verification items by capturing real MultiSync traffic
// from an FPP master and driving pkg/multisync's Listener and Timeline
// against it.
//
// # What this tool proves and what it does not
//
// pkg/multisync is currently L1 (source-verified): built by reading FPP's
// own documentation and source, never confirmed against a live FPP player.
// Running this probe and getting a clean-looking capture does not, by
// itself, raise that. RES-002 only moves to L2 once a human operator
// reviews a capture's evidence (the JSONL file this tool writes, and the
// summary it prints) against what they know they did on the FPP side
// during that capture, and records the result in RES-002 and
// docs/research/README.md. This tool collects evidence; it does not
// interpret it into a verification verdict, and its summary output says so
// explicitly for every item it cannot fully answer from network evidence
// alone.
//
// See docs/bench/RES-002-capture-procedure.md for the operator procedure
// this tool is meant to be run under.
//
// # Network effect
//
// By default this tool only receives; it transmits nothing and has zero
// effect on the show network. Passing -respond-discover changes that: see
// that flag's help text.
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

func main() {
	ifaceFlag := flag.String("iface", "", "network interface to join the MultiSync multicast group on (default: every suitable interface)")
	listenFlag := flag.String("listen", "", "listen address override, host:port (default \":32320\", FPP's real FPP_CTRL_PORT; override only for a non-standard bench setup)")
	outFlag := flag.String("out", "", "output JSONL capture path (default: showmesh-multisync-capture-<UTC timestamp>.jsonl in the current directory)")
	durationFlag := flag.Duration("duration", 0, "capture duration; 0 (default) runs until interrupted with Ctrl-C or SIGTERM")
	respondFlag := flag.Bool("respond-discover", false,
		"answer discover pings so this probe appears in FPP's MultiSync UI, per RES-002's documented non-FPP device etiquette. "+
			"OFF BY DEFAULT: unlike every other flag here, this one TRANSMITS Ping packets onto the network when a discover ping is observed. "+
			"Leave it off for a pure listen-only capture.")
	stepMSFlag := flag.Int("step-ms", int(multisync.DefaultStepTime/time.Millisecond),
		"assumed step time in milliseconds. RES-002 records that MultiSync never carries rate on the wire; this only affects position "+
			"derived from FrameNumber on packets where SecondsElapsed is unusable (zero), and the timeline snapshots and drift series this "+
			"tool records. It is a guess, not something this tool can verify from the network.")
	quietFlag := flag.Bool("quiet", false, "suppress the human-readable per-packet line on stdout; the JSONL capture is written either way")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.String())
		os.Exit(0)
	}

	os.Exit(run(runConfig{
		iface:      *ifaceFlag,
		listen:     *listenFlag,
		out:        *outFlag,
		duration:   *durationFlag,
		respond:    *respondFlag,
		stepTimeMS: *stepMSFlag,
		quiet:      *quietFlag,
	}))
}

type runConfig struct {
	iface      string
	listen     string
	out        string
	duration   time.Duration
	respond    bool
	stepTimeMS int
	quiet      bool
}

// run performs setup, then the capture, then prints the summary. Per its
// contract with RES-002 evidence-gathering: it exits non-zero only when
// setup itself failed (could not create the output file, could not bind
// the socket, could not join a named interface); receiving zero packets
// during an otherwise successful capture is not a failure, it is itself a
// finding, and the summary says so.
func run(cfg runConfig) int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	outPath := cfg.out
	if outPath == "" {
		outPath = fmt.Sprintf("showmesh-multisync-capture-%s.jsonl", time.Now().UTC().Format("20060102T150405Z"))
	}
	outFile, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "showmesh-multisync-probe: creating output file %q: %v\n", outPath, err)
		return 1
	}
	jsonlw := bufio.NewWriter(outFile)
	outputClosed := false
	closeOutput := func() {
		// Idempotent: called once explicitly, right after the capture ends
		// (so the summary can safely report the output file as complete),
		// and once more via defer as a safety net for early-return paths.
		// os.File.Close is not itself safe to call twice (the second call
		// returns an error), so without this guard the defer would log a
		// spurious "file already closed" warning on every normal run.
		if outputClosed {
			return
		}
		outputClosed = true
		if err := jsonlw.Flush(); err != nil {
			logger.Warn("failed to flush JSONL output", "error", err)
		}
		if err := outFile.Close(); err != nil {
			logger.Warn("failed to close JSONL output file", "error", err)
		}
	}
	defer closeOutput()

	stepTime := time.Duration(cfg.stepTimeMS) * time.Millisecond
	if stepTime <= 0 {
		stepTime = multisync.DefaultStepTime
	}

	lcfg := multisync.ListenerConfig{
		InterfaceName:          cfg.iface,
		RespondToDiscoverPings: cfg.respond,
		DiscoverResponse:       discoverResponsePing(),
		Logger:                 logger,
	}
	if cfg.listen != "" {
		lcfg.ListenAddr = cfg.listen
	}

	l, err := multisync.NewListener(lcfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "showmesh-multisync-probe: setup failed: %v\n", err)
		return 1
	}

	stdout := &summaryWriter{w: os.Stdout}

	fmt.Printf("showmesh-multisync-probe %s\n", version.String())
	fmt.Printf("listening on %s\n", l.LocalAddr())
	printJoinResults(stdout, l.JoinResults())
	if cfg.respond {
		fmt.Println("discover-ping responses: ENABLED -- this run WILL transmit Ping packets onto the network when it observes a discover ping")
	} else {
		fmt.Println("discover-ping responses: disabled (default) -- this run is receive-only, it transmits nothing")
	}
	fmt.Printf("output: %s\n", outPath)
	fmt.Println()

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
		fmt.Printf("capturing for %s (or until interrupted)...\n", cfg.duration)
	} else {
		fmt.Println("capturing until interrupted (Ctrl-C)...")
	}

	capt := newCapture(stepTime, jsonlw, logger, cfg.quiet)
	runErr := l.Run(ctx, capt.handle)

	closeOutput()

	fmt.Println()
	capt.printSummary(stdout, l, outPath, stepTime, cfg)
	if stdout.err != nil {
		// The summary itself is not evidence (the JSONL file is); a failure
		// writing it to stdout is worth a log line but never worth changing
		// the exit code or hiding whatever the capture actually did.
		logger.Warn("failed to write part of the summary report to stdout", "error", stdout.err)
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "showmesh-multisync-probe: listener stopped with an error: %v\n", runErr)
		return 1
	}
	return 0
}

// discoverResponsePing builds the Ping packet this probe answers discover
// requests with when -respond-discover is enabled. Leaving these fields at
// their zero values, as an earlier version of this tool did, means that even
// once the response reaches the right destination port, FPP's MultiSync UI
// displays this probe as version 0.0 with an empty hostname and hardware
// string. multisync.NewListener already fills in Hostname (via os.Hostname)
// and SystemType (multisync.SystemTypeShowMesh) when left at the zero value,
// but the fields only this tool can reasonably supply, real version
// information and a hardware identity string, are populated here.
func discoverResponsePing() multisync.PingPacket {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "showmesh-multisync-probe"
	}
	major, minor := probeVersionNumbers()
	return multisync.PingPacket{
		SystemType:    multisync.SystemTypeShowMesh,
		VersionMajor:  major,
		VersionMinor:  minor,
		Hostname:      hostname,
		VersionString: version.Version,
		HardwareType:  "ShowMesh multisync probe",
		// IP is deliberately left at its zero value (0.0.0.0), not an
		// oversight like the fields above were. RES-002's "Third-party
		// interoperability" section (accessed 2026-08-10) records
		// ControlProtocol.txt's etiquette for non-FPP devices: identify with
		// IP 0.0.0.0 rather than claiming a routable address of its own in
		// this field.
	}
}

// probeVersionNumbers derives a PingPacket VersionMajor/VersionMinor pair
// from internal/version.Version. That value is "dev" for an unversioned
// local build (see internal/version's doc comment) and is not guaranteed to
// be a plain "MAJOR.MINOR[.PATCH]" string even for a released build, so this
// parses what it can and otherwise falls back to 0.1: a deliberately
// non-zero placeholder, so an unversioned build does not read in FPP's UI as
// indistinguishable from the "never populated at all" bug this fixes.
func probeVersionNumbers() (major, minor uint16) {
	v := strings.TrimPrefix(version.Version, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		maj, majErr := strconv.ParseUint(parts[0], 10, 16)
		min, minErr := strconv.ParseUint(parts[1], 10, 16)
		if majErr == nil && minErr == nil {
			return uint16(maj), uint16(min)
		}
	}
	return 0, 1
}

// summaryWriter wraps an io.Writer so the many small Fprint calls the
// summary report makes (dozens, across printSummary and its per-item
// helpers) can be written as plain Printf/Println calls instead of each
// individually checking a write error that, writing to stdout, essentially
// never occurs. The first error (if any) is latched in err rather than
// silently discarded: every call after the first failure becomes a no-op,
// and the caller checks err once at the end.
type summaryWriter struct {
	w   io.Writer
	err error
}

func (s *summaryWriter) Printf(format string, args ...any) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintf(s.w, format, args...)
}

func (s *summaryWriter) Println(args ...any) {
	if s.err != nil {
		return
	}
	_, s.err = fmt.Fprintln(s.w, args...)
}

func printJoinResults(w *summaryWriter, results []multisync.JoinResult) {
	if len(results) == 0 {
		w.Println("multicast join: no suitable interface was found or selected; multicast group join was not attempted (unicast/broadcast delivery on this socket is unaffected)")
		return
	}
	for _, r := range results {
		if r.Err != nil {
			w.Printf("multicast join: %s: FAILED: %v\n", r.Interface.Name, r.Err)
		} else {
			w.Printf("multicast join: %s: joined %s\n", r.Interface.Name, multisync.MulticastGroup)
		}
	}
}

// --- capture: the stateful evidence collector driven by the Listener's
// Handler on every received datagram ---

// capture accumulates everything the summary report and the JSONL file
// need. It is only ever driven from the Listener's single read goroutine
// (via handle) and then read from the main goroutine after Run has
// returned, so it needs no locking of its own.
//
// JUDGMENT CALL: this runs two Timeline instances, one for sequence files
// and one for media files, rather than one. Timeline (per its own doc
// comment in pkg/multisync/timeline.go) tracks exactly one file at a time
// and treats a change of filename as a new session. A real FPP playlist
// entry can drive a sequence sync stream and a media (audio) sync stream
// concurrently, each with its own filename; feeding both into a single
// Timeline would make every sequence/media alternation look like a file
// change, corrupting the correction counts and drift bookkeeping this tool
// exists to collect. Two Timelines, split by SyncFileType, is what "drive a
// Timeline from the observed sync packets" is taken to mean here; both
// snapshots are recorded on every JSONL record, labeled separately.
type capture struct {
	stepTime time.Duration
	jsonlw   *bufio.Writer
	logger   *slog.Logger
	quiet    bool

	startedAt time.Time

	seqTimeline   *multisync.Timeline
	mediaTimeline *multisync.Timeline

	packetCount     int
	firstPacketAt   time.Time
	lastPacketAt    time.Time
	transportCounts map[multisync.Transport]int
	ifaceHits       map[int]int
	sourceAddrs     map[string]struct{}

	lastSeqSyncAt   time.Time
	lastMediaSyncAt time.Time
	seqIntervals    []time.Duration
	mediaIntervals  []time.Duration

	lifecycleEvents []lifecycleEvent
	stopBlankEvents []stopBlankEvent

	seqDrift        driftTracker
	mediaDrift      driftTracker
	seqDriftSamples []driftSample
	medDriftSamples []driftSample
}

func newCapture(stepTime time.Duration, jsonlw *bufio.Writer, logger *slog.Logger, quiet bool) *capture {
	return &capture{
		stepTime:        stepTime,
		jsonlw:          jsonlw,
		logger:          logger,
		quiet:           quiet,
		startedAt:       time.Now(),
		seqTimeline:     multisync.NewTimeline(time.Now, multisync.Config{StepTime: stepTime}),
		mediaTimeline:   multisync.NewTimeline(time.Now, multisync.Config{StepTime: stepTime}),
		transportCounts: make(map[multisync.Transport]int),
		ifaceHits:       make(map[int]int),
		sourceAddrs:     make(map[string]struct{}),
	}
}

// lifecycleEvent records one OPEN, START, or STOP MultiSync packet. SYNC
// action packets are deliberately not recorded here: their cadence is what
// RES-002 open item 1 asks about and is covered by the inter-arrival
// tracking instead, and including thousands of them here would bury the
// small number of lifecycle transitions item 2 actually cares about.
type lifecycleEvent struct {
	At             time.Time
	Action         multisync.SyncAction
	FileType       multisync.SyncFileType
	Filename       string
	FrameNumber    uint32
	SecondsElapsed float32
	Source         string
}

// stopBlankEvent records one STOP (MultiSync action Stop) or BLANK (packet
// type 0x03) event, for RES-002 open item 3.
type stopBlankEvent struct {
	At       time.Time
	Kind     string // "STOP" or "BLANK"
	FileType multisync.SyncFileType
	Filename string
	Source   string
}

// driftTracker holds an independent, never-corrected free-run reference for
// one file (sequence or media): the position it would estimate right now if
// it only ever extrapolated from the last (re)anchor point and never
// applied any of Timeline's slew/skip/jump correction. This is deliberately
// separate machinery from Timeline itself (see the capture doc comment):
// RES-002 open item 4 asks how far a naive free-running clock drifts from
// the master between corrections, which is exactly what Timeline's own
// correction logic prevents from being observable if this tool only read
// Timeline's already-corrected position.
type driftTracker struct {
	have      bool
	filename  string
	anchorAt  time.Time
	anchorPos time.Duration
}

func (d *driftTracker) reset(at time.Time, filename string, pos time.Duration) {
	d.have = true
	d.filename = filename
	d.anchorAt = at
	d.anchorPos = pos
}

func (d *driftTracker) estimate(at time.Time) time.Duration {
	if !d.have {
		return 0
	}
	return d.anchorPos + at.Sub(d.anchorAt)
}

// driftSample is one comparison of the master's reported position against
// this tool's independent free-run estimate, at the moment a sync packet
// was received.
type driftSample struct {
	At                     time.Time
	FileType               multisync.SyncFileType
	Filename               string
	MasterPositionMS       float64
	LocalFreeRunEstimateMS float64
	DeltaMS                float64
}

// positionFromSync mirrors Timeline's own positionFromPacketLocked
// (unexported, so re-implemented here): prefer SecondsElapsed when it is
// usable (greater than zero), matching FPP's own remote and RES-002's
// documented semantics; otherwise fall back to FrameNumber converted via
// the assumed step time. See -step-ms's help text for why that fallback is
// a guess this tool cannot verify from the wire.
func positionFromSync(p multisync.SyncPacket, stepTime time.Duration) time.Duration {
	if p.SecondsElapsed > 0 {
		return time.Duration(float64(p.SecondsElapsed) * float64(time.Second))
	}
	return time.Duration(p.FrameNumber) * stepTime
}

func msFloat(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// handle is the multisync.Handler this capture drives the Listener with. It
// runs on the Listener's single read goroutine.
func (c *capture) handle(rec multisync.Received) {
	now := rec.ReceivedAt
	c.packetCount++
	if c.firstPacketAt.IsZero() {
		c.firstPacketAt = now
	}
	c.lastPacketAt = now
	c.transportCounts[rec.Transport]++
	if rec.IfIndex != 0 {
		c.ifaceHits[rec.IfIndex]++
	}
	if rec.SrcAddr != nil {
		c.sourceAddrs[rec.SrcAddr.IP.String()] = struct{}{}
	}

	jr := jsonlRecord{
		WallClock:         now,
		MonotonicOffsetMS: now.Sub(c.startedAt).Milliseconds(),
		Transport:         string(rec.Transport),
		IfIndex:           rec.IfIndex,
		RawHex:            hex.EncodeToString(rec.Raw),
		HeaderType:        rec.Header.Type.String(),
		HeaderExtraLen:    rec.Header.ExtraDataLen,
	}
	if rec.SrcAddr != nil {
		jr.Source = rec.SrcAddr.String()
	}
	timelineSource := timelineSourceIdentity(rec.SrcAddr)
	if rec.DstAddr != nil {
		jr.DestAddr = rec.DstAddr.String()
	}
	if rec.DecodeErr != nil {
		jr.DecodeError = rec.DecodeErr.Error()
		jr.DecodeErrorKind = classifyDecodeError(rec.DecodeErr)
	}

	switch p := rec.Payload.(type) {
	case multisync.SyncPacket:
		jr.Sync = &syncFieldsJSON{
			Action:         p.Action.String(),
			FileType:       p.FileType.String(),
			FrameNumber:    p.FrameNumber,
			SecondsElapsed: p.SecondsElapsed,
			Filename:       p.Filename,
		}
		if p.Action == multisync.SyncActionSync {
			// Cadence means the interval between SYNC packets, not between
			// lifecycle events: a playlist of N entries injects N near-zero
			// OPEN-to-START gaps and N multi-second STOP-to-next-OPEN gaps
			// into this series if every sync-type packet is counted here,
			// which makes RES-002 item 1's min/max meaningless. Same
			// reasoning already applied to observeDrift below, which also
			// samples only SyncActionSync.
			c.observeInterArrival(p.FileType, now)
		}
		c.observeLifecycle(p, now, jr.Source)
		if sample := c.observeDrift(p, now); sample != nil {
			jr.Drift = sample
		}
		c.timelineFor(p.FileType).Observe(p, timelineSource)

	case multisync.BlankPacket:
		jr.Blank = true
		c.stopBlankEvents = append(c.stopBlankEvents, stopBlankEvent{At: now, Kind: "BLANK", Source: jr.Source})

	case multisync.PingPacket:
		jr.Ping = &pingFieldsJSON{
			Version:       p.Version,
			SubType:       p.SubType.String(),
			SystemType:    p.SystemType.String(),
			VersionMajor:  p.VersionMajor,
			VersionMinor:  p.VersionMinor,
			Mode:          p.Mode.String(),
			IP:            fmt.Sprintf("%d.%d.%d.%d", p.IP[0], p.IP[1], p.IP[2], p.IP[3]),
			Hostname:      p.Hostname,
			VersionString: p.VersionString,
			HardwareType:  p.HardwareType,
			Ranges:        p.Ranges,
		}

	case multisync.CommandPacket:
		jr.Command = &commandFieldsJSON{Host: p.Host, Command: p.Command, Args: p.Args}

	case multisync.PluginPacket:
		jr.Plugin = &pluginFieldsJSON{RawHex: hex.EncodeToString(p.Raw)}
	}

	jr.SequenceTimeline = toTimelineSnapshotJSON(c.seqTimeline.Snapshot())
	jr.MediaTimeline = toTimelineSnapshotJSON(c.mediaTimeline.Snapshot())

	c.writeJSONL(jr)
	if !c.quiet {
		c.printHumanLine(rec)
	}
}

func (c *capture) timelineFor(ft multisync.SyncFileType) *multisync.Timeline {
	if ft == multisync.SyncFileTypeMedia {
		return c.mediaTimeline
	}
	return c.seqTimeline
}

// timelineSourceIdentity derives the source identity passed to
// Timeline.Observe for competing-master detection. It deliberately returns
// the IP only, never "ip:port": RES-002 records that FPP's own
// SendControlPacket fans a single master's sync stream out over several
// independent, unbound sockets, one for unicast, one for broadcast, and one
// per interface for multicast, so a single real FPP master's packets arrive
// under a rotating cast of source ports, and every one of those ports
// changes again across an fppd restart. Feeding "ip:port" into Observe as
// the driving source therefore makes every SYNC packet that lands on a
// second source port look like a brand new competing master: Observe drops
// it as a conflict instead of applying it (see Timeline.Observe's doc
// comment), the timeline's position freezes, and even a subsequent STOP on
// yet another port is dropped the same way. This was reproduced directly: a
// captured run showed 40 consecutive SYNC packets from one master, arriving
// on a second source port, all rejected as a competing master. Do not
// "helpfully" restore the port here; the IP is deliberately the whole
// identity. A nil address or nil IP (should not happen for a real UDP
// datagram, but Received.SrcAddr's doc comment does not rule it out)
// returns "" rather than panicking; an empty source never conflicts, per
// Timeline.Observe's own handling of an empty source.
func timelineSourceIdentity(addr *net.UDPAddr) string {
	if addr == nil || addr.IP == nil {
		return ""
	}
	return addr.IP.String()
}

func (c *capture) observeInterArrival(ft multisync.SyncFileType, at time.Time) {
	switch ft {
	case multisync.SyncFileTypeMedia:
		if !c.lastMediaSyncAt.IsZero() {
			c.mediaIntervals = append(c.mediaIntervals, at.Sub(c.lastMediaSyncAt))
		}
		c.lastMediaSyncAt = at
	default:
		if !c.lastSeqSyncAt.IsZero() {
			c.seqIntervals = append(c.seqIntervals, at.Sub(c.lastSeqSyncAt))
		}
		c.lastSeqSyncAt = at
	}
}

func (c *capture) observeLifecycle(p multisync.SyncPacket, at time.Time, source string) {
	switch p.Action {
	case multisync.SyncActionOpen, multisync.SyncActionStart, multisync.SyncActionStop:
		c.lifecycleEvents = append(c.lifecycleEvents, lifecycleEvent{
			At: at, Action: p.Action, FileType: p.FileType, Filename: p.Filename,
			FrameNumber: p.FrameNumber, SecondsElapsed: p.SecondsElapsed, Source: source,
		})
	}
	if p.Action == multisync.SyncActionStop {
		c.stopBlankEvents = append(c.stopBlankEvents, stopBlankEvent{
			At: at, Kind: "STOP", FileType: p.FileType, Filename: p.Filename, Source: source,
		})
	}
}

// observeDrift updates the appropriate driftTracker for p.FileType and
// returns a driftSample (also recorded for the end-of-run summary) if a
// prior anchor for the same filename existed to compare against.
func (c *capture) observeDrift(p multisync.SyncPacket, at time.Time) *driftSampleJSON {
	tracker := &c.seqDrift
	samples := &c.seqDriftSamples
	if p.FileType == multisync.SyncFileTypeMedia {
		tracker = &c.mediaDrift
		samples = &c.medDriftSamples
	}

	masterPos := positionFromSync(p, c.stepTime)

	// Only SYNC actions produce a drift sample. OPEN's position is a
	// not-yet-playing anchor rather than a running position, and a STOP
	// packet's FrameNumber/SecondsElapsed are not reliably a meaningful
	// "position" either (a real master may send zeros there; this was
	// observed directly while exercising this tool against a synthetic
	// sender, where a zero-valued STOP produced a nonsensical multi-second
	// "delta" against an otherwise-tight drift series). RES-002 item 4 asks
	// about drift "between sync nudges" specifically, which is the SYNC
	// action's job, not START/OPEN/STOP's.
	var out *driftSampleJSON
	if p.Action == multisync.SyncActionSync && tracker.have && tracker.filename == p.Filename {
		local := tracker.estimate(at)
		delta := masterPos - local
		s := driftSample{
			At: at, FileType: p.FileType, Filename: p.Filename,
			MasterPositionMS: msFloat(masterPos), LocalFreeRunEstimateMS: msFloat(local), DeltaMS: msFloat(delta),
		}
		*samples = append(*samples, s)
		out = &driftSampleJSON{
			FileType: p.FileType.String(), Filename: p.Filename,
			MasterPositionMS: s.MasterPositionMS, LocalFreeRunEstimateMS: s.LocalFreeRunEstimateMS, DeltaMS: s.DeltaMS,
		}
	}

	// Re-anchor on OPEN/START unconditionally (a fresh session always
	// resets the free-run reference), and on any packet for a filename the
	// tracker was not already following (covers a bare SYNC starting a
	// session with no preceding OPEN/START, per RES-002's lifecycle
	// tolerance, and covers the very first packet observed).
	if p.Action == multisync.SyncActionOpen || p.Action == multisync.SyncActionStart || !tracker.have || tracker.filename != p.Filename {
		tracker.reset(at, p.Filename, masterPos)
	}

	return out
}

func classifyDecodeError(err error) string {
	switch {
	case errors.Is(err, multisync.ErrNotFPPD):
		return "not_fppd"
	case errors.Is(err, multisync.ErrMalformed):
		return "malformed"
	default:
		var unk *multisync.UnknownPacketTypeError
		if errors.As(err, &unk) {
			return "unknown_type"
		}
		return "other"
	}
}

func (c *capture) writeJSONL(rec jsonlRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		c.logger.Error("failed to marshal JSONL record", "error", err)
		return
	}
	if _, err := c.jsonlw.Write(b); err != nil {
		c.logger.Error("failed to write JSONL record", "error", err)
		return
	}
	if err := c.jsonlw.WriteByte('\n'); err != nil {
		c.logger.Error("failed to write JSONL record", "error", err)
	}
}

func (c *capture) printHumanLine(rec multisync.Received) {
	ts := rec.ReceivedAt.Format("15:04:05.000")
	src := "?"
	if rec.SrcAddr != nil {
		src = rec.SrcAddr.String()
	}

	essentials := "-"
	switch p := rec.Payload.(type) {
	case multisync.SyncPacket:
		essentials = fmt.Sprintf("%s %s frame=%d sec=%.3f file=%q", p.Action, p.FileType, p.FrameNumber, p.SecondsElapsed, p.Filename)
	case multisync.PingPacket:
		essentials = fmt.Sprintf("%s sys=%s host=%q ver=%d.%d", p.SubType, p.SystemType, p.Hostname, p.VersionMajor, p.VersionMinor)
	case multisync.BlankPacket:
		essentials = "(blank)"
	case multisync.CommandPacket:
		essentials = fmt.Sprintf("cmd=%q host=%q args=%v", p.Command, p.Host, p.Args)
	case multisync.PluginPacket:
		essentials = fmt.Sprintf("%d bytes", len(p.Raw))
	}

	typeLabel := rec.Header.Type.String()
	if rec.DecodeErr != nil {
		fmt.Printf("%s  %-21s  %-9s  %s  ERROR=%v\n", ts, src, typeLabel, essentials, rec.DecodeErr)
		return
	}
	fmt.Printf("%s  %-21s  %-9s  %s\n", ts, src, typeLabel, essentials)
}

// --- JSONL schema ---
//
// One jsonlRecord per received datagram, decoded or not. This is meant to
// be complete enough to re-derive the byte layout offline without the
// original network: RawHex is always present regardless of DecodeError, and
// every field this package's codec can produce is included, not just the
// ones convenient for the summary.

type jsonlRecord struct {
	WallClock         time.Time             `json:"wall_clock"`
	MonotonicOffsetMS int64                 `json:"monotonic_offset_ms"`
	Source            string                `json:"source,omitempty"`
	Transport         string                `json:"transport"`
	DestAddr          string                `json:"dest_addr,omitempty"`
	IfIndex           int                   `json:"if_index,omitempty"`
	RawHex            string                `json:"raw_hex"`
	HeaderType        string                `json:"header_type"`
	HeaderExtraLen    uint16                `json:"header_extra_len"`
	DecodeError       string                `json:"decode_error,omitempty"`
	DecodeErrorKind   string                `json:"decode_error_kind,omitempty"`
	Sync              *syncFieldsJSON       `json:"sync,omitempty"`
	Ping              *pingFieldsJSON       `json:"ping,omitempty"`
	Blank             bool                  `json:"blank,omitempty"`
	Command           *commandFieldsJSON    `json:"command,omitempty"`
	Plugin            *pluginFieldsJSON     `json:"plugin,omitempty"`
	Drift             *driftSampleJSON      `json:"drift,omitempty"`
	SequenceTimeline  *timelineSnapshotJSON `json:"sequence_timeline,omitempty"`
	MediaTimeline     *timelineSnapshotJSON `json:"media_timeline,omitempty"`
}

type syncFieldsJSON struct {
	Action         string  `json:"action"`
	FileType       string  `json:"file_type"`
	FrameNumber    uint32  `json:"frame_number"`
	SecondsElapsed float32 `json:"seconds_elapsed"`
	Filename       string  `json:"filename"`
}

type pingFieldsJSON struct {
	Version       uint8  `json:"version"`
	SubType       string `json:"sub_type"`
	SystemType    string `json:"system_type"`
	VersionMajor  uint16 `json:"version_major"`
	VersionMinor  uint16 `json:"version_minor"`
	Mode          string `json:"mode"`
	IP            string `json:"ip"`
	Hostname      string `json:"hostname"`
	VersionString string `json:"version_string"`
	HardwareType  string `json:"hardware_type"`
	Ranges        string `json:"ranges"`
}

type commandFieldsJSON struct {
	Host    string   `json:"host"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type pluginFieldsJSON struct {
	RawHex string `json:"raw_hex"`
}

type driftSampleJSON struct {
	FileType               string  `json:"file_type"`
	Filename               string  `json:"filename"`
	MasterPositionMS       float64 `json:"master_position_ms"`
	LocalFreeRunEstimateMS float64 `json:"local_free_run_estimate_ms"`
	DeltaMS                float64 `json:"delta_ms"`
}

type timelineSnapshotJSON struct {
	State           string     `json:"state"`
	Filename        string     `json:"filename"`
	FileType        string     `json:"file_type"`
	PositionMS      int64      `json:"position_ms"`
	LastSyncAtValid bool       `json:"last_sync_at_valid"`
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastCorrection  string     `json:"last_correction"`
	SlewCount       int        `json:"slew_count"`
	SkipCount       int        `json:"skip_count"`
	JumpCount       int        `json:"jump_count"`
	Source          string     `json:"source,omitempty"`
	Conflict        bool       `json:"conflict,omitempty"`
	ConflictCount   int        `json:"conflict_count,omitempty"`
}

func toTimelineSnapshotJSON(s multisync.Snapshot) *timelineSnapshotJSON {
	out := &timelineSnapshotJSON{
		State:           string(s.State),
		Filename:        s.Filename,
		FileType:        s.FileType.String(),
		PositionMS:      s.PositionMS,
		LastSyncAtValid: s.LastSyncAtValid,
		LastCorrection:  string(s.LastCorrection),
		SlewCount:       s.SlewCount,
		SkipCount:       s.SkipCount,
		JumpCount:       s.JumpCount,
		Source:          s.Source,
		Conflict:        s.Conflict,
		ConflictCount:   s.ConflictCount,
	}
	if s.LastSyncAtValid {
		t := s.LastSyncAt
		out.LastSyncAt = &t
	}
	return out
}

// --- summary report, organized explicitly by RES-002's five open bench
// items so this output can be judged directly against them ---

func (c *capture) printSummary(w *summaryWriter, l *multisync.Listener, outPath string, stepTime time.Duration, cfg runConfig) {
	stats := l.Stats()
	wallDuration := time.Since(c.startedAt)

	w.Println("=== ShowMesh MultiSync Probe: capture summary ===")
	w.Println()
	w.Printf("Run duration:        %s\n", wallDuration.Round(time.Millisecond))
	if c.packetCount > 0 {
		w.Printf("First/last packet:   %s -> %s (span %s)\n",
			c.firstPacketAt.Format(time.RFC3339Nano), c.lastPacketAt.Format(time.RFC3339Nano), c.lastPacketAt.Sub(c.firstPacketAt))
	}
	w.Printf("Output file:         %s\n", outPath)
	w.Printf("Assumed step time:   %s (NOT protocol-verified; only affects FrameNumber-derived position, see -step-ms)\n", stepTime)
	w.Printf("Discover responder:  %v\n", cfg.respond)
	w.Printf("Datagrams received:  %d  (decoded_ok=%d not_fppd=%d malformed=%d unknown_type=%d)\n",
		stats.PacketsReceived, stats.DecodedOK, stats.NotFPPD, stats.Malformed, stats.UnknownType)
	w.Println()
	w.Println("This is evidence from a SINGLE capture run. It establishes only what this")
	w.Println("run observed, on this network, at this time. It does not by itself confirm")
	w.Println("behavior across FPP versions, network modes, or impaired conditions (delay,")
	w.Println("loss, duplication, reordering, competing masters) that RES-002's full test")
	w.Println("method calls for. Moving RES-002 past L1 requires a human reviewing this")
	w.Println("evidence against what was actually done on the FPP side during the capture.")

	if c.packetCount == 0 {
		w.Println()
		w.Println("*** NO PACKETS WERE RECEIVED DURING THIS CAPTURE. ***")
		w.Println("This is itself a finding, not necessarily an error.")
		w.Println()
		w.Println("MOST LIKELY EXPECTED CAUSE, if the target is FPP 10: a fresh FPP 10 install")
		w.Println("ships with MultiSyncUnicast defaulting to on and MultiSyncMulticast carrying")
		w.Println("no default at all (RES-002; upstream www/settings.json at the 10.0 tag), and")
		w.Println("FPP 10's automatic unicast targeting only ever selects other FPP instances in")
		w.Println("remote mode (supportsUnicast in src/MultiSync.cpp) -- never a third-party")
		w.Println("listener such as this probe. A fresh FPP 10 player left at its shipped")
		w.Println("defaults will therefore send this probe nothing, on ANY transport, with no")
		w.Println("error logged on either side. THIS IS FPP 10 CONFIGURED THE WAY FPP 10 SHIPS,")
		w.Println("not a broken listener and not a broken network. It is not distinguishable from")
		w.Println("a genuine fault by packet evidence alone: confirm which case this is by adding")
		w.Println("this probe's/ShowMesh's address to MultiSyncRemotes (or MultiSyncExtraRemotes)")
		w.Println("under Settings -> MultiSync on the FPP 10 player -- this applies live, with no")
		w.Println("fppd restart, on FPP 10 -- and re-running the capture. If packets then arrive,")
		w.Println("this was the expected FPP 10 default, not a fault. See RES-002 and")
		w.Println("docs/bench/RES-002-capture-procedure.md for the full operator procedure.")
		w.Println()
		w.Println("Other possible (and still unruled-out) causes: FPP is not running or not")
		w.Println("playing anything; MultiSyncEnabled is off; a firewall is dropping UDP 32320;")
		w.Println("this host is on a different L2 segment or VLAN with no route for the")
		w.Println("configured transport; or -iface/-listen point somewhere traffic does not")
		w.Println("arrive. None of RES-002's five open items are answered by this run.")
	}

	c.printItem1(w)
	c.printItem2(w)
	c.printItem3(w)
	c.printItem4(w, wallDuration)
	c.printItem5(w, l, stats)

	w.Println()
	w.Println("=== end of summary ===")
}

func (c *capture) printItem1(w *summaryWriter) {
	w.Println()
	w.Println("--- RES-002 open item 1: cadence and jitter under load ---")
	w.Println("Statistics below are computed over the inter-arrival time between")
	w.Println("consecutive SyncAction=Sync packets only, per file type (sequence, media).")
	w.Println("OPEN, START, and STOP packets are excluded: they mark lifecycle")
	w.Println("transitions, not the periodic sync cadence this item asks about, and")
	w.Println("including them would inject a near-zero OPEN-to-START gap and a")
	w.Println("multi-second STOP-to-next-OPEN gap per playlist entry into the series.")
	printIntervalStats(w, "Sequence sync", c.seqIntervals)
	printIntervalStats(w, "Media sync", c.mediaIntervals)
	w.Println("Missing from this item: cadence/jitter specifically \"under load\" (RES-002's")
	w.Println("own phrasing) requires a capture taken while the reference show's full pixel")
	w.Println("and matrix output is running, not just an idle or lightly loaded FPP; this")
	w.Println("tool cannot tell whether that condition held during this run.")
}

func printIntervalStats(w *summaryWriter, label string, intervals []time.Duration) {
	if len(intervals) == 0 {
		w.Printf("%s: 0 inter-arrival samples (need at least 2 packets of this kind; none, or only one, observed)\n", label)
		return
	}
	minD, medD, p95D, maxD := durationStats(intervals)
	w.Printf("%s: n=%d inter-arrival min=%s median=%s p95=%s max=%s\n",
		label, len(intervals), minD, medD, p95D, maxD)
}

func durationStats(d []time.Duration) (minD, medD, p95D, maxD time.Duration) {
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	minD = sorted[0]
	maxD = sorted[len(sorted)-1]
	medD = percentileDuration(sorted, 0.50)
	p95D = percentileDuration(sorted, 0.95)
	return
}

func percentileDuration(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (c *capture) printItem2(w *summaryWriter) {
	w.Println()
	w.Println("--- RES-002 open item 2: pause/seek behavior, and whether OPEN precedes START ---")
	if len(c.lifecycleEvents) == 0 {
		w.Println("No OPEN, START, or STOP packets were observed in this capture. This item is unanswered.")
	} else {
		w.Printf("Full ordered lifecycle (OPEN/START/STOP only; SYNC cadence is covered under item 1), %d events:\n", len(c.lifecycleEvents))
		for _, e := range c.lifecycleEvents {
			w.Printf("  %s  %-5s %-8s file=%q frame=%d sec=%.3f source=%s\n",
				e.At.Format("15:04:05.000"), e.Action, e.FileType, e.Filename, e.FrameNumber, e.SecondsElapsed, e.Source)
		}
		w.Println("Per-file OPEN-before-START check (first OPEN/START seen per filename in this capture):")
		for _, line := range analyzeOpenBeforeStart(c.lifecycleEvents) {
			w.Println(line)
		}
	}
	w.Println("Missing from this item: pause and seek specifically. Nothing in the MultiSync")
	w.Println("wire protocol (per RES-002) has a distinct pause or seek packet; whatever FPP")
	w.Println("actually emits for those operator actions has to be read out of the sequence")
	w.Println("of OPEN/START/SYNC/STOP above by a human who knows they triggered a pause or")
	w.Println("seek at a specific time, and cross-referenced against the JSONL. This tool")
	w.Println("does not infer \"this was a pause\" or \"this was a seek\" on its own. Whether OPEN")
	w.Println("reliably precedes START across xSchedule and multiple FPP major versions also")
	w.Println("cannot be established from one capture against one master.")
}

func analyzeOpenBeforeStart(events []lifecycleEvent) []string {
	type fileState struct {
		filename string
		fileType multisync.SyncFileType
		hasOpen  bool
		openAt   time.Time
		hasStart bool
		startAt  time.Time
	}
	order := make([]string, 0)
	seen := make(map[string]*fileState)

	for _, e := range events {
		fs, ok := seen[e.Filename]
		if !ok {
			fs = &fileState{filename: e.Filename, fileType: e.FileType}
			seen[e.Filename] = fs
			order = append(order, e.Filename)
		}
		switch e.Action {
		case multisync.SyncActionOpen:
			if !fs.hasOpen {
				fs.hasOpen, fs.openAt = true, e.At
			}
		case multisync.SyncActionStart:
			if !fs.hasStart {
				fs.hasStart, fs.startAt = true, e.At
			}
		}
	}

	lines := make([]string, 0, len(order))
	for _, fn := range order {
		fs := seen[fn]
		switch {
		case fs.hasOpen && fs.hasStart && !fs.openAt.After(fs.startAt):
			lines = append(lines, fmt.Sprintf("  %q: OPEN observed %s before START", fn, fs.startAt.Sub(fs.openAt)))
		case fs.hasOpen && fs.hasStart:
			lines = append(lines, fmt.Sprintf("  %q: START observed %s BEFORE OPEN in this capture", fn, fs.openAt.Sub(fs.startAt)))
		case fs.hasStart && !fs.hasOpen:
			lines = append(lines, fmt.Sprintf("  %q: START observed with no OPEN seen in this capture (either no OPEN was sent, or it happened before this capture's window began; inconclusive from this run alone)", fn))
		case fs.hasOpen && !fs.hasStart:
			lines = append(lines, fmt.Sprintf("  %q: OPEN observed but no START seen before the capture ended", fn))
		}
	}
	return lines
}

func (c *capture) printItem3(w *summaryWriter) {
	w.Println()
	w.Println("--- RES-002 open item 3: STOP/BLANK at playlist end vs manual stop vs fppd shutdown ---")
	if len(c.stopBlankEvents) == 0 {
		w.Println("No STOP or BLANK packets were observed in this capture. This item is unanswered.")
	} else {
		w.Printf("Every STOP and BLANK observed, %d events:\n", len(c.stopBlankEvents))
		var prev time.Time
		for _, e := range c.stopBlankEvents {
			gap := "-"
			if !prev.IsZero() {
				gap = e.At.Sub(prev).String()
			}
			if e.Kind == "STOP" {
				w.Printf("  %s  %-5s file=%q source=%s  (gap since previous STOP/BLANK: %s)\n",
					e.At.Format("15:04:05.000"), e.Kind, e.Filename, e.Source, gap)
			} else {
				w.Printf("  %s  %-5s  (gap since previous STOP/BLANK: %s)\n",
					e.At.Format("15:04:05.000"), e.Kind, gap)
			}
			prev = e.At
		}
	}
	w.Println("Missing from this item: this tool cannot tell which real-world event (playlist")
	w.Println("end, a manual stop, or fppd shutdown) produced any given STOP/BLANK above, or")
	w.Println("detect an orphaned no-STOP case (a master going away with no STOP at all looks")
	w.Println("identical to \"nothing happened\" from this evidence alone) without a human")
	w.Println("correlating these timestamps against what they did, per")
	w.Println("docs/bench/RES-002-capture-procedure.md.")
}

func (c *capture) printItem4(w *summaryWriter, wallDuration time.Duration) {
	w.Println()
	w.Println("--- RES-002 open item 4: clock-drift accumulation over a 30-60 minute show ---")
	printDriftSummary(w, "Sequence", c.seqDriftSamples)
	printDriftSummary(w, "Media", c.medDriftSamples)
	if wallDuration < 25*time.Minute {
		w.Printf("This capture ran %s, well short of the 30-60 minute window this item asks\n", wallDuration.Round(time.Second))
		w.Println("for. Treat the series above as a preliminary sample of drift behavior, not")
		w.Println("the accumulation-over-a-full-show answer this item needs; re-run with -duration")
		w.Println("set to 30m or more (or no -duration, stopped manually after a full show).")
	}
}

func printDriftSummary(w *summaryWriter, label string, samples []driftSample) {
	if len(samples) == 0 {
		w.Printf("%s: 0 drift samples (need at least two sync packets for the same file to compare a free-run estimate against)\n", label)
		return
	}
	first, last := samples[0], samples[len(samples)-1]
	minDelta, maxDelta, sum := samples[0].DeltaMS, samples[0].DeltaMS, 0.0
	for _, s := range samples {
		if s.DeltaMS < minDelta {
			minDelta = s.DeltaMS
		}
		if s.DeltaMS > maxDelta {
			maxDelta = s.DeltaMS
		}
		sum += s.DeltaMS
	}
	mean := sum / float64(len(samples))
	w.Printf("%s: n=%d delta_ms first=%.1f last=%.1f min=%.1f max=%.1f mean=%.1f (delta = master position - this tool's uncorrected free-run estimate; Timeline's own applied corrections are separate, see slew/skip/jump counts in the JSONL timeline snapshots)\n",
		label, len(samples), first.DeltaMS, last.DeltaMS, minDelta, maxDelta, mean)
}

func (c *capture) printItem5(w *summaryWriter, l *multisync.Listener, stats multisync.Stats) {
	w.Println()
	w.Println("--- RES-002 open item 5: multicast IGMP behavior and discover-ping participation ---")
	w.Println("Multicast group join attempts:")
	printJoinResults(w, l.JoinResults())

	w.Println("Transports that actually delivered a packet during this capture:")
	if len(c.transportCounts) == 0 {
		w.Println("  (none; no packets were received)")
	} else {
		for _, t := range []multisync.Transport{multisync.TransportMulticast, multisync.TransportBroadcast, multisync.TransportUnicast, multisync.TransportUnknown} {
			if n := c.transportCounts[t]; n > 0 {
				w.Printf("  %-10s %d packet(s)\n", t, n)
			}
		}
	}

	if len(c.ifaceHits) > 0 {
		w.Println("Interfaces packets were observed arriving on (by OS interface index, where the platform reports it):")
		indices := make([]int, 0, len(c.ifaceHits))
		for idx := range c.ifaceHits {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			w.Printf("  ifindex=%d: %d packet(s)\n", idx, c.ifaceHits[idx])
		}
	} else if stats.PacketsReceived > 0 {
		w.Println("Interface-of-arrival was not determinable for any received packet on this")
		w.Println("platform/kernel (destination control messages unavailable); see the warning")
		w.Println("logged at startup if one was printed.")
	}

	if len(c.sourceAddrs) > 0 {
		w.Printf("Distinct source addresses seen: %d\n", len(c.sourceAddrs))
	}

	w.Println("Missing from this item: actual IGMP join/leave/query behavior on the reference")
	w.Println("switch (snooping, querier presence, group timeout) is not observable from this")
	w.Println("host's socket state at all; it requires switch-side evidence (port mirroring,")
	w.Println("switch CLI/SNMP, or a separate capture on the switch) that this tool does not")
	w.Println("collect. Whether this probe answering a discover ping actually causes it to")
	w.Println("appear in FPP's own MultiSync UI also has to be confirmed by looking at that")
	w.Println("UI during a run with -respond-discover enabled; this tool only reports whether")
	w.Println("it sent a response, not whether FPP displayed it.")
}
