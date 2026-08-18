package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	agentconfig "github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func decodeHello(t *testing.T, payload []byte) (mqttproto.Envelope, mqttproto.HelloPayload) {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	hello, err := mqttproto.DecodeHelloPayload(env)
	if err != nil {
		t.Fatalf("DecodeHelloPayload() error = %v", err)
	}
	return env, hello
}

func decodeLWT(t *testing.T, payload []byte) (mqttproto.Envelope, mqttproto.LWTPayload) {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	return env, lwt
}

// withStubCapabilityDetector replaces capabilityDetector for the duration
// of one test, restoring the real (gst-launch-shelling) detector via
// t.Cleanup. Every test in this file that leaves Config.Capabilities empty
// uses this, so the suite stays hermetic and does not depend on what is or
// is not installed on the machine running it — real detection is proven
// separately, and for real, by capabilities_test.go.
func withStubCapabilityDetector(t *testing.T, set capability.Set) {
	t.Helper()
	prev := capabilityDetector
	capabilityDetector = func(context.Context) capability.Set { return set }
	t.Cleanup(func() { capabilityDetector = prev })
}

func TestPublishHelloTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()
	cfg := agentconfig.Config{
		NodeID:    "media-03",
		NodeLabel: "Media Node 03",
	}
	startedAt := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)

	// caps passed explicitly and empty: since review finding 14, publishHello
	// performs no detection of its own — it publishes exactly what its
	// caller resolved. Advertising a capability the agent does not have is
	// exactly what this must never do, regardless of where that guarantee
	// now lives.
	if _, err := publishHello(context.Background(), pub, cfg, "boot-1", startedAt, capability.Set{}); err != nil {
		t.Fatalf("publishHello() error = %v", err)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	c := calls[0]

	wantTopic, err := mqttproto.HelloTopic("media-03")
	if err != nil {
		t.Fatalf("HelloTopic() error = %v", err)
	}
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.HelloDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.HelloDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.HelloDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v", c.retain, mqttproto.HelloDeliveryPolicy.Retain)
	}

	_, hello := decodeHello(t, c.payload)
	if hello.Label != "Media Node 03" {
		t.Errorf("Label = %q, want %q", hello.Label, "Media Node 03")
	}
	if hello.BootID != "boot-1" {
		t.Errorf("BootID = %q, want %q", hello.BootID, "boot-1")
	}
	if !hello.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", hello.StartedAt, startedAt)
	}
	if len(hello.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty (advertising a capability the agent does not have is exactly what this must never do)", hello.Capabilities)
	}
}

// resetCapabilityCacheForTest clears detectedCapabilityCache (process-
// lifetime, shared across every test in this package) before a test runs
// and restores it to empty afterward via t.Cleanup, so tests that exercise
// capabilitiesForImmediateHello / scheduleCapabilityDetection do not leak
// state into one another.
func resetCapabilityCacheForTest(t *testing.T) {
	t.Helper()
	detectedCapabilityCache.reset()
	t.Cleanup(detectedCapabilityCache.reset)
}

// TestCapabilitiesForImmediateHelloPrefersOverride proves the override
// half of the precedence publishHello itself used to own: a non-empty
// Config.Capabilities (the SHOWMESH_NODE_CAPABILITIES override) wins
// outright over anything sitting in the detection cache, even a populated
// one — an operator override must never be shadowed by a stale detection
// result.
func TestCapabilitiesForImmediateHelloPrefersOverride(t *testing.T) {
	resetCapabilityCacheForTest(t)
	detectedCapabilityCache.store(capability.Set{{ID: "should-not-be-used", Version: 1}})

	cfg := agentconfig.Config{
		NodeID:       "media-03",
		Capabilities: capability.Set{{ID: "matrix.render", Version: 1}},
	}

	got := capabilitiesForImmediateHello(cfg)
	if len(got) != 1 || got[0].ID != "matrix.render" {
		t.Errorf("capabilitiesForImmediateHello(cfg) = %v, want the one configured capability (this env var exists precisely to allow that override, even over a populated cache)", got)
	}
}

