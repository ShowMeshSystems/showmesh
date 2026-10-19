package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintNightSessionSiteActions_NotConfigured(t *testing.T) {
	var buf bytes.Buffer
	printNightSessionSiteActions(&buf, nil)
	if !strings.Contains(buf.String(), "not configured") {
		t.Fatalf("output = %q, want it to say site actions are not configured", buf.String())
	}
}

func TestPrintNightSessionSiteActions_Configured(t *testing.T) {
	a := &nightSessionSiteActions{
		PrepareSite: []string{"show-preheat"},
		PowerDown:   &nightSessionPowerDown{SkipSequences: []string{"fm"}, AdditionalSequences: []sitePowerSequence{{ID: "porch", Label: "Porch", Steps: []sitePowerStep{{Action: "porch-off"}}}}},
		EndSession:  []string{"standby"},
	}
	var buf bytes.Buffer
	printNightSessionSiteActions(&buf, a)
	for _, want := range []string{"show-preheat", "except fm", "porch", "standby"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output = %q, want it to contain %q", buf.String(), want)
		}
	}
}
