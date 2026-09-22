// Package fppbrightness polls one FPP host's ShowMesh plugin for the
// brightness state it is actually applying: the ceiling FPP's own
// schedule set, the coordinator's transition gain, the product of the
// two that reaches the channels, and whether a fade is running.
//
// It is read-only. The write half lives in internal/coordinator/api and
// dispatches FPP's own command through internal/coordinator/fppcommand;
// nothing in this package ever sends anything but a GET.
package fppbrightness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift is caught here rather than at the wiring site.
var _ collector.Collector = (*Collector)(nil)

// SourceName is [observation.Observation.Source] for every observation
// this package produces.
const SourceName = "fpp-brightness"

// Signal IDs this collector produces.
const (
	SignalCeiling         observation.SignalID = "fpp.brightness.ceiling"
	SignalTransitionGain  observation.SignalID = "fpp.brightness.transition_gain"
	SignalEffectiveOutput observation.SignalID = "fpp.brightness.effective_output"
	SignalFadeActive      observation.SignalID = "fpp.brightness.fade_active"
)

// AllSignals is this package's complete signal vocabulary.
var AllSignals = []observation.SignalID{
	SignalCeiling, SignalTransitionGain, SignalEffectiveOutput, SignalFadeActive,
}

// PluginPath is the plugin's own read-only brightness route on an FPP
// host. The LAN form FPP serves it at, not the plugin-internal one.
const PluginPath = "/api/plugin-apis/showmesh/brightness"

// absentOn404Reason is what an operator reads when the host answers but
// the plugin is not there to answer for it.
const absentOn404Reason = "the ShowMesh plugin on this FPP does not serve brightness state"

const (
	// DefaultPollInterval is the recommended collector.Runner.Add cadence.
	// A brightness slider an operator is dragging needs its readout to
	// settle in about the time it takes to let go of the slider.
	DefaultPollInterval = 5 * time.Second

	// DefaultValidFor is how long a collected value reads current before
	// it ages to stale: three poll intervals, the same ratio the FPP REST
	// collector uses.
	DefaultValidFor = 3 * DefaultPollInterval

	// DefaultRequestTimeout bounds one GET. SHOWMESH HYPOTHESIS, NOT
	// MEASURED: long enough for a healthy LAN answer, short enough that
	// an unreachable host does not stall the cycle.
	DefaultRequestTimeout = 3 * time.Second

	// maxResponseBytes bounds how much of one response is read. The real
	// document is well under a hundred bytes.
	maxResponseBytes = 64 << 10
)

// Options configures a Collector. Every zero-valued field takes its
// documented default.
type Options struct {
	// HTTPClient is shared across every request. Callers should pass one
	// client to every New call, as the FPP REST collector's own callers
	// do. Whatever is supplied is copied and forced to refuse redirects:
	// following one would let an FPP point this collector at any URL.
	HTTPClient *http.Client

	RequestTimeout time.Duration
	ValidFor       time.Duration
	Now            func() time.Time
}

func (o Options) withDefaults() Options {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		}}
	}
	guarded := *o.HTTPClient
	guarded.CheckRedirect = refuseRedirects
	o.HTTPClient = &guarded
	if o.ValidFor <= 0 {
		o.ValidFor = DefaultValidFor
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func refuseRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Collector polls one FPP instance's plugin brightness route.
type Collector struct {
	instanceID string
	baseURL    string
	client     *http.Client
	now        func() time.Time

	requestTimeout time.Duration
	validFor       time.Duration
}

// CollectorID is the collector.Runner key for one instance's brightness
// collector. It is prefixed so it never collides with the REST
// collector, which is registered on the same Runner under the bare
// instance id.
func CollectorID(instanceID string) string { return SourceName + ":" + instanceID }

// New constructs a Collector for one FPP instance. It applies the same
// base-URL rules the FPP REST collector does: an absolute http or https
// URL with a host and no userinfo, path, query or fragment.
func New(instanceID, baseURL string, opts Options) (*Collector, error) {
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		return nil, fmt.Errorf("fpp brightness collector: %w", err)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("fpp brightness collector %q: invalid URL %q: %w", instanceID, baseURL, err)
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return nil, fmt.Errorf("fpp brightness collector %q: URL %q must use http or https", instanceID, baseURL)
	case parsed.Host == "":
		return nil, fmt.Errorf("fpp brightness collector %q: URL %q must include a host", instanceID, baseURL)
	case parsed.User != nil:
		return nil, fmt.Errorf("fpp brightness collector %q: URL must not include userinfo/credentials", instanceID)
	case parsed.Path != "" && parsed.Path != "/":
		return nil, fmt.Errorf("fpp brightness collector %q: URL %q must not include a path", instanceID, baseURL)
	case parsed.RawQuery != "":
		return nil, fmt.Errorf("fpp brightness collector %q: URL %q must not include a query", instanceID, baseURL)
	case parsed.Fragment != "":
		return nil, fmt.Errorf("fpp brightness collector %q: URL %q must not include a fragment", instanceID, baseURL)
	}

	o := opts.withDefaults()
	return &Collector{
		instanceID:     instanceID,
		baseURL:        strings.TrimSuffix(parsed.String(), "/"),
		client:         o.HTTPClient,
		now:            o.Now,
		requestTimeout: o.RequestTimeout,
		validFor:       o.ValidFor,
	}, nil
}

