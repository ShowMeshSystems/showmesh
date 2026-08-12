package fpp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fixedClock returns a func() time.Time a test can move by assigning to
// *now directly (no atomics needed: every test using this drives Poll
// synchronously from the test goroutine).
func fixedClock(now *time.Time) func() time.Time {
	return func() time.Time { return *now }
}

// findSignal returns the observation for sig, failing the test if it is
// not present exactly once — every signal this collector produces should
// appear in every Poll result (as either a value or an absence; see
// contract section 6.2, "absent evidence is stated, never omitted").
func findSignal(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	var found []observation.Observation
	for _, o := range obs {
		if o.Signal == sig {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("signal %q appeared %d times in Poll result, want exactly 1", sig, len(found))
	}
	return found[0]
}

func newTestCollector(t *testing.T, baseURL string, opts Options) *Collector {
	t.Helper()
	c, err := New("player-01", baseURL, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

// --- Core behavior against real captured bodies -----------------------

// TestPollMultiSyncEnabledFromRealBody verifies, against a real 9.5.3
// capture where MultiSync was actually enabled on the daemon, that
// fpp.multisync.enabled is StateCurrent with value true. This is the
// "enabled" half of the trap regression; see
// TestPollMultiSyncDisabledIsPositiveFactNotFailure for the "disabled" half
// and TestMultiSyncEnabledNeverReadsTheSettingsEndpoint for the strongest
// version of this test.
func TestPollMultiSyncEnabledFromRealBody(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_enabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_enabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	got := findSignal(t, obs, SignalMultiSyncEnabled)
	if got.Value != true {
		t.Errorf("fpp.multisync.enabled value = %v, want true", got.Value)
	}
	if got.StateAt(now) != observation.StateCurrent {
		t.Errorf("fpp.multisync.enabled state = %v, want current", got.StateAt(now))
	}
	srv.assertOnlyGET(t)
}

// TestPollMultiSyncDisabledIsPositiveFactNotFailure verifies, against a
// real 9.5.3 capture where MultiSync was actually disabled, that
// fpp.multisync.enabled reports StateCurrent with value FALSE — a
// positively observed configuration fact, per contract section 3.1, not
// StateCollectionFailed and not a signal that should ever drag health
// toward degraded/failed on its own.
func TestPollMultiSyncDisabledIsPositiveFactNotFailure(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_disabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	got := findSignal(t, obs, SignalMultiSyncEnabled)
	if got.Value != false {
		t.Errorf("fpp.multisync.enabled value = %v, want false", got.Value)
	}
	if got.Absence != "" {
		t.Errorf("fpp.multisync.enabled Absence = %q, want empty (this is a value, not an absence)", got.Absence)
	}
	if got.StateAt(now) != observation.StateCurrent {
		t.Errorf("fpp.multisync.enabled state = %v, want current", got.StateAt(now))
	}
}

// TestMultiSyncEnabledNeverReadsTheSettingsEndpoint is the test the Task C
// spec calls "worth more than all the others": it proves the collector
// never even requests anything under /api/settings, regardless of what
// such an endpoint would return. This is stronger than asserting the
// decoded value, because it holds even if a future FPP version changes the
// settings endpoint's shape yet again — the collector simply never asks it
// the question.
//
// Step 3 review finding 4.3: an earlier version of this test trapped only
// the one exact path "/api/settings/MultiSyncEnabled". Confirmed by
// mutation, that version does fail correctly if the collector is changed to
// call that exact path again — but a plausible refactor calling a
// different path in the same family instead (e.g. "/api/settings", a
// hypothetical "read every setting at once" endpoint) is a different string
// and sailed straight through untrapped, leaving the whole package green.
// Nothing pinned the *set* of endpoints polled either, so an extra request
// added anywhere in Poll — to any path, settings-related or not — was
// invisible to this test.
//
// The fix is two assertions, not one: trapPrefix("/api/settings") panics on
// ANY request under that whole family, not only the one leaf this test used
// to know about by name; and assertExactPathSet pins the complete,
// literal set of paths a single Poll call is allowed to touch, so a request
// added anywhere — under /api/settings or anywhere else — makes this test
// fail even if nobody remembered to write a trap for that specific new
// path.
func TestMultiSyncEnabledNeverReadsTheSettingsEndpoint(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_disabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
	srv.trapPrefix("/api/settings")
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	// Pin the exact, complete set of endpoints one Poll call is allowed to
	// touch — not merely that one forbidden path was avoided. Step 5 added
	// /api/fppd/ports and /api/system/info to this set (contract section
	// 3); both are unregistered on this test server and so 404, which is
	// fine — this assertion only cares which paths were requested, not
	// what they returned.
	srv.assertExactPathSet(t, "/api/fppd/status", "/api/fppd/multiSyncSystems", "/api/fppd/ports", "/api/system/info")

	// The multisync.enabled signal must still be correctly derived from
	// status.multisync (which this capture set to false) — proving the
	// collector answered the question, just from the right source.
	got := findSignal(t, obs, SignalMultiSyncEnabled)
	if got.Value != false {
		t.Errorf("fpp.multisync.enabled value = %v, want false (from status.multisync)", got.Value)
	}
}

// TestPollHealthyStatusAllSignals is a broad assertion, against the same
// real "enabled" capture, that every static status-derived signal is
// present and carries the exact value (or, for the handful this fixture
// cannot answer, the exact Unsupported absence) the real daemon's document
// implies — pinning the whole table at once rather than one signal per
// test.
//
// This bench capture (status_multisync_enabled.json) is player mode with
// no "warnings" key and no "sensors" array, so four of the static signals
// are legitimately Unsupported here: fpp.media.filename and
// fpp.position.elapsed.seconds (remote-mode-only fields, contract section
// 3.3) and fpp.warnings.count/fpp.warnings.summary (contract section 3.4).
func TestPollHealthyStatusAllSignals(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_enabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_enabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	wantValue := map[observation.SignalID]any{
		SignalReachable:              true,
		SignalVersion:                "9.5.3",
		SignalMode:                   "player",
		SignalStatus:                 "idle",
		SignalPlaylistName:           "",
		SignalSequenceName:           "",
		SignalPositionSeconds:        float64(0),
		SignalPositionRemaining:      float64(0),
		SignalMultiSyncEnabled:       true,
		SignalMultiSyncSystems:       int64(1),
		SignalSchedulerStatus:        "idle",
		SignalUptimeSeconds:          float64(37),
		SignalSongName:               "",
		SignalPlaylistRepeatMode:     int64(0),
		SignalPlaylistIndex:          int64(0),
		SignalPlaylistCount:          int64(0),
		SignalPlaylistType:           "",
		SignalSchedulerEnabled:       true,
		SignalSchedulerNextPlaylist:  "No playlist scheduled.",
		SignalSchedulerNextStartTime: int64(0),
		SignalFPPDState:              "running",
		SignalPowerBad:               false,
		SignalBridging:               false,
		SignalChannelInputsEnabled:   false,
		SignalChannelOutputsEnabled:  false,
		SignalBranch:                 "(HEAD detached at 9.5.3)",
		SignalUUID:                   "M4-ce13ae2b62b238e533a474876a7a8b88",
		SignalHostName:               "fpp-master",
		SignalVolume:                 int64(0),
		SignalMQTTConfigured:         false,
		SignalMQTTConnected:          false,
	}
	wantUnsupported := []observation.SignalID{
		SignalMediaFilename, SignalPositionElapsedSeconds,
		SignalWarningsCount, SignalWarningsSummary,
	}

	for sig, wantVal := range wantValue {
		got := findSignal(t, obs, sig)
		if got.Absence != "" {
			t.Errorf("signal %q: Absence = %q (reason %q), want a value (%v)", sig, got.Absence, got.Reason, wantVal)
			continue
		}
		if got.Value != wantVal {
			t.Errorf("signal %q value = %v (%T), want %v (%T)", sig, got.Value, got.Value, wantVal, wantVal)
		}
		if got.StateAt(now) != observation.StateCurrent {
			t.Errorf("signal %q state = %v, want current", sig, got.StateAt(now))
		}
		if got.Source != sourceName {
			t.Errorf("signal %q source = %q, want %q", sig, got.Source, sourceName)
		}
	}

	for _, sig := range wantUnsupported {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateUnsupported {
			t.Errorf("signal %q Absence = %q, want unsupported", sig, got.Absence)
		}
		if got.Reason == "" {
			t.Errorf("signal %q Reason is empty, want an explanation", sig)
		}
	}

	// wantValue + SignalReachable is checked above but SignalReachable and
	// SignalMultiSyncSystems are not part of allStatusSignals (see its doc
	// comment) — the "+2" below accounts for exactly those two.
	if got, want := len(wantValue)+len(wantUnsupported), len(allStatusSignals)+2; got != want {
		t.Fatalf("test bug: want tables cover %d signals, expected %d (len(allStatusSignals)+2)", got, want)
	}
}

// --- Failure and absence handling --------------------------------------

// TestPollUnreachableProducesCollectionFailedNeverFabricatedFalse verifies
// that when the status request fails outright (connection refused, here
// simulated by pointing at a closed port), every data signal — including,
// critically, fpp.multisync.enabled — reports StateCollectionFailed with a
// reason, never a fabricated `false`. This is the other half of "never let
// a failed read become a negative answer": section 3.1 of the contract is
// about the disabled case, this test is about the failed-to-read case.
func TestPollUnreachableProducesCollectionFailedNeverFabricatedFalse(t *testing.T) {
	// An httptest.Server that has been closed still has a syntactically
	// valid URL that nothing is listening on — connection refused, not a
	// DNS or parse error, which is exactly the "instance is off" case this
	// test targets.
	srv := newFPPServer()
	ts := srv.start(t)
	ts.Close()

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{
		Now:            fixedClock(&now),
		RequestTimeout: 2 * time.Second,
	})
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll complete = false for a totally-unreachable instance, want true: a real attempt that found every endpoint down is still the complete, honest answer for this cycle, never the same claim as a skipped/backed-off poll (see Poll's doc comment)")
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if reachable.Reason == "" {
		t.Errorf("fpp.reachable Reason is empty, want a failure class")
	}

	enabled := findSignal(t, obs, SignalMultiSyncEnabled)
	if enabled.Value != nil {
		t.Fatalf("fpp.multisync.enabled Value = %v, want nil (must never fabricate false on failure)", enabled.Value)
	}
	if enabled.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.multisync.enabled Absence = %q, want collection_failed", enabled.Absence)
	}

	for _, sig := range allStatusSignals {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed when the instance is unreachable", sig, got.Absence)
		}
	}

	// The whole server is down (ts.Close()), so /api/fppd/ports and
	// /api/system/info fail too — their own independent
	// collection_failed, via the same path as multiSyncSystems below, not
	// because status failing "took them down."
	for _, sig := range portsFailureSignals {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed when the instance is unreachable", sig, got.Absence)
		}
	}
	for _, sig := range systemInfoStaticSignals {
		got := findSignal(t, obs, sig)
		if got.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed when the instance is unreachable", sig, got.Absence)
		}
	}

	msSig := findSignal(t, obs, SignalMultiSyncSystems)
	if msSig.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.multisync.systems Absence = %q, want collection_failed when the instance is unreachable", msSig.Absence)
	}
}

