package agent

import (
	"errors"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// TestSetChannelRangesRefusesOverlongValue is the regression test for the
// silent-discovery-death bug: before this check, a ranges string over
// multisync.MaxPingRangesLength bytes was accepted here and only failed
// much later, inside EncodePing, on every subsequent discover-ping reply,
// so the node stopped answering discovery entirely while multiSyncStatus
// still reported listening.
func TestSetChannelRangesRefusesOverlongValue(t *testing.T) {
	s := newFPPConnectState()
	if err := s.SetChannelRanges("0-100"); err != nil {
		t.Fatalf("SetChannelRanges(valid) unexpected error: %v", err)
	}

	overlong := strings.Repeat("9", multisync.MaxPingRangesLength+1)
	err := s.SetChannelRanges(overlong)
	if !errors.Is(err, ErrChannelRangesTooLong) {
		t.Fatalf("SetChannelRanges(%d bytes) error = %v, want errors.Is(err, ErrChannelRangesTooLong)", len(overlong), err)
	}
	if got := s.ChannelRanges(); got != "0-100" {
		t.Fatalf("ChannelRanges() after a refused overlong SetChannelRanges = %q, want the previous value %q", got, "0-100")
	}
}
