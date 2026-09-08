package config

import (
	"fmt"
	"strings"
	"testing"
)

// withTopLevelFragment injects fragment as extra top-level key(s) into
// base's JSON object, the same "TrimSuffix + append" idiom
// nightsession_test.go's own siteControl/interlocks tests already use.
func withTopLevelFragment(base, fragment string) string {
	return strings.TrimSuffix(base, "}") + "," + fragment + "}"
}

func decodeWithSiteControlFragment(t *testing.T, fragment string) (NightSessionPayload, *ValidationError) {
	t.Helper()
	raw := withTopLevelFragment(validNightSessionJSON, `"siteControl":`+fragment)
	return DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent)
}

func decodeWithInterlocksFragment(t *testing.T, fragment string) (NightSessionPayload, *ValidationError) {
	t.Helper()
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":`+fragment)
	return DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent)
}

// --- siteControl: not-configured / empty ---

func TestSiteControlOmittedIsNilNotZeroValue(t *testing.T) {
	p := decodeValidNightSession(t)
	if p.SiteControl != nil {
		t.Fatalf("expected a nil SiteControl when siteControl is omitted, got %+v", p.SiteControl)
	}
	if p.Interlocks != nil {
		t.Fatalf("expected nil Interlocks when interlocks is omitted, got %+v", p.Interlocks)
	}
}

func TestSiteControlEmptyObjectRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "siteControl" {
		t.Fatalf("expected field-required on an empty siteControl object, got %+v", verr)
	}
}

func TestSiteControlUnknownKeyRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{"presentationPowerOn":{"action":"site-on","powerDomain":"presentation","domainProvenance":"operator-declared"},"bogus":true}`)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key on siteControl.bogus, got %+v", verr)
	}
}

// --- siteControl: valid configurations ---

func TestSiteControlValidImmediate(t *testing.T) {
	p, verr := decodeWithSiteControlFragment(t, `{
		"requestThermalProfile": "show-preheat",
		"presentationPowerOn": {"action": "site-on", "powerDomain": "presentation", "domainProvenance": "operator-declared"},
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "immediate", "immediateSafeAttestation": true}
	}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.SiteControl == nil {
		t.Fatalf("expected a non-nil SiteControl")
	}
	if p.SiteControl.RequestThermalProfile != "show-preheat" {
		t.Fatalf("requestThermalProfile not decoded: %+v", p.SiteControl)
	}
	if p.SiteControl.PresentationPowerOn == nil || p.SiteControl.PresentationPowerOn.PowerDomain != NightPowerDomainPresentation {
		t.Fatalf("presentationPowerOn not decoded correctly: %+v", p.SiteControl.PresentationPowerOn)
	}
	off := p.SiteControl.PresentationPowerOff
	if off == nil || off.RemovalPolicy != NightRemovalPolicyImmediate || !off.ImmediateSafeAttestation {
		t.Fatalf("presentationPowerOff not decoded correctly: %+v", off)
	}
	if len(off.Prerequisites) != 0 {
		t.Fatalf("expected no prerequisites under immediate policy, got %+v", off.Prerequisites)
	}
}

func TestSiteControlValidAfterActions(t *testing.T) {
	p, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {
			"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared",
			"removalPolicy": "after-actions",
			"prerequisites": [
				{"kind": "action", "action": "projectors-safe-off", "requireConfirmation": true},
				{"kind": "delay", "delayMs": 300000},
				{"kind": "evidence", "action": "cooldown-evidence"}
			]
		}
	}`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	off := p.SiteControl.PresentationPowerOff
	if off.RemovalPolicy != NightRemovalPolicyAfterActions {
		t.Fatalf("expected after-actions, got %+v", off)
	}
	if len(off.Prerequisites) != 3 {
		t.Fatalf("expected 3 prerequisites, got %+v", off.Prerequisites)
	}
	if off.Prerequisites[0].Kind != NightPrerequisiteKindAction || !off.Prerequisites[0].RequireConfirmation {
		t.Fatalf("prerequisite 0 wrong: %+v", off.Prerequisites[0])
	}
	if off.Prerequisites[1].Kind != NightPrerequisiteKindDelay || off.Prerequisites[1].DelayMs != 300000 {
		t.Fatalf("prerequisite 1 wrong: %+v", off.Prerequisites[1])
	}
	if off.Prerequisites[2].Kind != NightPrerequisiteKindEvidence || off.Prerequisites[2].Action != "cooldown-evidence" {
		t.Fatalf("prerequisite 2 wrong: %+v", off.Prerequisites[2])
	}
}

// --- power domain refusal (RESTING-MODE.md §10.2) ---

func TestPresentationPowerOffWrongDomainRefused(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "environmental", "domainProvenance": "operator-declared", "removalPolicy": "immediate", "immediateSafeAttestation": true}
	}`)
	if verr == nil || verr.Code != ValidationCodePowerDomainRefused {
		t.Fatalf("expected power-domain-refused for an environmental presentationPowerOff, got %+v", verr)
	}
}