// TestPollHTTPErrorStatusProducesCollectionFailed verifies a non-2xx HTTP
// response (a real, if less common, FPP failure mode — e.g. mid-restart)
// is treated the same as a network failure: collection_failed, with the
// status code named in the reason.
func TestPollHTTPErrorStatusProducesCollectionFailed(t *testing.T) {
	srv := newFPPServer()
	srv.serveStatus("/api/fppd/status", http.StatusServiceUnavailable)
	srv.serveStatus("/api/fppd/multiSyncSystems", http.StatusServiceUnavailable)
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Fatalf("fpp.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if want := "503"; !contains(reachable.Reason, want) {
		t.Errorf("fpp.reachable Reason = %q, want it to mention the HTTP status %s", reachable.Reason, want)
	}
}

// TestPollDecodeErrorDegradesOnlyAffectedSignal proves the specific
// property the Task C spec names by example: a single field with an
// unexpected JSON type must degrade only that field's signal, not the
// whole document. The body here is a mutated COPY of the real "disabled"
// capture — every byte except the one field under test is the real
// capture — with current_sequence changed from a JSON string to a JSON
// number, a shape no real FPP response in this session's captures
// produced, but exactly the class of defect decode.go's per-field
// extraction exists to contain.
func TestPollDecodeErrorDegradesOnlyAffectedSignal(t *testing.T) {
	body := mutateJSONField(t, loadTestdata(t, "status_multisync_disabled.json"), "current_sequence", 12345)

	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", body)
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	broken := findSignal(t, obs, SignalSequenceName)
	if broken.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.sequence.name Absence = %q, want collection_failed (the field under test was mutated to the wrong JSON type)", broken.Absence)
	}

	// Every other signal must be entirely unaffected — decoded from the
	// same document as if the mutated field did not exist.
	stillGood := findSignal(t, obs, SignalMultiSyncEnabled)
	if stillGood.Value != false || stillGood.Absence != "" {
		t.Errorf("fpp.multisync.enabled = value %v absence %q, want value false absence \"\" — one bad field must not take down the rest of the document", stillGood.Value, stillGood.Absence)
	}
	version := findSignal(t, obs, SignalVersion)
	if version.Value != "9.5.3" {
		t.Errorf("fpp.version = %v, want unaffected \"9.5.3\"", version.Value)
	}
}

