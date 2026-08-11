package fpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift in either package is caught here rather than only at
// Task F's wiring site.
var _ collector.Collector = (*Collector)(nil)

// sourceName is [observation.Observation.Source] for every observation this
// package produces.
const sourceName = "fpp-rest"

// Signal IDs this collector produces. Namespaced "fpp.*" per the Step 3
// contract section 7. This is the complete set the Task C spec's minimum
// table asks for; nothing here is sourced from a field that was not
// actually observed in a captured response (see testdata).
//
// SignalPositionRemaining and SignalUptimeSeconds were originally
// "fpp.position.remainingSeconds" and "fpp.uptimeSeconds" — camelCase
// segments that violated contract section 7's "lowercase, dot-separated"
// rule (an artifact of the task spec that handed this package a camelCase
// example; the Task F wiring pass's own report explains the mistake). Task
// D's API test fixtures separately guessed at "fpp.status.player_state"
// and "fpp.status.uptime_seconds" for what this package actually calls
// SignalStatus ("fpp.status") and SignalUptimeSeconds — two different
// signals conflated into one guessed name. Both were fixed at the wiring
// pass in favor of THIS package's names, normalized to dotted lowercase,
// since this collector is the producer and Task D's fixtures were the
// guess: renaming a signal the collector actually emits to satisfy a test
// fixture would have been backwards.
const (
	SignalReachable         observation.SignalID = "fpp.reachable"
	SignalVersion           observation.SignalID = "fpp.version"
	SignalMode              observation.SignalID = "fpp.mode"
	SignalStatus            observation.SignalID = "fpp.status"
	SignalPlaylistName      observation.SignalID = "fpp.playlist.name"
	SignalSequenceName      observation.SignalID = "fpp.sequence.name"
	SignalPositionSeconds   observation.SignalID = "fpp.position.seconds"
	SignalPositionRemaining observation.SignalID = "fpp.position.remaining.seconds"
	SignalMultiSyncEnabled  observation.SignalID = "fpp.multisync.enabled"
	SignalMultiSyncSystems  observation.SignalID = "fpp.multisync.systems"
	SignalSchedulerStatus   observation.SignalID = "fpp.scheduler.status"
	SignalUptimeSeconds     observation.SignalID = "fpp.uptime.seconds"
)

// allDataSignals is every signal this collector produces from
// /api/fppd/status, excluding SignalReachable (built separately from
// whether the request itself succeeded) and SignalMultiSyncSystems (its own
// independent request). Used to fail every data signal uniformly when the
// status document could not be fetched or decoded at all.
var allDataSignals = []observation.SignalID{
	SignalVersion, SignalMode, SignalStatus, SignalSequenceName,
	SignalPlaylistName, SignalPositionSeconds, SignalPositionRemaining,
	SignalMultiSyncEnabled, SignalSchedulerStatus, SignalUptimeSeconds,
}

// Provenance-labeled defaults, in the pattern pkg/multisync/timeline.go
// established: every threshold states whether it is derived from something
// FPP-authoritative or is a ShowMesh guess awaiting bench verification. All
// of the ones below are guesses — RES-012 and RES-013 are the records that
// own real values, and neither has measured FPP REST latency, a safe poll
// cadence, or a safe backoff shape yet.
const (
	// DefaultRequestTimeout bounds a single GET. SHOWMESH HYPOTHESIS, NOT
	// MEASURED: intended to be comfortably longer than a healthy FPP's
	// LAN response time while still short enough that an unreachable
	// instance does not stall a poll cycle for long.
	DefaultRequestTimeout = 5 * time.Second

	// DefaultPollInterval is the recommended cadence for
	// collector.Runner.Add. SHOWMESH HYPOTHESIS, NOT MEASURED: chosen to
	// keep the operator surface reasonably fresh without polling FPP more
	// often than a REST status check plausibly needs to be, per
	// OBSERVABILITY section 5's "monitoring cannot impair show devices."
	DefaultPollInterval = 15 * time.Second

	// DefaultValidFor is how long a successfully collected value is
	// reported [observation.StateCurrent] before it ages to
	// [observation.StateStale]. SHOWMESH HYPOTHESIS, NOT MEASURED, in
	// exactly the sense internal/coordinator/inventory's
	// StalenessWindow is (Step 3 contract section 3.4): not a contract
	// constant, never published as one. Set to three poll intervals,
	// mirroring the roughly 1:3 heartbeat-to-staleness ratio
	// scripts/test-integration.sh documents for inventory liveness — a
	// value should survive missing a couple of polls before reading as
	// stale, without staying "current" long after polling has plainly
	// stopped.
	DefaultValidFor = 3 * DefaultPollInterval

	// DefaultBackoffBase and DefaultBackoffCeiling bound the exponential
	// backoff applied to poll ATTEMPTS (not evidence age) after
	// consecutive failures against one instance — see recordAttempt.
	// SHOWMESH HYPOTHESIS, NOT MEASURED.
	DefaultBackoffBase    = 2 * time.Second
	DefaultBackoffCeiling = 2 * time.Minute

	// DefaultMaxResponseBytes bounds how much of one response body is
	// read. SHOWMESH HYPOTHESIS: real FPP status documents are a few KB
	// (see testdata); this is generous headroom above that so a
	// misbehaving or compromised FPP streaming an unbounded body cannot
	// exhaust coordinator memory — OBSERVABILITY section 5's "monitoring
	// cannot impair show devices" cuts both ways.
	DefaultMaxResponseBytes = 4 << 20 // 4 MiB
)

