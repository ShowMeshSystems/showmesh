package agent

import (
	"context"
	"testing"
	"time"
)

func fppConnectConfigureParamsMap(schema, channelRanges string, activeShow any, showNames []string, settings map[string]any) map[string]any {
	return map[string]any{
		"schema":        schema,
		"channelRanges": channelRanges,
		"activeShow":    activeShow,
		"showNames":     showNames,
		"settings":      settings,
	}
}

func validFPPConnectSettingsMap() map[string]any {
	return map[string]any{"enabled": true, "maxFileBytes": float64(2147483648), "maxAssetDirBytes": float64(21474836480)}
}

func TestFPPConnectConfigureAppliesAndPersists(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149,300-449", "Front Yard", []string{"Front Yard", "Back Yard"}, validFPPConnectSettingsMap())

	result, err := op.configure(context.Background(), params, time.Now)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !result.Confirmed {
		t.Errorf("Confirmed = false, want true")
	}
	if result.Value != "0-149,300-449" {
		t.Errorf("Value = %v, want 0-149,300-449", result.Value)
	}

	if state.ChannelRanges() != "0-149,300-449" {
		t.Errorf("state.ChannelRanges() = %q", state.ChannelRanges())
	}
	name, known, ever := state.ActiveShow()
	if !ever || !known || name != "Front Yard" {
		t.Errorf("state.ActiveShow() = (%q, %v, %v), want (Front Yard, true, true)", name, known, ever)
	}
	names := state.ShowNames()
	if len(names) != 2 {
		t.Errorf("state.ShowNames() = %v, want 2 entries", names)
	}
	settings, ok := state.Settings()
	if !ok || settings.MaxFileBytes != 2147483648 {
		t.Errorf("state.Settings() = (%+v, %v)", settings, ok)
	}

	// Persisted: a fresh holder loading from the same directory sees it.
	restarted := newFPPConnectState()
	loaded, err := restarted.Load(dir)
	if err != nil || !loaded {
		t.Fatalf("Load after configure: loaded=%v err=%v", loaded, err)
	}
	if restarted.ChannelRanges() != "0-149,300-449" {
		t.Errorf("restarted.ChannelRanges() = %q", restarted.ChannelRanges())
	}
}

func TestFPPConnectConfigureNullActiveShow(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "", nil, []string{"Front Yard"}, validFPPConnectSettingsMap())
	if _, err := op.configure(context.Background(), params, time.Now); err != nil {
		t.Fatalf("configure: %v", err)
	}

	name, known, ever := state.ActiveShow()
	if !ever {
		t.Fatal("ever = false, want true")
	}
	if known {
		t.Error("known = true for a pushed null activeShow, want false")
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
}

func TestFPPConnectConfigureEmptyChannelRangesForNodeWithNoSurfaces(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "", nil, []string{}, validFPPConnectSettingsMap())
	result, err := op.configure(context.Background(), params, time.Now)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if result.Value != "" {
		t.Errorf("Value = %v, want empty string", result.Value)
	}
	if state.ChannelRanges() != "" {
		t.Errorf("state.ChannelRanges() = %q, want empty", state.ChannelRanges())
	}
}

func TestFPPConnectConfigureRefusesUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap("some.other.schema/v1", "0-149", "Front Yard", []string{"Front Yard"}, validFPPConnectSettingsMap())
	if _, err := op.configure(context.Background(), params, time.Now); err == nil {
		t.Fatal("configure() err = nil for an unknown schema, want an error")
	}
	if state.ChannelRanges() != "" {
		t.Error("state was mutated despite an unknown schema refusal")
	}
}

func TestFPPConnectConfigureRefusesUnknownKey(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149", "Front Yard", []string{"Front Yard"}, validFPPConnectSettingsMap())
	params["extra"] = "surprise"
	if _, err := op.configure(context.Background(), params, time.Now); err == nil {
		t.Fatal("configure() err = nil for an unrecognized key, want an error")
	}
}

func TestFPPConnectConfigureRefusesMissingField(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149", "Front Yard", []string{"Front Yard"}, validFPPConnectSettingsMap())
	delete(params, "showNames")
	if _, err := op.configure(context.Background(), params, time.Now); err == nil {
		t.Fatal("configure() err = nil for a missing required field, want an error")
	}
}

// TestFPPConnectConfigureRefusesInvalidSettings proves the agent
// re-validates settings itself rather than trusting the coordinator's
// push blindly — the review finding this closes: a null settings object,
// or a negative/zero byte cap, must be refused and must never reach
// state.Settings().
func TestFPPConnectConfigureRefusesInvalidSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]any
	}{
		{"null settings", nil},
		{"zero maxFileBytes", map[string]any{"enabled": true, "maxFileBytes": float64(0), "maxAssetDirBytes": float64(1000)}},
		{"negative maxFileBytes", map[string]any{"enabled": true, "maxFileBytes": float64(-1), "maxAssetDirBytes": float64(1000)}},
		{"zero maxAssetDirBytes", map[string]any{"enabled": true, "maxFileBytes": float64(100), "maxAssetDirBytes": float64(0)}},
		{"maxAssetDirBytes below maxFileBytes", map[string]any{"enabled": true, "maxFileBytes": float64(1000), "maxAssetDirBytes": float64(999)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := newFPPConnectState()
			op := &fppConnectConfigureOperation{state: state, assetDir: dir}

			params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149", "Front Yard", []string{"Front Yard"}, tc.settings)
			if _, err := op.configure(context.Background(), params, time.Now); err == nil {
				t.Fatal("configure() err = nil, want a refusal")
			}
			if _, ok := state.Settings(); ok {
				t.Error("state.Settings() was applied despite an invalid settings refusal")
			}
		})
	}
}

func TestFPPConnectOperationsRegisteredInAllowlist(t *testing.T) {
	state := newFPPConnectState()
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, nil, state)
	if _, ok := ops["fppconnect.configure"]; !ok {
		t.Fatal(`newOperationRegistry() does not contain "fppconnect.configure"`)
	}
}

func TestFPPConnectOperationsNilStateRegistersNothing(t *testing.T) {
	ops := fppConnectOperations(nil, t.TempDir())
	if ops != nil {
		t.Errorf("fppConnectOperations(nil, ...) = %v, want nil", ops)
	}
}
