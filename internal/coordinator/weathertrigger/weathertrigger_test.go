package weathertrigger

import (
	"testing"
	"time"
)

func TestTriggerEventValidate(t *testing.T) {
	cases := []struct {
		name string
		e    TriggerEvent
		ok   bool
	}{
		{"valid warning", TriggerEvent{Source: "nws", Kind: KindWarning}, true},
		{"valid lightning", TriggerEvent{Source: "lightning-01", Kind: KindLightning}, true},
		{"empty source", TriggerEvent{Source: "", Kind: KindWarning}, false},
		{"bad kind", TriggerEvent{Source: "nws", Kind: "hail"}, false},
	}
	for _, c := range cases {
		err := c.e.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: Validate() error = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestBuildReasonWarning(t *testing.T) {
	cases := []struct {
		eventType string
		want      string
	}{
		{"Severe Thunderstorm Warning", "A severe thunderstorm warning is in effect."},
		{"Tornado Warning", "A tornado warning is in effect."},
		{"", "A weather warning is in effect."},
	}
	for _, c := range cases {
		got := BuildReason(TriggerEvent{Kind: KindWarning, EventType: c.eventType})
		if got != c.want {
			t.Errorf("BuildReason(eventType=%q) = %q, want %q", c.eventType, got, c.want)
		}
	}
}

func TestBuildReasonNeverContainsAClockTime(t *testing.T) {
	expires := time.Date(2026, 9, 19, 21, 45, 0, 0, time.UTC)
	got := BuildReason(TriggerEvent{Kind: KindWarning, EventType: "Tornado Warning", ExpiresAt: &expires})
	if got != "A tornado warning is in effect." {
		t.Fatalf("BuildReason with ExpiresAt set = %q, want no clock time baked in", got)
	}
}

func TestBuildReasonLightning(t *testing.T) {
	six := 9.656 // ~6 miles
	one := 1.60934
	cases := []struct {
		km   *float64
		want string
	}{
		{&six, "Lightning was reported 6 miles away."},
		{&one, "Lightning was reported 1 mile away."},
		{nil, "Lightning was reported nearby."},
	}
	for _, c := range cases {
		got := BuildReason(TriggerEvent{Kind: KindLightning, DistanceKm: c.km})
		if got != c.want {
			t.Errorf("BuildReason(distanceKm=%v) = %q, want %q", c.km, got, c.want)
		}
	}
}

func TestWarningKeyDistinguishesEventTypesAndSources(t *testing.T) {
	a := WarningKey(TriggerEvent{Source: "nws", Kind: KindWarning, EventType: "Tornado Warning"})
	b := WarningKey(TriggerEvent{Source: "nws", Kind: KindWarning, EventType: "Severe Thunderstorm Warning"})
	c := WarningKey(TriggerEvent{Source: "other", Kind: KindWarning, EventType: "Tornado Warning"})
	d := WarningKey(TriggerEvent{Source: "nws", Kind: KindWarning, EventType: "Tornado Warning"})
	if a == b || a == c {
		t.Fatalf("WarningKey did not distinguish event type or source: a=%q b=%q c=%q", a, b, c)
	}
	if a != d {
		t.Fatalf("WarningKey not stable for identical inputs: a=%q d=%q", a, d)
	}
	lightning := WarningKey(TriggerEvent{Source: "nws", Kind: KindLightning})
	if lightning == a {
		t.Fatalf("WarningKey collided across kinds: %q", lightning)
	}
}

func TestClassifyQuestion(t *testing.T) {
	if q, a := ClassifyQuestion(false); q != QuestionDelay || a != ActionDelay {
		t.Fatalf("ClassifyQuestion(false) = (%q, %q), want (%q, %q)", q, a, QuestionDelay, ActionDelay)
	}
	if q, a := ClassifyQuestion(true); q != QuestionDelayOrCancel || a != ActionCancelNight {
		t.Fatalf("ClassifyQuestion(true) = (%q, %q), want (%q, %q)", q, a, QuestionDelayOrCancel, ActionCancelNight)
	}
}

func TestNewPendingDecisionWindowsAndDeadline(t *testing.T) {
	asked := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)

	delay := NewPendingDecision("d1", TriggerEvent{Source: "nws", Kind: KindWarning, EventType: "Tornado Warning"}, asked, 30, 180)
	if delay.Question != QuestionDelay || delay.DefaultAction != ActionDelay {
		t.Fatalf("delay decision = %+v", delay)
	}
	if want := asked.Add(30 * time.Second); !delay.Deadline.Equal(want) {
		t.Fatalf("delay deadline = %v, want %v", delay.Deadline, want)
	}

	cancel := NewPendingDecision("d2", TriggerEvent{Source: "ops", Kind: KindLightning, SuggestCancel: true}, asked, 30, 180)
	if cancel.Question != QuestionDelayOrCancel || cancel.DefaultAction != ActionCancelNight {
		t.Fatalf("cancel decision = %+v", cancel)
	}
	if want := asked.Add(180 * time.Second); !cancel.Deadline.Equal(want) {
		t.Fatalf("cancel deadline = %v, want %v", cancel.Deadline, want)
	}
	if cancel.Reason == "" || cancel.ID != "d2" || cancel.Source != "ops" {
		t.Fatalf("cancel decision fields = %+v", cancel)
	}
}
