package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// The signed weather delay start on the agent's inbound listener. It is the
// only route here that runs ahead of the fppconnect.settings.enabled gate,
// and no resume route exists.

const weatherDelayStartPath = "/showmesh/v1/weather-delay/start"

const weatherDelayHTTPMaxBodyBytes = 4 * 1024

// A signed start older than a day or more than five minutes ahead of this
// node's clock is refused. Replaying a fresh start is accepted on purpose.
const (
	weatherDelayHTTPMaxAge    = 24 * time.Hour
	weatherDelayHTTPMaxFuture = 5 * time.Minute
)

// weatherDelayHTTPConfig is the signed route's dependencies. A nil publicKey
// means this node holds no coordinator key, and the route answers 503.
type weatherDelayHTTPConfig struct {
	ops       *weatherDelayOperations
	publicKey ed25519.PublicKey
}

// loadWeatherDelayPublicKey reads the coordinator's base64 Ed25519 public key
// from path, or returns nil when path is empty or the key is unusable.
func loadWeatherDelayPublicKey(path string, logger *slog.Logger) ed25519.PublicKey {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Warn("failed to read the configured weather delay coordinator public key; this node will refuse a signed weather delay start with 503 until this is fixed",
			"path", path, "error", err)
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		logger.Warn("the configured weather delay coordinator public key is not valid base64; this node will refuse a signed weather delay start with 503 until this is fixed",
			"path", path, "error", err)
		return nil
	}
	if len(decoded) != ed25519.PublicKeySize {
		logger.Warn("the configured weather delay coordinator public key is the wrong size; this node will refuse a signed weather delay start with 503 until this is fixed",
			"path", path, "size", len(decoded), "want", ed25519.PublicKeySize)
		return nil
	}
	logger.Info("loaded the coordinator public key for signed weather delay starts", "path", path)
	return ed25519.PublicKey(decoded)
}

// handleWeatherDelayStart is POST /showmesh/v1/weather-delay/start: verify
// the signed request, then run the same start as weatherdelay.start.
func (s *fppConnectServer) handleWeatherDelayStart(w http.ResponseWriter, r *http.Request) {
	fppConnectSetReadDeadline(w, fppConnectDiscoveryReadDeadline, s.logger)
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)

	if s.weatherDelay.publicKey == nil || s.weatherDelay.ops == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, weatherDelayHTTPMaxBodyBytes+1))
	if err != nil {
		http.Error(w, "could not read the request body", http.StatusBadRequest)
		return
	}
	if len(body) > weatherDelayHTTPMaxBodyBytes {
		http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
		return
	}

	var signed weatherdelay.SignedStartRequest
	if err := json.Unmarshal(body, &signed); err != nil {
		http.Error(w, "request body is not valid JSON", http.StatusBadRequest)
		return
	}
	if err := signed.Verify(s.weatherDelay.publicKey); err != nil {
		http.Error(w, "request does not verify", http.StatusForbidden)
		return
	}

	now := time.Now
	if s.now != nil {
		now = s.now
	}
	age := now().Sub(signed.Request.IssuedAt)
	if age > weatherDelayHTTPMaxAge {
		http.Error(w, "request is too old", http.StatusForbidden)
		return
	}
	if age < -weatherDelayHTTPMaxFuture {
		http.Error(w, "request is too far in the future", http.StatusForbidden)
		return
	}

	// Detached so a caller that hangs up cannot cancel the alert mid-start.
	alertPlaying, alertReason, unsilenced := s.weatherDelay.ops.doStart(context.WithoutCancel(r.Context()), signed.Request.Kind, now())

	fppConnectWriteJSON(w, http.StatusOK, map[string]any{
		"kind":         signed.Request.Kind,
		"alertPlaying": alertPlaying,
		"alertReason":  alertReason,
		"unsilenced":   unsilenced,
	})
}
