package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

func TestWeatherDelayAdoptionAllowed(t *testing.T) {
	idle := func(revision int64) store.WeatherDelayStateRecord {
		return store.WeatherDelayStateRecord{Revision: revision}
	}
	active := func(kind string, revision int64) store.WeatherDelayStateRecord {
		return store.WeatherDelayStateRecord{Active: true, Kind: kind, Revision: revision}
	}
	report := func(kind string, held int64) mqttproto.HealthWeatherDelay {
		return mqttproto.HealthWeatherDelay{Active: true, Kind: kind, HeldRevision: held, Revision: held}
	}
	cases := []struct {
		name        string
		current     store.WeatherDelayStateRecord
		report      mqttproto.HealthWeatherDelay
		wantAllowed bool
		wantStale   bool
	}{
		{"node not delayed", idle(4), mqttproto.HealthWeatherDelay{HeldRevision: 4, Revision: 4}, false, false},
		{"began after the newest state the coordinator holds", idle(4), report(weatherdelay.KindDelay, 4), true, false},
		{"began before the coordinator's last resume", idle(4), report(weatherdelay.KindDelay, 3), false, true},
		{"node offline through a start and a resume", idle(6), report(weatherdelay.KindDelay, 4), false, true},
		{"coordinator store was reset", idle(0), report(weatherdelay.KindCancelNight, 9), true, false},
		{"coordinator already delayed, same kind", active(weatherdelay.KindDelay, 5), report(weatherdelay.KindDelay, 4), false, false},
		{"node cancelled the night during this delay", active(weatherdelay.KindDelay, 5), report(weatherdelay.KindCancelNight, 4), true, false},
		{"node cancel is older than the resume before this delay", active(weatherdelay.KindDelay, 5), report(weatherdelay.KindCancelNight, 3), false, true},
		{"coordinator night cancelled, node only delayed", active(weatherdelay.KindCancelNight, 5), report(weatherdelay.KindDelay, 5), false, false},
		{"coordinator night cancelled, node too", active(weatherdelay.KindCancelNight, 5), report(weatherdelay.KindCancelNight, 5), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, stale := weatherDelayAdoptionAllowed(tc.current, tc.report)
			if allowed != tc.wantAllowed || stale != tc.wantStale {
				t.Fatalf("allowed, stale = %v, %v; want %v, %v", allowed, stale, tc.wantAllowed, tc.wantStale)
			}
		})
	}
}

func (r *resumeHarness) adopter() *WeatherDelayAdopter {
	return NewWeatherDelayAdopter(r.h.deps, Options{Clock: func() time.Time { return r.now }, Logger: testLogger()})
}

func (r *resumeHarness) state() store.WeatherDelayStateRecord {
	r.t.Helper()
	rec, err := r.h.deps.WeatherDelay.GetWeatherDelayState(context.Background())
	if err != nil {
		r.t.Fatalf("GetWeatherDelayState: %v", err)
	}
	return rec
}

func (r *resumeHarness) adoptAudits() []identity.AuditEntry {
	r.t.Helper()
	entries, err := r.h.deps.Identity.ListAudit(context.Background(), 0, 500)
	if err != nil {
		r.t.Fatalf("ListAudit: %v", err)
	}
	var out []identity.AuditEntry
	for _, e := range entries {
		if e.Action == identity.AuditActionShowWeatherDelayAdopt {
			out = append(out, e)
		}
	}
	return out
}

func (r *resumeHarness) lastRetained() mqttproto.WeatherDelayMessage {
	r.t.Helper()
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	if len(r.pub.retained) == 0 {
		r.t.Fatal("no retained weather delay state was published")
	}
	msg, err := mqttproto.DecodeWeatherDelayMessage(r.pub.retained[len(r.pub.retained)-1])
	if err != nil {
		r.t.Fatalf("decode retained state: %v", err)
	}
	return msg
}

func (r *resumeHarness) nodeActions() []string {
	r.pub.mu.Lock()
	defer r.pub.mu.Unlock()
	return append([]string(nil), r.pub.actions...)
}

