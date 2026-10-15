package previewgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config is the gateway's run-time configuration.
type Config struct {
	Addr       string
	Token      string
	Go2RTCBin  string
	WebRTCPort string
}

// ConfigFromEnv reads SHOWMESH_PREVIEW_GATEWAY_* variables.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:       getenv("SHOWMESH_PREVIEW_GATEWAY_ADDR"),
		Token:      getenv("SHOWMESH_PREVIEW_GATEWAY_TOKEN"),
		Go2RTCBin:  getenv("SHOWMESH_PREVIEW_GATEWAY_GO2RTC"),
		WebRTCPort: getenv("SHOWMESH_PREVIEW_GATEWAY_WEBRTC_PORT"),
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
	if strings.TrimSpace(c.Token) == "" {
		return c, errors.New("The gateway token is not set. Set SHOWMESH_PREVIEW_GATEWAY_TOKEN and start the gateway again.")
	}
	if _, port, err := net.SplitHostPort(c.Addr); err != nil || port == "" {
		return c, fmt.Errorf("The gateway address %q is not a host:port. Set SHOWMESH_PREVIEW_GATEWAY_ADDR such as :8095.", c.Addr)
	}
	return c, nil
}

// Run serves the gateway until ctx ends.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	sup, client, err := NewSupervisor(cfg.Go2RTCBin, cfg.WebRTCPort, log)
	if err != nil {
		return err
	}
	disc := NewDiscovery(log)
	mgr := NewManager(client, disc, self, log)
	sup.OnReady = mgr.Resync

	go sup.Run(ctx)
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
	errc := make(chan error, 1)
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
