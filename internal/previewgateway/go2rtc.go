package previewgateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	go2rtcUser     = "gateway"
	engineCallWait = 5 * time.Second
)

// Go2RTCInfo is what go2rtc reports for one stream.
type Go2RTCInfo struct {
	Producers []map[string]any `json:"producers"`
	Consumers []map[string]any `json:"consumers"`
}

// Engine is the part of go2rtc the gateway drives.
type Engine interface {
	Put(ctx context.Context, id, source string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) (map[string]Go2RTCInfo, error)
	Snapshot(ctx context.Context, id string) ([]byte, error)
	WebRTC(ctx context.Context, id string, offer []byte) ([]byte, error)
}

// Go2RTCClient talks to go2rtc's loopback API.
type Go2RTCClient struct {
	mu       sync.RWMutex
	base     string
	password string
	http     *http.Client
}

func NewGo2RTCClient(addr, password string) *Go2RTCClient {
	c := &Go2RTCClient{http: &http.Client{}}
	c.Configure(addr, password)
	return c
}

// Configure points the client at one go2rtc run. Until it is called the
// client reports that the engine is not answering.
func (c *Go2RTCClient) Configure(addr, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base, c.password = "http://"+addr, password
	if addr == "" {
		c.base = ""
	}
}

func (c *Go2RTCClient) target() (base, password string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.base, c.password
}

func (c *Go2RTCClient) do(ctx context.Context, method, path string, q url.Values, contentType string, body []byte, limit int64) ([]byte, int, error) {
	base, password := c.target()
	if base == "" {
		return nil, 0, errors.New("the video engine is not answering")
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.SetBasicAuth(go2rtcUser, password)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("the video engine is not answering")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, errors.New("the video engine stopped mid-answer")
	}
	return data, resp.StatusCode, nil
}

func (c *Go2RTCClient) Put(ctx context.Context, id, source string) error {
	ctx, cancel := context.WithTimeout(ctx, engineCallWait)
	defer cancel()
	body, code, err := c.do(ctx, http.MethodPut, "/api/streams", url.Values{"name": {id}, "src": {source}}, "", nil, 1<<16)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("the video engine refused the stream with status %d: %s", code, redact(strings.TrimSpace(string(body))))
	}
	return nil
}

func (c *Go2RTCClient) Delete(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, engineCallWait)
	defer cancel()
	_, code, err := c.do(ctx, http.MethodDelete, "/api/streams", url.Values{"src": {id}}, "", nil, 1<<16)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("the video engine refused to remove the stream with status %d", code)
	}
	return nil
}

func (c *Go2RTCClient) List(ctx context.Context) (map[string]Go2RTCInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, engineCallWait)
	defer cancel()
	data, code, err := c.do(ctx, http.MethodGet, "/api/streams", nil, "", nil, 8<<20)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("the video engine answered status %d", code)
	}
	out := map[string]Go2RTCInfo{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New("the video engine sent a list the gateway cannot read")
	}
	return out, nil
}

func (c *Go2RTCClient) Snapshot(ctx context.Context, id string) ([]byte, error) {
	data, code, err := c.do(ctx, http.MethodGet, "/api/frame.jpeg", url.Values{"src": {id}}, "", nil, 8<<20)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK || !isJPEG(data) {
		return nil, ErrNoFrame
	}
	return data, nil
}

func (c *Go2RTCClient) WebRTC(ctx context.Context, id string, offer []byte) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{"type": "offer", "sdp": string(offer)})
	data, code, err := c.do(ctx, http.MethodPost, "/api/webrtc", url.Values{"src": {id}}, "application/json", body, 1<<20)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, ErrNoAnswer
	}
	var ans struct {
		SDP string `json:"sdp"`
	}
	if json.Unmarshal(data, &ans) != nil || ans.SDP == "" {
		return nil, ErrNoAnswer
	}
	return []byte(ans.SDP), nil
}

var (
	ErrNoFrame  = errors.New("no frame")
	ErrNoAnswer = errors.New("no answer")
)

func isJPEG(b []byte) bool {
	return len(b) > 4 && b[0] == 0xFF && b[1] == 0xD8 && b[len(b)-2] == 0xFF && b[len(b)-1] == 0xD9
}