// TestPollNumericStringFieldsDecodeFromRealBody pins the exact hazard
// found live: seconds_played and seconds_remaining arrive as JSON STRINGS
// ("0") in a real capture, while uptimeSeconds arrives as a genuine JSON
// number, in the SAME document. A naive struct with
// matching Go number fields would fail to decode the whole body; this test
// proves both encodings are tolerated from the actual bytes a real fppd
// sent, not a hand-written approximation of them.
func TestPollNumericStringFieldsDecodeFromRealBody(t *testing.T) {
	body := loadTestdata(t, "status_multisync_disabled.json")

	// Sanity-check the hazard is actually present in the fixture, so this
	// test cannot silently stop testing anything if the fixture is ever
	// replaced.
	if !bytes.Contains(body, []byte(`"seconds_played":"0"`)) {
		t.Fatalf("test fixture no longer contains the string-typed seconds_played hazard this test exists to check")
	}
	if bytes.Contains(body, []byte(`"uptimeSeconds":"`)) {
		t.Fatalf("test fixture's uptimeSeconds is unexpectedly string-typed; this test assumes it is a real JSON number")
	}

	doc, err := decodeRawDoc(body)
	if err != nil {
		t.Fatalf("decodeRawDoc() error = %v", err)
	}

	played, err := doc.numberField("seconds_played")
	if err != nil {
		t.Errorf("numberField(seconds_played) error = %v, want it to tolerate the string encoding", err)
	}
	if played != 0 {
		t.Errorf("seconds_played = %v, want 0", played)
	}

	uptime, err := doc.numberField("uptimeSeconds")
	if err != nil {
		t.Errorf("numberField(uptimeSeconds) error = %v, want it to accept the numeric encoding", err)
	}
	if uptime != 47 {
		t.Errorf("uptimeSeconds = %v, want 47", uptime)
	}
}

