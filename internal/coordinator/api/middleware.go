package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// apiVersionHeaderName is the header this API both emits on every response
// (contract section 6.2: "ShowMesh-API-Version: 1 on every /api/v1
// response, including errors") and accepts as an optional request header
// (contract section 6.6) for a client to declare the version it expects.
const apiVersionHeaderName = "ShowMesh-API-Version"

// servedAPIVersion is the version string this coordinator's /api/v1 tree
// serves. A string, not an int, because it is compared directly against
// the raw request header value — parsing it as a number first would accept
// "1.0" or "01" as equivalent to "1", which contract section 6.6's
// version-negotiation test (garbage input must 400) requires this package
// not to do.
const servedAPIVersion = "1"

// chain composes middlewares around h, with mws[0] as the outermost layer
// — the one that sees a request first and a response last. Used once, in
// [New], to keep the composition order visible in a single place rather
// than nested function calls.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// withAPIVersionHeader sets the [apiVersionHeaderName] response header on
// every response this API produces, success or error, per contract section
// 6.2. It must run outer enough that every other middleware's response
// (including a 400 or 401 written by a middleware below it in the chain)
// still carries the header — Header().Set here happens before next is
// called, and net/http only actually sends headers once something calls
// Write or WriteHeader, so setting it first is sufficient regardless of
// which layer ends up producing the status code.
func withAPIVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(apiVersionHeaderName, servedAPIVersion)
		next.ServeHTTP(w, r)
	})
}

