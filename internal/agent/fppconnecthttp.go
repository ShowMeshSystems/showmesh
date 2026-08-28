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
	"path/filepath"
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
	// fppConnectReadHeaderTimeout bounds how long a slow or hostile client
	// can hold this listener's goroutines open before it has even finished
	// sending headers.
	fppConnectReadHeaderTimeout = 5 * time.Second

	// fppConnectWriteDeadline bounds the response-write side of every
	// route, applied per request via fppConnectSetWriteDeadline /
	// http.ResponseController, never as the server-wide WriteTimeout
	// (see runFPPConnectHTTPListener's doc comment on why that is 0 now).
	//
	// Review round 3 finding 1, CONFIRMED and blocking: net/http arms a
	// nonzero server-wide WriteTimeout's deadline on the connection as
	// soon as headers are read, before the handler (and its body reads)
	// ever runs, and that deadline governs the whole rest of the
	// request-response cycle on that connection, not merely the final
	// Write() calls the "WriteTimeout" name suggests. With
	// fppConnectFileReadDeadline (10 minutes) far longer than the old
	// fixed 10s WriteTimeout, a real chunk that took more than 10s to
	// arrive would have that armed deadline expire mid-read: the client
	// got an EOF instead of a response, and xLights retried the same
	// chunk into this listener's gap branch, killing the upload with a
	// 409 it never sent a byte wrong to deserve.
	//
	// The fix is to never have an active write deadline while a body read
	// is still possibly in flight: every route sets this deadline only
	// once it is actually about to write its response (fppconnectupload.
	// go's handleFilePatch sets it after WriteChunk returns, never
	// before), so its window can never overlap a slow chunk transfer
	// regardless of how the two axes interact underneath net/http.
	fppConnectWriteDeadline = 10 * time.Second

	// fppConnectDiscoveryReadDeadline bounds request reading (headers
	// plus body) on every route except the file PATCH route: the four
	// fixed discovery routes and the playlist routes never wait on
	// anything slower than reading the holder's in-memory state or a
	// small POST body, so this stays short. Applied per request via
	// fppConnectSetReadDeadline / http.ResponseController, tighter than
	// the server-wide ReadTimeout floor below.
	fppConnectDiscoveryReadDeadline = 10 * time.Second

	// fppConnectFileReadDeadline bounds request reading on the file PATCH
	// route alone: review round 1 finding 4 found the old server-wide
	// ReadTimeout of 10s covered a chunk's body too, so a 16 MiB xLights
	// chunk (RES-003 section 9.4) on a slow link could time out mid-
	// transfer. Generous rather than idle-based (a single deadline set
	// once, not reset per read): simpler to reason about, and this is a
	// compatibility shim for one intended client on an isolated show
	// network, not a public upload endpoint that needs to bound a
	// deliberately slow-drip client's total connection time tightly.
	fppConnectFileReadDeadline = 10 * time.Minute

	// fppConnectServerReadTimeoutFloor is the http.Server-wide ReadTimeout
	// (review round 3 finding 8, PLAUSIBLE): kept as a generous floor
	// under the tighter per-route deadlines above, not 0. If a future
	// ResponseWriter wrapper's http.ResponseController does not support
	// SetReadDeadline (Go's own documented possibility), fppConnectSetReadDeadline's
	// failure is only logged, and request body reads would otherwise be
	// entirely unbounded on that connection. Well above
	// fppConnectFileReadDeadline, so it is never the deadline that
	// actually fires in normal operation; it exists only as the backstop
	// under a mechanism that has already failed once.
	//
	// The write side has no equivalent floor (review round 4 finding 6),
	// an accepted asymmetry, not an oversight. WriteTimeout is 0 (review
	// round 3 finding 1) because a nonzero server-wide write timeout is
	// exactly the bug that finding removed, and there is no write-side
	// failure mode comparable to an unbounded body READ: a hostile or
	// merely slow client can choose never to finish SENDING, which is
	// what this floor guards against, but this listener never waits on
	// further client input to finish producing a response, so a write has
	// nothing analogous to wait on indefinitely. A write-side
	// SetWriteDeadline failure is logged by fppConnectSetWriteDeadline the
	// same way a read-deadline failure is logged here; after that, the
	// write still bounds itself naturally, since a truly dead connection
	// is torn down by the kernel's own TCP retransmission timeout
	// regardless of any deadline this listener sets.
	fppConnectServerReadTimeoutFloor = 15 * time.Minute

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

	// fppConnectFilePrefix is FC2's chunked-upload route's fixed prefix;
	// route() matches anything after it as the (still escaped) directory
	// name, validated against fppConnectAllowedDirs in fppconnectupload.go.
	fppConnectFilePrefix = "/api/file/"
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
	// name). FC2's upload binding (ADR-044 decision 8) is this method's
	// only reader.
	ActiveShow() (name string, known bool, ever bool)

	// ShowNames returns the ShowMesh show names this node currently knows.
	// GET /api/playlists serves exactly this list, as a bare array. FC2's
	// upload binding also reads it to resolve (or find ambiguous, or find
	// unknown) a posted playlist name: a name occurring more than once in
	// this slice is two distinct shows sharing one display name (ADR-044
	// decision 8's ambiguous case), since this interface carries no show
	// id, only names.
	ShowNames() []string

	// ShowID resolves name to its show object id, when name currently
	// names EXACTLY ONE held show (FC3, ADR-028 decision 8): ok is false
	// when name matches zero shows or more than one. FC2's upload binding
	// is this method's only reader, always after ShowNames' own duplicate
	// count has already decided a name is unambiguous; POST /api/v1/assets
	// requires this id, never the display name, as its `show` field.
	ShowID(name string) (id string, ok bool)

	// MaxFileBytes returns the current fppconnect.settings per-file byte
	// cap (ADR-044 decision 4's second bound). Read fresh per upload
	// chunk, matching Enabled's fresh-per-request read.
	MaxFileBytes() int64

	// MaxAssetDirBytes returns the current fppconnect.settings total
	// asset-directory byte cap (ADR-044 decision 4's third bound). Read
	// fresh per upload chunk, matching Enabled's fresh-per-request read.
	MaxAssetDirBytes() int64
}