// TestCapabilitiesForImmediateHelloUsesCacheWhenNoOverride proves the other
// half: an empty Config.Capabilities falls through to whatever
// detectedCapabilityCache last stored, with no probing performed by this
// function at all (it takes no context and cannot shell out to anything).
func TestCapabilitiesForImmediateHelloUsesCacheWhenNoOverride(t *testing.T) {
	resetCapabilityCacheForTest(t)
	detectedCapabilityCache.store(capability.Set{{ID: "transport.ndi.send", Version: 1}})

	cfg := agentconfig.Config{NodeID: "media-03"}

	got := capabilitiesForImmediateHello(cfg)
	if len(got) != 1 || got[0].ID != "transport.ndi.send" {
		t.Errorf("capabilitiesForImmediateHello(cfg) = %v, want the one cached capability", got)
	}
}

// TestCapabilitiesForImmediateHelloEmptyBeforeFirstDetection proves the
// honest zero-value case: a process that has never completed a detection
// run (detectedCapabilityCache.have == false) advertises nothing rather
// than fabricating a value, on this node's very first connect.
func TestCapabilitiesForImmediateHelloEmptyBeforeFirstDetection(t *testing.T) {
	resetCapabilityCacheForTest(t)

	got := capabilitiesForImmediateHello(agentconfig.Config{NodeID: "media-03"})
	if len(got) != 0 {
		t.Errorf("capabilitiesForImmediateHello(cfg) = %v, want empty before any detection has ever completed", got)
	}
}

// TestScheduleCapabilityDetectionSkipsWhenOverrideConfigured proves
// scheduleCapabilityDetection never calls capabilityDetector, and never
// publishes anything, when an operator override is configured — matching
// capabilitiesForImmediateHello's own precedence: an override is never
// probed for.
func TestScheduleCapabilityDetectionSkipsWhenOverrideConfigured(t *testing.T) {
	resetCapabilityCacheForTest(t)
	prev := capabilityDetector
	capabilityDetector = func(context.Context) capability.Set {
		t.Fatal("capabilityDetector called despite a configured override")
		return nil
	}
	t.Cleanup(func() { capabilityDetector = prev })

	pub := newFakePublisher()
	cfg := agentconfig.Config{
		NodeID:       "media-03",
		Capabilities: capability.Set{{ID: "matrix.render", Version: 1}},
	}

	scheduleCapabilityDetection(context.Background(), pub, cfg, "boot-1", time.Now(), discardLogger())

	if calls := pub.snapshot(); len(calls) != 0 {
		t.Errorf("len(calls) = %d, want 0: an override configuration has nothing to detect and nothing to republish", len(calls))
	}
}

// TestScheduleCapabilityDetectionStoresAndRepublishes proves
// scheduleCapabilityDetection's own job when there is no override: run
// detection, cache the result, and publish a fresh hello carrying it.
func TestScheduleCapabilityDetectionStoresAndRepublishes(t *testing.T) {
	resetCapabilityCacheForTest(t)
	withStubCapabilityDetector(t, capability.Set{{ID: "transport.ndi.send", Version: 1}})

	pub := newFakePublisher()
	cfg := agentconfig.Config{NodeID: "media-03"}

	scheduleCapabilityDetection(context.Background(), pub, cfg, "boot-1", time.Now(), discardLogger())

	cached, have := detectedCapabilityCache.snapshot()
	if !have || len(cached) != 1 || cached[0].ID != "transport.ndi.send" {
		t.Errorf("detectedCapabilityCache after scheduleCapabilityDetection = (%v, %v), want the detected set cached", cached, have)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (the post-detection hello republish)", len(calls))
	}
	_, hello := decodeHello(t, calls[0].payload)
	if len(hello.Capabilities) != 1 || hello.Capabilities[0].ID != "transport.ndi.send" {
		t.Errorf("Capabilities = %v, want the one detected capability", hello.Capabilities)
	}
}

func TestPublishOnlineTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()

	if err := publishOnline(context.Background(), pub, "media-03"); err != nil {
		t.Fatalf("publishOnline() error = %v", err)
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	c := calls[0]

	wantTopic, err := mqttproto.LWTTopic("media-03")
	if err != nil {
		t.Fatalf("LWTTopic() error = %v", err)
	}
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", c.retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	_, lwt := decodeLWT(t, c.payload)
	if !lwt.Online {
		t.Errorf("Online = false, want true")
	}
}