// Options configures a Collector. Every field left at its zero value is
// replaced by the matching Default* constant; see each constant's doc
// comment for provenance.
type Options struct {
	// HTTPClient is shared across every request this Collector makes.
	// Callers SHOULD construct one *http.Client and pass it to every
	// fpp.New call for every configured instance (Step 3 contract /
	// OBSERVABILITY section 5: "use one http.Client with a sane
	// transport"); leaving this nil constructs a private client, which is
	// fine for a single instance or a test but wastes a connection pool
	// once more than a few FPP instances are configured. The client's own
	// Timeout is deliberately left unset by the default: RequestTimeout is
	// enforced per request via context, not via http.Client.Timeout, so
	// one shared client can serve Collectors with different
	// RequestTimeout values correctly.
	HTTPClient *http.Client

	// RequestTimeout bounds a single GET, via a context deadline applied
	// per request (not http.Client.Timeout — see HTTPClient). See
	// DefaultRequestTimeout.
	RequestTimeout time.Duration

	// ValidFor is set as [observation.WithValidFor] on every observation
	// this Collector produces. See DefaultValidFor.
	ValidFor time.Duration

	// BackoffBase and BackoffCeiling bound the backoff applied between
	// poll attempts to this instance after consecutive failures. See
	// DefaultBackoffBase and DefaultBackoffCeiling.
	BackoffBase    time.Duration
	BackoffCeiling time.Duration

	// MaxResponseBytes bounds how much of one response body is read. See
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64

	// Now is the clock used for every time-dependent decision: backoff
	// scheduling and the ObservedAt/CollectedAt stamped on every
	// observation. nil (the default in production) means time.Now. Tests
	// inject a fake clock so backoff and staleness assertions do not need
	// real sleeps.
	Now func() time.Time
}