// ID implements collector.Collector.
func (c *Collector) ID() string { return CollectorID(c.instanceID) }

// PollInterval is the recommended collector.Runner.Add cadence.
func (c *Collector) PollInterval() time.Duration { return DefaultPollInterval }

// brightnessDocument is the plugin's own response shape. Every field is a
// pointer so a missing key is absent rather than a plausible zero.
type brightnessDocument struct {
	Ceiling         *int64 `json:"ceiling"`
	TransitionGain  *int64 `json:"transitionGain"`
	EffectiveOutput *int64 `json:"effectiveOutput"`
	FadeActive      *bool  `json:"fadeActive"`
}

// Poll performs one collection cycle. complete is always true: every
// signal this collector owns is answered every cycle, as a value or as a
// stated absence.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	now := c.now()

	body, err := c.fetch(ctx)
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return c.absentAll(observation.StateUnsupported, absentOn404Reason, now), true
		}
		return c.absentAll(observation.StateCollectionFailed, classifyFetchError(err), now), true
	}

	var doc brightnessDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return c.absentAll(observation.StateCollectionFailed, "decode error: "+err.Error(), now), true
	}

	obs := make([]observation.Observation, 0, len(AllSignals))
	for _, f := range []struct {
		signal observation.SignalID
		value  *int64
	}{
		{SignalCeiling, doc.Ceiling},
		{SignalTransitionGain, doc.TransitionGain},
		{SignalEffectiveOutput, doc.EffectiveOutput},
	} {
		if f.value == nil {
			obs = append(obs, c.absence(f.signal, observation.StateNotCollected,
				"the ShowMesh plugin on this FPP did not report this value", now))
			continue
		}
		obs = append(obs, c.measured(f.signal, *f.value, now, observation.WithUnit("percent")))
	}
	if doc.FadeActive == nil {
		obs = append(obs, c.absence(SignalFadeActive, observation.StateNotCollected,
			"the ShowMesh plugin on this FPP did not report this value", now))
	} else {
		obs = append(obs, c.measured(SignalFadeActive, *doc.FadeActive, now))
	}
	return obs, true
}

func (c *Collector) fetch(ctx context.Context) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+PluginPath, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, fmt.Errorf("response body exceeded %d byte limit", int64(maxResponseBytes))
	}
	return body, nil
}

func (c *Collector) resource() observation.ResourceRef {
	return observation.ResourceRef{Kind: observation.ResourceFPP, ID: c.instanceID}
}

func (c *Collector) absentAll(state observation.State, reason string, now time.Time) []observation.Observation {
	obs := make([]observation.Observation, 0, len(AllSignals))
	for _, sig := range AllSignals {
		obs = append(obs, c.absence(sig, state, reason, now))
	}
	return obs
}

func (c *Collector) measured(sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) observation.Observation {
	all := append([]observation.Option{
		observation.WithSource(SourceName),
		observation.WithValidFor(c.validFor),
		observation.WithCollectedAt(observedAt),
	}, opts...)
	o, err := observation.Measured(c.resource(), sig, value, observedAt, all...)
	if err != nil {
		return c.absence(sig, observation.StateCollectionFailed,
			"internal error building observation: "+err.Error(), observedAt)
	}
	return o
}

func (c *Collector) absence(sig observation.SignalID, state observation.State, reason string, observedAt time.Time) observation.Observation {
	opts := []observation.Option{observation.WithSource(SourceName), observation.WithCollectedAt(observedAt)}
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
		panic(fmt.Sprintf("fpp brightness collector %q: absence(%q, %q) unexpectedly failed: %v", c.instanceID, sig, state, err))
	}
	return o
}

// httpStatusError carries a non-2xx status so Poll can tell a 404, which
// means the plugin is not installed, from every other failure.
type httpStatusError struct{ StatusCode int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.StatusCode)
}

// classifyFetchError renders err as a short failure class an operator can
// act on, never more of the URL than was already configured.
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

// init fails at package load rather than at the first poll if a signal
// constant here is malformed.
func init() {
	for _, sig := range AllSignals {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic(fmt.Sprintf("fppbrightness: invalid signal ID declared by this package: %v", err))
		}
	}
}