func TestWeatherDelayAdoptEntersTheNodesDelayThroughTheStartPath(t *testing.T) {
	r := newResumeHarness(t)
	nodeStartedAt := r.now.Add(-4 * time.Minute)
	report := mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay, StartedAt: nodeStartedAt, HeldRevision: 0, Revision: 0}

	r.adopter().adoptLocked(context.Background(), "render-01", report)

	got := r.state()
	if !got.Active || got.Kind != weatherdelay.KindDelay || got.Revision != 1 {
		t.Fatalf("state = %+v, want an active delay at revision 1", got)
	}
	if !got.StartedAt.Equal(nodeStartedAt) {
		t.Errorf("StartedAt = %s, want the node's own start %s", got.StartedAt, nodeStartedAt)
	}
	if got.StartedBy != "node render-01" || got.StartedByName != "node render-01" {
		t.Errorf("StartedBy, StartedByName = %q, %q", got.StartedBy, got.StartedByName)
	}

	retained := r.lastRetained()
	if !retained.Active || retained.Kind != weatherdelay.KindDelay || retained.Revision != 1 || retained.StartedBy != "node render-01" {
		t.Errorf("retained state = %+v, want the adopted delay at revision 1", retained)
	}
	cmds, _ := r.fppCommands()
	if len(cmds) == 0 || !strings.HasPrefix(cmds[0], "Stop") {
		t.Errorf("FPP commands = %v, want the start path's stop", cmds)
	}
	if actions := r.nodeActions(); len(actions) == 0 || actions[0] != "weatherdelay.start" {
		t.Errorf("node commands = %v, want weatherdelay.start", actions)
	}

	audits := r.adoptAudits()
	if len(audits) != 1 {
		t.Fatalf("adopt audit entries = %d, want 1", len(audits))
	}
	a := audits[0]
	if a.Target != "render-01" || a.Params["nodeId"] != "render-01" || a.Params["kind"] != weatherdelay.KindDelay {
		t.Errorf("audit target %q params %v, want the node and the kind", a.Target, a.Params)
	}
	wantReason := "A weather delay was started on node render-01, and the coordinator joined it. Press Resume when it is safe to continue."
	if a.OutcomeReason != wantReason {
		t.Errorf("audit OutcomeReason = %q, want %q", a.OutcomeReason, wantReason)
	}

	// A second report of the same delay changes nothing.
	r.adopter().adoptLocked(context.Background(), "render-01", report)
	if again := r.state(); again.Revision != 1 || len(r.adoptAudits()) != 1 {
		t.Fatalf("a repeated report moved the state to %+v with %d adopt audits", again, len(r.adoptAudits()))
	}

	r.resume()
	if cleared := r.state(); cleared.Active || cleared.Revision != 2 {
		t.Fatalf("state after resume = %+v, want not active at revision 2", cleared)
	}
	actions := r.nodeActions()
	if actions[len(actions)-1] != "weatherdelay.resume" {
		t.Errorf("node commands = %v, want weatherdelay.resume last", actions)
	}

	// The node missed that resume and still reports the delay it held.
	r.adopter().adoptLocked(context.Background(), "render-01", report)
	if stale := r.state(); stale.Active || stale.Revision != 2 || len(r.adoptAudits()) != 1 {
		t.Fatalf("a delay older than the resume started one again: state %+v, %d adopt audits", stale, len(r.adoptAudits()))
	}
}

func TestWeatherDelayAdoptNumbersTheStateAboveEverythingTheNodeHasSeen(t *testing.T) {
	r := newResumeHarness(t)
	report := mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay, StartedAt: r.now.Add(time.Hour), HeldRevision: 7, Revision: 9}

	r.adopter().adoptLocked(context.Background(), "render-01", report)

	got := r.state()
	if !got.Active || got.Revision != 10 {
		t.Fatalf("state = %+v, want active at revision 10, above the node's 9", got)
	}
	if !got.StartedAt.Equal(r.now) {
		t.Errorf("StartedAt = %s, want %s: a node clock running ahead must not date the delay in the future", got.StartedAt, r.now)
	}
	r.resume()
	if cleared := r.state(); cleared.Revision <= report.HeldRevision {
		t.Fatalf("resume revision %d is not above the node's held %d, so the node would ignore it", cleared.Revision, report.HeldRevision)
	}
}

func TestWeatherDelayAdoptCancelNightKinds(t *testing.T) {
	orig := weatherDelayCancelShutdownPollInterval
	weatherDelayCancelShutdownPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { weatherDelayCancelShutdownPollInterval = orig })
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)

	t.Run("a cancelled night is adopted as one", func(t *testing.T) {
		r := newResumeHarness(t)
		r.adopter().adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindCancelNight})
		if got := r.state(); !got.Active || got.Kind != weatherdelay.KindCancelNight {
			t.Fatalf("state = %+v, want an active cancelled night", got)
		}
		wantReason := "The night was cancelled on node render-01, and the coordinator joined it. Press Resume to clear it."
		if audits := r.adoptAudits(); len(audits) != 1 || audits[0].OutcomeReason != wantReason {
			t.Errorf("adopt audits = %+v, want one with reason %q", audits, wantReason)
		}
	})

	t.Run("a node's cancelled night changes an active delay", func(t *testing.T) {
		r := newResumeHarness(t)
		r.start()
		r.adopter().adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindCancelNight, HeldRevision: 0, Revision: 1})
		got := r.state()
		if !got.Active || got.Kind != weatherdelay.KindCancelNight || got.Revision != 2 || got.StartedBy != "node render-01" {
			t.Fatalf("state = %+v, want the delay changed to a cancelled night at revision 2", got)
		}
		audits := r.adoptAudits()
		if len(audits) != 1 {
			t.Fatalf("adopt audit entries = %d, want 1", len(audits))
		}
		wantReason := "Node render-01 reported a cancelled night, so the weather delay was changed to a cancelled night. Press Resume to clear it."
		if audits[0].OutcomeReason != wantReason {
			t.Errorf("audit OutcomeReason = %q, want %q", audits[0].OutcomeReason, wantReason)
		}
	})

	t.Run("a node's delay never changes a cancelled night", func(t *testing.T) {
		r := newResumeHarness(t)
		r.adopter().adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindCancelNight})
		r.adopter().adoptLocked(context.Background(), "audio-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay, HeldRevision: 1, Revision: 1})
		if got := r.state(); got.Kind != weatherdelay.KindCancelNight || got.Revision != 1 {
			t.Fatalf("state = %+v, want the cancelled night unchanged at revision 1", got)
		}
	})
}

