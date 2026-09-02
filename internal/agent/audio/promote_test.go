package audio

import (
	"context"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestPromoteMovesLoadedHandleWhenStagedContentMatches proves Manager.
// Promote's happy path: a session staged ahead of time (Apply, Prepare,
// never Start) under a separate id, once the show session desires the
// identical content, ends up Playing with the STAGED handle — never a
// freshly loaded one — and the staging session is left holding nothing.
func TestPromoteMovesLoadedHandleWhenStagedContentMatches(t *testing.T) {
	c := newClock(time.Now())
	m := newTestManager(t, c)
	ctx := context.Background()
	const staging = pkgaudio.SessionID("staging")
	const show = pkgaudio.SessionID("show")

	ref := writeTestAsset(t, m.assetDir, "a.wav", "asset-1", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	if r := m.Apply(ctx, staging, "stage-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply refused: %+v", r)
	}
	if r := m.Prepare(ctx, staging, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare refused: %+v", r)
	}
	stageSession, ok := m.get(staging)
	if !ok {
		t.Fatal("staging session missing after Apply+Prepare")
	}
	stageSession.mu.Lock()
	stagedHandle := stageSession.handle
	stageSession.mu.Unlock()
	if stagedHandle == "" {
		t.Fatal("staging session has no loaded handle after Apply+Prepare; test setup is broken")
	}

	if r := m.Apply(ctx, show, "show-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply refused: %+v", r)
	}

	res := m.Promote(ctx, staging, show, "show-start", 2)
	if res.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("promote refused: %+v", res)
	}

	showSession, ok := m.get(show)
	if !ok {
		t.Fatal("show session missing")
	}
	showSession.mu.Lock()
	gotState, gotHandle, gotLoaded := showSession.state, showSession.handle, showSession.handleLoaded
	showSession.mu.Unlock()
	if gotState != pkgaudio.StatePlaying {
		t.Fatalf("show session state = %q, want %q", gotState, pkgaudio.StatePlaying)
	}
	if !gotLoaded {
		t.Fatal("show session reports no loaded handle after a successful promote")
	}
	if gotHandle != stagedHandle {
		t.Fatalf("show session handle = %q, want the staged handle %q (promote must move it, not load a fresh one)", gotHandle, stagedHandle)
	}

	stageSession.mu.Lock()
	stageLoadedAfter, stageStateAfter := stageSession.handleLoaded, stageSession.state
	stageSession.mu.Unlock()
	if stageLoadedAfter {
		t.Fatal("staging session still reports a loaded handle after promote")
	}
	if stageStateAfter == pkgaudio.StatePlaying {
		t.Fatal("staging session must never itself reach Playing; only the show session promote targets may")
	}
}

// TestPromoteReleasesOrphanedHandleWhenDispatchDoesNotExecute constructs,
// deliberately, the one case Manager.Promote's own leak-closing check
// exists for: the staged handle is captured from the staging session (and
// that session's own record already cleared), but the destination
// session's dispatch gate refuses before ever calling exec — here, because
// its revision floor was already advanced past the revision this promote
// attempt requests, an ordinary stale-revision refusal, not a replay. If
// the outer `!res.executed` check in Promote were missing (or if a
// captured "ran" bool were reintroduced and, unlike res.executed, left
// unset on this path), the captured handle would never be released back to
// the engine, and FakeEngine's own [FakeEngine.get] would still report it
// as loaded after Promote returns.
func TestPromoteReleasesOrphanedHandleWhenDispatchDoesNotExecute(t *testing.T) {
	c := newClock(time.Now())
	dir := t.TempDir()
	engine := NewFakeEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()
	const staging = pkgaudio.SessionID("staging")
	const show = pkgaudio.SessionID("show")

	ref := writeTestAsset(t, dir, "a.wav", "asset-1", []byte("content"))
	req := pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}

	if r := m.Apply(ctx, staging, "stage-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage apply refused: %+v", r)
	}
	if r := m.Prepare(ctx, staging, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("stage prepare refused: %+v", r)
	}
	stageSession, ok := m.get(staging)
	if !ok {
		t.Fatal("staging session missing after Apply+Prepare")
	}
	stageSession.mu.Lock()
	stagedHandle := stageSession.handle
	stageSession.mu.Unlock()
	if stagedHandle == "" {
		t.Fatal("staging session has no loaded handle after Apply+Prepare; test setup is broken")
	}

	// Apply the show session's desired content, then drive its own revision
	// floor far ahead — a normal Start, at a revision much higher than the
	// promote attempt below will use. This session never actually needs to
	// play for the test; only its RevisionState's current floor matters.
	if r := m.Apply(ctx, show, "show-apply", 1, req); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show apply refused: %+v", r)
	}
	if r := m.Start(ctx, show, "show-advance-floor", 100); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("show start (floor-advancing) refused: %+v", r)
	}

	// The promote attempt below is a genuinely fresh invocation (never seen
	// on the show session before) requesting a revision (50) that is not
	// strictly greater than the floor (100) the Start above already set.
	// dispatchLocked's own revState.Apply gate refuses this BEFORE exec
	// ever runs — res.executed must be false.
	res := m.Promote(ctx, staging, show, "promote-stale", 50)
	if res.Outcome != pkgaudio.OutcomeRefused || res.Reason != pkgaudio.ReasonStaleRevision {
		t.Fatalf("promote outcome = %+v, want Refused/%s (the deliberately stale revision this test constructs)", res, pkgaudio.ReasonStaleRevision)
	}

	if _, err := engine.get(EngineHandle(stagedHandle)); err == nil {
		t.Fatalf("staged handle %q is still loaded in the engine after promote's dispatch never executed; the orphan-release path did not run", stagedHandle)
	}

	stageSession.mu.Lock()
	stageLoadedAfter := stageSession.handleLoaded
	stageSession.mu.Unlock()
	if stageLoadedAfter {
		t.Fatal("staging session still reports a loaded handle; promote must clear it from the staging session's own record regardless of whether the destination dispatch executes")
	}
}
