package previewgateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
)

type fakeEngine struct {
	mu        sync.Mutex
	streams   map[string]string
	puts      []string
	deletes   []string
	failPut   bool
	listErr   error
	info      map[string]Go2RTCInfo
	frame     []byte
	frameErr  error
	answer    []byte
	answerErr error
	lastOffer []byte
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{streams: map[string]string{}, info: map[string]Go2RTCInfo{}, frameErr: ErrNoFrame}
}

func (f *fakeEngine) Put(_ context.Context, id, src string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut {
		return errors.New("refused")
	}
	f.streams[id] = src
	f.puts = append(f.puts, id)
	return nil
}

func (f *fakeEngine) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.streams, id)
	f.deletes = append(f.deletes, id)
	return nil
}

func (f *fakeEngine) List(context.Context) (map[string]Go2RTCInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.listErr
}

func (f *fakeEngine) Snapshot(context.Context, string) ([]byte, error) { return f.frame, f.frameErr }

func (f *fakeEngine) WebRTC(_ context.Context, _ string, offer []byte) ([]byte, error) {
	f.lastOffer = offer
	return f.answer, f.answerErr
}

type fakeSources struct {
	names map[string]bool
	ok    bool
	list  []Source
}

func (f *fakeSources) Sources() []Source { return f.list }
func (f *fakeSources) Announced() (map[string]bool, bool) {
	return f.names, f.ok
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