// fppConnectDefaultMaxFileBytes and fppConnectDefaultMaxAssetDirBytes
// mirror IDENTIFIER-REGISTER.md's stated fppconnect.settings defaults
// (2 GiB, 20 GiB; see internal/coordinator/config.
// FPPConnectSettingsDefaultPayload) so fppConnectStateView's byte caps
// match what the coordinator itself would report before any push has ever
// landed. Not a builder choice made twice: this must not ship a different
// default than the coordinator's own owner-reviewed one, so it is copied
// rather than reinvented.
const (
	fppConnectDefaultMaxFileBytes     = 2 * 1024 * 1024 * 1024
	fppConnectDefaultMaxAssetDirBytes = 20 * 1024 * 1024 * 1024
)

// fppConnectStateView adapts *fppConnectState (fppconnectstate.go, FC1a) to
// fppConnectView. Enabled defaults true when the coordinator has never
// pushed fppconnect.settings (Settings' second return is false): a node
// with a bound listener and no push yet should still be discoverable,
// matching the coordinator's own resolveFPPConnectSettings default
// (ADR-044 decision 5) rather than reporting disabled for a setting that
// was simply never sent. The two byte caps take the same never-pushed
// fallback, using fppConnectDefaultMaxFileBytes/
// fppConnectDefaultMaxAssetDirBytes so FC2's bounds are real even before a
// push ever lands. Construct through newFPPConnectStateView, not this
// struct literal directly: a nil state would otherwise reach every method
// below and panic unpredictably on whichever route is hit first, instead
// of failing clearly at construction.
type fppConnectStateView struct {
	state *fppConnectState
}

