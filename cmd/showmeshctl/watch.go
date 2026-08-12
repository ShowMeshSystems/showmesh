package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// cmdWatch is the entry point wired to os.Stdin/Stdout/Stderr and real
// OS signals; runWatch below is the testable core that takes an explicit
// context so a test can cancel it deterministically instead of racing a
// real Ctrl+C.
func cmdWatch(args []string, stdout, stderr io.Writer, clock func() time.Time) int {
	fs, g := newFlagSet("showmeshctl watch", stderr)
	var deltas bool
	fs.BoolVar(&deltas, "deltas", false, "opt into observation-level delta frames (ADR-023): GET /api/v1/stream?deltas=1")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: showmeshctl watch [flags]")
		_, _ = fmt.Fprintln(stderr, "\nFetch the authoritative snapshot, then stream live changes from")
		_, _ = fmt.Fprintln(stderr, "GET /api/v1/stream until interrupted (Ctrl+C). Per OPERATOR-UI §6,")
		_, _ = fmt.Fprintln(stderr, "this never resumes from its local model: it refetches the snapshot")
		_, _ = fmt.Fprintln(stderr, "on every connect, on every reconnect, on stream.reset, and on any")
		_, _ = fmt.Fprintln(stderr, "detected sequence gap. --timeout applies to the snapshot request and")
		_, _ = fmt.Fprintln(stderr, "reconnect attempts, not to the open stream itself, which is long-lived")
		_, _ = fmt.Fprintln(stderr, "by design.")
		_, _ = fmt.Fprintln(stderr, "\nWith --deltas, this connection also receives fpp.observations.changed")
		_, _ = fmt.Fprintln(stderr, "frames (ADR-023): an FPP instance's observation-level changes arrive")
		_, _ = fmt.Fprintln(stderr, "as just the signals that moved, rather than repeating every one of that")
		_, _ = fmt.Fprintln(stderr, "instance's observations inside fpp.changed. Without it (the default),")
		_, _ = fmt.Fprintln(stderr, "this command behaves exactly as it always has: fpp.observations.changed")
		_, _ = fmt.Fprintln(stderr, "never appears, and this is what proves the flag is additive rather than")
		_, _ = fmt.Fprintln(stderr, "a silent behavior change for a script that never passes it.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if err := validateOutput(g); err != nil {
		return reportError(stderr, "watch", err)
	}
	if extra := fs.Args(); len(extra) > 0 {
		fs.Usage()
		return exitUsage
	}

	// The stream connection itself must not carry the --timeout deadline:
	// http.Client.Timeout bounds the entire request including reading the
	// (deliberately unbounded) response body, which would kill a healthy,
	// idle stream. --timeout still governs the snapshot refetches, via a
	// separate client.
	streamClient, err := newClient(g.server, g.token, &http.Client{})
	if err != nil {
		return reportError(stderr, "watch", err)
	}
	snapshotClient, err := newClient(g.server, g.token, &http.Client{Timeout: g.timeout})
	if err != nil {
		return reportError(stderr, "watch", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runWatch(ctx, streamClient, snapshotClient, g, deltas, stdout, stderr, clock)
}

// watchBackoff is bounded exponential backoff for the reconnect loop. The
// numbers below (1s initial, 30s ceiling, x2 factor) are a ShowMesh-chosen
// guess with no measurement behind them, exactly like the SSE keepalive
// interval contract §6.4 requires be labelled — there is no bench evidence
// for what a "well-behaved reconnect" looks like against a real
// coordinator yet.
type watchBackoff struct {
	initial time.Duration
	max     time.Duration
	current time.Duration
}

func newWatchBackoff() *watchBackoff {
	return &watchBackoff{initial: time.Second, max: 30 * time.Second}
}

func (b *watchBackoff) Reset() { b.current = 0 }

func (b *watchBackoff) Next() time.Duration {
	if b.current == 0 {
		b.current = b.initial
	} else {
		b.current *= 2
		if b.current > b.max {
			b.current = b.max
		}
	}
	return b.current
}

// runWatch is the reconnect loop: connect, stream until the connection
// drops or resets unrecoverably, back off, reconnect. It returns exitOK
// when ctx is cancelled (the normal, expected shutdown path for an
// interactive `watch`), and a non-zero code for a failure this program
// judges not worth retrying (unauthorized, version-incompatible). deltas
// is fixed for the whole run (from --deltas) and is applied to every
// (re)connection attempt identically — a client does not flip between
// delta and full-frame mid-session.
func runWatch(ctx context.Context, streamClient, snapshotClient *client, g *globalFlags, deltas bool, stdout, stderr io.Writer, clock func() time.Time) int {
	bo := newWatchBackoff()
	for {
		if ctx.Err() != nil {
			return exitOK
		}

		err := watchOnce(ctx, streamClient, snapshotClient, g, deltas, stdout, stderr, clock, bo)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return exitOK
		}

		var ce *cliError
		if errors.As(err, &ce) && (ce.code == exitUnauthorized || ce.code == exitForbidden || ce.code == exitVersionIncompatible) {
			// Reconnecting will not fix a bad token, a principal missing a
			// required scope (ADR-024 decision 4 — a 403 here means "this
			// principal does not hold node:read/fpp:read/etc.", not a
			// transient condition retrying will clear), or a coordinator
			// that will never advertise a version this CLI supports. Task
			// spec §3: "version skew is reported, not guessed at."
			return reportError(stderr, "watch", err)
		}
		_, _ = fmt.Fprintf(stderr, "showmeshctl watch: %v; reconnecting\n", err)

		d := bo.Next()
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(d):
		}
	}
}