// TestPollEmptyPlaylistAndSequenceAreCurrentNotAbsent verifies the "model
// that honestly" instruction: an idle FPP's empty current_playlist.playlist
// and current_sequence are real, current values, not absences.
func TestPollEmptyPlaylistAndSequenceAreCurrentNotAbsent(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_disabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{Now: fixedClock(&now)})
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{SignalPlaylistName, SignalSequenceName} {
		got := findSignal(t, obs, sig)
		if got.Absence != "" {
			t.Errorf("signal %q Absence = %q, want empty: an empty string is a value, not an absence", sig, got.Absence)
		}
		if got.Value != "" {
			t.Errorf("signal %q value = %v, want empty string", sig, got.Value)
		}
	}
}

// TestFetchRejectsOversizedBody verifies OBSERVABILITY section 5's
// requirement that a collector bound its response reads: a body larger
// than MaxResponseBytes must be an error, never a silently truncated
// document.
func TestFetchRejectsOversizedBody(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), 1024)

	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", oversized)
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{
		Now:              fixedClock(&now),
		MaxResponseBytes: 16, // far smaller than oversized, deliberately
	})
	obs, _ := c.Poll(context.Background())

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.reachable Absence = %q, want collection_failed for an oversized body", reachable.Absence)
	}
}

// --- Backoff -------------------------------------------------------------

