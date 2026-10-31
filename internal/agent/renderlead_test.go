package agent

import "testing"

func TestSurfaceLeadMS(t *testing.T) {
	ndi := func(v any) map[string]any {
		return map[string]any{"output": map[string]any{"transport": "ndi", "ndi": map[string]any{"sourceName": "a", "leadMs": v}}}
	}
	cases := []struct {
		name   string
		params map[string]any
		want   int
	}{
		{"set", ndi(float64(120)), 120},
		{"no output", map[string]any{}, 0},
		{"negative", ndi(float64(-5)), 0},
		{"malformed", ndi("soon"), 0},
		{"hdmi", map[string]any{"output": map[string]any{"transport": "hdmi", "hdmi": map[string]any{"display": "x"}}}, 0},
	}
	for _, c := range cases {
		if got := surfaceLeadMS(c.params); got != c.want {
			t.Errorf("%s: surfaceLeadMS = %d, want %d", c.name, got, c.want)
		}
	}
}