// watchOnce owns one stream connection end to end: connect, verify
// stream.start, fetch the snapshot, then apply deltas until the
// connection ends. A returned error always means the connection is gone
// (or unusable) and the caller should back off and retry, except for the
// two exit-worthy classes runWatch itself checks for.
//
// deltas, when true, requests observation-level delta frames per ADR-023
// by adding EXACTLY the query value the coordinator's own contract
// requires — "deltas=1" — never a looser truthy spelling: the coordinator
// side treats anything else as equivalent to omitting the parameter (see
// internal/coordinator/api's Hub.ServeHTTP doc comment), so this client
// side must send precisely what actually opts in.
func watchOnce(ctx context.Context, streamClient, snapshotClient *client, g *globalFlags, deltas bool, stdout, stderr io.Writer, clock func() time.Time, bo *watchBackoff) error {
	var query url.Values
	if deltas {
		query = url.Values{"deltas": {"1"}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamClient.endpoint("/api/v1/stream", query), nil)
	if err != nil {
		return newCLIError(exitUsage, "building stream request: %v", err)
	}
	streamClient.applyHeaders(req)
	req.Header.Set("Accept", "text/event-stream")
	// Deliberately never set Last-Event-ID: contract §6.4 says the server
	// ignores it regardless, and this client has no cursor to resume from
	// in the first place — see OPERATOR-UI §6.

	resp, err := streamClient.httpClient.Do(req)
	if err != nil {
		return classifyRequestError(streamClient.baseURL.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return decodeProblemError(resp, body)
	}
	if err := streamClient.checkAPIVersionHeader(resp); err != nil {
		return err
	}

	sr := newSSEReader(resp.Body)

	frame, err := sr.next()
	if err != nil {
		return fmt.Errorf("reading stream.start: %w", err)
	}
	if frame.event != "stream.start" {
		return newCLIError(exitAPIError, "expected the first stream event to be stream.start, got %q", frame.event)
	}
	var start streamStart
	if err := json.Unmarshal([]byte(frame.data), &start); err != nil {
		return newCLIError(exitAPIError, "decoding stream.start: %v", err)
	}
	_, _ = fmt.Fprintf(stdout, "--- connected: streamId=%s apiVersion=%d ---\n", start.StreamID, start.APIVersion)

	if err := refetchSnapshot(ctx, snapshotClient, g, stdout, stderr, clock, "initial connect"); err != nil {
		return err
	}
	// The connection is up and we have a verified snapshot under it: this
	// connection attempt succeeded, so the next disconnect should retry
	// promptly rather than inheriting whatever backoff got us here.
	bo.Reset()

	var seqState seqTracker

	for {
		frame, err := sr.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("stream closed by coordinator")
			}
			return fmt.Errorf("reading stream: %w", err)
		}

		switch frame.event {
		case "node.changed":
			var ev streamNodeChanged
			if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
				_, _ = fmt.Fprintf(stderr, "showmeshctl watch: decoding node.changed: %v\n", err)
				continue
			}
			if seqState.observe(ev.Seq) {
				if err := onSeqGap(ctx, snapshotClient, g, stdout, stderr, clock); err != nil {
					return err
				}
				continue
			}
			printStreamEvent(stdout, g, "node.changed", ev, func() {
				_, _ = fmt.Fprintf(stdout, "[node.changed] %s %s heartbeat=%s\n",
					ev.Node.NodeID, controlPlaneColumn(ev.Node.ControlPlane), evidenceColumn(ev.Node.Evidence.Heartbeat, ev.ServerTime))
			})
		case "fpp.changed":
			var ev streamFPPChanged
			if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
				_, _ = fmt.Fprintf(stderr, "showmeshctl watch: decoding fpp.changed: %v\n", err)
				continue
			}
			if seqState.observe(ev.Seq) {
				if err := onSeqGap(ctx, snapshotClient, g, stdout, stderr, clock); err != nil {
					return err
				}
				continue
			}
			printStreamEvent(stdout, g, "fpp.changed", ev, func() {
				_, _ = fmt.Fprintf(stdout, "[fpp.changed] %s health=%s\n", ev.Instance.InstanceID, healthGlyph(ev.Instance.Health))
			})
		case "fpp.observations.changed":
			// Only ever arrives on a connection opened with --deltas
			// (ADR-023); reached here regardless of that flag, in the same
			// additive-tolerant spirit as the "default" branch below, since
			// this program's own request is what actually controls whether
			// the coordinator ever sends one.
			var ev streamFPPObservationsChanged
			if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
				_, _ = fmt.Fprintf(stderr, "showmeshctl watch: decoding fpp.observations.changed: %v\n", err)
				continue
			}
			if seqState.observe(ev.Seq) {
				if err := onSeqGap(ctx, snapshotClient, g, stdout, stderr, clock); err != nil {
					return err
				}
				continue
			}
			printStreamEvent(stdout, g, "fpp.observations.changed", ev, func() {
				printFPPObservationsChangedLine(stdout, ev)
			})
		case "event.recorded":
			var ev streamEventRecorded
			if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
				_, _ = fmt.Fprintf(stderr, "showmeshctl watch: decoding event.recorded: %v\n", err)
				continue
			}
			if seqState.observe(ev.Seq) {
				if err := onSeqGap(ctx, snapshotClient, g, stdout, stderr, clock); err != nil {
					return err
				}
				continue
			}
			printStreamEvent(stdout, g, "event.recorded", ev, func() {
				_, _ = fmt.Fprintf(stdout, "[event.recorded] seq=%d %s %s: %s\n",
					ev.Event.Seq, severityGlyph(ev.Event.Severity), ev.Event.Category, ev.Event.Summary)
			})
		case "stream.reset":
			var rst streamReset
			if err := json.Unmarshal([]byte(frame.data), &rst); err != nil {
				_, _ = fmt.Fprintf(stderr, "showmeshctl watch: decoding stream.reset: %v\n", err)
				continue
			}
			seqState.observe(rst.Seq) // rearm from the reset's own seq regardless of gap-ness
			_, _ = fmt.Fprintf(stderr, "showmeshctl watch: stream reset (reason=%s); refetching snapshot\n", rst.Reason)
			if err := refetchSnapshot(ctx, snapshotClient, g, stdout, stderr, clock, "stream.reset"); err != nil {
				return err
			}
		default:
			// An event type this build predates (contract §6.2 is
			// additive-only on the wire, and §6.4 defines "no type you do
			// not produce" as a constraint on the server, not a promise
			// to this client about the future). Ignore, don't crash.
			_, _ = fmt.Fprintf(stderr, "showmeshctl watch: ignoring unrecognized event type %q\n", frame.event)
		}
	}
}