// TestBackoffSkipsRequestsUntilNextAttemptAt verifies that after a failure,
// the collector does not immediately retry: the next Poll call, at the
// same instant, makes no request at all, and only resumes once the fake
// clock advances past the scheduled backoff.
func TestBackoffSkipsRequestsUntilNextAttemptAt(t *testing.T) {
	srv := newFPPServer()
	srv.serveStatus("/api/fppd/status", http.StatusServiceUnavailable)
	srv.serveStatus("/api/fppd/multiSyncSystems", http.StatusServiceUnavailable)
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{
		Now:         fixedClock(&now),
		BackoffBase: time.Minute, // long enough that "no time has passed" definitely stays inside it
	})

	obs1, complete1 := c.Poll(context.Background())
	if len(obs1) == 0 {
		t.Fatalf("first Poll returned no observations, want a real (failed) attempt")
	}
	if !complete1 {
		t.Errorf("first Poll complete = false, want true: a real attempt (even a failed one) is complete")
	}
	hitsAfterFirst := srv.hitCount("/api/fppd/status")
	if hitsAfterFirst != 1 {
		t.Fatalf("hits after first Poll = %d, want 1", hitsAfterFirst)
	}

	// Same instant: the collector must be in backoff and skip entirely.
	// complete=false here is the property this collector's Sink depends on
	// to never prune stale rows on a cycle that checked nothing (see
	// Poll's doc comment and internal/coordinator/store's
	// TestReplaceObservationsSkippedPollDeletesNothing).
	obs2, complete2 := c.Poll(context.Background())
	if len(obs2) != 0 {
		t.Errorf("second Poll (still within backoff) returned %d observations, want 0 (skipped)", len(obs2))
	}
	if complete2 {
		t.Errorf("second Poll (skipped under backoff) complete = true, want false: nothing was checked, so this must never be read as \"these are all the signals that exist\"")
	}
	if got := srv.hitCount("/api/fppd/status"); got != hitsAfterFirst {
		t.Errorf("hits after second Poll = %d, want unchanged %d (request should have been skipped)", got, hitsAfterFirst)
	}

	// Advance well past any plausible backoff (base 1 minute, one failure)
	// and confirm the collector tries again.
	now = now.Add(10 * time.Minute)
	obs3, complete3 := c.Poll(context.Background())
	if len(obs3) == 0 {
		t.Fatalf("third Poll (after the backoff window) returned no observations, want a real attempt")
	}
	if !complete3 {
		t.Errorf("third Poll complete = false, want true: this is a real attempt again, not a skip")
	}
	if got := srv.hitCount("/api/fppd/status"); got != hitsAfterFirst+1 {
		t.Errorf("hits after third Poll = %d, want %d (one more real attempt)", got, hitsAfterFirst+1)
	}
}

