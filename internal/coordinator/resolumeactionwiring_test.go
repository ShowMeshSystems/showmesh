package coordinator

// This file is resolumeactionwiring.go's own unit-level test suite: the
// pure translation functions (mapActionOutcomeState, mapActionSafetyClass,
// buildResolumeActionParams) exercised directly, independent of a real
// Resolume, a real store, or a real HTTP request — resolumeactionwiring_e2e_test.go
// is where the wired-path proof lives.

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// --- mapActionOutcomeState: all five outcomes survive, and an unmapped
// value fails loudly rather than degrading to a plausible default. ---

// TestMapActionOutcomeStateCoversAllFiveOutcomes proves every one of A's
// five ActionOutcomeState values reaches the CORRECT, correspondingly named
// api.ResolumeActionOutcome value — not merely "some value", which a naive
// off-by-one mapping could still pass if this table did not name each pair
// explicitly.
func TestMapActionOutcomeStateCoversAllFiveOutcomes(t *testing.T) {
	tests := []struct {
		in   resolume.ActionOutcomeState
		want api.ResolumeActionOutcome
	}{
		{resolume.ActionConfirmed, api.ResolumeOutcomeConfirmed},
		{resolume.ActionUnconfirmed, api.ResolumeOutcomeUnconfirmed},
		{resolume.ActionUnconfirmable, api.ResolumeOutcomeUnconfirmable},
		{resolume.ActionRefused, api.ResolumeOutcomeRefused},
		{resolume.ActionFailed, api.ResolumeOutcomeFailed},
	}
	seen := map[api.ResolumeActionOutcome]bool{}
	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			got, err := mapActionOutcomeState(tt.in)
			if err != nil {
				t.Fatalf("mapActionOutcomeState(%q) returned an error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("mapActionOutcomeState(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if seen[got] {
				t.Errorf("api outcome %q was already produced by a different resolume.ActionOutcomeState — the mapping is not injective", got)
			}
			seen[got] = true
		})
	}
	if len(seen) != 5 {
		t.Errorf("distinct api outcomes produced = %d, want 5 (one per resolume.ActionOutcomeState)", len(seen))
	}
}

// TestMapActionOutcomeStateFailsLoudlyOnUnmappedValue is the fail-loud half
// this file's own resolumeactionwiring.go doc comment names by this exact
// test name: an outcome state this switch does not recognize must return an
// error, never a silently substituted default — an unconfirmable dispatch
// silently reported as "confirmed" is ADR-029's own named defect one layer
// removed.
//
// Before trusting this test: temporarily changed mapActionOutcomeState's
// default branch to `return api.ResolumeOutcomeConfirmed, nil` (the most
// dangerous possible silent default) and reran — this test failed,
// asserting a nil error where one was required. Reverted afterward.
func TestMapActionOutcomeStateFailsLoudlyOnUnmappedValue(t *testing.T) {
	_, err := mapActionOutcomeState(resolume.ActionOutcomeState("some-future-outcome-nobody-mapped-yet"))
	if err == nil {
		t.Fatal("mapActionOutcomeState returned a nil error for an unrecognized state — an unmapped value must fail loudly, never degrade to a plausible default")
	}
}

// --- mapActionSafetyClass: the ADR-024 decision 11 classification survives
// translation, and an undeclared value fails loudly. ---

