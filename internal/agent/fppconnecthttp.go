package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/fppconnect"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file implements the agent's one and only inbound HTTP listener,
// ADR-044's compatibility shim for xLights' FPP Connect dialog. It is not a
// ShowMesh API: it stays out of api/openapi.yaml and gains no operator
// endpoint (ADR-044 decision 2), and its specification lives in
// docs/build/TRACK-E-FPP-CONNECT.md's "Listener surface" section, not here.

const (
	// fppConnectReadHeaderTimeout and fppConnectReadTimeout bound how long a
	// slow or hostile client can hold this listener's goroutines open; none
	// of these routes ever wait on anything slower than reading the
	// holder's in-memory state, so both are short.
	fppConnectReadHeaderTimeout = 5 * time.Second
	fppConnectReadTimeout       = 10 * time.Second
	fppConnectWriteTimeout      = 10 * time.Second

	// fppConnectMaxHeaderBytes bounds the request line plus headers.
	fppConnectMaxHeaderBytes = 16 * 1024

	// fppConnectMaxBodyBytes bounds a request body. None of this listener's
	// routes have a body, so this exists only to refuse one a client sends
	// anyway rather than to serve any real payload.
	fppConnectMaxBodyBytes = 4 * 1024

	// fppConnectShutdownTimeout bounds Shutdown when the agent's context
	// ends, so a hung in-flight request cannot delay the rest of the
	// agent's own clean-shutdown path.
	fppConnectShutdownTimeout = 5 * time.Second

	// fppConnectPlatform is the value served wherever RES-003 records
	// xLights (or a human) reading a platform/vendor-type string: system
	// info's Platform and Variant, and the multiSyncSystems self-entry's
	// type. ADR-044 decision 10: no served content may claim "Falcon
	// Player"; this is ShowMesh's own name, not a borrowed one.
	fppConnectPlatform = "ShowMesh"

	// fppConnectEnabledPollInterval is how often the listener goroutine
	// re-reads view.Enabled() to keep the render report's disabled-by-
	// configuration evidence current. The view offers no change hook, so
	// this is a poll; every route already reads Enabled() fresh per
	// request (fppConnectRequireEnabled), so this interval only bounds how
	// promptly the STATUS an operator reads catches up, not how promptly
	// the routes themselves react.
	fppConnectEnabledPollInterval = 5 * time.Second

	// fppConnectDisabledReason is the status reason recorded while the
	// listener is bound but view.Enabled() is false, read by an operator
	// via the render report's fppConnectReason field.
	fppConnectDisabledReason = "disabled by configuration"

	// fppConnectPathSystemInfo, fppConnectPathMultiSyncSystems and
	// fppConnectPathPlaylists are the three fixed routes route() matches by
	// exact, literal comparison against r.URL.EscapedPath(). See route's
	// own doc comment for why matching happens this way instead of through
	// http.ServeMux.
	fppConnectPathSystemInfo       = "/api/system/info"
	fppConnectPathMultiSyncSystems = "/api/fppd/multiSyncSystems"
	fppConnectPathPlaylists        = "/api/playlists"

	// fppConnectPlaylistPrefix is the fourth route's fixed prefix; route()
	// matches anything after it as the (still escaped) playlist name.
	fppConnectPlaylistPrefix = "/api/playlist/"
)

// fppConnectNodeNamespace is a fixed, arbitrary namespace UUID used to
// derive every node's advertised uuid (UUIDv5, RFC 4122) from its ShowMesh
// node id. Declared once, as a constant fact, so the derived uuid is stable
// across restarts and identical on every node that shares a node id. Never
// regenerate this value: doing so changes every node's advertised uuid at
// once, which xLights (and anything else keying history on it) would see as
// an entirely new fleet of devices.
var fppConnectNodeNamespace = uuid.MustParse("e696afff-9455-447b-af78-4b4662644d35")

