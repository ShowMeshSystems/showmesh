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
// contract section 7. Nothing here is sourced from a field that was not
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

// Step 5 playback signals, from /api/fppd/status. See signals.go's
// StatusSignals and contract section 3.1's playback table.
const (
	SignalSongName               observation.SignalID = "fpp.song.name"
	SignalPlaylistRepeatMode     observation.SignalID = "fpp.playlist.repeat_mode"
	SignalPlaylistIndex          observation.SignalID = "fpp.playlist.index"
	SignalPlaylistCount          observation.SignalID = "fpp.playlist.count"
	SignalPlaylistType           observation.SignalID = "fpp.playlist.type"
	SignalSchedulerEnabled       observation.SignalID = "fpp.scheduler.enabled"
	SignalSchedulerNextPlaylist  observation.SignalID = "fpp.scheduler.next_playlist"
	SignalSchedulerNextStartTime observation.SignalID = "fpp.scheduler.next_start_time"
	SignalMediaFilename          observation.SignalID = "fpp.media.filename"
	SignalPositionElapsedSeconds observation.SignalID = "fpp.position.elapsed.seconds"
)

// Step 5 controller and network health signals, from /api/fppd/status. Per-
// sensor signals (fpp.sensor.<key>.value/.type) are dynamic — see
// signals.go's sensorSignals — and so have no constant here; the set of
// sensors an instance reports cannot be known before it is observed.
const (
	SignalFPPDState             observation.SignalID = "fpp.fppd.state"
	SignalPowerBad              observation.SignalID = "fpp.power.bad"
	SignalBridging              observation.SignalID = "fpp.bridging"
	SignalChannelInputsEnabled  observation.SignalID = "fpp.channel_inputs.enabled"
	SignalChannelOutputsEnabled observation.SignalID = "fpp.channel_outputs.enabled"
	SignalBranch                observation.SignalID = "fpp.branch"
	SignalUUID                  observation.SignalID = "fpp.uuid"
	SignalHostName              observation.SignalID = "fpp.host_name"
	SignalVolume                observation.SignalID = "fpp.volume"
	SignalMQTTConfigured        observation.SignalID = "fpp.mqtt.configured"
	SignalMQTTConnected         observation.SignalID = "fpp.mqtt.connected"
	SignalWarningsCount         observation.SignalID = "fpp.warnings.count"
	SignalWarningsSummary       observation.SignalID = "fpp.warnings.summary"
)

// Step 5 platform signals, from /api/system/info. See signals.go's
// SystemInfoSignals and contract section 3.1's platform table.
const (
	SignalOSVersion      observation.SignalID = "fpp.os.version"
	SignalOSRelease      observation.SignalID = "fpp.os.release"
	SignalPlatform       observation.SignalID = "fpp.platform"
	SignalVariant        observation.SignalID = "fpp.variant"
	SignalKernel         observation.SignalID = "fpp.kernel"
	SignalUtilizationCPU observation.SignalID = "fpp.utilization.cpu"
	SignalUtilizationMem observation.SignalID = "fpp.utilization.memory"
	SignalDiskMediaFree  observation.SignalID = "fpp.disk.media.free_bytes"
	SignalDiskMediaTotal observation.SignalID = "fpp.disk.media.total_bytes"
	SignalDiskRootFree   observation.SignalID = "fpp.disk.root.free_bytes"
	SignalDiskRootTotal  observation.SignalID = "fpp.disk.root.total_bytes"
)

// Step 5 ports signals, from /api/fppd/ports. Only the two aggregate
// signals and the decode-failure signal are declared as constants:
// fpp.port.<key>.* is a dynamic family (signals.go's portElementSignals) —
// the set of ports, and which key each normalizes to, cannot be known
// before an instance is actually polled.
const (
	SignalPortsCount        observation.SignalID = "fpp.ports.count"
	SignalPortsBlindCount   observation.SignalID = "fpp.ports.blind_count"
	SignalPortsDecodeFailed observation.SignalID = "fpp.ports.decode_failed"
)