// go2rtcConfig keeps every listener except WebRTC off the network. The API
// listens on loopback and still requires the per-run password, because other
// processes on a host-networked machine can reach loopback too. go2rtc offers
// no host candidates of its own, so they are listed here, and STUN is off
// because previews stay on the local network.
func go2rtcConfig(apiAddr, password, webrtcPort string, candidates []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "api:\n  listen: %q\n  username: %q\n  password: %q\n  local_auth: true\n", apiAddr, go2rtcUser, password)
	b.WriteString("rtsp:\n  listen: \"\"\nsrtp:\n  listen: \"\"\n")
	fmt.Fprintf(&b, "webrtc:\n  listen: %q\n  ice_servers: []\n", ":"+webrtcPort)
	if len(candidates) > 0 {
		b.WriteString("  candidates:\n")
		for _, c := range candidates {
			fmt.Fprintf(&b, "    - %q\n", c)
		}
	}
	b.WriteString("log:\n  level: warn\n")
	return b.String()
}

var skippedInterfacePrefixes = []string{"lo", "docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "tun", "tap"}

// WebRTCCandidates lists this host's usable IPv4 addresses as host:port
// candidates. override, when set, is a comma-separated list of addresses to
// use instead.
func WebRTCCandidates(port, override string) []string {
	if strings.TrimSpace(override) != "" {
		var out []string
		for _, h := range strings.Split(override, ",") {
			if h = strings.TrimSpace(h); h != "" {
				out = append(out, net.JoinHostPort(h, port))
			}
		}
		return out
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var entries []ifaceAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				entries = append(entries, ifaceAddr{ifc.Name, ipn.IP})
			}
		}
	}
	return candidatesFrom(entries, port)
}

type ifaceAddr struct {
	name string
	ip   net.IP
}

func candidatesFrom(entries []ifaceAddr, port string) []string {
	var out []string
	for _, e := range entries {
		ip4 := e.ip.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
			continue
		}
		skip := false
		for _, p := range skippedInterfacePrefixes {
			if strings.HasPrefix(e.name, p) {
				skip = true
			}
		}
		if !skip {
			out = append(out, net.JoinHostPort(ip4.String(), port))
		}
	}
	return out
}

// Supervisor runs go2rtc and restarts it when it exits.
type Supervisor struct {
	Binary     string
	WebRTCPort string
	// Candidates overrides the detected WebRTC addresses; see WebRTCCandidates.
	Candidates string
	Log        *slog.Logger
	// OnReady runs each time go2rtc has started and proved it is ours.
	OnReady func(ctx context.Context)

	client *Go2RTCClient
}

// engineError is a failure that restarting go2rtc will not fix.
type engineError struct{ msg string }

func (e *engineError) Error() string { return e.msg }

const readyTimeout = 15 * time.Second

func NewSupervisor(binary, webrtcPort string, log *slog.Logger) (*Supervisor, *Go2RTCClient) {
	c := NewGo2RTCClient("", "")
	return &Supervisor{Binary: binary, WebRTCPort: webrtcPort, Log: log, client: c}, c
}

// Run blocks until ctx ends, or returns the error that makes go2rtc unable to
// run at all.
func (s *Supervisor) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var fatal *engineError
		if errors.As(err, &fatal) {
			return fatal
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		s.Log.Warn("The video engine stopped. The gateway is restarting it.", "error", err, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

// freeLoopbackAddr picks a loopback port nothing is listening on.
func freeLoopbackAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String(), nil
}

// CheckWebRTCPort reports a clear error when the WebRTC port is taken.
func CheckWebRTCPort(port string) error {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return webrtcPortError(port)
	}
	_ = ln.Close()
	pc, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		return webrtcPortError(port)
	}
	_ = pc.Close()
	return nil
}

func webrtcPortError(port string) *engineError {
	return &engineError{fmt.Sprintf("The WebRTC port %s is already in use. Set SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT to a free port and restart the gateway.", port)}
}

