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
		case "ndi-pipeline":
			if len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: showmesh-preview-gateway ndi-pipeline <encoded-name>")
				os.Exit(2)
			}
			fmt.Fprintln(os.Stderr, previewgateway.ExecNDIPipeline(os.Args[2]))
			os.Exit(1)
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
	addr := os.Getenv("SHOWMESH_PREVIEW_GATEWAY_ADDR")
	if addr == "" {
		addr = ":8095"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
