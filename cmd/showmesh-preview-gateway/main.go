// Command showmesh-preview-gateway serves low-rate video previews of NDI
// sources and RTSP cameras to the coordinator (ADR-060).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/previewgateway"
	"github.com/showmeshsystems/showmesh/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version", "version":
			fmt.Println(version.String())
			return
		case "healthcheck":
			os.Exit(healthcheck())
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := previewgateway.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("The preview gateway is starting.", "addr", cfg.Addr, "version", version.String())
	if err := previewgateway.Run(ctx, cfg, log); err != nil {
		log.Error("The preview gateway stopped.", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	u, err := healthURL(os.Getenv("SHOWMESH_PREVIEW_GATEWAY_ADDR"))
	if err != nil {
		return 1
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// healthURL targets the host the gateway listens on, or loopback when addr
// names no host or an unspecified one.
func healthURL(addr string) (string, error) {
	if addr == "" {
		addr = ":8095"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