func TestWeatherDelayAdoptDoesNothingWhenTheStateCannotBeRead(t *testing.T) {
	r, fs := newNotSavedHarness(t)
	fs.setFail(false)
	fs.mu.Lock()
	fs.failRead = true
	fs.mu.Unlock()

	r.adopter().adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay})

	fs.mu.Lock()
	fs.failRead = false
	fs.mu.Unlock()
	if got := r.state(); got.Active {
		t.Fatalf("state = %+v, want not active: staleness could not be judged", got)
	}
	if cmds, _ := r.fppCommands(); len(cmds) != 0 {
		t.Fatalf("FPP commands = %v, want none", cmds)
	}
}

func TestWeatherDelayAdopterReportIgnoresANodeThatIsNotDelayed(t *testing.T) {
	r := newResumeHarness(t)
	a := r.adopter()
	a.Report("render-01", mqttproto.HealthWeatherDelay{HeldRevision: 3, Revision: 3})
	a.Report("render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay})
	weatherDelayAdoptBackground.Wait()
	if got := r.state(); !got.Active || got.Revision != 1 {
		t.Fatalf("state = %+v, want the one active report adopted at revision 1", got)
	}
}

func (r *resumeHarness) stopCommands() int {
	cmds, _ := r.fppCommands()
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, "Stop") {
			n++
		}
	}
	return n
}

// The start path answers a repeated command key from its stored result
// without sending anything, so each adoption must carry a key of its own.
func TestWeatherDelayAdoptSendsItsStopsEveryTime(t *testing.T) {
	r := newResumeHarness(t)
	a := r.adopter()

	a.adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay})
	if got := r.stopCommands(); got != 1 {
		t.Fatalf("FPP stop commands after the first adoption = %d, want 1", got)
	}
	r.resume()

	a.adoptLocked(context.Background(), "render-01", mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay, HeldRevision: 2, Revision: 2})
	if got := r.state(); !got.Active || got.Revision != 3 {
		t.Fatalf("state = %+v, want the second delay adopted at revision 3", got)
	}
	if got := r.stopCommands(); got != 2 {
		t.Fatalf("FPP stop commands after the second adoption = %d, want 2: the second adoption's stop was not sent", got)
	}
}

// A report numbered so high that nothing can follow it would leave Resume
// with no number to write. It is refused and adopts nothing.
func TestWeatherDelayAdoptRefusesAReportThatLeavesNoRoomForResume(t *testing.T) {
	r := newResumeHarness(t)
	r.start()
	a := r.adopter()

	for _, report := range []mqttproto.HealthWeatherDelay{
		{Active: true, Kind: weatherdelay.KindCancelNight, HeldRevision: 9223372036854775806, Revision: 9223372036854775806},
		{Active: true, Kind: weatherdelay.KindCancelNight, HeldRevision: 1, Revision: 9223372036854775806},
		{Active: true, Kind: weatherdelay.KindCancelNight, HeldRevision: 1, Revision: mqttproto.MaxHealthWeatherDelayRevision + 1},
		{Active: true, Kind: weatherdelay.KindCancelNight, HeldRevision: -1, Revision: 1},
	} {
		a.Report("render-01", report)
		weatherDelayAdoptBackground.Wait()
	}

	got := r.state()
	if !got.Active || got.Kind != weatherdelay.KindDelay || got.Revision != 1 || len(r.adoptAudits()) != 0 {
		t.Fatalf("state = %+v with %d adopt audits, want the operator's delay untouched at revision 1", got, len(r.adoptAudits()))
	}
	r.resume()
	if cleared := r.state(); cleared.Active || cleared.Revision != 2 {
		t.Fatalf("state after resume = %+v, want not active at revision 2", cleared)
	}
}