func TestPublishOfflineTopicRetainQoSAndPayload(t *testing.T) {
	pub := newFakePublisher()

	if err := publishOffline(context.Background(), pub, "media-03", "clean shutdown"); err != nil {
		t.Fatalf("publishOffline() error = %v", err)
	}

	c := pub.snapshot()[0]
	wantTopic, _ := mqttproto.LWTTopic("media-03")
	if c.topic != wantTopic {
		t.Errorf("topic = %q, want %q", c.topic, wantTopic)
	}
	if c.qos != mqttproto.LWTDeliveryPolicy.QoS {
		t.Errorf("qos = %d, want %d", c.qos, mqttproto.LWTDeliveryPolicy.QoS)
	}
	if c.retain != mqttproto.LWTDeliveryPolicy.Retain {
		t.Errorf("retain = %v, want %v (mqttproto.LWTDeliveryPolicy.Retain)", c.retain, mqttproto.LWTDeliveryPolicy.Retain)
	}

	_, lwt := decodeLWT(t, c.payload)
	if lwt.Online {
		t.Errorf("Online = true, want false")
	}
	if lwt.Reason != "clean shutdown" {
		t.Errorf("Reason = %q, want %q", lwt.Reason, "clean shutdown")
	}
}

func TestPublishAdvertisementPublishesHelloThenOnline(t *testing.T) {
	pub := newFakePublisher()
	// An explicit override, not an empty Config.Capabilities: this test is
	// about publish ordering, not detection, and an override means
	// scheduleCapabilityDetection's background goroutine is a guaranteed
	// no-op (see TestScheduleCapabilityDetectionSkipsWhenOverrideConfigured)
	// rather than a source of a nondeterministic third publish call racing
	// this test's assertions.
	cfg := agentconfig.Config{NodeID: "media-03", Capabilities: capability.Set{{ID: "matrix.render", Version: 1}}}

	publishAdvertisement(context.Background(), pub, cfg, "boot-1", time.Now(), discardLogger())

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2 (hello, then lwt online=true)", len(calls))
	}

	helloTopic, _ := mqttproto.HelloTopic("media-03")
	lwtTopic, _ := mqttproto.LWTTopic("media-03")

	if calls[0].topic != helloTopic {
		t.Errorf("first publish topic = %q, want hello topic %q", calls[0].topic, helloTopic)
	}
	if calls[1].topic != lwtTopic {
		t.Errorf("second publish topic = %q, want lwt topic %q", calls[1].topic, lwtTopic)
	}
}

// TestPublishAdvertisementACLRejectionLogsDistinctlyAndStillAttemptsOnline
// is advertise.go's half of the ADR-024 decision 10 "surface it as evidence
// distinct from an unreachable broker" requirement: an ACL rejection on the
// hello publish must be logged with a message naming the rejection (not
// folded into the generic "failed to publish hello" line), and — matching
// publishAdvertisement's existing "both publishes are individually
// best-effort" contract — must not stop the online publish that follows it.
func TestPublishAdvertisementACLRejectionLogsDistinctlyAndStillAttemptsOnline(t *testing.T) {
	pub := newFakePublisher()
	pub.rejectOn = map[int]bool{0: true} // the hello publish (call 0) is ACL-rejected
	logger, logs := capturingLogger()

	// Override configured for the same reason as
	// TestPublishAdvertisementPublishesHelloThenOnline: this test is about
	// ACL-rejection logging and publish ordering, not detection, and a
	// background scheduleCapabilityDetection call would otherwise be a
	// nondeterministic third publish racing this test's exact-2 assertion.
	cfg := agentconfig.Config{NodeID: "media-03", Capabilities: capability.Set{{ID: "matrix.render", Version: 1}}}
	publishAdvertisement(context.Background(), pub, cfg, "boot-1", time.Now(), logger)

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2: an ACL-rejected hello publish must not prevent the online publish attempt that follows it", len(calls))
	}

	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "not authorized") {
		t.Errorf("logs = %q, want an ERROR-level line naming the ACL rejection for the hello publish", logged)
	}
	if strings.Contains(logged, `msg="failed to publish hello"`) {
		t.Errorf("logs = %q, an ACL rejection must not be logged with the plain generic-failure message (no distinguishing detail)", logged)
	}
}

