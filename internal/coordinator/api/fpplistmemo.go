package api

import (
	"context"
	"sync"
)

type fppListMemoKey struct{}

type fppListMemo struct {
	mu    sync.Mutex
	done  bool
	views []FPPInstanceView
	err   error
}

// withFPPListMemo returns a context on which every memoized FPPLister shares
// one ListInstances read, so one hub render pass reads FPP evidence once.
func withFPPListMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, fppListMemoKey{}, &fppListMemo{})
}

// memoFPPLister reads through to next unless ctx carries a memo.
type memoFPPLister struct{ next FPPLister }

func memoizeFPPLister(l FPPLister) FPPLister {
	if _, ok := l.(memoFPPLister); ok {
		return l
	}
	return memoFPPLister{next: l}
}

func (l memoFPPLister) ListInstances(ctx context.Context) ([]FPPInstanceView, error) {
	m, ok := ctx.Value(fppListMemoKey{}).(*fppListMemo)
	if !ok {
		return l.next.ListInstances(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.done {
		m.views, m.err = l.next.ListInstances(ctx)
		m.done = true
	}
	return m.views, m.err
}