// fppConnectNodeUUID derives nodeID's stable advertised uuid.
func fppConnectNodeUUID(nodeID string) uuid.UUID {
	return uuid.NewSHA1(fppConnectNodeNamespace, []byte(nodeID))
}

// fppConnectView is the read-only view of this node's FPP Connect
// compatibility state that this listener's handlers need. It is defined
// here, against a small interface, rather than directly against
// fppConnectState's own method set, so this seam's tests keep exercising
// the handlers through fakeFPPConnectView (fppconnecthttp_test.go)
// independent of fppConnectState's own construction. fppConnectStateView
// below is the production adapter over the real holder (fppconnectstate.go,
// FC1a).
type fppConnectView interface {
	// ChannelRanges returns the advertised channel range string, or "" for
	// a node with no configured surface.
	ChannelRanges() string

	// Enabled reports the fppconnect.settings enabled flag's current value.
	// False means every route on this listener answers 404 while the
	// socket stays bound, so the operator's next push takes effect with no
	// restart.
	Enabled() bool

	// ActiveShow returns the coordinator-pushed active show's tri-state
	// (fppConnectState.ActiveShow's own shape, ADR-044 decision 5): ever
	// is false only before this node has ever been told an active show at
	// all, in which case known and name are meaningless; once ever is
	// true, known distinguishes an explicit "no active show" (known
	// false) from a named active show (known true, name is its display
	// name). Not read by any route this seam serves; it exists on this
	// interface because FC2's upload binding (ADR-044 decision 8) needs it
	// from the same holder.
	ActiveShow() (name string, known bool, ever bool)

	// ShowNames returns the ShowMesh show names this node currently knows.
	// GET /api/playlists serves exactly this list, as a bare array.
	ShowNames() []string
}

// fppConnectStateView adapts *fppConnectState (fppconnectstate.go, FC1a) to
// fppConnectView. Enabled defaults true when the coordinator has never
// pushed fppconnect.settings (Settings' second return is false): a node
// with a bound listener and no push yet should still be discoverable,
// matching the coordinator's own resolveFPPConnectSettings default
// (ADR-044 decision 5) rather than reporting disabled for a setting that
// was simply never sent.
type fppConnectStateView struct {
	state *fppConnectState
}

func (v fppConnectStateView) ChannelRanges() string { return v.state.ChannelRanges() }

func (v fppConnectStateView) Enabled() bool {
	settings, ok := v.state.Settings()
	if !ok {
		return true
	}
	return settings.Enabled
}

func (v fppConnectStateView) ActiveShow() (name string, known bool, ever bool) {
	return v.state.ActiveShow()
}

func (v fppConnectStateView) ShowNames() []string { return v.state.ShowNames() }

// fppConnectLocalAddrKey is the context key runFPPConnectHTTPListener's
// ConnContext hook stores each accepted connection's real local address
// under. This is deliberately NOT http.LocalAddrContextKey: net/http's own
// Server.Serve populates that key from the *listener's* address (l.Addr()),
// which for a listener bound to ":80" or "0.0.0.0:80" is the wildcard, not
// the specific interface a given request actually arrived on. RES-003's
// multiSyncSystems self-entry needs the latter.
type fppConnectLocalAddrKey struct{}

// fppConnectConnContext is the http.Server.ConnContext hook that makes
// fppConnectLocalAddrKey available to every handler on this listener.
func fppConnectConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, fppConnectLocalAddrKey{}, c.LocalAddr())
}

// fppConnectRequestLocalIP returns the bare IP (no port) of the connection r
// arrived on, or "" when fppConnectConnContext never ran (a request built
// with no real net.Conn behind it, e.g. httptest.NewRecorder).
func fppConnectRequestLocalIP(r *http.Request) string {
	addr, _ := r.Context().Value(fppConnectLocalAddrKey{}).(net.Addr)
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		// addr.String() had no port to split off; it is already bare.
		return addr.String()
	}
	return host
}

