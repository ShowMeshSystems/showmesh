package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
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
// push blindly, the review finding this closes: a null settings object,
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
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, nil, state, discardLogger())
	if _, ok := ops["fppconnect.configure"]; !ok {
		t.Fatal(`newOperationRegistry() does not contain "fppconnect.configure"`)
	}
}

func TestFPPConnectOperationsNilStateRegistersNothing(t *testing.T) {
	ops := fppConnectOperations(nil, t.TempDir(), discardLogger())
	if ops != nil {
		t.Errorf("fppConnectOperations(nil, ...) = %v, want nil", ops)
	}
}

// TestFPPConnectConfigureClampsOverlongChannelRangesButAppliesTheRest
// proves the review-round-2 fix: an over-long channelRanges is clamped to
// "" rather than rejecting the whole command, and activeShow, showNames,
// and settings still apply and persist, the coordinator's own equivalent
// degrade shape (internal/coordinator/fppconnectpush drops only the
// range), mirrored on the agent's decode boundary.
func TestFPPConnectConfigureClampsOverlongChannelRangesButAppliesTheRest(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir, logger: discardLogger()}

	overlong := strings.Repeat("9", multisync.MaxPingRangesLength+1)
	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, overlong, "Front Yard", []string{"Front Yard", "Back Yard"}, validFPPConnectSettingsMap())

	result, err := op.configure(context.Background(), params, time.Now)
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !result.Confirmed {
		t.Errorf("Confirmed = false, want true (the clamp itself is the confirmed outcome)")
	}
	if result.Value != "" {
		t.Errorf("Value = %v, want empty (clamped)", result.Value)
	}
	if got := state.ChannelRanges(); got != "" {
		t.Errorf("state.ChannelRanges() = %q, want empty (clamped)", got)
	}

	name, known, ever := state.ActiveShow()
	if !ever || !known || name != "Front Yard" {
		t.Errorf("state.ActiveShow() = (%q, %v, %v), want (Front Yard, true, true), the rest of the push must still apply", name, known, ever)
	}
	if names := state.ShowNames(); len(names) != 2 {
		t.Errorf("state.ShowNames() = %v, want 2 entries, the rest of the push must still apply", names)
	}
	if _, ok := state.Settings(); !ok {
		t.Error("state.Settings() ok = false, want true, the rest of the push must still apply")
	}

	restarted := newFPPConnectState()
	loaded, err := restarted.Load(dir)
	if err != nil || !loaded {
		t.Fatalf("Load after configure: loaded=%v err=%v", loaded, err)
	}
	if got := restarted.ChannelRanges(); got != "" {
		t.Errorf("restarted.ChannelRanges() = %q, want empty (the clamped value must be what was persisted)", got)
	}
}

// TestFPPConnectConfigureSaveFailureLeavesStateUnchanged proves the
// review-round-2 fix: when persisting fails, the live in-memory holder is
// never updated, a restart must never silently revert state a failed
// Save never actually wrote.
func TestFPPConnectConfigureSaveFailureLeavesStateUnchanged(t *testing.T) {
	dir := t.TempDir()
	// Occupy the state subdirectory's own path with a file, so MkdirAll
	// inside saveFPPConnectSnapshot fails.
	blocker := filepath.Join(dir, fppConnectStateSubdir)
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	state := newFPPConnectState()
	op := &fppConnectConfigureOperation{state: state, assetDir: dir, logger: discardLogger()}

	params := fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149", "Front Yard", []string{"Front Yard"}, validFPPConnectSettingsMap())
	if _, err := op.configure(context.Background(), params, time.Now); err == nil {
		t.Fatal("configure() err = nil despite an unwritable state directory, want an error")
	}

	if got := state.ChannelRanges(); got != "" {
		t.Errorf("state.ChannelRanges() = %q after a failed Save, want unchanged (empty), Apply must not run before a successful Save", got)
	}
	name, known, ever := state.ActiveShow()
	if ever || known || name != "" {
		t.Errorf("state.ActiveShow() = (%q, %v, %v) after a failed Save, want (\"\", false, false), unchanged", name, known, ever)
	}
	if _, ok := state.Settings(); ok {
		t.Error("state.Settings() ok = true after a failed Save, want false, unchanged")
	}
}

