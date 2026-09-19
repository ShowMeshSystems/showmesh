package agent

import (
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

// This file is ADR-053 decision 9's one superseding route on the agent's
// existing inbound listener (fppconnecthttp.go): a signed weather delay
// start, verified with the coordinator public key this node holds
// (ADR-025's node-side key pinning). ADR-044's reasoning stands for every
// other capability on this listener; this route is the sole exception,
// and it runs ahead of the fppconnect.settings.enabled gate, see
// newFPPConnectHandler's own doc comment.

// weatherDelayStartPath is the one route this decision adds. No resume
// route exists and no other new route is added.
const weatherDelayStartPath = "/showmesh/v1/weather-delay/start"

// weatherDelayHTTPMaxBodyBytes is the 4 KiB cap ADR-053 decision 9 sets
// on the signed start request body.
const weatherDelayHTTPMaxBodyBytes = 4 * 1024

// weatherDelayHTTPMaxAge and weatherDelayHTTPMaxFuture bound how stale or
// how far ahead of this node's own clock a signed request's IssuedAt may
// be: older than a day, or more than five minutes ahead, is refused.
// Replay of an otherwise-fresh, valid start is accepted on purpose
// (ADR-053 decision 8): replaying a start can only cause darkness.
const (
	weatherDelayHTTPMaxAge    = 24 * time.Hour
	weatherDelayHTTPMaxFuture = 5 * time.Minute
)

// weatherDelayHTTPConfig is this route's own dependencies, threaded
// through runFPPConnectHTTPListener -> newFPPConnectProductionServer ->
// newFPPConnectHandler -> fppConnectServer, alongside its other fixed,
// per-node values. PublicKey nil means this node holds no coordinator
// signing key: the route answers 503 and does nothing, a valid, degraded
// state (ADR-025 decision 7's "enrollment while the coordinator is
// unreachable is a defined state, not an error," applied here to a node
// that was never given a key to pin at all).
type weatherDelayHTTPConfig struct {
	ops       *weatherDelayOperations
	publicKey ed25519.PublicKey
}

// loadWeatherDelayPublicKey reads and decodes the coordinator's Ed25519
// public key from path (base64 standard encoding, matching
// internal/coordinator/signingkey's own logging convention for this key),
// or returns nil when path is empty or the key cannot be read or decoded.
// A missing or unreadable key is never fatal: this node simply holds no
// key, and the route it verifies answers 503 (ADR-025 decision 7).
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
// the signed request, then run the identical code
// "weatherdelay.start" runs as a dispatched operation
// (weatherDelayOperations.doStart) — ADR-053 decision 8's parallel
// delivery paths reach one implementation, never two independently
// written ones.
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

	alertPlaying, alertReason := s.weatherDelay.ops.doStart(r.Context(), signed.Request.Kind, now())

	fppConnectWriteJSON(w, http.StatusOK, map[string]any{
		"kind":         signed.Request.Kind,
		"alertPlaying": alertPlaying,
		"alertReason":  alertReason,
	})
}
