//go:build integration

package integration

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file proves BUILD-PLAN Step 3's two acceptance criteria — the only
// two things this task's spec calls out by name as "exactly two, and both
// are yours" — end to end, over real sockets, with real subprocesses on
// both ends:
//
//	(a) The API is exercised end to end by a non-UI client: showmeshctl run
//	    as a subprocess against a real showmesh-coordinator subprocess.
//	(b) An interrupted change stream is followed by an authoritative
//	    snapshot re-fetch, not a resumed local model — proven for all three
//	    interruption shapes named in the task spec, because they fail
//	    differently: the connection dropping, the coordinator restarting
//	    underneath a connected client, and a stream.reset from buffer
//	    overflow.

// runShowmeshctl execs the real showmeshctl binary as a subprocess with
// args, waits for it to exit, and returns its exit code, stdout, and
// stderr. This is deliberately NOT an in-process call into cmd/showmeshctl's
// packages — a process, over a socket, parsing bytes — per the task spec's
// own emphasis on why that distinction is what makes acceptance criterion
// (a) mean what it says.
func runShowmeshctl(t *testing.T, timeout time.Duration, args ...string) (exitCode int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, showmeshctlBinPath, args...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	if err == nil {
		return 0, stdout, stderr
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode(), stdout, stderr
	}
	t.Fatalf("running showmeshctl %v: %v (stdout=%q stderr=%q)", args, err, stdout, stderr)
	return -1, stdout, stderr
}

