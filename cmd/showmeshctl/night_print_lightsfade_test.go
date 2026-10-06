package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestNightPrintShowsBothLightsFades(t *testing.T) {
	out, in := 20000, 8000
	var buf bytes.Buffer
	printNightSessionLightsFades(&buf, nightSession{LightsFadeOutMs: &out, LightsFadeInMs: &in})
	got := buf.String()
	if !strings.Contains(got, "Lights fade-out before a show: 20000ms") || !strings.Contains(got, "Lights fade-in after a show: 8000ms") {
		t.Fatalf("both fades must print: %q", got)
	}
	buf.Reset()
	printNightSessionLightsFades(&buf, nightSession{})
	if strings.Count(buf.String(), "none") != 2 {
		t.Fatalf("an unset fade prints as none: %q", buf.String())
	}
}
