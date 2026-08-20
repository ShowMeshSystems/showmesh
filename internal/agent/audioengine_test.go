package agent

import (
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// closeCountingEngine is an [audio.Engine] that records how often it was
// closed, so a rebind can be checked for releasing the engine it
// replaced rather than leaving it holding an output device.
type closeCountingEngine struct {
	audio.Engine
	closed int
}

func (e *closeCountingEngine) Close() error {
	e.closed++
	return nil
}

func TestRebindClosesTheEngineItReplaced(t *testing.T) {
	prev := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	closeReplacedEngine(prev, nil)
	if prev.closed != 1 {
		t.Fatalf("outgoing engine closed %d times, want 1: a gstengine holds its output device until it is closed", prev.closed)
	}
}

func TestRebindEngineHandsBackTheEngineItReplaced(t *testing.T) {
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(t.TempDir()), t.TempDir(), audio.RealDecoder{}, time.Now, nil)

	first := audio.NewFakeEngine(time.Now)
	if prev := mgr.RebindEngine(switchable, first, audio.RebindReasonEngineRebind); prev != nil {
		t.Fatalf("first rebind returned %v, want nil: nothing was bound before it", prev)
	}
	second := audio.NewFakeEngine(time.Now)
	prev := mgr.RebindEngine(switchable, second, audio.RebindReasonEngineRebind)
	if prev != audio.Engine(first) {
		t.Fatal("rebind did not hand back the engine it replaced, so nothing can release it")
	}
}
