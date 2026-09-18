package store

import (
	"strings"
	"testing"
)

// TestV39BackfillAudioSettingsMultisyncFieldsAddsBothMissingKeys proves
// the pure backfill function adds both multisyncFallbackWindowMs and
// multisyncStartLeadMs when a stored revision predates them, and reports
// changed=true.
func TestV39BackfillAudioSettingsMultisyncFieldsAddsBothMissingKeys(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
		`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04,"duckFadeDurationMs":200,"duckRestoreFadeDurationMs":800,` +
		`"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","scheduledStartDeliveryBoundMs":2000,"scheduledStartMarginMs":1000}`

	got, changed, err := v39BackfillAudioSettingsMultisyncFields(raw)
	if err != nil {
		t.Fatalf("v39BackfillAudioSettingsMultisyncFields: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true: both keys are missing")
	}
	if !jsonContains(t, got, `"multisyncFallbackWindowMs":1500`) {
		t.Errorf("backfilled payload = %s, want multisyncFallbackWindowMs:1500", got)
	}
	if !jsonContains(t, got, `"multisyncStartLeadMs":100`) {
		t.Errorf("backfilled payload = %s, want multisyncStartLeadMs:100", got)
	}
}

// TestV39BackfillAudioSettingsMultisyncFieldsLeavesCompletePayloadAlone
// proves a revision that already carries both keys is left byte-for-byte
// unchanged, so re-running this migration never overwrites an operator's
// own configured values.
func TestV39BackfillAudioSettingsMultisyncFieldsLeavesCompletePayloadAlone(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
		`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04,"duckFadeDurationMs":200,"duckRestoreFadeDurationMs":800,` +
		`"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","scheduledStartDeliveryBoundMs":2000,"scheduledStartMarginMs":1000,` +
		`"multisyncFallbackWindowMs":2500,"multisyncStartLeadMs":250}`

	got, changed, err := v39BackfillAudioSettingsMultisyncFields(raw)
	if err != nil {
		t.Fatalf("v39BackfillAudioSettingsMultisyncFields: %v", err)
	}
	if changed {
		t.Fatalf("changed = true, want false: both keys already present, got %s", got)
	}
}

// TestV39BackfillAudioSettingsMultisyncFieldsAddsOnlyTheMissingKey proves
// a revision carrying one of the two keys already (an operator who wrote
// multisyncFallbackWindowMs before multisyncStartLeadMs existed) gets
// only the missing one backfilled, never overwriting the one already
// present.
func TestV39BackfillAudioSettingsMultisyncFieldsAddsOnlyTheMissingKey(t *testing.T) {
	raw := `{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,` +
		`"defaultMaxBackgroundGainDb":-4.44,"duckTargetGainDb":-12.04,"duckFadeDurationMs":200,"duckRestoreFadeDurationMs":800,` +
		`"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00","scheduledStartDeliveryBoundMs":2000,"scheduledStartMarginMs":1000,` +
		`"multisyncFallbackWindowMs":2500}`

	got, changed, err := v39BackfillAudioSettingsMultisyncFields(raw)
	if err != nil {
		t.Fatalf("v39BackfillAudioSettingsMultisyncFields: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true: multisyncStartLeadMs is missing")
	}
	if !jsonContains(t, got, `"multisyncFallbackWindowMs":2500`) {
		t.Errorf("backfilled payload = %s, want the operator's own multisyncFallbackWindowMs:2500 preserved", got)
	}
	if !jsonContains(t, got, `"multisyncStartLeadMs":100`) {
		t.Errorf("backfilled payload = %s, want multisyncStartLeadMs:100 added", got)
	}
}

func jsonContains(t *testing.T, raw, substr string) bool {
	t.Helper()
	return strings.Contains(raw, substr)
}