// TestBackoffResetsOnSuccess verifies a successful poll clears
// consecutiveFailures so the very next Poll call, at the same instant, is
// not skipped — a recovered FPP must not stay artificially backed off.
func TestBackoffResetsOnSuccess(t *testing.T) {
	srv := newFPPServer()
	srv.serveStatus("/api/fppd/status", http.StatusServiceUnavailable)
	srv.serveStatus("/api/fppd/multiSyncSystems", http.StatusServiceUnavailable)
	ts := srv.start(t)

	now := time.Now()
	c := newTestCollector(t, ts.URL, Options{
		Now:         fixedClock(&now),
		BackoffBase: time.Minute,
	})

	c.Poll(context.Background()) // establishes backoff after a failure

	// Recovery: swap in a healthy body for the same path before the next
	// Poll call. httptest lets the handler be replaced at any time since
	// srv.route is consulted per-request.
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_disabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_disabled.json"))

	// Still "in the backoff window" by wall clock, but the collector must
	// have reset nextAttemptAt on... wait, it hasn't polled again yet, so
	// backoff from the FIRST failure is still active. Advance past it once
	// to let the healthy poll happen, then verify the NEXT poll (which
	// would still be inside backoff if the reset had not happened) is not
	// skipped.
	now = now.Add(10 * time.Minute)
	obsHealthy, _ := c.Poll(context.Background())
	if len(obsHealthy) == 0 {
		t.Fatalf("recovery Poll returned no observations")
	}
	if got := findSignal(t, obsHealthy, SignalReachable); got.Value != true {
		t.Fatalf("recovery Poll fpp.reachable = %v, want true", got.Value)
	}

	hitsAfterRecovery := srv.hitCount("/api/fppd/status")

	// Same instant as the successful poll: if backoff had NOT been reset,
	// this would be skipped just like in
	// TestBackoffSkipsRequestsUntilNextAttemptAt. It must not be.
	obsNext, _ := c.Poll(context.Background())
	if len(obsNext) == 0 {
		t.Errorf("Poll immediately after a success returned no observations, want a real attempt (backoff should have been reset)")
	}
	if got := srv.hitCount("/api/fppd/status"); got != hitsAfterRecovery+1 {
		t.Errorf("hits after post-recovery Poll = %d, want %d (backoff must not still apply after a success)", got, hitsAfterRecovery+1)
	}
}

// --- Construction validation ---------------------------------------------

func TestNewRejectsInvalidInstanceID(t *testing.T) {
	if _, err := New("Not_Valid", "http://10.0.1.20", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for an id mqttproto.ValidateNodeID rejects")
	}
}

func TestNewRejectsBadURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"no scheme", "10.0.1.20"},
		{"unsupported scheme", "ftp://10.0.1.20"},
		{"no host", "http://"},
		{"userinfo", "http://user:pass@10.0.1.20"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New("player-01", tt.url, Options{}); err == nil {
				t.Fatalf("New(%q) error = nil, want an error", tt.url)
			}
		})
	}
}

// --- helpers --------------------------------------------------------------

func contains(s, substr string) bool {
	return bytes.Contains([]byte(s), []byte(substr))
}

