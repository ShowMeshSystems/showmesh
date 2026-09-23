// Package sendsignal lets a caller learn that a dispatch has been sent,
// before that dispatch's own confirmation wait finishes.
package sendsignal

import (
	"context"
	"sync"
)

type hookKey struct{}

// WithHook returns ctx carrying fn. fn runs at most once, on the first
// [Sent] call made with ctx or a context derived from it.
func WithHook(ctx context.Context, fn func()) context.Context {
	var once sync.Once
	return context.WithValue(ctx, hookKey{}, func() { once.Do(fn) })
}

// Sent reports that the dispatch running under ctx has been sent. It does
// nothing when ctx carries no hook.
func Sent(ctx context.Context) {
	if fn, ok := ctx.Value(hookKey{}).(func()); ok {
		fn()
	}
}
