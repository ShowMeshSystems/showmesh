package config

import (
	"strings"
	"testing"
)

func TestPreviewGatewayProblem(t *testing.T) {
	ok := []string{"", "http://192.168.1.20:8095", "https://gw.example", "http://gw:8095/", "http://[fd00::1]:8095"}
	for _, u := range ok {
		if got := PreviewGatewayProblem(PreviewGatewayConfig{URL: u}); got != "" {
			t.Errorf("%q refused: %s", u, got)
		}
	}
	refused := map[string]string{
		"ftp://gw:8095":          "http://",
		"gw:8095":                "http://",
		"http://:8095":           "name a host",
		"http://":                "name a host",
		"http:gw":                "name a host",
		"http://user:pw@gw:8095": "login",
		"http://gw:8095/api":     "after the port",
		"http://gw:8095/a/b/":    "after the port",
		"http://gw:8095?x=1":     "after the port",
		"http://gw:8095/?x=1":    "after the port",
		"http://gw:8095?":        "after the port",
		"http://gw:8095#frag":    "after the port",
		"http://gw:8095/#":       "after the port",
	}
	for u, want := range refused {
		got := PreviewGatewayProblem(PreviewGatewayConfig{URL: u})
		if got == "" || !strings.Contains(got, want) {
			t.Errorf("%q: problem = %q, want it to mention %q", u, got, want)
		}
	}
}