func TestPresentationPowerOnWrongDomainRefused(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOn": {"action": "site-on", "powerDomain": "mixed", "domainProvenance": "operator-declared"}
	}`)
	if verr == nil || verr.Code != ValidationCodePowerDomainRefused {
		t.Fatalf("expected power-domain-refused for a mixed presentationPowerOn, got %+v", verr)
	}
}

func TestDomainProvenanceProviderRefused(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOn": {"action": "site-on", "powerDomain": "presentation", "domainProvenance": "provider"}
	}`)
	if verr == nil || verr.Code != ValidationCodeDomainProvenanceRefused {
		t.Fatalf("expected domain-provenance-refused for domainProvenance \"provider\", got %+v", verr)
	}
}

// --- removal policy: no default, no half-filled configuration ---

func TestRemovalPolicyImmediateRequiresAttestation(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "immediate"}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "siteControl.presentationPowerOff.immediateSafeAttestation" {
		t.Fatalf("expected field-required on missing immediateSafeAttestation, got %+v", verr)
	}
}

func TestRemovalPolicyImmediateFalseAttestationRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "immediate", "immediateSafeAttestation": false}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "siteControl.presentationPowerOff.immediateSafeAttestation" {
		t.Fatalf("expected field-invalid on immediateSafeAttestation:false, got %+v", verr)
	}
}

func TestRemovalPolicyImmediateRejectsPrerequisites(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "immediate", "immediateSafeAttestation": true, "prerequisites": [{"kind":"delay","delayMs":1000}]}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "siteControl.presentationPowerOff.prerequisites" {
		t.Fatalf("expected field-invalid rejecting prerequisites under immediate, got %+v", verr)
	}
}

func TestRemovalPolicyAfterActionsRequiresPrerequisites(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "after-actions"}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "siteControl.presentationPowerOff.prerequisites" {
		t.Fatalf("expected field-required on missing prerequisites, got %+v", verr)
	}
}

func TestRemovalPolicyAfterActionsRejectsAttestation(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "after-actions", "immediateSafeAttestation": true, "prerequisites": [{"kind":"delay","delayMs":1000}]}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "siteControl.presentationPowerOff.immediateSafeAttestation" {
		t.Fatalf("expected field-invalid rejecting immediateSafeAttestation under after-actions, got %+v", verr)
	}
}

func TestRemovalPolicyAfterActionsEmptyPrerequisitesRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared", "removalPolicy": "after-actions", "prerequisites": []}
	}`)
	if verr == nil || verr.Code != ValidationCodePrerequisitesEmpty {
		t.Fatalf("expected prerequisites-empty, got %+v", verr)
	}
}

func TestRemovalPolicyMissingIsRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared"}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "siteControl.presentationPowerOff.removalPolicy" {
		t.Fatalf("expected field-required on a missing removalPolicy (no default), got %+v", verr)
	}
}

// --- prerequisite cycle detection ---

func TestPrerequisiteCycleSelfReferenceRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {
			"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared",
			"removalPolicy": "after-actions",
			"prerequisites": [{"kind": "action", "action": "site-off"}]
		}
	}`)
	if verr == nil || verr.Code != ValidationCodePowerOffPrerequisiteCycle {
		t.Fatalf("expected power-off-prerequisite-cycle for a self-referencing prerequisite, got %+v", verr)
	}
}

func TestPrerequisiteDelayRejectsActionField(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {
			"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared",
			"removalPolicy": "after-actions",
			"prerequisites": [{"kind": "delay", "delayMs": 1000, "action": "not-allowed"}]
		}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid rejecting action alongside a delay prerequisite, got %+v", verr)
	}
}