// allStatusSignals is every STATIC signal StatusSignals can produce from
// /api/fppd/status, excluding SignalReachable (built separately in Poll
// from whether the request itself succeeded — not a StatusSignals output)
// and the dynamic fpp.sensor.<key>.* family (see sensorSignals). Used to
// fail every one of these signals uniformly when the status document could
// not be fetched or could not even be parsed as a JSON object — the case
// where nothing in the document can be trusted, as distinct from one field
// having an unexpected shape, which StatusSignals already degrades
// per-field.
var allStatusSignals = []observation.SignalID{
	SignalVersion, SignalMode, SignalStatus, SignalSequenceName,
	SignalPlaylistName, SignalPositionSeconds, SignalPositionRemaining,
	SignalMultiSyncEnabled, SignalSchedulerStatus, SignalUptimeSeconds,
	SignalSongName, SignalPlaylistRepeatMode, SignalPlaylistIndex,
	SignalPlaylistCount, SignalPlaylistType, SignalSchedulerEnabled,
	SignalSchedulerNextPlaylist, SignalSchedulerNextStartTime,
	SignalMediaFilename, SignalPositionElapsedSeconds,
	SignalFPPDState, SignalPowerBad, SignalBridging,
	SignalChannelInputsEnabled, SignalChannelOutputsEnabled,
	SignalBranch, SignalUUID, SignalHostName, SignalVolume,
	SignalMQTTConfigured, SignalMQTTConnected,
	SignalWarningsCount, SignalWarningsSummary,
}

// portsFailureSignals is every STATIC signal PortSignals can produce,
// excluding the dynamic fpp.port.<key>.* family and
// SignalPortsDecodeFailed (a conditional signal, only ever emitted when a
// problem is actually found — see PortSignals). Used to fail the two
// aggregate signals when /api/fppd/ports itself could not be fetched or
// parsed at all.
var portsFailureSignals = []observation.SignalID{
	SignalPortsCount, SignalPortsBlindCount,
}

// systemInfoStaticSignals is every signal SystemInfoSignals can produce
// from /api/system/info — every one of them is static; this endpoint has
// no dynamic per-element family the way ports and sensors do. Used both to
// fail every signal uniformly when /api/system/info could not be fetched
// or parsed at all, and (via AllSignals) as part of this package's
// exported static vocabulary.
var systemInfoStaticSignals = []observation.SignalID{
	SignalOSVersion, SignalOSRelease, SignalPlatform, SignalVariant, SignalKernel,
	SignalUtilizationCPU, SignalUtilizationMem,
	SignalDiskMediaFree, SignalDiskMediaTotal, SignalDiskRootFree, SignalDiskRootTotal,
}

// AllSignals is the complete STATIC signal-ID vocabulary this package can
// produce, across all three endpoints it polls plus SignalReachable and
// SignalMultiSyncSystems (each its own independent request/signal — see
// Poll). It deliberately excludes the two dynamic signal families this
// package also produces, fpp.port.<key>.* and fpp.sensor.<key>.*, whose
// exact members cannot be known before a real poll observes what ports and
// sensors a given instance actually has.
//
// Exported so a caller does not need to hand-maintain a duplicate list
// that can silently drift from what this package actually emits —
// apiwiring.go's not-yet-polled placeholder synthesis (contract section
// 5.4) is the motivating caller, restricted to this static set for exactly
// the reason AllSignals itself excludes the dynamic families.
var AllSignals = buildAllSignals()

func buildAllSignals() []observation.SignalID {
	all := make([]observation.SignalID, 0, 2+len(allStatusSignals)+len(portsFailureSignals)+1+len(systemInfoStaticSignals))
	all = append(all, SignalReachable, SignalMultiSyncSystems)
	all = append(all, allStatusSignals...)
	all = append(all, portsFailureSignals...)
	all = append(all, SignalPortsDecodeFailed)
	all = append(all, systemInfoStaticSignals...)
	return all
}