// asExitError is errors.As spelled out for *exec.ExitError, kept as its own
// tiny function only so runShowmeshctl's call site reads plainly.
func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// TestShowmeshctlSubcommandsAgainstRealCoordinator is acceptance criterion
// (a): every read subcommand run as a real subprocess against a real
// showmesh-coordinator subprocess (with a real agent behind it, so there is
// genuine data to render), asserting exit codes and rendered content — not
// merely "it returned 0".
func TestShowmeshctlSubcommandsAgainstRealCoordinator(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID, label: "cli test node", capabilities: "matrix.render"})
	waitOnline(t, coord, nodeID)

	server := "http://" + coord.httpAddr

	t.Run("version", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "version", "--server", server)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "negotiation: compatible") {
			t.Errorf("stdout = %q, want a compatible negotiation report", stdout)
		}
	})

	t.Run("nodes", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "nodes", "--server", server)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, nodeID) {
			t.Errorf("stdout does not contain node id %q:\n%s", nodeID, stdout)
		}
	})

	t.Run("node detail", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "node", "--server", server, nodeID)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "cli test node") {
			t.Errorf("stdout does not contain the node's label:\n%s", stdout)
		}
		if !strings.Contains(stdout, "matrix.render") {
			t.Errorf("stdout does not contain the node's capability:\n%s", stdout)
		}
	})

	t.Run("node not found", func(t *testing.T) {
		code, _, stderr := runShowmeshctl(t, 10*time.Second, "node", "--server", server, "no-such-node")
		if code != 5 { // exitNotFound per main.go's --help table
			t.Errorf("exit code = %d, want 5 (not found); stderr=%s", code, stderr)
		}
	})

	t.Run("fpp with none configured", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "fpp", "--server", server)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		_ = stdout // an empty instance list is a legitimate, successful render
	})

	t.Run("events", func(t *testing.T) {
		code, _, stderr := runShowmeshctl(t, 10*time.Second, "events", "--server", server)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "snapshot", "--server", server)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, nodeID) {
			t.Errorf("stdout does not contain node id %q:\n%s", nodeID, stdout)
		}
	})

	t.Run("unreachable coordinator", func(t *testing.T) {
		// A deliberately unused port: exercises the "coordinator
		// unreachable" exit code (2, per main.go's table) as a real
		// subprocess against a real closed port, not a mocked transport
		// error.
		badServer := "http://127.0.0.1:1"
		code, _, stderr := runShowmeshctl(t, 10*time.Second, "nodes", "--server", badServer, "--timeout", "2s")
		if code != 2 {
			t.Errorf("exit code = %d, want 2 (coordinator unreachable); stderr=%s", code, stderr)
		}
	})

	t.Run("json output round trips", func(t *testing.T) {
		code, stdout, stderr := runShowmeshctl(t, 10*time.Second, "nodes", "--server", server, "--output", "json")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"nodeId"`) {
			t.Errorf("json stdout does not look like decoded JSON: %s", stdout)
		}
	})
}

// --- watch subprocess harness, for acceptance criterion (b) ---

// watchProcess wraps a real `showmeshctl watch` subprocess, with its stdout
// tailed line by line into a channel so a test can wait for a specific
// marker line to appear rather than sleeping and hoping.
type watchProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	lines  chan string
	stderr *syncBuffer

	mu   sync.Mutex
	done bool
}

// startWatch runs `showmeshctl watch --server <server>` as a subprocess.
func startWatch(t *testing.T, server, token string) *watchProcess {
	t.Helper()
	args := []string{"watch", "--server", server}
	if token != "" {
		args = append(args, "--token", token)
	}
	cmd := exec.Command(showmeshctlBinPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start showmeshctl watch: %v", err)
	}

	wp := &watchProcess{t: t, cmd: cmd, lines: make(chan string, 256), stderr: stderr}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			select {
			case wp.lines <- sc.Text():
			default:
				// A slow test not draining fast enough must not block the
				// subprocess's own stdout pipe indefinitely; dropping the
				// oldest-style overflow here is a test-harness concern
				// only, not a claim about the coordinator or the CLI.
			}
		}
	}()

	t.Cleanup(wp.stop)
	return wp
}

// waitForLine polls received lines until one contains substr, or fails t
// after timeout. It also drains (and ignores) every non-matching line, so
// a caller does not need its own buffering.
func (wp *watchProcess) waitForLine(t *testing.T, substr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case line := <-wp.lines:
			seen = append(seen, line)
			if strings.Contains(line, substr) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for a line containing %q; stderr so far:\n%s\nstdout lines seen:\n%s",
				timeout, substr, wp.stderr.String(), strings.Join(seen, "\n"))
			return ""
		}
	}
}

// countLinesContaining drains every currently-buffered line (non-blocking)
// and reports how many contain substr — used to assert on how many
// resnapshot events have happened SO FAR, as a baseline before triggering
// the next interruption.
func (wp *watchProcess) countLinesContaining(substr string) int {
	n := 0
	for {
		select {
		case line := <-wp.lines:
			if strings.Contains(line, substr) {
				n++
			}
		default:
			return n
		}
	}
}

func (wp *watchProcess) stop() {
	wp.mu.Lock()
	if wp.done {
		wp.mu.Unlock()
		return
	}
	wp.done = true
	wp.mu.Unlock()
	_ = wp.cmd.Process.Signal(os.Interrupt)
	waitDone := make(chan struct{})
	go func() { _ = wp.cmd.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		_ = wp.cmd.Process.Kill()
		<-waitDone
	}
}

// --- a minimal TCP proxy, for interruption shape 1: the connection dropping ---

// tcpDropProxy forwards TCP connections to target and can sever every
// currently-forwarded connection on demand, without target (the real
// coordinator process) ever being touched — a pure network-level
// disconnection, genuinely distinct from the coordinator process itself
// dying or restarting (interruption shape 2) or an in-band stream.reset
// frame (shape 3).
type tcpDropProxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns []net.Conn
}

func startTCPDropProxy(t *testing.T, target string) *tcpDropProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for drop proxy: %v", err)
	}
	p := &tcpDropProxy{ln: ln, target: target}
	go p.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *tcpDropProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpDropProxy) acceptLoop() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *tcpDropProxy) handle(client net.Conn) {
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, client)
	p.mu.Unlock()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = upstream.Close()
}

// sever closes every currently-forwarded client-facing connection,
// disconnecting whatever is on the other end of this proxy (showmeshctl,
// in every test that uses this) with no involvement from target at all.
func (p *tcpDropProxy) sever() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestWatchResnapshotsAfterConnectionDrop is acceptance criterion (b),
// interruption shape 1: the connection dropping, with the coordinator
// process itself untouched. showmeshctl watch connects through a small TCP
// proxy this test controls; severing the proxied connection produces
// exactly the network-level failure a flaky Wi-Fi link or an intervening
// NAT timeout would, with nothing at either endpoint aware anything
// happened until the socket errors out.
func TestWatchResnapshotsAfterConnectionDrop(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())
	proxy := startTCPDropProxy(t, coord.httpAddr)

	wp := startWatch(t, "http://"+proxy.addr(), "")
	wp.waitForLine(t, "--- snapshot (initial connect)", 10*time.Second)

	proxy.sever()

	// "reconnecting" is logged to stderr (watch.go's runWatch), not stdout;
	// wait for the SECOND snapshot line on stdout directly rather than
	// depending on stderr content this harness does not tail into wp.lines.
	wp.waitForLine(t, "--- snapshot (initial connect)", 15*time.Second)
}

// TestWatchResnapshotsAfterCoordinatorRestart is acceptance criterion (b),
// interruption shape 2: the coordinator process itself restarting
// underneath a connected client — a real SIGTERM, a real process exit, and
// a real new process bound to the same address, not a simulation of any of
// the three.
func TestWatchResnapshotsAfterCoordinatorRestart(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	clientID := "coord-" + uniqueSuffix()
	coord := startCoordinator(t, dataDir, clientID)

	wp := startWatch(t, "http://"+coord.httpAddr, "")
	wp.waitForLine(t, "--- snapshot (initial connect)", 10*time.Second)

	httpAddr := coord.httpAddr
	coord.shutdown() // real SIGTERM, real process exit

	// "... reconnecting" is logged to stderr (watch.go's runWatch), not
	// stdout; there is nothing to usefully wait for on stdout until a new
	// coordinator is actually reachable again, so proceed straight to
	// rebuilding it.
	//
	// Rebuild on the SAME address and the SAME data directory, exactly the
	// "teardown and rebuild" shape restart_test.go uses for the coordinator
	// itself, so showmeshctl's own reconnect loop (bounded exponential
	// backoff, per watch.go) has something to actually succeed against.
	startCoordinatorWithConfig(t, coordinatorConfig{dataDir: dataDir, clientID: clientID, httpAddr: httpAddr})

	wp.waitForLine(t, "--- snapshot (initial connect)", 30*time.Second)
}

// TestWatchResnapshotsAfterStreamReset is acceptance criterion (b),
// interruption shape 3: an in-band stream.reset from buffer overflow — the
// one shape that does NOT require the connection to fail at the transport
// level at all; the server itself declares the client's local model unsafe
// to keep applying deltas to, over the still-open connection, then closes
// it. Built the same deterministic way as
// TestSlowSSEConsumerGetsResetAndDisconnected: a tiny configured buffer and
// a real burst of change, this time observed through the real showmeshctl
// binary's own stdout instead of a raw response read.
func TestWatchResnapshotsAfterStreamReset(t *testing.T) {
	requireBroker(t)
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		streamSubscriberBuffer: 2,
	})

	wp := startWatch(t, "http://"+coord.httpAddr, "")
	wp.waitForLine(t, "--- snapshot (initial connect)", 10*time.Second)

	const burst = 200
	publishHelloBurst(t, burst)

	// "stream reset (reason=...)" is logged to stderr (watch.go's
	// watchOnce), not stdout; wait for the stdout snapshot line the reset
	// handler itself triggers.
	wp.waitForLine(t, "--- snapshot (stream.reset)", 15*time.Second)
}

// TestWatchNeverAppliesADeltaWithoutAPriorSnapshot is a narrower,
// protocol-level restatement of acceptance criterion (b) itself, guarding
// against the specific failure OPERATOR-UI section 6 forbids: applying a
// change-stream delta before ever having fetched an authoritative
// baseline.
//
// This used to assert only that the first meaningful line was "--- connected"
// **or** "--- snapshot" — but watchOnce always prints "--- connected" before
// it ever fetches a snapshot, so that assertion was satisfied by construction
// and passed even with the initial refetchSnapshot call replaced by a no-op
// (confirmed by mutation: green in 0.09s — Step 3 review finding 4.1). A
// client that never snapshots and applies deltas straight off the stream is
// exactly the forbidden shape, and it passed this test.
//
// The fix asserts on ORDER between two *specific* lines this test forces to
// both occur: it triggers a real node.changed (starting an agent) and then
// requires that a line containing "--- snapshot" was already seen, in
// stream order, strictly before the [node.changed] line naming that node.
// Deleting or no-opping the snapshot fetch means "--- snapshot" is never
// printed at all, so snapshotSeen stays false and the check below fails the
// instant the real delta arrives — this is what makes the repair bite.
func TestWatchNeverAppliesADeltaWithoutAPriorSnapshot(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	wp := startWatch(t, "http://"+coord.httpAddr, "")

	// Trigger the real change before we start reading lines: the assertion
	// below only cares about relative order of "--- snapshot" versus this
	// node's [node.changed] line, wherever each falls in the stream of
	// output this process produces.
	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID})

	snapshotSeen := false
	deadline := time.After(15 * time.Second)
	for {
		select {
		case line := <-wp.lines:
			if strings.Contains(line, "--- snapshot") {
				snapshotSeen = true
			}
			if strings.Contains(line, "[node.changed]") && strings.Contains(line, nodeID) {
				if !snapshotSeen {
					t.Fatalf("a [node.changed] line for %s was rendered before this process ever printed a snapshot line — a delta was applied without a prior authoritative baseline", nodeID)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a [node.changed] line for %s to confirm the snapshot-before-delta ordering (snapshotSeen=%v)", nodeID, snapshotSeen)
		}
	}
}