// fppConnectSystemInfoResponse is GET /api/system/info's body. Field names
// and casing match what RES-003 section 9.3 and ADR-044 record xLights
// reading; the mix of PascalCase and camelCase keys is FPP's own, not a
// ShowMesh choice, and must not be normalized.
type fppConnectSystemInfoResponse struct {
	UUID         string `json:"uuid"`
	HostName     string `json:"HostName"`
	Version      string `json:"Version"`
	MajorVersion int    `json:"majorVersion"`
	MinorVersion int    `json:"minorVersion"`
	Mode         string `json:"Mode"`
	TypeID       int    `json:"typeId"`
	// ChannelRanges omits the key entirely when empty (omitempty), never
	// serving "channelRanges":"". RES-003 section 9.5: an empty
	// channelRanges string, if xLights read one, would fall back to
	// rendering a full non-sparse FSEQ, so a node with no configured
	// surface must not advertise the key at all.
	ChannelRanges string `json:"channelRanges,omitempty"`
	Platform      string `json:"Platform"`
	Variant       string `json:"Variant"`
}

// fppConnectMultiSyncEntry is the one self-entry GET /api/fppd/multiSyncSystems
// serves. Field names and casing match RES-003 sections 9.2 and 9.7.
type fppConnectMultiSyncEntry struct {
	Address       string `json:"address"`
	HostName      string `json:"hostname"`
	FPPMode       int    `json:"fppMode"`
	FPPModeString string `json:"fppModeString"`
	Version       string `json:"version"`
	MajorVersion  int    `json:"majorVersion"`
	MinorVersion  int    `json:"minorVersion"`
	Type          string `json:"type"`
	TypeID        int    `json:"typeId"`
	UUID          string `json:"uuid"`
	ChannelRanges string `json:"channelRanges,omitempty"`
	Local         bool   `json:"local"`
}

type fppConnectMultiSyncSystemsResponse struct {
	Systems []fppConnectMultiSyncEntry `json:"systems"`
}

// fppConnectPlaylistInfo is GET /api/playlist/{name}'s playlistInfo object.
// The snake_case keys are FPP's own wire shape, not a ShowMesh choice.
type fppConnectPlaylistInfo struct {
	TotalDuration int `json:"total_duration"`
	TotalItems    int `json:"total_items"`
}

// fppConnectPlaylistResponse is GET /api/playlist/{name}'s body for a name
// on the holder's show list. mainPlaylist is always empty here: FC2's
// upload receiver is what populates it, later.
type fppConnectPlaylistResponse struct {
	Name         string                 `json:"name"`
	MainPlaylist []any                  `json:"mainPlaylist"`
	LeadIn       []any                  `json:"leadIn"`
	LeadOut      []any                  `json:"leadOut"`
	PlaylistInfo fppConnectPlaylistInfo `json:"playlistInfo"`
}

// fppConnectServer holds the fixed, per-node values this listener's
// handlers serve, computed once at construction rather than per request.
type fppConnectServer struct {
	view   fppConnectView
	nodeID string
	uuid   string
}

// newFPPConnectHandler builds the complete handler for this node's FPP
// Connect HTTP listener: the four routes ADR-044 decision 1 names for this
// seam (the upload and playlist-write routes are FC2's), a request body
// size cap, and the enabled-flag gate that 404s every route when this
// node's fppconnect.settings.enabled is false. Anything not matching one of
// the four routes gets route's own 404: a short plain-text body, never
// HTML and never a stack trace.
func newFPPConnectHandler(view fppConnectView, nodeID string) http.Handler {
	srv := &fppConnectServer{
		view:   view,
		nodeID: nodeID,
		uuid:   fppConnectNodeUUID(nodeID).String(),
	}

	return fppConnectLimitBody(fppConnectRequireEnabled(view, http.HandlerFunc(srv.route)))
}