// withDefaults returns a copy of o with every zero-valued field replaced by
// its documented default. RequestTimeout is resolved before HTTPClient so
// a private default client's construction (which does not itself need
// RequestTimeout, since the timeout is applied via context — see
// Options.HTTPClient) still happens after every other field is settled.
func (o Options) withDefaults() Options {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	if o.ValidFor <= 0 {
		o.ValidFor = DefaultValidFor
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = DefaultBackoffBase
	}
	if o.BackoffCeiling <= 0 {
		o.BackoffCeiling = DefaultBackoffCeiling
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Collector polls one FPP instance's REST API and produces
// [observation.Observation] values for it. It implements
// internal/coordinator/collector.Collector.
//
// SERIALIZATION CONTRACT: Poll must never be called concurrently with
// itself for the same Collector. internal/coordinator/collector.Runner
// guarantees this by construction — each registered collector gets its own
// goroutine with a self-paced timer that only starts counting down after
// the previous Poll call has returned — so this type carries no in-flight
// guard of its own. A caller that calls Poll directly, outside a Runner,
// concurrently with itself will race on the backoff state guarded by mu
// below; mu prevents data corruption in that case but does not prevent two
// simultaneous HTTP round trips to the same FPP, which is the actual
// property "never two in-flight requests to the same instance" (Task C
// spec) requires. Go through a Runner in production.
type Collector struct {
	id      string
	baseURL string
	client  *http.Client
	now     func() time.Time

	requestTimeout   time.Duration
	validFor         time.Duration
	backoffBase      time.Duration
	backoffCeiling   time.Duration
	maxResponseBytes int64

	// mu guards the backoff state below. See the serialization contract
	// above for why this is a correctness backstop, not the mechanism
	// that actually prevents overlapping requests.
	mu                  sync.Mutex
	consecutiveFailures int
	nextAttemptAt       time.Time
}

// New constructs a Collector for one FPP instance. id must satisfy
// [mqttproto.ValidateNodeID] (Step 3 contract section 7: FPP instance IDs
// use the same syntax as node IDs). baseURL must be an absolute http or
// https URL with a host and no userinfo/credentials — the same rule
// internal/coordinator/config's SHOWMESH_FPP_ENDPOINTS parsing enforces at
// startup, duplicated here (deliberately: a *Collector must be safe to
// construct directly, including in tests, without relying on config
// package validation already having run).
func New(id, baseURL string, opts Options) (*Collector, error) {
	if err := mqttproto.ValidateNodeID(id); err != nil {
		return nil, fmt.Errorf("fpp collector: %w", err)
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("fpp collector %q: invalid URL %q: %w", id, baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fpp collector %q: URL %q must use http or https", id, baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("fpp collector %q: URL %q must include a host", id, baseURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("fpp collector %q: URL must not include userinfo/credentials", id)
	}

	o := opts.withDefaults()

	return &Collector{
		id:               id,
		baseURL:          strings.TrimSuffix(parsed.String(), "/"),
		client:           o.HTTPClient,
		now:              o.Now,
		requestTimeout:   o.RequestTimeout,
		validFor:         o.ValidFor,
		backoffBase:      o.BackoffBase,
		backoffCeiling:   o.BackoffCeiling,
		maxResponseBytes: o.MaxResponseBytes,
	}, nil
}

// ID returns this instance's identifier.
func (c *Collector) ID() string { return c.id }

// Poll performs one collection cycle: a GET of /api/fppd/status and a GET
// of /api/fppd/multiSyncSystems, and builds an observation for every signal
// in the Task C table from whatever was actually obtained. It never issues
// a request to /api/settings/MultiSyncEnabled — see this package's doc
// comment for why that endpoint is a trap rather than a shortcut.
//
// If this instance is currently backed off following consecutive failures
// (see recordAttempt), Poll makes no request at all this cycle and returns
// an empty slice: the store's last-recorded observations (already a
// collection_failed absence, from the failure that triggered the backoff)
// remain the coordinator's answer until the next real attempt, which is
// honest — nothing changed because nothing was checked — rather than
// re-asserting the same failure on a timer for no evidentiary reason.
func (c *Collector) Poll(ctx context.Context) []observation.Observation {
	now := c.now()

	c.mu.Lock()
	skip := now.Before(c.nextAttemptAt)
	c.mu.Unlock()
	if skip {
		return nil
	}

	statusBody, statusErr := c.fetch(ctx, "/api/fppd/status")
	c.recordAttempt(statusErr, now)

	obs := make([]observation.Observation, 0, len(allDataSignals)+2)

	if statusErr != nil {
		reason := classifyFetchError(statusErr)
		obs = append(obs, c.failed(SignalReachable, reason, now))
		obs = append(obs, c.allDataSignalsFailed(reason, now)...)
	} else {
		obs = append(obs, c.measured(SignalReachable, true, now, observation.WithQuality(observation.QualityDerived)))

		doc, err := decodeRawDoc(statusBody)
		if err != nil {
			reason := "decode error: " + err.Error()
			obs = append(obs, c.allDataSignalsFailed(reason, now)...)
		} else {
			obs = append(obs, c.statusSignals(doc, now)...)
		}
	}

	// multiSyncSystems is an independent request and an independent
	// signal: its failure does not change SignalReachable's verdict or
	// this instance's backoff timing (recordAttempt is keyed to the
	// primary status request only — see that method's doc comment), and
	// the primary status request's failure does not stop this one from
	// being attempted.
	msBody, msErr := c.fetch(ctx, "/api/fppd/multiSyncSystems")
	if msErr != nil {
		obs = append(obs, c.failed(SignalMultiSyncSystems, classifyFetchError(msErr), now))
	} else {
		count, err := multiSyncSystemsCount(msBody)
		if err != nil {
			obs = append(obs, c.failed(SignalMultiSyncSystems, "decode error: "+err.Error(), now))
		} else {
			obs = append(obs, c.measured(SignalMultiSyncSystems, int64(count), now))
		}
	}

	return obs
}

// statusSignals builds every /api/fppd/status-derived signal from doc. Each
// field is extracted independently (decode.go), so one field with an
// unexpected shape degrades only its own signal to
// [observation.StateCollectionFailed] rather than this whole method
// returning nothing.
func (c *Collector) statusSignals(doc rawDoc, now time.Time) []observation.Observation {
	obs := make([]observation.Observation, 0, len(allDataSignals))

	obs = append(obs, c.stringSignal(doc, SignalVersion, "version", now))
	obs = append(obs, c.stringSignal(doc, SignalMode, "mode_name", now))
	obs = append(obs, c.stringSignal(doc, SignalStatus, "status_name", now))
	obs = append(obs, c.stringSignal(doc, SignalSequenceName, "current_sequence", now))
	obs = append(obs, c.playlistNameSignal(doc, now))
	obs = append(obs, c.numberSignal(doc, SignalPositionSeconds, "seconds_played", "seconds", now))
	obs = append(obs, c.numberSignal(doc, SignalPositionRemaining, "seconds_remaining", "seconds", now))
	obs = append(obs, c.multiSyncEnabledSignal(doc, now))
	obs = append(obs, c.schedulerStatusSignal(doc, now))
	obs = append(obs, c.numberSignal(doc, SignalUptimeSeconds, "uptimeSeconds", "seconds", now))

	return obs
}

// multiSyncEnabledSignal reads the top-level "multisync" boolean from
// /api/fppd/status — the running daemon's actual behavior — never
// /api/settings/MultiSyncEnabled's schema. See this package's doc comment.
//
// false is reported here exactly like any other successfully-read value:
// [observation.StateCurrent], a real value, not an absence. A disabled
// feature is a configuration fact an operator needs stated positively (Step
// 3 contract section 3.1), not a fault — this signal must never drive a
// degraded or failed health verdict on its own. If the field cannot be
// read at all, that is [observation.StateCollectionFailed] with a reason,
// never a fabricated false: see doc.go's "never let a failed read become a
// negative answer."
func (c *Collector) multiSyncEnabledSignal(doc rawDoc, now time.Time) observation.Observation {
	v, err := doc.boolField("multisync")
	if err != nil {
		return c.failed(SignalMultiSyncEnabled, err.Error(), now)
	}
	return c.measured(SignalMultiSyncEnabled, v, now)
}

func (c *Collector) playlistNameSignal(doc rawDoc, now time.Time) observation.Observation {
	// Idle-state values (verified against a real fppd) include
	// current_playlist.playlist as a genuinely empty string, not an
	// absent field. An empty string is reported here as StateCurrent with
	// value "" — a real, positively-observed answer ("nothing is
	// playing"), never conflated with StateNotCollected or
	// StateCollectionFailed.
	v, err := doc.nestedStringField("current_playlist", "playlist")
	if err != nil {
		return c.failed(SignalPlaylistName, err.Error(), now)
	}
	return c.measured(SignalPlaylistName, v, now)
}

func (c *Collector) schedulerStatusSignal(doc rawDoc, now time.Time) observation.Observation {
	v, err := doc.nestedStringField("scheduler", "status")
	if err != nil {
		return c.failed(SignalSchedulerStatus, err.Error(), now)
	}
	return c.measured(SignalSchedulerStatus, v, now)
}

func (c *Collector) stringSignal(doc rawDoc, sig observation.SignalID, key string, now time.Time) observation.Observation {
	v, err := doc.stringField(key)
	if err != nil {
		return c.failed(sig, err.Error(), now)
	}
	return c.measured(sig, v, now)
}

func (c *Collector) numberSignal(doc rawDoc, sig observation.SignalID, key, unit string, now time.Time) observation.Observation {
	v, err := doc.numberField(key)
	if err != nil {
		return c.failed(sig, err.Error(), now)
	}
	return c.measured(sig, v, now, observation.WithUnit(unit))
}

// allDataSignalsFailed builds a StateCollectionFailed observation, all with
// the same reason, for every signal in allDataSignals. Used when the status
// document could not be fetched or could not be parsed as a JSON object at
// all — cases where nothing in the document can be trusted, as opposed to
// one field having an unexpected shape (handled per-field by statusSignals).
func (c *Collector) allDataSignalsFailed(reason string, now time.Time) []observation.Observation {
	obs := make([]observation.Observation, 0, len(allDataSignals))
	for _, sig := range allDataSignals {
		obs = append(obs, c.failed(sig, reason, now))
	}
	return obs
}

func (c *Collector) resource() observation.ResourceRef {
	return observation.ResourceRef{Kind: observation.ResourceFPP, ID: c.id}
}

// measured builds a StateCurrent observation via [observation.Measured],
// stamped with this Collector's source, ValidFor, and clock. opts are
// applied after those defaults, so a call site (Poll's SignalReachable
// observation is the one example in this file) can override Quality.
func (c *Collector) measured(sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) observation.Observation {
	allOpts := append([]observation.Option{
		observation.WithSource(sourceName),
		observation.WithValidFor(c.validFor),
		observation.WithCollectedAt(observedAt),
	}, opts...)

	o, err := observation.Measured(c.resource(), sig, value, observedAt, allOpts...)
	if err != nil {
		// Unreachable in practice: every call site above passes a value
		// type Validate accepts (bool, string, float64) and a non-empty
		// signal/resource ID. Surfaced as a collection_failed observation
		// rather than dropped or panicking, so a future bug in this file
		// is visible on the API instead of invisible.
		return c.failed(sig, fmt.Sprintf("internal error building observation: %v", err), observedAt)
	}
	return o
}

// failed builds a StateCollectionFailed observation via
// [observation.CollectionFailed]. reason must be non-empty (every call site
// above supplies one; see decode.go's extractors, which always return a
// non-empty error message alongside a non-nil error).
func (c *Collector) failed(sig observation.SignalID, reason string, observedAt time.Time) observation.Observation {
	o, err := observation.CollectionFailed(c.resource(), sig, reason,
		observation.WithSource(sourceName), observation.WithCollectedAt(observedAt))
	if err != nil {
		// reason is non-empty and sig/resource are always set by this
		// file; a failure here is a bug in this file, not a runtime
		// condition to degrade gracefully from.
		panic(fmt.Sprintf("fpp collector %q: CollectionFailed(%q) unexpectedly failed: %v", c.id, sig, err))
	}
	return o
}

// recordAttempt updates backoff state from the primary /api/fppd/status
// request's outcome. A nil err resets the backoff entirely (this instance
// is healthy again); a non-nil err increases consecutiveFailures and
// schedules nextAttemptAt via backoffDelay.
//
// Keyed to the status request alone, not the multiSyncSystems request:
// status is what SignalReachable measures and is the request every other
// data signal depends on, so it is the meaningful signal of "is this
// instance up" for backoff purposes. multiSyncSystems failing independently
// (e.g. a version that lacks the endpoint) says nothing about whether the
// instance itself is reachable, and backing off the whole collector for
// that would suppress genuinely fresh status data on a healthy, reachable
// FPP.
func (c *Collector) recordAttempt(err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err == nil {
		c.consecutiveFailures = 0
		c.nextAttemptAt = time.Time{}
		return
	}

	c.consecutiveFailures++
	c.nextAttemptAt = now.Add(backoffDelay(c.backoffBase, c.backoffCeiling, c.consecutiveFailures))
}

// backoffDelay computes an exponential-backoff-with-full-jitter delay:
// double the base delay for each consecutive failure up to ceiling, then
// pick uniformly at random in [0, that value]. Full jitter (as opposed to a
// fixed delay, or a percentage +/- a fixed delay) means several instances
// failing at the same moment do not retry in lockstep even after several
// backoff steps — the AWS Architecture Blog's "Exponential Backoff and
// Jitter" post's reasoning, applied here to REST polling rather than the
// service-retry case it was written for.
func backoffDelay(base, ceiling time.Duration, consecutiveFailures int) time.Duration {
	if consecutiveFailures < 1 {
		consecutiveFailures = 1
	}

	d := base
	for i := 1; i < consecutiveFailures && d < ceiling; i++ {
		d *= 2
		if d <= 0 { // overflow guard: a Duration is a signed int64 of nanoseconds
			d = ceiling
			break
		}
	}
	if d > ceiling {
		d = ceiling
	}

	return time.Duration(rand.Int64N(int64(d) + 1))
}

// httpStatusError records a non-2xx HTTP response so classifyFetchError can
// name it specifically rather than falling back to a generic message.
type httpStatusError struct {
	StatusCode int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}

// fetch issues one bounded GET against path on this instance's base URL and
// returns the response body. It never returns a partial body silently: a
// body larger than maxResponseBytes is an error, not a truncated result
// that would look like a short, valid, but wrong document.
func (c *Collector) fetch(ctx context.Context, path string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Drain and close so the underlying connection can be reused by
		// the shared client's pool; the body's content past this point is
		// not evidence of anything.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return nil, fmt.Errorf("response body exceeded %d byte limit", c.maxResponseBytes)
	}
	return body, nil
}

// classifyFetchError renders err as a short, operator-useful failure class
// (Task C spec: "reason carries the failure class ... and is useful to an
// operator"), never including a credential (New already rejects any
// endpoint URL carrying userinfo, so there is structurally nothing of that
// kind to leak here) or more of the URL than the operator already
// configured.
func classifyFetchError(err error) string {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	return err.Error()
}