// printStreamEvent renders one *.changed/event.recorded frame either as
// JSON (the decoded struct) or via the given text renderer, depending on
// --output.
func printStreamEvent(stdout io.Writer, g *globalFlags, _ string, payload any, textRender func()) {
	if g.output == outputJSON {
		_ = printJSON(stdout, payload)
		return
	}
	textRender()
}

// seqTracker implements the gap detection task spec §3 requires: "If it
// sees a seq gap it treats it exactly like a reset." seq is per-connection
// (contract §6.4) and starts at 1 for the first event after stream.start,
// so the tracker arms itself on the first observed seq rather than
// assuming 1, and only starts checking for gaps from the second event
// onward.
type seqTracker struct {
	armed    bool
	expected uint64
}

// observe records seq and reports whether it was a gap (true) relative to
// what was expected. It always rearms from seq going forward, whether or
// not this call reported a gap — matching "treats it exactly like a
// reset": after a reset there is nothing left to compare against but
// whatever arrives next.
func (t *seqTracker) observe(seq uint64) (gap bool) {
	gap = t.armed && seq != t.expected
	t.armed = true
	t.expected = seq + 1
	return gap
}

// onSeqGap is the seq-gap branch of watchOnce's per-event-type cases: log
// it, then refetch the snapshot exactly as stream.reset does. A gap is
// reported to stderr, not silently absorbed, and refetching (rather than
// applying the event that revealed the gap) is deliberate: the snapshot
// is a strictly newer, authoritative view than any one delta could be.
func onSeqGap(ctx context.Context, snapshotClient *client, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time) error {
	_, _ = fmt.Fprintln(stderr, "showmeshctl watch: sequence gap detected; treating as a reset and refetching snapshot")
	return refetchSnapshot(ctx, snapshotClient, g, stdout, stderr, clock, "sequence gap")
}