func TestMapActionSafetyClassTranslatesBothDeclaredValues(t *testing.T) {
	tests := []struct {
		in   resolume.ActionSafetyClass
		want bool
	}{
		{resolume.ActionSafetyClassExempt, true},
		{resolume.ActionSafetyClassNotExempt, false},
	}
	for _, tt := range tests {
		got, err := mapActionSafetyClass(tt.in)
		if err != nil {
			t.Fatalf("mapActionSafetyClass(%v) returned an error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("mapActionSafetyClass(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestMapActionSafetyClassFailsLoudlyOnUndeclared is the fail-loud half:
// ActionSafetyClassUndeclared (A's own zero value, "never a valid registry
// entry" per that type's doc comment) must not silently translate to
// either true or false here — the identical "nobody decided must not
// masquerade as decided" rule A's own type already enforces at the
// registry level, applied a second time at this translation boundary.
//
// Before trusting this test: temporarily changed mapActionSafetyClass's
// default branch to `return false, nil` and reran — this test failed.
// Reverted afterward.
func TestMapActionSafetyClassFailsLoudlyOnUndeclared(t *testing.T) {
	if _, err := mapActionSafetyClass(resolume.ActionSafetyClassUndeclared); err == nil {
		t.Fatal("mapActionSafetyClass(ActionSafetyClassUndeclared) returned a nil error — an undeclared safety class must fail loudly")
	}
	if _, err := mapActionSafetyClass(resolume.ActionSafetyClass(99)); err == nil {
		t.Fatal("mapActionSafetyClass(99) returned a nil error — an unrecognized safety class must fail loudly")
	}
}

// --- Actions(): the real registry's safety class and params vocabulary,
// through the real adapter (a nil *resolume.Collector is safe here — A's
// own Actions() never touches its collector field, the identical pattern
// resolume's own TestActionsReturnsASortedCopy already relies on). ---

// TestAdapterActionsMatchesADR024Decision11ExactlyThroughTheRealRegistry is
// this task's own "safety class must survive the translation" requirement,
// proven against A's REAL actionRegistry (internal/coordinator/collector/resolume,
// action.go) — not a hand-maintained duplicate of the spec's table, the
// shape internal/coordinator/api's own standardResolumeActionDescriptors
// (resolumeaction_test.go) necessarily is, since that package does not
// import resolume in production code.
func TestAdapterActionsMatchesADR024Decision11ExactlyThroughTheRealRegistry(t *testing.T) {
	adapter := newResolumeActionDispatcherAdapter(nil)
	descriptors := adapter.Actions()
	if len(descriptors) != 7 {
		t.Fatalf("len(Actions()) = %d, want 7", len(descriptors))
	}

	wantExempt := map[string]bool{"blackout": true, "clearLayer": true}
	seen := map[string]bool{}
	for _, d := range descriptors {
		seen[d.Name] = true
		want := wantExempt[d.Name]
		if d.AuditExempt != want {
			t.Errorf("action %q: AuditExempt = %v, want %v (ADR-024 decision 11: only blackout and clearLayer are exempt)",
				d.Name, d.AuditExempt, want)
		}
		if !d.CoordinatorRequired {
			t.Errorf("action %q: CoordinatorRequired = false, want true (every action in this vocabulary is coordinator-required)", d.Name)
		}
	}
	for _, name := range []string{"launchClip", "clearLayer", "blackout", "launchColumn", "selectDeck", "setLayerBypass", "setLayerMaster"} {
		if !seen[name] {
			t.Errorf("action %q is missing from Actions()", name)
		}
	}
}

// TestAdapterActionsBlackoutHasNoParams and its siblings pin down the
// params vocabulary this file's own resolumeActionParamVocabulary declares
// — the metadata resolumeActionParamVocabulary's own doc comment says A's
// registry has nowhere to carry, so this file supplies it, matching the
// already-shipped, contract-tested wire vocabulary
// (cmd/showmeshctl/cmd_resolume_action.go, api/openapi.yaml) rather than
// inventing a new one.
func TestAdapterActionsParamsVocabularyMatchesTheShippedWireContract(t *testing.T) {
	adapter := newResolumeActionDispatcherAdapter(nil)
	byName := map[string][]api.ResolumeActionParam{}
	for _, d := range adapter.Actions() {
		byName[d.Name] = d.Params
	}

	clipParam := api.ResolumeActionParam{Name: "clip", Kind: api.ResolumeActionParamString, Required: true}
	deckParamOptional := api.ResolumeActionParam{Name: "deck", Kind: api.ResolumeActionParamString, Required: false}
	deckParamRequired := api.ResolumeActionParam{Name: "deck", Kind: api.ResolumeActionParamString, Required: true}
	layerParamOptional := api.ResolumeActionParam{Name: "layer", Kind: api.ResolumeActionParamString, Required: false}
	layerParamRequired := api.ResolumeActionParam{Name: "layer", Kind: api.ResolumeActionParamString, Required: true}
	persistentParam := api.ResolumeActionParam{Name: "persistent", Kind: api.ResolumeActionParamBool, Required: false}
	columnParam := api.ResolumeActionParam{Name: "column", Kind: api.ResolumeActionParamString, Required: true}
	tests := []struct {
		action string
		want   []api.ResolumeActionParam
	}{
		{"launchClip", []api.ResolumeActionParam{clipParam, deckParamOptional, layerParamOptional, persistentParam}},
		{"clearLayer", []api.ResolumeActionParam{layerParamRequired}},
		{"launchColumn", []api.ResolumeActionParam{columnParam, deckParamRequired}},
		{"selectDeck", []api.ResolumeActionParam{deckParamRequired}},
		{"blackout", nil},
		{"setLayerBypass", []api.ResolumeActionParam{layerParamRequired, {Name: "bypassed", Kind: api.ResolumeActionParamBool, Required: true}}},
		{"setLayerMaster", []api.ResolumeActionParam{layerParamRequired, {Name: "master", Kind: api.ResolumeActionParamNumber, Required: true}}},
	}
	for _, tt := range tests {
		got := byName[tt.action]
		if len(got) != len(tt.want) {
			t.Errorf("action %q: params = %+v, want %+v", tt.action, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("action %q: params[%d] = %+v, want %+v", tt.action, i, got[i], tt.want[i])
			}
		}
	}
}

// --- buildResolumeActionParams: the one thing this adapter DOES decide
// (pure boundary translation, see resolumeactionwiring.go's own top
// comment). ---

// buildTestTrackedComposition is this file's own small fixture: one deck,
// one layer, one deck clip on it — just enough for buildResolumeActionParams'
// own tests to resolve a name against, independent of resolumeactionwiring_e2e_test.go's
// larger fixture (that file drives the real HTTP stack; these tests drive
// only the pure translation function).
func buildTestTrackedComposition(t *testing.T) *resolume.TrackedComposition {
	t.Helper()
	comp := &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1001", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "2001", Index: 0, Name: "Layer A"}},
		Clips:  []resolumecomp.Clip{{ID: "3001", DeckID: "1001", LayerIndex: 0, Name: "Snow"}},
	}
	tc, err := resolume.BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}
	return tc
}

// TestBuildResolumeActionParamsSetLayerMasterPassesTheNumberThrough is
// defect 3's own regression test (2026-08-15): setLayerMaster's wire
// "master" is a continuous number, passed straight through to
// resolume.ActionParams.Master unchanged — no bool-to-endpoint translation
// left to test, since none exists anymore. A non-numeric value (a bool
// included) is rejected as a builder-internal-consistency error, mirroring
// every other param-shape mismatch buildResolumeActionParams guards
// against.
func TestBuildResolumeActionParamsSetLayerMasterPassesTheNumberThrough(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	tests := []float64{0.0, 1.0, 0.4, 0.256}
	for _, want := range tests {
		params, refusal, err := buildResolumeActionParams(resolume.ActionSetLayerMaster, map[string]any{"layer": "Layer A", "master": want}, tc)
		if err != nil || refusal != "" {
			t.Fatalf("master=%v: err=%v refusal=%q, want both empty", want, err, refusal)
		}
		if params.Master != want {
			t.Errorf("master=%v: params.Master = %v, want %v", want, params.Master, want)
		}
	}
}

func TestBuildResolumeActionParamsSetLayerMasterRejectsNonNumeric(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	_, _, err := buildResolumeActionParams(resolume.ActionSetLayerMaster, map[string]any{"layer": "Layer A", "master": true}, tc)
	if err == nil {
		t.Fatalf("err = nil, want a non-nil error for a boolean master param")
	}
}

// TestBuildResolumeActionParamsLaunchClipResolvesByNameNoID is this seam's
// own headline property (ADR-037): a launchClip reference resolves to the
// composition's own object id, and nothing in the params map this function
// was given, or the ActionParams it returns, ever carries a raw id as a
// REFERENCE — ClipID is the resolved result, not an echo of caller input.
func TestBuildResolumeActionParamsLaunchClipResolvesByNameNoID(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	params, refusal, err := buildResolumeActionParams(resolume.ActionLaunchClip, map[string]any{"clip": "Snow", "deck": "Main"}, tc)
	if err != nil || refusal != "" {
		t.Fatalf("err=%v refusal=%q, want both empty", err, refusal)
	}
	if params.ClipID != resolume.ObjectID(3001) {
		t.Errorf("params.ClipID = %v, want 3001 (resolved from the composition, not an input id)", params.ClipID)
	}
}

// TestBuildResolumeActionParamsLaunchClipRefusesUnknownName proves a name
// that is not in the stored composition is a REFUSAL (empty err, non-empty
// refusal), never a Go error and never a fabricated id.
func TestBuildResolumeActionParamsLaunchClipRefusesUnknownName(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	params, refusal, err := buildResolumeActionParams(resolume.ActionLaunchClip, map[string]any{"clip": "Nope", "deck": "Main"}, tc)
	if err != nil {
		t.Fatalf("err = %v, want nil (an unresolved name is a refusal, not an error)", err)
	}
	if refusal == "" {
		t.Fatal("refusal is empty, want a reason naming the unresolved clip")
	}
	if params != (resolume.ActionParams{}) {
		t.Errorf("params = %+v, want the zero value on a refusal", params)
	}
}

// TestBuildResolumeActionParamsLaunchClipDeckAndPersistentBothRefuse
// exercises the deck/persistent exclusivity rule through this file's own
// translation layer, rather than only against resolume.ResolveClip
// directly.
func TestBuildResolumeActionParamsLaunchClipDeckAndPersistentBothRefuse(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	_, refusal, err := buildResolumeActionParams(resolume.ActionLaunchClip,
		map[string]any{"clip": "Snow", "deck": "Main", "persistent": true}, tc)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if refusal == "" {
		t.Fatal("refusal is empty, want a reason: deck and persistent must not both be given")
	}
}

func TestBuildResolumeActionParamsLaunchClipNeitherDeckNorPersistentRefuses(t *testing.T) {
	tc := buildTestTrackedComposition(t)
	_, refusal, err := buildResolumeActionParams(resolume.ActionLaunchClip, map[string]any{"clip": "Snow"}, tc)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if refusal == "" {
		t.Fatal("refusal is empty, want a reason: a clip reference must name its deck")
	}
}

// --- sanitizeResolumeActionReason (Review fix 2): a reason string that
// leaked a Resolume URL (and, embedded in its path, a raw object id) must
// never reach the wire; a reason with no URL passes through unchanged. ---

func TestSanitizeResolumeActionReasonRedactsAURLAndTheObjectIDInIt(t *testing.T) {
	// The exact shape a *url.Error's own Error() method produces — this is
	// what ClassifyError's fallthrough (client.go) returns today for a
	// connection reset, since that vocabulary has no entry for it.
	leaked := `dispatching the clip launch failed: Put "http://10.0.0.20:8080/api/v1/parameter/by-id/8391": read tcp 10.0.0.20:54321->10.0.0.20:8080: read: connection reset by peer`

	got := sanitizeResolumeActionReason(resolume.ActionLaunchClip, leaked)

	if strings.Contains(got, "://") {
		t.Errorf("sanitized reason still contains a URL: %q", got)
	}
	if strings.Contains(got, "10.0.0.20") {
		t.Errorf("sanitized reason still contains the Resolume host: %q", got)
	}
	if strings.Contains(got, "8391") {
		t.Errorf("sanitized reason still contains the raw object id: %q", got)
	}
	if !strings.Contains(got, string(resolume.ActionLaunchClip)) {
		t.Errorf("sanitized reason = %q, want it to still name the action", got)
	}
}

func TestSanitizeResolumeActionReasonPassesThroughAnOrdinaryReason(t *testing.T) {
	ordinary := "the layer's own confirming evidence reports active_clip absent"
	if got := sanitizeResolumeActionReason(resolume.ActionClearLayer, ordinary); got != ordinary {
		t.Errorf("sanitizeResolumeActionReason(%q) = %q, want it unchanged (no URL present)", ordinary, got)
	}
}

// TestTranslateActionOutcomeSanitizesTheReason proves the sanitization is
// actually wired into the translation path a real dispatch goes through
// (translateActionOutcome), not merely available as an unused function.
func TestTranslateActionOutcomeSanitizesTheReason(t *testing.T) {
	adapter := &resolumeActionDispatcherAdapter{now: time.Now}
	leaked := `dispatching the master change failed: Put "http://resolume.local:8080/api/v1/parameter/by-id/42": connection reset by peer`

	result, err := adapter.translateActionOutcome(resolume.ActionOutcome{
		Action: resolume.ActionSetLayerMaster,
		State:  resolume.ActionFailed,
		Reason: leaked,
	})
	if err != nil {
		t.Fatalf("translateActionOutcome: %v", err)
	}
	if strings.Contains(result.Reason, "://") || strings.Contains(result.Reason, "resolume.local") {
		t.Errorf("translateActionOutcome's own Reason still carries a URL/host: %q", result.Reason)
	}
}

func TestBuildResolumeActionParamsBlackoutTakesNoID(t *testing.T) {
	params, refusal, err := buildResolumeActionParams(resolume.ActionBlackout, map[string]any{}, nil)
	if err != nil || refusal != "" {
		t.Fatalf("err=%v refusal=%q, want both empty", err, refusal)
	}
	if params != (resolume.ActionParams{}) {
		t.Errorf("params = %+v, want the zero value", params)
	}
}