func TestPrerequisiteDelayExceedsBoundRejected(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {
			"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared",
			"removalPolicy": "after-actions",
			"prerequisites": [{"kind": "delay", "delayMs": 999999999999}]
		}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid rejecting an out-of-bounds delayMs, got %+v", verr)
	}
}

func TestPrerequisiteEvidenceRejectsRequireConfirmation(t *testing.T) {
	_, verr := decodeWithSiteControlFragment(t, `{
		"presentationPowerOff": {
			"action": "site-off", "powerDomain": "presentation", "domainProvenance": "operator-declared",
			"removalPolicy": "after-actions",
			"prerequisites": [{"kind": "evidence", "action": "cooldown-ok", "requireConfirmation": true}]
		}
	}`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid rejecting requireConfirmation on an evidence prerequisite, got %+v", verr)
	}
}

// --- cross-show references inside siteControl ---

func TestSiteControlActionCrossShowRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"siteControl":{"presentationPowerOn":{"action":"site-on","powerDomain":"presentation","domainProvenance":"operator-declared"}}`)
	christmasResolver := func(string) (string, bool) { return "christmas-2026", true }
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, christmasResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference {
		t.Fatalf("expected cross-show-reference on siteControl's own action, got %+v", verr)
	}
}

func TestSiteControlActionUnknownRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"siteControl":{"presentationPowerOn":{"action":"site-on","powerDomain":"presentation","domainProvenance":"operator-declared"}}`)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysFalseActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("expected field-unknown-reference on an unresolvable siteControl action, got %+v", verr)
	}
}

// --- interlocks: the closed behavior matrix's own valid shapes ---

func TestInterlockObserveValid(t *testing.T) {
	p, verr := decodeWithInterlocksFragment(t, `[{"name":"temp-observe","phase":"projector-strike","posture":"observe","signal":"enclosure-temp","failureText":"enclosure is cold"}]`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if len(p.Interlocks) != 1 || p.Interlocks[0].OnUnavailable != "" || p.Interlocks[0].OverridePolicy != "" {
		t.Fatalf("unexpected observe rule shape: %+v", p.Interlocks)
	}
}

func TestInterlockBlockValid(t *testing.T) {
	p, verr := decodeWithInterlocksFragment(t, `[{"name":"temp-block","phase":"projector-strike","posture":"block","signal":"enclosure-temp","failureText":"too cold to strike","onUnavailable":"block","overridePolicy":"authorized-operator","freshnessSeconds":30}]`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	r := p.Interlocks[0]
	if r.OnUnavailable != NightInterlockOnUnavailableBlock || r.OverridePolicy != NightInterlockOverridePolicyAuthorizedOperator {
		t.Fatalf("unexpected block rule shape: %+v", r)
	}
	if r.FreshnessSeconds == nil || *r.FreshnessSeconds != 30 {
		t.Fatalf("expected freshnessSeconds 30, got %+v", r.FreshnessSeconds)
	}
}

func TestInterlockDisabledValid(t *testing.T) {
	p, verr := decodeWithInterlocksFragment(t, `[{"name":"unused","phase":"fade-out-night","posture":"disabled"}]`)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Interlocks[0].Signal != "" || p.Interlocks[0].FailureText != "" {
		t.Fatalf("expected a bare disabled rule, got %+v", p.Interlocks[0])
	}
}

// --- interlocks: the closed behavior matrix's own invalid combinations ---

func TestInterlockDisabledRejectsExtraFields(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"fade-out-night","posture":"disabled","signal":"whatever"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key on a disabled rule carrying signal, got %+v", verr)
	}
}

func TestInterlockObserveRejectsOnUnavailable(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"projector-strike","posture":"observe","signal":"s","failureText":"f","onUnavailable":"block"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid rejecting onUnavailable on an observe rule, got %+v", verr)
	}
}

func TestInterlockObserveRejectsOverridePolicy(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"projector-strike","posture":"observe","signal":"s","failureText":"f","overridePolicy":"authorized-operator"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid rejecting overridePolicy on an observe rule, got %+v", verr)
	}
}

