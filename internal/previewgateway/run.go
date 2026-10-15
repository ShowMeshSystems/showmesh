package previewgateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Config is the gateway's run-time configuration.
type Config struct {
	Addr       string
	Token      string
	Go2RTCBin  string
	WebRTCPort string
	// Candidates overrides the WebRTC addresses offered to browsers.
	Candidates string
}

// ConfigFromEnv reads SHOWMESH_PREVIEW_GATEWAY_* variables.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:       getenv("SHOWMESH_PREVIEW_GATEWAY_ADDR"),
		Token:      getenv("SHOWMESH_PREVIEW_GATEWAY_TOKEN"),
		Go2RTCBin:  getenv("SHOWMESH_PREVIEW_GATEWAY_GO2RTC"),
		WebRTCPort: getenv("SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT"),
		Candidates: getenv("SHOWMESH_PREVIEW_GATEWAY_WEBRTC_CANDIDATES"),
	}
	if c.Addr == "" {
		c.Addr = ":8095"
	}
	if c.Go2RTCBin == "" {
		c.Go2RTCBin = "go2rtc"
	}
	if c.WebRTCPort == "" {
		c.WebRTCPort = "8555"
	}
	if n, err := strconv.Atoi(c.WebRTCPort); err != nil || n < 1 || n > 65535 {
		return c, problemf("The WebRTC port %q is not a port number. Set SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT to a number from 1 to 65535.", c.WebRTCPort)
	}
	if strings.TrimSpace(c.Token) == "" {
		return c, problem("The gateway token is not set. Set SHOWMESH_PREVIEW_GATEWAY_TOKEN and start the gateway again.")
	}
	if _, port, err := net.SplitHostPort(c.Addr); err != nil || port == "" {
		return c, problemf("The gateway address %q is not a host:port. Set SHOWMESH_PREVIEW_GATEWAY_ADDR such as :8095.", c.Addr)
	}
	return c, nil
}

// Run serves the gateway until ctx ends.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	if err := CheckWebRTCPort(cfg.WebRTCPort); err != nil {
		return err
	}
	sup, client := NewSupervisor(cfg.Go2RTCBin, cfg.WebRTCPort, log)
	disc := NewDiscovery(log)
	secret, err := randomHex(16)
	if err != nil {
		return err
	}
	feedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	feedURL := "http://" + feedLn.Addr().String() + "/ndi/" + secret
	mgr := NewManager(client, disc, feedURL, log)
	sup.OnReady = mgr.Resync
	feed := &http.Server{Handler: NewFeed(secret, mgr, disc, log).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = feed.Serve(feedLn) }()
	defer func() { _ = feed.Close() }()

	sup.Candidates = cfg.Candidates
	errc := make(chan error, 2)
	go func() {
		if err := sup.Run(ctx); err != nil {
			errc <- err
		}
	}()
	go disc.Loop(ctx)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mgr.Retry(ctx)
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           NewServer(cfg.Token, mgr, disc, client).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