// newFPPConnectStateView builds a fppConnectStateView over state, panicking
// if state is nil. A nil holder here is a construction bug, never a
// runtime condition this listener should degrade through: agent.go always
// builds the real *fppConnectState before starting this listener, so the
// only way state is nil is a future call site wiring an empty
// fppConnectStateView{} literal (or an equivalent nil) into a handler,
// which would otherwise fail unpredictably per-route instead of loudly at
// startup.
func newFPPConnectStateView(state *fppConnectState) fppConnectStateView {
	if state == nil {
		panic("fppconnect: newFPPConnectStateView called with a nil *fppConnectState")
	}
	return fppConnectStateView{state: state}
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

func (v fppConnectStateView) ShowID(name string) (string, bool) { return v.state.ShowID(name) }

func (v fppConnectStateView) MaxFileBytes() int64 {
	settings, ok := v.state.Settings()
	if !ok {
		return fppConnectDefaultMaxFileBytes
	}
	return settings.MaxFileBytes
}

func (v fppConnectStateView) MaxAssetDirBytes() int64 {
	settings, ok := v.state.Settings()
	if !ok {
		return fppConnectDefaultMaxAssetDirBytes
	}
	return settings.MaxAssetDirBytes
}

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

// fppConnectPlaylistEntry is one mainPlaylist element, in the "without
// media" shape RES-003 section 10.6 documents:
//
//	{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"<name>","duration":<float seconds>}
//
// FC2's held store (fppconnectheld.go) always emits this shape rather than
// the "with media" variant: it binds a sequence file and a media file as
// two independent held records sharing one show, not as one paired entry,
// so there is no mediaName to carry on any single entry it can construct.
// Duration is always 0: this seam never parses FSEQ or media duration:
// duration is not read back by xLights' own read-modify-write (RES-003
// section 10.6), only sequenceName presence is, so an honest 0 is
// sufficient for the round trip this exists to support.
type fppConnectPlaylistEntry struct {
	Type         string  `json:"type"`
	Enabled      int     `json:"enabled"`
	PlayOnce     int     `json:"playOnce"`
	SequenceName string  `json:"sequenceName"`
	Duration     float64 `json:"duration"`
}

// fppConnectPlaylistResponse is GET /api/playlist/{name}'s body for a name
// on the holder's show list. mainPlaylist is populated from the held
// store's current bindings for name (fppConnectHeldStore.MainPlaylistFor),
// so xLights' own read-modify-write round-trips (RES-003 section 10.6).
type fppConnectPlaylistResponse struct {
	Name         string                    `json:"name"`
	MainPlaylist []fppConnectPlaylistEntry `json:"mainPlaylist"`
	LeadIn       []any                     `json:"leadIn"`
	LeadOut      []any                     `json:"leadOut"`
	PlaylistInfo fppConnectPlaylistInfo    `json:"playlistInfo"`
}

// fppConnectServer holds the fixed, per-node values this listener's
// handlers serve, computed once at construction rather than per request.
type fppConnectServer struct {
	view   fppConnectView
	nodeID string
	uuid   string
	held   *fppConnectHeldStore
	now    func() time.Time
	logger *slog.Logger
}

// newFPPConnectHandler builds the complete handler for this node's FPP
// Connect HTTP listener: the six routes ADR-044 decision 1 names for this
// seam (FC1's four discovery routes plus FC2's chunked upload and
// playlist-write routes), the enabled-flag gate that 404s every route when
// this node's fppconnect.settings.enabled is false, and a request body
// size cap on FC1's four fixed routes (FC2's upload and playlist-POST
// routes bound their own, much larger, bodies themselves; see route's own
// doc comment). Anything not matching one of the six routes gets route's
// own 404: a short plain-text body, never HTML and never a stack trace.
// held is FC2's upload/binding state; now is this server's clock,
// threaded through rather than read from time.Now directly so a test can
// control it.
func newFPPConnectHandler(view fppConnectView, nodeID string, held *fppConnectHeldStore, now func() time.Time, logger *slog.Logger) http.Handler {
	srv := &fppConnectServer{
		view:   view,
		nodeID: nodeID,
		uuid:   fppConnectNodeUUID(nodeID).String(),
		held:   held,
		now:    now,
		logger: logger,
	}

	return fppConnectRequireEnabled(view, http.HandlerFunc(srv.route), logger)
}

// route is this listener's entire dispatch table, deliberately not built on
// http.ServeMux. ServeMux 301-redirects, with an HTML body, any request
// whose path needs cleaning: a "//" anywhere, or a literal ".." segment
// (verified against this Go version: GET //api/system/info and
// GET /api/playlist/../system/info both come back as a 301 to the cleaned
// path with a text/html body), regardless of which patterns are
// registered, since the cleaning check runs before pattern matching. ADR-044
// decision 1 permits only 404 for a request outside the six named routes,
// and this package's own doc comment promises no served content is HTML;
// a redirect breaks both. Matching r.URL.EscapedPath() by hand, and never
// handing a request to anything that performs its own path cleaning, makes
// that redirect path structurally unreachable rather than merely unobserved.
//
// The file and playlist routes are matched first, ahead of the fixed
// four-route switch, and each dispatches on method itself
// (handleFileRoute, handlePlaylistRoute): they carry PATCH and POST, which
// the fixed routes never do, and the file route in particular carries a
// chunk body far larger than fppConnectMaxBodyBytes, so it must never pass
// through fppConnectLimitBody's small cap. Only the fixed four-route
// switch is wrapped in that cap, applied here rather than in
// newFPPConnectHandler so the file and playlist routes bound their own
// bodies instead (fppConnectMaxChunkBytes, fppConnectMaxPlaylistBodyBytes).
//
// The fixed routes serve only GET and HEAD. net/http's server already
// strips a HEAD response's body while sending accurate headers (verified:
// Content-Length reflects the real body size, the body itself is empty),
// so no handler below needs its own HEAD case. Every other method on a
// fixed route is a plain 404: ADR-044 decision 1 says everything outside
// the six routes is 404, not the 405 + Allow header http.ServeMux would
// answer with for a wrong method on a registered path.
func (s *fppConnectServer) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()

	if dir, ok := fppConnectMatchFilePath(path); ok {
		s.handleFileRoute(w, r, dir)
		return
	}
	if name, ok := fppConnectMatchPlaylistPath(path); ok {
		s.handlePlaylistRoute(w, r, name)
		return
	}

	fppConnectLimitBody(http.HandlerFunc(s.routeFixed), s.logger).ServeHTTP(w, r)
}

