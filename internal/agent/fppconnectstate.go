package agent

import (
	"errors"
	"fmt"
	"sync"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// ErrChannelRangesTooLong is returned by fppConnectState.SetChannelRanges
// when v exceeds multisync.MaxPingRangesLength bytes, the most the ping
// packet's Ranges field can hold. Without this check a longer value would
// silently fail to encode later (see discoverResponse in multisync.go),
// which answers no discover ping at all rather than reporting the error at
// the point it was introduced.
var ErrChannelRangesTooLong = errors.New("fppconnect: channel ranges string exceeds the ping Ranges field capacity")

// fppConnectState holds this node's FPP Connect compatibility state that
// must be readable fresh at reply time rather than fixed at startup, shared
// between the MultiSync discover-ping responder and, in a later seam, the
// node's HTTP compatibility listener.
type fppConnectState struct {
	mu            sync.RWMutex
	channelRanges string
}

// newFPPConnectState returns a state holder with an empty channel range
// string, the correct starting value for a node with no configured surface.
func newFPPConnectState() *fppConnectState {
	return &fppConnectState{}
}

// ChannelRanges returns the node's currently advertised channel range
// string, or "" if none is set.
func (s *fppConnectState) ChannelRanges() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channelRanges
}

// SetChannelRanges updates the node's advertised channel range string. It
// refuses, and keeps the previous value, when v exceeds
// multisync.MaxPingRangesLength bytes.
func (s *fppConnectState) SetChannelRanges(v string) error {
	if len(v) > multisync.MaxPingRangesLength {
		return fmt.Errorf("%w: %d bytes, limit is %d", ErrChannelRangesTooLong, len(v), multisync.MaxPingRangesLength)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelRanges = v
	return nil
}