// init runs [observation.ValidateSignalID] over every signal constant this
// package declares (via AllSignals), so a malformed signal ID fails at
// package load rather than at the first poll — contract section 3.1.
func init() {
	for _, sig := range AllSignals {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic(fmt.Sprintf("fpp: invalid signal ID declared by this package: %v", err))
		}
	}
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
	//
	// GET-ONLY MEANS NO REDIRECT EITHER (Step 5 review finding 1): FPP
	// invokes commands over GET at /api/command/... , so a coordinator
	// that follows an arbitrary redirect is a confused deputy that can be
	// made to start a playlist, or worse, on the operator's live display —
	// reproduced with a real FPP answering /api/fppd/status with a 302 to
	// another host's /api/command/Start%20Playlist/... . Go's zero-value
	// http.Client follows up to 10 redirects to ANY host and ANY path by
	// default, which this package's own guarantee cannot depend on a
	// caller happening to configure correctly: whatever *http.Client is
	// supplied here — including the nil default, and including one whose
	// own CheckRedirect is already set to something permissive —
	// withDefaults takes a shallow copy of it (preserving the Transport
	// pointer, so connection pooling across fpp.New calls still works) and
	// forces CheckRedirect to refuse every redirect, unconditionally. This
	// is the "wrap/copy the supplied client" choice named in the Step 5
	// review, picked over the alternatives: refusing a client with a nil
	// CheckRedirect would still let a caller supply a permissive non-nil
	// one through, and "treat any 3xx as collection_failed" (which fetch
	// already does, via httpStatusError) only ever gets a chance to run if
	// something first stops the client from silently following the
	// redirect before fetch ever sees a status code. The one thing this
	// cannot defend against — no client-level guarantee can — is a
	// caller-supplied Transport whose RoundTrip method itself performs an
	// entirely different request instead of returning the 3xx it received;
	// that requires the caller to hand this package a hostile
	// RoundTripper, which is a different threat model than an FPP
	// responding with a redirect.
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
	// Force CheckRedirect on a private, shallow copy of whatever client is
	// now in o.HTTPClient (the freshly-built default above, or a
	// caller-supplied one) — see Options.HTTPClient's doc comment for why
	// this is a copy, never a mutation of a caller's original *http.Client,
	// and why it happens unconditionally rather than only when
	// CheckRedirect is nil. The Transport field is a pointer, so the copy
	// still shares the caller's connection pool.
	guarded := *o.HTTPClient
	guarded.CheckRedirect = refuseRedirects
	o.HTTPClient = &guarded
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
	// Step 5 review finding 9: paths are string-concatenated in fetch
	// (c.baseURL+path), so a configured base URL carrying its own path or
	// query — SHOWMESH_FPP_ENDPOINTS=http://host/api/command/Start%20Playlist?arg=
	// — turns into GET /api/command/Start Playlist?arg=/api/fppd/status
	// for every request this Collector ever makes. Lower severity than
	// finding 1 (this requires a config-file typo or a malicious
	// SHOWMESH_FPP_ENDPOINTS value, not merely an FPP's own response), but
	// worth closing for the same reason: this package's GET-only guarantee
	// should not depend on every path this Collector requests happening to
	// still land under /api/ by coincidence.
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("fpp collector %q: URL %q must not include a path", id, baseURL)
	}
	if parsed.RawQuery != "" {
		return nil, fmt.Errorf("fpp collector %q: URL %q must not include a query", id, baseURL)
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("fpp collector %q: URL %q must not include a fragment", id, baseURL)
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

// Poll performs one collection cycle: a GET each of /api/fppd/status,
// /api/fppd/multiSyncSystems, /api/fppd/ports, and /api/system/info, and
// builds an observation for every signal this package knows about from
// whatever was actually obtained. It never issues a request to
// /api/settings/MultiSyncEnabled, or anything else under /api/settings —
// see this package's doc comment for why that family is a trap rather
// than a shortcut.
//
// Each of the four requests is independent (contract section 3): only
// /api/fppd/status drives SignalReachable and this instance's backoff
// timing (see recordAttempt). A failure fetching or decoding
// /api/fppd/ports or /api/system/info degrades only that endpoint's own
// signals — SignalReachable, the status-derived signals, and the other
// endpoint's signals are entirely unaffected, exactly as
// /api/fppd/multiSyncSystems already worked before Step 5.
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

	obs := make([]observation.Observation, 0, len(allStatusSignals)+len(systemInfoStaticSignals)+16)

	if statusErr != nil {
		reason := classifyFetchError(statusErr)
		obs = append(obs, c.failed(SignalReachable, reason, now))
		obs = append(obs, c.absentAll(allStatusSignals, observation.StateCollectionFailed, reason, now)...)
	} else {
		obs = append(obs, c.measured(SignalReachable, true, now, observation.WithQuality(observation.QualityDerived)))

		sigs, err := StatusSignals(statusBody)
		if err != nil {
			reason := "decode error: " + err.Error()
			obs = append(obs, c.absentAll(allStatusSignals, observation.StateCollectionFailed, reason, now)...)
		} else {
			for _, sv := range sigs {
				obs = append(obs, c.toObservation(sv, now))
			}
		}
	}

	// multiSyncSystems: an independent request and an independent signal
	// (unchanged from before Step 5).
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

	// /api/fppd/ports: independent request, independent signals.
	portsBody, portsErr := c.fetch(ctx, "/api/fppd/ports")
	if portsErr != nil {
		obs = append(obs, c.absentAll(portsFailureSignals, observation.StateCollectionFailed, classifyFetchError(portsErr), now)...)
	} else {
		sigs, err := PortSignals(portsBody)
		if err != nil {
			obs = append(obs, c.absentAll(portsFailureSignals, observation.StateCollectionFailed, "decode error: "+err.Error(), now)...)
		} else {
			for _, sv := range sigs {
				obs = append(obs, c.toObservation(sv, now))
			}
		}
	}

	// /api/system/info: independent request, independent signals.
	sysInfoBody, sysInfoErr := c.fetch(ctx, "/api/system/info")
	if sysInfoErr != nil {
		obs = append(obs, c.absentAll(systemInfoStaticSignals, observation.StateCollectionFailed, classifyFetchError(sysInfoErr), now)...)
	} else {
		sigs, err := SystemInfoSignals(sysInfoBody)
		if err != nil {
			obs = append(obs, c.absentAll(systemInfoStaticSignals, observation.StateCollectionFailed, "decode error: "+err.Error(), now)...)
		} else {
			for _, sv := range sigs {
				obs = append(obs, c.toObservation(sv, now))
			}
		}
	}

	return obs
}

