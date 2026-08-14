package fppcommand

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a single command request, via a context deadline
// applied per request (never http.Client.Timeout — see Options.HTTPClient
// for why, mirroring the identical reasoning
// internal/coordinator/collector/fpp's own Options.RequestTimeout gives,
// independently arrived at here rather than shared with that package).
// SHOWMESH HYPOTHESIS, NOT MEASURED: RES-009 owns real latency evidence.
// Chosen to be comfortably longer than a healthy FPP's LAN response time
// for a command endpoint, which this project has not benchmarked any
// differently from a status endpoint.
const DefaultTimeout = 5 * time.Second

// Options configures a [Client]. Every field left at its zero value is
// replaced by its documented default in [New].
type Options struct {
	// HTTPClient is the client this Client issues requests with. Left nil,
	// New builds a private one. Whatever is supplied — nil or not — New
	// takes a shallow copy and forces CheckRedirect to refuse every
	// redirect; see [New]'s doc comment for why this package does not
	// trust a caller-supplied client's own CheckRedirect any more than
	// internal/coordinator/collector/fpp trusts one, and does not share
	// that package's implementation to prove it independently (this
	// package's own doc comment).
	HTTPClient *http.Client

	// Timeout bounds a single request. See [DefaultTimeout].
	Timeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	// Force CheckRedirect on a private, shallow copy — never a mutation of
	// a caller's original *http.Client, and never conditional on whether
	// CheckRedirect was already set. A 302 from this instance's own
	// /api/command/... response would, if followed, dispatch a SECOND,
	// FPP-invoked command on whatever host and path the Location header
	// names — a sharper version of the collector's own confused-deputy
	// risk, since what would be walked onto an unnamed host here is not a
	// status read but a command.
	guarded := *o.HTTPClient
	guarded.CheckRedirect = refuseRedirects
	o.HTTPClient = &guarded
	return o
}

// refuseRedirects is this package's own copy of the identical policy
// internal/coordinator/collector/fpp's fpp.go carries under the same
// name: return [http.ErrUseLastResponse] so Client.Do stops after the
// first hop and hands back the 3xx itself, with its Location header
// intact but never dereferenced. Written independently rather than
// imported from that package — see this package's doc comment for why a
// shared implementation would undermine the exact property
// TestPackageNeverImportsFPPCommand exists to prove: two
// packages that agree today because they share code are not proven to
// still agree once one of them changes, only asserted to.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Client dispatches FPP's own native lifecycle commands against ONE
// configured FPP instance's base URL, over POST at /api/command with a
// JSON body — see [Client.Invoke] and package doc for why this is POST
// rather than the GET-positional-path form
// internal/coordinator/collector/fpp's own doc comment names as the
// reason that package forces CheckRedirect; this package forces the
// identical guard on its own POST for the identical reason, a followed
// redirect dispatching a second, FPP-invoked command. A Client is built
// fresh per dispatch by internal/coordinator/api (commands are rare;
// there is no poll loop here to amortize a shared connection pool
// against), so it carries no mutable state beyond its own configuration.
type Client struct {
	baseURL string
	client  *http.Client
	timeout time.Duration
}

// New constructs a Client targeting baseURL, which must be an absolute
// http or https URL with a host and no userinfo, path beyond a bare
// trailing slash, query, or fragment — the identical rule
// internal/coordinator/collector/fpp.New enforces on the same configured
// endpoint value, reproduced here rather than imported (this package's
// own doc comment) so a Client is safe to construct directly, including
// in tests, without depending on that package's validation having already
// run. In production both packages are handed the exact same configured
// endpoint string for the same FPP instance, and both must agree it is
// well-formed independently, not because one delegates to the other.
func New(baseURL string, opts Options) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("fppcommand: invalid URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fppcommand: URL %q must use http or https", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("fppcommand: URL %q must include a host", baseURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("fppcommand: URL must not include userinfo/credentials")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a path", baseURL)
	}
	if parsed.RawQuery != "" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a query", baseURL)
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("fppcommand: URL %q must not include a fragment", baseURL)
	}

	o := opts.withDefaults()
	return &Client{
		baseURL: strings.TrimSuffix(parsed.String(), "/"),
		client:  o.HTTPClient,
		timeout: o.Timeout,
	}, nil
}