// fppConnectSetReadDeadline sets w's underlying connection's read deadline
// to d from now, via http.ResponseController (Go 1.20+): the per-route,
// per-request deadline that governs body reading, tighter than the
// server-wide fppConnectServerReadTimeoutFloor this listener still sets
// as a backstop (runFPPConnectHTTPListener's doc comment). A failure is
// logged rather than treated as fatal: SetReadDeadline can fail on a
// ResponseWriter that does not support it (Go's own documented
// possibility, e.g. some non-standard Handler wrapping), and
// fppConnectServerReadTimeoutFloor still bounds the read in that case.
func fppConnectSetReadDeadline(w http.ResponseWriter, d time.Duration, logger *slog.Logger) {
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(d)); err != nil {
		logger.Warn("fppconnect: failed to set a per-route read deadline", "error", err)
	}
}

// fppConnectSetWriteDeadline sets w's underlying connection's write
// deadline to d from now, via http.ResponseController. The per-route
// replacement for the server-wide WriteTimeout this listener no longer
// sets (review round 3 finding 1; see fppConnectWriteDeadline's own doc
// comment on why that was the actual bug, and runFPPConnectHTTPListener's
// doc comment on the server construction). Every caller must call this
// only once it is actually about to write a response, never before a
// body read that might still be in flight. A failure is logged, not
// fatal, matching fppConnectSetReadDeadline's identical reasoning.
func fppConnectSetWriteDeadline(w http.ResponseWriter, d time.Duration, logger *slog.Logger) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
		logger.Warn("fppconnect: failed to set a per-route write deadline", "error", err)
	}
}