func TestInterlockBlockRequiresOnUnavailable(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"projector-strike","posture":"block","signal":"s","failureText":"f","overridePolicy":"none"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "interlocks[0].onUnavailable" {
		t.Fatalf("expected field-required for a missing onUnavailable on a block rule, got %+v", verr)
	}
}

func TestInterlockBlockRequiresOverridePolicy(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"projector-strike","posture":"block","signal":"s","failureText":"f","onUnavailable":"allow"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "interlocks[0].overridePolicy" {
		t.Fatalf("expected field-required for a missing overridePolicy on a block rule, got %+v", verr)
	}
}

func TestInterlockUnknownPhaseRejected(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"lunch-break","posture":"disabled"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid on an unknown phase, got %+v", verr)
	}
}

func TestInterlockUnknownPostureRejected(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"start-night","posture":"maybe"}]`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid on an unknown posture, got %+v", verr)
	}
}

func TestInterlockDuplicateNameRejected(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[
		{"name":"dup","phase":"start-night","posture":"disabled"},
		{"name":"dup","phase":"fade-out-night","posture":"disabled"}
	]`)
	if verr == nil || verr.Code != ValidationCodeInterlockNameDuplicate {
		t.Fatalf("expected interlock-name-duplicate, got %+v", verr)
	}
}

func TestInterlockFreshnessOutOfBoundsRejected(t *testing.T) {
	_, verr := decodeWithInterlocksFragment(t, `[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f","freshnessSeconds":999999}]`)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid {
		t.Fatalf("expected field-invalid on an out-of-bounds freshnessSeconds, got %+v", verr)
	}
}

// --- interlocks: signal must be a confirmable mqtt action ---

func TestInterlockSignalNonMQTTRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f"}]`)
	nonMQTT := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationFPP, MQTTExpectKind: ""}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, nonMQTT, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeInterlockSignalNotConfirmable {
		t.Fatalf("expected interlock-signal-not-confirmable for a non-mqtt signal, got %+v", verr)
	}
}

func TestInterlockSignalExpectNoneRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f"}]`)
	fireAndForget := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindNone}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, fireAndForget, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeInterlockSignalNotConfirmable {
		t.Fatalf("expected interlock-signal-not-confirmable for expect.kind \"none\", got %+v", verr)
	}
}

func TestInterlockSignalCrossShowRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f"}]`)
	christmas := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "christmas-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindBoolean}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, christmas, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeCrossShowReference {
		t.Fatalf("expected cross-show-reference for an interlock signal in a different show, got %+v", verr)
	}
}

func TestInterlockSignalUnresolvableRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f"}]`)
	none := func(string) (NightInterlockSignalInfo, bool) { return NightInterlockSignalInfo{}, false }
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, none, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference {
		t.Fatalf("expected field-unknown-reference for an unresolvable signal, got %+v", verr)
	}
}

// --- omitted configuration: the reference installation's own shape ---

// TestNightSessionRunsUnchangedWithoutSiteControlOrInterlocks is the
// omitted-configuration case named explicitly: a payload with neither
// block decodes to a valid, fully populated NightSessionPayload with both
// fields empty, never a degraded or partially-decoded result.
func TestNightSessionRunsUnchangedWithoutSiteControlOrInterlocks(t *testing.T) {
	p := decodeValidNightSession(t)
	if p.SiteControl != nil || len(p.Interlocks) != 0 {
		t.Fatalf("expected no site control and no interlocks on the reference installation's own shape, got siteControl=%+v interlocks=%+v", p.SiteControl, p.Interlocks)
	}
	if p.Show == "" || p.Resting.Playlist == "" {
		t.Fatalf("expected the rest of the payload to decode normally: %+v", p)
	}
}

// --- interlock count cap: reviewer suspicion, this seam's safety review round ---

// TestInterlocksRejectsExcessiveCount proves night.session.interlocks is
// bounded the same way presentationPowerOff.prerequisites already is: a
// reviewer-constructed 200-rule payload previously decoded successfully,
// and each rule is dispatched serially at run-readiness with its own
// deadline of up to 120 seconds, making one HTTP request's own
// live-dispatch time unbounded in practice.
func TestInterlocksRejectsExcessiveCount(t *testing.T) {
	var rules []string
	for i := 0; i <= nightInterlockMaxCount; i++ {
		rules = append(rules, fmt.Sprintf(`{"name":"r%d","phase":"start-night","posture":"disabled"}`, i))
	}
	fragment := "[" + strings.Join(rules, ",") + "]"
	_, verr := decodeWithInterlocksFragment(t, fragment)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "interlocks" {
		t.Fatalf("expected field-invalid rejecting more than %d interlocks, got %+v", nightInterlockMaxCount, verr)
	}
}