// route is this listener's entire dispatch table, deliberately not built on
// http.ServeMux. ServeMux 301-redirects, with an HTML body, any request
// whose path needs cleaning: a "//" anywhere, or a literal ".." segment
// (verified against this Go version: GET //api/system/info and
// GET /api/playlist/../system/info both come back as a 301 to the cleaned
// path with a text/html body), regardless of which patterns are
// registered, since the cleaning check runs before pattern matching. ADR-044
// decision 1 permits only 404 for a request outside the four named routes,
// and this package's own doc comment promises no served content is HTML;
// a redirect breaks both. Matching r.URL.EscapedPath() by hand, and never
// handing a request to anything that performs its own path cleaning, makes
// that redirect path structurally unreachable rather than merely unobserved.
//
// Only GET and HEAD are served. net/http's server already strips a HEAD
// response's body while sending accurate headers (verified: Content-Length
// reflects the real body size, the body itself is empty), so no handler
// below needs its own HEAD case. Every other method is a plain 404: ADR-044
// decision 1 says everything outside the four routes is 404, not the 405 +
// Allow header http.ServeMux would answer with for a wrong method on a
// registered path.
func (s *fppConnectServer) route(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	switch r.URL.EscapedPath() {
	case fppConnectPathSystemInfo:
		s.handleSystemInfo(w, r)
		return
	case fppConnectPathMultiSyncSystems:
		s.handleMultiSyncSystems(w, r)
		return
	case fppConnectPathPlaylists:
		s.handlePlaylists(w, r)
		return
	}

	if name, ok := fppConnectMatchPlaylistPath(r.URL.EscapedPath()); ok {
		s.handlePlaylist(w, r, name)
		return
	}

	http.NotFound(w, r)
}

// fppConnectMatchPlaylistPath reports whether escapedPath is
// fppConnectPlaylistPrefix followed by exactly one further path segment,
// and if so returns that segment decoded. A literal (still-escaped) "/"
// anywhere after the prefix means more than one segment reached this
// listener (the shape GET /api/playlist/../system/info's ".." would take
// once route() no longer had a mux to clean it away), which is never a
// valid playlist name and is refused here rather than partially matched.
// The returned name is NOT yet validated for filesystem safety; see
// fppConnectValidPlaylistName, applied by the caller.
func fppConnectMatchPlaylistPath(escapedPath string) (name string, ok bool) {
	rest, found := strings.CutPrefix(escapedPath, fppConnectPlaylistPrefix)
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	decoded, err := url.PathUnescape(rest)
	if err != nil {
		return "", false
	}
	return decoded, true
}

