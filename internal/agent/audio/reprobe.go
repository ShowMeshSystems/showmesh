package audio

import (
	"context"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// ReprobeIdleMediaFaults re-checks the media of every stopped session whose
// file is missing, and clears the fault when the asset is now ready. Call it
// whenever the node's asset inventory may have changed.
func (m *Manager) ReprobeIdleMediaFaults(ctx context.Context) {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.reprobeIdleMediaFault(ctx)
	}
}

func (s *Session) reprobeIdleMediaFault(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != pkgaudio.StateStopped || s.fault != pkgaudio.FaultMediaDisappeared {
		return
	}
	item, ok := s.currentItemLocked()
	if !ok {
		return
	}
	probe := ProbeAsset(ctx, s.mgr.assetDir, item.Media, s.mgr.decoder)
	s.lastProbe = probe
	if probe.State != MediaReady {
		return
	}
	s.clearFaultLocked()
	s.persistBestEffortLocked("missing media fault cleared")
}