// engineLineFault turns go2rtc's report of a failed API listener, which it
// logs and then keeps running without an API, into an error that ends the run.
func engineLineFault(line string) error {
	if strings.Contains(line, "[api] listen") && strings.Contains(line, "error=") {
		return errors.New("go2rtc could not open its API port")
	}
	return nil
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	if err := CheckWebRTCPort(s.WebRTCPort); err != nil {
		return err
	}
	apiAddr, err := freeLoopbackAddr()
	if err != nil {
		return err
	}
	pwBytes := make([]byte, 24)
	if _, err := rand.Read(pwBytes); err != nil {
		return err
	}
	password := hex.EncodeToString(pwBytes)

	dir, err := os.MkdirTemp("", "go2rtc-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cfg := filepath.Join(dir, "go2rtc.yaml")
	if err := os.WriteFile(cfg, []byte(go2rtcConfig(apiAddr, password, s.WebRTCPort, WebRTCCandidates(s.WebRTCPort, s.Candidates))), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(s.Binary, "-config", cfg)
	cmd.Env = minimalEnv(engineEnvKeys...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	defer s.client.Configure("", "")
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = pw.Close(); done <- err }()
	fault := make(chan error, 1)
	go s.pumpLogs(pr, func(line string) {
		if err := engineLineFault(line); err != nil {
			select {
			case fault <- err:
			default:
			}
		}
	})

	readyCtx, cancelReady := context.WithCancel(ctx)
	defer cancelReady()
	go func() {
		ready := s.waitReady(readyCtx, apiAddr, password)
		if readyCtx.Err() != nil {
			return
		}
		if !ready {
			select {
			case fault <- errors.New("go2rtc did not answer on its own API port"):
			default:
			}
			return
		}
		s.client.Configure(apiAddr, password)
		if s.OnReady != nil {
			s.OnReady(readyCtx)
		}
	}()

	stop := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	select {
	case err := <-done:
		return err
	case err := <-fault:
		stop()
		return err
	case <-ctx.Done():
		stop()
		return ctx.Err()
	}
}

// waitReady polls until the process on addr proves it is the go2rtc this
// supervisor started, and reports false if that does not happen in time.
func (s *Supervisor) waitReady(ctx context.Context, addr, password string) bool {
	deadline := time.Now().Add(readyTimeout)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		if proveEngine(ctx, addr, password) {
			return true
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

// proveEngine shows that addr is answered by a go2rtc started with this run's
// password: a request without credentials is refused and one with them is
// accepted. A process that does not know the password cannot do both.
func proveEngine(ctx context.Context, addr, password string) bool {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status := func(auth bool) int {
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, "http://"+addr+"/api/streams", nil)
		if err != nil {
			return 0
		}
		if auth {
			req.SetBasicAuth(go2rtcUser, password)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	return status(false) == http.StatusUnauthorized && status(true) == http.StatusOK
}

var rtspURL = regexp.MustCompile(`(?i)rtsps?://[^\s"]+`)

// redact keeps only the scheme and host:port of every rtsp and rtsps URL. The
// password ends at the last '@' because it may contain one, and the path and
// query are dropped because cameras put tokens there.
func redact(line string) string {
	return rtspURL.ReplaceAllStringFunc(line, func(u string) string {
		scheme := u[:strings.Index(u, "://")+3]
		rest := u[len(scheme):]
		out := scheme
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			out += "***@"
			rest = rest[i+1:]
		}
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			return out + rest[:i] + "/***"
		}
		return out + rest
	})
}

func (s *Supervisor) pumpLogs(r io.Reader, inspect func(line string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		s.Log.Warn("go2rtc", "line", redact(sc.Text()))
		inspect(sc.Text())
	}
	_, _ = io.Copy(io.Discard, r)
}

// producerBytes is the number of bytes go2rtc has received from the source.
func producerBytes(gi Go2RTCInfo) float64 {
	var total float64
	for _, p := range gi.Producers {
		if n, ok := p["bytes_recv"].(float64); ok {
			total += n
		}
	}
	return total
}

// producerConnected reports whether go2rtc holds an open connection to the
// source, which it does only while someone watches.
func producerConnected(gi Go2RTCInfo) bool {
	for _, p := range gi.Producers {
		if _, ok := p["id"]; ok {
			return true
		}
	}
	return false
}
