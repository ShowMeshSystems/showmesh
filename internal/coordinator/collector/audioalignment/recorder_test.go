package audioalignment

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

type fakeSink struct {
	calls []mqttproto.AudioPayload
}

func (f *fakeSink) Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time) {
	f.calls = append(f.calls, payload)
}

type fakeStore struct {
	active      store.AlignmentRunRecord
	activeErr   error
	appended    []store.AlignmentSampleRecord
	appendErr   error
	findCalls   int
	appendCalls int
}

func (f *fakeStore) FindActiveAlignmentRun(ctx context.Context, nodeID string) (store.AlignmentRunRecord, error) {
	f.findCalls++
	return f.active, f.activeErr
}

func (f *fakeStore) AppendAlignmentSample(ctx context.Context, sample store.AlignmentSampleRecord) error {
	f.appendCalls++
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appended = append(f.appended, sample)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecorderAppendsWhenMeasuredAndRunActive(t *testing.T) {
	sink := &fakeSink{}
	sampledAt := time.Date(2026, 9, 11, 0, 0, 1, 0, time.UTC)
	fs := &fakeStore{active: store.AlignmentRunRecord{ID: "run-1", NodeID: "node-a"}}
	r := NewRecorder(sink, fs, discardLogger())

	r.Put("node-a", mqttproto.AudioPayload{
		AlignmentMeasured: true, AlignmentOffsetMs: 12, AlignmentSampledAt: &sampledAt, AlignmentSessionID: "sess-1",
	}, time.Now())

	if len(sink.calls) != 1 {
		t.Fatalf("wrapped sink calls = %d, want 1", len(sink.calls))
	}
	if len(fs.appended) != 1 {
		t.Fatalf("appended = %d, want 1", len(fs.appended))
	}
	got := fs.appended[0]
	if got.RunID != "run-1" || got.OffsetMs != 12 || got.SessionID != "sess-1" || !got.SampledAt.Equal(sampledAt) {
		t.Errorf("appended = %+v, want run-1/12/sess-1/%v", got, sampledAt)
	}
}

func TestRecorderAppendsNothingWhenUnmeasured(t *testing.T) {
	sink := &fakeSink{}
	fs := &fakeStore{active: store.AlignmentRunRecord{ID: "run-1"}}
	r := NewRecorder(sink, fs, discardLogger())

	r.Put("node-a", mqttproto.AudioPayload{AlignmentMeasured: false}, time.Now())

	if len(sink.calls) != 1 {
		t.Fatalf("wrapped sink calls = %d, want 1 (always forwarded)", len(sink.calls))
	}
	if fs.findCalls != 0 {
		t.Errorf("findCalls = %d, want 0 for an unmeasured tick", fs.findCalls)
	}
	if len(fs.appended) != 0 {
		t.Errorf("appended = %d, want 0", len(fs.appended))
	}
}

func TestRecorderAppendsNothingWithNoActiveRun(t *testing.T) {
	sink := &fakeSink{}
	sampledAt := time.Now()
	fs := &fakeStore{activeErr: store.ErrAlignmentRunNotFound}
	r := NewRecorder(sink, fs, discardLogger())

	r.Put("node-a", mqttproto.AudioPayload{AlignmentMeasured: true, AlignmentSampledAt: &sampledAt}, time.Now())

	if len(fs.appended) != 0 {
		t.Errorf("appended = %d, want 0 with no active run", len(fs.appended))
	}
}

func TestRecorderAlwaysForwardsToWrappedSink(t *testing.T) {
	sink := &fakeSink{}
	fs := &fakeStore{activeErr: errors.New("boom")}
	r := NewRecorder(sink, fs, discardLogger())

	sampledAt := time.Now()
	r.Put("node-a", mqttproto.AudioPayload{AlignmentMeasured: true, AlignmentSampledAt: &sampledAt}, time.Now())

	if len(sink.calls) != 1 {
		t.Fatalf("wrapped sink calls = %d, want 1 even when the store errors", len(sink.calls))
	}
}
