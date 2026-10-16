package main

import "testing"

func TestHealthURLDialsTheConfiguredHost(t *testing.T) {
	cases := map[string]string{
		"":                 "http://127.0.0.1:8095/healthz",
		":8095":            "http://127.0.0.1:8095/healthz",
		"0.0.0.0:9000":     "http://127.0.0.1:9000/healthz",
		"[::]:9000":        "http://127.0.0.1:9000/healthz",
		"192.168.1.20:80":  "http://192.168.1.20:80/healthz",
		"[fd00::5]:8095":   "http://[fd00::5]:8095/healthz",
		"gateway.lan:8095": "http://gateway.lan:8095/healthz",
	}
	for addr, want := range cases {
		if got, err := healthURL(addr); err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := healthURL("no-port"); err == nil {
		t.Error("an address without a port was accepted")
	}
}