// refetchSnapshot is the one place this program calls GET /api/v1/snapshot
// from watch, used on initial connect, on stream.reset, and on a detected
// seq gap — the three cases task spec §3 names explicitly. It never reads
// from any local model to decide what to fetch: the request is always the
// same, unconditional GET.
func refetchSnapshot(ctx context.Context, c *client, g *globalFlags, stdout, stderr io.Writer, clock func() time.Time, reason string) error {
	reqCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	var snap snapshot
	if err := c.getJSON(reqCtx, "/api/v1/snapshot", nil, &snap); err != nil {
		return err
	}
	printClockSkew(stderr, snap.ServerTime, clock())

	_, _ = fmt.Fprintf(stdout, "--- snapshot (%s): %d node(s), %d FPP instance(s), latestEventSeq=%d ---\n",
		reason, len(snap.Nodes), len(snap.FPP.Instances), snap.LatestEventSeq)

	if g.output == outputJSON {
		return printJSON(stdout, snap)
	}
	printNodesTable(stdout, nodesResponse{ServerTime: snap.ServerTime, Nodes: snap.Nodes})
	printFPPTable(stdout, fppResponse{ServerTime: snap.ServerTime, Instances: snap.FPP.Instances})
	return nil
}

// --- Server-Sent Events framing ---
//
// showmeshctl parses SSE by hand (net/http for the transport, this file
// for the wire format) rather than a library: contract §6.4 chose SSE
// partly so it would need no client library beyond fetch/ReadableStream
// on the browser side, and the equivalent on this side is bufio, not a
// dependency. See main task report re: dependency posture.

// sseFrame is one dispatched SSE event: an event type and its (possibly
// multi-line, here always single-line per contract §6.4) data payload.
type sseFrame struct {
	event string
	data  string
}

// sseReader reads dispatched SSE frames from a stream, one per next()
// call. It deliberately does not look at any "id:" line — see contract
// §6.4's "no id: field is ever emitted, and any Last-Event-ID request
// header is ignored" and the same rule applied from this side in
// cmdWatch/watchOnce.
type sseReader struct {
	scanner *bufio.Scanner
}

// newSSEReader wraps r with a scanner large enough for a full node or FPP
// instance representation on one data: line; the default 64KiB
// bufio.Scanner token limit is a real risk for a node with many
// capabilities, so this raises it to 4MiB. That ceiling is a ShowMesh
// guess, not a measured bound on payload size.
func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &sseReader{scanner: sc}
}

// next reads lines until one dispatched event (terminated by a blank
// line, per the SSE spec) has accumulated, and returns it. It returns
// io.EOF when the underlying stream ends cleanly with no event pending,
// and the scanner's error otherwise.
func (r *sseReader) next() (sseFrame, error) {
	var f sseFrame
	var dataLines []string
	sawField := false

	for r.scanner.Scan() {
		line := r.scanner.Text()

		if line == "" {
			if sawField {
				f.data = strings.Join(dataLines, "\n")
				return f, nil
			}
			continue // a blank line before any field starts is not a dispatch
		}
		if strings.HasPrefix(line, ":") {
			continue // comment line, e.g. ": keepalive" (contract §6.4)
		}

		sawField = true
		switch {
		case strings.HasPrefix(line, "event:"):
			f.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// Everything else this reader does not act on: an "id:" line
			// (ignored on purpose — see the type doc comment: no
			// Last-Event-ID resumption, ever) or any other SSE field this
			// client does not recognize (additive tolerance, same as an
			// unknown JSON field).
		}
	}

	if err := r.scanner.Err(); err != nil {
		return f, err
	}
	if sawField {
		f.data = strings.Join(dataLines, "\n")
		return f, nil
	}
	return f, io.EOF
}
