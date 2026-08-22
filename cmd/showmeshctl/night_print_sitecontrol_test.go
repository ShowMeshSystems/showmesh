package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintNightSessionSiteControl_NotConfigured proves the omitted-
// configuration case renders plainly rather than as an empty section,
// the reference installation's own shape (RESTING-MODE.md §10).
func TestPrintNightSessionSiteControl_NotConfigured(t *testing.T) {
	var buf bytes.Buffer
	printNightSessionSiteControl(&buf, nil)
	if !strings.Contains(buf.String(), "not configured") {
		t.Fatalf("output = %q, want it to say siteControl is not configured", buf.String())
	}
}

// TestPrintNightSessionSiteControl_Configured proves a configured
// presentationPowerOff (immediate policy) actually reaches the printed
// detail, domain/provenance included.
func TestPrintNightSessionSiteControl_Configured(t *testing.T) {
	sc := &nightSessionSiteControl{
		RequestThermalProfile: "show-preheat",
		PresentationPowerOn:   &nightSessionPowerBinding{Action: "site-on", PowerDomain: "presentation", DomainProvenance: "operator-declared"},
		PresentationPowerOff: &nightSessionPresentationPowerOff{
			Action: "site-off", PowerDomain: "presentation", DomainProvenance: "operator-declared",
			RemovalPolicy: "immediate", ImmediateSafeAttestation: true,
		},
	}
	var buf bytes.Buffer
	printNightSessionSiteControl(&buf, sc)
	out := buf.String()
	for _, want := range []string{"show-preheat", "site-on", "site-off", "presentation", "operator-declared", "immediate", "true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want it to contain %q", out, want)
		}
	}
}

// TestPrintNightSessionSiteControl_AfterActionsPrerequisites proves the
// prerequisite list (action, delay, evidence) is rendered, not silently
// dropped.
func TestPrintNightSessionSiteControl_AfterActionsPrerequisites(t *testing.T) {
	sc := &nightSessionSiteControl{
		PresentationPowerOff: &nightSessionPresentationPowerOff{
			Action: "site-off", PowerDomain: "presentation", DomainProvenance: "operator-declared",
			RemovalPolicy: "after-actions",
			Prerequisites: []nightSessionPrerequisite{
				{Kind: "action", Action: "projectors-safe-off", RequireConfirmation: true},
				{Kind: "delay", DelayMs: 300000},
			},
		},
	}
	var buf bytes.Buffer
	printNightSessionSiteControl(&buf, sc)
	out := buf.String()
	for _, want := range []string{"after-actions", "projectors-safe-off", "300000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want it to contain %q", out, want)
		}
	}
}

func TestPrintNightSessionInterlocks_Empty(t *testing.T) {
	var buf bytes.Buffer
	printNightSessionInterlocks(&buf, nil)
	if !strings.Contains(buf.String(), "Interlocks (0)") {
		t.Fatalf("output = %q, want it to report zero interlocks", buf.String())
	}
}

func TestPrintNightSessionInterlocks_Configured(t *testing.T) {
	rules := []nightSessionInterlock{
		{Name: "cooldown", Phase: "start-night", Posture: "block", Signal: "cooldown-check", OnUnavailable: "block", OverridePolicy: "authorized-operator"},
	}
	var buf bytes.Buffer
	printNightSessionInterlocks(&buf, rules)
	out := buf.String()
	for _, want := range []string{"cooldown", "start-night", "block", "cooldown-check", "authorized-operator"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want it to contain %q", out, want)
		}
	}
}