func TestInterlocksAcceptsMaxCount(t *testing.T) {
	var rules []string
	for i := 0; i < nightInterlockMaxCount; i++ {
		rules = append(rules, fmt.Sprintf(`{"name":"r%d","phase":"start-night","posture":"disabled"}`, i))
	}
	fragment := "[" + strings.Join(rules, ",") + "]"
	p, verr := decodeWithInterlocksFragment(t, fragment)
	if verr != nil {
		t.Fatalf("unexpected error at exactly the cap: %+v", verr)
	}
	if len(p.Interlocks) != nightInterlockMaxCount {
		t.Fatalf("expected %d interlocks, got %d", nightInterlockMaxCount, len(p.Interlocks))
	}
}

// --- also-fix: a "block" rule's signal must be able to say no ---

// TestInterlockBlockSignalTextKindRejected proves a "block" rule whose
// signal uses expect.kind "text" is refused at write time: text always
// confirms on any valid UTF-8 payload (config/showaction.go's own
// resolveMQTTActionPayload), so it can never produce a negative answer
// and could only ever withhold via onUnavailable, never a real
// condition-false reading.
func TestInterlockBlockSignalTextKindRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"block","signal":"s","failureText":"f","onUnavailable":"block","overridePolicy":"authorized-operator"}]`)
	textKind := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindText}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, textKind, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeInterlockSignalNoFalseAnswer {
		t.Fatalf("expected interlock-signal-no-false-answer for a block rule on kind text, got %+v", verr)
	}
}

// TestInterlockBlockSignalNumberWithNoValueRejected proves a "block"
// rule whose signal uses expect.kind "number" with no configured
// comparison value is refused the same way: any successfully parsed
// number confirms, so it can never produce a negative answer either.
func TestInterlockBlockSignalNumberWithNoValueRejected(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"block","signal":"s","failureText":"f","onUnavailable":"block","overridePolicy":"authorized-operator"}]`)
	numberNoValue := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindNumber, MQTTExpectValuePresent: false}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, numberNoValue, alwaysTrueMediaPlaylistCurrent)
	if verr == nil || verr.Code != ValidationCodeInterlockSignalNoFalseAnswer {
		t.Fatalf("expected interlock-signal-no-false-answer for a block rule on kind number with no value, got %+v", verr)
	}
}

// TestInterlockBlockSignalNumberWithValueAccepted proves the refusal is
// specific to the inexpressible cases, not to kind "number" in general.
func TestInterlockBlockSignalNumberWithValueAccepted(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"block","signal":"s","failureText":"f","onUnavailable":"block","overridePolicy":"authorized-operator"}]`)
	numberWithValue := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindNumber, MQTTExpectValuePresent: true}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, numberWithValue, alwaysTrueMediaPlaylistCurrent)
	if verr != nil {
		t.Fatalf("unexpected error for a block rule on kind number with a value: %+v", verr)
	}
}

// TestInterlockObserveSignalTextKindAccepted proves the refusal is
// specific to "block": an "observe" rule that can never report false is
// merely uninformative, not misleading about a control effect that never
// fires, so it is not refused.
func TestInterlockObserveSignalTextKindAccepted(t *testing.T) {
	raw := withTopLevelFragment(validNightSessionJSON, `"interlocks":[{"name":"x","phase":"start-night","posture":"observe","signal":"s","failureText":"f"}]`)
	textKind := func(string) (NightInterlockSignalInfo, bool) {
		return NightInterlockSignalInfo{Show: "halloween-2026", Integration: ShowActionIntegrationMQTT, MQTTExpectKind: MQTTExpectKindText}, true
	}
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, textKind, alwaysTrueMediaPlaylistCurrent)
	if verr != nil {
		t.Fatalf("unexpected error for an observe rule on kind text: %+v", verr)
	}
}
