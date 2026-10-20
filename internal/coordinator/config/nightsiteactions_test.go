package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSiteActionsFromLegacyKeepsThermalProfileAndPowerOffOrder(t *testing.T) {
	got := siteActionsFromLegacy(&NightSiteControl{
		RequestThermalProfile: "show-preheat",
		PresentationPowerOn:   &NightPowerBinding{Action: "power-on"},
		PresentationPowerOff: &NightPresentationPowerOff{
			NightPowerBinding: NightPowerBinding{Action: "power-off", PowerDomain: NightPowerDomainPresentation, DomainProvenance: NightDomainProvenanceOperatorDeclared},
			RemovalPolicy:     NightRemovalPolicyAfterActions,
			Prerequisites: []NightPrerequisite{
				{Kind: NightPrerequisiteKindAction, Action: "lamps-off"},
				{Kind: NightPrerequisiteKindEvidence, Action: "lamps-cool"},
				{Kind: NightPrerequisiteKindDelay, DelayMs: 5000},
			},
		},
	})
	want := &NightSiteActions{
		PrepareSite: []string{"show-preheat"},
		PowerDown: &NightSitePowerDown{AdditionalSequences: []ShutdownSequence{{ID: "power-off", Label: "Power off", Steps: []ShutdownStep{
			{Action: "lamps-off"},
			{Action: "lamps-cool", RequireConfirmation: true},
			{DelayMs: 5000},
			{Action: "power-off", PowerDomain: NightPowerDomainPresentation, DomainProvenance: NightDomainProvenanceOperatorDeclared},
		}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPowerDownSequencesSkipsGlobalAndAppendsNightOnly(t *testing.T) {
	global := []ShutdownSequence{{ID: "fm"}, {ID: "projectors"}}
	a := &NightSiteActions{PowerDown: &NightSitePowerDown{SkipSequences: []string{"fm"}, AdditionalSequences: []ShutdownSequence{{ID: "porch"}}}}
	var ids []string
	for _, s := range a.PowerDownSequences(global) {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, []string{"projectors", "porch"}) {
		t.Fatalf("ids = %v", ids)
	}
	if n := len((*NightSiteActions)(nil).PowerDownSequences(global)); n != 2 {
		t.Fatalf("nil site actions ran %d sequences, want every global one", n)
	}
}

func TestStoredLegacyRevisionLoadsAsSiteActions(t *testing.T) {
	var p NightSessionPayload
	raw := `{"show":"halloween-2026","siteControl":{"requestThermalProfile":"show-preheat"}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Show != "halloween-2026" || p.SiteActions == nil || len(p.SiteActions.PrepareSite) != 1 || p.SiteActions.PrepareSite[0] != "show-preheat" {
		t.Fatalf("payload = %+v, want the thermal profile as the prepare-site action", p)
	}
}