// routeFixed is FC1's original four-route dispatch table: GET/HEAD only,
// wrapped by route() in fppConnectLimitBody's small body cap.
func (s *fppConnectServer) routeFixed(w http.ResponseWriter, r *http.Request) {
	fppConnectSetReadDeadline(w, fppConnectDiscoveryReadDeadline, s.logger)
	// Safe to set up front on this route: every branch below responds
	// immediately with no further body read in between, unlike the file
	// PATCH route (fppconnectupload.go's handleFilePatch).
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	switch r.URL.EscapedPath() {
	case fppConnectPathSystemInfo:
		s.handleSystemInfo(w, r)
	case fppConnectPathMultiSyncSystems:
		s.handleMultiSyncSystems(w, r)
	case fppConnectPathPlaylists:
		s.handlePlaylists(w, r)
	default:
		http.NotFound(w, r)
	}
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

// fppConnectMatchFilePath reports whether escapedPath is
// fppConnectFilePrefix followed by anything at all, and if so returns that
// remainder decoded as dir. Unlike fppConnectMatchPlaylistPath, a literal
// "/" in the remainder does NOT fail the match here: ADR-044 decision 4's
// directory allowlist check (fppConnectAllowedDirs, in
// fppconnectupload.go) must itself refuse "../sequences", an empty
// segment, and a name containing "/" with a 403 naming the reason, per the
// seam spec's explicit list of what that refusal covers. Requiring a
// single segment at the routing layer would instead send those shapes to
// this listener's generic 404, which is a different, less informative
// outcome than the one specified. A decode failure (malformed percent-
// encoding) is treated as no match at all, falling through to the generic
// 404, mirroring fppConnectMatchPlaylistPath's identical choice.
func fppConnectMatchFilePath(escapedPath string) (dir string, ok bool) {
	rest, found := strings.CutPrefix(escapedPath, fppConnectFilePrefix)
	if !found {
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
	// Review round 1 finding 9: "." passed every check above (it is not
	// "..", contains no separator) and then failed FC2's rename into the
	// held area with a 500, rather than being refused up front with the
	// 403 ADR-044 decision 4's bounds specify. filepath.Clean also
	// catches "..", already refused above, so this is belt-and-braces
	// against any other name that cleans down to either.
	if cleaned := filepath.Clean(name); cleaned == "." || cleaned == ".." {
		return false
	}
	return true
}

// fppConnectRequireEnabled 404s every request when view.Enabled() is false,
// ahead of routing: a disabled listener answers exactly like an unmapped
// path on every one of its own routes, per ADR-044's "enabled false"
// requirement. The socket itself stays bound either way; only the routes'
// behavior changes. This 404 sets its own write deadline before writing
// (review round 4 finding 3): it runs ahead of every route's own
// deadline-setting, including the file PATCH route's, so with the
// server-wide WriteTimeout gone (review round 3 finding 1) this response
// would otherwise have none at all. Safe to set immediately: nothing above
// this check has read any part of the request.
func fppConnectRequireEnabled(view fppConnectView, next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !view.Enabled() {
			fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, logger)
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
// an unmapped path is, a plain 404, never by reading first, and (review
// round 4 finding 3) with its own write deadline set first: this refusal
// runs ahead of routeFixed's own deadline-setting, and nothing above it
// has read any part of the request either.
func fppConnectLimitBody(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 0 || r.ContentLength > fppConnectMaxBodyBytes {
			fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, logger)
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

// handlePlaylistRoute is the fourth route's method dispatch: GET/HEAD
// serves the read-modify-write's read half (handlePlaylist); POST is FC2's
// binding write (handlePlaylistPost, fppconnectupload.go). Any other
// method is this listener's ordinary 404, matching every other route's
// "wrong method is 404, not 405" rule.
//
// The write deadline is set inside each of the three branches below, not
// once up front here (review round 4 finding 4): an earlier version set
// it here unconditionally, which for POST ran before handlePlaylistPost
// had read its own body, exactly the "never before a body read that
// might still be in flight" ordering fppConnectSetWriteDeadline's own doc
// comment forbids. GET/HEAD reads no body, so handlePlaylist is free to
// set it immediately.
func (s *fppConnectServer) handlePlaylistRoute(w http.ResponseWriter, r *http.Request, name string) {
	fppConnectSetReadDeadline(w, fppConnectDiscoveryReadDeadline, s.logger)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.handlePlaylist(w, r, name)
	case http.MethodPost:
		s.handlePlaylistPost(w, r, name)
	default:
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		http.NotFound(w, r)
	}
}

// handlePlaylist serves a name route() has already matched and decoded
// (url.PathUnescape, so '+' stays literal rather than becoming a space:
// that conversion is query-string-only behavior FPP's own receiver does not
// apply either). name is validated here, before the membership check,
// against fppConnectValidPlaylistName: a name a filesystem would read as a
// path (a separator, a NUL byte, or a ".." segment) is refused with the
// same 404 an unknown name gets, never reaching the membership check that
// would otherwise tell an attacker whether it happened to collide with a
// real show name. mainPlaylist is populated from s.held's current bindings
// for name (FC2), so xLights' own read-modify-write round-trips (RES-003
// section 10.6): a name it already posted comes back present, so its
// client-side merge never re-appends it.
func (s *fppConnectServer) handlePlaylist(w http.ResponseWriter, r *http.Request, name string) {
	if !fppConnectValidPlaylistName(name) || !fppConnectContainsShow(s.view.ShowNames(), name) {
		fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
		http.NotFound(w, r)
		return
	}
	entries := s.held.MainPlaylistFor(name)
	if entries == nil {
		entries = []fppConnectPlaylistEntry{}
	}
	fppConnectSetWriteDeadline(w, fppConnectWriteDeadline, s.logger)
	fppConnectWriteJSON(w, http.StatusOK, fppConnectPlaylistResponse{
		Name:         name,
		MainPlaylist: entries,
		LeadIn:       []any{},
		LeadOut:      []any{},
		PlaylistInfo: fppConnectPlaylistInfo{TotalDuration: 0, TotalItems: len(entries)},
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

// newFPPConnectProductionServer builds the *http.Server this listener runs
// in production, with every deadline and safety field this seam's review
// rounds have settled on. runFPPConnectHTTPListener and
// TestFPPConnectUploadDrippedOverTenSeconds (review round 4 finding 6)
// both call this one constructor, not each their own copy: a future
// regression that re-adds a server-wide WriteTimeout (review round 3
// finding 1, the actual bug that test exists to catch) would otherwise
// only break a hand-copied literal nobody was still keeping in sync,
// rather than the test itself.
func newFPPConnectProductionServer(view fppConnectView, nodeID string, held *fppConnectHeldStore, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           newFPPConnectHandler(view, nodeID, held, time.Now, logger),
		ReadHeaderTimeout: fppConnectReadHeaderTimeout,
		// ReadTimeout is fppConnectServerReadTimeoutFloor (15 minutes),
		// not 0 (review round 3 finding 8): a generous floor under the
		// tighter per-route deadlines every handler sets itself
		// (fppConnectSetReadDeadline: fppConnectDiscoveryReadDeadline for
		// the four discovery routes and the playlist routes,
		// fppConnectFileReadDeadline for the file PATCH route), so a
		// ResponseWriter whose http.ResponseController does not support
		// SetReadDeadline never leaves a body read entirely unbounded.
		// ReadHeaderTimeout above still bounds a client that never
		// finishes sending headers at all. See this constant's own doc
		// comment for why the write side below has no equivalent floor
		// (review round 4 finding 6).
		ReadTimeout: fppConnectServerReadTimeoutFloor,
		// WriteTimeout is 0 (review round 3 finding 1, CONFIRMED and
		// blocking): net/http arms a nonzero WriteTimeout's deadline on
		// the connection as soon as headers are read, before the handler
		// or its body reads ever run, and that deadline covers the whole
		// rest of the request-response cycle on that connection, not
		// merely the final Write() calls. With fppConnectFileReadDeadline
		// (10 minutes) far longer than the old fixed 10s WriteTimeout, a
		// real chunk that took more than 10s to arrive had that armed
		// deadline expire mid-read: the client got an EOF instead of a
		// response, and xLights retried the same chunk into this
		// listener's gap branch, killing the upload with a 409 it never
		// earned. Every route now sets its own write deadline instead
		// (fppConnectSetWriteDeadline), only once it is actually about to
		// write a response, so that window can never overlap a body read
		// still in flight.
		WriteTimeout:   0,
		MaxHeaderBytes: fppConnectMaxHeaderBytes,
		ConnContext:    fppConnectConnContext,
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
}

// runFPPConnectHTTPListener binds this render node's FPP Connect HTTP
// compatibility listener and serves it until ctx is done. A bind failure
// never stops the agent (ADR-044's consequence "a node that cannot bind
// the listener still renders, still answers MQTT"): it is recorded on
// status, exactly like runMultiSyncListener's identical bind-failure
// handling in multisync.go, and this function simply returns.
func runFPPConnectHTTPListener(ctx context.Context, listenAddr string, view fppConnectView, nodeID string, held *fppConnectHeldStore, status *fppConnectHTTPStatus, logger *slog.Logger) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		reason := fmt.Sprintf("failed to bind fppconnect http listener on %s: %v", listenAddr, err)
		logger.Warn("fppconnect: failed to bind http listener; this node will not appear in xLights' FPP Connect dialog until this is fixed",
			"listen_addr", listenAddr, "error", err)
		status.set(false, reason)
		return
	}

	srv := newFPPConnectProductionServer(view, nodeID, held, logger)

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