// toObservation stamps sv (a clock-free fact from signals.go's decode
// functions) into an [observation.Observation] using now as both
// ObservedAt and CollectedAt — this is the one place in this package that
// combines a decoded signal with the current time, per signals.go's
// SignalValue doc comment.
func (c *Collector) toObservation(sv SignalValue, now time.Time) observation.Observation {
	if sv.Absence != "" {
		return c.absence(sv.Signal, sv.Absence, sv.Reason, now)
	}
	var opts []observation.Option
	if sv.Unit != "" {
		opts = append(opts, observation.WithUnit(sv.Unit))
	}
	return c.measured(sv.Signal, sv.Value, now, opts...)
}

// absentAll builds an absence observation in state for every signal in
// sigs, all with the same reason. Used when a whole endpoint could not be
// fetched or could not be parsed at all — cases where nothing from that
// endpoint can be trusted, as opposed to one field having an unexpected
// shape (already degraded per-field by signals.go's decode functions).
func (c *Collector) absentAll(sigs []observation.SignalID, state observation.State, reason string, now time.Time) []observation.Observation {
	obs := make([]observation.Observation, 0, len(sigs))
	for _, sig := range sigs {
		obs = append(obs, c.absence(sig, state, reason, now))
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

// absence builds an absence observation in the given state — one of
// [observation.StateUnsupported], [observation.StateNotCollected], or
// [observation.StateCollectionFailed], the three states signals.go's
// SignalValue.Absence and this method's callers ever actually pass (any
// other value falls through to CollectionFailed, since it is the
// conservative choice — a state this package does not recognize is closer
// to "an attempt was made and something unexpected happened" than to a
// clean "not applicable" or "not yet tried"). reason must be non-empty; see
// [failed]'s doc comment for why every call site already guarantees that.
func (c *Collector) absence(sig observation.SignalID, state observation.State, reason string, observedAt time.Time) observation.Observation {
	opts := []observation.Option{observation.WithSource(sourceName), observation.WithCollectedAt(observedAt)}

	var o observation.Observation
	var err error
	switch state {
	case observation.StateUnsupported:
		o, err = observation.Unsupported(c.resource(), sig, reason, opts...)
	case observation.StateNotCollected:
		o, err = observation.NotCollected(c.resource(), sig, reason, opts...)
	default:
		o, err = observation.CollectionFailed(c.resource(), sig, reason, opts...)
	}
	if err != nil {
		// reason is non-empty and sig/resource are always set by this
		// file; a failure here is a bug in this file, not a runtime
		// condition to degrade gracefully from — see failed's identical
		// reasoning.
		panic(fmt.Sprintf("fpp collector %q: absence(%q, %q) unexpectedly failed: %v", c.id, sig, state, err))
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

// refuseRedirects is installed as every request client's CheckRedirect by
// withDefaults (Step 5 review finding 1): returning
// [http.ErrUseLastResponse] tells the standard library's Client.Do to stop
// after the first hop and hand back the redirect response itself — the 3xx,
// with its Location header, from the FPP this Collector actually asked —
// rather than silently following it (Go's zero-value behavior: up to 10
// redirects to whatever host and path the response names). fetch below
// already treats any non-2xx status, 3xx included, as an httpStatusError,
// so refusing the follow is the whole fix: once Do stops early, the
// existing status-code check does the rest, turning a would-be confused
// deputy into an ordinary collection_failed observation naming the HTTP
// status.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// fetch issues one bounded GET against path on this instance's base URL and
// returns the response body. It never returns a partial body silently: a
// body larger than maxResponseBytes is an error, not a truncated result
// that would look like a short, valid, but wrong document. Every non-2xx
// response, including a 3xx this Collector's client was built to never
// follow (see refuseRedirects), becomes an *httpStatusError here — never a
// silent retry, never a success.
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