// Outcome is FPP's own HTTP-level response to one dispatched command —
// NOT confirmation that the command took effect. ADR-003: a 200 here is
// never success on its own, and docs/bench/fpp-command-vocabulary.md
// section 2 measured exactly how little a 200 means: FPP constructs its
// success body unconditionally, without consulting whether the command
// actually did anything. Captured live: "Start Playlist/no-such-playlist"
// returned 200 "Playlist Starting" while the host stayed idle, and
// "Pause Playlist", "Resume Playlist", "Next Playlist Item", and "Prev
// Playlist Item" all returned 200 with cheerful, specific-sounding bodies
// ("Playlist Paused", "Playlist Restarted", "Next Item Playing", "Prev
// Item Playing") while idle and changing nothing. internal/coordinator/api
// confirms by evidence against the collector separately, on evidence that
// post-dates dispatch, and this type carries nothing that could be
// mistaken for that confirmation.
type Outcome struct {
	// StatusCode is FPP's own response status. A 2xx means FPP's command
	// dispatcher ran the command synchronously (FPP's command endpoint
	// does not queue or defer) — it does NOT mean the command had any
	// effect; see the type's own doc comment. It is Invoke's caller's
	// decision what a non-2xx means for the command's own lifecycle, not
	// this package's.
	StatusCode int

	// Body is FPP's raw response body, bounded by maxResponseBytes. FPP's
	// command endpoint answers with a short human-readable string (e.g.
	// "Playlist Starting", "Stopped", "Volume Set", "Next Item Playing"),
	// never structured JSON, so this is carried as text for the caller to
	// log or surface verbatim rather than parsed here.
	Body string
}

// maxResponseBytes bounds how much of FPP's command response body is
// read. SHOWMESH HYPOTHESIS: FPP's command responses are a handful of
// bytes (e.g. "Stopped"); this is generous headroom mirroring
// internal/coordinator/collector/fpp's own DefaultMaxResponseBytes
// reasoning, independently sized smaller here because a command response
// is not a status document.
const maxResponseBytes = 64 << 10 // 64 KiB

// commandRequest is the exact JSON shape FPP's own POST /api/command
// endpoint expects. It is also the shape fppd normalizes every command
// to internally — GET or POST alike — before republishing it to its own
// MQTT command/run topic (docs/bench/fpp-command-vocabulary.md section
// 1.2), so this is FPP's own canonical representation, not a translation
// layer this package invented.
//
// Args is a plain []string with no ",omitempty": encoding/json marshals
// a nil slice as JSON null, and Invoke always substitutes []string{}
// before marshaling for exactly that reason — see [Client.Invoke]. Never
// add omitempty here; it would reintroduce the absent-key case section
// 1.4 measured FPP rejecting identically to null for a command with
// required arguments.
type commandRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Invoke issues POST {baseURL}/api/command with Content-Type
// application/json and a body of {"command": name, "args": args}, and
// returns FPP's raw [Outcome]. This is a deliberate change from the
// GET-positional-path form Step 7 used:
// docs/bench/fpp-command-vocabulary.md section 1.3 proved FPP's own
// Apache configuration rejects a percent-encoded "/" in a GET path
// segment before fppd ever sees the request (AllowEncodedSlashes is
// off), so a GET-encoded argument value containing "/" — a media
// filename under a subdirectory, a script argument — is categorically
// unreachable through GET. POST's JSON body has no argument value it
// cannot express, and section 1.2 confirms it is FPP's own canonical
// internal representation, not a translation layer this package
// invented.
//
// args is always encoded as a JSON array, never as an absent key and
// never as null, even when nil or empty — see [commandRequest]. Section
// 1.4 measured an absent args key, "args":null, and "args":[] all
// rejected identically by FPP (500 "Not found") for a command with
// required arguments; only "args":[] is correct for a zero-argument
// command, so this package never emits either of the other two.
//
// A 2xx here means only that FPP's command dispatcher ran — see
// [Outcome]'s doc comment; it is never confirmation that anything
// changed. An unknown command name surfaces as 500 "No Command: X" on
// this POST form, NOT 404 — 404 is the GET-path form's error shape
// (section 1.6); do not assume a 404 means "unknown command" against
// this client. Wrong arity is 500 "Not found" on both forms.
//
// Every non-2xx response, including a 3xx this Client's own client was
// built to never follow (see [refuseRedirects]), is returned as a
// non-nil error wrapping [httpStatusError] — never a silent retry, never
// a fabricated success. A transport-level failure (dial error, timeout)
// is likewise a non-nil error with no Outcome.
func (c *Client) Invoke(ctx context.Context, name string, args []string) (Outcome, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if args == nil {
		args = []string{}
	}
	payload, err := json.Marshal(commandRequest{Command: name, Args: args})
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: encoding request for %q: %w", name, err)
	}

	target := c.baseURL + "/api/command"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: dispatching %q: %w", name, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Outcome{}, fmt.Errorf("fppcommand: reading response to %q: %w", name, err)
	}
	truncated := int64(len(body)) > maxResponseBytes
	if truncated {
		body = body[:maxResponseBytes]
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Outcome{StatusCode: resp.StatusCode, Body: string(body)},
			fmt.Errorf("fppcommand: dispatching %q: %w", name, &httpStatusError{StatusCode: resp.StatusCode})
	}
	return Outcome{StatusCode: resp.StatusCode, Body: string(body)}, nil
}

// StopPlaylist dispatches FPP's own "Stop Now" command: the native,
// zero-argument command that stops playback immediately. See this
// package's report for how that name was confirmed against a real, bench
// fppd — FPP's command list carries no command literally named "Stop
// Playlist" (BUILD-PLAN's own descriptive name for this step's primitive
// command); "Stop Now" is the zero-argument member of FPP's Stop family
// ("Stop Gracefully" requires an "afterLoop" argument, so it is not the
// no-target-parameter command BUILD-PLAN's decision names) confirmed live
// to transition FPP's status_name from "playing" directly to "idle".
func (c *Client) StopPlaylist(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Stop Now", nil)
}

