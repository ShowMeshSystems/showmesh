package fppbrightness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const okBody = `{"schemaVersion":1,"ceiling":60,"transitionGain":75,"effectiveOutput":45,"fadeActive":true,"updatedAtMillis":1700000000000}`

func newCollector(t *testing.T, handler http.HandlerFunc) *Collector {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New("bench-fpp", srv.URL, Options{Now: func() time.Time { return time.Unix(1700000000, 0).UTC() }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func bySignal(obs []observation.Observation) map[observation.SignalID]observation.Observation {
	out := make(map[observation.SignalID]observation.Observation, len(obs))
	for _, o := range obs {
		out[o.Signal] = o
	}
	return out
}

// TestPollReportsEveryBrightnessSignal.
func TestPollReportsEveryBrightnessSignal(t *testing.T) {
	c := newCollector(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PluginPath {
			t.Errorf("polled %q, want %q", r.URL.Path, PluginPath)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET: this collector is read-only", r.Method)
		}
		_, _ = w.Write([]byte(okBody))
	})

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatal("complete = false, want true: every signal was answered")
	}
	got := bySignal(obs)
	if len(got) != len(AllSignals) {
		t.Fatalf("got %d signals, want %d", len(got), len(AllSignals))
	}
	for sig, want := range map[observation.SignalID]any{
		SignalCeiling:         int64(60),
		SignalTransitionGain:  int64(75),
		SignalEffectiveOutput: int64(45),
		SignalFadeActive:      true,
	} {
		o := got[sig]
		if o.Absence != "" {
			t.Errorf("%s is absent (%q), want a measured value", sig, o.Absence)
		}
		if o.Value != want {
			t.Errorf("%s value = %v (%T), want %v", sig, o.Value, o.Value, want)
		}
		if o.Source != SourceName {
			t.Errorf("%s source = %q, want %q", sig, o.Source, SourceName)
		}
		if o.Resource.ID != "bench-fpp" || o.Resource.Kind != observation.ResourceFPP {
			t.Errorf("%s resource = %+v, want fpp/bench-fpp", sig, o.Resource)
		}
	}
	for _, sig := range []observation.SignalID{SignalCeiling, SignalTransitionGain, SignalEffectiveOutput} {
		if got[sig].Unit != "percent" {
			t.Errorf("%s unit = %q, want percent", sig, got[sig].Unit)
		}
	}
}

// TestA404RecordsEverySignalAbsentWithThePluginReason is the case an
// operator meets most: the host is up and the plugin is not installed.
func TestA404RecordsEverySignalAbsentWithThePluginReason(t *testing.T) {
	c := newCollector(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatal("complete = false, want true: a 404 is a real answer for this cycle")
	}
	got := bySignal(obs)
	if len(got) != len(AllSignals) {
		t.Fatalf("got %d signals, want %d", len(got), len(AllSignals))
	}
	for _, sig := range AllSignals {
		o := got[sig]
		if o.Absence != observation.StateUnsupported {
			t.Errorf("%s state = %q, want unsupported", sig, o.Absence)
		}
		if o.Reason != absentOn404Reason {
			t.Errorf("%s reason = %q, want %q", sig, o.Reason, absentOn404Reason)
		}
		if o.Value != nil {
			t.Errorf("%s carried a value on a 404: %v", sig, o.Value)
		}
	}
}

// TestAnUnreachableHostRecordsTheTransportError, distinct from a 404: one
// says the plugin is missing, the other says the host is.
func TestAnUnreachableHostRecordsTheTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c, err := New("bench-fpp", url, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatal("complete = false, want true")
	}
	for _, o := range obs {
		if o.Absence != observation.StateCollectionFailed {
			t.Errorf("%s state = %q, want collection_failed", o.Signal, o.Absence)
		}
		if o.Reason == "" || o.Reason == absentOn404Reason {
			t.Errorf("%s reason = %q, want the transport error", o.Signal, o.Reason)
		}
	}
}

// TestAMissingFieldIsAbsentRatherThanAPlausibleZero.
func TestAMissingFieldIsAbsentRatherThanAPlausibleZero(t *testing.T) {
	c := newCollector(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schemaVersion":1,"ceiling":60}`))
	})

	got := bySignal(mustPoll(t, c))
	if got[SignalCeiling].Value != int64(60) {
		t.Errorf("ceiling = %v, want 60", got[SignalCeiling].Value)
	}
	for _, sig := range []observation.SignalID{SignalTransitionGain, SignalEffectiveOutput, SignalFadeActive} {
		if got[sig].Absence != observation.StateNotCollected {
			t.Errorf("%s state = %q, want not_collected", sig, got[sig].Absence)
		}
	}
}

// TestAnUndecodableBodyFailsEverySignal.
func TestAnUndecodableBodyFailsEverySignal(t *testing.T) {
	c := newCollector(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	for _, o := range mustPoll(t, c) {
		if o.Absence != observation.StateCollectionFailed {
			t.Errorf("%s state = %q, want collection_failed", o.Signal, o.Absence)
		}
	}
}

// TestCollectorIDNeverCollidesWithTheRESTCollectors: both are registered
// on one Runner, which silently ignores a duplicate id.
func TestCollectorIDNeverCollidesWithTheRESTCollectors(t *testing.T) {
	c, err := New("bench-fpp", "http://fpp.invalid", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.ID() == "bench-fpp" {
		t.Fatal("this collector's Runner id is the bare instance id, which the REST collector already holds")
	}
	if c.ID() != CollectorID("bench-fpp") {
		t.Fatalf("ID() = %q, want %q", c.ID(), CollectorID("bench-fpp"))
	}
}

// TestNewRefusesAnUnusableBaseURL.
func TestNewRefusesAnUnusableBaseURL(t *testing.T) {
	for _, url := range []string{
		"", "ftp://fpp.invalid", "http://", "http://user:pass@fpp.invalid",
		"http://fpp.invalid/api", "http://fpp.invalid?a=1", "http://fpp.invalid#f",
	} {
		if _, err := New("bench-fpp", url, Options{}); err == nil {
			t.Errorf("New(%q) succeeded, want an error", url)
		}
	}
	if _, err := New("Not Valid!", "http://fpp.invalid", Options{}); err == nil {
		t.Error("New with an invalid instance id succeeded, want an error")
	}
}

func mustPoll(t *testing.T, c *Collector) []observation.Observation {
	t.Helper()
	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatal("complete = false, want true")
	}
	return obs
}
