package previewgateway

import (
	"context"
	"testing"
	"time"
)

func TestDiscoveryReportsAvahiDown(t *testing.T) {
	d := NewDiscovery(quietLog())
	d.Avahi = func() error { return errNoAvahi }
	if err := d.Available(context.Background()); err != errNoAvahi {
		t.Fatalf("Available = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go d.Loop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for d.Unavailable() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if d.Unavailable() != msgNoAvahi {
		t.Fatalf("Unavailable = %q", d.Unavailable())
	}
}
