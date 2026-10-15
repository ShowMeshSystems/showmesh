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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, errors.New("the video engine stopped mid-answer")
	}
	return data, resp.StatusCode, nil
}

func (c *Go2RTCClient) Put(ctx context.Context, id, source string) error {
	_, code, err := c.do(ctx, http.MethodPut, "/api/streams", url.Values{"name": {id}, "src": {source}}, "", nil, 1<<16)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("the video engine refused the stream with status %d", code)
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
// processes on a host-networked machine can reach loopback too.
func go2rtcConfig(password, webrtcPort string) string {
	return fmt.Sprintf(`api:
  listen: "%s"
  username: "%s"
  password: "%s"
  local_auth: true
rtsp:
  listen: ""
srtp:
  listen: ""
webrtc:
  listen: ":%s"
log:
  level: warn
`, go2rtcAPIAddr, go2rtcUser, password, webrtcPort)
}

// Supervisor runs go2rtc and restarts it when it exits.
type Supervisor struct {
	Binary     string
	WebRTCPort string
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
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "go2rtc.yaml")
	if err := os.WriteFile(cfg, []byte(go2rtcConfig(s.client.password, s.WebRTCPort)), 0o600); err != nil {
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
	go func() { err := cmd.Wait(); pw.Close(); done <- err }()
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

// producerLive reports whether go2rtc is receiving media for the stream.
func producerLive(gi Go2RTCInfo) bool {
	for _, p := range gi.Producers {
		if n, ok := p["recv"].(float64); ok && n > 0 {
			return true
		}
	}
	return false
}

// producerConnecting reports whether a producer has started but has not
// received media yet.
func producerConnecting(gi Go2RTCInfo) bool {
	for _, p := range gi.Producers {
		if _, started := p["state"]; started {
			return true
		}
	}
	return false
}