// StartPlaylist dispatches FPP's "Start Playlist" command with exactly
// three positional arguments: [name, repeat, ifNotRunning], encoded as
// [name, encodeBool(repeat), encodeBool(ifNotRunning)] per
// docs/bench/fpp-command-vocabulary.md section 4's authoritative table.
//
// FPP's fourth argument, scheduleProtected, is deliberately NOT sent, so
// FPP's own default applies. scheduleProtected asks FPP's own scheduler
// not to override the playlist ShowMesh started (capture section 4);
// ADR-001 makes FPP the authoritative scheduler, so sending it would be
// ShowMesh overriding FPP's own schedule through a command argument —
// exactly the constraint ADR-001 exists to prevent, arrived at sideways.
//
// name is validated with [ValidatePlaylistName] before dispatch; a
// validation failure returns before any request is sent. Capture section
// 3.2 recorded two behaviors this method does NOT attempt to prevent,
// because they are FPP's own semantics, not this package's to police:
// Start Playlist always replaces whatever playlist is currently running,
// and ifNotRunning does not mean "only start if nothing is running" — it
// means "only start if the requested playlist is not already the one
// running." Refusing to start against a busy host is
// internal/coordinator/api's ifBusy decision (capture section 5), not
// this package's.
func (c *Client) StartPlaylist(ctx context.Context, name string, repeat bool, ifNotRunning bool) (Outcome, error) {
	if err := ValidatePlaylistName(name); err != nil {
		return Outcome{}, err
	}
	return c.Invoke(ctx, "Start Playlist", []string{name, encodeBool(repeat), encodeBool(ifNotRunning)})
}

// StopPlaylistGracefully dispatches FPP's "Stop Gracefully" command with
// one argument, afterLoop, encoded via [encodeBool]. Capture section 3.3
// measured this command's terminal state as bounded by show content, not
// by any deadline ShowMesh can choose — a 120-second pause item held
// "stopping gracefully" indefinitely, cleared only by a subsequent Stop
// Now — so a confirmation predicate built on this method's Outcome must
// never wait for status to reach idle; it confirms on FPP having entered
// a stop state at all. See capture section 4.
func (c *Client) StopPlaylistGracefully(ctx context.Context, afterLoop bool) (Outcome, error) {
	return c.Invoke(ctx, "Stop Gracefully", []string{encodeBool(afterLoop)})
}

// PausePlaylist dispatches FPP's zero-argument "Pause Playlist" command.
// Capture section 2 measured this returning 200 "Playlist Paused" while
// FPP was idle and pausing nothing; a 2xx Outcome from this method is not
// evidence anything paused.
func (c *Client) PausePlaylist(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Pause Playlist", nil)
}

// ResumePlaylist dispatches FPP's zero-argument "Resume Playlist"
// command. Capture section 2 measured this returning 200 "Playlist
// Restarted" while FPP was idle and resuming nothing; the body's wording
// ("Restarted") is FPP's own and is not evidence of a restart — capture
// section 3.4 confirmed the observed playlist index did not move.
func (c *Client) ResumePlaylist(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Resume Playlist", nil)
}

// NextPlaylistItem dispatches FPP's zero-argument "Next Playlist Item"
// command. Capture section 3.5: past the last item, one more Next ends
// the playlist entirely (status becomes idle, playlist becomes empty) —
// "skip forward" and "stop the show" are the same command at the last
// item, and FPP answers "Next Item Playing" in both cases regardless.
func (c *Client) NextPlaylistItem(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Next Playlist Item", nil)
}

// PrevPlaylistItem dispatches FPP's zero-argument "Prev Playlist Item"
// command. Capture section 3.5 confirmed index movement 3 -> 2 on the
// bench playlist; unlike NextPlaylistItem, moving before the first item
// was not captured.
func (c *Client) PrevPlaylistItem(ctx context.Context) (Outcome, error) {
	return c.Invoke(ctx, "Prev Playlist Item", nil)
}

// SetVolume dispatches FPP's "Volume Set" command with one argument,
// strconv.Itoa(volume). volume is validated with [ValidateVolume] before
// dispatch — capture section 1.5 measured FPP itself silently clamping
// Volume Set/999 to 100 and silently coercing Volume Set/abc to 0, so
// there is no version of "let FPP reject an out-of-range value" that
// works; this method rejects it before anything is sent.
func (c *Client) SetVolume(ctx context.Context, volume int) (Outcome, error) {
	if err := ValidateVolume(volume); err != nil {
		return Outcome{}, err
	}
	return c.Invoke(ctx, "Volume Set", []string{strconv.Itoa(volume)})
}

// httpStatusError records a non-2xx HTTP response, distinguishable via
// [errors.As] from a transport-level failure to reach FPP at all (a bare
// *url.Error wrapping a dial or timeout failure, returned as-is from
// Invoke with no wrapping of this type).
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}