// mutateJSONField returns a copy of body with key's value replaced by
// newValue, failing the test if key is not present in body — used to
// derive a "real capture plus one deliberately broken field" fixture
// without hand-writing an entire document from scratch.
func mutateJSONField(t *testing.T, body []byte, key string, newValue any) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("mutateJSONField: base body is not a JSON object: %v", err)
	}
	if _, ok := doc[key]; !ok {
		t.Fatalf("mutateJSONField: key %q not present in base body", key)
	}
	raw, err := json.Marshal(newValue)
	if err != nil {
		t.Fatalf("mutateJSONField: marshal newValue: %v", err)
	}
	doc[key] = raw
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("mutateJSONField: marshal mutated document: %v", err)
	}
	return out
}

func TestBackoffDelayNeverExceedsCeiling(t *testing.T) {
	base := 100 * time.Millisecond
	ceiling := time.Second
	for attempt := 1; attempt <= 20; attempt++ {
		for i := 0; i < 20; i++ { // repeat: backoffDelay is randomized (jitter)
			d := backoffDelay(base, ceiling, attempt)
			if d < 0 || d > ceiling {
				t.Fatalf("backoffDelay(attempt=%d) = %v, want in [0, %v]", attempt, d, ceiling)
			}
		}
	}
}

func TestBackoffDelayGrowsWithAttempts(t *testing.T) {
	// Not randomized-value-exact (jitter is random by design), but the
	// ceiling actually reached should grow monotonically until it hits
	// BackoffCeiling. Sample the max observed delay at low attempt counts
	// as a proxy for "the underlying exponential base is actually growing"
	// rather than asserting on any single jittered sample.
	base := 10 * time.Millisecond
	ceiling := time.Hour // effectively unreachable within the attempts tested

	maxAt := func(attempt int) time.Duration {
		var max time.Duration
		for i := 0; i < 200; i++ {
			if d := backoffDelay(base, ceiling, attempt); d > max {
				max = d
			}
		}
		return max
	}

	m1, m2, m3 := maxAt(1), maxAt(2), maxAt(3)
	if m1 >= m2 || m2 >= m3 {
		t.Errorf("max observed backoff delays did not grow with attempts: attempt1=%v attempt2=%v attempt3=%v", m1, m2, m3)
	}
}

// recordingSink implements collector.Sink for
// TestCollectorComposesWithGenericRunner.
type recordingSink struct {
	mu   sync.Mutex
	recs [][]observation.Observation
}

func (s *recordingSink) RecordObservations(ctx context.Context, obs []observation.Observation, complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, obs)
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

func (s *recordingSink) last() []observation.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.recs) == 0 {
		return nil
	}
	return s.recs[len(s.recs)-1]
}

// TestCollectorComposesWithGenericRunner proves this package's Collector
// actually satisfies internal/coordinator/collector.Collector end to end —
// registered with a real collector.Runner, polled, and delivered to a
// Sink — rather than relying on the package-level var assertion in fpp.go
// plus each package's separate unit tests to imply the two compose
// correctly together.
func TestCollectorComposesWithGenericRunner(t *testing.T) {
	srv := newFPPServer()
	srv.serveBody("/api/fppd/status", loadTestdata(t, "status_multisync_enabled.json"))
	srv.serveBody("/api/fppd/multiSyncSystems", loadTestdata(t, "multisync_systems_enabled.json"))
	ts := srv.start(t)

	c := newTestCollector(t, ts.URL, Options{})
	sink := &recordingSink{}
	runner := collector.NewRunner(sink, testLogger(t))
	runner.Add(c, time.Hour) // long enough that only the immediate poll matters

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for sink.count() < 1 {
		select {
		case <-deadline:
			t.Fatalf("Runner never delivered a poll result within 2s")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	<-done

	delivered := sink.last()
	found := false
	for _, o := range delivered {
		if o.Signal == SignalMultiSyncEnabled {
			found = true
			if o.Value != true {
				t.Errorf("fpp.multisync.enabled delivered via Runner = %v, want true", o.Value)
			}
		}
	}
	if !found {
		t.Fatalf("Runner-delivered observations did not include %q", SignalMultiSyncEnabled)
	}
}
