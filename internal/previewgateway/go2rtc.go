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
	"syscall"
	"time"
)

const (
	go2rtcAPIAddr = "127.0.0.1:1984"
	go2rtcUser    = "gateway"
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
	base     string
	password string
	http     *http.Client
}

func NewGo2RTCClient(addr, password string) *Go2RTCClient {
	return &Go2RTCClient{base: "http://" + addr, password: password, http: &http.Client{}}
}

func (c *Go2RTCClient) do(ctx context.Context, method, path string, q url.Values, contentType string, body []byte, limit int64) ([]byte, int, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.SetBasicAuth(go2rtcUser, c.password)
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
func go2rtcConfig(password, webrtcPort string, candidates []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "api:\n  listen: %q\n  username: %q\n  password: %q\n  local_auth: true\n", go2rtcAPIAddr, go2rtcUser, password)
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
	// OnReady runs each time go2rtc has started and answers on its API.
	OnReady func(ctx context.Context)

	client *Go2RTCClient
}

func NewSupervisor(binary, webrtcPort string, log *slog.Logger) (*Supervisor, *Go2RTCClient, error) {
	pw := make([]byte, 24)
	if _, err := rand.Read(pw); err != nil {
		return nil, nil, err
	}
	c := NewGo2RTCClient(go2rtcAPIAddr, hex.EncodeToString(pw))
	return &Supervisor{Binary: binary, WebRTCPort: webrtcPort, Log: log, client: c}, c, nil
}

// Run blocks until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		s.Log.Warn("The video engine stopped. The gateway is restarting it.", "error", err, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "go2rtc-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cfg := filepath.Join(dir, "go2rtc.yaml")
	if err := os.WriteFile(cfg, []byte(go2rtcConfig(s.client.password, s.WebRTCPort, WebRTCCandidates(s.WebRTCPort, s.Candidates))), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(s.Binary, "-config", cfg)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = pw.Close(); done <- err }()
	go s.pumpLogs(pr)

	readyCtx, cancelReady := context.WithCancel(ctx)
	defer cancelReady()
	go func() {
		if s.waitReady(readyCtx) && s.OnReady != nil {
			s.OnReady(readyCtx)
		}
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

func (s *Supervisor) waitReady(ctx context.Context) bool {
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.client.List(cctx)
		cancel()
		if err == nil {
			return true
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

var urlCredentials = regexp.MustCompile(`(?i)(rtsps?://)[^@/\s"]+@`)

func redact(line string) string {
	return urlCredentials.ReplaceAllString(line, "${1}***@")
}

func (s *Supervisor) pumpLogs(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		s.Log.Warn("go2rtc", "line", redact(sc.Text()))
	}
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
