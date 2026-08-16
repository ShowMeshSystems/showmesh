package coordinator

// This file is review finding B1's own proof (2026-08-16): before this
// finding was fixed, newResolumeWiring had no parameter for
// resolume.Options.OnUnreachableTransition, so production never supplied
// anything for it, [resolume.Recovery.CaptureCrashTarget] was never called
// by a real crash, and [resolume.Recovery.HandleReachableTransition]'s own
// takeCrashTarget always came back empty on the return that followed —
// the automatic restore silently reported nothing_to_do after every real
// crash. resolumewiring_test.go's own tests never exercised this because
// they pass nil for onReachableTransition/onUnreachableTransition and
// never drive a crash-return sequence; this file does both, through the
// SAME production wiring functions (newResolumeWiring,
// newResolumeRecoveryWiring) coordinator.go's own Run calls, against a
// fake Arena that can genuinely go unreachable and come back — proof that
// the hook reaches CaptureCrashTarget, not just that the parameter exists.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestResolumeCrashWiringReachesCaptureCrashTarget drives a real crash and
// a real return through the actual production wiring path — newResolumeWiring
// wired the identical way coordinator.go's own Run wires it (an
// atomic.Pointer[resolume.Recovery] late-binding cell backing BOTH hooks) —
// and asserts the automatic restore that follows the return actually
// RESTORES the layer a pre-crash confirmed action established, rather than
// reporting nothing_to_do. nothing_to_do is exactly what a caller sees
// when CaptureCrashTarget was never wired in at all (takeCrashTarget
// finds nothing), so this outcome is the proof this finding asks for: the
// unreachable transition reached CaptureCrashTarget, not merely that a
// callback of the right shape was passed somewhere.
func TestResolumeCrashWiringReachesCaptureCrashTarget(t *testing.T) {
	arena := newE2EFakeArena()

	var mu sync.Mutex
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/product" {
			mu.Lock()
			curUp := up
			mu.Unlock()
			if !curUp {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		arena.ServeHTTP(w, r)
	}))
	defer srv.Close()

	ctx := context.Background()
	st := openTestStore(t)
	logger := testLogger()

	writeTestCompositionRevision(t, st, 1, e2eTestComposition())
	compWiring := newResolumeCompositionWiring(ctx, st, logger)

	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-crash-wiring"}
	sink := &fppSink{st: st, logger: logger}
	runner := collector.NewRunner(sink, logger)

	// The SAME late-binding shape coordinator.go's own Run uses (see
	// coordinator.go's resolumeRecoveryHolder and its own doc comment):
	// both hooks must be supplied at newResolumeWiring's own call time,
	// before the *resolume.Recovery either one calls into exists.
	var recoveryHolder atomic.Pointer[resolume.Recovery]
	wire, err := newResolumeWiring(ctx, cfg, runner, compWiring.store, logger,
		func(returnedAt time.Time) {
			if rec := recoveryHolder.Load(); rec != nil {
				rec.HandleReachableTransition(ctx, returnedAt)
			}
		},
		func(time.Time) {
			if rec := recoveryHolder.Load(); rec != nil {
				rec.CaptureCrashTarget()
			}
		},
	)
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.collector == nil {
		t.Fatal("wire.collector = nil, want a real *resolume.Collector when ResolumeURL is set")
	}

	// A short poll interval — real wall-clock time drives this test (no
	// fake Now, matching resolumeactionwiring_e2e_test.go's own real-time
	// style), so every later Poll call below must clear
	// FootprintControls.PollInterval's own liveness throttle without
	// waiting out the collector's real default (10s).
	wire.collector.Footprint().SetPollInterval(time.Millisecond)

	// Warm-up poll: this Collector's first-ever liveness result (never a
	// "return"), which also confirms composition identity for real —
	// mirrors resolumeactionwiring_e2e_test.go's own identical warm-up
	// step and its own reasoning for why it must be checked explicitly.
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: warm-up did not run (unexpectedly throttled on its very first call)")
	}
	if snap := wire.collector.LastSurveySnapshot(); !snap.SurveyRan || snap.Identity != resolume.IdentityTrue {
		t.Fatalf("composition identity after warmup poll = %+v, want SurveyRan=true, Identity=identified "+
			"(fixture wiring is wrong if this fails, not the wiring under test)", snap)
	}

	// A real, confirmed pre-crash action — production's own dispatcher
	// over the SAME collector, exactly as coordinator.go's Run constructs
	// it (resolume.NewActionDispatcher(resolumeWire.collector, ...)) — so
	// the collector's recovery record holds a real action-sourced entry
	// for CaptureCrashTarget to snapshot.
	dispatcher := resolume.NewActionDispatcher(wire.collector, resolume.ActionDispatcherOptions{})
	// resolume.ObjectID(3001) mirrors e2eClipID's own literal value
	// (resolumeactionwiring_e2e_test.go); ObjectID is an int64 wire type,
	// not a string, so the untyped string constant cannot be used
	// directly here.
	const e2eClipObjectID = resolume.ObjectID(3001)
	outcome, err := dispatcher.Dispatch(ctx, resolume.ActionLaunchClip, resolume.ActionParams{ClipID: e2eClipObjectID})
	if err != nil {
		t.Fatalf("pre-crash Dispatch error = %v", err)
	}
	if outcome.State != resolume.ActionConfirmed {
		t.Fatalf("pre-crash Dispatch State = %q, want %q (reason: %s)", outcome.State, resolume.ActionConfirmed, outcome.Reason)
	}

	// The real recovery controller — production's own constructor
	// (resolumerecoverywiring.go), over the SAME collector and dispatcher
	// — settle 0 so the gate proceeds without an artificial test wait.
	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(logger))
	recovery, _ := newResolumeRecoveryWiring(st, svc, wire.collector, dispatcher, 0, logger, nil)
	recoveryHolder.Store(recovery)

	// The crash: liveness fails. [Collector.Poll]'s own failure path calls
	// onUnreachableTransition SYNCHRONOUSLY — see
	// [resolume.Options.OnUnreachableTransition]'s own doc comment — so
	// CaptureCrashTarget has already run by the time this call returns.
	mu.Lock()
	up = false
	mu.Unlock()
	time.Sleep(2 * time.Millisecond) // clears the real-clock liveness throttle set above
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: crash poll did not run")
	}

	// Arena "comes back with nothing playing" (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md
	// §2) — the layer the pre-crash action launched is cleared.
	arena.mu.Lock()
	arena.clipConnected = "Disconnected"
	arena.layerActiveClip = nil
	arena.mu.Unlock()

	// The return: liveness succeeds again. This spawns the automatic
	// restore goroutine (Options.OnReachableTransition is invoked via
	// `go cb(at)` — never synchronously), so the report is awaited below
	// rather than asserted on immediately.
	mu.Lock()
	up = true
	mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	if _, ok := wire.collector.Poll(ctx); !ok {
		t.Fatal("wire.collector.Poll: return poll did not run")
	}

	deadline := time.Now().Add(5 * time.Second)
	var report *resolume.RestoreReport
	for time.Now().Before(deadline) {
		if report = recovery.LastReport(); report != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if report == nil {
		t.Fatal("no restore report ever appeared after the return")
	}
	if report.Outcome != resolume.RestoreOutcomeRestored {
		t.Fatalf("restore outcome = %q, want %q — a %q outcome means the crash was never captured through the production wiring path "+
			"(OnUnreachableTransition was never reached, exactly the failure mode this finding fixed); report = %+v",
			report.Outcome, resolume.RestoreOutcomeRestored, resolume.RestoreOutcomeNothingToDo, report)
	}
	if arena.layerActiveClip == nil || *arena.layerActiveClip != 3001 {
		t.Errorf("arena layer active clip not restored to clip 3001 after the automatic restore")
	}
}