// TestPublishAdvertisementReturnsPromptlyWhenCapabilityDetectionHangs is
// review finding 14's regression test. Before the fix, publishHello ran
// capabilityDetector synchronously inside publishAdvertisement's
// advertiseTimeout-bounded pubCtx, so a wedged probe (a real failure mode:
// a hung GPU/driver wedges gst-launch-1.0) could consume the whole budget
// and leave nothing to publish the hello with — the node then vanishes
// from inventory on every broker reconnect. This proves the fix: detection
// runs in its own background goroutine on its own capabilityDetectionTimeout,
// so a detector that never returns on its own must never be able to delay
// publishAdvertisement's hello/online pair at all.
func TestPublishAdvertisementReturnsPromptlyWhenCapabilityDetectionHangs(t *testing.T) {
	resetCapabilityCacheForTest(t)

	// capabilityDetectionTimeout is a var precisely so this test can shrink
	// it, matching pipeline/probe.go's probeTimeout convention; restored via
	// t.Cleanup so no other test in this package inherits a shrunk budget.
	prevTimeout := capabilityDetectionTimeout
	capabilityDetectionTimeout = 100 * time.Millisecond
	t.Cleanup(func() { capabilityDetectionTimeout = prevTimeout })

	// This stub mirrors real detectCapabilities' own contract (runProbe's
	// ctx.Done() branch): it never returns on its own, only when its ctx is
	// canceled — exactly what a wedged gst-launch-1.0 subprocess looks like
	// from the caller's side.
	prevDetector := capabilityDetector
	capabilityDetector = func(ctx context.Context) capability.Set {
		<-ctx.Done()
		return capability.Set{}
	}
	t.Cleanup(func() { capabilityDetector = prevDetector })

	pub := newFakePublisher()
	cfg := agentconfig.Config{NodeID: "media-03"} // no override: detection applies

	start := time.Now()
	publishAdvertisement(context.Background(), pub, cfg, "boot-1", time.Now(), discardLogger())
	elapsed := time.Since(start)

	// A generous margin under capabilityDetectionTimeout: publishAdvertisement
	// must return long before the wedged detector ever resolves, not merely
	// "eventually."
	if elapsed >= 50*time.Millisecond {
		t.Errorf("publishAdvertisement took %v to return with capability detection wedged; want near-instant, well under capabilityDetectionTimeout (%v) — a hung probe must never be able to delay the hello/online publish pair", elapsed, capabilityDetectionTimeout)
	}

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2 (hello, then lwt online=true) even with capability detection wedged — a hung probe must never cost this node its hello", len(calls))
	}
	helloTopic, _ := mqttproto.HelloTopic("media-03")
	if calls[0].topic != helloTopic {
		t.Errorf("first publish topic = %q, want hello topic %q", calls[0].topic, helloTopic)
	}
	_, hello := decodeHello(t, calls[0].payload)
	if len(hello.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty: no detection has completed yet at the moment this hello had to be published", hello.Capabilities)
	}

	// The background detection goroutine must still be running (or about
	// to finish resolving via its own timeout) — confirm it eventually
	// republishes rather than silently disappearing. Polled (bounded, short
	// interval) rather than a single fixed sleep, and independent of
	// pub.notify's buffering, which already holds the hello/online signals
	// from above and would make a single-receive wait ambiguous about which
	// publish it was even seeing.
	deadline := time.Now().Add(2 * time.Second)
	for len(pub.snapshot()) < 3 {
		if time.Now().After(deadline) {
			t.Fatal("no republish observed after the wedged detector's own timeout elapsed; a hung probe must degrade this pass's capability set, not silently drop the reconnect's detection run")
		}
		time.Sleep(5 * time.Millisecond)
	}

	calls = pub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("len(calls) = %d, want 3 (hello, online, then the post-timeout republish)", len(calls))
	}
	if calls[2].topic != helloTopic {
		t.Errorf("third publish topic = %q, want hello topic %q (the post-detection republish)", calls[2].topic, helloTopic)
	}

	cached, have := detectedCapabilityCache.snapshot()
	if !have || len(cached) != 0 {
		t.Errorf("detectedCapabilityCache after the wedged probe's timeout = (%v, %v), want (empty, true): the timeout resolves to a completed detection pass that found nothing, not to detection never having run", cached, have)
	}
}