// baseFPPConnectConfigureCmd builds a valid "fppconnect.configure"
// CmdPayload carrying idempotencyKey, mirroring baseEchoCmd
// (command_test.go) one action over.
func baseFPPConnectConfigureCmd(commandID, idempotencyKey string) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:      commandID,
		IdempotencyKey: idempotencyKey,
		Action:         "fppconnect.configure",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params: fppConnectConfigureParamsMap(fppConnectConfigureSchema, "0-149", "Front Yard",
			[]string{"Front Yard"}, validFPPConnectSettingsMap()),
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "showmesh-coordinator-config-push", PrincipalName: "showmesh-coordinator-config-push"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
}

// TestHandleMessageFPPConnectConfigureSaveFailureDoesNotStrandTheIdempotencyKey
// proves the review-round-2 fix for the bug review round 1 introduced: a
// Save failure must not occupy the idempotency cache under its key,
// because the coordinator's own key for a config push is deterministic in
// the resolved state (internal/coordinator/fppconnectpush) and a real
// redelivery (the node's own next hello, or the coordinator's periodic
// re-push) would otherwise carry the SAME key and be answered from the
// cached failure without ever executing again. This drives the real
// CommandHandler.HandleMessage path (not fppConnectConfigureOperation.
// configure directly), because the fix lives in HandleMessage's own
// cache-vs-release decision, not in the operation.
func TestHandleMessageFPPConnectConfigureSaveFailureDoesNotStrandTheIdempotencyKey(t *testing.T) {
	dir := t.TempDir()
	state := newFPPConnectState()
	ops := fppConnectOperations(state, dir, discardLogger())
	clock := &fakeClock{t: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)}
	h := newTestHandler(ops, clock)

	// Occupy the state subdirectory's own path with a file, so the first
	// delivery's Save fails.
	blocker := filepath.Join(dir, fppConnectStateSubdir)
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := baseFPPConnectConfigureCmd("cmd-1", "idem-fppconnect-1")
	topic, payload := buildCmdMessage(t, clock, cmd)

	pub1 := newFakePublisher()
	h.HandleMessage(context.Background(), pub1, topic, payload)
	calls1 := pub1.snapshot()
	if len(calls1) != 1 {
		t.Fatalf("first delivery: %d results published, want 1", len(calls1))
	}
	result1 := decodeResultFromCall(t, calls1[0])
	if result1.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("first delivery outcome = %q, want %q", result1.Outcome, mqttproto.OutcomeFailed)
	}
	if !strings.Contains(result1.Reason, "persist state") {
		t.Errorf("first delivery reason = %q, want it to name the persist failure", result1.Reason)
	}
	if got := state.ChannelRanges(); got != "" {
		t.Fatalf("state.ChannelRanges() after the failed first delivery = %q, want empty", got)
	}

	// The directory is writable again (the transient condition cleared),
	// and the SAME command (same IdempotencyKey) is redelivered, exactly
	// as a real QoS 1 redelivery, or the coordinator's own next push
	// carrying the identical resolved state, would.
	if err := os.Remove(blocker); err != nil {
		t.Fatalf("clearing the blocker: %v", err)
	}

	pub2 := newFakePublisher()
	h.HandleMessage(context.Background(), pub2, topic, payload)
	calls2 := pub2.snapshot()
	if len(calls2) != 1 {
		t.Fatalf("redelivery: %d results published, want 1", len(calls2))
	}
	result2 := decodeResultFromCall(t, calls2[0])
	if result2.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("redelivery outcome = %q, want %q (a cached failure replay would still be %q); body: %+v",
			result2.Outcome, mqttproto.OutcomeConfirmed, mqttproto.OutcomeFailed, result2)
	}

	if got := state.ChannelRanges(); got != "0-149" {
		t.Errorf("state.ChannelRanges() after the redelivery = %q, want 0-149 (the redelivery must actually re-execute)", got)
	}

	restarted := newFPPConnectState()
	loaded, err := restarted.Load(dir)
	if err != nil || !loaded {
		t.Fatalf("Load after redelivery: loaded=%v err=%v (the redelivery must have persisted, not merely applied in memory)", loaded, err)
	}
	if got := restarted.ChannelRanges(); got != "0-149" {
		t.Errorf("restarted.ChannelRanges() = %q, want 0-149", got)
	}
}