// fppConnectValidPlaylistName reports whether a decoded playlist name is
// safe to compare against the holder's show list, and later to reuse: FC2's
// upload receiver touches this exact string near the filesystem. Checked on
// the DECODED name, so a percent-encoded traversal attempt (".." as
// "%2e%2e", a separator as "%2f") is caught here even when it matched
// route()'s single-segment shape cleanly.
func fppConnectValidPlaylistName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	if strings.IndexByte(name, 0) != -1 {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// fppConnectRequireEnabled 404s every request when view.Enabled() is false,
// ahead of routing: a disabled listener answers exactly like an unmapped
// path on every one of its own routes, per ADR-044's "enabled false"
// requirement. The socket itself stays bound either way; only the routes'
// behavior changes.
func fppConnectRequireEnabled(view fppConnectView, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !view.Enabled() {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fppConnectLimitBody refuses a request body larger than
// fppConnectMaxBodyBytes, or one whose size cannot be bounded upfront at
// all (a chunked request, ContentLength == -1). None of this listener's
// routes read the body, so MaxBytesReader alone is not the bound it looks
// like: nothing here ever calls r.Body.Read, so a client that declares (or
// streams) more than the cap would otherwise sit un-rejected until
// ReadTimeout rather than being turned away outright. Refused the same way
// an unmapped path is, a plain 404, never by reading first.
func fppConnectLimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 || r.ContentLength > fppConnectMaxBodyBytes {
			http.NotFound(w, r)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, fppConnectMaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *fppConnectServer) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	fppConnectWriteJSON(w, http.StatusOK, fppConnectSystemInfoResponse{
		UUID:          s.uuid,
		HostName:      s.nodeID,
		Version:       fppconnect.AdvertisedVersion,
		MajorVersion:  fppconnect.AdvertisedVersionMajor,
		MinorVersion:  fppconnect.AdvertisedVersionMinor,
		Mode:          fppconnect.AdvertisedMode,
		TypeID:        int(multisync.SystemTypeShowMesh),
		ChannelRanges: s.view.ChannelRanges(),
		Platform:      fppConnectPlatform,
		Variant:       fppConnectPlatform,
	})
}

func (s *fppConnectServer) handleMultiSyncSystems(w http.ResponseWriter, r *http.Request) {
	entry := fppConnectMultiSyncEntry{
		Address: fppConnectRequestLocalIP(r),
		// FPPModeString is fppconnect.AdvertisedMode ("player"), not this
		// listener's own copy of the mode string: both must always name
		// the same protocol value (RES-003 section 10.6), and inlining a
		// second "player" literal here is exactly the kind of copy ADR-044
		// decision 7 and this file's own package doc comment warn against.
		HostName:      s.nodeID,
		FPPMode:       int(multisync.PingModePlayer),
		FPPModeString: fppconnect.AdvertisedMode,
		Version:       fppconnect.AdvertisedVersion,
		MajorVersion:  fppconnect.AdvertisedVersionMajor,
		MinorVersion:  fppconnect.AdvertisedVersionMinor,
		Type:          fppConnectPlatform,
		TypeID:        int(multisync.SystemTypeShowMesh),
		UUID:          s.uuid,
		ChannelRanges: s.view.ChannelRanges(),
		Local:         true,
	}
	fppConnectWriteJSON(w, http.StatusOK, fppConnectMultiSyncSystemsResponse{
		Systems: []fppConnectMultiSyncEntry{entry},
	})
}

func (s *fppConnectServer) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	names := s.view.ShowNames()
	if names == nil {
		// A bare "[]", never "null": RES-003 section 10.6 records that
		// xLights parses this as a list of plain strings and keeps it only
		// on an exact 200, so encoding/json's nil-slice-marshals-to-null
		// default would be read as a parse failure rather than "no shows".
		names = []string{}
	}
	fppConnectWriteJSON(w, http.StatusOK, names)
}

// handlePlaylist serves a name route() has already matched and decoded
// (url.PathUnescape, so '+' stays literal rather than becoming a space:
// that conversion is query-string-only behavior FPP's own receiver does not
// apply either). name is validated here, before the membership check,
// against fppConnectValidPlaylistName: a name a filesystem would read as a
// path (a separator, a NUL byte, or a ".." segment) is refused with the
// same 404 an unknown name gets, never reaching the membership check that
// would otherwise tell an attacker whether it happened to collide with a
// real show name.
func (s *fppConnectServer) handlePlaylist(w http.ResponseWriter, r *http.Request, name string) {
	if !fppConnectValidPlaylistName(name) || !fppConnectContainsShow(s.view.ShowNames(), name) {
		http.NotFound(w, r)
		return
	}
	fppConnectWriteJSON(w, http.StatusOK, fppConnectPlaylistResponse{
		Name:         name,
		MainPlaylist: []any{},
		LeadIn:       []any{},
		LeadOut:      []any{},
		PlaylistInfo: fppConnectPlaylistInfo{TotalDuration: 0, TotalItems: 0},
	})
}

func fppConnectContainsShow(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func fppConnectWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// runFPPConnectHTTPListener binds this render node's FPP Connect HTTP
// compatibility listener and serves it until ctx is done. A bind failure
// never stops the agent (ADR-044's consequence "a node that cannot bind
// the listener still renders, still answers MQTT"): it is recorded on
// status, exactly like runMultiSyncListener's identical bind-failure
// handling in multisync.go, and this function simply returns.
func runFPPConnectHTTPListener(ctx context.Context, listenAddr string, view fppConnectView, nodeID string, status *fppConnectHTTPStatus, logger *slog.Logger) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		reason := fmt.Sprintf("failed to bind fppconnect http listener on %s: %v", listenAddr, err)
		logger.Warn("fppconnect: failed to bind http listener; this node will not appear in xLights' FPP Connect dialog until this is fixed",
			"listen_addr", listenAddr, "error", err)
		status.set(false, reason)
		return
	}

	srv := &http.Server{
		Handler:           newFPPConnectHandler(view, nodeID),
		ReadHeaderTimeout: fppConnectReadHeaderTimeout,
		ReadTimeout:       fppConnectReadTimeout,
		WriteTimeout:      fppConnectWriteTimeout,
		MaxHeaderBytes:    fppConnectMaxHeaderBytes,
		ConnContext:       fppConnectConnContext,
		// net/http already recovers a handler panic per connection and logs
		// it through this *log.Logger rather than letting it reach the
		// agent's own structured logger by default; routing it through
		// logger's handler keeps that recovery visible in the same
		// structured log every other agent subsystem writes to.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		// Without this, net/http answers "OPTIONS * HTTP/1.1" itself, with a
		// 200 and no body, before route() ever runs: its own general
		// handler for that one specific request line, unconditional on
		// which patterns are registered. ADR-044 decision 1 makes
		// everything outside the four named routes 404, so that built-in
		// answer is disabled here the same way route() replaced
		// http.ServeMux's own redirect and 405 behavior in review round 1.
		DisableGeneralOptionsHandler: true,
	}

	// The first status is a real poll of view.Enabled(), not a blind
	// "listening, no reason": a node whose fppconnect.settings.enabled is
	// already false when this listener binds must report that from its
	// very first status, not report healthy for one tick.
	fppConnectPollEnabled(view, status)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	// enabledPoll re-reads view.Enabled() on a short interval so the
	// render report's disabled-by-configuration evidence (finding 3) stays
	// current: the view offers no change hook, and every route already
	// reads Enabled() fresh per request regardless of this ticker, so this
	// only bounds how promptly the STATUS an operator reads catches up.
	enabledPoll := time.NewTicker(fppConnectEnabledPollInterval)
	defer enabledPoll.Stop()

	for {
		select {
		case <-ctx.Done():
			// Ordered ahead of the agent's existing clean-shutdown path
			// (renderOps.Shutdown/sup.Shutdown/shutdownCleanly in
			// agent.go): this fires as soon as ctx ends, and agent.go
			// joins this function's own done-channel before any of those
			// run.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), fppConnectShutdownTimeout)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Warn("fppconnect: http listener did not shut down cleanly", "error", err)
			}
			<-serveErr
			return
		case err := <-serveErr:
			// Serve returns http.ErrServerClosed on an ordinary Shutdown; a
			// different error here is a genuine mid-session listener
			// failure, the same degradation as never having bound at all.
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Warn("fppconnect: http listener stopped unexpectedly", "error", err)
				status.set(false, fmt.Sprintf("fppconnect http listener stopped unexpectedly: %v", err))
			}
			return
		case <-enabledPoll.C:
			fppConnectPollEnabled(view, status)
		}
	}
}

// fppConnectPollEnabled records view's current Enabled() value on status:
// listening stays true either way (the socket is bound in both cases), and
// reason is fppConnectDisabledReason when disabled or "" when enabled,
// matching fppConnectHTTPStatus's own doc comment on what "listening but
// disabled" means. Factored out of runFPPConnectHTTPListener so a test can
// exercise the transition in both directions without depending on a real
// ticker tick.
func fppConnectPollEnabled(view fppConnectView, status *fppConnectHTTPStatus) {
	if view.Enabled() {
		status.set(true, "")
		return
	}
	status.set(true, fppConnectDisabledReason)
}