// withVersionNegotiation implements contract section 6.6's version
// negotiation: an absent header is fine (defaults to what this coordinator
// serves), an exact match ("1") is fine, and anything else — "2", garbage,
// an empty string sent explicitly — is a 400 unsupported-api-version
// problem naming what this coordinator does serve. This handles the
// request-header form; an unknown API version IN THE PATH (e.g.
// /api/v2/nodes) is handled separately by the catch-all route in
// handlers.go, since net/http's mux, not this middleware, is what decides a
// path never matched anything under /api/v1.
func withVersionNegotiation(logger *slog.Logger, clock func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if v := r.Header.Get(apiVersionHeaderName); v != "" && v != servedAPIVersion {
				writeProblem(w, logger, clock(), unsupportedAPIVersionProblem(
					"this coordinator serves API version 1; the "+apiVersionHeaderName+" request header must be absent or \"1\""))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// withAuth implements contract section 6.8: when token is non-empty, every
// request must carry "Authorization: Bearer <token>", compared in constant
// time via crypto/subtle so response timing cannot leak how much of a
// guessed token matched. When token is empty, auth is disabled entirely —
// New's caller (internal/coordinator/coordinator.go, a later task) is
// responsible for logging the startup warning contract section 6.8
// requires; this middleware only enforces the check, it does not decide
// whether the deployment is being run open.
func withAuth(token string, logger *slog.Logger, clock func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		want := []byte(token)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeProblem(w, logger, clock(), unauthorizedProblem("a valid Authorization: Bearer <token> header is required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header value. ok is false for any other shape (missing, wrong scheme, no
// token), including one with leading/trailing whitespace variations this
// package does not try to tolerate — an operator's client should send the
// header correctly formed.
func bearerToken(header string) (token string, ok bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token = strings.TrimPrefix(header, prefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// withCORS implements contract section 6.8's CORS rule: no allowed
// origins configured means no CORS headers at all (a browser cross-origin
// request simply fails the browser's own same-origin check, which is the
// safe default for a control API with no configured trust). An origin
// present in allowedOrigins is echoed back exactly — never a "*" wildcard,
// which contract section 6.8 forbids pairing with credentials, and this
// API always requires credentials (the bearer token) once auth is
// enabled. A preflight OPTIONS request is answered directly here (204, no
// body) rather than being allowed to reach [withAuth] or a route handler:
// a browser's own preflight request never carries the application's
// Authorization header, so requiring one here would make every
// cross-origin request fail before the real request is ever sent.
func withCORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			if r.Method == http.MethodOptions {
				if origin != "" && allowed[origin] {
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, "+apiVersionHeaderName)
					w.Header().Set("Access-Control-Max-Age", "600")
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// withRequestLogging logs one line per request at Debug (not Info: contract
// section 6 middleware guidance forbids info-level logging on every poll,
// and this API is expected to be polled and/or held open as an SSE stream
// continuously). It logs the method, path (never RawQuery — a future query
// parameter carrying something sensitive must not become a logging
// regression just because this middleware only ever intended to log
// "?since=..."), status, and duration. It never logs headers, so the
// Authorization header's token value never reaches a log line through this
// path.
func withRequestLogging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			if logger != nil {
				logger.Debug("api request",
					"method", r.Method, "path", r.URL.Path,
					"status", sw.status, "duration_ms", time.Since(start).Milliseconds())
			}
		})
	}
}

// withMethodNotAllowedAsProblem wraps next (the router mux) so that
// net/http.ServeMux's own automatic "405 Method Not Allowed" response —
// which it emits whenever a request's path matches a registered pattern
// but the request's method does not (Go 1.22+ ServeMux behavior) — is
// rewritten into contract section 6.6's RFC 9457 application/problem+json
// shape instead of ServeMux's plain-text default body.
//
// This exists because of a review finding (2.8): api.go's catch-all route
// used to be registered with no method restriction at all
// (mux.Handle("/api/", ...)), which meant it — not ServeMux's own 405
// logic — always won the match for a non-GET request to a real route, so
// e.g. POST /api/v1/nodes answered a lying 404 resource-not-found instead
// of 405. The fix is two-part: api.go now registers that catch-all as
// "GET /api/..." so ServeMux's built-in method-mismatch detection can
// actually fire (it only fires when nothing else registered for that path
// lacks a method restriction), and this middleware reformats what it
// produces. The Allow header ServeMux computed — the exact, real set of
// methods registered for the matched path — is read here and preserved
// verbatim; nothing in this package recomputes or hand-maintains that
// list, so it cannot drift from the routes actually registered in api.go.
func withMethodNotAllowedAsProblem(next http.Handler, logger *slog.Logger, clock func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := &methodNotAllowedInterceptor{ResponseWriter: w, logger: logger, clock: clock}
		next.ServeHTTP(mw, r)
	})
}

// methodNotAllowedInterceptor rewrites exactly one status code — 405 —
// from whatever the wrapped http.ResponseWriter would otherwise have
// sent. Every other status passes through this type untouched, byte for
// byte: a 200, a 404 this package's own handleUnknownAPIPath already
// renders as problem+json, or anything else must never be altered by this
// wrapper existing in the chain.
type methodNotAllowedInterceptor struct {
	http.ResponseWriter
	logger *slog.Logger
	clock  func() time.Time

	// rewriting is set only while this type is replacing a 405 response's
	// body; it is what tells Write to discard net/http.Error's own
	// "Method Not Allowed\n" plain-text body once WriteHeader has already
	// substituted a problem+json document for it — without this, that
	// text would be appended straight after the JSON this type wrote,
	// corrupting the response.
	rewriting bool
}

// WriteHeader intercepts exactly [http.StatusMethodNotAllowed]. For that
// status only, it reads the Allow header net/http.ServeMux already set on
// the real header map before calling WriteHeader (Header() is not
// intercepted by this type — it delegates straight to the embedded
// http.ResponseWriter — so ServeMux's own Set("Allow", ...) call landed on
// the real map, not a copy), strips the "X-Content-Type-Options: nosniff"
// header net/http.Error also set (so a 405 carries the same header shape
// every other problem response in this package does, with nothing
// text/plain-specific left over), and writes a problem+json body directly
// to the wrapped writer via [writeProblem] — which performs its own
// Header().Set(Content-Type) and WriteHeader call, so this method must
// not call the embedded WriteHeader itself first or every response would
// carry two WriteHeader calls (net/http logs the second as
// "superfluous").
func (m *methodNotAllowedInterceptor) WriteHeader(status int) {
	if status != http.StatusMethodNotAllowed {
		m.ResponseWriter.WriteHeader(status)
		return
	}
	allow := m.ResponseWriter.Header().Get("Allow")
	m.ResponseWriter.Header().Del("X-Content-Type-Options")
	m.rewriting = true
	writeProblem(m.ResponseWriter, m.logger, m.clock(), methodNotAllowedProblem(
		"this route does not support this request's method; it supports: "+allow))
}

// Write discards net/http.Error's own "Method Not Allowed\n" body once
// [methodNotAllowedInterceptor.WriteHeader] has already replaced it with a
// problem+json document; every other response's body passes through to
// the wrapped writer unchanged.
func (m *methodNotAllowedInterceptor) Write(b []byte) (int, error) {
	if m.rewriting {
		return len(b), nil
	}
	return m.ResponseWriter.Write(b)
}

// Flush and Unwrap exist for exactly the reason
// [statusCapturingWriter.Flush] and [statusCapturingWriter.Unwrap] do —
// see those methods' doc comments — except one layer closer to the mux:
// this type wraps every request, including GET /api/v1/stream, so without
// these two methods every SSE connection would lose both its Flusher and
// its http.ResponseController path to the real *http.response the moment
// this middleware was added to the chain, and would then be silently
// killed by httpapi.NewServer's WriteTimeout a few seconds after
// connecting — the same class of defect this project already found once
// by running the real binary, one wrapper further out.
func (m *methodNotAllowedInterceptor) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (m *methodNotAllowedInterceptor) Unwrap() http.ResponseWriter {
	return m.ResponseWriter
}

// statusCapturingWriter records the status code an inner handler wrote, so
// [withRequestLogging] can log it without altering response behavior.
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Flush lets the SSE stream handler (stream.go) keep working through a
// [statusCapturingWriter] wrapper: net/http.Flusher is satisfied
// structurally, so a plain type assertion on this wrapper still succeeds
// as long as this method exists and delegates to the real
// http.ResponseWriter underneath.
func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying http.ResponseWriter, per the net/http
// convention http.ResponseController documents for exactly this purpose
// (Go 1.20+): a wrapper type implementing Unwrap lets ResponseController
// see through it to reach the real *http.response and its optional
// SetWriteDeadline/SetReadDeadline support.
//
// This exists for one reason: [Hub.ServeHTTP] (stream.go) clears the write
// deadline internal/coordinator/httpapi.NewServer's WriteTimeout would
// otherwise install on this connection, and it can only do that if
// http.NewResponseController(w) can walk through this middleware's wrapper
// to the underlying connection. Without this method, every SSE stream
// mounted behind withRequestLogging would be silently killed by that
// WriteTimeout a few seconds after connecting — a defect this project's
// own wiring task found by actually running the real binary and watching a
// real stream die, not by inspecting this file.
func (w *statusCapturingWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
