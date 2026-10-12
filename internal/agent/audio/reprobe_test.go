package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func TestReprobeClearsIdleMediaFaultOnceFileLands(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t, newClock(time.Now()))
	id := pkgaudio.SessionID("s-late-file")

	ref := writeTestAsset(t, m.assetDir, "late.wav", "late", []byte("late"))
	if err := removeTestAsset(m.assetDir, "late.wav"); err != nil {
		t.Fatalf("remove test asset: %v", err)
	}
	m.Apply(ctx, id, "apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Prepare(ctx, id, "prepare", 2)

	faultOf := func() pkgaudio.SessionFault {
		s, _ := m.get(id)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.fault
	}
	if got := faultOf(); got != pkgaudio.FaultMediaDisappeared {
		t.Fatalf("fault before the file lands = %q, want %q", got, pkgaudio.FaultMediaDisappeared)
	}

	m.ReprobeIdleMediaFaults(ctx)
	if got := faultOf(); got != pkgaudio.FaultMediaDisappeared {
		t.Fatalf("fault while the file is still missing = %q, want it kept", got)
	}

	writeTestAsset(t, m.assetDir, "late.wav", "late", []byte("late"))
	m.ReprobeIdleMediaFaults(ctx)
	if got := faultOf(); got != pkgaudio.FaultNone {
		t.Fatalf("fault after the file landed = %q, want none", got)
	}
	for _, snap := range m.Snapshot(ctx) {
		if snap.ID == id && snap.State != pkgaudio.StateStopped {
			t.Fatalf("state after the fault cleared = %q, want stopped", snap.State)
		}
	}
}

func TestSnapshotMarksOnlyIdleLeftoverSessionsUnreferenced(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t, newClock(time.Now()))

	ref := writeTestAsset(t, m.assetDir, "bed.wav", "bed", []byte("bed"))
	for _, id := range []pkgaudio.SessionID{"old-test", "cue-activation:background"} {
		m.Apply(ctx, id, "apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
		m.Prepare(ctx, id, "prepare", 2)
		m.Stop(ctx, id, "stop", 3)
	}
	m.Apply(ctx, "in-use", "apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	m.Start(ctx, "in-use", "start", 2)

	got := map[pkgaudio.SessionID]bool{}
	for _, snap := range m.Snapshot(ctx) {
		got[snap.ID] = snap.Unreferenced
	}
	want := map[pkgaudio.SessionID]bool{"old-test": true, "cue-activation:background": false, "in-use": false}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("session %q Unreferenced = %v, want %v (snapshots: %v)", id, got[id], w, got)
		}
	}
}
